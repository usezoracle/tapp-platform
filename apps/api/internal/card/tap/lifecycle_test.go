package tap

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/usezoracle/tapp/api/internal/card/token"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

// The failure the two-phase rotation exists for. A debit issues a new token,
// the NFC write fails, and the card still holds the old one. It must keep
// working. The predecessor rotated at debit time, so this bricked a legitimate
// card and sent its holder into a Web NFC resync flow that iOS cannot run.
func TestAFailedTokenWriteLeavesTheCardWorking(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))

	receipt, err := f.pay(t, money.Naira(1_000))
	if err != nil {
		t.Fatalf("first tap: %v", err)
	}

	// The write failed; the merchant app says so.
	if err := f.Svc.Acknowledge(context.Background(), f.Merchant, receipt.TapID, false); err != nil {
		t.Fatalf("Acknowledge(false): %v", err)
	}

	current, pending := f.tokens(t)
	if !bytes.Equal(current, f.Token) {
		t.Error("the card's current token changed even though the write failed")
	}
	if pending != nil {
		t.Error("an unwritten token was left pending")
	}

	// The card still has its original token, and it still works.
	if _, err := f.pay(t, money.Naira(1_000)); err != nil {
		t.Fatalf("the card stopped working after a failed write: %v", err)
	}
}

// The other half: the write landed but the acknowledgement was lost, so the
// card presents the token the server thinks is only pending. That must work
// too, and it promotes.
func TestACardPresentingAnUnacknowledgedTokenIsAccepted(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))

	receipt, err := f.pay(t, money.Naira(1_000))
	if err != nil {
		t.Fatalf("first tap: %v", err)
	}
	// No acknowledgement arrives. The card now holds receipt.NewToken.
	f.Token = receipt.NewToken

	if _, err := f.pay(t, money.Naira(1_000)); err != nil {
		t.Fatalf("a card presenting its unacknowledged token was refused: %v", err)
	}

	// The old token is gone: it was promoted out on the second tap.
	current, _ := f.tokens(t)
	if bytes.Equal(current, receipt.NewToken) {
		return // promoted, as expected
	}
}

// A successful acknowledgement retires the old token, so a clone made before
// the tap no longer works.
func TestAnAcknowledgedWritePromotesAndRetires(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	cloned := append([]byte(nil), f.Token...)

	receipt, err := f.pay(t, money.Naira(1_000))
	if err != nil {
		t.Fatalf("tap: %v", err)
	}
	if err := f.Svc.Acknowledge(context.Background(), f.Merchant, receipt.TapID, true); err != nil {
		t.Fatalf("Acknowledge(true): %v", err)
	}

	current, pending := f.tokens(t)
	if !bytes.Equal(current, receipt.NewToken) {
		t.Error("the new token was not promoted")
	}
	if pending != nil {
		t.Error("a promoted token was left pending as well")
	}

	// The clone presents the retired token.
	f.Token = cloned
	ch := f.challenge(t, money.Naira(500))
	_, err = f.Svc.Debit(context.Background(), Request{
		CardUIDHash: f.UIDHash, PresentedToken: cloned, Nonce: ch.Nonce,
		MerchantID: f.Merchant, Amount: money.Naira(500),
	})
	if !errors.Is(err, ErrTokenStale) {
		t.Fatalf("a clone with a retired token returned %v, want ErrTokenStale", err)
	}
}

// Repeated unrecognised tokens lock the card. This is the detection the whole
// rotating-token scheme exists to provide.
func TestRepeatedStaleTokensLockTheCard(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	stale, err := token.New()
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < MismatchesBeforeLock; i++ {
		ch := f.challenge(t, money.Naira(500))
		_, err := f.Svc.Debit(context.Background(), Request{
			CardUIDHash: f.UIDHash, PresentedToken: stale, Nonce: ch.Nonce,
			MerchantID: f.Merchant, Amount: money.Naira(500),
		})
		if !errors.Is(err, ErrTokenStale) {
			t.Fatalf("attempt %d returned %v, want ErrTokenStale", i+1, err)
		}
	}

	status, _, mismatches, _ := f.cardStatus(t)
	if status != StatusLocked {
		t.Errorf("status = %q after %d stale taps, want locked", status, MismatchesBeforeLock)
	}
	if mismatches < MismatchesBeforeLock {
		t.Errorf("mismatch count = %d, want at least %d -- the count did not persist",
			mismatches, MismatchesBeforeLock)
	}
}

// The daily limit is summed from taps inside the debit transaction. The
// predecessor kept a counter on the card row that was raced by concurrent taps
// and never reset.
func TestTheDailyLimitIsEnforcedAndDerived(t *testing.T) {
	f := newFixture(t, money.Naira(200_000))

	// Four taps of ₦10,000 reach the ₦40,000 daily limit.
	for i := 0; i < 4; i++ {
		if _, err := f.pay(t, money.Naira(10_000)); err != nil {
			t.Fatalf("tap %d: %v", i+1, err)
		}
	}

	if _, err := f.pay(t, money.Naira(1)); !errors.Is(err, ErrDailyLimitReached) {
		t.Fatalf("a tap past the daily limit returned %v, want ErrDailyLimitReached", err)
	}

	// Tomorrow it resets, with no counter to have failed to reset.
	f.Svc.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if _, err := f.pay(t, money.Naira(1_000)); err != nil {
		t.Fatalf("the daily limit did not reset overnight: %v", err)
	}
}

// A reversed tap stops consuming the cardholder's daily allowance. A refunded
// payment should not go on counting against them.
func TestAReversalReturnsTheDailyAllowance(t *testing.T) {
	f := newFixture(t, money.Naira(200_000))

	var last *Receipt
	for i := 0; i < 4; i++ {
		r, err := f.pay(t, money.Naira(10_000))
		if err != nil {
			t.Fatalf("tap %d: %v", i+1, err)
		}
		last = r
	}
	if _, err := f.pay(t, money.Naira(1)); !errors.Is(err, ErrDailyLimitReached) {
		t.Fatal("setup: the limit was not reached")
	}

	if err := f.Svc.Reverse(context.Background(), f.Merchant, last.TapID, "goods_not_supplied"); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	if _, err := f.pay(t, money.Naira(5_000)); err != nil {
		t.Fatalf("the reversal did not free up the allowance: %v", err)
	}

	// And a tap cannot be refunded twice.
	if err := f.Svc.Reverse(context.Background(), f.Merchant, last.TapID, "again"); err == nil {
		t.Fatal("a tap was reversed twice")
	}
}

// Concurrent taps against a balance that covers three. The predecessor held no
// transaction at all, so every one of these would have charged.
func TestConcurrentTapsCannotOverdrawTheCard(t *testing.T) {
	f := newFixture(t, money.Naira(3_000))

	const attempts = 8
	amount := money.Naira(1_000)

	// Challenges are issued up front so the goroutines race on the debit, not
	// on issuing.
	challenges := make([]*Challenge, attempts)
	for i := range challenges {
		challenges[i] = f.challenge(t, amount)
	}

	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.Svc.Debit(context.Background(), Request{
				CardUIDHash: f.UIDHash, PresentedToken: f.Token, Nonce: challenges[i].Nonce,
				MerchantID: f.Merchant, Amount: amount,
			})
		}()
	}
	wg.Wait()

	charged := 0
	for _, err := range errs {
		switch {
		case err == nil:
			charged++
		case errors.Is(err, movements.ErrInsufficientFunds), errors.Is(err, ErrTokenStale),
			errors.Is(err, ErrRepeatTap):
			// Any of these refusals is correct: the balance ran out, the
			// card's token moved on under a concurrent tap, or the same
			// amount had just been taken from this card at this till.
		default:
			t.Errorf("unexpected failure: %v", err)
		}
	}

	if charged > 3 {
		t.Errorf("%d taps charged against a ₦3,000 balance, want at most 3", charged)
	}
	if final := f.balance(t); final.IsNegative() {
		t.Fatalf("the cardholder was overdrawn to %s", final)
	}

	// Whatever got through, the books still balance.
	audit, err := ledger.Auditor(context.Background(), f.Pool)
	if err != nil {
		t.Fatalf("Auditor: %v", err)
	}
	if !audit.Balanced {
		t.Fatal("the ledger does not balance after concurrent taps")
	}
}

// A card whose limits were never set cannot transact. The predecessor fell
// back to package constants, so a card that had never completed linking
// silently acquired a ₦40,000 daily allowance.
func TestACardWithNoLimitsIsRefused(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	if _, err := f.Pool.Exec(context.Background(), `
		UPDATE tapp_cards SET daily_limit_subunit = 0, per_tap_limit_subunit = 0,
		       step_up_threshold_subunit = 0 WHERE id = $1`, f.CardID); err != nil {
		t.Fatalf("clear limits: %v", err)
	}

	if _, err := f.Svc.Challenge(context.Background(), ChallengeRequest{
		CardUIDHash: f.UIDHash, MerchantID: f.Merchant, Amount: money.Naira(100),
	}); err == nil {
		t.Fatal("a card with no agreed limits issued a challenge")
	}
	if got := f.balance(t); got.Minor() != 1_000_000 {
		t.Errorf("balance = %s, want ₦10,000.00 untouched", got)
	}
}

// An unknown card and a revoked one are indistinguishable, so the endpoint
// cannot be used to enumerate which cards exist.
func TestUnknownAndRevokedCardsLookTheSame(t *testing.T) {
	f := newFixture(t, money.Naira(1_000))

	unknown := make([]byte, 32)
	for i := range unknown {
		unknown[i] = 0xEE
	}
	_, unknownErr := f.Svc.Challenge(context.Background(), ChallengeRequest{
		CardUIDHash: unknown, MerchantID: f.Merchant, Amount: money.Naira(100),
	})

	if _, err := f.Pool.Exec(context.Background(),
		`UPDATE tapp_cards SET status = 'revoked' WHERE id = $1`, f.CardID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_, revokedErr := f.Svc.Challenge(context.Background(), ChallengeRequest{
		CardUIDHash: f.UIDHash, MerchantID: f.Merchant, Amount: money.Naira(100),
	})

	if !errors.Is(unknownErr, ErrCardUnknown) || !errors.Is(revokedErr, ErrCardUnknown) {
		t.Fatalf("unknown gave %v, revoked gave %v; both should be ErrCardUnknown",
			unknownErr, revokedErr)
	}
}
