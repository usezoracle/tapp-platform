package config

import "github.com/spf13/viper"

// SwitchConfiguration is how this deployment reaches Switch, the provider
// that pays a merchant's bank for the USDC a card tap sells.
//
// The key is optional. Without it nothing is sold: taps still charge and
// their settlement rows wait, exactly as they did when no gateway was set,
// and bank-account checks fall back to the local catalogue.
type SwitchConfiguration struct {
	// BaseURL is Switch's API root. One host serves sandbox and live; the
	// key decides which.
	BaseURL string
	// ServiceKey is sent as x-service-key on every call.
	ServiceKey string
}

// SwitchConfig reads the Switch settings from env.
func SwitchConfig() *SwitchConfiguration {
	viper.SetDefault("SWITCH_API_BASE_URL", "https://api.onswitch.xyz")
	return &SwitchConfiguration{
		BaseURL:    viper.GetString("SWITCH_API_BASE_URL"),
		ServiceKey: viper.GetString("SWITCH_SERVICE_KEY"),
	}
}

// Enabled reports whether Switch can be called at all.
func (c *SwitchConfiguration) Enabled() bool {
	return c.BaseURL != "" && c.ServiceKey != ""
}
