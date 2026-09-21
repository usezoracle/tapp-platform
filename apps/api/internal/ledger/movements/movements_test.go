package movements

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	t.Cleanup(pool.Close)
	return pool
}

// spend runs a movement in its own transaction, the way every caller must.
func spend(t *testing.T, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	t.Helper()
	return InTx(context.Background(), pool, fn)
}

func balance(t *testing.T, pool *pgxpool.Pool, o ledger.Owner, kind string, c money.Currency) money.Amount {
	t.Helper()
	b, err := ledger.Balance(context.Background(), pool, o, kind, c)
	if err != nil {
		t.Fatalf("Balance(%s/%s/%s): %v", o.Kind, kind, c, err)
	}
	return b
}

// System accounts (revenue, fx_position, payable, external) are shared
// singletons: every test in this package posts to the same rows. Asserting an
// absolute balance on one makes the test depend on what every other test did
// first. Measure the change instead.
func delta(t *testing.T, pool *pgxpool.Pool, o ledger.Owner, kind string, c money.Currency, during func()) money.Amount {
	t.Helper()
	before := balance(t, pool, o, kind, c)
	during()
	after := balance(t, pool, o, kind, c)
	d, err := after.Sub(before)
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	return d
}

// The global sum across every account and every currency. Value can only enter
// or leave through `external`, so this must be zero after any sequence of
// movements -- if it is not, value was invented or destroyed.
func globalSums(t *testing.T, pool *pgxpool.Pool) map[money.Currency]int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT currency, COALESCE(SUM(amount_minor), 0) FROM ledger_entries GROUP BY currency`)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	defer rows.Close()

	sums := map[money.Currency]int64{}
	for rows.Next() {
		var c string
		var sum int64
		if err := rows.Scan(&c, &sum); err != nil {
			t.Fatalf("scan: %v", err)
		}
		sums[money.Currency(c)] = sum
	}
	return sums
}

func TestATapMovesValueFromCardholderToMerchant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	cardholder, merchant := uuid.New(), uuid.New()
	amount := money.Naira(2_000)
	fee := money.FeeFor(amount, 50) // 0.5% = ₦10

	if _, err := Deposit(ctx, pool, cardholder, money.Naira(10_000), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	if err := spend(t, pool, func(tx pgx.Tx) error {
		_, e := Tap(ctx, tx, cardholder, merchant, amount, fee, uuid.New())
		return e
	}); err != nil {
		t.Fatalf("Tap: %v", err)
	}

	if got := balance(t, pool, ledger.User(cardholder), ledger.KindAvailable, money.NGN); got.Minor() != 800_000 {
		t.Errorf("cardholder = %s, want ₦8,000.00", got)
	}
	if got := balance(t, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, money.NGN); got.Minor() != 199_000 {
		t.Errorf("merchant owed = %s, want ₦1,990.00", got)
	}
	for c, sum := range globalSums(t, pool) {
		if sum != 0 {
			t.Errorf("%s ledger sums to %d, must be 0", c, sum)
		}
	}
}

// The retry a merchant app actually makes: it cannot tell a lost response from
// a declined one, so it sends the debit again. The cardholder must be charged
// once.
func TestARetriedTapChargesOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	cardholder, merchant := uuid.New(), uuid.New()
	tapID := uuid.New()
	amount := money.Naira(500)

	if _, err := Deposit(ctx, pool, cardholder, money.Naira(1_000), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	tap := func() error {
		return spend(t, pool, func(tx pgx.Tx) error {
			_, e := Tap(ctx, tx, cardholder, merchant, amount, money.Zero(money.NGN), tapID)
			return e
		})
	}
	if err := tap(); err != nil {
		t.Fatalf("first tap: %v", err)
	}
	err := tap()
	if !errors.Is(err, ledger.ErrDuplicate) {
		t.Fatalf("retry returned %v, want ErrDuplicate", err)
	}

	if got := balance(t, pool, ledger.User(cardholder), ledger.KindAvailable, money.NGN); got.Minor() != 50_000 {
		t.Errorf("cardholder = %s, want ₦500.00 -- charged more than once", got)
	}
}

// A reversal is a new opposite movement, not a deletion, and it returns the fee
// too -- keeping a fee on a payment that did not stand charges for a service
// not rendered.
func TestAReversalRestoresTheCardholderIncludingTheFee(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	cardholder, merchant := uuid.New(), uuid.New()
	tapID := uuid.New()
	amount := money.Naira(1_000)
	fee := money.FeeFor(amount, 50)

	if _, err := Deposit(ctx, pool, cardholder, money.Naira(1_000), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	revenueDelta := delta(t, pool, ledger.System(), ledger.KindRevenue, money.NGN, func() {
		if err := spend(t, pool, func(tx pgx.Tx) error {
			_, e := Tap(ctx, tx, cardholder, merchant, amount, fee, tapID)
			return e
		}); err != nil {
			t.Fatalf("Tap: %v", err)
		}
		if _, err := TapReversal(ctx, pool, cardholder, merchant, amount, fee, tapID, "goods_not_supplied"); err != nil {
			t.Fatalf("TapReversal: %v", err)
		}
	})

	if got := balance(t, pool, ledger.User(cardholder), ledger.KindAvailable, money.NGN); got.Minor() != 100_000 {
		t.Errorf("cardholder = %s, want the full ₦1,000.00 back", got)
	}
	if got := balance(t, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, money.NGN); !got.IsZero() {
		t.Errorf("merchant still owed %s after a reversal", got)
	}
	if !revenueDelta.IsZero() {
		t.Errorf("revenue kept %s from a reversed payment", revenueDelta)
	}
}

// The conversion, end to end, with both legs balancing and the exposure
// landing where a treasury can see it.
func TestAConversionBooksTheSpreadAndTheExposure(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := uuid.New()

	if _, err := Deposit(ctx, pool, user, money.Dollars(10), "base", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// $10 at ₦1,540, 0.5% spread: gross ₦15,400, spread ₦77, user gets ₦15,323.
	convert := func() {
		if err := spend(t, pool, func(tx pgx.Tx) error {
			_, e := Convert(ctx, tx, user, Conversion{
				Sold:    money.Dollars(10),
				Bought:  money.New(1_532_300, money.NGN),
				Spread:  money.New(7_700, money.NGN),
				QuoteID: uuid.NewString(),
			})
			return e
		}); err != nil {
			t.Fatalf("Convert: %v", err)
		}
	}

	var spread, posUSD, posNGN money.Amount
	posNGN = delta(t, pool, ledger.System(), ledger.KindFXPosition, money.NGN, func() {
		posUSD = delta(t, pool, ledger.System(), ledger.KindFXPosition, money.USD, func() {
			spread = delta(t, pool, ledger.System(), ledger.KindRevenue, money.NGN, convert)
		})
	})

	if got := balance(t, pool, ledger.User(user), ledger.KindAvailable, money.USD); !got.IsZero() {
		t.Errorf("USD balance = %s, want zero after selling the lot", got)
	}
	if got := balance(t, pool, ledger.User(user), ledger.KindAvailable, money.NGN); got.Minor() != 1_532_300 {
		t.Errorf("NGN balance = %s, want ₦15,323.00", got)
	}
	if spread.Minor() != 7_700 {
		t.Errorf("spread = %s, want ₦77.00", spread)
	}
	// The platform is now long dollars and short naira. That is a real
	// position, and the point of fx_position is that it is visible.
	if posUSD.Minor() != 1_000 {
		t.Errorf("USD position moved %s, want +$10.00", posUSD)
	}
	if posNGN.Minor() != -1_540_000 {
		t.Errorf("NGN position moved %s, want -₦15,400.00", posNGN)
	}

	for c, sum := range globalSums(t, pool) {
		if sum != 0 {
			t.Errorf("%s ledger sums to %d after a conversion, must be 0", c, sum)
		}
	}
}

func TestAConversionRefusesWhatCannotBePriced(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := uuid.New()

	for name, conv := range map[string]Conversion{
		"no quote": {
			Sold: money.Dollars(1), Bought: money.Naira(1500), Spread: money.Zero(money.NGN)},
		"same currency": {
			Sold: money.Naira(1), Bought: money.Naira(1), Spread: money.Zero(money.NGN), QuoteID: "q"},
		"spread in the wrong currency": {
			Sold: money.Dollars(1), Bought: money.Naira(1500), Spread: money.Dollars(1), QuoteID: "q"},
		"negative spread": {
			Sold: money.Dollars(1), Bought: money.Naira(1500), Spread: money.New(-1, money.NGN), QuoteID: "q"},
	} {
		t.Run(name, func(t *testing.T) {
			err := spend(t, pool, func(tx pgx.Tx) error {
				_, e := Convert(ctx, tx, user, conv)
				return e
			})
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// A withdrawal parks value in payable until the provider confirms, and only a
// confirmation discharges it. This is what stops the ledger claiming it paid
// somebody when all that happened was a request being accepted.
func TestValueSitsInPayableUntilTheProviderConfirms(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	user := uuid.New()
	withdrawalID := uuid.New()

	if _, err := Deposit(ctx, pool, user, money.Naira(5_000), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	owed := delta(t, pool, ledger.System(), ledger.KindPayable, money.NGN, func() {
		if err := spend(t, pool, func(tx pgx.Tx) error {
			_, e := Withdraw(ctx, tx, user, money.Naira(5_000), money.Naira(10), withdrawalID)
			return e
		}); err != nil {
			t.Fatalf("Withdraw: %v", err)
		}
	})
	if owed.Minor() != 499_000 {
		t.Errorf("payable rose by %s, want ₦4,990.00 owed and undelivered", owed)
	}

	ref := uuid.NewString()
	discharged := delta(t, pool, ledger.System(), ledger.KindPayable, money.NGN, func() {
		if _, err := Settled(ctx, pool, money.New(499_000, money.NGN), ref); err != nil {
			t.Fatalf("Settled: %v", err)
		}
	})
	if discharged.Minor() != -499_000 {
		t.Errorf("settlement discharged %s, want -₦4,990.00", discharged)
	}

	// A redelivered confirmation must not discharge it twice.
	if _, err := Settled(ctx, pool, money.New(499_000, money.NGN), ref); !errors.Is(err, ledger.ErrDuplicate) {
		t.Errorf("a replayed confirmation returned %v, want ErrDuplicate", err)
	}

	for c, sum := range globalSums(t, pool) {
		if sum != 0 {
			t.Errorf("%s ledger sums to %d, must be 0", c, sum)
		}
	}
}

// A refused destination returns the money to the person now holding nothing,
// rather than leaving it in payable pretending to still be on its way.
func TestARefusedWithdrawalGoesBackToTheSender(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	user := uuid.New()
	withdrawalID := uuid.New()

	if _, err := Deposit(ctx, pool, user, money.Naira(2_000), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	if err := spend(t, pool, func(tx pgx.Tx) error {
		_, e := Withdraw(ctx, tx, user, money.Naira(2_000), money.Zero(money.NGN), withdrawalID)
		return e
	}); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	released := delta(t, pool, ledger.System(), ledger.KindPayable, money.NGN, func() {
		if _, err := Returned(ctx, pool, user, money.Naira(2_000), withdrawalID, "account_closed"); err != nil {
			t.Fatalf("Returned: %v", err)
		}
	})

	if got := balance(t, pool, ledger.User(user), ledger.KindAvailable, money.NGN); got.Minor() != 200_000 {
		t.Errorf("user = %s, want their ₦2,000.00 back", got)
	}
	if released.Minor() != -200_000 {
		t.Errorf("payable released %s, want -₦2,000.00", released)
	}
}

// You cannot spend money you do not have. The ledger primitive permits a
// negative balance -- obligation accounts must go negative -- so this is the
// movement layer's job, and without it nothing in the system stops a card
// being tapped against an empty account.
func TestATapAgainstAnEmptyBalanceIsDeclined(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	cardholder, merchant := uuid.New(), uuid.New()
	if _, err := Deposit(ctx, pool, cardholder, money.Naira(100), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	err := spend(t, pool, func(tx pgx.Tx) error {
		_, e := Tap(ctx, tx, cardholder, merchant, money.Naira(500), money.Zero(money.NGN), uuid.New())
		return e
	})
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("tap of ₦500 against ₦100 returned %v, want ErrInsufficientFunds", err)
	}

	// The declined tap must leave both sides exactly as they were.
	if got := balance(t, pool, ledger.User(cardholder), ledger.KindAvailable, money.NGN); got.Minor() != 10_000 {
		t.Errorf("cardholder = %s, want their ₦100.00 untouched", got)
	}
	if got := balance(t, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, money.NGN); !got.IsZero() {
		t.Errorf("merchant was credited %s by a declined tap", got)
	}
}

// The double-spend. Ten simultaneous taps against a balance that covers three.
//
// This is the test the predecessor could not pass: its card debit held no
// database transaction at all, so every concurrent request read the same
// spent-today figure, every one of them concluded there was room, and every
// one of them charged. Here they queue on the account row and exactly three
// get through.
func TestConcurrentTapsCannotOverdraw(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	cardholder, merchant := uuid.New(), uuid.New()
	if _, err := Deposit(ctx, pool, cardholder, money.Naira(300), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	const attempts = 10
	const each = 100 // ₦100 each, so exactly three can succeed

	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = spend(t, pool, func(tx pgx.Tx) error {
				_, e := Tap(ctx, tx, cardholder, merchant,
					money.Naira(each), money.Zero(money.NGN), uuid.New())
				return e
			})
		}()
	}
	wg.Wait()

	charged, declined := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			charged++
		case errors.Is(err, ErrInsufficientFunds):
			declined++
		default:
			t.Errorf("unexpected failure: %v", err)
		}
	}

	if charged != 3 {
		t.Errorf("%d taps succeeded against a ₦300 balance, want exactly 3", charged)
	}
	if declined != attempts-3 {
		t.Errorf("%d taps declined, want %d", declined, attempts-3)
	}

	// The only thing that really matters: the balance never went negative.
	final := balance(t, pool, ledger.User(cardholder), ledger.KindAvailable, money.NGN)
	if final.IsNegative() {
		t.Fatalf("the cardholder was overdrawn to %s", final)
	}
	if final.Minor() != 0 {
		t.Errorf("final balance = %s, want ₦0.00", final)
	}
	if got := balance(t, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, money.NGN); got.Minor() != 30_000 {
		t.Errorf("merchant owed %s, want ₦300.00 -- no more than was actually spent", got)
	}
}

// The same guarantee across different spending paths. A tap and a withdrawal
// racing for the last of a balance must not both win, which is why the lock is
// on the account rather than on the card.
func TestATapAndAWithdrawalCannotBothTakeTheLastOfIt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	user, merchant := uuid.New(), uuid.New()
	if _, err := Deposit(ctx, pool, user, money.Naira(1_000), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	var wg sync.WaitGroup
	var tapErr, withdrawErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		tapErr = spend(t, pool, func(tx pgx.Tx) error {
			_, e := Tap(ctx, tx, user, merchant, money.Naira(1_000), money.Zero(money.NGN), uuid.New())
			return e
		})
	}()
	go func() {
		defer wg.Done()
		withdrawErr = spend(t, pool, func(tx pgx.Tx) error {
			_, e := Withdraw(ctx, tx, user, money.Naira(1_000), money.Zero(money.NGN), uuid.New())
			return e
		})
	}()
	wg.Wait()

	won := 0
	for _, err := range []error{tapErr, withdrawErr} {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrInsufficientFunds) {
			t.Errorf("unexpected failure: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d of 2 racing spends succeeded against one ₦1,000 balance, want 1", won)
	}

	if final := balance(t, pool, ledger.User(user), ledger.KindAvailable, money.NGN); final.IsNegative() {
		t.Fatalf("balance went negative: %s", final)
	}
}

// A refunded order puts the merchant back where they were before the sale:
// owed, and visibly so. The cardholder is untouched -- they were charged at the
// till and the tap stands.
func TestARefundedSettlementRestoresTheMerchantsClaim(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	cardholder, merchant := uuid.New(), uuid.New()
	tapID := uuid.New()
	amount := money.Naira(1_600)
	fee := money.FeeFor(amount, 50)
	owed, _ := amount.Sub(fee)

	if _, err := Deposit(ctx, pool, cardholder, money.Naira(1_600), "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	if err := spend(t, pool, func(tx pgx.Tx) error {
		_, e := Tap(ctx, tx, cardholder, merchant, amount, fee, tapID)
		return e
	}); err != nil {
		t.Fatalf("Tap: %v", err)
	}
	if err := spend(t, pool, func(tx pgx.Tx) error {
		_, e := MerchantSettledOnChain(ctx, tx, merchant, owed, tapID, 0)
		return e
	}); err != nil {
		t.Fatalf("MerchantSettledOnChain: %v", err)
	}
	if got := balance(t, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, money.NGN); !got.IsZero() {
		t.Fatalf("merchant owed %s after the sale, want nothing", got)
	}

	externalDelta := delta(t, pool, ledger.System(), ledger.KindExternal, money.NGN, func() {
		if err := spend(t, pool, func(tx pgx.Tx) error {
			_, e := MerchantSettlementRefunded(ctx, tx, merchant, owed, tapID, 0, "refunded by the gateway")
			return e
		}); err != nil {
			t.Fatalf("MerchantSettlementRefunded: %v", err)
		}
	})

	if got := balance(t, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, money.NGN); got.Minor() != owed.Minor() {
		t.Errorf("merchant owed %s after the refund, want %s", got, owed)
	}
	if externalDelta.Minor() != -owed.Minor() {
		t.Errorf("external moved %s, want %s back out", externalDelta, owed.Neg())
	}
	if got := balance(t, pool, ledger.User(cardholder), ledger.KindAvailable, money.NGN); !got.IsZero() {
		t.Errorf("cardholder holds %s, but a refunded settlement is not a refunded tap", got)
	}

	// Posting the same round's refund again is a replay, not a second refund.
	err := spend(t, pool, func(tx pgx.Tx) error {
		_, e := MerchantSettlementRefunded(ctx, tx, merchant, owed, tapID, 0, "refunded by the gateway")
		return e
	})
	if !errors.Is(err, ledger.ErrDuplicate) {
		t.Errorf("second refund of round 0: err = %v, want ErrDuplicate", err)
	}

	// The next round is a new sale, and its own discharge.
	if err := spend(t, pool, func(tx pgx.Tx) error {
		_, e := MerchantSettledOnChain(ctx, tx, merchant, owed, tapID, 1)
		return e
	}); err != nil {
		t.Fatalf("MerchantSettledOnChain round 1: %v", err)
	}
	if got := balance(t, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, money.NGN); !got.IsZero() {
		t.Errorf("merchant owed %s after round 1 sold, want nothing", got)
	}
	for c, sum := range globalSums(t, pool) {
		if sum != 0 {
			t.Errorf("global %s sum = %d, value was invented or destroyed", c, sum)
		}
	}
}
