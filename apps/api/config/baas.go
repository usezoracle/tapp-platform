package config

import (
	"github.com/shopspring/decimal"
	"github.com/spf13/viper"
)

// BaaSConfiguration holds credentials for the the BaaS provider MFB BaaS rail —
// the NGN fiat payout used by Route C (managed liquidity) and Route A's
// treasury mode. See services/baas/mfb.
type BaaSConfiguration struct {
	// ClientID is the OAuth Client ID from the the BaaS provider dashboard.
	ClientID string
	// PrivateKeyPEM is the RSA private key whose public cert is registered on
	// the dashboard. Store as a secret; "\n" escapes are tolerated.
	PrivateKeyPEM string
	// BaseURL is the API root (prod by default).
	BaseURL string
	// Audience overrides the JWT aud claim; empty → BaseURL.
	Audience string
	// Issuer overrides the JWT iss claim; empty → ClientID.
	Issuer string
	// DebitAccountNumber is our funded the BaaS provider account that payouts debit.
	DebitAccountNumber string
	// WebhookSecret verifies inbound the BaaS provider transfer/credit callbacks. When
	// empty, signature verification is skipped (dev only).
	WebhookSecret string
	// MaxTransferNGN caps a single admin payout to guard against fat-finger /
	// stolen-token catastrophe. Zero means unlimited (not recommended in prod).
	MaxTransferNGN decimal.Decimal

	// public key authenticates the misc endpoints (banks, resolve).

	// Fintava rail (fintava.readme.io). One bearer key per env; the
	// webhook secret comes from their dashboard (HMAC-SHA512).
	FintavaAPIKey        string
	FintavaWebhookSecret string
	FintavaBaseURL       string // default live; sandbox = https://dev.fintavapay.com/api/dev

	// FintavaDepositBankName / FintavaDepositBankCode name the partner bank
	// behind cardholders' STATIC_FUND deposit accounts, for when Fintava's
	// create-customer response does not say (or said it in a shape this
	// code did not recognise). Read at provisioning when the response has no
	// bank, and again when a stored row still carries the old placeholder.
	// Empty means "no fallback": the row keeps whatever it has.
	FintavaDepositBankName string
	FintavaDepositBankCode string
}

// BaaSConfig reads the BaaS provider settings from env.
func BaaSConfig() *BaaSConfiguration {
	viper.SetDefault("SAFEHAVEN_BASE_URL", "https://api.safehavenmfb.com")
	viper.SetDefault("SAFEHAVEN_MAX_TRANSFER_NGN", "1000000")

	maxTransfer, err := decimal.NewFromString(viper.GetString("SAFEHAVEN_MAX_TRANSFER_NGN"))
	if err != nil {
		maxTransfer = decimal.NewFromInt(1_000_000)
	}

	viper.SetDefault("FINTAVA_BASE_URL", "https://live.fintavapay.com/api/dev")
	viper.SetDefault("FINTAVA_DEPOSIT_BANK_NAME", "")
	viper.SetDefault("FINTAVA_DEPOSIT_BANK_CODE", "")

	return &BaaSConfiguration{
		ClientID:           viper.GetString("SAFEHAVEN_CLIENT_ID"),
		PrivateKeyPEM:      viper.GetString("SAFEHAVEN_PRIVATE_KEY"),
		BaseURL:            viper.GetString("SAFEHAVEN_BASE_URL"),
		Audience:           viper.GetString("SAFEHAVEN_AUDIENCE"),
		Issuer:             viper.GetString("SAFEHAVEN_ISSUER"),
		DebitAccountNumber: viper.GetString("SAFEHAVEN_DEBIT_ACCOUNT_NUMBER"),
		WebhookSecret:      viper.GetString("SAFEHAVEN_WEBHOOK_SECRET"),
		MaxTransferNGN:     maxTransfer,

		FintavaAPIKey:        viper.GetString("FINTAVA_API_KEY"),
		FintavaWebhookSecret: viper.GetString("FINTAVA_WEBHOOK_SECRET"),
		FintavaBaseURL:       viper.GetString("FINTAVA_BASE_URL"),

		FintavaDepositBankName: viper.GetString("FINTAVA_DEPOSIT_BANK_NAME"),
		FintavaDepositBankCode: viper.GetString("FINTAVA_DEPOSIT_BANK_CODE"),
	}
}
