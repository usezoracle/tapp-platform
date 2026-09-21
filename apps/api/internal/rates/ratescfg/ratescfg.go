package ratescfg

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"github.com/spf13/viper"

	"github.com/usezoracle/tapp/api/internal/rates"
	"github.com/usezoracle/tapp/api/internal/rates/sources"
)

// FromEnv builds the rate engine and spread table from environment
// configuration.
//
// Its own package rather than inside `rates`, because assembling concrete
// providers is composition: rates defines the Source interface and sources
// implements it, and having the interface package reach back for the
// implementations is a cycle. Its own package rather than the API wiring,
// because more than the API needs it -- an operational command that converts
// balances must price them exactly as the deposit path does, and two copies
// of this parsing would eventually disagree about a rate.
//
// Sources are declared as JSON rather than as one env var per provider,
// because adding a provider should not require a code change or a new
// variable. A deployment with no sources declared cannot quote at all, which
// is the correct behaviour: there is no default rate.
//
//	FX_SOURCES=[{"id":"a","url":"https://…/{base}/{quote}","path":"data.rate"}]
//	FX_SPREADS=USD/NGN:50,NGN/USD:50
func FromEnv() (*rates.Engine, rates.Spread, error) {
	engine := &rates.Engine{
		MaxDeviation: decimal.NewFromFloat(viper.GetFloat64("FX_MAX_DEVIATION")),
	}

	raw := strings.TrimSpace(viper.GetString("FX_SOURCES"))
	if raw != "" {
		var declared []struct {
			ID     string `json:"id"`
			URL    string `json:"url"`
			Path   string `json:"path"`
			Invert bool   `json:"invert"`
		}
		if err := json.Unmarshal([]byte(raw), &declared); err != nil {
			return nil, nil, fmt.Errorf("FX_SOURCES is not valid JSON: %w", err)
		}
		for _, d := range declared {
			if d.ID == "" || d.URL == "" {
				return nil, nil, fmt.Errorf("FX_SOURCES: every source needs an id and a url")
			}
			engine.Sources = append(engine.Sources, &sources.JSONSource{
				ID: d.ID, URL: d.URL, Path: d.Path, Invert: d.Invert,
			})
		}
	}

	spread := rates.Spread{}
	for _, entry := range strings.Split(viper.GetString("FX_SPREADS"), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		pair, bps, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, nil, fmt.Errorf("FX_SPREADS: %q is not PAIR:BPS", entry)
		}
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(bps), "%d", &n); err != nil {
			return nil, nil, fmt.Errorf("FX_SPREADS: %q has no basis points", entry)
		}
		spread[strings.TrimSpace(pair)] = n
	}

	return engine, spread, nil
}
