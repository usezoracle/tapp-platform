package movements

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/money"
)

// Settled records value actually reaching its destination in the outside
// world: a bank confirming a credit, or a chain transaction reaching finality.
//
// This is the only movement that discharges `payable`, and it must be driven
// by the provider CONFIRMING the credit -- never by our own request having
// been accepted. A request that was accepted and then failed leaves money
// owed; a request that timed out may or may not have moved money. Neither is
// a settlement, and treating them as one is how a ledger comes to claim it has
// paid somebody it has not.
func Settled(
	ctx context.Context,
	q ledger.Querier,
	amount money.Amount,
	providerRef string,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a settlement must be positive, got %s", amount)
	}
	if providerRef == "" {
		return uuid.Nil, fmt.Errorf("movements: a settlement needs the provider's reference")
	}

	c := amount.Currency()
	r := newResolver(ctx, q)
	payable := r.account(ledger.System(), ledger.KindPayable, c)
	external := r.account(ledger.System(), ledger.KindExternal, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, q, ledger.Ref{
		Type:    "settlement",
		IdemKey: "settlement:" + providerRef,
	}, []ledger.Entry{
		{AccountID: payable, Amount: amount.Neg(), Reason: "settlement.delivered"},
		{AccountID: external, Amount: amount, Reason: "settlement.left_the_system"},
	})
}

// Returned handles value the destination would not accept -- a wrong account
// number, a closed account, a rejected transfer.
//
// It goes back to the party who is now holding nothing, so they can correct
// the details and send it again or withdraw it. It does not vanish, and it does
// not stay in payable pretending to still be on its way. Anything else quietly
// keeps money the platform did not earn.
func Returned(
	ctx context.Context,
	q ledger.Querier,
	user uuid.UUID,
	amount money.Amount,
	withdrawalID uuid.UUID,
	reason string,
) (uuid.UUID, error) {
	if reason == "" {
		return uuid.Nil, fmt.Errorf("movements: a return must say why the destination refused it")
	}

	c := amount.Currency()
	r := newResolver(ctx, q)
	payable := r.account(ledger.System(), ledger.KindPayable, c)
	to := r.account(ledger.User(user), ledger.KindAvailable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, q, ledger.Ref{
		Type:    "withdrawal_returned",
		ID:      &withdrawalID,
		IdemKey: "withdrawal_returned:" + withdrawalID.String(),
	}, []ledger.Entry{
		{AccountID: payable, Amount: amount.Neg(), Reason: "settlement.returned:" + reason},
		{AccountID: to, Amount: amount, Reason: "settlement.refunded_sender"},
	})
}

// MerchantSettled moves a merchant's earnings from what they are owed into the
// queue of things leaving the system, when a payout to their bank is raised.
func MerchantSettled(
	ctx context.Context,
	tx pgx.Tx,
	merchant uuid.UUID,
	amount money.Amount,
	payoutID uuid.UUID,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a merchant payout must be positive, got %s", amount)
	}

	c := amount.Currency()
	r := newResolver(ctx, tx)

	// A merchant cannot be paid out more than they are owed. Spending account
	// first, then its lock, then everything else. See Tap.
	owed := r.account(ledger.Merchant(merchant), ledger.KindMerchantPayable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}
	if err := ensureFunds(ctx, tx, owed, amount); err != nil {
		return uuid.Nil, err
	}

	payable := r.account(ledger.System(), ledger.KindPayable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "merchant_payout",
		ID:      &payoutID,
		IdemKey: "merchant_payout:" + payoutID.String(),
	}, []ledger.Entry{
		{AccountID: owed, Amount: amount.Neg(), Reason: "merchant_payout.claim_settled"},
		{AccountID: payable, Amount: amount, Reason: "merchant_payout.owed_to_bank"},
	})
}

// MerchantPayoutReturned puts a failed payout back to what the merchant is
// owed.
//
// They earned it and we could not deliver it, so it returns to
// merchant_payable rather than staying in `payable` or vanishing. Leaving it
// in payable would be the platform quietly holding money it neither earned nor
// delivered, and it would go on looking like an outstanding obligation nobody
// was acting on.
// MerchantSettledOnChain discharges what a merchant is owed when the payment
// has been sold to a settlement gateway instead of paid from here.
//
// The claim does not move to `payable`, because the platform is not the one
// paying: a liquidity provider is, out of the cardholder's own tokens. Holding
// it in payable would say we owe money we have no way to send, and leaving it
// in merchant_payable would say we still owe it after somebody else has paid.
// It leaves the books entirely, which is what actually happened.
//
// Round is which order for this tap was submitted. It is part of the
// idempotency key because a refunded order is sold again as a new round, and
// that round's discharge is a new movement, not a replay of the first.
func MerchantSettledOnChain(
	ctx context.Context,
	tx pgx.Tx,
	merchant uuid.UUID,
	amount money.Amount,
	tapID uuid.UUID,
	round int,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a settlement must be positive, got %s", amount)
	}

	c := amount.Currency()
	r := newResolver(ctx, tx)

	// A merchant cannot be discharged of more than they are owed. Spending
	// account first, then its lock, then everything else. See Tap.
	owed := r.account(ledger.Merchant(merchant), ledger.KindMerchantPayable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}
	if err := ensureFunds(ctx, tx, owed, amount); err != nil {
		return uuid.Nil, err
	}

	external := r.account(ledger.System(), ledger.KindExternal, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "merchant_settled_onchain",
		ID:      &tapID,
		IdemKey: fmt.Sprintf("merchant_settled_onchain:%s:%d", tapID, round),
	}, []ledger.Entry{
		{AccountID: owed, Amount: amount.Neg(), Reason: "merchant.settled_onchain"},
		{AccountID: external, Amount: amount, Reason: "merchant.paid_by_provider"},
	})
}

// MerchantSettlementRefunded puts a merchant's claim back after the Gateway
// refunded the order that was meant to pay it.
//
// The exact mirror of MerchantSettledOnChain. That movement said a provider
// had paid the merchant; the refund says nobody did, and the cardholder's
// tokens went back to their own account. The claim therefore returns to
// merchant_payable, where it is a liability the audit can see, rather than
// staying discharged against a payment that never happened.
//
// Only the merchant's side moves. The cardholder was charged at the till and
// the tap stands: they have their goods, and the tokens that came back are
// still the ones that pay for them. What happens next is either another order
// or an operator's decision, and neither is this movement's business.
func MerchantSettlementRefunded(
	ctx context.Context,
	tx pgx.Tx,
	merchant uuid.UUID,
	amount money.Amount,
	tapID uuid.UUID,
	round int,
	reason string,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a refunded settlement must be positive, got %s", amount)
	}
	if reason == "" {
		return uuid.Nil, fmt.Errorf("movements: a refunded settlement must say why")
	}

	c := amount.Currency()
	r := newResolver(ctx, tx)

	// No funds check on external: it is the outside world's contra account
	// and its sign says nothing about what can be taken back. That an order
	// was discharged before it is refunded is the tracker's invariant,
	// enforced by the settlement row's state and this movement's key.
	external := r.account(ledger.System(), ledger.KindExternal, c)
	owed := r.account(ledger.Merchant(merchant), ledger.KindMerchantPayable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "merchant_settlement_refunded",
		ID:      &tapID,
		IdemKey: fmt.Sprintf("merchant_settlement_refunded:%s:%d", tapID, round),
	}, []ledger.Entry{
		{AccountID: external, Amount: amount.Neg(), Reason: "merchant.refunded_by_gateway:" + reason},
		{AccountID: owed, Amount: amount, Reason: "merchant.still_owed"},
	})
}

func MerchantPayoutReturned(
	ctx context.Context,
	tx pgx.Tx,
	merchant uuid.UUID,
	amount money.Amount,
	payoutID uuid.UUID,
	reason string,
) (uuid.UUID, error) {
	if reason == "" {
		return uuid.Nil, fmt.Errorf("movements: a returned payout must say why")
	}

	c := amount.Currency()
	r := newResolver(ctx, tx)

	payable := r.account(ledger.System(), ledger.KindPayable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}
	if err := ensureFunds(ctx, tx, payable, amount); err != nil {
		return uuid.Nil, err
	}

	owed := r.account(ledger.Merchant(merchant), ledger.KindMerchantPayable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "merchant_payout_returned",
		ID:      &payoutID,
		IdemKey: "merchant_payout_returned:" + payoutID.String(),
	}, []ledger.Entry{
		{AccountID: payable, Amount: amount.Neg(), Reason: "merchant_payout.returned:" + reason},
		{AccountID: owed, Amount: amount, Reason: "merchant_payout.still_owed"},
	})
}

// -----------------------------------------------------------------------------
// Settling a tap's naira leg from the cardholder's own wallet
//
// A tap paid from a naira balance is settled by paying the merchant's bank
// straight out of the cardholder's own wallet at the rail. The platform is
// not the payer, any more than it is when USDC is sold on chain: the money
// goes from the cardholder's asset to the merchant's bank, and the books say
// the same thing they say for the on-chain leg -- the claim leaves entirely
// when the rail is asked, and comes back if the rail refuses. Keyed on the
// tap, so its own timeline (transactions.Events) shows every step.
//
// Attempt is which ask of the rail this is. A failed attempt returns the
// claim; a retry discharges it again, and that is a new movement, not a
// replay of the first.
// -----------------------------------------------------------------------------

// MerchantSettledFromWallet discharges the naira leg of what a merchant is
// owed, when the cardholder's wallet is asked to pay it.
func MerchantSettledFromWallet(
	ctx context.Context,
	tx pgx.Tx,
	merchant uuid.UUID,
	amount money.Amount,
	tapID uuid.UUID,
	attempt int,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a wallet settlement must be positive, got %s", amount)
	}

	c := amount.Currency()
	r := newResolver(ctx, tx)

	// A merchant cannot be discharged of more than they are owed. See Tap.
	owed := r.account(ledger.Merchant(merchant), ledger.KindMerchantPayable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}
	if err := ensureFunds(ctx, tx, owed, amount); err != nil {
		return uuid.Nil, err
	}

	external := r.account(ledger.System(), ledger.KindExternal, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "merchant_settled_wallet",
		ID:      &tapID,
		IdemKey: fmt.Sprintf("merchant_settled_wallet:%s:%d", tapID, attempt),
	}, []ledger.Entry{
		{AccountID: owed, Amount: amount.Neg(), Reason: "merchant.settled_from_wallet"},
		{AccountID: external, Amount: amount, Reason: "merchant.paid_by_cardholder_wallet"},
	})
}

// MerchantWalletSettlementReturned puts the naira leg's claim back after the
// rail refused to pay it. The exact mirror of MerchantSettledFromWallet, for
// the same reason MerchantSettlementRefunded mirrors the on-chain discharge:
// nobody paid, and the claim belongs where the audit can see it.
func MerchantWalletSettlementReturned(
	ctx context.Context,
	tx pgx.Tx,
	merchant uuid.UUID,
	amount money.Amount,
	tapID uuid.UUID,
	attempt int,
	reason string,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a returned wallet settlement must be positive, got %s", amount)
	}
	if reason == "" {
		return uuid.Nil, fmt.Errorf("movements: a returned wallet settlement must say why")
	}

	c := amount.Currency()
	r := newResolver(ctx, tx)
	external := r.account(ledger.System(), ledger.KindExternal, c)
	owed := r.account(ledger.Merchant(merchant), ledger.KindMerchantPayable, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "merchant_wallet_settlement_returned",
		ID:      &tapID,
		IdemKey: fmt.Sprintf("merchant_wallet_settlement_returned:%s:%d", tapID, attempt),
	}, []ledger.Entry{
		{AccountID: external, Amount: amount.Neg(), Reason: "merchant.wallet_refused:" + reason},
		{AccountID: owed, Amount: amount, Reason: "merchant.still_owed"},
	})
}

// RailFeesPaid books what the rail charged to deliver a tap's naira leg:
// the sweep fee taken from the cardholder's wallet on top of the sweep, and
// the bank transfer fee taken from the platform's wallet. Both come out of
// the scheme fee the tap earned, which is the only money of the platform's
// in either wallet; a tap too small for its fee to cover them leaves the
// platform down the difference, and the books say so.
//
// Keyed on the tap and the attempt that settled, so a redelivered
// confirmation books nothing twice.
func RailFeesPaid(
	ctx context.Context,
	tx pgx.Tx,
	fees money.Amount,
	tapID uuid.UUID,
	attempt int,
) (uuid.UUID, error) {
	if !fees.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: rail fees must be positive, got %s", fees)
	}

	c := fees.Currency()
	r := newResolver(ctx, tx)
	revenue := r.account(ledger.System(), ledger.KindRevenue, c)
	external := r.account(ledger.System(), ledger.KindExternal, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, tx, ledger.Ref{
		Type:    "rail_fees_paid",
		ID:      &tapID,
		IdemKey: fmt.Sprintf("rail_fees_paid:%s:%d", tapID, attempt),
	}, []ledger.Entry{
		{AccountID: revenue, Amount: fees.Neg(), Reason: "scheme_fee.rail_fees"},
		{AccountID: external, Amount: fees, Reason: "rail.fees_charged"},
	})
}
