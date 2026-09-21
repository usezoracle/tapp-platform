package v1

import (
	"context"
	"fmt"
	"time"

	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/spf13/viper"

	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/internal/chain/base"
	"github.com/usezoracle/tapp/api/internal/chain/cdp"
	"github.com/usezoracle/tapp/api/internal/chain/gas"
	"github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// BaseRail is everything the USDC rail needs, assembled once.
type BaseRail struct {
	// Gas records what on-chain work costs and watches the balance that pays
	// for it. Always built when the rail is: gas is spent whether or not
	// anybody is counting, and the point is to stop that being invisible.
	Gas       *gas.Recorder
	GasWallet *gas.Wallet

	Addresses   *base.Addresses
	Deposits    *base.Deposits
	Watcher     *base.Watcher
	Sweeper     *base.Sweeper
	Withdrawals *base.Withdrawals
	Chain       *base.Chain
	ChainID     int64
}

// NewBaseRail builds the deposit rail from configuration.
//
// Returns nil when it is not configured, and the deposit routes are then not
// registered. A deposit address that cannot be watched is an address people
// send money to that nobody credits, which is worse than a missing feature by
// a wide margin.
//
// The seed is REQUIRED when the rail is on, with no default. A fixed fallback
// would put every deployment's deposits under one key -- the same failure as
// the per-user keys this replaces, just concentrated.
func NewBaseRail(ctx context.Context) (*BaseRail, error) {
	rpcURL := viper.GetString("BASE_RPC_URL")
	usdc := viper.GetString("BASE_USDC_CONTRACT")
	seedHex := viper.GetString("BASE_DEPOSIT_SEED")

	if rpcURL == "" || usdc == "" || seedHex == "" {
		logger.Infof("base: not configured (needs BASE_RPC_URL, BASE_USDC_CONTRACT, " +
			"BASE_DEPOSIT_SEED) -- USDC deposits are not available")
		return nil, nil
	}
	if !common.IsHexAddress(usdc) {
		return nil, fmt.Errorf("BASE_USDC_CONTRACT is not an address: %q", usdc)
	}

	seed, err := base.ParseSeed(seedHex)
	if err != nil {
		return nil, fmt.Errorf("BASE_DEPOSIT_SEED: %w", err)
	}
	deriver, err := base.NewDeriver(seed)
	if err != nil {
		return nil, err
	}

	chainID := viper.GetInt64("BASE_CHAIN_ID")
	chain, err := base.NewChain(ctx, rpcURL, usdc, viper.GetString("BASE_TREASURY_KEY"), chainID)
	if err != nil {
		return nil, err
	}
	client := chain.Client

	addresses := &base.Addresses{Pool: storage.Pool, Deriver: deriver}

	// Every deposit address is a CDP Smart Account. The seed still SPENDS the
	// derived addresses it issued -- they stay watched and swept -- but it no
	// longer issues new ones.
	//
	// A partial configuration is refused here so that main.go fails the boot:
	// an operator who set two of three secrets meant to turn this on, and a
	// rail that quietly carried on minting seed-derived addresses would hide
	// that from them until the day the seed had to be produced.
	var smart base.SmartAccounts
	if cdpCfg := config.CDPConfig(); cdpCfg.Enabled() {
		client, err := cdp.New(cdp.Config{
			APIKeyID: cdpCfg.APIKeyID, APIKeySecret: cdpCfg.APIKeySecret,
			WalletSecret: cdpCfg.WalletSecret, PaymasterURL: cdpCfg.PaymasterURL,
			BaseURL: cdpCfg.BaseURL,
		}, chainID)
		if err != nil {
			return nil, err
		}
		smart = client
		addresses.SmartAccounts = client
		logger.Infof("base: new deposit addresses are CDP smart accounts on chain %d, gas sponsored", chainID)
	} else {
		// Not a fallback to the seed. Deposits already taken still credit and
		// still sweep; what cannot happen is issuing anybody a new address.
		logger.Infof("base: CDP is not configured -- existing deposit addresses still " +
			"credit and sweep, but no new address can be issued")
	}
	// Deposits are credited in the currency they arrive as: USDC is dollars,
	// so balances are dollars. Converting on the way in would put the platform
	// long naira from the moment somebody deposited, for money they might
	// never spend. The exchange happens where the spending does -- at the
	// till, for the amount actually being spent -- and balances are shown in
	// naira at the live rate without being held in it.
	deposits := &base.Deposits{
		Pool: storage.Pool, Addresses: addresses,
		Confirmations: uint64(viper.GetInt("BASE_CONFIRMATIONS")),
		// So money returned from the treasury, or refunded by the settlement
		// gateway, is not credited as a fresh deposit. See Deposits.Record.
		Treasury: chain.Treasury,
		Gateway:  common.HexToAddress(viper.GetString("BASE_GATEWAY_CONTRACT")),
	}

	if !chain.CanSend() {
		// Deposits still credit correctly without a treasury key; nothing can
		// leave, and saying so at boot beats discovering it at a withdrawal.
		logger.Infof("base: no BASE_TREASURY_KEY -- deposits will be credited but not swept, " +
			"and USDC withdrawals are unavailable")
	}

	recorder := &gas.Recorder{Pool: storage.Pool, Client: client, ChainID: chainID}

	// The sweeper reports what each sweep cost, after the sweep is recorded.
	sweeper := &base.Sweeper{Pool: storage.Pool, Chain: chain, Deriver: deriver, SmartAccounts: smart}
	sweeper.OnSpend = func(ctx context.Context, depositID uuid.UUID, txHash string) {
		id := depositID
		if _, err := recorder.Record(ctx, "sweep", &id, txHash); err != nil {
			// Never fatal to the sweep: the money has moved and the deposit
			// row already says so. An unrecorded cost is a gap in the books,
			// which is worth an error and not a rollback.
			logger.Errorf("gas: could not record sweep cost for %s: %v", txHash, err)
		}
	}

	return &BaseRail{
		Gas: recorder,
		GasWallet: &gas.Wallet{
			Pool: storage.Pool, Client: client,
			Address: chain.Treasury, ChainID: chainID,
			LowWei:           big.NewInt(viper.GetInt64("BASE_NATIVE_LOW_THRESHOLD_WEI")),
			MaxRefillsPerDay: viper.GetInt("GAS_MAX_REFILLS_PER_DAY"),
		},
		Addresses: addresses,
		Deposits:  deposits,
		Chain:     chain,
		ChainID:   chainID,
		Watcher: &base.Watcher{
			Pool: storage.Pool, Client: client,
			USDC:       common.HexToAddress(usdc),
			Deposits:   deposits,
			StartBlock: uint64(viper.GetInt64("BASE_START_BLOCK")),
		},
		Sweeper: sweeper,
		// Paid from the person's own deposit address, sponsored, because
		// nothing is swept into the treasury any more. Addresses and
		// SmartAccounts come from the same objects the deposit side uses, so
		// a withdrawal cannot disagree with a deposit about where somebody's
		// money is.
		Withdrawals: &base.Withdrawals{
			Pool: storage.Pool, Chain: chain,
			Addresses:     addresses,
			SmartAccounts: smart,
		},
	}, nil
}

// PollInterval is how often the chain is read.
func BasePollInterval() time.Duration {
	viper.SetDefault("BASE_POLL_INTERVAL_SECONDS", 15)
	seconds := viper.GetInt("BASE_POLL_INTERVAL_SECONDS")
	if seconds < 2 {
		seconds = 2
	}
	return time.Duration(seconds) * time.Second
}

// rail is the process-wide Base rail, built once at startup.
//
// A package-level value rather than a parameter threaded through the router,
// because the watcher and the HTTP handler must share one: two rails would
// mean two ethclients and, worse, two watchers advancing the same position
// past each other's work.
var rail *BaseRail

// SetRail records the rail built at startup.
func SetRail(r *BaseRail) { rail = r }

// Rail returns it, or nil when the rail is not configured.
func Rail() *BaseRail { return rail }
