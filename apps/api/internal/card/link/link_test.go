package link

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/card/auth"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
)

func testService(t *testing.T) *Service {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://tapp:tapp@localhost:5433/tapp?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no test database (%v)", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database (%v)", err)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	ensureCardTables(t, pool)
	t.Cleanup(pool.Close)

	return &Service{
		Pool: pool,
		// Verified to ₦40,000 a day.
		MaxDailyMinor: func(context.Context, uuid.UUID) (int64, error) { return 4_000_000, nil },
	}
}

// The card table belongs to ent, which builds it at boot. The columns this
// package touches are created here so the tests do not need the ent runtime;
// drift between the two shows up as a failing test, which is where it should.
func ensureCardTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS users (
			id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			first_name text NOT NULL DEFAULT '', last_name text NOT NULL DEFAULT '',
			email text UNIQUE NOT NULL, password text NOT NULL DEFAULT '',
			scope text NOT NULL DEFAULT 'sender');
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
			user_tapp_cards uuid REFERENCES users(id));`)
	if err != nil {
		t.Fatalf("create card tables: %v", err)
	}
}

func issueCard(t *testing.T, s *Service) string {
	t.Helper()
	activation := uuid.NewString()
	if _, err := s.Pool.Exec(context.Background(),
		`INSERT INTO tapp_cards (id, created_at, updated_at, activation_token)
		 VALUES (gen_random_uuid(), now(), now(), $1)`,
		activation); err != nil {
		t.Fatalf("issue card: %v", err)
	}
	return activation
}

func newUser(t *testing.T, s *Service) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := s.Pool.Exec(context.Background(),
		`INSERT INTO users
			(id, created_at, updated_at, first_name, last_name, email, password, scope)
		 VALUES ($1, now(), now(), 'Test', 'User', $2, '', 'user')`,
		id, id.String()+"@test.local"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func provisioning(t *testing.T) Provisioning {
	t.Helper()
	return Provisioning{
		Anchor: auth.Anchor([]byte("32-bytes-of-secret-living-on-crd"), "1379"),
		Limits: Limits{PerTapMinor: 200_000, StepUpMinor: 1_500_000, DailyMinor: 4_000_000},
	}
}

// activation carries the UID the client read back off the chip.
func activation(t *testing.T, readBack []byte) Activation {
	t.Helper()
	uid := make([]byte, 32)
	if _, err := rand.Read(uid); err != nil {
		t.Fatal(err)
	}
	return Activation{UIDHash: uid, ReadBack: readBack}
}

// The whole ceremony, and then the card is live.
func TestLinkingACardEndToEnd(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user := newUser(t, s)

	session, err := s.Start(ctx, issueCard(t, s), user)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if session.State != StateStarted {
		t.Fatalf("state = %s, want started", session.State)
	}

	session, err = s.Provision(ctx, session.ID, user, provisioning(t))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if session.State != StateProvisioned || session.WriteToken == "" {
		t.Fatalf("state = %s, token = %q", session.State, session.WriteToken)
	}

	written, _ := hex.DecodeString(session.WriteToken)
	session, err = s.Activate(ctx, session.ID, user, activation(t, written))
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if session.State != StateActivated {
		t.Fatalf("state = %s, want activated", session.State)
	}

	var status string
	var current []byte
	if err := s.Pool.QueryRow(ctx,
		`SELECT status, current_token_ciphertext FROM tapp_cards WHERE id = $1`,
		session.CardID).Scan(&status, &current); err != nil {
		t.Fatalf("read card: %v", err)
	}
	if status != "live" {
		t.Errorf("card status = %q, want live", status)
	}
	if hex.EncodeToString(current) != hex.EncodeToString(written) {
		t.Error("the card's token is not what was written to it")
	}
}

// The reason the session exists: a client that loses its connection can find
// its place instead of repeating a ceremony that generates a secret, writes it
// to a chip, and commits a PIN proof.
func TestAnInterruptedCeremonyResumes(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user := newUser(t, s)

	session, err := s.Start(ctx, issueCard(t, s), user)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	provisioned, err := s.Provision(ctx, session.ID, user, provisioning(t))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// The client drops here and comes back asking where it got to.
	resumed, err := s.Get(ctx, session.ID, user)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resumed.State != StateProvisioned {
		t.Fatalf("state = %s, want provisioned", resumed.State)
	}
	// Crucially the SAME token: two different tokens written to one chip is
	// how a card ends up out of sync before it has ever been used.
	if resumed.WriteToken != provisioned.WriteToken {
		t.Fatal("resuming issued a different write token")
	}

	written, _ := hex.DecodeString(resumed.WriteToken)
	if _, err := s.Activate(ctx, session.ID, user, activation(t, written)); err != nil {
		t.Fatalf("Activate after resuming: %v", err)
	}
}

// Provisioning twice must not issue a second token.
func TestProvisioningIsIdempotent(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user := newUser(t, s)

	session, _ := s.Start(ctx, issueCard(t, s), user)
	p := provisioning(t)

	first, err := s.Provision(ctx, session.ID, user, p)
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	second, err := s.Provision(ctx, session.ID, user, p)
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	if first.WriteToken != second.WriteToken {
		t.Fatal("provisioning twice issued two different tokens")
	}
}

// Starting twice returns the same session, not a second ceremony racing the
// first to write a different secret to one chip.
func TestStartingTwiceResumesRatherThanRacing(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user := newUser(t, s)
	activation := issueCard(t, s)

	first, err := s.Start(ctx, activation, user)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	second, err := s.Start(ctx, activation, user)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("two sessions opened on one card: %s and %s", first.ID, second.ID)
	}
}

// An NFC write that reports success and did not land is common enough that
// trusting it would leave a fraction of cards permanently unusable. Saying so
// now is better than at a checkout counter.
func TestActivationRequiresTheCardToActuallyHoldTheToken(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user := newUser(t, s)

	session, _ := s.Start(ctx, issueCard(t, s), user)
	session, err := s.Provision(ctx, session.ID, user, provisioning(t))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	wrong := make([]byte, 32)
	if _, err := rand.Read(wrong); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activate(ctx, session.ID, user, activation(t, wrong)); !errors.Is(err, ErrWrongState) {
		t.Fatalf("activation with the wrong read-back returned %v", err)
	}

	var status string
	if err := s.Pool.QueryRow(ctx, `SELECT status FROM tapp_cards WHERE id = $1`,
		session.CardID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "live" {
		t.Fatal("a card went live without holding the token")
	}
}

// Somebody holding a card that is not theirs should be told that, not left
// retrying against an opaque failure.
func TestACardClaimedBySomebodyElseSaysSo(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	activation := issueCard(t, s)

	owner, stranger := newUser(t, s), newUser(t, s)
	if _, err := s.Start(ctx, activation, owner); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := s.Start(ctx, activation, stranger); !errors.Is(err, ErrCardTaken) {
		t.Fatalf("got %v, want ErrCardTaken", err)
	}
}

func TestAnUnknownCardIsRefused(t *testing.T) {
	s := testService(t)
	if _, err := s.Start(context.Background(), "not-a-real-token", newUser(t, s)); !errors.Is(err, ErrCardUnknown) {
		t.Fatalf("got %v, want ErrCardUnknown", err)
	}
}

// One physical chip, one card record. This was merely indexed before, so two
// rows could claim one card and every tap of it then failed as "not
// recognised" -- a conflict nobody could diagnose from the symptom.
func TestOnePhysicalChipCannotBackTwoCards(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user := newUser(t, s)

	uid := make([]byte, 32)
	if _, err := rand.Read(uid); err != nil {
		t.Fatal(err)
	}

	first, _ := s.Start(ctx, issueCard(t, s), user)
	first, err := s.Provision(ctx, first.ID, user, provisioning(t))
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	written, _ := hex.DecodeString(first.WriteToken)
	if _, err := s.Activate(ctx, first.ID, user, Activation{UIDHash: uid, ReadBack: written}); err != nil {
		t.Fatalf("first Activate: %v", err)
	}

	// A second card record presenting the same physical chip.
	second, _ := s.Start(ctx, issueCard(t, s), user)
	second, err = s.Provision(ctx, second.ID, user, provisioning(t))
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	written2, _ := hex.DecodeString(second.WriteToken)
	if _, err := s.Activate(ctx, second.ID, user, Activation{UIDHash: uid, ReadBack: written2}); !errors.Is(err, ErrUIDTaken) {
		t.Fatalf("got %v, want ErrUIDTaken", err)
	}
}

// Limits are chosen at linking, never defaulted. The predecessor fell back to
// package constants, so a card that never finished linking silently acquired a
// daily allowance nobody agreed to.
func TestLimitsMustBeChosenAndCoherent(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user := newUser(t, s)

	for name, limits := range map[string]Limits{
		"unset":                 {},
		"per-tap above step-up": {PerTapMinor: 2_000_000, StepUpMinor: 1_500_000, DailyMinor: 4_000_000},
		"step-up above daily":   {PerTapMinor: 200_000, StepUpMinor: 5_000_000, DailyMinor: 4_000_000},
		"beyond verification":   {PerTapMinor: 200_000, StepUpMinor: 1_500_000, DailyMinor: 900_000_000},
	} {
		t.Run(name, func(t *testing.T) {
			session, _ := s.Start(ctx, issueCard(t, s), user)
			p := provisioning(t)
			p.Limits = limits
			if _, err := s.Provision(ctx, session.ID, user, p); err == nil {
				t.Fatalf("%s limits were accepted", name)
			}
		})
	}
}

// A session nobody finished releases its card rather than holding it forever.
func TestAnAbandonedSessionExpires(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user := newUser(t, s)

	session, err := s.Start(ctx, issueCard(t, s), user)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := s.Pool.Exec(ctx,
		`UPDATE card_link_sessions SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		session.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Expire(ctx); err != nil {
		t.Fatalf("Expire: %v", err)
	}

	var state string
	if err := s.Pool.QueryRow(ctx, `SELECT state FROM card_link_sessions WHERE id = $1`,
		session.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(StateAbandoned) {
		t.Errorf("state = %q, want abandoned", state)
	}
}

// A session belongs to one user; nobody else may drive it.
func TestOnlyTheOwnerMayDriveASession(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	user, stranger := newUser(t, s), newUser(t, s)

	session, _ := s.Start(ctx, issueCard(t, s), user)
	if _, err := s.Provision(ctx, session.ID, stranger, provisioning(t)); !errors.Is(err, ErrSessionUnknown) {
		t.Fatalf("a stranger provisioned somebody else's session: %v", err)
	}
	if _, err := s.Get(ctx, session.ID, stranger); !errors.Is(err, ErrSessionUnknown) {
		t.Fatalf("a stranger read somebody else's session: %v", err)
	}
}
