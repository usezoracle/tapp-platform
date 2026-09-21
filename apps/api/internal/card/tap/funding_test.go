package tap

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

// fundingOf reads what the tap row says paid for it.
func fundingOf(t *testing.T, f *fixture, tap uuid.UUID) (source string, ngn, usdc int64) {
	t.Helper()
	if err := f.Pool.QueryRow(context.Background(),
		`SELECT funding_source, funded_ngn_minor, funded_usdc_minor FROM card_taps WHERE id = $1`, tap).
		Scan(&source, &ngn, &usdc); err != nil {
		t.Fatalf("funding_source: %v", err)
	}
	return
}

// A tap records the split that paid for it -- what came from a naira balance
// and what had to be bought out of dollars -- and the settlement hook is told
// the same split in the tap's own transaction.
func TestATapRecordsWhatFundedIt(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	ctx := context.Background()

	var told []Charged
	f.Svc.Settle = func(_ context.Context, _ pgx.Tx, c Charged) error {
		told = append(told, c)
		return nil
	}
	f.Svc.Funding = money.USD
	f.Svc.Quoter = &fixedQuoter{ratePerMajor: 150_000} // ₦1,500.00 per $1

	// Naira on hand covers the tap: nothing converted.
	r, err := f.pay(t, money.Naira(1_500))
	if err != nil {
		t.Fatalf("naira tap: %v", err)
	}
	if source, ngn, usdc := fundingOf(t, f, r.TapID); source != "ngn" || ngn != 150_000 || usdc != 0 {
		t.Errorf("recorded %s ngn=%d usdc=%d, want ngn 150000/0", source, ngn, usdc)
	}
	if len(told) != 1 || told[0].Funding.Source() != FundedByNGN || told[0].TapID != r.TapID ||
		told[0].Merchant != f.Merchant || told[0].Owed().Minor() != r.Amount.Minor()-r.Fee.Minor() {
		t.Fatalf("settle hook was told %+v", told)
	}
	// The fee comes out of the naira; the whole of what is owed is the
	// naira leg, and there is no USDC leg.
	if ngn, usdc := told[0].Legs(); ngn.Minor() != told[0].Owed().Minor() || !usdc.IsZero() {
		t.Errorf("legs = %s / %s, want %s / nothing", ngn, usdc, told[0].Owed())
	}

	// ₦8,500 naira left; a ₦9,000 tap takes all of it and buys the rest.
	f.later()
	if _, err := movements.Deposit(ctx, f.Pool, f.Cardholder, money.New(10_000, money.USD), "test", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	r, err = f.pay(t, money.Naira(9_000))
	if err != nil {
		t.Fatalf("mixed tap: %v", err)
	}
	if source, ngn, usdc := fundingOf(t, f, r.TapID); source != "mixed" || ngn != 850_000 || usdc != 50_000 {
		t.Errorf("recorded %s ngn=%d usdc=%d, want mixed 850000/50000", source, ngn, usdc)
	}
	if len(told) != 2 || told[1].Funding.Source() != FundedMixed {
		t.Fatalf("settle hook was told %+v", told)
	}
	ngn, usdc := told[1].Legs()
	if ngn.Minor() != 850_000-told[1].Fee.Minor() || usdc.Minor() != 50_000 {
		t.Errorf("legs = %s / %s, want ₦8,500 less the fee / ₦500", ngn, usdc)
	}
	if ngn.Minor()+usdc.Minor() != told[1].Owed().Minor() {
		t.Errorf("legs sum to %d, want what is owed %d", ngn.Minor()+usdc.Minor(), told[1].Owed().Minor())
	}

	// No naira at all: the whole tap is bought, and it is all USDC leg.
	f.later()
	r, err = f.pay(t, money.Naira(1_500))
	if err != nil {
		t.Fatalf("usdc tap: %v", err)
	}
	if source, ngn, usdc := fundingOf(t, f, r.TapID); source != "usdc" || ngn != 0 || usdc != 150_000 {
		t.Errorf("recorded %s ngn=%d usdc=%d, want usdc 0/150000", source, ngn, usdc)
	}
	if ngn, usdc := told[2].Legs(); !ngn.IsZero() || usdc.Minor() != told[2].Owed().Minor() {
		t.Errorf("legs = %s / %s, want nothing / %s", ngn, usdc, told[2].Owed())
	}
}

// Dust from an earlier conversion never becomes a bank transfer of its own:
// a naira share no bigger than the fee is absorbed by it.
func TestNairaDustIsAbsorbedByTheFee(t *testing.T) {
	c := Charged{
		Amount: money.Naira(1_600), Fee: money.Naira(8),
		Funding: Funding{NGN: money.Naira(5), USDC: money.Naira(1_595)},
	}
	ngn, usdc := c.Legs()
	if !ngn.IsZero() || usdc.Minor() != 159_200 {
		t.Errorf("legs = %s / %s, want nothing / ₦1,592.00", ngn, usdc)
	}
}

// A settlement hook that refuses -- a merchant with nowhere to be paid --
// fails the tap, and the charge rolls back with it.
func TestASettlementRefusalRollsTheTapBack(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	refused := errors.New("the merchant has no verified bank account")
	f.Svc.Settle = func(context.Context, pgx.Tx, Charged) error { return refused }

	if _, err := f.pay(t, money.Naira(1_500)); !errors.Is(err, refused) {
		t.Fatalf("Debit = %v, want the hook's refusal", err)
	}
	if got := f.balance(t); got.Minor() != 1_000_000 {
		t.Errorf("cardholder balance = %s after a refused tap, want ₦10,000.00 untouched", got)
	}
	var taps int
	if err := f.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM card_taps WHERE cardholder_id = $1`, f.Cardholder).Scan(&taps); err != nil {
		t.Fatal(err)
	}
	if taps != 0 {
		t.Errorf("%d tap rows recorded for a refused tap, want none", taps)
	}
}
