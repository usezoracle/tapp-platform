package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/equity"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
)

func equityTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://tapp:tapp@localhost:5433/tapp?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no test database (%v)", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database (%v)", err)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			first_name text NOT NULL DEFAULT '', last_name text NOT NULL DEFAULT '',
			email text UNIQUE NOT NULL, password text NOT NULL DEFAULT '',
			scope text NOT NULL DEFAULT 'sender');
		CREATE TABLE IF NOT EXISTS sender_profiles (
			id uuid PRIMARY KEY, updated_at timestamptz NOT NULL DEFAULT now(),
			domain_whitelist jsonb NOT NULL DEFAULT '[]'::jsonb,
			user_sender_profile uuid UNIQUE NOT NULL REFERENCES users(id))`); err != nil {
		pool.Close()
		t.Fatalf("ent tables: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// fakeRail is a scripted Freedom: a map from "METHOD /path" to status and body.
type fakeRail struct {
	answers map[string]struct {
		status int
		body   string
	}
	got map[string]map[string]any
}

func newFakeRail() *fakeRail {
	return &fakeRail{answers: map[string]struct {
		status int
		body   string
	}{}, got: map[string]map[string]any{}}
}

func (r *fakeRail) on(method, path string, status int, body string) {
	r.answers[method+" "+path] = struct {
		status int
		body   string
	}{status, body}
}

func (r *fakeRail) serve(t *testing.T) *equity.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key := req.Method + " " + req.URL.Path
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.got[key] = body
		a, ok := r.answers[key]
		if !ok {
			t.Errorf("unexpected fakeRail call %s", key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	return equity.New(srv.URL, "tok")
}

type envelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func call(t *testing.T, r http.Handler, method, path, body string) (int, envelope) {
	t.Helper()
	var in *bytes.Reader
	if body != "" {
		in = bytes.NewReader([]byte(body))
	} else {
		in = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, in)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s %s: not JSON: %s", method, path, rec.Body.String())
	}
	return rec.Code, env
}

func newMerchant(t *testing.T, pool *pgxpool.Pool) (profile, user uuid.UUID) {
	t.Helper()
	profile, user = uuid.New(), uuid.New()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, first_name, last_name, email) VALUES ($1, 'Mama', 'Put', $2)`,
		user, user.String()+"@test.local"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sender_profiles (id, user_sender_profile) VALUES ($1, $2)`,
		profile, user); err != nil {
		t.Fatal(err)
	}
	return
}

const businessBody = `{
  "legal_name": "Mama Put Kitchens Ltd", "trading_name": "Mama Put",
  "rc_number": "rc1483920", "mcc": "5812", "symbol": "mamaput",
  "evidence": {"trading_months": 30, "audited_accounts": true, "auditor_on_list": true,
    "shares_in_issue": 800000000000000, "public_shares": 120000000000000, "holders": 31,
    "treasury_units": 180000000000000, "board_resolution": true, "directors_clear": true,
    "net_assets": {"minor": 20000000000, "currency": "NGN"}, "revenue": {"minor": 12000000000, "currency": "NGN"}},
  "shares_authorised_units": 1000000000000000, "daily_release_units": 50000000000000, "cofund_bps": 0
}`

func TestABusinessIsListedStoredAndReadBackWithItsCapTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := equityTestPool(t)
	profile, user := newMerchant(t, pool)
	r := newFakeRail()
	// Fair value = net assets + 1.0× revenue = ₦320,000,000; over 8,000,000
	// shares in issue that is ₦40.00: the exchange names 4000 kobo.
	r.on("POST", "/v1/rail/businesses", 201, `{"symbol":"MAMAPUT","instrument_id":"inst-1","state":"listed",
		"findings":[{"criterion":"free_float","met":true,"detail":"15.0% ≥ 10%"}],
		"reference_price_kobo":4000,"fair_value_kobo":32000000000,"treasury_units":130000000000000}`)
	r.on("GET", "/v1/rail/businesses/"+profile.String(), 200, `{"symbol":"MAMAPUT","instrument_id":"inst-1","state":"listed",
		"findings":[],"reference_price_kobo":4000,"treasury_units":130000000000000,
		"shares_authorised":1000000000000000,"in_issue":800000000000000,"treasury_remaining":130000000000000,
		"released_today":12500000,"daily_release_units":50000000000000,"holders":32,
		"top_holders":[{"cardholder_ref":"`+user.String()+`","units":50000000000000}],
		"pending_funding_kobo":500,"escrowed_funding_kobo":0,
		"last_session":{"date":"2026-09-17","state":"published","price_kobo":4000,"volume_units":12500000},
		"halted":false}`)

	h := &BusinessHandler{Pool: pool, Client: r.serve(t),
		Merchant: func(*gin.Context) (uuid.UUID, bool) { return profile, true }}
	router := gin.New()
	router.POST("/business", h.Create)
	router.GET("/business", h.Get)

	code, env := call(t, router, "POST", "/business", businessBody)
	if code != 200 || env.Status != "success" {
		t.Fatalf("POST = %d %s %s", code, env.Message, env.Data)
	}
	sent := r.got["POST /v1/rail/businesses"]
	if sent["merchant_ref"] != profile.String() || sent["cardholder_ref"] != user.String() ||
		sent["rc_number"] != "RC1483920" || sent["symbol"] != "MAMAPUT" {
		t.Errorf("forwarded %v", sent)
	}
	// The audited figures go to the rail in kobo; no price is proposed.
	evidence, _ := sent["evidence"].(map[string]any)
	if evidence["net_assets_kobo"] != float64(20_000_000_000) || evidence["revenue_kobo"] != float64(12_000_000_000) ||
		evidence["shares_in_issue"] != float64(800_000_000_000_000) {
		t.Errorf("forwarded evidence %v", evidence)
	}
	if _, has := sent["reference_price_kobo"]; has {
		t.Errorf("a price was proposed to the exchange: %v", sent)
	}
	for _, leak := range []string{"reference_price", "net_assets", "revenue"} {
		if _, has := evidence[leak]; has {
			t.Errorf("money.Amount %q leaked onto the fakeRail: %v", leak, sent)
		}
		if _, has := sent[leak]; has {
			t.Errorf("money.Amount %q leaked onto the fakeRail: %v", leak, sent)
		}
	}

	var view struct {
		SenderID string `json:"sender_id"`
		Symbol   string `json:"symbol"`
		State    string `json:"state"`
		Findings []struct {
			Criterion string `json:"criterion"`
			Met       bool   `json:"met"`
		} `json:"findings"`
		InstrumentID   *string `json:"instrument_id"`
		ReferencePrice struct {
			Minor    int64  `json:"minor"`
			Currency string `json:"currency"`
			Display  string `json:"display"`
		} `json:"reference_price"`
		Evidence struct {
			NetAssets struct {
				Minor   int64  `json:"minor"`
				Display string `json:"display"`
			} `json:"net_assets"`
			Revenue struct {
				Minor   int64  `json:"minor"`
				Display string `json:"display"`
			} `json:"revenue"`
		} `json:"evidence"`
		FairValue struct {
			Minor    int64  `json:"minor"`
			Currency string `json:"currency"`
			Display  string `json:"display"`
		} `json:"fair_value"`
		DecidedAt *string `json:"decided_at"`
		Live      *struct {
			SharesAuthorised struct {
				Units  int64  `json:"units"`
				Shares string `json:"shares"`
			} `json:"shares_authorised"`
			Holders    int `json:"holders"`
			TopHolders []struct {
				CardholderRef string `json:"cardholder_ref"`
				Shares        string `json:"shares"`
			} `json:"top_holders"`
			PendingFunding struct {
				Minor int64 `json:"minor"`
			} `json:"pending_funding"`
			LastSession *struct {
				Date  string `json:"date"`
				Price struct {
					Display string `json:"display"`
				} `json:"price"`
				Volume struct {
					Shares string `json:"shares"`
				} `json:"volume"`
			} `json:"last_session"`
			Halted bool `json:"halted"`
		} `json:"live"`
		LiveError *string `json:"live_error"`
	}
	decode := func(data json.RawMessage) {
		t.Helper()
		view.Live, view.LiveError = nil, nil
		if err := json.Unmarshal(data, &view); err != nil {
			t.Fatalf("decode: %v: %s", err, data)
		}
	}
	decode(env.Data)
	if view.SenderID != profile.String() || view.Symbol != "MAMAPUT" || view.State != "listed" ||
		len(view.Findings) != 1 || !view.Findings[0].Met || view.InstrumentID == nil || *view.InstrumentID != "inst-1" ||
		view.ReferencePrice.Minor != 4000 || view.ReferencePrice.Currency != "NGN" || view.ReferencePrice.Display == "" ||
		view.DecidedAt == nil {
		t.Errorf("POST view = %s", env.Data)
	}
	checkFinancials := func(when string) {
		t.Helper()
		if view.Evidence.NetAssets.Minor != 20_000_000_000 || view.Evidence.NetAssets.Display != "₦200,000,000.00" ||
			view.Evidence.Revenue.Minor != 12_000_000_000 || view.Evidence.Revenue.Display != "₦120,000,000.00" ||
			view.FairValue.Minor != 32_000_000_000 || view.FairValue.Currency != "NGN" ||
			view.FairValue.Display != "₦320,000,000.00" {
			t.Errorf("%s financials = evidence %+v fair_value %+v", when, view.Evidence, view.FairValue)
		}
	}
	checkFinancials("POST")

	// Stored.
	var state, symbol string
	var priceMinor, netAssets, revenue, fairValue int64
	if err := pool.QueryRow(context.Background(),
		`SELECT state, symbol, reference_price_minor, net_assets_minor, revenue_minor, fair_value_minor
		   FROM merchant_businesses WHERE sender_id = $1`, profile).
		Scan(&state, &symbol, &priceMinor, &netAssets, &revenue, &fairValue); err != nil {
		t.Fatalf("not stored: %v", err)
	}
	if state != "listed" || symbol != "MAMAPUT" || priceMinor != 4000 ||
		netAssets != 20_000_000_000 || revenue != 12_000_000_000 || fairValue != 32_000_000_000 {
		t.Errorf("stored %s %s price=%d net_assets=%d revenue=%d fair_value=%d",
			state, symbol, priceMinor, netAssets, revenue, fairValue)
	}

	// Read back, merged with the live cap table.
	code, env = call(t, router, "GET", "/business", "")
	if code != 200 {
		t.Fatalf("GET = %d %s", code, env.Message)
	}
	decode(env.Data)
	checkFinancials("GET")
	if view.ReferencePrice.Minor != 4000 {
		t.Errorf("GET reference_price = %+v", view.ReferencePrice)
	}
	if view.Live == nil {
		t.Fatalf("GET has no live cap table: %s", env.Data)
	}
	live := view.Live
	if live.SharesAuthorised.Units != 1_000_000_000_000_000 || live.SharesAuthorised.Shares != "10000000" ||
		live.Holders != 32 || len(live.TopHolders) != 1 || live.TopHolders[0].Shares != "500000" ||
		live.PendingFunding.Minor != 500 || live.LastSession == nil || live.LastSession.Price.Display == "" ||
		live.LastSession.Volume.Shares != "0.125" || live.Halted {
		t.Errorf("live = %+v", live)
	}

	// The market goes away: the record still answers, live is null.
	h.Client = equity.New("http://127.0.0.1:1", "tok")
	code, env = call(t, router, "GET", "/business", "")
	if code != 200 {
		t.Fatalf("GET with market down = %d %s", code, env.Message)
	}
	decode(env.Data)
	if view.Live != nil || view.LiveError == nil || view.State != "listed" {
		t.Errorf("with market down: live=%v live_error=%v state=%s", view.Live, view.LiveError, view.State)
	}
}

func TestARejectedBusinessIsStoredWithItsFindings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := equityTestPool(t)
	profile, _ := newMerchant(t, pool)
	r := newFakeRail()
	r.on("POST", "/v1/rail/businesses", 200, `{"symbol":"MAMAPUT","state":"rejected",
		"findings":[{"criterion":"free_float","met":false,"detail":"5.0% < 10%"}]}`)
	h := &BusinessHandler{Pool: pool, Client: r.serve(t),
		Merchant: func(*gin.Context) (uuid.UUID, bool) { return profile, true }}
	router := gin.New()
	router.POST("/business", h.Create)
	router.GET("/business", h.Get)

	// A proposed price is still accepted and forwarded, for a caller that
	// has not caught up; the exchange ignores it.
	body := strings.Replace(businessBody, `"shares_authorised_units"`,
		`"reference_price": {"minor": 9999, "currency": "NGN"}, "shares_authorised_units"`, 1)
	code, env := call(t, router, "POST", "/business", body)
	if code != 200 || !strings.Contains(string(env.Data), `"state":"rejected"`) ||
		!strings.Contains(string(env.Data), `"met":false`) {
		t.Errorf("POST = %d %s", code, env.Data)
	}
	if sent := r.got["POST /v1/rail/businesses"]; sent["reference_price_kobo"] != float64(9999) {
		t.Errorf("proposed price not passed through: %v", sent)
	}
	code, env = call(t, router, "GET", "/business", "")
	// Not listed: no cap table is asked for, and none is expected. The
	// exchange named no price and no fair value, so neither is invented.
	if code != 200 || !strings.Contains(string(env.Data), `"live":null`) ||
		!strings.Contains(string(env.Data), `"fair_value":null`) {
		t.Errorf("GET = %d %s", code, env.Data)
	}
	if !strings.Contains(string(env.Data), `"net_assets":{"minor":20000000000`) {
		t.Errorf("GET lost the submitted evidence: %s", env.Data)
	}
}

func TestABusinessNeedNotProposeAPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := equityTestPool(t)
	profile, _ := newMerchant(t, pool)
	r := newFakeRail()
	// The exchange sets the price and no price was proposed: the stored
	// price is the exchange's, and none was stored before it answered.
	r.on("POST", "/v1/rail/businesses", 200, `{"symbol":"MAMAPUT","state":"rejected","findings":[]}`)
	h := &BusinessHandler{Pool: pool, Client: r.serve(t),
		Merchant: func(*gin.Context) (uuid.UUID, bool) { return profile, true }}
	router := gin.New()
	router.POST("/business", h.Create)

	code, env := call(t, router, "POST", "/business", businessBody)
	if code != 200 || !strings.Contains(string(env.Data), `"reference_price":null`) {
		t.Errorf("POST without a price = %d %s %s", code, env.Message, env.Data)
	}
}

func TestABusinessIsValidatedBeforeTheMarketSeesIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newFakeRail() // no answers: any call is a test failure
	h := &BusinessHandler{Client: r.serve(t),
		Merchant: func(*gin.Context) (uuid.UUID, bool) { return uuid.New(), true }}
	router := gin.New()
	router.POST("/business", h.Create)

	code, env := call(t, router, "POST", "/business", `{"legal_name":"", "rc_number":"12345", "symbol":"1abc",
		"evidence":{"trading_months":-1, "revenue":{"minor":0,"currency":"NGN"}}, "reference_price":{"minor":0,"currency":"NGN"}}`)
	if code != 400 {
		t.Fatalf("code = %d %s", code, env.Message)
	}
	var problems map[string]string
	_ = json.Unmarshal(env.Data, &problems)
	for _, field := range []string{"legal_name", "rc_number", "symbol", "evidence.trading_months",
		"evidence.net_assets", "evidence.revenue", "reference_price"} {
		if problems[field] == "" {
			t.Errorf("%s was not reported; got %v", field, problems)
		}
	}
	// Absent is "required"; present but zero is "greater than zero"; the
	// wrong currency is named. In plain words, for a form.
	if !strings.Contains(problems["evidence.net_assets"], "required") ||
		!strings.Contains(problems["evidence.revenue"], "greater than zero") {
		t.Errorf("problems = %v", problems)
	}
	code, env = call(t, router, "POST", "/business", strings.Replace(businessBody,
		`"revenue": {"minor": 12000000000, "currency": "NGN"}`, `"revenue": {"minor": 12000000000, "currency": "USD"}`, 1))
	problems = nil
	_ = json.Unmarshal(env.Data, &problems)
	if code != 400 || problems["evidence.revenue"] != "must be in NGN" || len(problems) != 1 {
		t.Errorf("USD revenue: %d %v", code, problems)
	}
	if len(r.got) != 0 {
		t.Errorf("the market was called with an invalid request: %v", r.got)
	}
}

func TestEquityEndpointsSaySoWhenThereIsNoMarket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := &BusinessHandler{Client: nil, Merchant: func(*gin.Context) (uuid.UUID, bool) { return uuid.New(), true }}
	h := &HoldingsHandler{Client: nil, User: func(*gin.Context) (uuid.UUID, bool) { return uuid.New(), true }}
	router := gin.New()
	router.POST("/business", b.Create)
	router.GET("/business", b.Get)
	router.GET("/holdings", h.List)
	router.GET("/holdings/:symbol", h.Get)
	router.GET("/equity-activity", h.Activity)

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/business", businessBody}, {"GET", "/business", ""},
		{"GET", "/holdings", ""}, {"GET", "/holdings/MAMAPUT", ""}, {"GET", "/equity-activity", ""},
	} {
		code, env := call(t, router, c.method, c.path, c.body)
		if code != 404 || !strings.Contains(env.Message, "FREEDOM_BASE_URL") {
			t.Errorf("%s %s = %d %q", c.method, c.path, code, env.Message)
		}
	}
	_, env := call(t, router, "GET", "/holdings", "")
	if string(env.Data) != `{"holdings":[]}` {
		t.Errorf("disabled holdings data = %s, want an empty list", env.Data)
	}
}

func TestHoldingsAreReshapedForTheCardholder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := uuid.New()
	r := newFakeRail()
	r.on("GET", "/v1/rail/cardholders/"+user.String()+"/holdings", 200, `{"cardholder_ref":"`+user.String()+`",
		"as_of":"2026-09-18","total_value_kobo":812500,"total_cost_kobo":800000,
		"holdings":[{"symbol":"MAMAPUT","legal_name":"Mama Put Kitchens Ltd","trading_name":"Mama Put",
		  "units":20312500,"shares":"0.203125","sellable_units":0,"locked_units":20312500,"next_unlock":"2027-01-16",
		  "cost_kobo":800000,"reference_price_kobo":4000,"value_kobo":812500,"change_bps":156,"lots":3,
		  "last_session":{"date":"2026-09-17","price_kobo":4000,"source":"auction"}}]}`)
	r.on("GET", "/v1/rail/cardholders/"+user.String()+"/holdings/MAMAPUT", 200, `{"symbol":"MAMAPUT",
		"legal_name":"Mama Put Kitchens Ltd","units":20312500,"locked_units":20312500,"cost_kobo":800000,
		"reference_price_kobo":4000,"value_kobo":812500,"change_bps":156,
		"lots":[{"units":12500000,"cost_kobo":500,"acquired_at":"2026-09-18","transferable_from":"2027-01-16","tap_ref":"tap-1"}],
		"prices":[{"date":"2026-09-17","price_kobo":4000}]}`)
	r.on("GET", "/v1/rail/cardholders/"+user.String()+"/activity", 200, `[{"tap_ref":"tap-1","symbol":"MAMAPUT",
		"funding_kobo":500,"state":"allocated","units":12500000,"price_kobo":4000,"at":"2026-09-18T11:24:03Z"},
		{"tap_ref":"tap-2","symbol":null,"funding_kobo":250,"state":"escrowed","units":0,"price_kobo":0,"at":"2026-09-18T12:00:00Z"}]`)

	h := &HoldingsHandler{Client: r.serve(t), User: func(*gin.Context) (uuid.UUID, bool) { return user, true }}
	router := gin.New()
	router.GET("/holdings", h.List)
	router.GET("/holdings/:symbol", h.Get)
	router.GET("/equity-activity", h.Activity)

	code, env := call(t, router, "GET", "/holdings", "")
	if code != 200 {
		t.Fatalf("holdings = %d %s", code, env.Message)
	}
	var list struct {
		AsOf       string `json:"as_of"`
		TotalValue struct {
			Minor    int64  `json:"minor"`
			Currency string `json:"currency"`
			Display  string `json:"display"`
		} `json:"total_value"`
		Holdings []struct {
			Symbol  string `json:"symbol"`
			Holding struct {
				Units  int64  `json:"units"`
				Shares string `json:"shares"`
			} `json:"holding"`
			Locked struct {
				Shares string `json:"shares"`
			} `json:"locked"`
			NextUnlock *string `json:"next_unlock"`
			Value      struct {
				Display string `json:"display"`
			} `json:"value"`
			ChangeBPS   int64 `json:"change_bps"`
			LotCount    int   `json:"lot_count"`
			LastSession *struct {
				Price struct {
					Minor int64 `json:"minor"`
				} `json:"price"`
			} `json:"last_session"`
		} `json:"holdings"`
	}
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatal(err)
	}
	if list.AsOf != "2026-09-18" || list.TotalValue.Minor != 812_500 || list.TotalValue.Currency != "NGN" ||
		list.TotalValue.Display != "₦8,125.00" || len(list.Holdings) != 1 {
		t.Errorf("list = %s", env.Data)
	}
	hd := list.Holdings[0]
	if hd.Symbol != "MAMAPUT" || hd.Holding.Units != 20_312_500 || hd.Holding.Shares != "0.203125" ||
		hd.Locked.Shares != "0.203125" || hd.NextUnlock == nil || hd.Value.Display != "₦8,125.00" ||
		hd.ChangeBPS != 156 || hd.LotCount != 3 || hd.LastSession == nil || hd.LastSession.Price.Minor != 4000 {
		t.Errorf("holding = %+v", hd)
	}
	if strings.Contains(string(env.Data), "kobo") {
		t.Errorf("kobo leaked to the app: %s", env.Data)
	}

	code, env = call(t, router, "GET", "/holdings/mamaput", "")
	if code != 200 {
		t.Fatalf("holding = %d %s", code, env.Message)
	}
	var one struct {
		LotCount int `json:"lot_count"`
		Lots     []struct {
			Shares string `json:"shares"`
			Cost   struct {
				Minor int64 `json:"minor"`
			} `json:"cost"`
			TapID *string `json:"tap_id"`
		} `json:"lots"`
		Prices json.RawMessage `json:"prices"`
	}
	if err := json.Unmarshal(env.Data, &one); err != nil {
		t.Fatal(err)
	}
	if one.LotCount != 1 || len(one.Lots) != 1 || one.Lots[0].Shares != "0.125" || one.Lots[0].Cost.Minor != 500 ||
		one.Lots[0].TapID == nil || *one.Lots[0].TapID != "tap-1" || !strings.Contains(string(one.Prices), "4000") {
		t.Errorf("holding detail = %s", env.Data)
	}

	code, env = call(t, router, "GET", "/equity-activity", "")
	if code != 200 {
		t.Fatalf("activity = %d %s", code, env.Message)
	}
	var act struct {
		Activity []struct {
			TapID   string  `json:"tap_id"`
			Symbol  *string `json:"symbol"`
			Funding struct {
				Display string `json:"display"`
			} `json:"funding"`
			State  string `json:"state"`
			Bought struct {
				Shares string `json:"shares"`
			} `json:"bought"`
			Price json.RawMessage `json:"price"`
		} `json:"activity"`
	}
	if err := json.Unmarshal(env.Data, &act); err != nil {
		t.Fatal(err)
	}
	if len(act.Activity) != 2 || act.Activity[0].TapID != "tap-1" || act.Activity[0].Symbol == nil ||
		act.Activity[0].Funding.Display != "₦5.00" || act.Activity[0].Bought.Shares != "0.125" ||
		act.Activity[1].Symbol != nil || act.Activity[1].State != "escrowed" || string(act.Activity[1].Price) != "null" {
		t.Errorf("activity = %s", env.Data)
	}

	// The market down is a 503, not a 500 and not an empty list.
	h.Client = equity.New("http://127.0.0.1:1", "tok")
	if code, env := call(t, router, "GET", "/holdings", ""); code != 503 || !strings.Contains(env.Message, "unreachable") {
		t.Errorf("market down: %d %q", code, env.Message)
	}
}
