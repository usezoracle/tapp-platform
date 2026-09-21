package base

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	mrand "math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
)

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

// fakeSmartAccounts stands in for CDP: deterministic per user and idempotent
// by user, which is the only contract the real one promises.
type fakeSmartAccounts struct{ calls int }

func (f *fakeSmartAccounts) EnsureSmartAccount(_ context.Context, user uuid.UUID) (SmartAccount, error) {
	f.calls++
	b := user[:]
	return SmartAccount{
		Address: common.BytesToAddress(b).Hex(),
		Owner:   common.BytesToAddress(append([]byte{0x01}, b[:15]...)).Hex(),
		Name:    "tapp-deposit-" + user.String()[:8],
	}, nil
}

func (f *fakeSmartAccounts) SweepSmartAccount(context.Context, string, common.Address,
	common.Address, *big.Int, string) (string, error) {
	return "0x" + strings.Repeat("f", 64), nil
}

// txHash returns a tx hash no other test run has used.
//
// These tests run against a persistent database, and (tx_hash, log_index) is
// the deposit idempotency key. Hashes built from a couple of random characters
// collide with rows left by earlier runs, and the insert then does nothing --
// so the test reads as "the deposit was not credited" when what actually
// happened is that it was never recorded.
func txHash(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "0x" + hex.EncodeToString(b)
}

func fixture(t *testing.T) (*Addresses, *Deposits) {
	t.Helper()
	pool := testPool(t)
	addrs := &Addresses{Pool: pool, Deriver: deriver(t), SmartAccounts: &fakeSmartAccounts{}}
	return addrs, &Deposits{Pool: pool, Addresses: addrs, Confirmations: 12}
}

func TestEachUserGetsTheirOwnStableAddress(t *testing.T) {
	addrs, _ := fixture(t)
	ctx := context.Background()

	alice, bob := uuid.New(), uuid.New()

	a1, err := addrs.For(ctx, alice)
	if err != nil {
		t.Fatalf("For(alice): %v", err)
	}
	a2, err := addrs.For(ctx, alice)
	if err != nil {
		t.Fatalf("For(alice) again: %v", err)
	}
	if a1 != a2 {
		t.Errorf("alice got two addresses: %s then %s", a1, a2)
	}

	b, err := addrs.For(ctx, bob)
	if err != nil {
		t.Fatalf("For(bob): %v", err)
	}
	if b == a1 {
		t.Fatal("two users share a deposit address; their deposits are indistinguishable")
	}
}

// The check that catches a changed seed.
//
// The seed no longer issues addresses, but it still has to SPEND the retired
// ones, and this is what stops the sweeper signing for an address the current
// seed does not produce -- which would be a transaction that fails, recorded
// as a sweep, losing the deposit from view.
func TestAnAddressFromADifferentSeedIsRefused(t *testing.T) {
	addrs, _ := fixture(t)

	mine, err := addrs.Deriver.Address(7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := addrs.verify(mine.Hex(), 7); err != nil {
		t.Fatalf("verify rejected our own address: %v", err)
	}

	otherSeed, _ := ParseSeed(strings.Repeat("cd", SeedLen))
	other, err := NewDeriver(otherSeed)
	if err != nil {
		t.Fatal(err)
	}
	addrs.Deriver = other

	if _, err := addrs.verify(mine.Hex(), 7); !errors.Is(err, ErrAddressMismatch) {
		t.Fatalf("got %v, want ErrAddressMismatch", err)
	}
}

// Addresses come from CDP now. Falling back to the seed when CDP is missing is
// how a deployment quietly goes on minting addresses under the scheme it was
// migrated off -- nobody notices, because a derived address works perfectly
// until the day the seed has to be produced.
func TestWithoutCDPNoAddressIsIssued(t *testing.T) {
	addrs, _ := fixture(t)
	addrs.SmartAccounts = nil

	if _, err := addrs.For(context.Background(), uuid.New()); !errors.Is(err, ErrNoSmartAccounts) {
		t.Fatalf("got %v, want ErrNoSmartAccounts", err)
	}
}

// A user still holding a seed-derived address is moved across on the next
// read, so a row the migration missed heals itself rather than persisting.
func TestALegacyDerivedAddressIsRetiredAndReissued(t *testing.T) {
	addrs, _ := fixture(t)
	ctx := context.Background()
	user := uuid.New()

	// A fresh index each run: the column is unique table-wide, and retired
	// rows accumulate, so a literal would collide with the previous run.
	index := uint32(1_000_000 + mrand.IntN(1_000_000))
	legacy, err := addrs.Deriver.Address(index)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.ToLower(legacy.Hex())
	if _, err := addrs.Pool.Exec(ctx, `
		INSERT INTO base_deposit_addresses (user_id, provider, index, address)
		VALUES ($1, 'derived', $2, $3)`, user, index, old); err != nil {
		t.Fatal(err)
	}

	fresh, err := addrs.For(ctx, user)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if strings.EqualFold(fresh, old) {
		t.Fatal("the derived address is still being handed out")
	}

	var provider string
	if err := addrs.Pool.QueryRow(ctx,
		`SELECT provider FROM base_deposit_addresses WHERE user_id = $1 AND retired_at IS NULL`,
		user).Scan(&provider); err != nil {
		t.Fatalf("read current address: %v", err)
	}
	if provider != ProviderCDP {
		t.Errorf("current provider = %q, want %q", provider, ProviderCDP)
	}

	// Retired, not deleted. The row is what keeps the address watched.
	var retired *time.Time
	if err := addrs.Pool.QueryRow(ctx,
		`SELECT retired_at FROM base_deposit_addresses WHERE address = $1`, old).
		Scan(&retired); err != nil {
		t.Fatalf("the old address row is gone, so it is no longer watched: %v", err)
	}
	if retired == nil {
		t.Error("the old address was left current")
	}
}

// The invariant the whole retire-don't-delete design exists for: somebody who
// saved an old address as a payee and pays into it must still be credited.
func TestARetiredAddressStillCredits(t *testing.T) {
	addrs, deposits := fixture(t)
	ctx := context.Background()
	user := uuid.New()

	old, err := addrs.For(ctx, user)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if err := addrs.retire(ctx, old); err != nil {
		t.Fatalf("retire: %v", err)
	}

	if err := deposits.Record(ctx, Transfer{
		TxHash: txHash(t), LogIndex: 0, From: "0xsender", To: old,
		AmountMicro: 7_000_000, BlockNumber: 100,
	}); err != nil {
		t.Fatalf("a deposit to a retired address was refused: %v", err)
	}
	if credited, err := deposits.CreditConfirmed(ctx, 200); err != nil || credited != 1 {
		t.Fatalf("credited=%d err=%v, want 1 -- a retired address stopped crediting", credited, err)
	}

	b, err := ledger.Balance(ctx, deposits.Pool, ledger.User(user), ledger.KindAvailable, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	if b.Minor() != 700 {
		t.Errorf("balance = %s, want $7.00", b)
	}
}

// The chain's own identity for an event is the idempotency key, which is what
// makes a restarted watcher safe.
func TestReScanningABlockDoesNotCreditTwice(t *testing.T) {
	addrs, deposits := fixture(t)
	ctx := context.Background()
	user := uuid.New()

	address, err := addrs.For(ctx, user)
	if err != nil {
		t.Fatalf("For: %v", err)
	}

	transfer := Transfer{
		TxHash:   txHash(t),
		LogIndex: 3, From: "0xsender", To: address,
		AmountMicro: 10_000_000, BlockNumber: 100, // $10
	}

	// Seen three times, as a restarted watcher would.
	for i := 0; i < 3; i++ {
		if err := deposits.Record(ctx, transfer); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	credited, err := deposits.CreditConfirmed(ctx, 200)
	if err != nil {
		t.Fatalf("CreditConfirmed: %v", err)
	}
	if credited != 1 {
		t.Errorf("credited %d times, want once", credited)
	}

	balance, err := ledger.Balance(ctx, deposits.Pool, ledger.User(user), ledger.KindAvailable, money.USD)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if balance.Minor() != 1000 {
		t.Errorf("balance = %s, want $10.00", balance)
	}

	// And crediting again finds nothing to do.
	if again, _ := deposits.CreditConfirmed(ctx, 300); again != 0 {
		t.Errorf("a second pass credited %d more", again)
	}
}

// Base can reorg. Crediting on first sighting would mean crediting deposits
// that later never happened, by which time the money is spent.
func TestADepositIsNotCreditedUntilConfirmed(t *testing.T) {
	addrs, deposits := fixture(t)
	ctx := context.Background()
	user := uuid.New()

	address, _ := addrs.For(ctx, user)
	if err := deposits.Record(ctx, Transfer{
		TxHash:   txHash(t),
		LogIndex: 0, From: "0xsender", To: address,
		AmountMicro: 5_000_000, BlockNumber: 100,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Only 5 blocks on top: not enough.
	if credited, _ := deposits.CreditConfirmed(ctx, 105); credited != 0 {
		t.Fatalf("credited %d deposits with 5 confirmations, want 0", credited)
	}
	if b, _ := ledger.Balance(ctx, deposits.Pool, ledger.User(user), ledger.KindAvailable, money.USD); !b.IsZero() {
		t.Fatalf("balance = %s before confirmation", b)
	}

	// Twelve is enough.
	if credited, _ := deposits.CreditConfirmed(ctx, 112); credited != 1 {
		t.Fatalf("credited %d deposits with 12 confirmations, want 1", credited)
	}
}

// A transfer to an address nobody owns is not ours and must not error.
func TestATransferToAnUnknownAddressIsIgnored(t *testing.T) {
	_, deposits := fixture(t)
	err := deposits.Record(context.Background(), Transfer{
		TxHash: txHash(t), LogIndex: 0,
		From: "0xsender", To: "0x" + strings.Repeat("9", 40),
		AmountMicro: 1_000_000, BlockNumber: 10,
	})
	if err != nil {
		t.Fatalf("a transfer to somebody else's address errored: %v", err)
	}
}

// USDC has six decimals, the ledger has two. Truncating rather than rounding:
// rounding up would credit a cent that never arrived.
func TestSubCentAmountsAreNotRoundedUp(t *testing.T) {
	for micro, wantCents := range map[int64]int64{
		10_000_000: 1000, // $10.00
		1_234_567:  123,  // $1.234567 -> $1.23, not $1.24
		9_999:      0,    // under a cent
		10_000:     1,    // exactly a cent
	} {
		if got := usdFromMicro(micro); got.Minor() != wantCents {
			t.Errorf("%d micro -> %s, want %d cents", micro, got, wantCents)
		}
	}
}

// A sub-cent deposit cannot be credited and must not be retried forever.
func TestASubCentDepositIsMarkedRatherThanRetried(t *testing.T) {
	addrs, deposits := fixture(t)
	ctx := context.Background()
	user := uuid.New()

	address, _ := addrs.For(ctx, user)
	hash := txHash(t)
	if err := deposits.Record(ctx, Transfer{
		TxHash: hash, LogIndex: 0, From: "0xsender", To: address,
		AmountMicro: 500, BlockNumber: 100, // half a cent
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if credited, err := deposits.CreditConfirmed(ctx, 200); err != nil || credited != 0 {
		t.Fatalf("credited=%d err=%v, want 0 and no error", credited, err)
	}

	var state string
	if err := deposits.Pool.QueryRow(ctx,
		`SELECT state FROM base_deposits WHERE tx_hash = $1`, strings.ToLower(hash)).
		Scan(&state); err != nil {
		t.Fatalf("read deposit: %v", err)
	}
	if state != "failed" {
		t.Errorf("state = %q, want failed so it is not retried every pass", state)
	}
}

// Money sent back from the treasury is not a deposit. Returning funds swept
// before the platform went non-custodial credited them a second time -- the
// same money counted once when it arrived and again when it was given back.
func TestMoneyReturnedFromTheTreasuryIsNotADeposit(t *testing.T) {
	addrs, deposits := fixture(t)
	ctx := context.Background()
	user := uuid.New()

	treasury := common.HexToAddress("0x1232c53d0e537e275E70C401AAB7e9E7E97E57C5")
	deposits.Treasury = treasury

	address, err := addrs.For(ctx, user)
	if err != nil {
		t.Fatal(err)
	}

	// A stranger paying in still credits.
	if err := deposits.Record(ctx, Transfer{
		TxHash: txHash(t), LogIndex: 0, From: "0x00000000000000000000000000000000000000A1",
		To: address, AmountMicro: 1_000_000, BlockNumber: 100,
	}); err != nil {
		t.Fatal(err)
	}
	// The same amount coming back from the treasury does not.
	if err := deposits.Record(ctx, Transfer{
		TxHash: txHash(t), LogIndex: 0, From: treasury.Hex(),
		To: address, AmountMicro: 1_000_000, BlockNumber: 101,
	}); err != nil {
		t.Fatal(err)
	}

	if credited, err := deposits.CreditConfirmed(ctx, 200); err != nil {
		t.Fatalf("CreditConfirmed: %v", err)
	} else if credited != 1 {
		t.Fatalf("credited %d deposits, want 1 -- the return was counted as money arriving", credited)
	}

	b, err := ledger.Balance(ctx, deposits.Pool, ledger.User(user), ledger.KindAvailable, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	if b.Minor() != 100 {
		t.Errorf("balance = %s, want $1.00 -- the same money was credited twice", b)
	}
}

// A settlement order nobody filled is refunded by the Gateway to the account
// that funded it. That is the cardholder's own USDC coming back, for a tap the
// ledger has already charged, and crediting it would give them a second
// balance for the same money.
func TestARefundFromTheGatewayIsNotADeposit(t *testing.T) {
	addrs, deposits := fixture(t)
	ctx := context.Background()
	user := uuid.New()

	gateway := common.HexToAddress("0x30F6A8457F8E42371E204a9c103f2Bd42341dD0F")
	deposits.Gateway = gateway

	address, err := addrs.For(ctx, user)
	if err != nil {
		t.Fatal(err)
	}

	if err := deposits.Record(ctx, Transfer{
		TxHash: txHash(t), LogIndex: 0, From: "0x00000000000000000000000000000000000000A1",
		To: address, AmountMicro: 1_208_679, BlockNumber: 100,
	}); err != nil {
		t.Fatal(err)
	}
	// The order's refund, as the Gateway sends it: lower-cased, the way an
	// RPC prints addresses, against a checksummed configured value.
	if err := deposits.Record(ctx, Transfer{
		TxHash: txHash(t), LogIndex: 0, From: strings.ToLower(gateway.Hex()),
		To: address, AmountMicro: 1_208_679, BlockNumber: 101,
	}); err != nil {
		t.Fatal(err)
	}

	if credited, err := deposits.CreditConfirmed(ctx, 200); err != nil {
		t.Fatalf("CreditConfirmed: %v", err)
	} else if credited != 1 {
		t.Fatalf("credited %d deposits, want 1 -- the refund was counted as money arriving", credited)
	}

	b, err := ledger.Balance(ctx, deposits.Pool, ledger.User(user), ledger.KindAvailable, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	if b.Minor() != 120 {
		t.Errorf("balance = %s, want $1.20 -- the refund was credited as a deposit", b)
	}
}

// Nothing is swept into the treasury any more, so a withdrawal paid from it
// would fail with nothing to send. It comes out of the person's own deposit
// address instead, sponsored, so withdrawing costs them no gas.
func TestAWithdrawalIsPaidFromTheUsersOwnAccount(t *testing.T) {
	addrs, deposits := fixture(t)
	ctx := context.Background()
	user := uuid.New()

	address, err := addrs.For(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	// Fund them, so the withdrawal has something to debit.
	if err := deposits.Record(ctx, Transfer{
		TxHash: txHash(t), LogIndex: 0, From: "0x00000000000000000000000000000000000000A1",
		To: address, AmountMicro: 5_000_000, BlockNumber: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := deposits.CreditConfirmed(ctx, 200); err != nil {
		t.Fatal(err)
	}

	sender := &recordingSender{}
	w := &Withdrawals{
		Pool: deposits.Pool, Chain: &Chain{USDC: common.HexToAddress("0x8335")},
		Addresses: addrs, SmartAccounts: sender,
	}

	dest := "0x00000000000000000000000000000000000000B2"
	if _, err := w.Open(ctx, Request{
		UserID: user, Amount: money.New(200, money.USD), To: dest,
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Send(ctx); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if sender.from == "" {
		t.Fatal("nothing was sent -- the withdrawal did not reach the smart account")
	}
	if !strings.EqualFold(sender.from, address) {
		t.Errorf("sent from %s, want the user's own deposit address %s", sender.from, address)
	}
	if !strings.EqualFold(sender.to.Hex(), dest) {
		t.Errorf("sent to %s, want %s", sender.to, dest)
	}
	// $2.00 is 2,000,000 in USDC's six decimals.
	if sender.amount == nil || sender.amount.Int64() != 2_000_000 {
		t.Errorf("sent %v, want 2000000 micro-USDC", sender.amount)
	}
	// The withdrawal id keys the operation, so a retry after a lost response
	// cannot send the same money twice.
	if !strings.HasPrefix(sender.idem, "withdrawal:") {
		t.Errorf("idempotency key %q does not identify the withdrawal", sender.idem)
	}
}

type recordingSender struct {
	from   string
	to     common.Address
	amount *big.Int
	idem   string
}

func (r *recordingSender) SweepSmartAccount(
	_ context.Context, account string, _, to common.Address, amount *big.Int, idem string,
) (string, error) {
	r.from, r.to, r.amount, r.idem = account, to, amount, idem
	return "0x" + strings.Repeat("ab", 32), nil
}
