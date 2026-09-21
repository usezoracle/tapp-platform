package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/viper"

	"github.com/usezoracle/tapp/api/config"
	apiv1 "github.com/usezoracle/tapp/api/internal/api/v1"
	"github.com/usezoracle/tapp/api/internal/cash"
	"github.com/usezoracle/tapp/api/internal/equity"
	"github.com/usezoracle/tapp/api/internal/settlement"
	"github.com/usezoracle/tapp/api/routers"
	"github.com/usezoracle/tapp/api/services"
	"github.com/usezoracle/tapp/api/services/baas"
	"github.com/usezoracle/tapp/api/services/baas/fintava"
	"github.com/usezoracle/tapp/api/services/baas/mfb"
	"github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/tasks"
	"github.com/usezoracle/tapp/api/utils/crypto"
	"github.com/usezoracle/tapp/api/utils/logger"
)

func main() {
	// Set timezone
	conf := config.ServerConfig()
	loc, _ := time.LoadLocation(conf.Timezone)
	time.Local = loc

	// Custody keys are resolved before anything can serve a request. A signup
	// creates a private key that must be sealed at rest, so a process with no
	// valid WALLET_MASTER_KEY has no business accepting one. Fatal on purpose:
	// the alternative this replaced was a hardcoded key in source.
	if err := crypto.RequireMasterKey(); err != nil {
		logger.Fatalf("custody key: %s", err)
	}
	// Likewise the secrets every session is signed with. An empty signing key
	// is not a degraded mode, it is no authentication at all.
	if err := config.RequireSecrets(); err != nil {
		logger.Fatalf("auth secrets: %s", err)
	}

	// Connect to the database
	DSN := config.DBConfig()

	if err := storage.DBConnection(DSN); err != nil {
		logger.Fatalf("database DBConnection: %s", err)
	}

	defer storage.GetClient().Close()

	// Initialize Redis
	if err := storage.InitializeRedis(); err != nil {
		logger.Fatalf("Redis initialization: %s", err)
	}

	// Initialize the BaaS (NGN fiat) rail behind the provider-agnostic baas
	// adapter, selected by BAAS_PROVIDER (default "safehaven"). Absent
	// credentials are non-fatal (Route A `mode=lp` and other flows don't need
	// it); present but invalid credentials fail fast so misconfiguration
	// surfaces at boot. To switch providers, add a case here — no consumer
	// changes (they all depend on baas.Default()).
	initBaaSRail()

	// Background workers: Redis keyspace listeners + cron jobs (event
	// indexers, Route-A dispatcher, reconcilers). DISABLE_BACKGROUND_JOBS=true
	// runs this process API-only — exactly one worker instance should ever
	// run against a database, so side-by-side instances (canary builds,
	// separate HTTP scaling) must set this flag.
	viper.SetDefault("DISABLE_BACKGROUND_JOBS", false)
	if viper.GetBool("DISABLE_BACKGROUND_JOBS") {
		logger.Infof("DISABLE_BACKGROUND_JOBS=true — API-only mode (no keyspace subscriptions, no cron jobs)")
	} else {
		// Subscribe to Redis keyspace events
		tasks.SubscribeToRedisKeyspaceEvents()

		// Start cron jobs
		tasks.StartCronJobs()
	}

	// The USDC rail on Base. Built before the router so the HTTP handler and
	// the watcher share one instance -- two would mean two watchers advancing
	// the same position past each other's work.
	baseRail, err := apiv1.NewBaseRail(context.Background())
	if err != nil {
		// Fatal rather than degraded. A misconfigured deposit rail that starts
		// anyway hands out addresses nobody is watching.
		logger.Fatalf("base rail: %s", err)
	}
	apiv1.SetRail(baseRail)

	// Release float locked against handovers nobody turned up for.
	//
	// Not a tidy-up job: until this runs, an agent's capital is committed to a
	// trader who never came and cannot serve anybody else. Every minute of
	// delay is float withdrawn from the market.
	if !viper.GetBool("DISABLE_BACKGROUND_JOBS") {
		cashSvc := &cash.Service{Pool: storage.Pool}
		go cashSvc.RunSweeper(context.Background(), cashSweepInterval())

		if baseRail != nil {
			go baseRail.Watcher.Run(context.Background(), apiv1.BasePollInterval())

			// The sweeper is deliberately NOT started.
			//
			// It pooled every deposit into the treasury, which is what made
			// the platform custodian of the money. A card tap now sells the
			// cardholder's own USDC to the settlement gateway from their own
			// smart account, so the money has to still be there: a swept
			// balance is an account that cannot pay for anything.
			//
			// The code is kept because retired deposit addresses still hold
			// seed-derived funds that only it can move. Starting it again
			// would empty every account the offramp spends from.
			go baseRail.Withdrawals.Run(context.Background(), apiv1.BasePollInterval())
		}

		// Sell each tap's USDC to the settlement gateway, from the
		// cardholder's own account, so a liquidity provider pays the
		// merchant's bank. This is what actually delivers a card payment.
		if s := apiv1.SharedSettler(); s != nil {
			go s.Run(context.Background(), settlementInterval())
		}

		// Bank payouts for everything that is not a card tap: the sender
		// offramp still reserves and delivers through a provider.
		go (&settlement.Worker{Pool: storage.Pool, Rail: baas.Default()}).
			Run(context.Background(), settlementInterval())

		// The naira leg of a tap: what the cardholder paid from a naira
		// balance has no USDC to sell, and is paid to the merchant out of
		// the cardholder's own wallet at the bank rail.
		go apiv1.SharedNairaWorker().Run(context.Background(), apiv1.NairaSettlementInterval())

		// Tell the equity market about each tap, from the outbox the tap's
		// own transaction wrote to. Nothing without a market.
		if c := apiv1.SharedEquity(); c.Enabled() {
			go (&equity.Worker{Pool: storage.Pool, Client: c}).
				Run(context.Background(), config.EquityConfig().OutboxInterval)
		}
	}

	// Run the server
	router := routers.Routes()

	appServer := fmt.Sprintf("%s:%s", conf.Host, conf.Port)
	logger.Infof("Server Running at :%v", appServer)

	logger.Fatalf("%v", router.Run(appServer))
}

// initBaaSRail builds the configured BaaS provider and registers it as the
// process-wide baas.Default(). The composition root is the only place that
// knows a concrete vendor; everything else depends on the baas interface.
func initBaaSRail() {
	// The default provider is an ADMIN decision (Payment Rails card →
	// Redis), not an env. Before the first switch, safehaven (the
	// original rail) stands so legacy deploys behave unchanged.
	provider := services.BaaSProviderOverride()
	if provider == "" {
		provider = "safehaven"
	}
	switch provider {
	case "safehaven":
		shConf := config.BaaSConfig()
		switch shClient, err := mfb.NewClientFromCredentials(
			shConf.ClientID, shConf.PrivateKeyPEM, shConf.BaseURL, shConf.Audience, shConf.Issuer,
		); {
		case errors.Is(err, mfb.ErrNotConfigured):
			logger.Infof("BaaS rail (mfb) not configured; fiat payout routes disabled")
		case err != nil:
			logger.Fatalf("BaaS rail (mfb) init: %s", err)
		default:
			baas.SetDefault(mfb.NewAdapter(shClient, shConf.WebhookSecret))
			logger.Infof("BaaS rail ready (provider=mfb)")
		}
	case "fintava":
		fConf := config.BaaSConfig()
		if fConf.FintavaAPIKey == "" {
			logger.Infof("BaaS rail (fintava) not configured (FINTAVA_API_KEY empty); fiat payout routes disabled")
			return
		}
		baas.SetDefault(fintava.NewAdapter(fintava.New(
			fConf.FintavaAPIKey, fConf.FintavaWebhookSecret, fConf.FintavaBaseURL,
		)))
		logger.Infof("BaaS rail ready (provider=fintava)")
	default:
		logger.Fatalf("BaaS rail: unknown provider %q (admin config)", provider)
	}
}

// cashSweepInterval is how often expired handovers are released.
func cashSweepInterval() time.Duration {
	viper.SetDefault("CASH_SWEEP_INTERVAL_SECONDS", 30)
	seconds := viper.GetInt("CASH_SWEEP_INTERVAL_SECONDS")
	if seconds < 5 {
		// A sweep every second or two would hammer the database for no gain;
		// the shortest handover window is measured in minutes.
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

// settlementInterval is how often owed money is pushed to banks.
func settlementInterval() time.Duration {
	viper.SetDefault("SETTLEMENT_INTERVAL_SECONDS", 20)
	seconds := viper.GetInt("SETTLEMENT_INTERVAL_SECONDS")
	if seconds < 5 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}
