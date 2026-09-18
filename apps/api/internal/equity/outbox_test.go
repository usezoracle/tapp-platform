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
}

func (f *freedom) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
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
	if r := read(t, pool, tapID, KindTap); r.State != StatePending || r.Attempts != 0 {
		t.Errorf("row = %+v, want pending with no attempts", r)
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
	tapID := uuid.New()
	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: newUser(t, pool, "Ada", ""), Merchant: uuid.New(),
		Amount: money.Naira(10_000), At: time.Now()})

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
	tapID := uuid.New()
	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: newUser(t, pool, "Ada", ""), Merchant: uuid.New(),
		Amount: money.Naira(10_000), At: time.Now()})
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
	tapID := uuid.New()
	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: newUser(t, pool, "A", "B"), Merchant: uuid.New(),
		Amount: money.Naira(10_000), At: time.Now()})

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
		if r.State != StatePending || r.Attempts != attempt {
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
	tapID := uuid.New()
	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: newUser(t, pool, "A", "B"), Merchant: uuid.New(),
		Amount: money.Naira(10_000), At: time.Now()})

	now := time.Now()
	w := &Worker{Pool: pool, Client: client, Now: func() time.Time { return now }}
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		_, failed, err := w.Tick(context.Background())
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		r := read(t, pool, tapID, KindTap)
		if attempt < MaxAttempts {
			if r.State != StatePending {
				t.Fatalf("attempt %d: failed=%d row=%+v; want still pending", attempt, failed, r)
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
	tapID := uuid.New()
	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: newUser(t, pool, "A", "B"), Merchant: uuid.New(),
		Amount: money.Naira(10_000), At: time.Now()})

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
		if r.State != StatePending || r.Attempts != attempt {
			t.Fatalf("attempt %d: row = %+v, want still pending", attempt, r)
		}
		now = r.NextAt
	}
}

func TestAReversalWaitsForItsTap(t *testing.T) {
	pool := testPool(t)
	f, client := stub(t)
	tapID := uuid.New()
	cardholder := newUser(t, pool, "A", "B")

	// The reversal is queued first, as if the tap's delivery were still
	// backing off.
	err := movements.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		return EnqueueReverse(context.Background(), tx, tapID, "merchant_reversal")
	})
	if err != nil {
		t.Fatalf("EnqueueReverse: %v", err)
	}
	w := &Worker{Pool: pool, Client: client}
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := read(t, pool, tapID, KindReverse); r.State != StatePending || r.Attempts != 0 {
		t.Errorf("reverse row = %+v, want untouched until the tap is delivered", r)
	}
	if f.callsFor(tapID) != 0 {
		t.Fatalf("a reversal was delivered before its tap")
	}

	enqueue(t, pool, TapEvent{TapID: tapID, Cardholder: cardholder, Merchant: uuid.New(),
		Amount: money.Naira(10_000), At: time.Now()})

	// One tick delivers the tap; the reversal is only eligible once the
	// tap's delivery is committed, so it goes on the next.
	if _, _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := read(t, pool, tapID, KindTap); r.State != StateDelivered {
		t.Fatalf("tap row = %+v, want delivered", r)
	}
	if r := read(t, pool, tapID, KindReverse); r.State != StatePending {
		t.Fatalf("reverse row = %+v, want still pending on the tick that delivered its tap", r)
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
