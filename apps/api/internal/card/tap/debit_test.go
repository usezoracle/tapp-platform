package tap

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/card/auth"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/rates"
)

// challenge asks for a tier and nonce the way a merchant app does.
func (f *fixture) challenge(t *testing.T, amount money.Amount) *Challenge {
	t.Helper()
	f.later()
	ch, err := f.Svc.Challenge(context.Background(), ChallengeRequest{
		CardUIDHash: f.UIDHash, MerchantID: f.Merchant, Amount: amount,
	})
	if err != nil {
		t.Fatalf("Challenge(%s): %v", amount, err)
	}
	return ch
}

// pay runs a full challenge-then-debit, answering the PIN when asked.
func (f *fixture) pay(t *testing.T, amount money.Amount) (*Receipt, error) {
	t.Helper()
	ch := f.challenge(t, amount)

	req := Request{
		CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: ch.Nonce,
		MerchantID: f.Merchant, Amount: amount,
	}
	if ch.Tier == auth.TierPIN {
		req.PINResponse = auth.Respond(f.Anchor, ch.Nonce)
	}
	return f.Svc.Debit(context.Background(), req)
}

func (f *fixture) balance(t *testing.T) money.Amount {
	t.Helper()
	b, err := ledger.Balance(context.Background(), f.Pool,
		ledger.User(f.Cardholder), ledger.KindAvailable, money.NGN)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}

func TestATapChargesTheCardholderAndCreditsTheMerchant(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))

	receipt, err := f.pay(t, money.Naira(1_500))
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if receipt.Tier != auth.TierNone {
		t.Errorf("₦1,500 required tier %q, want none (per-tap threshold is ₦2,000)", receipt.Tier)
	}

	// ₦1,500 less 0.5% = ₦7.50 fee
	if receipt.Fee.Minor() != 750 {
		t.Errorf("fee = %s, want ₦7.50", receipt.Fee)
	}
	if got := f.balance(t); got.Minor() != 850_000 {
		t.Errorf("cardholder = %s, want ₦8,500.00", got)
	}
	owed, _ := ledger.Balance(context.Background(), f.Pool,
		ledger.Merchant(f.Merchant), ledger.KindMerchantPayable, money.NGN)
	if owed.Minor() != 149_250 {
		t.Errorf("merchant owed %s, want ₦1,492.50", owed)
	}
	if receipt.RemainingDaily.Minor() != 3_850_000 {
		t.Errorf("remaining daily = %s, want ₦38,500.00", receipt.RemainingDaily)
	}
}

// A challenge is single-use. Presenting the same nonce twice -- which is what
// a captured response looks like -- must fail on the second attempt whatever
// else is true.
func TestAChallengeCannotBeUsedTwice(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	amount := money.Naira(1_000)
	ch := f.challenge(t, amount)

	req := Request{
		CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: ch.Nonce,
		MerchantID: f.Merchant, Amount: amount,
	}
	if _, err := f.Svc.Debit(context.Background(), req); err != nil {
		t.Fatalf("first debit: %v", err)
	}
	if _, err := f.Svc.Debit(context.Background(), req); !errors.Is(err, ErrNonceInvalid) {
		t.Fatalf("replay returned %v, want ErrNonceInvalid", err)
	}

	if got := f.balance(t); got.Minor() != 900_000 {
		t.Errorf("cardholder = %s, want ₦9,000.00 -- charged twice", got)
	}
}

// The tier is fixed when the challenge is issued. Without that, a merchant
// could ask what ₦500 needs, be told nothing, and then charge ₦50,000 against
// the same unauthenticated challenge.
func TestAMerchantCannotChargeMoreThanItAskedFor(t *testing.T) {
	f := newFixture(t, money.Naira(100_000))
	ch := f.challenge(t, money.Naira(500))
	if ch.Tier != auth.TierNone {
		t.Fatalf("setup: ₦500 gave tier %q", ch.Tier)
	}

	_, err := f.Svc.Debit(context.Background(), Request{
		CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: ch.Nonce,
		MerchantID: f.Merchant, Amount: money.Naira(50_000),
	})
	if !errors.Is(err, ErrAmountChanged) {
		t.Fatalf("charging ₦50,000 against a ₦500 challenge returned %v, want ErrAmountChanged", err)
	}
	if got := f.balance(t); got.Minor() != 10_000_000 {
		t.Errorf("cardholder = %s, want ₦100,000.00 untouched", got)
	}
}

// A challenge issued to one merchant cannot be spent at another, so a response
// captured at a compromised terminal cannot be carried elsewhere.
func TestAChallengeIsBoundToItsMerchant(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	other := newFixture(t, money.Naira(0))
	amount := money.Naira(1_000)
	ch := f.challenge(t, amount)

	_, err := f.Svc.Debit(context.Background(), Request{
		CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: ch.Nonce,
		MerchantID: other.Merchant, Amount: amount,
	})
	if !errors.Is(err, ErrNonceInvalid) {
		t.Fatalf("a challenge was spent at a different merchant: %v", err)
	}
}

func TestAPINIsRequiredAboveTheThreshold(t *testing.T) {
	f := newFixture(t, money.Naira(50_000))
	amount := money.Naira(5_000)
	ch := f.challenge(t, amount)
	if ch.Tier != auth.TierPIN {
		t.Fatalf("₦5,000 gave tier %q, want pin", ch.Tier)
	}

	// No PIN offered.
	_, err := f.Svc.Debit(context.Background(), Request{
		CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: ch.Nonce,
		MerchantID: f.Merchant, Amount: amount,
	})
	if !errors.Is(err, auth.ErrWrongPIN) {
		t.Fatalf("a PIN-tier tap with no PIN returned %v", err)
	}

	// With the right one.
	receipt, err := f.pay(t, amount)
	if err != nil {
		t.Fatalf("Debit with the correct PIN: %v", err)
	}
	if receipt.Tier != auth.TierPIN {
		t.Errorf("recorded tier %q, want pin", receipt.Tier)
	}
}

// Five wrong PINs lock the card, and the lock expires. The predecessor set
// locked_until and never compared it, so a card locked this way stayed locked
// forever and its holder had to contact support.
func TestWrongPINsLockTheCardAndTheLockExpires(t *testing.T) {
	f := newFixture(t, money.Naira(50_000))
	amount := money.Naira(5_000)
	wrongAnchor := auth.Anchor([]byte("32-bytes-of-secret-living-on-crd"), "0000")

	for attempt := 1; attempt <= PINAttempts; attempt++ {
		ch := f.challenge(t, amount)
		_, err := f.Svc.Debit(context.Background(), Request{
			CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: ch.Nonce,
			MerchantID: f.Merchant, Amount: amount,
			PINResponse: auth.Respond(wrongAnchor, ch.Nonce),
		})
		if !errors.Is(err, auth.ErrWrongPIN) {
			t.Fatalf("attempt %d returned %v, want ErrWrongPIN", attempt, err)
		}
	}

	status, attempts, _, lockedUntil := f.cardStatus(t)
	if status != StatusLocked {
		t.Errorf("card status = %q after %d wrong PINs, want locked", status, PINAttempts)
	}
	if attempts != 0 {
		t.Errorf("attempts remaining = %d, want 0", attempts)
	}
	if lockedUntil == nil {
		t.Fatal("card was locked with no expiry -- it can never recover")
	}

	// While locked, nothing works.
	if _, err := f.Svc.Challenge(context.Background(), ChallengeRequest{
		CardUIDHash: f.UIDHash, MerchantID: f.Merchant, Amount: money.Naira(100),
	}); !errors.Is(err, ErrCardUnavailable) {
		t.Fatalf("a locked card issued a challenge: %v", err)
	}

	// Once the window passes, it works again without anyone intervening.
	f.Svc.Now = func() time.Time { return lockedUntil.Add(time.Minute) }
	if _, err := f.Svc.Challenge(context.Background(), ChallengeRequest{
		CardUIDHash: f.UIDHash, MerchantID: f.Merchant, Amount: money.Naira(100),
	}); err != nil {
		t.Fatalf("the lock did not expire: %v", err)
	}
}

// A correct PIN restores the allowance, so four mistakes spread over a year do
// not lock a card on the fifth.
func TestACorrectPINRestoresTheAllowance(t *testing.T) {
	f := newFixture(t, money.Naira(50_000))
	amount := money.Naira(5_000)
	wrongAnchor := auth.Anchor([]byte("32-bytes-of-secret-living-on-crd"), "0000")

	ch := f.challenge(t, amount)
	_, _ = f.Svc.Debit(context.Background(), Request{
		CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: ch.Nonce,
		MerchantID: f.Merchant, Amount: amount,
		PINResponse: auth.Respond(wrongAnchor, ch.Nonce),
	})
	if _, attempts, _, _ := f.cardStatus(t); attempts != PINAttempts-1 {
		t.Fatalf("attempts = %d after one failure, want %d", attempts, PINAttempts-1)
	}

	if _, err := f.pay(t, amount); err != nil {
		t.Fatalf("correct PIN: %v", err)
	}
	if _, attempts, _, _ := f.cardStatus(t); attempts != PINAttempts {
		t.Errorf("attempts = %d after a correct PIN, want the full %d back", attempts, PINAttempts)
	}
}

func TestALargeAmountNeedsCardholderApproval(t *testing.T) {
	f := newFixture(t, money.Naira(100_000))
	amount := money.Naira(20_000)
	ch := f.challenge(t, amount)
	if ch.Tier != auth.TierStepUp {
		t.Fatalf("₦20,000 gave tier %q, want step_up", ch.Tier)
	}
	if ch.StepUpRef == "" {
		t.Fatal("a step-up challenge carried no reference for the cardholder to approve")
	}

	req := Request{
		CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: ch.Nonce,
		MerchantID: f.Merchant, Amount: amount, StepUpRef: ch.StepUpRef,
	}
	if _, err := f.Svc.Debit(context.Background(), req); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("an unapproved step-up returned %v, want ErrStepUpRequired", err)
	}

	// A step-up awaiting approval rolls back, so the challenge is still
	// usable once the cardholder approves it -- the merchant does not have to
	// start over every time it polls.
	if _, err := f.Pool.Exec(context.Background(),
		`UPDATE card_server_nonces SET step_up_granted_at = now() WHERE id = $1`,
		ch.ID); err != nil {
		t.Fatalf("grant approval: %v", err)
	}
	if _, err := f.Svc.Debit(context.Background(), req); err != nil {
		t.Fatalf("an approved step-up was refused: %v", err)
	}
}

// fixedQuoter prices at a flat rate with no spread, so the test asserts that
// the conversion happened rather than re-deriving live arithmetic.
type fixedQuoter struct {
	// ratePerMajor is minor units of the bought currency per one MAJOR unit
	// of the sold one: 150_000 means ₦1,500.00 per $1.
	ratePerMajor int64
	issued       *rates.Quote
	calls        int
}

func (f *fixedQuoter) OfferForBuy(
	_ context.Context, sell money.Currency, buy money.Amount,
) (*rates.Quote, error) {
	f.calls++
	// Ceiling, as the real reverse quote does: a sale that rounds down leaves
	// the buy short and the debit declines after the card has been read.
	sellMinor := (buy.Minor()*sell.Scale() + f.ratePerMajor - 1) / f.ratePerMajor
	f.issued = &rates.Quote{
		ID: uuid.New(), Pair: rates.Pair{Base: sell, Quote: buy.Currency()},
		Sell: money.New(sellMinor, sell), Buy: buy, Fee: money.Zero(buy.Currency()),
	}
	return f.issued, nil
}

func (f *fixedQuoter) Redeem(_ context.Context, _ pgx.Tx, id uuid.UUID) (*rates.Quote, error) {
	if f.issued == nil || f.issued.ID != id {
		return nil, fmt.Errorf("no such quote %s", id)
	}
	return f.issued, nil
}

// Balances are held in the currency they arrived as -- dollars, from USDC --
// and the exchange happens at the till for exactly what is being spent. So a
// naira tap has to buy its naira out of a dollar balance first.
func TestATapBuysItsNairaOutOfADollarBalance(t *testing.T) {
	f := newFixture(t, money.Amount{})
	ctx := context.Background()

	// Fund in dollars, which is what a deposit actually credits.
	if _, err := movements.Deposit(ctx, f.Pool, f.Cardholder,
		money.New(1000, money.USD), "test", uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	q := &fixedQuoter{ratePerMajor: 150_000} // ₦1,500.00 per $1
	f.Svc.Funding = money.USD
	f.Svc.Quoter = q

	if _, err := f.pay(t, money.Naira(1_500)); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if q.calls != 1 {
		t.Errorf("quoted %d times, want exactly once per tap", q.calls)
	}

	// The naira bought is spent in full: what the cardholder paid plus the
	// platform's fee. Anything left would be dust nobody asked to hold.
	ngn, err := ledger.Balance(ctx, f.Pool, ledger.User(f.Cardholder), ledger.KindAvailable, money.NGN)
	if err != nil {
		t.Fatal(err)
	}
	if !ngn.IsZero() {
		t.Errorf("naira left over = %s, want nothing", ngn)
	}

	// And the dollars actually went down.
	usd, err := ledger.Balance(ctx, f.Pool, ledger.User(f.Cardholder), ledger.KindAvailable, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	if usd.Minor() >= 1000 {
		t.Errorf("dollar balance = %s, want less than $10.00 -- the tap was not funded from it", usd)
	}
}

// No rate is a refusal, not a fault: the cardholder has the money and the
// platform cannot say what it is worth. Guessing one at a till would charge
// somebody a price nobody quoted.
func TestATapWithoutARateIsRefusedNotGuessed(t *testing.T) {
	f := newFixture(t, money.Amount{})
	ctx := context.Background()

	if _, err := movements.Deposit(ctx, f.Pool, f.Cardholder,
		money.New(1000, money.USD), "test", uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	f.Svc.Funding = money.USD
	f.Svc.Quoter = nil // configured to convert, unable to price

	_, err := f.pay(t, money.Naira(1_500))
	if !errors.Is(err, ErrCannotPrice) {
		t.Fatalf("err = %v, want ErrCannotPrice", err)
	}

	usd, err := ledger.Balance(ctx, f.Pool, ledger.User(f.Cardholder), ledger.KindAvailable, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	if usd.Minor() != 1000 {
		t.Errorf("dollar balance = %s, want $10.00 untouched by a refused tap", usd)
	}
}

// A funding currency with coarser minor units cannot buy an exact amount: one
// US cent is worth about thirteen naira, so buying ₦1,500 means buying the
// next whole cent and keeping the change. That change has to be spent by the
// next tap rather than left behind on every one of them.
func TestATapSpendsLeftoverNairaBeforeBuyingMore(t *testing.T) {
	f := newFixture(t, money.Amount{})
	ctx := context.Background()

	if _, err := movements.Deposit(ctx, f.Pool, f.Cardholder,
		money.New(1000, money.USD), "test", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	// Change left by an earlier tap.
	if _, err := movements.Deposit(ctx, f.Pool, f.Cardholder,
		money.Naira(400), "test", uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	q := &fixedQuoter{ratePerMajor: 150_000} // ₦1,500.00 per $1
	f.Svc.Funding = money.USD
	f.Svc.Quoter = q

	if _, err := f.pay(t, money.Naira(1_500)); err != nil {
		t.Fatalf("Debit: %v", err)
	}

	// It must have bought ₦1,100, not ₦1,500: the ₦400 already held is spent
	// first. At ₦1,500 per dollar that is 74 cents rather than a whole dollar.
	if q.issued == nil {
		t.Fatal("no quote was taken")
	}
	if got, want := q.issued.Buy, money.Naira(1_100); got.Minor() != want.Minor() {
		t.Errorf("bought %s, want %s -- the naira already held was not spent first", got, want)
	}
}

// Enough on hand means no conversion at all: a tap that can be paid from the
// balance already held must not touch a rate source or spend a quote.
func TestATapWithEnoughNairaBuysNothing(t *testing.T) {
	f := newFixture(t, money.Naira(5_000))
	ctx := context.Background()

	if _, err := movements.Deposit(ctx, f.Pool, f.Cardholder,
		money.New(1000, money.USD), "test", uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	q := &fixedQuoter{ratePerMajor: 150_000}
	f.Svc.Funding = money.USD
	f.Svc.Quoter = q

	if _, err := f.pay(t, money.Naira(1_500)); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if q.calls != 0 {
		t.Errorf("quoted %d times, want 0 -- the naira on hand covered it", q.calls)
	}

	usd, err := ledger.Balance(ctx, f.Pool, ledger.User(f.Cardholder), ledger.KindAvailable, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	if usd.Minor() != 1000 {
		t.Errorf("dollar balance = %s, want $10.00 untouched", usd)
	}
}

// A card left resting on the phone is read again the moment the till is
// re-armed. The same amount from the same card at the same till inside
// RepeatWindow is refused at the challenge and at the debit; a different
// amount, or a moment later, goes through.
func TestTheSameCardIsNotChargedTheSameAmountTwiceInAMoment(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	amount := money.Naira(1_500)
	ctx := context.Background()

	// A challenge issued before the first tap posts: the race the debit
	// guard exists for.
	early := f.challenge(t, amount)

	if _, err := f.pay(t, amount); err != nil {
		t.Fatalf("first tap: %v", err)
	}
	at := f.elapsed

	// Ten seconds later, the same again: refused before anything moves.
	f.Svc.Now = func() time.Time { return time.Now().Add(at + 10*time.Second) }
	_, err := f.Svc.Challenge(ctx, ChallengeRequest{CardUIDHash: f.UIDHash, MerchantID: f.Merchant, Amount: amount})
	if !errors.Is(err, ErrRepeatTap) {
		t.Fatalf("second challenge: err = %v, want ErrRepeatTap", err)
	}

	// The early challenge, presented now, is refused at the debit.
	_, err = f.Svc.Debit(ctx, Request{
		CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: early.Nonce,
		MerchantID: f.Merchant, Amount: amount,
	})
	if !errors.Is(err, ErrRepeatTap) {
		t.Fatalf("debit inside the window: err = %v, want ErrRepeatTap", err)
	}
	if got := f.balance(t); got.Minor() != 850_000 {
		t.Errorf("cardholder = %s, want ₦8,500.00 (one tap, not two)", got)
	}

	// A different amount is a different purchase.
	if _, err := f.Svc.Challenge(ctx, ChallengeRequest{CardUIDHash: f.UIDHash, MerchantID: f.Merchant, Amount: money.Naira(1_499)}); err != nil {
		t.Errorf("a different amount inside the window: %v", err)
	}

	// Past the window it is a new payment.
	f.Svc.Now = func() time.Time { return time.Now().Add(f.elapsed) }
	if _, err := f.pay(t, amount); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// A reversed tap does not block a retake: the merchant refunded it precisely
// so that it could be taken again.
func TestAReversedTapDoesNotBlockTheNextOne(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	amount := money.Naira(1_500)
	receipt, err := f.pay(t, amount)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Svc.Reverse(context.Background(), f.Merchant, receipt.TapID, "wrong amount entered"); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if _, err := f.pay(t, amount); err != nil {
		t.Fatalf("retake after reversal: %v", err)
	}
}
