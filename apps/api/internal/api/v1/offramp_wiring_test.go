package v1

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/usezoracle/tapp/api/internal/money"
	paycrest "github.com/usezoracle/tapp/api/services/settlement"
)

// The aggregator's rate depends on the amount, and the amount on the rate.
// These are the brackets Paycrest served on 2026-09-17: a small order at one
// price, anything from 1.5 USDC at a better one.
func bracketedRates(t *testing.T) (*paycrest.Client, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /v2/rates/base/USDC/{amount}/NGN
		parts := strings.Split(r.URL.Path, "/")
		amount := parts[len(parts)-2]
		asked = append(asked, amount)
		rate := "1332.392524"
		if strings.HasPrefix(amount, "1.5") || strings.HasPrefix(amount, "2") {
			rate = "1340.426667"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"sell":{"rate":%q,"providerIds":["p"],"orderType":"regular","refundTimeoutMinutes":2}}}`, rate)
	}))
	t.Cleanup(srv.Close)
	return paycrest.New(srv.URL+"/v1", time.Hour), &asked
}

func TestAnOrderIsPricedAtTheAggregatorsRateForItsOwnAmount(t *testing.T) {
	client, asked := bracketedRates(t)
	price := priceWith(client, "base")

	// ₦1,592.00 owed; the tap estimated 1.208679 USDC from a 1-USDC quote.
	sell, err := price(context.Background(), money.Naira(1_592), 1_208_679)
	if err != nil {
		t.Fatalf("price: %v", err)
	}
	// 1592 / 1332.392524 = 1.194843… USDC, rounded up to the micro.
	if sell != 1_194_844 {
		t.Errorf("sell = %d, want 1194844", sell)
	}
	// The rate the order will carry is what it delivers over what it sells:
	// within a kobo of the quote, and never above it.
	rate := float64(159_200) / 100 / (float64(sell) / 1e6)
	if rate > 1332.392524 || rate < 1332.39 {
		t.Errorf("order rate = %.6f, want within ₦0.01 below 1332.392524", rate)
	}
	if len(*asked) != 2 || (*asked)[0] != "1.208679" || (*asked)[1] != "1.194844" {
		t.Errorf("asked the aggregator for %v; want the estimate, then the amount it produced", *asked)
	}
}

func TestAnEstimateInTheWrongBracketIsRepriced(t *testing.T) {
	client, _ := bracketedRates(t)
	price := priceWith(client, "base")

	// ₦2,000 owed but the estimate is under the 1.5 bracket: the first pass
	// quotes the small-order rate, the amount that gives lands in the larger
	// bracket, and the second pass reprices there.
	sell, err := price(context.Background(), money.Naira(2_000), 1_400_000)
	if err != nil {
		t.Fatalf("price: %v", err)
	}
	// 2000 / 1340.426667 = 1.4920622… → 1.492063
	if sell != 1_492_063 {
		t.Errorf("sell = %d, want 1492063 (priced in the bracket the order lands in)", sell)
	}
}
