// Market rates.
//
// Each source is queried independently and the median of the live ones wins.
// A source that fails is dropped rather than defaulted, and if none answer the
// last good rate stands -- writing a zero would price every order at nothing.

package tasks

import (
	"context"
	"fmt"
	"strings"
	"time"

	fastshot "github.com/opus-domini/fast-shot"
	"github.com/shopspring/decimal"
	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/ent/fiatcurrency"
	"github.com/usezoracle/tapp/api/services/onswitch"
	"github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// supportedRateCurrencies is the set of fiats we compute a live market rate for.
var supportedRateCurrencies = map[string]bool{
	"KES": true, "NGN": true, "GHS": true, "TZS": true, "UGX": true, "XOF": true,
}

const rateSourceTimeout = 15 * time.Second

// fetchExternalRate returns the live USDT/<fiat> market price aggregated across
// several independent sources — Switch's payout rate (NGN), Binance P2P, and (for NGN)
// Quidax. It takes the MEDIAN of whatever sources respond, so a source being
// down, geo-restricted (Binance P2P is region-gated), or returning an outlier
// never breaks the rate. It errors only when EVERY source fails. There is no
// seeded/fixed fallback — the rate is always live or nothing.
func fetchExternalRate(currency string) (decimal.Decimal, error) {
	currency = strings.ToUpper(currency)
	if !supportedRateCurrencies[currency] {
		return decimal.Zero, fmt.Errorf("fetchExternalRate: currency %s not supported", currency)
	}

	var rates []decimal.Decimal

	// Source 1 — Switch, the provider card taps settle through: the rate it
	// pays a Nigerian bank for USDC right now.
	if r, err := fetchSwitchRate(currency); err != nil {
		logger.Warnf("fetchExternalRate: switch %s: %v", currency, err)
	} else if r.IsPositive() {
		rates = append(rates, r)
	}

	// Source 2 — Binance P2P SELL-ad median (available where Binance P2P is not
	// geo-restricted; empty data there is a soft miss, not a failure).
	if r, err := fetchBinanceP2PRate(currency); err != nil {
		logger.Warnf("fetchExternalRate: binance %s: %v", currency, err)
	} else if r.IsPositive() {
		rates = append(rates, r)
	}

	// Source 3 — Quidax USDT/<fiat> ticker (NGN market).
	if currency == "NGN" {
		if r, err := fetchQuidaxRate(currency); err != nil {
			logger.Warnf("fetchExternalRate: quidax %s: %v", currency, err)
		} else if r.IsPositive() {
			rates = append(rates, r)
		}
	}

	if len(rates) == 0 {
		return decimal.Zero, fmt.Errorf("fetchExternalRate: all sources failed for %s", currency)
	}
	return utils.Median(rates), nil
}

// fetchSwitchRate reads Switch's naira payout rate for 100 USDC. Switch pays
// Nigeria only, so any other currency is a miss, not a failure of the median.
func fetchSwitchRate(currency string) (decimal.Decimal, error) {
	if currency != "NGN" {
		return decimal.Zero, fmt.Errorf("switch: no payout rate for %s", currency)
	}
	cfg := config.SwitchConfig()
	if !cfg.Enabled() {
		return decimal.Zero, onswitch.ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), rateSourceTimeout)
	defer cancel()
	q, err := onswitch.New(cfg.BaseURL, cfg.ServiceKey).Quote(ctx, onswitch.QuoteRequest{
		Amount:   100,
		Country:  onswitch.Country,
		Currency: currency,
		Asset:    onswitch.Asset,
		Channel:  onswitch.Channel,
	})
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromFloat(q.Rate), nil
}

// fetchQuidaxRate reads the USDT/<fiat> buy ticker from Quidax. Note the API
// lives on app.quidax.io — the old www.quidax.com host now blackholes requests.
func fetchQuidaxRate(currency string) (decimal.Decimal, error) {
	res, err := fastshot.NewClient("https://app.quidax.io").
		Config().SetTimeout(rateSourceTimeout).
		Build().GET(fmt.Sprintf("/api/v1/markets/tickers/usdt%s", strings.ToLower(currency))).
		Retry().Set(2, 3*time.Second).
		Send()
	if err != nil {
		return decimal.Zero, err
	}
	data, err := utils.ParseJSONResponse(res.RawResponse)
	if err != nil {
		return decimal.Zero, err
	}
	d, ok := data["data"].(map[string]interface{})
	if !ok {
		return decimal.Zero, fmt.Errorf("quidax: unexpected response shape")
	}
	ticker, ok := d["ticker"].(map[string]interface{})
	if !ok {
		return decimal.Zero, fmt.Errorf("quidax: missing ticker")
	}
	buy, ok := ticker["buy"].(string)
	if !ok {
		return decimal.Zero, fmt.Errorf("quidax: missing buy price")
	}
	return decimal.NewFromString(buy)
}

// fetchBinanceP2PRate returns the median of the top USDT/<fiat> SELL ads on
// Binance P2P. The endpoint is public but region-gated: restricted IPs get an
// empty list (success:true, data:[]) — treated here as a soft miss.
func fetchBinanceP2PRate(currency string) (decimal.Decimal, error) {
	res, err := fastshot.NewClient("https://p2p.binance.com").
		Config().SetTimeout(rateSourceTimeout).
		Header().Add("Content-Type", "application/json").
		Build().POST("/bapi/c2c/v2/friendly/c2c/adv/search").
		Retry().Set(2, 3*time.Second).
		Body().AsJSON(map[string]interface{}{
		"asset":             "USDT",
		"fiat":              currency,
		"tradeType":         "SELL",
		"page":              1,
		"rows":              20,
		"payTypes":          []string{},
		"countries":         []string{},
		"proMerchantAds":    false,
		"shieldMerchantAds": false,
		"publisherType":     nil,
	}).
		Send()
	if err != nil {
		return decimal.Zero, err
	}
	resData, err := utils.ParseJSONResponse(res.RawResponse)
	if err != nil {
		return decimal.Zero, err
	}
	data, ok := resData["data"].([]interface{})
	if !ok || len(data) == 0 {
		return decimal.Zero, fmt.Errorf("binance: no ads (region-gated or none available)")
	}
	var prices []decimal.Decimal
	for _, item := range data {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		adv, ok := m["adv"].(map[string]interface{})
		if !ok {
			continue
		}
		priceStr, ok := adv["price"].(string)
		if !ok {
			continue
		}
		if price, err := decimal.NewFromString(priceStr); err == nil {
			prices = append(prices, price)
		}
	}
	if len(prices) == 0 {
		return decimal.Zero, fmt.Errorf("binance: no parseable prices")
	}
	return utils.Median(prices), nil
}

// ComputeMarketRate computes the market price for fiat currencies
func ComputeMarketRate() error {
	ctx := context.Background()

	// Fetch all fiat currencies
	currencies, err := storage.Client.FiatCurrency.
		Query().
		Where(fiatcurrency.IsEnabledEQ(true)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("ComputeMarketRate: %w", err)
	}

	for _, currency := range currencies {
		// The market rate is the live, externally-computed price — never seeded
		// and never derived from provider rates. If every source fails we keep
		// the last good value rather than overwrite it with a bad/zero rate.
		rate, err := fetchExternalRate(currency.Code)
		if err != nil {
			logger.Errorf("ComputeMarketRate: %s: %v", currency.Code, err)
			continue
		}
		if !rate.IsPositive() {
			continue
		}

		if _, err := storage.Client.FiatCurrency.
			UpdateOneID(currency.ID).
			SetMarketRate(rate).
			Save(ctx); err != nil {
			logger.Errorf("ComputeMarketRate: update %s: %v", currency.Code, err)
		}
	}

	return nil
}
