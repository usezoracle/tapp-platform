package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/baas"
	"github.com/usezoracle/tapp/api/storage"
)

// ngnTestPool points the package's storage at the test database for the
// duration of one test. The ngn deposit code reads storage.Pool directly.
func ngnTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := equityTestPool(t)
	prev := storage.Pool
	storage.Pool = pool
	t.Cleanup(func() { storage.Pool = prev })
	return pool
}

// withBankFallback sets the configured partner bank for one test.
func withBankFallback(t *testing.T, name, code string) {
	t.Helper()
	prev := ngnBankFallback
	ngnBankFallback = func() (string, string) { return name, code }
	t.Cleanup(func() { ngnBankFallback = prev })
}

// newNGNAccount inserts a user and a deposit account row the way the first
// version of the feature did: placeholder bank, customer id in rail_ref,
// and no wallet id.
func newNGNAccount(t *testing.T, pool *pgxpool.Pool, bankName string) (user uuid.UUID, account, email string) {
	t.Helper()
	ctx := context.Background()
	user = uuid.New()
	email = user.String() + "@test.local"
	account = "1" + user.String()[:9]
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, first_name, last_name, email) VALUES ($1, 'Ada', 'O', $2)`,
		user, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ngn_deposit_accounts (user_id, rail, account_number, bank_name, account_name, rail_ref)
		VALUES ($1, 'fintava', $2, $3, 'ADA O', 'cust-1')`, user, account, bankName); err != nil {
		t.Fatal(err)
	}
	return user, account, email
}

// A row still carrying the placeholder shows the configured bank to the
// person reading it, code included; a row with a real bank is left alone.
func TestReadSubstitutesConfiguredBankForPlaceholder(t *testing.T) {
	pool := ngnTestPool(t)
	withBankFallback(t, "Loma Microfinance Bank", "090620")

	user, _, _ := newNGNAccount(t, pool, legacyBankPlaceholder)
	got, err := loadNGNAccount(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if got.BankName != "Loma Microfinance Bank" || got.BankCode != "090620" {
		t.Fatalf("read back bank %q/%q", got.BankName, got.BankCode)
	}

	real, _, _ := newNGNAccount(t, pool, "Iyin-Ekiti Microfinance Bank")
	got, err = loadNGNAccount(context.Background(), real)
	if err != nil {
		t.Fatal(err)
	}
	if got.BankName != "Iyin-Ekiti Microfinance Bank" {
		t.Fatalf("a real bank was overwritten: %q", got.BankName)
	}
	// The configured code is Loma's; it must not be shown against another
	// bank's name.
	if got.BankCode != "" {
		t.Fatalf("code = %q, want none", got.BankCode)
	}

	// A row naming the configured bank without its code gets the code.
	same, _, _ := newNGNAccount(t, pool, "loma microfinance bank")
	got, err = loadNGNAccount(context.Background(), same)
	if err != nil {
		t.Fatal(err)
	}
	if got.BankCode != "090620" {
		t.Fatalf("code = %q, want 090620", got.BankCode)
	}
}

// With nothing configured, the placeholder still never reaches a person.
func TestReadNeverShowsPlaceholder(t *testing.T) {
	pool := ngnTestPool(t)
	withBankFallback(t, "", "")
	user, _, _ := newNGNAccount(t, pool, legacyBankPlaceholder)
	got, err := loadNGNAccount(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if got.BankName != "" {
		t.Fatalf("bank = %q", got.BankName)
	}
}

// A rail response with no bank is stored as the configured bank, and the
// wallet id is kept alongside the customer id.
func TestSaveFallsBackToConfiguredBankAndKeepsWalletID(t *testing.T) {
	pool := ngnTestPool(t)
	withBankFallback(t, "Loma Microfinance Bank", "090620")
	ctx := context.Background()
	user := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email) VALUES ($1, $2)`, user, user.String()+"@t.local"); err != nil {
		t.Fatal(err)
	}
	got, err := saveNGNAccount(ctx, user, "fintava", &baas.Account{
		ID: "cust-9", WalletID: "wal-9", AccountNumber: "9" + user.String()[:9], AccountName: "ADA O",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.BankName != "Loma Microfinance Bank" || got.BankCode != "090620" {
		t.Fatalf("stored bank %q/%q", got.BankName, got.BankCode)
	}
	row, err := NGNAccountByNumber(ctx, got.AccountNumber)
	if err != nil {
		t.Fatal(err)
	}
	if row.RailRef != "cust-9" || row.WalletID != "wal-9" || row.BankName != "Loma Microfinance Bank" || row.NeedsBankFix {
		t.Fatalf("row = %+v", row)
	}
}

// The operator's correction is stored as given and read back as given.
func TestSetBankCorrectsRow(t *testing.T) {
	pool := ngnTestPool(t)
	withBankFallback(t, "", "")
	user, account, email := newNGNAccount(t, pool, legacyBankPlaceholder)
	ctx := context.Background()

	rows, err := NGNAccountsByEmail(ctx, "  "+email+"  ")
	if err != nil || len(rows) != 1 || rows[0].AccountNumber != account || !rows[0].NeedsBankFix {
		t.Fatalf("by email: %v %+v", err, rows)
	}

	if _, err := SetNGNDepositBank(ctx, account, legacyBankPlaceholder, ""); err == nil {
		t.Fatal("the placeholder must not be storable")
	}
	row, err := SetNGNDepositBank(ctx, account, "Loma Microfinance Bank", "090620")
	if err != nil {
		t.Fatal(err)
	}
	if row.BankName != "Loma Microfinance Bank" || row.BankCode != "090620" || row.NeedsBankFix {
		t.Fatalf("row = %+v", row)
	}
	got, _ := loadNGNAccount(ctx, user)
	if got.BankName != "Loma Microfinance Bank" || got.BankCode != "090620" {
		t.Fatalf("read back %q/%q", got.BankName, got.BankCode)
	}
	if _, err := SetNGNDepositBank(ctx, "0000000000", "X Bank", ""); !errors.Is(err, ErrNGNAccountNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

// stubRail is a Fintava that holds one wallet. Every Provider method not
// listed here panics through the nil embedded interface, which is the point:
// reconciliation must need nothing but a balance read and a wallet lookup.
type stubRail struct {
	baas.Provider
	walletID string
	balance  decimal.Decimal
	located  int
	read     []string
}

func (s *stubRail) Name() string { return "fintava" }
func (s *stubRail) GetAccount(_ context.Context, id string) (*baas.Account, error) {
	s.read = append(s.read, id)
	if id != s.walletID {
		return nil, errors.New("no such wallet")
	}
	return &baas.Account{ID: id, Balance: s.balance, Currency: "NGN"}, nil
}
func (s *stubRail) LocateWallet(_ context.Context, email, account string) (string, error) {
	s.located++
	return s.walletID, nil
}

func creditedNGN(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) int64 {
	t.Helper()
	var minor int64
	if err := pool.QueryRow(context.Background(), `
		SELECT coalesce(sum(e.amount_minor),0) FROM ledger_entries e
		  JOIN ledger_accounts a ON a.id = e.account_id
		 WHERE a.owner_id = $1 AND a.kind = 'available' AND a.currency = 'NGN'`, user).Scan(&minor); err != nil {
		t.Fatal(err)
	}
	return minor
}

// The ₦100 that arrived before the webhook was forwarded: the wallet holds
// it, the ledger does not, and one run posts exactly the difference. A
// second run posts nothing.
func TestReconcilePostsShortfallOnce(t *testing.T) {
	pool := ngnTestPool(t)
	user, account, _ := newNGNAccount(t, pool, legacyBankPlaceholder)
	rail := &stubRail{walletID: "wal-1", balance: decimal.RequireFromString("100.00")}
	ctx := context.Background()

	res, err := ReconcileNGNDeposit(ctx, rail, account)
	if err != nil {
		t.Fatal(err)
	}
	if res.WalletBalance.Minor() != 10_000 || res.CreditedBefore.Minor() != 0 || res.Posted.Minor() != 10_000 {
		t.Fatalf("first run = %+v", res)
	}
	if res.Reference == "" || res.Reference[:len("reconcile:"+account+":10000:")] != "reconcile:"+account+":10000:" {
		t.Fatalf("reference = %q", res.Reference)
	}
	if got := creditedNGN(t, pool, user); got != 10_000 {
		t.Fatalf("ledger credited %d kobo, want 10000", got)
	}
	// The wallet id was found by account number and written to the row, so
	// the next run does not have to look it up again.
	if rail.located != 1 || res.WalletID != "wal-1" {
		t.Fatalf("located %d times, wallet %q", rail.located, res.WalletID)
	}
	row, _ := NGNAccountByNumber(ctx, account)
	if row.WalletID != "wal-1" {
		t.Fatalf("wallet id not recorded: %+v", row)
	}

	again, err := ReconcileNGNDeposit(ctx, rail, account)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Posted.IsZero() || again.CreditedBefore.Minor() != 10_000 || again.Reference != "" {
		t.Fatalf("second run = %+v", again)
	}
	if rail.located != 1 {
		t.Fatalf("looked the wallet up again (%d)", rail.located)
	}
	if got := creditedNGN(t, pool, user); got != 10_000 {
		t.Fatalf("second run changed the ledger: %d", got)
	}
}

// Credits the webhook DID deliver count: only what is missing is posted.
// And a wallet holding less than the ledger says is reported, not debited.
func TestReconcileCountsWebhookCreditsAndNeverDebits(t *testing.T) {
	pool := ngnTestPool(t)
	user, account, _ := newNGNAccount(t, pool, "Loma Microfinance Bank")
	ctx := context.Background()
	if ours, err := CreditNGNDeposit(ctx, account, "40.00", "fintava", "FTV-"+account); err != nil || !ours {
		t.Fatalf("webhook credit: %v %v", ours, err)
	}
	rail := &stubRail{walletID: "wal-2", balance: decimal.RequireFromString("100")}

	res, err := ReconcileNGNDeposit(ctx, rail, account)
	if err != nil {
		t.Fatal(err)
	}
	if res.CreditedBefore.Minor() != 4_000 || res.Posted.Minor() != 6_000 {
		t.Fatalf("run = %+v", res)
	}
	if got := creditedNGN(t, pool, user); got != 10_000 {
		t.Fatalf("ledger = %d", got)
	}

	rail.balance = decimal.RequireFromString("25")
	res, err = ReconcileNGNDeposit(ctx, rail, account)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Posted.IsZero() || res.Note == "" || res.Reference != "" {
		t.Fatalf("smaller balance = %+v", res)
	}
	if got := creditedNGN(t, pool, user); got != 10_000 {
		t.Fatalf("a smaller balance changed the ledger: %d", got)
	}
	if _, err := ReconcileNGNDeposit(ctx, rail, "0000000000"); !errors.Is(err, ErrNGNAccountNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	var _ money.Amount = res.Posted
}
