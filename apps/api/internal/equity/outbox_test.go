package equity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
)

// Against a real Postgres: what is under test is the queue itself -- that a
// row is written in the caller's transaction, that a reversal waits for its
// tap, that the unique index holds -- and none of that means anything
// against a fake.
func testPool(t *testing.T) *pgxpool.Pool {
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
	// users is ent's; the columns the display name is read from are enough.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			first_name text NOT NULL DEFAULT '', last_name text NOT NULL DEFAULT '',
			email text UNIQUE NOT NULL, password text NOT NULL DEFAULT '',
			scope text NOT NULL DEFAULT 'sender')`); err != nil {
		pool.Close()
		t.Fatalf("ent tables: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// freedom is a stub of the rail API that answers however the test says.
//
// Calls are counted per tap, because the outbox table is shared with every
// other test that writes it (in this package and in tap's), and a worker
// tick delivers whatever is due, not only this test's rows.
type freedom struct {
	t     *testing.T
	mu    sync.Mutex
	calls map[string]int
	// status and body for the next answers; a body of "" echoes a
	// plausible success for the path.
	status int
	body   string
	last   struct {
		method, path string
		body         map[string]any
	}
	// hold, when set, makes a tap delivery wait to be released: entered is
	// closed when the stub is inside the call, and the answer is not sent
	// until hold is closed. For observing a delivery in flight.
	hold, entered chan struct{}
}

func (f *freedom) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.hold != nil && r.URL.Path == "/v1/rail/taps" {
			close(f.entered)
			<-f.hold
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.last.method, f.last.path = r.Method, r.URL.Path
		f.last.body = nil
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&f.last.body)
		}
		ref, _ := f.last.body["tap_ref"].(string)
		if ref == "" {
			// .../taps/{tap_ref}/reverse
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) >= 2 {
				ref = parts[len(parts)-2]
			}
		}
		f.calls[ref]++
		status := f.status
		if status == 0 {
			status = http.StatusCreated
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		body := f.body
		if body == "" {
			switch {
			case strings.HasSuffix(r.URL.Path, "/reverse"):
				body = `{"state":"reversed","unwound_units":12500000}`
			default:
				body = `{"tap_ref":"` + ref + `","presentment_id":"p1",` +
					`"fee_kobo":5000,"buyback_funding_kobo":500,"intent_id":"i1",` +
					`"intent_state":"allocated","allocated_units":12500000,"price_kobo":4000,` +
					`"symbol":"MAMAPUT","lock_until":"2027-01-16"}`
			}
		}
		_, _ = w.Write([]byte(body))
	})
}

// callsFor is how many times the stub was asked about one tap.
func (f *freedom) callsFor(tapID uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[tapID.String()]
}

func stub(t *testing.T) (*freedom, *Client) {
	t.Helper()
	f := &freedom{t: t, calls: map[string]int{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, New(srv.URL, "test-token")
}

func enqueue(t *testing.T, pool *pgxpool.Pool, e TapEvent) {
	t.Helper()
	err := movements.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		return EnqueueTap(context.Background(), tx, e)
	})
	if err != nil {
		t.Fatalf("EnqueueTap: %v", err)
	}
}

// charge is a naira tap the way the tap package writes one: the ledger
// movement, the card_taps row and the outbox row in one transaction. It is
// what release has to look at, so the queue tests need the real thing.
func charge(t *testing.T, pool *pgxpool.Pool, cardholder uuid.UUID, amount money.Amount) (tapID, merchant uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tapID, merchant = uuid.New(), uuid.New()
	fee := money.FeeFor(amount, 50)
	if _, err := movements.Deposit(ctx, pool, cardholder, amount, "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	err := movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		ledgerTx, err := movements.Tap(ctx, tx, cardholder, merchant, amount, fee, tapID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO card_taps (id, card_id, cardholder_id, merchant_id, currency,
			                       amount_minor, fee_minor, tier, funded_usdc_minor, ledger_tx_id, nonce)
			VALUES ($1, $2, $3, $4, 'NGN', $5, $6, 'none', $5, $7, $8)`,
			tapID, uuid.New(), cardholder, merchant, amount.Minor(), fee.Minor(), ledgerTx, uuid.NewString()); err != nil {
			return err
		}
		return EnqueueTap(ctx, tx, TapEvent{TapID: tapID, Cardholder: cardholder, Merchant: merchant,
			Amount: amount, At: time.Now()})
	})
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	return tapID, merchant
}

// usdcLeg puts the tap's on-chain leg in a state, as the offramp settler
// would, WITHOUT calling ReleaseIfSettled: what the tests then observe is
// the release, not the settler.
func usdcLeg(t *testing.T, pool *pgxpool.Pool, tapID uuid.UUID, state string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO card_tap_settlements (tap_id, from_address, sell_micro, state)
		VALUES ($1, '0xfrom', 1, $2)
		ON CONFLICT (tap_id) DO UPDATE SET state = EXCLUDED.state, updated_at = now()`, tapID, state); err != nil {
		t.Fatalf("usdc leg: %v", err)
	}
}

// ngnLeg is the same for the naira leg.
func ngnLeg(t *testing.T, pool *pgxpool.Pool, tapID uuid.UUID, state string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO card_tap_ngn_settlements (tap_id, cardholder_id, merchant_id, source_customer_id,
		        source_account_number, currency, amount_minor, bank_code, account_number, account_name,
		        reference, state, settled_at)
		SELECT id, cardholder_id, merchant_id, 'c', 'a', 'NGN', 1, 'B', '1', 'N', $1::text, $2,
		       CASE WHEN $2 = 'settled' THEN now() END
		  FROM card_taps WHERE id = $3
		ON CONFLICT (tap_id) DO UPDATE SET state = EXCLUDED.state, settled_at = EXCLUDED.settled_at, updated_at = now()`,
		tapID.String(), state, tapID); err != nil {
		t.Fatalf("ngn leg: %v", err)
	}
}

// reverse records a reversal as the tap package does, in its own transaction.
func reverse(t *testing.T, pool *pgxpool.Pool, tapID uuid.UUID, reason string) {
	t.Helper()
	err := movements.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		return RecordReversal(context.Background(), tx, tapID, reason)
	})
	if err != nil {
		t.Fatalf("RecordReversal: %v", err)
	}
}

// rowCount is how many outbox rows a tap has of a kind.
func rowCount(t *testing.T, pool *pgxpool.Pool, tapID uuid.UUID, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM equity_outbox WHERE tap_id = $1 AND kind = $2`, tapID, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type outboxRow struct {
	State       string
	Attempts    int
	LastError   *string
	NextAt      time.Time
	DeliveredAt *time.Time
	Response    []byte
}

func read(t *testing.T, pool *pgxpool.Pool, tapID uuid.UUID, kind string) outboxRow {
	t.Helper()
	var r outboxRow
	err := pool.QueryRow(context.Background(), `
		SELECT state, attempts, last_error, next_at, delivered_at, response
		  FROM equity_outbox WHERE tap_id = $1 AND kind = $2`, tapID, kind).
		Scan(&r.State, &r.Attempts, &r.LastError, &r.NextAt, &r.DeliveredAt, &r.Response)
	if err != nil {
		t.Fatalf("read outbox %s/%s: %v", tapID, kind, err)
	}
	return r
}

func newUser(t *testing.T, pool *pgxpool.Pool, first, last string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO users (id, first_name, last_name, email) VALUES ($1, $2, $3, $4)`,
		id, first, last, id.String()+"@test.local"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func TestATapIsQueuedWithTheRailRequestAsItsPayload(t *testing.T) {
	pool := testPool(t)
	cardholder := newUser(t, pool, "Ada", "Okafor")
	tapID, merchant := uuid.New(), uuid.New()
	at := time.Date(2026, 9, 18, 11, 24, 3, 0, time.UTC)

	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: cardholder, Merchant: merchant,
		Amount: money.Naira(10_000), At: at})

	var payload TapRequest
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT payload FROM equity_outbox WHERE tap_id = $1 AND kind = 'tap'`, tapID).Scan(&raw); err != nil {
		t.Fatalf("no outbox row: %v", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	want := TapRequest{
		TapRef: tapID.String(), MerchantRef: merchant.String(), CardholderRef: cardholder.String(),
		CardholderDisplayName: "Ada Okafor", AmountKobo: 1_000_000, ChargedAt: "2026-09-18T11:24:03Z",
	}
	if payload != want {
		t.Errorf("payload = %+v, want %+v", payload, want)
	}
	if r := read(t, pool, tapID, KindTap); r.State != StateHeld || r.Attempts != 0 {
		t.Errorf("row = %+v, want held with no attempts", r)
	}

	// A second row for the same tap cannot exist.
	err := movements.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		return EnqueueTap(context.Background(), tx, TapEvent{TapID: tapID, Cardholder: cardholder,
			Merchant: merchant, Amount: money.Naira(1), At: at})
	})
	if err == nil {
		t.Error("the same tap was queued twice")
	}
}

func TestANonNairaTapIsNotQueued(t *testing.T) {
	pool := testPool(t)
	tapID := uuid.New()
	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: newUser(t, pool, "A", "B"), Merchant: uuid.New(),
		Amount: money.Dollars(10), At: time.Now()})

	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM equity_outbox WHERE tap_id = $1`, tapID).Scan(&n)
	if n != 0 {
		t.Errorf("a USD tap was queued for an NGN market")
	}
}

func TestTheWorkerDeliversAndKeepsTheAnswer(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	tapID, _ := charge(t, pool, newUser(t, pool, "Ada", ""), money.Naira(10_000))
	usdcLeg(t, pool, tapID, "fulfilled")

	w := &Worker{Pool: pool, Client: client}
	delivered, failed, err := w.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if delivered < 1 || failed != 0 {
		t.Errorf("delivered %d, failed %d; want at least 1 and 0", delivered, failed)
	}
	if f.callsFor(tapID) != 1 {
		t.Errorf("tap was sent %d times, want once", f.callsFor(tapID))
	}

	r := read(t, pool, tapID, KindTap)
	if r.State != StateDelivered || r.Attempts != 1 || r.DeliveredAt == nil || r.LastError != nil {
		t.Errorf("row = %+v, want delivered once", r)
	}
	var resp TapResponse
	if err := json.Unmarshal(r.Response, &resp); err != nil {
		t.Fatalf("response: %v", err)
	}
	if resp.IntentState != "allocated" || resp.AllocatedUnits != 12_500_000 || resp.PriceKobo != 4000 ||
		resp.Symbol == nil || *resp.Symbol != "MAMAPUT" {
		t.Errorf("stored response = %+v", resp)
	}

	// Delivered rows are not delivered again.
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if f.callsFor(tapID) != 1 {
		t.Errorf("a delivered row was sent again")
	}
}

func TestTheWorkerSendsTheRailRequest(t *testing.T) {
	// One row, one stub, read what arrived. Separate from the delivery test
	// because f.last is whichever row the tick sent last, and the tick
	// sends every due row in the table.
	pool := testPool(t)
	f, client := stub(t)
	tapID, _ := charge(t, pool, newUser(t, pool, "Ada", ""), money.Naira(10_000))
	usdcLeg(t, pool, tapID, "fulfilled")
	f.status, f.body = http.StatusInternalServerError, `{}` // hold everything else back
	if _, _, err := (&Worker{Pool: pool, Client: client}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.status, f.body = 0, ""
	// Only our row is due now; the rest are backing off.
	_, _ = pool.Exec(context.Background(), `UPDATE equity_outbox SET next_at = now() WHERE tap_id = $1`, tapID)
	if _, _, err := (&Worker{Pool: pool, Client: client}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last.method != http.MethodPost || f.last.path != "/v1/rail/taps" {
		t.Errorf("called %s %s, want POST /v1/rail/taps", f.last.method, f.last.path)
	}
	if f.last.body["tap_ref"] != tapID.String() || f.last.body["cardholder_display_name"] != "Ada" ||
		f.last.body["amount_kobo"] != float64(1_000_000) {
		t.Errorf("sent %v", f.last.body)
	}
}

func TestAnOutageIsRetriedWithBackoffForever(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	f.status, f.body = http.StatusInternalServerError, `{"error":"down"}`
	tapID, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(10_000))
	usdcLeg(t, pool, tapID, "fulfilled")

	// The row was queued at the database's clock; the worker's frozen clock
	// must sit after it or the row is never due. Microsecond precision is
	// what timestamptz keeps, so the equality checks below stay exact.
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	w := &Worker{Pool: pool, Client: client, Now: func() time.Time { return now }}

	for attempt := 1; attempt <= 12; attempt++ {
		delivered, failed, err := w.Tick(context.Background())
		if err != nil {
			t.Fatalf("Tick %d: %v", attempt, err)
		}
		if delivered != 0 || failed != 0 {
			t.Fatalf("Tick %d: delivered %d failed %d against a 500", attempt, delivered, failed)
		}
		r := read(t, pool, tapID, KindTap)
		if r.State != StateQueued || r.Attempts != attempt {
			t.Fatalf("after %d failures row = %+v", attempt, r)
		}
		if r.LastError == nil || !strings.Contains(*r.LastError, "500") {
			t.Errorf("last_error = %v, want the 500", r.LastError)
		}
		if want := now.Add(Backoff(attempt)); !r.NextAt.Equal(want) {
			t.Errorf("after %d failures next_at = %s, want %s", attempt, r.NextAt, want)
		}
		// Not due yet: a tick now does nothing to it.
		before := f.callsFor(tapID)
		_, _, _ = w.Tick(context.Background())
		if f.callsFor(tapID) != before {
			t.Errorf("a row was retried before its next_at")
		}
		now = r.NextAt
	}
	if Backoff(1) != 5*time.Second || Backoff(2) != 10*time.Second || Backoff(8) != 10*time.Minute ||
		Backoff(100) != 10*time.Minute {
		t.Errorf("backoff series is wrong: %s %s %s %s", Backoff(1), Backoff(2), Backoff(8), Backoff(100))
	}

	// The market comes back; the row is delivered on the next due tick.
	f.status, f.body = 0, ""
	if delivered, _, err := w.Tick(context.Background()); err != nil || delivered < 1 {
		t.Fatalf("after recovery delivered %d, err %v", delivered, err)
	}
	if r := read(t, pool, tapID, KindTap); r.State != StateDelivered || r.Attempts != 13 {
		t.Errorf("row = %+v, want delivered on the 13th attempt", r)
	}
}

func TestARefusalIsFailedAfterFiveAttempts(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	f.status, f.body = http.StatusUnprocessableEntity, `{"error":"unknown merchant"}`
	tapID, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(10_000))
	usdcLeg(t, pool, tapID, "fulfilled")

	now := time.Now()
	w := &Worker{Pool: pool, Client: client, Now: func() time.Time { return now }}
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		_, failed, err := w.Tick(context.Background())
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		r := read(t, pool, tapID, KindTap)
		if attempt < MaxAttempts {
			if r.State != StateQueued {
				t.Fatalf("attempt %d: failed=%d row=%+v; want still queued", attempt, failed, r)
			}
			now = r.NextAt
			continue
		}
		if failed < 1 || r.State != StateFailed || r.Attempts != MaxAttempts {
			t.Fatalf("attempt %d: failed=%d row=%+v; want failed", attempt, failed, r)
		}
		if r.LastError == nil || !strings.Contains(*r.LastError, "422") {
			t.Errorf("last_error = %v", r.LastError)
		}
	}
	// And it stays there.
	before := f.callsFor(tapID)
	now = now.Add(time.Hour)
	_, _, _ = w.Tick(context.Background())
	if f.callsFor(tapID) != before {
		t.Error("a failed row was retried")
	}
}

func TestConflictAndRateLimitAreNotRefusals(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	f.status, f.body = http.StatusTooManyRequests, `{"error":"slow down"}`
	tapID, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(10_000))
	usdcLeg(t, pool, tapID, "fulfilled")

	now := time.Now()
	w := &Worker{Pool: pool, Client: client, Now: func() time.Time { return now }}
	for attempt := 1; attempt <= MaxAttempts+2; attempt++ {
		if attempt%2 == 0 {
			f.status = http.StatusConflict
		} else {
			f.status = http.StatusTooManyRequests
		}
		if _, _, err := w.Tick(context.Background()); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		r := read(t, pool, tapID, KindTap)
		if r.State != StateQueued || r.Attempts != attempt {
			t.Fatalf("attempt %d: row = %+v, want still queued", attempt, r)
		}
		now = r.NextAt
	}
}

func TestAReversalWaitsForItsTap(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	tapID := uuid.New()
	cardholder := newUser(t, pool, "A", "B")

	// The reversal is queued first, for a tap that has no row of its own
	// (charged before the market was wired up).
	reverse(t, pool, tapID, "merchant_reversal")
	w := &Worker{Pool: pool, Client: client}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := read(t, pool, tapID, KindReverse); r.State != StateQueued || r.Attempts != 0 {
		t.Errorf("reverse row = %+v, want untouched until the tap is delivered", r)
	}
	if f.callsFor(tapID) != 0 {
		t.Fatalf("a reversal was delivered before its tap")
	}

	// Its tap row turns up after all, and is released.
	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: cardholder, Merchant: uuid.New(),
		Amount: money.Naira(10_000), At: time.Now()})
	if _, err := pool.Exec(context.Background(),
		`UPDATE equity_outbox SET state = 'queued' WHERE tap_id = $1 AND kind = 'tap'`, tapID); err != nil {
		t.Fatal(err)
	}

	// One tick delivers the tap; the reversal is only eligible once the
	// tap's delivery is committed, so it goes on the next.
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := read(t, pool, tapID, KindTap); r.State != StateDelivered {
		t.Fatalf("tap row = %+v, want delivered", r)
	}
	if r := read(t, pool, tapID, KindReverse); r.State != StateQueued {
		t.Fatalf("reverse row = %+v, want still queued on the tick that delivered its tap", r)
	}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := read(t, pool, tapID, KindReverse)
	if r.State != StateDelivered {
		t.Errorf("reverse row = %+v", r)
	}
	if f.callsFor(tapID) != 2 {
		t.Errorf("tap was called %d times, want 2 (charge, reverse)", f.callsFor(tapID))
	}
	var resp ReverseResponse
	_ = json.Unmarshal(r.Response, &resp)
	if resp.State != "reversed" || resp.UnwoundUnits != 12_500_000 {
		t.Errorf("stored reverse response = %+v", resp)
	}
}

// The fee that funds a buyback is earned when the merchant is paid, so
// nothing reaches the market until the tap is settled.
func TestATapIsHeldUntilTheMerchantIsPaid(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	tapID, _ := charge(t, pool, newUser(t, pool, "Ada", "Okafor"), money.Naira(10_000))
	w := &Worker{Pool: pool, Client: client}

	if r := read(t, pool, tapID, KindTap); r.State != StateHeld {
		t.Fatalf("row = %+v, want held", r)
	}
	// No leg at all: nothing to release, nothing delivered.
	if released, err := ReleaseIfSettled(context.Background(), pool, tapID); err != nil || released {
		t.Errorf("released a tap with no settlement: %v %v", released, err)
	}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callsFor(tapID) != 0 || read(t, pool, tapID, KindTap).State != StateHeld {
		t.Fatalf("a held tap was delivered")
	}

	// The order is on chain but not filled: still held.
	usdcLeg(t, pool, tapID, "submitted")
	if released, _ := ReleaseIfSettled(context.Background(), pool, tapID); released {
		t.Error("released on a submitted order")
	}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callsFor(tapID) != 0 {
		t.Fatalf("delivered before the order was fulfilled")
	}

	// The provider paid the merchant.
	usdcLeg(t, pool, tapID, "fulfilled")
	if released, err := ReleaseIfSettled(context.Background(), pool, tapID); err != nil || !released {
		t.Fatalf("release after fulfilment: %v %v", released, err)
	}
	if r := read(t, pool, tapID, KindTap); r.State != StateQueued {
		t.Fatalf("row = %+v, want queued", r)
	}
	// And a second release is a no-op, not a second queueing.
	if released, _ := ReleaseIfSettled(context.Background(), pool, tapID); released {
		t.Error("released twice")
	}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callsFor(tapID) != 1 || read(t, pool, tapID, KindTap).State != StateDelivered {
		t.Errorf("tap sent %d times, state %s; want once, delivered", f.callsFor(tapID), read(t, pool, tapID, KindTap).State)
	}
}

func TestANairaTapIsReleasedWhenItsLegSettles(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	tapID, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(2_000))
	w := &Worker{Pool: pool, Client: client}

	ngnLeg(t, pool, tapID, "submitted")
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callsFor(tapID) != 0 || read(t, pool, tapID, KindTap).State != StateHeld {
		t.Fatalf("delivered while the naira leg was in flight")
	}

	ngnLeg(t, pool, tapID, "settled")
	if released, err := ReleaseIfSettled(context.Background(), pool, tapID); err != nil || !released {
		t.Fatalf("release after the naira leg settled: %v %v", released, err)
	}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callsFor(tapID) != 1 || read(t, pool, tapID, KindTap).State != StateDelivered {
		t.Errorf("naira tap not delivered after its leg settled")
	}
}

func TestAMixedTapNeedsBothLegs(t *testing.T) {
	pool := testPool(t)
	tapID, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(2_000))

	usdcLeg(t, pool, tapID, "fulfilled")
	ngnLeg(t, pool, tapID, "submitted")
	if released, _ := ReleaseIfSettled(context.Background(), pool, tapID); released {
		t.Fatal("released with the naira leg outstanding")
	}
	ngnLeg(t, pool, tapID, "settled")
	usdcLeg(t, pool, tapID, "submitted")
	if released, _ := ReleaseIfSettled(context.Background(), pool, tapID); released {
		t.Fatal("released with the USDC leg outstanding")
	}
	usdcLeg(t, pool, tapID, "fulfilled")
	if released, err := ReleaseIfSettled(context.Background(), pool, tapID); err != nil || !released {
		t.Fatalf("both legs settled: released=%v err=%v", released, err)
	}
	if r := read(t, pool, tapID, KindTap); r.State != StateQueued {
		t.Errorf("row = %+v, want queued", r)
	}

	// A failed leg never releases.
	failed, _ := charge(t, pool, newUser(t, pool, "C", "D"), money.Naira(2_000))
	usdcLeg(t, pool, failed, "fulfilled")
	ngnLeg(t, pool, failed, "failed")
	if released, _ := ReleaseIfSettled(context.Background(), pool, failed); released {
		t.Error("released a tap whose naira leg failed")
	}
}

// A leg that settled without calling ReleaseIfSettled cannot strand the
// shares: the worker sweeps every tick.
func TestTheSweepReleasesAStrandedHeldRow(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	tapID, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(2_000))
	usdcLeg(t, pool, tapID, "fulfilled") // no ReleaseIfSettled

	if r := read(t, pool, tapID, KindTap); r.State != StateHeld {
		t.Fatalf("row = %+v, want held", r)
	}
	n, err := ReleaseSettled(context.Background(), pool)
	if err != nil || n < 1 {
		t.Fatalf("sweep released %d, err %v", n, err)
	}
	if r := read(t, pool, tapID, KindTap); r.State != StateQueued {
		t.Fatalf("row = %+v, want queued by the sweep", r)
	}

	// The tick itself sweeps, so a stranded row is delivered on the tick
	// after its tap settles.
	other, _ := charge(t, pool, newUser(t, pool, "E", "F"), money.Naira(2_000))
	ngnLeg(t, pool, other, "settled")
	if _, _, err := (&Worker{Pool: pool, Client: client}).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callsFor(other) != 1 || read(t, pool, other, KindTap).State != StateDelivered {
		t.Errorf("the tick did not release and deliver a stranded row")
	}
}

// A tap reversed before the market heard of it has nothing to unwind:
// the row is cancelled and nothing is ever sent.
func TestAReversalBeforeDeliveryCancelsTheTap(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	w := &Worker{Pool: pool, Client: client}

	// Held.
	held, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(2_000))
	reverse(t, pool, held, "customer_returned")
	r := read(t, pool, held, KindTap)
	if r.State != StateCancelled || r.LastError == nil || !strings.Contains(*r.LastError, "customer_returned") {
		t.Fatalf("held row after reversal = %+v, want cancelled with the reason", r)
	}
	if rowCount(t, pool, held, KindReverse) != 0 {
		t.Error("a reversal was queued for a tap the market never heard of")
	}
	// Even if it settles afterwards, it stays cancelled.
	usdcLeg(t, pool, held, "fulfilled")
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callsFor(held) != 0 || read(t, pool, held, KindTap).State != StateCancelled {
		t.Errorf("a cancelled tap reached the market")
	}

	// Queued but not yet delivered.
	queued, _ := charge(t, pool, newUser(t, pool, "C", "D"), money.Naira(2_000))
	usdcLeg(t, pool, queued, "fulfilled")
	if released, _ := ReleaseIfSettled(context.Background(), pool, queued); !released {
		t.Fatal("not released")
	}
	reverse(t, pool, queued, "duplicate")
	if read(t, pool, queued, KindTap).State != StateCancelled || rowCount(t, pool, queued, KindReverse) != 0 {
		t.Errorf("queued row not cancelled by reversal")
	}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.callsFor(queued) != 0 {
		t.Errorf("a cancelled tap reached the market")
	}
}

func TestAReversalAfterDeliveryIsSentToTheMarket(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	w := &Worker{Pool: pool, Client: client}
	tapID, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(2_000))
	usdcLeg(t, pool, tapID, "fulfilled")
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read(t, pool, tapID, KindTap).State != StateDelivered {
		t.Fatal("tap not delivered")
	}

	reverse(t, pool, tapID, "customer_returned")
	if read(t, pool, tapID, KindTap).State != StateDelivered {
		t.Error("a delivered row was touched by the reversal")
	}
	if r := read(t, pool, tapID, KindReverse); r.State != StateQueued {
		t.Fatalf("reverse row = %+v, want queued", r)
	}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read(t, pool, tapID, KindReverse).State != StateDelivered || f.callsFor(tapID) != 2 {
		t.Errorf("reversal not delivered: calls=%d", f.callsFor(tapID))
	}
}

// A reversal that arrives while the tap is being delivered cannot decide
// "never sent" a moment before it is: the row is locked for the delivery,
// so the reversal waits and then queues a reversal of the delivered tap.
func TestAReversalCannotCrossADeliveryInFlight(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	f.hold, f.entered = make(chan struct{}), make(chan struct{})
	tapID, _ := charge(t, pool, newUser(t, pool, "A", "B"), money.Naira(2_000))
	usdcLeg(t, pool, tapID, "fulfilled")
	if released, _ := ReleaseIfSettled(context.Background(), pool, tapID); !released {
		t.Fatal("not released")
	}

	ticked := make(chan error, 1)
	go func() {
		_, _, err := (&Worker{Pool: pool, Client: client}).Tick(context.Background())
		ticked <- err
	}()
	<-f.entered // the worker is inside the call, row locked

	reversed := make(chan error, 1)
	go func() {
		reversed <- movements.InTx(context.Background(), pool, func(tx pgx.Tx) error {
			return RecordReversal(context.Background(), tx, tapID, "customer_returned")
		})
	}()
	select {
	case err := <-reversed:
		t.Fatalf("the reversal did not wait for the delivery in flight (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(f.hold)
	if err := <-ticked; err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if err := <-reversed; err != nil {
		t.Fatalf("RecordReversal: %v", err)
	}
	if read(t, pool, tapID, KindTap).State != StateDelivered {
		t.Errorf("tap row = %+v, want delivered", read(t, pool, tapID, KindTap))
	}
	if r := read(t, pool, tapID, KindReverse); r.State != StateQueued {
		t.Errorf("reverse row = %+v, want queued: the market was told and must be told to unwind", r)
	}
}

func TestUnsentTapsAreListedForTheCardholder(t *testing.T) {
	pool := testPool(t)
	cardholder := newUser(t, pool, "A", "B")
	held, _ := charge(t, pool, cardholder, money.Naira(2_000))
	queued, _ := charge(t, pool, cardholder, money.Naira(3_000))
	usdcLeg(t, pool, queued, "fulfilled")
	if released, _ := ReleaseIfSettled(context.Background(), pool, queued); !released {
		t.Fatal("not released")
	}
	delivered, _ := charge(t, pool, cardholder, money.Naira(4_000))
	if _, err := pool.Exec(context.Background(),
		`UPDATE equity_outbox SET state = 'delivered' WHERE tap_id = $1`, delivered); err != nil {
		t.Fatal(err)
	}
	cancelled, _ := charge(t, pool, cardholder, money.Naira(5_000))
	reverse(t, pool, cancelled, "x")

	rows, err := UnsentFor(context.Background(), pool, cardholder, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].TapID != queued || rows[0].State != StateQueued ||
		rows[1].TapID != held || rows[1].State != StateHeld ||
		rows[1].Amount.Minor() != 200_000 || rows[1].Merchant == uuid.Nil {
		t.Errorf("unsent = %+v", rows)
	}
	if rows, _ := UnsentFor(context.Background(), pool, cardholder, 1); len(rows) != 1 || rows[0].TapID != queued {
		t.Errorf("limit 1 = %+v", rows)
	}
}

func TestADisabledClientIsANoOp(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Error("nil client reports enabled")
	}
	if _, err := c.Holdings(context.Background(), "x"); err != ErrDisabled {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
	if New("", "token") != nil || New("http://x", "") != nil {
		t.Error("a half-configured client was built")
	}
}

func TestShares(t *testing.T) {
	for units, want := range map[int64]string{
		0: "0", 12_500_000: "0.125", 20_312_500: "0.203125", 100_000_000: "1", 150_000_000_000_000: "1500000",
	} {
		if got := Shares(units); got != want {
			t.Errorf("Shares(%d) = %q, want %q", units, got, want)
		}
	}
}
