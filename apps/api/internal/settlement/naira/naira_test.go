package naira

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
	"github.com/usezoracle/tapp/api/services/baas"
)

// These run against a real Postgres: the claim is a conditional update, the
// idempotency is the ledger's, and neither means anything against a fake.
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
	// ent-owned tables the package reads. The columns it reads are enough.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			first_name text NOT NULL DEFAULT '', last_name text NOT NULL DEFAULT '',
			email text UNIQUE NOT NULL, password text NOT NULL DEFAULT '',
			scope text NOT NULL DEFAULT 'sender');
		CREATE TABLE IF NOT EXISTS merchant_bank_accounts (
			id uuid PRIMARY KEY, created_at timestamptz, updated_at timestamptz,
			currency text, bank_code text, account_number text, account_name text,
			verified_at timestamptz, sender_profile_merchant_bank_account uuid)`); err != nil {
		pool.Close()
		t.Fatalf("ent tables: %v", err)
	}
	// The worker pays every queued row there is; one another test left
	// behind would be paid by this one's rail.
	if _, err := pool.Exec(ctx, `DELETE FROM card_tap_ngn_settlements`); err != nil {
		pool.Close()
		t.Fatalf("clear: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// refusal is a rail error that says it read the request and declined it.
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg }
func (r *refusal) Refused() bool { return true }

// fakeRail is the bank as the test scripts it: a Fintava that can pay out of
// a customer wallet.
type fakeRail struct {
	mu sync.Mutex

	accountName string

	status      baas.TransferStatus
	message     string
	transferErr error
	chase       *baas.Transfer

	transfers []baas.WalletTransferRequest
}

func (f *fakeRail) Name() string { return "fintava" }

func (f *fakeRail) NameEnquiry(_ context.Context, bankCode, account string) (*baas.NameEnquiry, error) {
	return &baas.NameEnquiry{AccountNumber: account, AccountName: f.accountName, BankCode: bankCode}, nil
}

func (f *fakeRail) TransferFromWallet(_ context.Context, req baas.WalletTransferRequest) (*baas.Transfer, error) {
	f.mu.Lock()
	f.transfers = append(f.transfers, req)
	f.mu.Unlock()
	if f.transferErr != nil {
		return nil, f.transferErr
	}
	return &baas.Transfer{
		Reference: "ftv-" + req.PaymentReference, PaymentReference: req.PaymentReference,
		Status: f.status, Message: f.message,
	}, nil
}

func (f *fakeRail) TransferStatus(_ context.Context, ref string) (*baas.Transfer, error) {
	if f.chase != nil {
		return f.chase, nil
	}
	return &baas.Transfer{Reference: ref, Status: baas.TransferPending, RawStatus: "not_found"}, nil
}

func (f *fakeRail) sent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.transfers)
}

// Transfer is the platform-account payout; a naira leg must never use it.
func (f *fakeRail) Transfer(context.Context, baas.TransferRequest) (*baas.Transfer, error) {
	return nil, errors.New("a naira leg must be paid from the cardholder's wallet, not the platform's account")
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

type fixture struct {
	Pool   *pgxpool.Pool
	Rail   *fakeRail
	Worker *Worker
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testPool(t)
	rail := &fakeRail{accountName: "OLUMIDE SILAS OGUNDELE", status: baas.TransferSuccess}
	return &fixture{Pool: pool, Rail: rail, Worker: &Worker{
		Pool: pool, Rail: rail,
		MerchantName: func(context.Context, uuid.UUID) string { return "Mama Put" },
	}}
}

// wallet gives a cardholder a naira deposit account at the rail, opened
// under customerID.
func (f *fixture) wallet(t *testing.T, cardholder uuid.UUID, customerID string) (accountNumber string) {
	t.Helper()
	ctx := context.Background()
	accountNumber = "1" + cardholder.String()[:9]
	if _, err := f.Pool.Exec(ctx, `INSERT INTO users (id, email) VALUES ($1, $2)`,
		cardholder, cardholder.String()+"@t.local"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO ngn_deposit_accounts (user_id, rail, account_number, bank_name, account_name, rail_ref, wallet_id)
		VALUES ($1, 'fintava', $2, 'Loma', 'ADA O', $3, 'wal-' || $3)`, cardholder, accountNumber, customerID); err != nil {
		t.Fatal(err)
	}
	return accountNumber
}

// verifiedBank gives a merchant the account the fixture's rail will name.
func (f *fixture) verifiedBank(t *testing.T, merchant uuid.UUID) {
	t.Helper()
	if _, err := f.Pool.Exec(context.Background(), `
		INSERT INTO merchant_bank_accounts (id, currency, bank_code, account_number, account_name,
		                                    verified_at, sender_profile_merchant_bank_account, created_at, updated_at)
		VALUES ($1, 'NGN', 'OPAYNGPC', '9034409271', 'OLUMIDE SILAS OGUNDELE', now(), $2, now(), now())`,
		uuid.New(), merchant); err != nil {
		t.Fatalf("bank: %v", err)
	}
}

// nairaTap charges a cardholder with a wallet from their naira balance and
// records the leg, the way the tap's own transaction does.
func (f *fixture) nairaTap(t *testing.T, merchant uuid.UUID) (tap uuid.UUID, owed money.Amount) {
	t.Helper()
	ctx := context.Background()
	tap, cardholder := uuid.New(), uuid.New()
	f.wallet(t, cardholder, "cust-"+cardholder.String()[:8])
	amount := money.Naira(1_600)
	fee := money.FeeFor(amount, 50)
	owed, _ = amount.Sub(fee)

	if _, err := movements.Deposit(ctx, f.Pool, cardholder, amount, "fintava", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	err := movements.InTx(ctx, f.Pool, func(tx pgx.Tx) error {
		ledgerTx, err := movements.Tap(ctx, tx, cardholder, merchant, amount, fee, tap)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO card_taps (id, card_id, cardholder_id, merchant_id, currency,
			                       amount_minor, fee_minor, tier, funding_source, funded_ngn_minor, ledger_tx_id, nonce)
			VALUES ($1, $2, $3, $4, 'NGN', $5, $6, 'none', 'ngn', $5, $7, $8)`,
			tap, uuid.New(), cardholder, merchant, amount.Minor(), fee.Minor(), ledgerTx, uuid.NewString()); err != nil {
			return err
		}
		return Record(ctx, tx, tap, cardholder, merchant, "fintava", owed)
	})
	if err != nil {
		t.Fatalf("naira tap: %v", err)
	}
	return tap, owed
}

func (f *fixture) balance(t *testing.T, owner ledger.Owner, kind string) money.Amount {
	t.Helper()
	b, err := ledger.Balance(context.Background(), f.Pool, owner, kind, money.NGN)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fixture) row(t *testing.T, tap uuid.UUID) *Settlement {
	t.Helper()
	s, err := Get(context.Background(), f.Pool, tap)
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	return s
}

// A merchant with no verified bank account cannot be paid, and a cardholder
// with no wallet at the rail has nothing to pay from. Either refuses the
// tap before it charges anybody.
func TestATapNobodyCanBePaidForIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	merchant, cardholder := uuid.New(), uuid.New()

	// A wallet, but the merchant's account is unverified -- the same as
	// absent: nobody has proved the number.
	f.wallet(t, cardholder, "cust-1")
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO merchant_bank_accounts (id, currency, bank_code, account_number, account_name,
		                                    verified_at, sender_profile_merchant_bank_account)
		VALUES ($1, 'NGN', '058', '0123456789', 'TYPED BY HAND', NULL, $2)`, uuid.New(), merchant); err != nil {
		t.Fatal(err)
	}
	err := movements.InTx(ctx, f.Pool, func(tx pgx.Tx) error {
		return Record(ctx, tx, uuid.New(), cardholder, merchant, "fintava", money.Naira(100))
	})
	if !errors.Is(err, ErrNoBankAccount) {
		t.Fatalf("Record = %v, want ErrNoBankAccount", err)
	}

	// A verified merchant, but a cardholder with no wallet -- or one on a
	// rail this deployment no longer pays from, or one whose customer id
	// was never recorded.
	f.verifiedBank(t, merchant)
	for _, c := range []struct {
		name       string
		cardholder uuid.UUID
		rail       string
	}{
		{"no wallet", uuid.New(), "fintava"},
		{"wallet on another rail", cardholder, "safehaven"},
		{"no rail configured", cardholder, ""},
	} {
		err := movements.InTx(ctx, f.Pool, func(tx pgx.Tx) error {
			return Record(ctx, tx, uuid.New(), c.cardholder, merchant, c.rail, money.Naira(100))
		})
		if !errors.Is(err, ErrNoWallet) {
			t.Errorf("%s: Record = %v, want ErrNoWallet", c.name, err)
		}
	}
	noID := uuid.New()
	f.wallet(t, noID, "")
	err = movements.InTx(ctx, f.Pool, func(tx pgx.Tx) error {
		return Record(ctx, tx, uuid.New(), noID, merchant, "fintava", money.Naira(100))
	})
	if !errors.Is(err, ErrNoWallet) {
		t.Errorf("no customer id: Record = %v, want ErrNoWallet", err)
	}
}

// The worker pays a queued settlement exactly once, under the tap's own
// reference, and the rail's synchronous confirmation settles it: the
// merchant's claim leaves the books, and a second tick sends nothing.
func TestAQueuedSettlementIsPaidOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	merchant := uuid.New()
	f.verifiedBank(t, merchant)
	tap, owed := f.nairaTap(t, merchant)

	s := f.row(t, tap)
	if s.State != Queued || s.Reference != "tap-"+tap.String()+"-ngn" || s.AccountName != "OLUMIDE SILAS OGUNDELE" ||
		s.SourceCustomerID != "cust-"+s.CardholderID.String()[:8] || s.SourceAccountNumber == "" {
		t.Fatalf("recorded row = %+v", s)
	}
	if got := f.balance(t, ledger.Merchant(merchant), ledger.KindMerchantPayable); got.Minor() != owed.Minor() {
		t.Fatalf("merchant owed %s before payout, want %s", got, owed)
	}

	paid, _, err := f.Worker.Tick(ctx)
	if err != nil || paid != 1 {
		t.Fatalf("Tick: paid=%d err=%v", paid, err)
	}
	if f.Rail.sent() != 1 {
		t.Fatalf("transfers = %+v, want one", f.Rail.transfers)
	}
	sent := f.Rail.transfers[0]
	if sent.PaymentReference != s.Reference || sent.SourceID != s.SourceCustomerID ||
		sent.BeneficiaryAccount != "9034409271" || sent.BeneficiaryBankCode != "OPAYNGPC" ||
		sent.BeneficiaryName != "OLUMIDE SILAS OGUNDELE" || sent.Narration != "Tapp: Mama Put" {
		t.Fatalf("rail was asked %+v; want the tap's reference, from the cardholder's wallet, to the merchant's verified bank", sent)
	}
	if sent.Amount.String() != "1592" {
		t.Errorf("sent %s, want 1592 (the tap less the fee)", sent.Amount)
	}

	s = f.row(t, tap)
	if s.State != Settled || s.Attempts != 1 || s.RailRef != "ftv-"+s.Reference || s.SettledAt == nil {
		t.Fatalf("row after payout = %+v, want settled", s)
	}
	if got := f.balance(t, ledger.Merchant(merchant), ledger.KindMerchantPayable); !got.IsZero() {
		t.Errorf("merchant still owed %s after settlement", got)
	}
	if paid, _ := PaidFromWallet(ctx, f.Pool, s.SourceAccountNumber); paid.Minor() != owed.Minor() {
		t.Errorf("PaidFromWallet = %s, want %s", paid, owed)
	}

	// Nothing left to pay.
	paid, _, err = f.Worker.Tick(ctx)
	if err != nil || paid != 0 || f.Rail.sent() != 1 {
		t.Fatalf("second Tick: paid=%d sent=%d err=%v", paid, f.Rail.sent(), err)
	}
}

// A rail that accepts the transfer but does not confirm it leaves the row
// submitted, with the claim reserved. The webhook settles it -- once,
// however many times it is delivered.
func TestAPendingSettlementIsSettledByTheWebhook(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	merchant := uuid.New()
	f.verifiedBank(t, merchant)
	tap, owed := f.nairaTap(t, merchant)
	f.Rail.status = baas.TransferPending

	if paid, _, err := f.Worker.Tick(ctx); err != nil || paid != 1 {
		t.Fatalf("Tick: paid=%d err=%v", paid, err)
	}
	s := f.row(t, tap)
	if s.State != Submitted || s.RailRef == "" {
		t.Fatalf("row = %+v, want submitted with the rail's reference", s)
	}
	// Discharged on submission, as the on-chain leg is: the books say the
	// cardholder's wallet has paid it.
	if got := f.balance(t, ledger.Merchant(merchant), ledger.KindMerchantPayable); !got.IsZero() {
		t.Fatalf("merchant owed %s while in flight, want the claim discharged", got)
	}
	// And it counts as having left the wallet, so reconciliation adds it
	// back to the balance the rail reports.
	if paid, _ := PaidFromWallet(ctx, f.Pool, s.SourceAccountNumber); paid.Minor() != owed.Minor() {
		t.Errorf("PaidFromWallet while in flight = %s, want %s", paid, owed)
	}

	ev := &baas.WebhookEvent{
		Type: "customer_bank_transfer", PaymentReference: s.Reference, ProviderRef: s.RailRef,
		Status: baas.TransferSuccess, RawStatus: "SUCCESS",
	}
	for i := 0; i < 3; i++ {
		ours, err := f.Worker.ApplyWebhook(ctx, ev)
		if err != nil || !ours {
			t.Fatalf("delivery %d: ours=%v err=%v", i, ours, err)
		}
	}
	if s := f.row(t, tap); s.State != Settled {
		t.Fatalf("row = %+v, want settled", s)
	}
	if got := f.balance(t, ledger.Merchant(merchant), ledger.KindMerchantPayable); !got.IsZero() {
		t.Errorf("merchant owed %s after three confirmations", got)
	}

	// Not ours: another flow's reference is left alone.
	if ours, _ := f.Worker.ApplyWebhook(ctx, &baas.WebhookEvent{PaymentReference: "lpwd-" + uuid.NewString(), Status: baas.TransferSuccess}); ours {
		t.Error("claimed a withdrawal's webhook")
	}
}

// A refusal is terminal: the row fails with the rail's words, the merchant
// is owed again, and nothing retries it until an operator does. The retry
// pays under the same reference.
func TestARefusedSettlementStaysFailedUntilRetried(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	merchant := uuid.New()
	f.verifiedBank(t, merchant)
	tap, owed := f.nairaTap(t, merchant)
	f.Rail.transferErr = &refusal{"fintava: http 400: insufficient wallet balance"}

	if paid, _, err := f.Worker.Tick(ctx); err != nil || paid != 0 {
		t.Fatalf("Tick: paid=%d err=%v", paid, err)
	}
	s := f.row(t, tap)
	if s.State != Failed || s.Error != "fintava: http 400: insufficient wallet balance" || s.Attempts != 1 {
		t.Fatalf("row = %+v, want failed with the rail's message", s)
	}
	if got := f.balance(t, ledger.Merchant(merchant), ledger.KindMerchantPayable); got.Minor() != owed.Minor() {
		t.Errorf("merchant owed %s after a refusal, want %s back", got, owed)
	}
	if paid, _ := PaidFromWallet(ctx, f.Pool, s.SourceAccountNumber); !paid.IsZero() {
		t.Errorf("PaidFromWallet after a refusal = %s, want nothing (the money is still in the wallet)", paid)
	}

	// Ticks do not retry on their own.
	for i := 0; i < 3; i++ {
		if _, _, err := f.Worker.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.Rail.sent() != 1 {
		t.Fatalf("the rail was asked %d times; a failed settlement must wait for an operator", f.Rail.sent())
	}

	// A settled or queued row cannot be retried, only a failed one.
	f.Rail.transferErr = nil
	retried, err := f.Worker.Retry(ctx, tap)
	if err != nil || retried.State != Queued {
		t.Fatalf("Retry: %+v %v", retried, err)
	}
	if _, err := f.Worker.Retry(ctx, tap); !errors.Is(err, ErrNotFailed) {
		t.Fatalf("second Retry = %v, want ErrNotFailed", err)
	}
	if paid, _, err := f.Worker.Tick(ctx); err != nil || paid != 1 {
		t.Fatalf("Tick after retry: paid=%d err=%v", paid, err)
	}
	if f.Rail.sent() != 2 || f.Rail.transfers[1].PaymentReference != f.Rail.transfers[0].PaymentReference {
		t.Fatalf("retry sent under %q, want the same reference %q", f.Rail.transfers[1].PaymentReference, f.Rail.transfers[0].PaymentReference)
	}
	s = f.row(t, tap)
	if s.State != Settled || s.Attempts != 2 {
		t.Fatalf("row = %+v, want settled on attempt 2", s)
	}
	if got := f.balance(t, ledger.Merchant(merchant), ledger.KindMerchantPayable); !got.IsZero() {
		t.Errorf("merchant owed %s after the retry settled", got)
	}
}

// A submitted settlement the rail has no record of, and that never got a
// reference from it, is failed by the chase -- not sent again.
func TestAnUnansweredSettlementIsChasedNotResent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	merchant := uuid.New()
	f.verifiedBank(t, merchant)
	tap, owed := f.nairaTap(t, merchant)
	f.Rail.transferErr = errors.New("fintava: POST /bank/credit: connection reset")

	if _, _, err := f.Worker.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	s := f.row(t, tap)
	if s.State != Submitted || s.RailRef != "" || s.Error == "" {
		t.Fatalf("row = %+v, want submitted with the error and no rail reference", s)
	}
	if got := f.balance(t, ledger.Merchant(merchant), ledger.KindMerchantPayable); !got.IsZero() {
		t.Fatalf("merchant owed %s while unknown, want nothing returned until the rail says", got)
	}

	// Not stale yet: nothing happens.
	if _, chased, err := f.Worker.Tick(ctx); err != nil || chased != 0 {
		t.Fatalf("early chase: %d %v", chased, err)
	}
	f.Worker.Now = func() time.Time { return time.Now().Add(StaleAfter + time.Second) }
	if _, chased, err := f.Worker.Tick(ctx); err != nil || chased != 1 {
		t.Fatalf("chase: %d %v", chased, err)
	}
	s = f.row(t, tap)
	if s.State != Failed {
		t.Fatalf("row = %+v, want failed", s)
	}
	if f.Rail.sent() != 1 {
		t.Fatalf("sent %d times; an unanswered transfer must not be resent", f.Rail.sent())
	}
	if got := f.balance(t, ledger.Merchant(merchant), ledger.KindMerchantPayable); got.Minor() != owed.Minor() {
		t.Errorf("merchant owed %s, want %s back", got, owed)
	}
}

// A rail that cannot pay out of a customer wallet pays nothing: rows queue
// rather than being sent from the platform's own account.
func TestARailWithoutWalletsPaysNothing(t *testing.T) {
	f := newFixture(t)
	merchant := uuid.New()
	f.verifiedBank(t, merchant)
	tap, _ := f.nairaTap(t, merchant)

	w := &Worker{Pool: f.Pool, Rail: pooledRail{f.Rail}}
	if _, _, err := w.Tick(context.Background()); !errors.Is(err, ErrNoRail) {
		t.Fatalf("Tick = %v, want ErrNoRail", err)
	}
	if s := f.row(t, tap); s.State != Queued {
		t.Fatalf("row = %+v, want still queued", s)
	}
}

// pooledRail is a provider with no wallets to pay from.
type pooledRail struct{ baas.Provider }
