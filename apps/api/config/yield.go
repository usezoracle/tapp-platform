package config

import (
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// YieldConfiguration is how cardholders' idle USDC earns in a vault. See
// docs/yield.md.
//
// The vault address is what wires releasing and crediting, and they run
// whenever it is set, so money already in the vault can always be spent and
// keeps earning. Sweeping, the only thing that puts money in, also needs
// YIELD_ENABLED: turning that off stops new deposits and strands nothing.
type YieldConfiguration struct {
	Vault    common.Address
	Sweeping bool
	// SweepMin is in USDC micro-units.
	SweepMin int64
	// Users limits sweeping to these cardholders while it is tried out;
	// empty means everyone.
	Users    map[uuid.UUID]bool
	Interval time.Duration
}

// YieldConfig reads the yield settings from env.
func YieldConfig() *YieldConfiguration {
	viper.SetDefault("YIELD_SWEEP_MIN_MICRO", 1_000_000)
	viper.SetDefault("YIELD_INTERVAL_SECONDS", 300)
	c := &YieldConfiguration{
		Sweeping: viper.GetBool("YIELD_ENABLED"),
		SweepMin: viper.GetInt64("YIELD_SWEEP_MIN_MICRO"),
		Users:    map[uuid.UUID]bool{},
		Interval: time.Duration(max(viper.GetInt("YIELD_INTERVAL_SECONDS"), 30)) * time.Second,
	}
	if a := viper.GetString("YIELD_VAULT_ADDRESS"); common.IsHexAddress(a) {
		c.Vault = common.HexToAddress(a)
	}
	for _, id := range strings.Split(viper.GetString("YIELD_USERS"), ",") {
		if u, err := uuid.Parse(strings.TrimSpace(id)); err == nil {
			c.Users[u] = true
		}
	}
	return c
}

// Configured reports whether there is a vault at all.
func (c *YieldConfiguration) Configured() bool {
	return c.Vault != (common.Address{})
}
