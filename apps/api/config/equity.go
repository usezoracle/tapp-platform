package config

import (
	"time"

	"github.com/spf13/viper"
)

// EquityConfiguration is how this deployment reaches the equity market
// (Freedom's rail API).
//
// Two values, both optional. When either is missing the equity feature is off:
// taps charge exactly as before and nothing is queued, the business and
// holdings endpoints answer 404 with a message saying so, and no worker runs.
// This is a working state, not a broken one -- it is how every deployment
// runs until a market exists for it.
type EquityConfiguration struct {
	// BaseURL is the origin of Freedom's API, without the /v1/rail suffix.
	BaseURL string
	// RailToken is the bearer token every rail call carries.
	RailToken string
	// OutboxInterval is how often queued taps are delivered.
	OutboxInterval time.Duration
}

// EquityConfig reads the equity settings from env. No defaults for the
// address or the secret: absent means off.
func EquityConfig() *EquityConfiguration {
	viper.SetDefault("EQUITY_OUTBOX_INTERVAL_SECONDS", 5)
	seconds := viper.GetInt("EQUITY_OUTBOX_INTERVAL_SECONDS")
	if seconds < 1 {
		seconds = 1
	}
	return &EquityConfiguration{
		BaseURL:        viper.GetString("FREEDOM_BASE_URL"),
		RailToken:      viper.GetString("FREEDOM_RAIL_TOKEN"),
		OutboxInterval: time.Duration(seconds) * time.Second,
	}
}

// Enabled reports whether the market can be reached at all.
func (c *EquityConfiguration) Enabled() bool {
	return c.BaseURL != "" && c.RailToken != ""
}
