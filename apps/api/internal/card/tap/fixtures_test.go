package tap

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/card/auth"
	"github.com/usezoracle/tapp/api/internal/card/token"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
)

// These run against a real Postgres. The guarantees under test are the atomic
// nonce claim, the row lock, the unique constraints and the balance trigger --
// all of which live in the database. Against a fake they would all pass and
// none of them would mean anything.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://tapp:tapp@localhost:5433/tapp?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no test database (%v); start it with `docker compose up -d postgres`", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database (%v); start it with `docker compose up -d postgres`", err)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	ensureEntTables(t, pool)
	t.Cleanup(pool.Close)
	return pool
}

// The card and nonce tables belong to ent, which builds them from its own
// schema at boot. The tests need them present without pulling the whole ent
// runtime in, so the columns this package actually reads are created here.
// Any drift between this and the ent schema shows up as a failing test, which
// is the right place for it to show up.
func ensureEntTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS users (
			id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			first_name text NOT NULL DEFAULT '', last_name text NOT NULL DEFAULT '',
			email text UNIQUE NOT NULL, password text NOT NULL DEFAULT '',
			scope text NOT NULL DEFAULT 'sender');

		CREATE TABLE IF NOT EXISTS sender_profiles (
			id uuid PRIMARY KEY, updated_at timestamptz NOT NULL DEFAULT now(),
			domain_whitelist jsonb NOT NULL DEFAULT '[]'::jsonb,
			user_sender_profile uuid UNIQUE NOT NULL REFERENCES users(id));

		CREATE TABLE IF NOT EXISTS tapp_cards (
			id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			activation_token text UNIQUE NOT NULL,
			status text NOT NULL DEFAULT 'issued',
			card_uid_hash bytea UNIQUE,
			linking_proof bytea, pin_verifier bytea,
			pin_attempts_remaining int NOT NULL DEFAULT 5,
			locked_until timestamptz, card_password bytea,
			current_token_ciphertext bytea, token_rotated_at timestamptz,
			pending_token_ciphertext bytea, pending_token_issued_at timestamptz,
			token_mismatch_count int NOT NULL DEFAULT 0,
			daily_limit_subunit bigint NOT NULL DEFAULT 0,
			per_tap_limit_subunit bigint NOT NULL DEFAULT 0,
			step_up_threshold_subunit bigint NOT NULL DEFAULT 0,
			spent_today_subunit bigint NOT NULL DEFAULT 0,
			day_index bigint NOT NULL DEFAULT 0,
			needs_resync boolean NOT NULL DEFAULT false,
			cap_object_id text, coin_type text,
			user_tapp_cards uuid REFERENCES users(id));

		CREATE TABLE IF NOT EXISTS card_server_nonces (
			id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			nonce bytea UNIQUE NOT NULL, tier text NOT NULL,
			amount text NOT NULL, currency text NOT NULL DEFAULT 'NGN',
			expires_at timestamptz NOT NULL, consumed_at timestamptz,
			step_up_granted_at timestamptz,
			sender_profile_card_server_nonces uuid NOT NULL REFERENCES sender_profiles(id),
			tapp_card_server_nonces uuid NOT NULL REFERENCES tapp_cards(id));`)
	if err != nil {
		t.Fatalf("create ent-owned tables: %v", err)
	}
}

// fixture is one cardholder with one live card, and one merchant.
type fixture struct {
	Pool       *pgxpool.Pool
	Svc        *Service
	Cardholder uuid.UUID
	Merchant   uuid.UUID
	CardID     uuid.UUID
	UIDHash    []byte
	Token      []byte
	Anchor     []byte
	PIN        string
	Limits     auth.Limits

	// elapsed is how far the fixture's clock has run ahead of real time.
	// Every tap steps it past RepeatWindow, the way seconds pass at a till
	// between one customer and the next; a test that wants two taps inside
	// the window sets Svc.Now itself.
	elapsed time.Duration
}

// later moves the fixture's clock past the repeat window.
func (f *fixture) later() {
	f.elapsed += RepeatWindow + time.Second
}

const testPIN = "1379"

func newFixture(t *testing.T, funded money.Amount) *fixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()

	cardholder, merchantUser, merchant := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{cardholder, merchantUser} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO users
			(id, created_at, updated_at, first_name, last_name, email, password, scope)
		 VALUES ($1, now(), now(), 'Test', 'User', $2, '', 'user')`,
			id, id.String()+"@test.local"); err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO sender_profiles
			(id, updated_at, domain_whitelist, user_sender_profile)
		 VALUES ($1, now(), '{}', $2)`,
		merchant, merchantUser); err != nil {
		t.Fatalf("create merchant: %v", err)
	}

	uidHash := make([]byte, 32)
	copy(uidHash, cardholder[:])
	tok, err := token.New()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	k := []byte("32-bytes-of-secret-living-on-crd")
	anchor, _ := cardholderAnswers(k, testPIN, make([]byte, auth.NonceLen))

	limits := auth.Limits{
		PerTap: money.Naira(2_000),
		StepUp: money.Naira(15_000),
		Daily:  money.Naira(40_000),
	}
	cardID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO tapp_cards
			(id, created_at, updated_at, activation_token, status, card_uid_hash,
			 current_token_ciphertext, linking_proof, daily_limit_subunit,
			 per_tap_limit_subunit, step_up_threshold_subunit, user_tapp_cards)
		VALUES ($1, now(), now(), $2, 'live', $3, $4, $5, $6, $7, $8, $9)`,
		cardID, uuid.NewString(), uidHash, tok, anchor,
		limits.Daily.Minor(), limits.PerTap.Minor(), limits.StepUp.Minor(), cardholder); err != nil {
		t.Fatalf("create card: %v", err)
	}

	if funded.IsPositive() {
		if _, err := movements.Deposit(ctx, pool, cardholder, funded, "test", uuid.NewString()); err != nil {
			t.Fatalf("fund cardholder: %v", err)
		}
	}

	f := &fixture{
		Pool: pool, Cardholder: cardholder, Merchant: merchant, CardID: cardID,
		UIDHash: uidHash, Token: tok, Anchor: anchor, PIN: testPIN, Limits: limits,
		Svc: &Service{Pool: pool, Fee: BasisPointFee(50)},
	}
	f.Svc.Now = func() time.Time { return time.Now().Add(f.elapsed) }
	return f
}

// cardholderAnswers is the client half of the PIN protocol.
func cardholderAnswers(k []byte, pin string, nonce []byte) (anchor, response []byte) {
	a := auth.Anchor(k, pin)
	return a, auth.Respond(a, nonce)
}

func (f *fixture) cardStatus(t *testing.T) (status string, attempts, mismatches int, lockedUntil *time.Time) {
	t.Helper()
	err := f.Pool.QueryRow(context.Background(),
		`SELECT status, pin_attempts_remaining, token_mismatch_count, locked_until
		   FROM tapp_cards WHERE id = $1`, f.CardID).
		Scan(&status, &attempts, &mismatches, &lockedUntil)
	if err != nil {
		t.Fatalf("read card: %v", err)
	}
	return
}

func (f *fixture) tokens(t *testing.T) (current, pending []byte) {
	t.Helper()
	if err := f.Pool.QueryRow(context.Background(),
		`SELECT current_token_ciphertext, pending_token_ciphertext FROM tapp_cards WHERE id = $1`,
		f.CardID).Scan(&current, &pending); err != nil {
		t.Fatalf("read tokens: %v", err)
	}
	return
}
