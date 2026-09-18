package v1

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

// queryCounter counts round trips, so a test can say "one extra query per
// page" as an assertion rather than a promise in a comment.
type queryCounter struct{ n atomic.Int64 }

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// countingPool is equityTestPool with every query counted.
func countingPool(t *testing.T) (*pgxpool.Pool, *queryCounter) {
	t.Helper()
	equityTestPool(t).Close() // migrations and the ent stand-ins, once
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://tapp:tapp@localhost:5433/tapp?sslmode=disable"
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, counter
}

// tapAt charges the cardholder at a merchant, the way the tap service does:
// the movement and the card_taps row in one transaction.
func tapAt(t *testing.T, pool *pgxpool.Pool, cardholder, merchant uuid.UUID, amount money.Amount) uuid.UUID {
	t.Helper()
	tapID := uuid.New()
	err := movements.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		ledgerTx, err := movements.Tap(context.Background(), tx, cardholder, merchant, amount, money.Zero(amount.Currency()), tapID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(context.Background(), `
			INSERT INTO card_taps (id, card_id, cardholder_id, merchant_id, currency, amount_minor, fee_minor, tier, ledger_tx_id)
			VALUES ($1, $2, $3, $4, $5::currency, $6, 0, 'none', $7)`,
			tapID, uuid.New(), cardholder, merchant, string(amount.Currency()), amount.Minor(), ledgerTx)
		return err
	})
	if err != nil {
		t.Fatalf("tap: %v", err)
	}
	return tapID
}

func listBusiness(t *testing.T, pool *pgxpool.Pool, sender uuid.UUID, trading, symbol string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO merchant_businesses (sender_id, legal_name, trading_name, rc_number, mcc, symbol, state, reference_price_minor)
		VALUES ($1, 'Mama Put Kitchens Ltd', $2, 'rc1', '5812', $3, 'listed', 4000)`, sender, trading, symbol); err != nil {
		t.Fatal(err)
	}
}

type merchantJSON struct {
	Ref    string  `json:"ref"`
	Name   string  `json:"name"`
	Symbol *string `json:"symbol"`
}

func TestActivityNamesTheMerchantOfEachTap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool, counter := countingPool(t)
	ctx := context.Background()

	cardholder := uuid.New()
	if _, err := movements.Deposit(ctx, pool, cardholder, money.New(1_000_000, money.NGN), "test", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	listed, _ := newMerchant(t, pool) // has a business: "Mama Put"
	listBusiness(t, pool, listed, "Mama Put", "MAMAPUT")
	unlisted, _ := newMerchant(t, pool) // no business: the person, Mama Put by name

	tap1 := tapAt(t, pool, cardholder, listed, money.New(10_000, money.NGN))
	tap2 := tapAt(t, pool, cardholder, unlisted, money.New(5_000, money.NGN))

	h := &BalanceHandler{DB: pool, User: func(*gin.Context) (uuid.UUID, bool) { return cardholder, true }}
	router := gin.New()
	router.GET("/activity", h.Activity)

	counter.n.Store(0)
	code, env := call(t, router, "GET", "/activity", "")
	if code != 200 {
		t.Fatalf("activity = %d %s", code, env.Message)
	}
	// The page itself and one query naming every tap on it. Not one per row.
	if got := counter.n.Load(); got != 2 {
		t.Errorf("activity page took %d queries, want 2 (history + merchants)", got)
	}

	var page struct {
		Movements []struct {
			Reason   string        `json:"reason"`
			RefType  string        `json:"refType"`
			RefID    *uuid.UUID    `json:"refId"`
			Merchant *merchantJSON `json:"merchant"`
		} `json:"movements"`
	}
	if err := json.Unmarshal(env.Data, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Movements) != 3 {
		t.Fatalf("movements = %s", env.Data)
	}
	// Newest first: the unlisted tap, the listed tap, the deposit.
	m := page.Movements[0]
	if m.RefType != "tap" || m.RefID == nil || *m.RefID != tap2 || m.Merchant == nil ||
		m.Merchant.Ref != unlisted.String() || m.Merchant.Name != "Mama Put" || m.Merchant.Symbol != nil {
		t.Errorf("tap at an unlisted merchant = %s", env.Data)
	}
	m = page.Movements[1]
	if m.RefType != "tap" || m.RefID == nil || *m.RefID != tap1 || m.Merchant == nil ||
		m.Merchant.Ref != listed.String() || m.Merchant.Name != "Mama Put" ||
		m.Merchant.Symbol == nil || *m.Merchant.Symbol != "MAMAPUT" {
		t.Errorf("tap at a listed merchant = %s", env.Data)
	}
	if d := page.Movements[2]; d.Reason != "deposit.credited" || d.Merchant != nil {
		t.Errorf("deposit should carry no merchant: %s", env.Data)
	}
	// The field is present, as null, on every non-tap movement: a client
	// switching on it sees the key rather than guessing at its absence.
	var raw struct {
		Movements []map[string]json.RawMessage `json:"movements"`
	}
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		t.Fatal(err)
	}
	if v, ok := raw.Movements[2]["merchant"]; !ok || string(v) != "null" {
		t.Errorf("deposit merchant = %s, want null", v)
	}
}

func TestEquityActivityNamesTheMerchantFreedomCouldNot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := equityTestPool(t)
	user := uuid.New()

	listed, _ := newMerchant(t, pool)
	listBusiness(t, pool, listed, "Mama Put", "MAMAPUT")
	unlisted, _ := newMerchant(t, pool)
	stranger := uuid.New() // a ref the platform has never seen

	r := newFakeRail()
	r.on("GET", "/v1/rail/cardholders/"+user.String()+"/activity", 200, `[
		{"tap_ref":"tap-1","merchant_ref":"`+listed.String()+`","merchant_name":"Mama Put (Freedom)","symbol":"MAMAPUT",
		 "tap_amount_kobo":1000000,"funding_kobo":500,"state":"allocated","units":12500000,"price_kobo":4000,"at":"2026-09-18T11:24:03Z"},
		{"tap_ref":"tap-2","merchant_ref":"`+listed.String()+`","merchant_name":"`+listed.String()+`","symbol":null,
		 "tap_amount_kobo":500000,"funding_kobo":250,"state":"escrowed","units":0,"price_kobo":0,"at":"2026-09-18T12:00:00Z"},
		{"tap_ref":"tap-3","merchant_ref":"`+unlisted.String()+`","merchant_name":"","symbol":null,
		 "tap_amount_kobo":200000,"funding_kobo":100,"state":"escrowed","units":0,"price_kobo":0,"at":"2026-09-18T12:30:00Z"},
		{"tap_ref":"tap-4","merchant_ref":"`+stranger.String()+`","merchant_name":"","symbol":null,
		 "tap_amount_kobo":100,"funding_kobo":1,"state":"escrowed","units":0,"price_kobo":0,"at":"2026-09-18T12:45:00Z"}]`)

	h := &HoldingsHandler{Client: r.serve(t), DB: pool, User: func(*gin.Context) (uuid.UUID, bool) { return user, true }}
	router := gin.New()
	router.GET("/equity-activity", h.Activity)

	code, env := call(t, router, "GET", "/equity-activity", "")
	if code != 200 {
		t.Fatalf("activity = %d %s", code, env.Message)
	}
	var act struct {
		Activity []struct {
			TapID     string       `json:"tap_id"`
			Merchant  merchantJSON `json:"merchant"`
			TapAmount struct {
				Minor   int64  `json:"minor"`
				Display string `json:"display"`
			} `json:"tap_amount"`
			Funding struct {
				Minor int64 `json:"minor"`
			} `json:"funding"`
		} `json:"activity"`
	}
	if err := json.Unmarshal(env.Data, &act); err != nil {
		t.Fatal(err)
	}
	if len(act.Activity) != 4 {
		t.Fatalf("activity = %s", env.Data)
	}
	// Freedom's name is used when it has one.
	if a := act.Activity[0]; a.Merchant.Ref != listed.String() || a.Merchant.Name != "Mama Put (Freedom)" ||
		a.Merchant.Symbol == nil || *a.Merchant.Symbol != "MAMAPUT" ||
		a.TapAmount.Minor != 1_000_000 || a.TapAmount.Display != "₦10,000.00" || a.Funding.Minor != 500 {
		t.Errorf("named by Freedom = %+v", a)
	}
	// A bare ref is not a name: the business's trading name stands in.
	if a := act.Activity[1]; a.Merchant.Name != "Mama Put" || a.Merchant.Symbol != nil || a.TapAmount.Minor != 500_000 {
		t.Errorf("placeholder merchant = %+v", a)
	}
	// No business: the person behind the profile.
	if a := act.Activity[2]; a.Merchant.Ref != unlisted.String() || a.Merchant.Name != "Mama Put" {
		t.Errorf("unlisted merchant = %+v", a)
	}
	// Nobody knows this one; the ref is all there is, and the name stays empty
	// rather than lying.
	if a := act.Activity[3]; a.Merchant.Ref != stranger.String() || a.Merchant.Name != "" {
		t.Errorf("stranger = %+v", a)
	}
}
