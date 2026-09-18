package equity

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Against a real Freedom. Opt-in: set FREEDOM_TEST_URL and
// FREEDOM_TEST_TOKEN (e.g. http://localhost:8091 and dev-rail-token).
//
// This is the one test where the other side is not a stub, so it is the
// one that says whether the client's structs match what Freedom actually
// sends. A field that decodes to zero here is a client bug.
func liveClient(t *testing.T) *Client {
	t.Helper()
	url, token := os.Getenv("FREEDOM_TEST_URL"), os.Getenv("FREEDOM_TEST_TOKEN")
	if url == "" || token == "" {
		t.Skip("set FREEDOM_TEST_URL and FREEDOM_TEST_TOKEN to run against a live Freedom")
	}
	return New(url, token)
}

func randomDigits(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		s += d.String()
	}
	return s
}

func randomSymbol() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	s := "T"
	for i := 0; i < 5; i++ {
		d, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		s += string(letters[d.Int64()])
	}
	return s
}

func dump(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func TestLiveFreedomRoundTrip(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	merchant, cardholder, tap := uuid.New(), uuid.New(), uuid.New()
	symbol := randomSymbol()

	// 1. List a business.
	biz, err := c.CreateBusiness(ctx, BusinessRequest{
		MerchantRef: merchant.String(), CardholderRef: uuid.NewString(),
		LegalName: "Live Test " + symbol + " Ltd", TradingName: symbol,
		RCNumber: "RC" + randomDigits(7), MCC: "5812", Symbol: symbol,
		Evidence: Evidence{
			TradingMonths: 30, AuditedAccounts: true, AuditorOnList: true,
			SharesInIssue: 800_000_000_000_000, PublicShares: 120_000_000_000_000,
			Holders: 31, TreasuryUnits: 180_000_000_000_000,
			BoardResolution: true, DirectorsClear: true,
		},
		ReferencePriceKobo: 4000, SharesAuthorisedUnits: 1_000_000_000_000_000,
		DailyReleaseUnits: 50_000_000_000_000, CofundBPS: 0, Holders: []Holder{},
	})
	if err != nil {
		t.Fatalf("CreateBusiness: %v", err)
	}
	t.Logf("business: %s", dump(biz))
	if biz.State != "listed" || biz.Symbol != symbol || biz.InstrumentID == "" || biz.ReferencePriceKobo != 4000 {
		t.Fatalf("business = %+v, want listed %s", biz, symbol)
	}
	if len(biz.Findings) != 7 {
		t.Errorf("%d findings, want 7", len(biz.Findings))
	}
	for _, f := range biz.Findings {
		if !f.Met {
			t.Errorf("finding %s unmet: %s", f.Criterion, f.Detail)
		}
	}

	// 2. Close the market so today's session has a price.
	rows, err := c.CloseMarket(ctx, "")
	if err != nil {
		t.Fatalf("CloseMarket: %v", err)
	}
	var closed *CloseRow
	for i := range rows {
		if rows[i].Symbol == symbol {
			closed = &rows[i]
		}
	}
	if closed == nil {
		t.Fatalf("%s not in the close: %s", symbol, dump(rows))
	}
	t.Logf("close: %s", dump(closed))
	if closed.State != "published" {
		t.Errorf("close state = %q, want published", closed.State)
	}
	today, err := c.MarketToday(ctx)
	if err != nil {
		t.Fatalf("MarketToday: %v", err)
	}
	found := false
	for _, i := range today.Instruments {
		if i.Symbol == symbol {
			found = true
			if i.SessionState != "published" || i.ReferencePriceKobo != 4000 {
				t.Errorf("today: %+v", i)
			}
		}
	}
	if !found {
		t.Errorf("%s not in today's market: %s", symbol, dump(today))
	}

	// 3. A tap.
	tr, err := c.DeliverTap(ctx, TapRequest{
		TapRef: tap.String(), MerchantRef: merchant.String(), CardholderRef: cardholder.String(),
		CardholderDisplayName: "Ada", AmountKobo: 1_000_000,
		ChargedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("DeliverTap: %v", err)
	}
	t.Logf("tap: %s", dump(tr))
	if tr.TapRef != tap.String() || tr.FeeKobo != 5000 || tr.BuybackFundingKobo != 500 ||
		tr.IntentState != "allocated" || tr.AllocatedUnits != 12_500_000 || tr.PriceKobo != 4000 ||
		tr.Symbol == nil || *tr.Symbol != symbol || tr.LockUntil == "" || tr.PresentmentID == "" || tr.IntentID == "" {
		t.Errorf("tap response = %+v", tr)
	}

	// 4. The cardholder's holdings.
	h, err := c.Holdings(ctx, cardholder.String())
	if err != nil {
		t.Fatalf("Holdings: %v", err)
	}
	t.Logf("holdings: %s", dump(h))
	if h.CardholderRef != cardholder.String() || len(h.Holdings) != 1 {
		t.Fatalf("holdings = %+v", h)
	}
	hd := h.Holdings[0]
	if hd.Symbol != symbol || hd.Units != 12_500_000 || hd.Shares != "0.125" || hd.LockedUnits != hd.Units ||
		hd.SellableUnits != 0 || hd.NextUnlock == "" || hd.CostKobo != 500 || hd.ValueKobo != 500 ||
		hd.ReferencePriceKobo != 4000 || hd.LegalName == "" {
		t.Errorf("holding = %+v", hd)
	}
	var lotCount int
	if err := json.Unmarshal(hd.Lots, &lotCount); err != nil || lotCount != 1 {
		t.Errorf("lots = %s (%v), want 1", hd.Lots, err)
	}
	if hd.LastSession == nil || hd.LastSession.PriceKobo != 4000 {
		t.Errorf("last_session = %+v", hd.LastSession)
	}
	one, err := c.Holding(ctx, cardholder.String(), symbol)
	if err != nil {
		t.Fatalf("Holding: %v", err)
	}
	var lots []Lot
	if err := json.Unmarshal(one.Lots, &lots); err != nil || len(lots) != 1 || lots[0].Units != 12_500_000 ||
		lots[0].TapRef != tap.String() || lots[0].TransferableFrom == "" {
		t.Errorf("lots = %s (%v)", one.Lots, err)
	}
	act, err := c.Activity(ctx, cardholder.String(), 10)
	if err != nil {
		t.Fatalf("Activity: %v", err)
	}
	if len(act) != 1 || act[0].TapRef != tap.String() || act[0].State != "allocated" || act[0].Units != 12_500_000 ||
		act[0].FundingKobo != 500 || act[0].Symbol == nil || *act[0].Symbol != symbol {
		t.Errorf("activity = %s", dump(act))
	}

	// 5. The business with its live cap table.
	d, err := c.GetBusiness(ctx, merchant.String())
	if err != nil {
		t.Fatalf("GetBusiness: %v", err)
	}
	t.Logf("business detail: %s", dump(d))
	if d.State != "listed" || d.Symbol != symbol || d.TreasuryRemaining <= 0 || d.Holders < 1 ||
		d.SharesAuthorised != 1_000_000_000_000_000 || d.DailyReleaseUnits != 50_000_000_000_000 ||
		d.ReleasedToday < 12_500_000 || len(d.TopHolders) < 1 || d.Halted.Halted {
		t.Errorf("business detail = %+v", d)
	}
	if d.LastSession == nil || d.LastSession.PriceKobo != 4000 || d.LastSession.Source != "carry_forward" ||
		d.LastSession.Date == "" || d.LastSession.State == "" {
		t.Errorf("last_session = %+v", d.LastSession)
	}
	page, err := c.ListHolders(ctx, merchant.String(), 10, "")
	if err != nil {
		t.Fatalf("ListHolders: %v", err)
	}
	holderSeen := false
	for _, r := range page.Holders {
		if r.CardholderRef == cardholder.String() {
			holderSeen = true
			if r.Units != 12_500_000 || r.LockedUnits != 12_500_000 || r.CostKobo != 500 || r.FirstAcquired == "" {
				t.Errorf("holder row = %+v", r)
			}
		}
	}
	if !holderSeen {
		t.Errorf("cardholder not among holders: %s", dump(page))
	}

	// 6. Reverse the tap.
	rv, err := c.ReverseTap(ctx, tap.String(), ReverseRequest{Reason: "merchant_reversal"})
	if err != nil {
		t.Fatalf("ReverseTap: %v", err)
	}
	t.Logf("reverse: %s", dump(rv))
	if rv.State != "reversed" || rv.UnwoundUnits != 12_500_000 {
		t.Errorf("reverse = %+v", rv)
	}
	h, err = c.Holdings(ctx, cardholder.String())
	if err != nil {
		t.Fatalf("Holdings after reverse: %v", err)
	}
	for _, hd := range h.Holdings {
		if hd.Units != 0 {
			t.Errorf("after reverse still holds %s", dump(hd))
		}
	}
	if h.TotalValueKobo != 0 {
		t.Errorf("after reverse total value = %d", h.TotalValueKobo)
	}
	fmt.Println("live round trip OK:", symbol)
}
