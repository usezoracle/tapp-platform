package v1

import (
	"context"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"github.com/spf13/viper"

	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/internal/card/tap"
	"github.com/usezoracle/tapp/api/internal/chain/cdp"
	"github.com/usezoracle/tapp/api/internal/chain/offramp"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/rates"
	"github.com/usezoracle/tapp/api/internal/settlement/naira"
	"github.com/usezoracle/tapp/api/services/baas"
	"github.com/usezoracle/tapp/api/services/onswitch"
	"github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/utils/logger"
)

var (
	settlerOnce sync.Once
	settler     *offramp.Settler
)

// SharedSettler builds the tap settler, or returns nil when this deployment
// cannot sell.
//
// Nil is a working state, not a broken one: taps still charge, and their
// settlement rows accumulate until Switch is configured and a later tick
// drains them. What must never happen is a tap failing because a provider is
// unreachable -- the cardholder is at a till and their money has already
// moved.
func SharedSettler() *offramp.Settler {
	settlerOnce.Do(func() {
		switchCfg := config.SwitchConfig()
		if !switchCfg.Enabled() {
			logger.Infof("offramp: no SWITCH_SERVICE_KEY -- taps will charge and " +
				"queue for settlement, but nothing will be sold")
			return
		}

		cdpCfg := config.CDPConfig()
		if !cdpCfg.Enabled() {
			logger.Errorf("offramp: Switch is configured but CDP is not, so no " +
				"cardholder account can fund a payout")
			return
		}
		chainID := config.OrderConfig().BaseChainID
		signer, err := cdp.New(cdp.Config{
			APIKeyID: cdpCfg.APIKeyID, APIKeySecret: cdpCfg.APIKeySecret,
			WalletSecret: cdpCfg.WalletSecret, PaymasterURL: cdpCfg.PaymasterURL,
			BaseURL: cdpCfg.BaseURL,
		}, chainID)
		if err != nil {
			logger.Errorf("offramp: cdp: %v", err)
			return
		}

		orders := &offramp.Client{
			Sender: signer,
			Switch: onswitch.New(switchCfg.BaseURL, switchCfg.ServiceKey),
			USDC:   common.HexToAddress(viper.GetString("BASE_USDC_CONTRACT")),
		}
		// A cardholder whose USDC is in the yield vault has it released
		// before the payout is funded. Assigned only when there is one, so
		// the interface is nil, not a nil *yield.Service.
		if r := Rail(); r != nil && r.Yield != nil {
			orders.Liquidity = r.Yield
		}
		settler = &offramp.Settler{Pool: storage.Pool, Orders: orders}
		logger.Infof("offramp: card taps settle through Switch (%s), "+
			"sold from each cardholder's own account on chain %d", switchCfg.BaseURL, chainID)
	})
	return settler
}

// RecordTapSettlement adapts the settlement rails to what tap.Service calls,
// and splits what the merchant is owed between them.
//
// The split turns on where the tap's money came from, which only the tap
// knows (tap.Charged.Legs):
//
//   - what the cardholder paid from a naira balance is paid to the merchant
//     straight out of the cardholder's own wallet at the bank rail;
//   - what the cardholder had to buy by converting USDC is paid by selling
//     that USDC to Switch, which pays the merchant's bank.
//
// One tap can have both legs, and each is recorded in the tap's own
// transaction. Either leg that cannot be recorded -- no wallet, no bank
// account, no deposit address -- fails the tap, because a charge that can
// never reach the merchant is worse than a decline.
//
// The conversion from what the merchant is owed to what the cardholder must
// sell happens here, at the wiring layer, because it is the one place that
// knows both the card and the chain. The tap package does not learn what a
// token is, and neither settlement package learns what a card is.
func RecordTapSettlement(s *offramp.Settler) func(context.Context, pgx.Tx, tap.Charged) error {
	return func(ctx context.Context, tx pgx.Tx, c tap.Charged) error {
		ngn, usdc := c.Legs()

		// A naira share too small for the bank rail to move -- it does not
		// cover the ₦15 sweep fee -- would fail on every attempt and hold
		// the tap unsettled forever. A share that small is the remainder of
		// earlier conversions bought in whole cents, and Switch sold only
		// what each of those taps delivered, so the USDC behind it is still
		// in the cardholder's account on chain: Switch pays it, with the
		// rest. Only alongside a USDC leg, because
		// Switch will not open a payout for a few naira on its own.
		if ngn.IsPositive() && usdc.IsPositive() && !SharedNairaWorker().Covers(c.Funding.NGN) {
			ngn, usdc = money.Zero(ngn.Currency()), c.Owed()
		}

		if ngn.IsPositive() {
			if err := naira.Record(ctx, tx, c.TapID, c.Cardholder, c.Merchant, nairaRail(), ngn, c.Funding.NGN); err != nil {
				return err
			}
		}
		if !usdc.IsPositive() {
			return nil
		}

		if s == nil {
			// No Switch: the tap charges and nothing is recorded for this
			// leg, as before the naira leg existed. See SharedSettler.
			return nil
		}
		address, _, ok, err := Rail().Addresses.Current(ctx, tx, c.Cardholder)
		if err != nil {
			return err
		}
		if !ok {
			// No deposit address means no USDC to sell. The tap has already
			// charged their ledger balance, so this is a genuine
			// inconsistency worth failing the tap over rather than charging
			// somebody with nothing to settle from.
			return errNoDepositAddress
		}

		sell, err := sellFor(ctx, usdc)
		if err != nil {
			return err
		}
		if sell < offramp.MinSell*102/100 {
			// Switch would refuse the payout on every round, leaving the
			// merchant unpaid and the tap's stock held for good. Declined
			// here, the charge rolls back and the card is not charged. The
			// 2% is headroom for the mid differing from the rate Switch
			// prices the payout at a moment later: a tap that only just
			// clears the minimum here must clear it there too.
			return offramp.ErrBelowMinimum
		}
		return s.Record(ctx, tx, c.TapID, address, sell, usdc)
	}
}

// nairaRail names the rail cardholders' naira wallets were opened on, which
// is the one their naira legs are paid from. Empty when none is configured,
// in which case a naira leg cannot be recorded and the tap is refused.
func nairaRail() string {
	if r := baas.Default(); r != nil {
		return r.Name()
	}
	return ""
}

// sellFor is how much USDC covers what the merchant is owed.
//
// Priced through the same quoter the tap itself used, so the amount sold and
// the amount charged were struck against one rate. The MID is used rather than
// a fresh quote: the spread was already taken when the cardholder's balance
// was converted, and taking it twice would charge them for one exchange at two
// prices.
func sellFor(ctx context.Context, owed money.Amount) (int64, error) {
	q := SharedQuoter()
	if q == nil {
		return 0, errNoRate
	}
	pair := rates.Pair{Base: money.USD, Quote: owed.Currency()}
	market, err := q.Engine.Market(ctx, pair)
	if err != nil {
		return 0, err
	}
	if market.Mid.Sign() <= 0 {
		return 0, errNoRate
	}

	// Whole fiat units, divided by the price of one token, scaled into USDC's
	// six decimals. Rounded UP: a sale that rounds down leaves the order
	// short of what the merchant is owed, and the provider fills what the
	// order says.
	fiat := decimal.NewFromInt(owed.Minor()).Div(decimal.NewFromInt(owed.Currency().Scale()))
	tokens := fiat.Div(market.Mid)
	micro := tokens.Mul(decimal.New(1, 6)).Ceil()
	if micro.Sign() <= 0 || !micro.BigInt().IsInt64() {
		return 0, errNoRate
	}
	return micro.BigInt().Int64(), nil
}

var (
	errNoDepositAddress = &settlementError{"the cardholder has no deposit address to settle from"}
	errNoRate           = &settlementError{"no rate to price the settlement"}
)

type settlementError struct{ msg string }

func (e *settlementError) Error() string { return "offramp: " + e.msg }
