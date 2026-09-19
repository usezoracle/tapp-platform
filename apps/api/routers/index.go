package routers

import (
	"context"
	"github.com/spf13/viper"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/controllers"
	"github.com/usezoracle/tapp/api/controllers/accounts"
	adminCtrl "github.com/usezoracle/tapp/api/controllers/admin"
	"github.com/usezoracle/tapp/api/controllers/cards"
	"github.com/usezoracle/tapp/api/controllers/lp"
	"github.com/usezoracle/tapp/api/controllers/provider"
	"github.com/usezoracle/tapp/api/controllers/sender"
	"github.com/usezoracle/tapp/api/internal/agents"
	apiv1 "github.com/usezoracle/tapp/api/internal/api/v1"
	"github.com/usezoracle/tapp/api/internal/card/link"
	"github.com/usezoracle/tapp/api/internal/card/tap"
	"github.com/usezoracle/tapp/api/internal/checkout"
	"github.com/usezoracle/tapp/api/internal/identity/kyc"
	kycfintava "github.com/usezoracle/tapp/api/internal/identity/kyc/fintava"
	"github.com/usezoracle/tapp/api/internal/identity/limits"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/orders"
	"github.com/usezoracle/tapp/api/internal/settlement"
	"github.com/usezoracle/tapp/api/routers/middleware"
	"github.com/usezoracle/tapp/api/services/baas"
	"github.com/usezoracle/tapp/api/services/baas/fintava"
	"github.com/usezoracle/tapp/api/storage"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// RegisterRoutes add all routing list here automatically get main router
// tapService is the one wired tap service: the merchant app charges and
// reverses through it, and so does the console, so a reversal from either
// leaves the books — and the exchange — in the same shape.
var tapService *tap.Service

func RegisterRoutes(route *gin.Engine) {

	route.NoRoute(func(ctx *gin.Context) {
		u.APIResponse(ctx, http.StatusNotFound, "error", "Route Not Found", nil)
	})
	route.GET("/health", func(ctx *gin.Context) { ctx.JSON(http.StatusOK, gin.H{"live": "ok"}) })

	// API docs — public. Swagger UI shell loads SWG assets from a CDN
	// and points at /openapi.yaml. The raw spec is hand-written at
	// docs/openapi.yaml (single source of truth, no codegen).
	docsCtrl := controllers.NewController()
	route.GET("/docs", docsCtrl.ServeSwaggerUI)
	route.GET("/openapi.yaml", docsCtrl.ServeOpenAPISpec)

	// Add all routes
	authRoutes(route)
	senderRoutes(route)
	providerRoutes(route)
	cardsRoutes(route)

	ctrl := controllers.NewController()

	v1 := route.Group("/v1/")

	v1.GET(
		"currencies",
		ctrl.GetFiatCurrencies,
	)
	v1.GET(
		"institutions/:currency_code",
		ctrl.GetInstitutionsByCurrency,
	)
	v1.GET("rates/:token/:amount/:fiat", ctrl.GetTokenRate)
	v1.GET("pubkey", ctrl.GetAggregatorPublicKey)
	v1.POST("verify-account", ctrl.VerifyAccount)

	// The agent network. Finding somewhere to hand cash to is a public
	// question -- a trader deciding whether this product is usable in their
	// market should not have to sign up first to find out.
	agentHandler := &apiv1.AgentHandler{
		Store: &agents.Store{Pool: storage.Pool},
		User:  apiv1.UserFromContext,
	}
	v1.GET("agents/nearby", agentHandler.Nearby)
	v1.POST("agents", middleware.JWTMiddleware, agentHandler.Register)

	// USDC deposits on Base. Not registered when the rail is not configured:
	// an address people send money to that nobody watches is worse than a
	// missing feature by a wide margin.
	if rail := apiv1.Rail(); rail != nil {
		deposits := &apiv1.DepositHandler{
			Addresses: rail.Addresses, ChainID: rail.ChainID,
			Token: "USDC", User: apiv1.UserFromContext,
		}
		v1.GET("deposits/address", middleware.JWTMiddleware, deposits.Address)

		// The other direction. Registered alongside deposits and under the
		// same `rail != nil` guard: with no chain configured there is nowhere
		// for a withdrawal to go, and an endpoint that always answers 503
		// reads as an outage rather than as a feature that is switched off.
		if rail.Withdrawals != nil {
			withdraw := &apiv1.WithdrawHandler{
				Svc: rail.Withdrawals, User: apiv1.UserFromContext,
			}
			v1.POST("withdrawals", middleware.JWTMiddleware, withdraw.Open)
			v1.GET("withdrawals/:id", middleware.JWTMiddleware, withdraw.Get)
		}
	}

	// Cash pledges. Not registered at all when there is no recogniser: a
	// pledge with no recognition is a photograph nobody looked at, and
	// accepting those would mean crediting people for pictures.
	if cashHandler := apiv1.NewCashHandler(); cashHandler != nil {
		pledges := v1.Group("cash", middleware.JWTMiddleware)
		pledges.GET("pledges", cashHandler.List)
		pledges.GET("pledges/:id", cashHandler.Get)
		pledges.POST("pledges", cashHandler.Pledge)
		pledges.POST("pledges/:id/match", cashHandler.Match)
		pledges.POST("handovers/:id/confirm", cashHandler.ConfirmByTrader)
		pledges.POST("handovers/:id/receive", cashHandler.ConfirmByAgent)
	}
	// Phone-to-phone checkout. The merchant opens a request and broadcasts the
	// URL; this is what opens on the payer's phone.
	//
	// Reading one is public: somebody has to see what they are being asked for
	// before they decide whether to sign in and pay it. Paying one is not.
	publicCheckout := &apiv1.CheckoutHandler{
		Svc: &checkout.Service{
			Pool: storage.Pool,
			Fee:  tap.BasisPointFee(config.OrderConfig().CardFeeBPS),
		},
		Merchant:        apiv1.MerchantFromContext,
		User:            apiv1.UserFromContext,
		CheckoutBaseURL: config.CheckoutBaseURL(),
	}
	v1.GET("checkouts/:id", publicCheckout.Get)
	v1.POST("checkouts/:id/pay", middleware.JWTMiddleware, publicCheckout.Pay)

	v1.GET("orders/:id", ctrl.GetLockPaymentOrderStatus)
	// Public order-scoped SSE — customer checkout PWA subscribes after
	// submitting their on-chain payment to advance through the bridge
	// → settle pipeline in real time.
	v1.GET("orders/:id/stream", ctrl.StreamOrderStatus)
	// Customer "I sent it" ack — pre-emits payment.deposited so the
	// merchant UI advances without waiting for the settlement worker.
	v1.POST("orders/:id/confirm", ctrl.ConfirmOrderPayment)
	v1.POST("gas-station/sponsor", middleware.JWTMiddleware, ctrl.SponsorTransaction)

	// Identity verification, and the naira account it unlocks.
	//
	// Both sit on Fintava: the BVN checked here is the BVN Fintava checks
	// again when it opens the account, so a person cannot pass verification
	// and then be refused an account for disagreeing about who they are --
	// which is what happens when identity and banking come from two companies
	// with two views of the same person.
	//
	// Every route is behind the JWT. The predecessor authenticated KYC with an
	// EIP-191 wallet signature carried in the body: a second auth scheme, for
	// one feature, on a platform where every other endpoint already knows who
	// is calling.
	kycStore := &kyc.Store{Pool: storage.Pool}
	if verifier := kycProvider(); verifier != nil {
		kycHandler := &apiv1.KYCHandler{
			Provider: verifier,
			Store:    kycStore,
			Policy:   limits.NGNPolicy(),
			User:     apiv1.UserFromContext,
		}
		v1.GET("kyc", middleware.JWTMiddleware, kycHandler.Get)
		v1.POST("kyc/bvn", middleware.JWTMiddleware, kycHandler.BVN)
		v1.POST("kyc/selfie", middleware.JWTMiddleware, kycHandler.Selfie)
	}

	// A cardholder's own naira account number: the third funding route,
	// alongside cash handed to an agent and USDC on Base. Registered whether
	// or not a rail is configured -- somebody who was issued an account under
	// a rail that has since been switched off still has it saved as a payee,
	// and must still be able to read it back.
	ngnDeposits := &apiv1.NGNDepositHandler{
		Rail: baas.Default, KYC: kycStore, User: apiv1.UserFromContext,
	}
	v1.GET("deposits/ngn/account", middleware.JWTMiddleware, ngnDeposits.Get)
	v1.POST("deposits/ngn/account", middleware.JWTMiddleware, ngnDeposits.Provision)

	// the BaaS provider (BaaS) transfer/credit callbacks
	v1.POST("safehaven/webhook", ctrl.BaaSWebhook)

	// Liquidity-provider surface (Route B): onboarding, ledger,
	// withdrawals + the rail callbacks that drive deposits and
	// withdrawal finality. Webhook is unauthenticated (signature-
	// verified inside); the rest require the user JWT.
	lpCtrl := lp.NewController()
	v1.POST("fintava/webhook", lpCtrl.FintavaWebhook)
	lpGroup := v1.Group("lp/")
	lpGroup.Use(middleware.JWTMiddleware)
	lpGroup.POST("onboard", lpCtrl.Onboard)
	lpGroup.GET("account", lpCtrl.GetAccount)
	lpGroup.PUT("account", lpCtrl.UpdateAccount)
	lpGroup.GET("ledger", lpCtrl.GetLedger)
	lpGroup.GET("banks", lpCtrl.Banks)
	lpGroup.GET("resolve", lpCtrl.Resolve)
	lpGroup.POST("withdraw", lpCtrl.Withdraw)
}

func authRoutes(route *gin.Engine) {
	authCtrl := accounts.NewAuthController()
	var profileCtrl accounts.ProfileController

	// OnlyWebMiddleware was retired — mobile clients are first-class now.
	// The previous gate required all callers to send `Client-Type: web`,
	// which mobile apps had to spoof for no security gain. Auth + scope
	// middleware handle access control; transport headers aren't auth.
	//
	// Rate limits are per-IP (fixed-window via Redis). Tunable
	// per-bucket; the limits below are tight enough to stop password
	// spraying but generous enough that an honest user fat-fingering
	// won't get locked out.
	v1 := route.Group("/v1/")
	v1.POST("auth/register", middleware.RateLimit("auth.register", 15, time.Hour, nil), authCtrl.Register)
	v1.POST("auth/login", middleware.RateLimit("auth.login", 40, 15*time.Minute, nil), authCtrl.Login)
	v1.POST("auth/google", middleware.RateLimit("auth.google", 20, 15*time.Minute, nil), authCtrl.GoogleAuth)
	v1.POST("auth/confirm-account", middleware.RateLimit("auth.confirm", 10, 10*time.Minute, nil), authCtrl.ConfirmEmail)
	v1.POST("auth/resend-token", middleware.RateLimit("auth.resend", 3, 10*time.Minute, nil), authCtrl.ResendVerificationToken)
	v1.POST("auth/refresh", middleware.RateLimit("auth.refresh", 60, time.Minute, nil), authCtrl.RefreshJWT)
	// Logout is intentionally unauthenticated. It revokes the refresh
	// token in the request body (idempotent), so a client whose access
	// JWT is already expired can still sign out cleanly. Returning 401
	// here would force the client to give up + clear local anyway —
	// just do the revocation.
	v1.POST("auth/logout", middleware.RateLimit("auth.logout", 30, time.Minute, nil), authCtrl.Logout)
	v1.POST("auth/reset-password-token", middleware.RateLimit("auth.reset.request", 3, time.Hour, nil), authCtrl.ResetPasswordToken)
	v1.PATCH("auth/reset-password", middleware.RateLimit("auth.reset.submit", 10, time.Hour, nil), authCtrl.ResetPassword)
	v1.PATCH("auth/change-password", middleware.JWTMiddleware, authCtrl.ChangePassword)

	v1.GET(
		"settings/provider",
		middleware.JWTMiddleware,
		middleware.OnlyProviderMiddleware,
		profileCtrl.GetProviderProfile,
	)
	v1.PATCH(
		"settings/provider",
		middleware.JWTMiddleware,
		middleware.OnlyProviderMiddleware,
		profileCtrl.UpdateProviderProfile,
	)

	v1.GET(
		"settings/sender",
		middleware.JWTMiddleware,
		middleware.OnlySenderMiddleware,
		profileCtrl.GetSenderProfile,
	)
	v1.PATCH(
		"settings/sender",
		middleware.JWTMiddleware,
		middleware.OnlySenderMiddleware,
		profileCtrl.UpdateSenderProfile,
	)

	v1.GET("me", middleware.JWTMiddleware, authCtrl.Me)
	v1.PATCH("me", middleware.JWTMiddleware, authCtrl.UpdateMe)

	// What somebody holds, per currency. Read from the ledger view, so this
	// answer and the entries behind it cannot drift apart.
	balances := &apiv1.BalanceHandler{User: apiv1.UserFromContext}
	v1.GET("me/balances", middleware.JWTMiddleware, balances.Balances)
	v1.GET("me/activity", middleware.JWTMiddleware, balances.Activity)

	// What the cardholder's taps have bought them on the equity market.
	// Thin proxies to the market, keyed on the user id; 404 without a
	// market, 503 when it cannot be reached.
	holdings := &apiv1.HoldingsHandler{Client: apiv1.SharedEquity(), User: apiv1.UserFromContext}
	v1.GET("me/holdings", middleware.JWTMiddleware, holdings.List)
	v1.GET("me/holdings/:symbol", middleware.JWTMiddleware, holdings.Get)
	v1.GET("me/equity-activity", middleware.JWTMiddleware, holdings.Activity)

	// Currency conversion. Two steps by design: a price is offered, then
	// accepted. Quoting and executing in one call would convert at whatever
	// the rate happened to be when the request arrived, which is what the
	// predecessor did and why no conversion could be reconciled afterwards.
	//
	// Mounted on the cardholder scope, not the merchant one. Converting is
	// something a person with a balance does; requiring a sender profile to
	// reach it would put it out of reach of exactly the people it is for.
	if convertHandler := apiv1.NewConvertHandler(); convertHandler != nil {
		v1.POST("me/convert/quote", middleware.JWTMiddleware, convertHandler.Quote)
		v1.POST("me/convert", middleware.JWTMiddleware, convertHandler.Execute)
	}
}

func senderRoutes(route *gin.Engine) {
	senderCtrl := sender.NewSenderController()

	v1 := route.Group("/v1/sender/")
	v1.Use(middleware.DynamicAuthMiddleware)
	v1.Use(middleware.OnlySenderMiddleware)

	orderHandler := &apiv1.OrderHandler{
		Svc: &orders.Service{
			Pool:       storage.Pool,
			Quoter:     apiv1.SharedQuoter(),
			Settlement: &settlement.Worker{Pool: storage.Pool, Rail: baas.Default()},
		},
		User: apiv1.UserFromContext,
	}

	// The offramp: value in, fiat out. One endpoint where there were two --
	// the second existed only to choose the Route A bridge, and there is no
	// bridge to choose.
	v1.POST("orders", orderHandler.Create)
	v1.GET("orders/:id", senderCtrl.GetPaymentOrderByID)
	v1.GET("orders", senderCtrl.GetPaymentOrders)
	v1.POST("orders/:id/cancel", senderCtrl.CancelOrder)
	v1.GET("stats", senderCtrl.Stats)

	// Tapp Merchant — mobile-first merchant API.
	cardsCtrl := cards.NewController()
	me := v1.Group("me/")
	me.POST("bank-account", senderCtrl.SaveMerchantBankAccount)
	me.GET("bank-account", senderCtrl.GetMerchantBankAccount)
	checkoutHandler := &apiv1.CheckoutHandler{
		Svc: &checkout.Service{
			Pool: storage.Pool,
			Fee:  tap.BasisPointFee(config.OrderConfig().CardFeeBPS),
		},
		Merchant:        apiv1.MerchantFromContext,
		User:            apiv1.UserFromContext,
		CheckoutBaseURL: config.CheckoutBaseURL(),
	}
	me.POST("tap", checkoutHandler.Open)
	me.GET("payments/stream", senderCtrl.StreamPayments)

	// Card payments. The ledger authorises the debit in one transaction and
	// the bank rail settles behind it; nothing here waits on a chain.
	//
	// The fee is configuration, not a constant buried in the handler -- the
	// predecessor applied a hardcoded 100 basis points inline, with a comment
	// apologising for it.
	offrampSettler := apiv1.SharedSettler()
	tapService = &tap.Service{
		Pool: storage.Pool,
		Fee:  tap.BasisPointFee(config.OrderConfig().CardFeeBPS),
		// Balances are held as they arrive -- USDC, so dollars -- and the
		// exchange happens here, at the till, for the amount actually
		// being spent. Converting at deposit instead would leave the
		// platform long naira against money nobody has spent yet.
		Funding: money.Currency(viper.GetString("FUNDING_CURRENCY")),
		Quoter:  apiv1.SharedQuoter(),
		// The tap records what has to be settled and where the money
		// came from; the wiring splits it between the rails. What was
		// bought with USDC is sold a moment later from the cardholder's
		// own account; what came from a naira balance is paid out of
		// the cardholder's own naira wallet.
		Settle: apiv1.RecordTapSettlement(offrampSettler),
		// And that the equity market has to hear of it. Queued in the
		// tap's transaction, delivered by a worker; nil without a
		// market, and the tap package never learns one exists.
		Equity:         apiv1.RecordTapEquity(apiv1.SharedEquity()),
		EquityReversal: apiv1.RecordReversalEquity(apiv1.SharedEquity()),
	}
	tapHandler := &apiv1.TapHandler{Svc: tapService, Merchant: apiv1.MerchantFromContext}
	me.GET("tap-card/nonce", tapHandler.Challenge)
	me.POST("tap-card", tapHandler.Debit)
	me.POST("tap-card/:tap_id/token-ack", tapHandler.Acknowledge)
	me.POST("tap-card/:tap_id/reverse", tapHandler.Reverse)
	me.GET("tap-card/step-up", cardsCtrl.TapCardStepUpPoll)

	// The merchant's business on the equity market: register (= list), and
	// read the record with its live cap table. Off without a market, and
	// the handlers say so.
	businessHandler := &apiv1.BusinessHandler{
		Pool: storage.Pool, Client: apiv1.SharedEquity(), Merchant: apiv1.MerchantFromContext,
	}
	me.POST("business", businessHandler.Create)
	me.GET("business", businessHandler.Get)
	me.GET("business/holders", businessHandler.Holders)
}

func providerRoutes(route *gin.Engine) {
	providerCtrl := provider.NewProviderController()

	v1 := route.Group("/v1/provider/")
	v1.Use(middleware.DynamicAuthMiddleware)
	v1.Use(middleware.OnlyProviderMiddleware)

	v1.GET("orders", providerCtrl.GetLockPaymentOrders)
	v1.POST("orders/:id/accept", providerCtrl.AcceptOrder)
	v1.POST("orders/:id/decline", providerCtrl.DeclineOrder)
	v1.POST("orders/:id/fulfill", providerCtrl.FulfillOrder)
	v1.POST("orders/:id/cancel", providerCtrl.CancelOrder)
	v1.GET("rates/:token/:fiat", providerCtrl.GetMarketRate)
	v1.GET("stats", providerCtrl.Stats)
	v1.GET("balance", providerCtrl.GetBalance)
	v1.GET("events", providerCtrl.Events)
	v1.GET("node-info", providerCtrl.NodeInfo)
}

// cardsRoutes wires the Tapp Card surface — public redirect, full
// cardholder flow (link, top-up, revoke, resync), and admin recovery.
// The merchant-facing debit endpoints live under /v1/sender/me/tap-card
// and are wired in senderRoutes (different middleware stack).
func cardsRoutes(route *gin.Engine) {
	cardsCtrl := cards.NewController()

	// Public: the tap-to-URL redirect. Anyone with a card hits this.
	route.GET("/c/:token", cardsCtrl.Resolve)

	// Cardholder (PWA): JWT-authenticated via /v1/auth/google.
	cardholder := route.Group("/v1/cards/")
	cardholder.Use(middleware.JWTMiddleware)
	// Card linking is one resumable session rather than four unrelated
	// endpoints with no state between them. A dropped connection resumes
	// instead of repeating a ceremony that generates a secret, writes it to a
	// chip over NFC, and commits a PIN proof.
	linkHandler := &apiv1.LinkHandler{
		Svc: &link.Service{
			Pool: storage.Pool,
			// The most a card may be set up to spend is what its holder's
			// identity supports. Passing it in keeps the linking package
			// unaware that verification has tiers at all.
			MaxDailyMinor: func(ctx context.Context, user uuid.UUID) (int64, error) {
				tier, err := (&kyc.Store{Pool: storage.Pool}).TierOf(ctx, user)
				if err != nil {
					return 0, err
				}
				return limits.NGNPolicy().For(tier).Daily.Minor(), nil
			},
		},
		User: apiv1.UserFromContext,
	}
	cardholder.POST("link/sessions", linkHandler.Start)
	cardholder.GET("link/sessions/:id", linkHandler.Get)
	cardholder.POST("link/sessions/:id/provision", linkHandler.Provision)
	cardholder.POST("link/sessions/:id/activate", linkHandler.Activate)
	cardholder.GET("me", cardsCtrl.Me)
	cardholder.POST("reset", cardsCtrl.Reset)
	cardholder.POST("me/limits", cardsCtrl.UpdateLimits)
	// No top-up. A card spends the holder's ledger balance directly, so there
	// is no second pot to move money into -- the endpoint that used to do it
	// returned a Move call for a package that no longer exists.
	cardholder.POST("revoke", cardsCtrl.Revoke)
	cardholder.POST("me/resync", cardsCtrl.Resync)
	cardholder.POST("me/resync/complete", cardsCtrl.ResyncComplete)
	cardholder.POST("me/relink", cardsCtrl.Relink)
	cardholder.POST("me/step-up/parse", cardsCtrl.StepUpParse)
	cardholder.POST("me/step-up/grant", cardsCtrl.StepUpGrant)

	// Admin: shared-secret-gated.
	admin := route.Group("/v1/cards/")
	admin.Use(cards.AdminTokenMiddleware)
	admin.POST("issue-batch", cardsCtrl.IssueBatch)

	// Admin recovery (iOS no-Web-NFC escape hatch).
	adminCards := route.Group("/v1/admin/cards/")
	adminCards.Use(cards.AdminTokenMiddleware)
	adminCards.POST(":id/recovery", cardsCtrl.AdminRecovery)
	cardOpsCtrl := adminCtrl.NewCardOpsController()
	adminCards.GET("", cardOpsCtrl.GetCards)
	adminCards.GET(":id", cardOpsCtrl.GetCard)
	adminCards.POST(":id/unlock", cardOpsCtrl.Unlock)
	adminCards.POST(":id/status", cardOpsCtrl.SetStatus)
	adminCards.POST(":id/resync", cardOpsCtrl.Resync)

	// The Route A admin group is gone with the bridge it inspected: an event
	// timeline for orders that no longer pass through a bridge, and a
	// force-state control for a pipeline that no longer has stages.

	// Admin: operator console — transaction timeline, funding dashboard +
	// gated money-movement, config management, refunds. Shared-secret-gated;
	// all writes audited to admin_audit_logs.
	adminConsole := route.Group("/v1/admin/")
	adminConsole.Use(cards.AdminTokenMiddleware)

	// The ledger's own health. Watch this: the per-currency zero-sum it checks
	// is enforced by a database trigger, so it can only fail if something
	// wrote around the ledger, and it answers non-2xx when it does.
	adminConsole.GET("ledger/audit", apiv1.LedgerAudit)

	// What on-chain work costs, and whether the wallet paying for it is still
	// healthy. Registered unconditionally: when the rail is off the handler
	// says so, which is a more useful answer than a missing route when the
	// question is "why has nothing settled".
	gasCtrl := &apiv1.GasHandler{}
	adminConsole.GET("gas", gasCtrl.Status)

	// Verifying premises and putting float behind them are operator
	// decisions: the first is what makes an agent able to take handovers at
	// all, and the second moves real capital.
	adminAgents := &apiv1.AgentHandler{
		Store: &agents.Store{Pool: storage.Pool},
		User:  apiv1.UserFromContext,
	}
	adminConsole.POST("agents/:id/verify", adminAgents.Verify)
	adminConsole.POST("agents/:id/allocate", adminAgents.Allocate)

	// Every payment -- card taps and integrator offramps -- with the ledger's
	// own record of each as its timeline. Read straight off the tables that
	// hold them; there is no log to keep in step.
	txCtrl := adminCtrl.NewTransactionsController()
	adminConsole.GET("transactions", txCtrl.GetTransactions)
	adminConsole.GET("transactions/:id", txCtrl.GetTransaction)
	adminConsole.POST("transactions/:id/reverse", (&adminCtrl.ReverseTapController{Svc: tapService}).ReverseTap)

	integratorsCtrl := adminCtrl.NewIntegratorsController()
	adminConsole.POST("integrators", integratorsCtrl.CreateIntegrator)
	adminConsole.GET("integrators", integratorsCtrl.GetIntegrators)
	adminConsole.GET("integrators/:kind/:id", integratorsCtrl.GetIntegrator)
	adminConsole.POST("integrators/:kind/:id/api-key/rotate", integratorsCtrl.RotateAPIKey)

	fundCtrl := adminCtrl.NewFundingController()
	adminConsole.GET("funding/balances", fundCtrl.GetBalances)
	adminConsole.POST("funding/transfer", fundCtrl.Transfer)

	// Route B liquidity providers — visibility + suspend/reactivate.
	lpOpsCtrl := adminCtrl.NewLpOpsController()
	adminConsole.GET("lps", lpOpsCtrl.GetLPs)
	adminConsole.GET("lps/:id", lpOpsCtrl.GetLP)
	adminConsole.POST("lps/:id/status", lpOpsCtrl.SetStatus)

	cfgCtrl := adminCtrl.NewConfigController()
	adminConsole.GET("config/currencies", cfgCtrl.GetCurrencies)
	adminConsole.PATCH("config/currencies/:id", cfgCtrl.UpdateCurrency)
	adminConsole.GET("config/tokens", cfgCtrl.GetTokens)
	adminConsole.PATCH("config/tokens/:id", cfgCtrl.UpdateToken)
	adminConsole.GET("config/networks", cfgCtrl.GetNetworks)
	adminConsole.GET("config/providers", cfgCtrl.GetProviders)
	adminConsole.PATCH("config/providers/:id", cfgCtrl.UpdateProvider)
	adminConsole.GET("config/settle-mode", cfgCtrl.GetSettleMode)
	adminConsole.PUT("config/settle-mode", cfgCtrl.SetSettleMode)
	adminConsole.GET("config/rails", cfgCtrl.GetRails)
	adminConsole.PUT("config/rails", cfgCtrl.SetRails)
	adminConsole.GET("config/params", cfgCtrl.GetParams)

	refundCtrl := adminCtrl.NewRefundController()
	adminConsole.POST("orders/:id/refund", refundCtrl.RefundOrder)

	userCtrl := adminCtrl.NewUsersController()
	adminConsole.GET("users", userCtrl.GetUsers)
	adminConsole.GET("users/:id", userCtrl.GetUser)
	adminConsole.PATCH("users/:id", userCtrl.UpdateUser)
	adminConsole.PATCH("users/:id/early-access", userCtrl.UpdateEarlyAccess)
	adminConsole.POST("users/:id/revoke-sessions", userCtrl.RevokeSessions)

	statsCtrl := adminCtrl.NewStatsController()
	adminConsole.GET("stats", statsCtrl.GetStats)

	treasuryCtrl := adminCtrl.NewTreasuryController()
	adminConsole.GET("treasury/overview", treasuryCtrl.GetOverview)
	adminConsole.GET("treasury/float-account", treasuryCtrl.GetFloatAccount)

	auditCtrl := adminCtrl.NewAuditController()
	adminConsole.GET("audit-logs", auditCtrl.GetAuditLogs)

	webhookCtrl := adminCtrl.NewWebhooksController()
	adminConsole.GET("webhooks", webhookCtrl.GetWebhookAttempts)
	adminConsole.POST("webhooks/:id/retry", webhookCtrl.RetryWebhook)

	// Cardholders' naira deposit accounts: find by email, correct the bank a
	// row names, and post the credits a webhook that went elsewhere never
	// delivered. The reconcile is a credit to a person and is audited with
	// every figure it was computed from.
	ngnOps := adminCtrl.NewNGNDepositsController()
	adminConsole.GET("deposits/ngn/accounts", ngnOps.Find)
	adminConsole.POST("deposits/ngn/accounts/:account_number/bank", ngnOps.SetBankName)
	adminConsole.POST("deposits/ngn/accounts/:account_number/reconcile", ngnOps.ReconcileAccount)

	// Naira legs: what taps took from naira balances, paid to merchants out
	// of cardholders' own wallets. List them, and retry one the rail
	// refused.
	ngnSettlements := adminCtrl.NewNGNSettlementsController(apiv1.SharedNairaWorker())
	adminConsole.GET("settlements/ngn", ngnSettlements.GetSettlements)
	adminConsole.POST("settlements/ngn/:tap_id/retry", ngnSettlements.RetrySettlement)
}

// kycProvider builds the identity verifier, or nil when it is not configured.
//
// Read from config directly rather than from the operator's float-rail switch:
// which rail the platform PAYS from is a commercial decision that changes, and
// who it verifies identities with is not. Tying them together would mean
// flipping a payout preference silently retires everybody's route to a higher
// limit.
//
// Nil leaves the routes unregistered, so an unconfigured deployment 404s
// rather than answering 503 forever -- a feature that is switched off should
// not look like one that is broken.
func kycProvider() kyc.Provider {
	bc := config.BaaSConfig()
	if bc.FintavaAPIKey == "" {
		logger.Infof("kyc: not configured (needs FINTAVA_API_KEY) -- identity verification is unavailable")
		return nil
	}
	p, err := kycfintava.New(fintava.New(bc.FintavaAPIKey, bc.FintavaWebhookSecret, bc.FintavaBaseURL))
	if err != nil {
		logger.Errorf("kyc: %v -- identity verification is unavailable", err)
		return nil
	}
	return p
}
