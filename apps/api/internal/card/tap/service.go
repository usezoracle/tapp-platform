package tap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/card/auth"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/rates"
)

// Failed-PIN policy. Five attempts, then a day's lockout -- long enough to
// make guessing a four-digit PIN pointless, short enough that a cardholder who
// simply forgot theirs is not permanently cut off.
const (
	PINAttempts   = 5
	PINLockWindow = 24 * time.Hour

	// A card that repeatedly presents a token we do not recognise is either
	// cloned or badly out of sync. Either way it stops transacting until a
	// human looks.
	MismatchesBeforeLock = 3
)

var (
	// ErrStepUpRequired means the cardholder has not yet approved this amount
	// in their own app.
	ErrStepUpRequired = errors.New("cardholder approval required")

	// ErrDailyLimitReached means this tap would take the card past its daily
	// limit.
	ErrDailyLimitReached = errors.New("daily card limit reached")

	// ErrTokenStale means the card presented a token we do not hold. The
	// cardholder is told to re-sync; repeated occurrences lock the card.
	ErrTokenStale = errors.New("card must be re-synced")

	// ErrIdentityLimitReached means the amount is beyond what this person's
	// verification supports. Distinct from the card's own daily limit, because
	// the way out differs: one is waiting until tomorrow, the other is
	// verifying an identity.
	ErrIdentityLimitReached = errors.New("identity verification limit reached")

	// ErrCannotPrice means the balance is held in another currency and no rate
	// was available to buy the spend.
	//
	// A refusal, not a fault: the cardholder has the money and the platform
	// cannot currently say what it is worth. Guessing a rate at a till is how
	// somebody is charged a price nobody quoted.
	ErrCannotPrice = errors.New("cannot price this amount right now")
)

// FeePolicy decides the platform's cut of a tap.
type FeePolicy interface {
	FeeFor(amount money.Amount) money.Amount
}

// BasisPointFee charges a flat rate in basis points.
type BasisPointFee int

func (b BasisPointFee) FeeFor(a money.Amount) money.Amount { return money.FeeFor(a, int(b)) }

// Limiter answers whether a person's identity supports an amount. Optional:
// a service with none enforces only the card's own limits, which is the
// correct behaviour before KYC is switched on rather than a silent bypass --
// the card limits are still enforced, and they are lower.
type Limiter interface {
	Check(ctx context.Context, user uuid.UUID, amount money.Amount) (allowed bool, reason string, err error)
}

// Service performs card payments.
// Quoter prices the conversion a tap needs. Satisfied by rates.Quoter.
//
// OfferForBuy rather than Offer: the till knows the naira it must collect,
// not the dollars that will pay for it.
type Quoter interface {
	OfferForBuy(ctx context.Context, sell money.Currency, buy money.Amount) (*rates.Quote, error)
	Redeem(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*rates.Quote, error)
}

type Service struct {
	Pool   *pgxpool.Pool
	Fee    FeePolicy
	Limits Limiter

	// Funding is the currency balances are actually held in.
	//
	// Deposits arrive as USDC and stay dollars: converting on the way in would
	// put the platform long naira for money nobody has spent yet. So the
	// exchange happens here, at the till, for exactly the amount being spent
	// -- which is also the only moment a rate has been agreed to by anybody.
	//
	// Zero means balances are already in the tap's currency and no conversion
	// is attempted.
	Funding money.Currency

	// Quoter prices Funding -> the tap currency. Required when they differ; a
	// tap that cannot be priced is refused rather than guessed at.
	Quoter Quoter

	// Settle records that a tap must be sold on chain, in the tap's own
	// transaction.
	//
	// A function rather than a dependency, because this package must not know
	// what a settlement gateway is: it charges cards, and the fact that the
	// charge is later financed by selling a token belongs to whoever wired
	// the two together. Nil means settlement is handled elsewhere.
	Settle func(ctx context.Context, tx pgx.Tx, tapID, cardholder uuid.UUID, amount money.Amount) error

	// Equity records that a tap must be reported to the equity market, in
	// the tap's own transaction, and EquityReversal the same for a reversal.
	//
	// The same shape as Settle, for the same reason: this package charges
	// cards and must not know that a market exists, let alone how to reach
	// one. Whoever wires the two together decides what "report" means; here
	// it is one row in an outbox that a worker drains later, so a market
	// that is slow or down cannot hold up a till. Nil means no market.
	Equity         func(ctx context.Context, tx pgx.Tx, e Charged) error
	EquityReversal func(ctx context.Context, tx pgx.Tx, tapID uuid.UUID, reason string) error

	// Now is injectable so lockout and daily-window behaviour can be tested
	// without waiting a day. Nil means time.Now.
	Now func() time.Time
}

// fundTap makes sure the cardholder holds the spend in the tap's currency,
// buying only what they are short, and reports whether it succeeded.
//
// (false, nil) means it could not be priced -- a refusal the caller turns into
// ErrCannotPrice. (true, nil) with no conversion means none was needed.
//
// Redeemed inside the caller's transaction so one quote prices exactly one
// tap, and posted there too so the exchange and the spend commit together.
func (s *Service) fundTap(
	ctx context.Context, tx pgx.Tx, cardholder uuid.UUID, spend money.Amount,
) (bool, error) {
	if s.Funding == "" || s.Funding == spend.Currency() {
		return true, nil
	}
	if s.Quoter == nil {
		return false, nil
	}

	// Buy only the shortfall.
	//
	// The cardholder may already hold some of the tap currency, because a
	// funding currency with coarser minor units cannot buy an exact amount:
	// a US cent is worth about thirteen naira, so buying ₦1,500 means buying
	// the next whole cent up and keeping the difference. Ignoring that and
	// buying the full spend every time would leave the remainder behind on
	// every tap, accumulating dust the holder can see and never spends.
	held, err := ledger.Balance(ctx, tx, ledger.User(cardholder), ledger.KindAvailable, spend.Currency())
	if err != nil {
		return false, err
	}
	if held.Minor() >= spend.Minor() {
		// Already covered by what is on hand; no conversion, no quote.
		return true, nil
	}
	shortfall, err := spend.Sub(held)
	if err != nil {
		return false, err
	}

	quote, err := s.Quoter.OfferForBuy(ctx, s.Funding, shortfall)
	if err != nil {
		// A rate source that is down or a pair with no spread is not a card
		// problem, and not something to invent a number for.
		return false, nil
	}
	redeemed, err := s.Quoter.Redeem(ctx, tx, quote.ID)
	if err != nil {
		return false, err
	}
	if _, err := movements.Convert(ctx, tx, cardholder, movements.Conversion{
		Sold: redeemed.Sell, Bought: redeemed.Buy, Spread: redeemed.Fee,
		QuoteID: redeemed.ID.String(),
	}); err != nil {
		// Insufficient funds here is the honest answer to the tap: the
		// cardholder does not hold enough of the funding currency to buy what
		// they are spending. It surfaces as a decline, not an error.
		return false, err
	}
	return true, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ChallengeRequest asks what a proposed amount will require.
type ChallengeRequest struct {
	CardUIDHash []byte
	MerchantID  uuid.UUID
	Amount      money.Amount
}

// Challenge resolves the authentication tier for an amount and issues a
// single-use nonce bound to it.
//
// The tier is decided HERE and stored on the nonce, not recomputed at debit
// time. That is what stops a merchant asking for a challenge on ₦500, being
// told no PIN is needed, and then presenting a debit for ₦50,000.
func (s *Service) Challenge(ctx context.Context, req ChallengeRequest) (*Challenge, error) {
	if !req.Amount.IsPositive() {
		return nil, fmt.Errorf("tap: an amount must be positive, got %s", req.Amount)
	}

	var ch *Challenge
	err := movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		now := s.now()
		k, err := loadCard(ctx, tx, req.CardUIDHash, req.Amount.Currency())
		if err != nil {
			return err
		}
		if err := k.usable(now); err != nil {
			return err
		}
		if err := refuseRepeat(ctx, tx, k.ID, req.MerchantID, req.Amount, now); err != nil {
			return err
		}

		tier, err := k.Limits.TierFor(req.Amount)
		if err != nil {
			return err
		}

		nonce, err := newNonce()
		if err != nil {
			return err
		}
		expires := now.Add(NonceTTL)

		var id uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO card_server_nonces
				(id, created_at, updated_at, nonce, tier, amount, currency, expires_at,
				 tapp_card_server_nonces, sender_profile_card_server_nonces)
			VALUES (gen_random_uuid(), now(), now(), $1, $2, $3, $4, $5, $6, $7)
			RETURNING id`,
			nonce, string(tier), formatMinor(req.Amount), string(req.Amount.Currency()),
			expires, k.ID, req.MerchantID).Scan(&id)
		if err != nil {
			return fmt.Errorf("tap: issue challenge: %w", err)
		}

		ch = &Challenge{
			ID: id, Nonce: nonce, Tier: tier,
			Amount: req.Amount, ExpiresAt: expires,
		}
		if tier == auth.TierStepUp {
			// The cardholder approves in their own app, which identifies the
			// pending approval by this reference.
			ch.StepUpRef = id.String()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// Request is a debit.
type Request struct {
	CardUIDHash    []byte
	PresentedToken []byte
	Nonce          []byte
	MerchantID     uuid.UUID
	Amount         money.Amount

	// PINResponse answers the challenge for a TierPIN tap.
	PINResponse []byte
	// StepUpRef is echoed back for a TierStepUp tap.
	StepUpRef string
}

// Charged is what a tap's transaction knows about the charge it just made,
// handed to the Equity hook.
type Charged struct {
	TapID      uuid.UUID
	Cardholder uuid.UUID
	Merchant   uuid.UUID
	Amount     money.Amount
	At         time.Time
}

// Receipt is what the merchant app needs after a successful debit.
type Receipt struct {
	TapID      uuid.UUID
	LedgerTxID uuid.UUID
	Amount     money.Amount
	Fee        money.Amount
	Tier       auth.Tier
	// NewToken must be written to the card. Until the write is acknowledged
	// the card's previous token also remains valid, so a failed write costs
	// nothing.
	NewToken       []byte
	RemainingDaily money.Amount
}
