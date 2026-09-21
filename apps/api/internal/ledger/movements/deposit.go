package movements

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/money"
)

// Deposit brings value in from outside: an NGN bank transfer that landed, or
// USDC that arrived on Base and has the confirmations to be treated as final.
//
// The counter-leg is `external`, which represents the world outside this
// system. Its balance runs large and negative in the steady state, and that is
// correct -- it is the mirror of everything held inside. If external plus every
// internal account does not sum to zero, value has been invented.
//
// reference identifies the deposit at its source: a provider payment
// reference, or a chain transaction hash and log index. It is the idempotency
// key, because both sources redeliver. Fintava retries webhooks for 72 hours,
// and a chain watcher that restarts will re-scan blocks it has already seen.
func Deposit(
	ctx context.Context,
	q ledger.Querier,
	user uuid.UUID,
	amount money.Amount,
	source, reference string,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a deposit must be positive, got %s", amount)
	}
	if source == "" || reference == "" {
		return uuid.Nil, fmt.Errorf("movements: a deposit needs a source and a reference to be idempotent")
	}

	c := amount.Currency()
	r := newResolver(ctx, q)
	to := r.account(ledger.User(user), ledger.KindAvailable, c)
	external := r.account(ledger.System(), ledger.KindExternal, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, q, ledger.Ref{
		Type:    "deposit",
		IdemKey: "deposit:" + source + ":" + reference,
	}, []ledger.Entry{
		{AccountID: to, Amount: amount, Reason: "deposit.credited"},
		{AccountID: external, Amount: amount.Neg(), Reason: "deposit.from_" + source},
	})
}

// DepositReversed takes back a deposit that should never have been credited.
//
// Not for a customer's money: what somebody deposited is theirs, and a
// platform that can quietly remove it has no business holding it. This is for
// a credit the chain never justified -- a transfer counted as an arrival when
// it was the platform returning its own funds, which the watcher cannot tell
// apart from a payment without being told.
//
// The entries are the deposit's own, negated, so the pair nets to nothing and
// the history shows both: what was credited, and that it was taken back. A
// deletion would leave a balance nobody can explain.
//
// It refuses to overdraw. If the money has already been spent there is nothing
// to reverse without taking it from somewhere else, and which somewhere is a
// decision for a person, not for this.
func DepositReversed(
	ctx context.Context,
	tx pgx.Tx,
	user uuid.UUID,
	amount money.Amount,
	source, reference, why string,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a reversal must be positive, got %s", amount)
	}
	if source == "" || reference == "" {
		return uuid.Nil, fmt.Errorf("movements: a reversal needs the deposit's source and reference")
	}
	if why == "" {
		return uuid.Nil, fmt.Errorf("movements: a reversed deposit must say why")
	}

	c := amount.Currency()
	r := newResolver(ctx, tx)
	from := r.account(ledger.User(user), ledger.KindAvailable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}
	if err := ensureFunds(ctx, tx, from, amount); err != nil {
		return uuid.Nil, err
	}

	external := r.account(ledger.System(), ledger.KindExternal, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "deposit_reversed",
		IdemKey: "deposit_reversed:" + source + ":" + reference,
	}, []ledger.Entry{
		{AccountID: from, Amount: amount.Neg(), Reason: "deposit.reversed:" + why},
		{AccountID: external, Amount: amount, Reason: "deposit.returned_to_" + source},
	})
}

// Withdraw is the mirror: value leaving for a bank account or a chain address.
//
// It debits the user immediately and parks the value in `payable` rather than
// sending it straight to `external`. That gap is the honest representation of
// "we owe this and it has not landed yet", and it is what the settlement
// worker later discharges with Settled -- driven by the provider confirming
// the credit, never by our own request having been accepted.
func Withdraw(
	ctx context.Context,
	tx pgx.Tx,
	user uuid.UUID,
	amount money.Amount,
	fee money.Amount,
	withdrawalID uuid.UUID,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a withdrawal must be positive, got %s", amount)
	}
	net, err := amount.Sub(fee)
	if err != nil {
		return uuid.Nil, err
	}
	if !net.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a fee of %s leaves nothing to withdraw from %s", fee, amount)
	}

	c := amount.Currency()
	r := newResolver(ctx, tx)

	// Spending account first, then its lock, then everything else. See Tap.
	from := r.account(ledger.User(user), ledger.KindAvailable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}
	if err := ensureFunds(ctx, tx, from, amount); err != nil {
		return uuid.Nil, err
	}

	payable := r.account(ledger.System(), ledger.KindPayable, c)
	revenue := r.account(ledger.System(), ledger.KindRevenue, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	entries := []ledger.Entry{
		{AccountID: from, Amount: amount.Neg(), Reason: "withdrawal.debited"},
		{AccountID: payable, Amount: net, Reason: "withdrawal.owed"},
	}
	if fee.IsPositive() {
		entries = append(entries, ledger.Entry{AccountID: revenue, Amount: fee, Reason: "withdrawal.fee"})
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "withdrawal",
		ID:      &withdrawalID,
		IdemKey: "withdrawal:" + withdrawalID.String(),
	}, entries)
}
