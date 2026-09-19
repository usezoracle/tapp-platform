package v1

import (
	"context"
	"sync"
	"time"

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
	paycrest "github.com/usezoracle/tapp/api/services/settlement"
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
// settlement rows accumulate until a gateway is configured and a later tick
// drains them. What must never happen is a tap failing because the chain is
// unreachable -- the cardholder is at a till and their money has already
// moved.
func SharedSettler() *offramp.Settler {
	settlerOnce.Do(func() {
		gateway := viper.GetString("BASE_GATEWAY_CONTRACT")
		if gateway == "" {
			logger.Infof("offramp: no BASE_GATEWAY_CONTRACT -- taps will charge and " +
				"queue for settlement, but nothing will be sold")
			return
		}

		cdpCfg := config.CDPConfig()
		if !cdpCfg.Enabled() {
			logger.Errorf("offramp: a gateway is configured but CDP is not, so no " +
				"cardholder account can sign an order")
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

		keys := paycrest.New(
			config.OrderConfig().SettlementAPIURL+"/v1",
			time.Duration(config.OrderConfig().SettlementPubkeyTTLSeconds)*time.Second,
		)

		// Outcomes are read through the rail's own client: one connection to
		// the chain, and the settler cannot exist without the deposit
		// accounts it sells from anyway.
		var reader offramp.Reader
		if r := Rail(); r != nil && r.Chain != nil && r.Chain.Client != nil {
			reader = r.Chain.Client
		} else {
			logger.Errorf("offramp: no Base rail, so orders can be created but " +
				"never followed to fulfilled or refunded")
		}

		settler = &offramp.Settler{
			Pool:  storage.Pool,
			Price: priceWith(keys, settlementNetwork(chainID)),
			Orders: &offramp.Client{
				Sender:  signer,
				Keys:    keys,
				Reader:  reader,
				Gateway: common.HexToAddress(gateway),
				USDC:    common.HexToAddress(viper.GetString("BASE_USDC_CONTRACT")),
				// No on-chain sender fee.
				//
				// The platform's margin is already taken at the till, booked
				// to revenue in fiat when the tap posts. Charging it again
				// here would take the same cut twice -- once in the ledger and
				// once out of the tokens -- and the second one would come out
				// of what the merchant receives.
				//
				// A deployment that would rather earn on chain sets a
				// recipient and drops the card fee, but it must be one or the
				// other.
				SenderFeeBPS: 0,
				FeeRecipient: common.HexToAddress(viper.GetString("BASE_FEE_RECIPIENT")),
			},
		}
		logger.Infof("offramp: card taps settle through gateway %s on chain %d, "+
			"sold from each cardholder's own account", gateway, chainID)
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
//     that USDC on chain to the settlement gateway, where a liquidity
//     provider pays the merchant.
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

		if ngn.IsPositive() {
			if err := naira.Record(ctx, tx, c.TapID, c.Cardholder, c.Merchant, nairaRail(), ngn); err != nil {
				return err
			}
		}
		if !usdc.IsPositive() {
			return nil
		}

		if s == nil {
			// No gateway: the tap charges and nothing is recorded for this
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

// priceWith prices an order at the aggregator's own sell-side rate for the
// amount being sold, which is the only rate its matcher will accept.
//
// The rate depends on the amount -- providers serve different size brackets
// at different prices -- and the amount depends on the rate. One pass from
// the recorded estimate lands in the right bracket almost always; a second
// pass with the amount that pass produced catches the case where it did not.
// The sale is rounded UP, so the order delivers at least what the merchant
// is owed, and the order's rate (what it delivers over what it sells) sits
// a fraction of a kobo under the quote rather than over it.
func priceWith(p *paycrest.Client, network string) func(context.Context, money.Amount, int64) (int64, error) {
	return func(ctx context.Context, owed money.Amount, estimate int64) (int64, error) {
		fiat := decimal.NewFromInt(owed.Minor()).Div(decimal.NewFromInt(owed.Currency().Scale()))
		sell := decimal.NewFromInt(estimate).Div(decimal.New(1, 6))

		for pass := 0; pass < 2; pass++ {
			q, err := p.FetchRate(ctx, network, "USDC", sell, string(owed.Currency()))
			if err != nil {
				return 0, err
			}
			if q.Rate.Sign() <= 0 {
				return 0, errNoRate
			}
			next := fiat.Div(q.Rate).Mul(decimal.New(1, 6)).Ceil().Div(decimal.New(1, 6))
			if next.Equal(sell) {
				break
			}
			sell = next
		}

		micro := sell.Mul(decimal.New(1, 6))
		if micro.Sign() <= 0 || !micro.BigInt().IsInt64() {
			return 0, errNoRate
		}
		return micro.BigInt().Int64(), nil
	}
}

// settlementNetwork is the aggregator's name for a chain, which is not its id.
func settlementNetwork(chainID int64) string {
	switch chainID {
	case 84532:
		return "base-sepolia"
	default:
		return "base"
	}
}

var (
	errNoDepositAddress = &settlementError{"the cardholder has no deposit address to settle from"}
	errNoRate           = &settlementError{"no rate to price the settlement"}
)

type settlementError struct{ msg string }

func (e *settlementError) Error() string { return "offramp: " + e.msg }
