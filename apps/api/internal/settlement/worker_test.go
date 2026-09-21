package settlement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
	"github.com/usezoracle/tapp/api/services/baas"
)

// A bank rail the test drives. Not a runtime mode: anything that fabricates a
// transfer result in production is one deployment mistake from telling people
// they have been paid when they have not.
type fakeRail struct {
	mu sync.Mutex

	accountName string
	enquiryErr  error

	status      baas.TransferStatus
	message     string
	transferErr error

	statusOnChase baas.TransferStatus

	transfers []baas.TransferRequest
}

func (f *fakeRail) Name() string { return "fake" }

func (f *fakeRail) NameEnquiry(_ context.Context, bankCode, account string) (*baas.NameEnquiry, error) {
	if f.enquiryErr != nil {
		return nil, f.enquiryErr
	}
	return &baas.NameEnquiry{
		Reference: "enq-" + account, AccountNumber: account,
		AccountName: f.accountName, BankCode: bankCode,
	}, nil
}

func (f *fakeRail) Transfer(_ context.Context, req baas.TransferRequest) (*baas.Transfer, error) {
	f.mu.Lock()
	f.transfers = append(f.transfers, req)
	f.mu.Unlock()

	if f.transferErr != nil {
		return nil, f.transferErr
	}
	return &baas.Transfer{
		Reference: "ref-" + req.PaymentReference, PaymentReference: req.PaymentReference,
		Status: f.status, Message: f.message,
	}, nil
}

func (f *fakeRail) TransferStatus(_ context.Context, ref string) (*baas.Transfer, error) {
	return &baas.Transfer{Reference: ref, Status: f.statusOnChase, Message: f.message}, nil
}

func (f *fakeRail) sent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.transfers)
}

func (f *fakeRail) ListBanks(context.Context) ([]baas.Bank, error)             { return nil, nil }
func (f *fakeRail) ListAccounts(context.Context, bool) ([]baas.Account, error) { return nil, nil }
func (f *fakeRail) GetAccount(context.Context, string) (*baas.Account, error)  { return nil, nil }
func (f *fakeRail) InitiateIdentity(context.Context, baas.IdentityInit) (*baas.IdentityResult, error) {
	return nil, nil
}
func (f *fakeRail) ValidateIdentity(context.Context, string, string, string) (*baas.IdentityResult, error) {
	return nil, nil
}
func (f *fakeRail) CreateSubAccount(context.Context, baas.CreateSubAccountRequest) (*baas.Account, error) {
	return nil, nil
}
func (f *fakeRail) VerifyWebhook([]byte, string) bool               { return true }
func (f *fakeRail) WebhookConfigured() bool                         { return true }
func (f *fakeRail) ParseWebhook([]byte) (*baas.WebhookEvent, error) { return nil, nil }

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
	t.Cleanup(pool.Close)
	return pool
}

type fixture struct {
	Pool     *pgxpool.Pool
	Worker   *Worker
	Rail     *fakeRail
	Merchant uuid.UUID
}

func newFixture(t *testing.T, earned money.Amount) *fixture {
	t.Helper()
	pool := testPool(t)
	// The worker's Tick claims the oldest pending payouts first, whoever
	// raised them. Other packages' tests share this database and leave
	// payouts behind, so start from an empty queue or the batch fills with
	// somebody else's rows and this test's payout never gets its turn.
	if _, err := pool.Exec(context.Background(), `DELETE FROM payouts WHERE state IN ('pending','submitting')`); err != nil {
		t.Fatalf("clear payouts: %v", err)
	}
	rail := &fakeRail{accountName: "ADA OKAFOR", status: baas.TransferSuccess}

	merchant := uuid.New()
	if earned.IsPositive() {
		// A tap gives the merchant something to be paid.
		if _, err := movements.Deposit(context.Background(), pool, uuid.New(), earned, "test", uuid.NewString()); err != nil {
			t.Fatalf("seed: %v", err)
		}
		payer := uuid.New()
		if _, err := movements.Deposit(context.Background(), pool, payer, earned, "test", uuid.NewString()); err != nil {
			t.Fatalf("seed payer: %v", err)
		}
		if err := movements.InTx(context.Background(), pool, func(tx pgx.Tx) error {
			_, e := movements.Tap(context.Background(), tx, payer, merchant, earned,
				money.Zero(earned.Currency()), uuid.New())
			return e
		}); err != nil {
			t.Fatalf("seed tap: %v", err)
		}
	}

	return &fixture{
		Pool: pool, Rail: rail, Merchant: merchant,
		Worker: &Worker{Pool: pool, Rail: rail},
	}
}

func (f *fixture) request(amount money.Amount) Request {
	return Request{
		Beneficiary:   Beneficiary{Kind: Merchant, ID: f.Merchant},
		Amount:        amount,
		BankCode:      "058",
		AccountNumber: "0123456789",
		AccountName:   "ADA OKAFOR",
	}
}

func (f *fixture) owed(t *testing.T) money.Amount {
	t.Helper()
	b, err := ledger.Balance(context.Background(), f.Pool,
		ledger.Merchant(f.Merchant), ledger.KindMerchantPayable, money.NGN)
	if err != nil {
		t.Fatalf("owed: %v", err)
	}
	return b
}

func (f *fixture) state(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := f.Pool.QueryRow(context.Background(),
		`SELECT state FROM payouts WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("read payout: %v", err)
	}
	return s
}

func TestAConfirmedPayoutDischargesWhatIsOwed(t *testing.T) {
	amount := money.Naira(5_000)
	f := newFixture(t, amount)
	ctx := context.Background()

	if got := f.owed(t); got.Minor() != 500_000 {
		t.Fatalf("setup: merchant owed %s", got)
	}

	p, err := f.Worker.Open(ctx, f.request(amount))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Reserving moves it out of what they are owed and into payable.
	if got := f.owed(t); !got.IsZero() {
		t.Errorf("merchant still owed %s after the payout was raised", got)
	}

	if _, _, err := f.Worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := f.state(t, p.ID); got != string(Confirmed) {
		t.Fatalf("state = %q, want confirmed", got)
	}

	audit, err := ledger.Auditor(ctx, f.Pool)
	if err != nil || !audit.Balanced {
		t.Fatalf("ledger does not balance after a payout: %v", err)
	}
}

// The distinction the whole worker turns on. An empty float is temporary and
// must be retried; a wrong account number is terminal and must return the
// money.
func TestATemporaryFailureRetriesAndATerminalOneReturnsTheMoney(t *testing.T) {
	amount := money.Naira(2_000)

	t.Run("temporary", func(t *testing.T) {
		f := newFixture(t, amount)
		ctx := context.Background()
		f.Rail.transferErr = errors.New("Account balance is insufficient")

		p, err := f.Worker.Open(ctx, f.request(amount))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, _, err := f.Worker.Tick(ctx); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if got := f.state(t, p.ID); got != string(Pending) {
			t.Fatalf("state = %q, want pending -- an empty float is fixed by a top-up", got)
		}

		// A further tick right away must NOT retry: the backoff is what stops
		// one blip burning the whole attempt budget in a fraction of a second.
		before := f.Rail.sent()
		if _, _, err := f.Worker.Tick(ctx); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if f.Rail.sent() != before {
			t.Fatal("a failed payout was retried immediately instead of backing off")
		}

		// Once the backoff has passed and the float is topped up, it goes.
		f.Rail.transferErr = nil
		if _, err := f.Pool.Exec(ctx,
			`UPDATE payouts SET updated_at = now() - interval '1 hour' WHERE id = $1`,
			p.ID); err != nil {
			t.Fatalf("age the payout: %v", err)
		}
		if _, _, err := f.Worker.Tick(ctx); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if got := f.state(t, p.ID); got != string(Confirmed) {
			t.Errorf("state = %q, want confirmed after the float was topped up", got)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		f := newFixture(t, amount)
		ctx := context.Background()
		f.Rail.status = baas.TransferFailed
		f.Rail.message = "account is closed"

		p, err := f.Worker.Open(ctx, f.request(amount))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, _, err := f.Worker.Tick(ctx); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if got := f.state(t, p.ID); got != string(Failed) {
			t.Fatalf("state = %q, want failed", got)
		}
		// The merchant is owed it again: they earned it, we could not deliver.
		if got := f.owed(t); got.Minor() != 200_000 {
			t.Errorf("merchant owed %s after a failed payout, want their ₦2,000.00 back", got)
		}
	})
}

// A request that timed out may or may not have moved money. It must be chased,
// never retried -- retrying an unknown is how somebody gets paid twice.
func TestATimeoutIsChasedNotRetried(t *testing.T) {
	amount := money.Naira(1_000)
	f := newFixture(t, amount)
	ctx := context.Background()
	f.Rail.transferErr = fmt.Errorf("context deadline exceeded")

	p, err := f.Worker.Open(ctx, f.request(amount))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, _, err := f.Worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := f.state(t, p.ID); got != string(Unknown) {
		t.Fatalf("state = %q, want unknown", got)
	}

	// A further tick must NOT send it again.
	before := f.Rail.sent()
	f.Rail.transferErr = nil
	if _, _, err := f.Worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if f.Rail.sent() != before {
		t.Fatal("an unknown payout was sent again; the beneficiary could be paid twice")
	}
}

// Money must never go to an account that is not who it was.
func TestAChangedAccountNameStopsThePayout(t *testing.T) {
	amount := money.Naira(3_000)
	f := newFixture(t, amount)
	ctx := context.Background()
	f.Rail.accountName = "SOMEBODY ELSE"

	p, err := f.Worker.Open(ctx, f.request(amount))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, _, err := f.Worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := f.state(t, p.ID); got != string(Failed) {
		t.Fatalf("state = %q, want failed", got)
	}
	if f.Rail.sent() != 0 {
		t.Fatal("a transfer was sent to an account whose name did not match")
	}
	if got := f.owed(t); got.Minor() != 300_000 {
		t.Errorf("merchant owed %s, want their money back", got)
	}
}

// Two workers racing must not both send the same payout. That claim is the
// only thing between a scaled deployment and paying people twice.
func TestTwoWorkersCannotSendTheSamePayout(t *testing.T) {
	amount := money.Naira(1_500)
	f := newFixture(t, amount)
	ctx := context.Background()

	if _, err := f.Worker.Open(ctx, f.request(amount)); err != nil {
		t.Fatalf("Open: %v", err)
	}

	second := &Worker{Pool: f.Pool, Rail: f.Rail}
	var wg sync.WaitGroup
	wg.Add(2)
	for _, w := range []*Worker{f.Worker, second} {
		go func(w *Worker) {
			defer wg.Done()
			_, _, _ = w.Tick(ctx)
		}(w)
	}
	wg.Wait()

	if f.Rail.sent() != 1 {
		t.Fatalf("the payout was sent %d times, want once", f.Rail.sent())
	}
}

// The reference is deterministic, so a retry presents the same one and the
// rail refuses the duplicate rather than paying twice.
func TestTheProviderReferenceIsDeterministic(t *testing.T) {
	amount := money.Naira(1_000)
	f := newFixture(t, amount)
	ctx := context.Background()

	p, err := f.Worker.Open(ctx, f.request(amount))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, _, err := f.Worker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	f.Rail.mu.Lock()
	defer f.Rail.mu.Unlock()
	if len(f.Rail.transfers) != 1 {
		t.Fatalf("sent %d transfers", len(f.Rail.transfers))
	}
	want := baas.PaymentReference("payout", p.ID.String())
	if f.Rail.transfers[0].PaymentReference != want {
		t.Errorf("reference = %q, want %q", f.Rail.transfers[0].PaymentReference, want)
	}
}

// No rail configured is our outage, not their refusal. Nothing may be marked
// failed for it.
func TestNoRailDoesNotFailPayouts(t *testing.T) {
	amount := money.Naira(1_000)
	f := newFixture(t, amount)
	ctx := context.Background()
	f.Worker.Rail = nil

	p, err := f.Worker.Open(ctx, f.request(amount))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, _, err := f.Worker.Tick(ctx); !errors.Is(err, ErrNoRail) {
		t.Fatalf("got %v, want ErrNoRail", err)
	}
	if got := f.state(t, p.ID); got != string(Pending) {
		t.Errorf("state = %q, want pending -- our outage is not their refusal", got)
	}
}

func TestAPayoutNeedsTheNameTheBankReturned(t *testing.T) {
	f := newFixture(t, money.Naira(1_000))
	req := f.request(money.Naira(1_000))
	req.AccountName = ""
	if _, err := f.Worker.Open(context.Background(), req); err == nil {
		t.Fatal("a payout with no verified account name was accepted")
	}
}

// bankAccount gives the merchant somewhere to be paid, verified, because
// PayMerchants will not pay an unverified account.
func (f *fixture) bankAccount(t *testing.T, verified bool) {
	t.Helper()
	verifiedAt := "now()"
	if !verified {
		verifiedAt = "NULL"
	}
	// The profile is owned by a real user row: sender_profiles carries a
	// foreign key to users.
	owner := uuid.New()
	if _, err := f.Pool.Exec(context.Background(), `
		INSERT INTO users
			(id, created_at, updated_at, first_name, last_name, email, password, scope)
		VALUES ($1, now(), now(), 'Test', 'Merchant', $2, '', 'user')`,
		owner, owner.String()+"@test.local"); err != nil {
		t.Fatalf("seed merchant user: %v", err)
	}
	if _, err := f.Pool.Exec(context.Background(), `
		INSERT INTO sender_profiles (id, updated_at, domain_whitelist, user_sender_profile)
		VALUES ($1, now(), '{}', $2)`, f.Merchant, owner); err != nil {
		t.Fatalf("seed sender profile: %v", err)
	}
	if _, err := f.Pool.Exec(context.Background(), `
		INSERT INTO merchant_bank_accounts
			(id, created_at, updated_at, currency, bank_code, account_number,
			 account_name, verified_at, sender_profile_merchant_bank_account)
		VALUES (gen_random_uuid(), now(), now(), 'NGN', '058', '0123456789',
		        'ADA OKAFOR', `+verifiedAt+`, $1)`, f.Merchant); err != nil {
		t.Fatalf("seed bank account: %v", err)
	}
}

// A tap credits merchant_payable and nothing used to draw it down, so
// merchants accrued balances no process ever delivered. This is that step.
func TestWhatAMerchantIsOwedBecomesAPayout(t *testing.T) {
	f := newFixture(t, money.Naira(1_500))
	f.bankAccount(t, true)
	ctx := context.Background()

	if _, err := f.Worker.PayMerchants(ctx); err != nil {
		t.Fatalf("PayMerchants: %v", err)
	}

	// Counted for THIS merchant, not globally: the test database is
	// persistent and carries merchants left owed by earlier runs, so a global
	// count measures the leftovers as much as the behaviour under test.
	if n := f.payoutCount(t); n != 1 {
		t.Fatalf("opened %d payouts for this merchant, want 1", n)
	}

	// The claim is reserved, not still sitting there: paying it out twice is
	// the failure this must not have.
	if owed := f.owed(t); !owed.IsZero() {
		t.Errorf("still owed %s after a payout was opened, want nothing", owed)
	}

	// A second pass must find nothing left to do for this merchant.
	if _, err := f.Worker.PayMerchants(ctx); err != nil {
		t.Fatalf("second PayMerchants: %v", err)
	}
	if n := f.payoutCount(t); n != 1 {
		t.Errorf("payouts for this merchant = %d after a second pass, want 1 -- the same money would go twice", n)
	}
}

// payoutCount is how many payouts exist for this fixture's merchant.
func (f *fixture) payoutCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payouts WHERE beneficiary_id = $1`, f.Merchant).Scan(&n); err != nil {
		t.Fatalf("count payouts: %v", err)
	}
	return n
}

// An unverified account is skipped, not paid and not failed. account_name is
// what the bank returned for the number; paying before that check is how money
// reaches a mistyped digit and does not come back.
func TestAnUnverifiedBankAccountIsNotPaid(t *testing.T) {
	f := newFixture(t, money.Naira(1_500))
	f.bankAccount(t, false)

	if _, err := f.Worker.PayMerchants(context.Background()); err != nil {
		t.Fatalf("PayMerchants: %v", err)
	}
	if n := f.payoutCount(t); n != 0 {
		t.Fatalf("opened %d payouts against an unverified account, want 0", n)
	}
	// Still owed: the merchant has the money coming and can add an account.
	if owed := f.owed(t); owed.Minor() != 150_000 {
		t.Errorf("owed = %s, want the claim left intact at ₦1,500.00", owed)
	}
}

// A bank transfer costs the same whatever it carries, so a few naira is left
// to accrue rather than spent on fees. Nothing is deducted -- this decides
// only WHEN the money moves.
func TestATinyBalanceWaitsRatherThanPayingAFee(t *testing.T) {
	f := newFixture(t, money.Naira(50))
	f.bankAccount(t, true)

	if _, err := f.Worker.PayMerchants(context.Background()); err != nil {
		t.Fatalf("PayMerchants: %v", err)
	}
	if n := f.payoutCount(t); n != 0 {
		t.Fatalf("opened %d payouts for ₦50, want 0 -- below the minimum", n)
	}
	if owed := f.owed(t); owed.Minor() != 5_000 {
		t.Errorf("owed = %s, want ₦50.00 still owed and visible", owed)
	}
}

// A payout stranded by a change of arrangement still owes its beneficiary the
// money. Leaving it reserved in `payable` is the platform quietly holding what
// it neither earned nor delivered.
func TestAbandoningAPayoutReturnsWhatIsOwed(t *testing.T) {
	f := newFixture(t, money.Naira(1_500))
	ctx := context.Background()

	p, err := f.Worker.Open(ctx, f.request(money.Naira(1_500)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if owed := f.owed(t); !owed.IsZero() {
		t.Fatalf("owed %s after opening, want it reserved", owed)
	}

	if _, err := f.Worker.Abandon(ctx, p.ID, "the rail was retired"); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if owed := f.owed(t); owed.Minor() != 150_000 {
		t.Errorf("owed = %s after abandoning, want ₦1,500.00 back", owed)
	}
	if s := f.state(t, p.ID); s != "failed" {
		t.Errorf("payout state = %q, want failed", s)
	}

	// Twice must not pay twice.
	if _, err := f.Worker.Abandon(ctx, p.ID, "again"); err == nil {
		t.Error("abandoning an already-returned payout was allowed")
	}
	if owed := f.owed(t); owed.Minor() != 150_000 {
		t.Errorf("owed = %s after a second abandon, want it unchanged", owed)
	}
}

// A confirmed payout moved real money. Returning its reservation would credit
// the beneficiary a second time for a transfer they have already received.
func TestAConfirmedPayoutCannotBeAbandoned(t *testing.T) {
	f := newFixture(t, money.Naira(1_500))
	ctx := context.Background()

	p, err := f.Worker.Open(ctx, f.request(money.Naira(1_500)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := f.Pool.Exec(ctx,
		`UPDATE payouts SET state = 'confirmed' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Worker.Abandon(ctx, p.ID, "should be refused"); err == nil {
		t.Fatal("a confirmed payout was abandoned -- the beneficiary would be paid twice")
	}
	if owed := f.owed(t); !owed.IsZero() {
		t.Errorf("owed = %s, want nothing restored for a delivered payout", owed)
	}
}
