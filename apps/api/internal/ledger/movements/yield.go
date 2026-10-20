package movements

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/money"
)

// YieldCredited brings in what a cardholder's vault position earned: value
// from outside, like a deposit, against `external`. The USDC never moved --
// the vault's shares grew in the cardholder's own account -- so this is the
// ledger catching up with what they already hold.
//
// The reason is a deposit's, with ":yield" after it, so every client that
// names a deposit names this one ("USD received") without being told about
// yield. day keys it: one credit per cardholder per UTC day.
func YieldCredited(
	ctx context.Context,
	q ledger.Querier,
	user uuid.UUID,
	amount money.Amount,
	day string,
) (uuid.UUID, error) {
	if !amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("movements: a yield credit must be positive, got %s", amount)
	}
	if day == "" {
		return uuid.Nil, fmt.Errorf("movements: a yield credit needs its day to be idempotent")
	}

	c := amount.Currency()
	r := newResolver(ctx, q)
	to := r.account(ledger.User(user), ledger.KindAvailable, c)
	external := r.account(ledger.System(), ledger.KindExternal, c)
	if r.err != nil {
		return uuid.Nil, r.err
	}

	return ledger.Post(ctx, q, ledger.Ref{
		Type:    "yield",
		IdemKey: "yield:" + user.String() + ":" + day,
	}, []ledger.Entry{
		{AccountID: to, Amount: amount, Reason: "deposit.credited:yield"},
		{AccountID: external, Amount: amount.Neg(), Reason: "yield.from_vault"},
	})
}
