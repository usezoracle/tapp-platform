// Package equity is the door between Tapp and the equity market (Freedom).
//
// Tapp is a card rail: a tap debits a cardholder and credits a merchant, in
// one transaction, in this database. Freedom is a market: a merchant's fee is
// split, part of it buys the merchant's own shares, and the cardholder ends
// up holding them. This package is the only place the two meet, and it meets
// them in exactly three ways:
//
//   - a typed HTTP client for Freedom's rail API (this file), used verbatim by
//     the business and holdings endpoints, which are thin proxies;
//   - an outbox (outbox.go): a tap's transaction queues one row, and a worker
//     delivers it afterwards. A tap never waits on the market;
//   - the read model's view of what the market answered (transactions.Equity).
//
// Everything in it is optional. With no FREEDOM_BASE_URL or FREEDOM_RAIL_TOKEN
// the client is nil, nothing is queued, and every endpoint says so.
package equity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls Freedom's rail API.
//
// A nil *Client is the disabled feature: every method reports ErrDisabled.
// Callers check Enabled() to answer differently, and never have to guard
// against nil themselves.
type Client struct {
	// BaseURL is Freedom's origin, e.g. https://api.freedom.example. The
	// /v1/rail prefix is appended here.
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a client, or returns nil when either setting is empty.
func New(baseURL, token string) *Client {
	if baseURL == "" || token == "" {
		return nil
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled reports whether there is a market to talk to.
func (c *Client) Enabled() bool { return c != nil }

// ErrDisabled means no market is configured.
var ErrDisabled = errors.New("equity: not enabled (FREEDOM_BASE_URL and FREEDOM_RAIL_TOKEN are required)")

// StatusError is a response Freedom sent that was not a success.
//
// Kept distinct from a transport failure because the two mean opposite
// things to a retry: a 4xx is Freedom saying the request is wrong and will be
// wrong again; a timeout is nobody saying anything.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	body := e.Body
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	return fmt.Sprintf("equity: freedom answered %d: %s", e.Code, body)
}

// IsUnreachable reports whether err is the market not answering at all --
// as opposed to answering with a refusal.
func IsUnreachable(err error) bool {
	if err == nil {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		// A gateway in front of Freedom answering for it is still nobody
		// home.
		return se.Code == http.StatusBadGateway || se.Code == http.StatusServiceUnavailable ||
			se.Code == http.StatusGatewayTimeout
	}
	return !errors.Is(err, ErrDisabled)
}

// StatusOf returns the HTTP status err carries, or 0 when it is not a
// StatusError.
func StatusOf(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// do sends one request and decodes a 2xx body into out (when non-nil).
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	if c == nil {
		return ErrDisabled
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("equity: encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/v1/rail"+path, body)
	if err != nil {
		return fmt.Errorf("equity: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("equity: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("equity: %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("equity: %s %s: decode %d response: %w", method, path, resp.StatusCode, err)
	}
	return nil
}

// ------------------------------------------------------------ businesses

// Evidence is what the listing rulebook is assessed against.
type Evidence struct {
	TradingMonths   int   `json:"trading_months"`
	AuditedAccounts bool  `json:"audited_accounts"`
	AuditorOnList   bool  `json:"auditor_on_list"`
	SharesInIssue   int64 `json:"shares_in_issue"`
	PublicShares    int64 `json:"public_shares"`
	Holders         int   `json:"holders"`
	TreasuryUnits   int64 `json:"treasury_units"`
	BoardResolution bool  `json:"board_resolution"`
	DirectorsClear  bool  `json:"directors_clear"`
}

// Holder is a founder's allocation made at listing, from treasury.
type Holder struct {
	CardholderRef string `json:"cardholder_ref"`
	Units         int64  `json:"units"`
	Label         string `json:"label,omitempty"`
}

// BusinessRequest is POST /v1/rail/businesses.
type BusinessRequest struct {
	MerchantRef   string `json:"merchant_ref"`
	CardholderRef string `json:"cardholder_ref,omitempty"`

	LegalName   string `json:"legal_name"`
	TradingName string `json:"trading_name"`
	RCNumber    string `json:"rc_number"`
	MCC         string `json:"mcc"`
	Symbol      string `json:"symbol"`

	Evidence Evidence `json:"evidence"`

	ReferencePriceKobo    int64    `json:"reference_price_kobo"`
	SharesAuthorisedUnits int64    `json:"shares_authorised_units"`
	DailyReleaseUnits     int64    `json:"daily_release_units"`
	CofundBPS             int      `json:"cofund_bps"`
	Holders               []Holder `json:"holders"`
}

// Finding is one listing criterion, and whether it was met.
type Finding struct {
	Criterion string `json:"criterion"`
	Met       bool   `json:"met"`
	Detail    string `json:"detail"`
}

// BusinessResponse is what POST /v1/rail/businesses answers.
type BusinessResponse struct {
	Symbol             string    `json:"symbol"`
	InstrumentID       string    `json:"instrument_id"`
	State              string    `json:"state"` // listed | rejected
	Findings           []Finding `json:"findings"`
	ReferencePriceKobo int64     `json:"reference_price_kobo"`
	TreasuryUnits      int64     `json:"treasury_units"`
}

// CreateBusiness registers (lists) a merchant. Idempotent on MerchantRef.
func (c *Client) CreateBusiness(ctx context.Context, req BusinessRequest) (*BusinessResponse, error) {
	var out BusinessResponse
	if err := c.do(ctx, http.MethodPost, "/businesses", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TopHolder is one of the largest holders, as the cap table lists them.
type TopHolder struct {
	CardholderRef string `json:"cardholder_ref"`
	Units         int64  `json:"units"`
}

// Session is the last auction, as the cap table reports it.
//
// Every field is optional on the way in: the contract names the facts
// (date, state, price, volume) and this decodes the keys Freedom uses for
// them, tolerating any that are absent.
type Session struct {
	Date         string `json:"date"`
	State        string `json:"state"`
	PriceKobo    int64  `json:"price_kobo"`
	VolumeUnits  int64  `json:"volume_units"`
	Source       string `json:"source,omitempty"`
	MatchedUnits int64  `json:"matched_units,omitempty"`
}

// Halted is whether trading in the instrument is suspended.
//
// Freedom describes this as "bool + reason"; both a bare boolean and an
// object are accepted, so the wire shape can settle without breaking this.
type Halted struct {
	Halted bool   `json:"halted"`
	Reason string `json:"reason,omitempty"`
}

// UnmarshalJSON accepts `true`, `false`, or `{"halted": bool, "reason": …}`.
func (h *Halted) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch s {
	case "null":
		*h = Halted{}
		return nil
	case "true", "false":
		h.Halted, h.Reason = s == "true", ""
		return nil
	}
	var obj struct {
		Halted bool   `json:"halted"`
		Reason string `json:"reason"`
		// Freedom may spell it either way.
		HaltReason string `json:"halt_reason"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	h.Halted = obj.Halted
	h.Reason = obj.Reason
	if h.Reason == "" {
		h.Reason = obj.HaltReason
	}
	return nil
}

// BusinessDetail is GET /v1/rail/businesses/{merchant_ref}: the record plus
// the live cap table.
type BusinessDetail struct {
	BusinessResponse

	SharesAuthorised    int64       `json:"shares_authorised"`
	InIssue             int64       `json:"in_issue"`
	OnRegister          int64       `json:"on_register"`
	TreasuryRemaining   int64       `json:"treasury_remaining"`
	PriceKobo           int64       `json:"price_kobo"`
	MarketCapKobo       int64       `json:"market_cap_kobo"`
	ReleasedToday       int64       `json:"released_today"`
	DailyReleaseUnits   int64       `json:"daily_release_units"`
	Holders             int         `json:"holders"`
	TopHolders          []TopHolder `json:"top_holders"`
	PendingFundingKobo  int64       `json:"pending_funding_kobo"`
	EscrowedFundingKobo int64       `json:"escrowed_funding_kobo"`
	LastSession         *Session    `json:"last_session"`
	Halted              Halted      `json:"halted"`
}

// GetBusiness reads a merchant's record and cap table.
func (c *Client) GetBusiness(ctx context.Context, merchantRef string) (*BusinessDetail, error) {
	var out BusinessDetail
	if err := c.do(ctx, http.MethodGet, "/businesses/"+url.PathEscape(merchantRef), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HolderRow is one holder of a merchant's shares.
type HolderRow struct {
	CardholderRef string `json:"cardholder_ref"`
	Units         int64  `json:"units"`
	CostKobo      int64  `json:"cost_kobo"`
	FirstAcquired string `json:"first_acquired"`
	LockedUnits   int64  `json:"locked_units"`
}

// HoldersPage is GET /v1/rail/businesses/{merchant_ref}/holders.
type HoldersPage struct {
	Holders    []HolderRow `json:"holders"`
	NextCursor string      `json:"next_cursor"`
}

// ListHolders pages through a merchant's holders.
func (c *Client) ListHolders(ctx context.Context, merchantRef string, limit int, cursor string) (*HoldersPage, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	path := "/businesses/" + url.PathEscape(merchantRef) + "/holders"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out HoldersPage
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	if out.Holders == nil {
		out.Holders = []HolderRow{}
	}
	return &out, nil
}

// ------------------------------------------------------------------ taps

// TapRequest is POST /v1/rail/taps.
type TapRequest struct {
	TapRef                string `json:"tap_ref"`
	MerchantRef           string `json:"merchant_ref"`
	CardholderRef         string `json:"cardholder_ref"`
	CardholderDisplayName string `json:"cardholder_display_name"`
	AmountKobo            int64  `json:"amount_kobo"`
	ChargedAt             string `json:"charged_at"`
}

// TapResponse is what Freedom did with a tap.
//
// IntentState is the fact a merchant cares about: allocated means shares were
// bought; pending means the funding waits for a session with a price;
// escrowed means the merchant is not listed and the funding accrues.
type TapResponse struct {
	TapRef             string  `json:"tap_ref"`
	PresentmentID      string  `json:"presentment_id"`
	FeeKobo            int64   `json:"fee_kobo"`
	BuybackFundingKobo int64   `json:"buyback_funding_kobo"`
	IntentID           string  `json:"intent_id"`
	IntentState        string  `json:"intent_state"`
	AllocatedUnits     int64   `json:"allocated_units"`
	PriceKobo          int64   `json:"price_kobo"`
	Symbol             *string `json:"symbol"`
	LockUntil          string  `json:"lock_until"`
}

// DeliverTap reports a charged tap. Idempotent on TapRef.
func (c *Client) DeliverTap(ctx context.Context, req TapRequest) (*TapResponse, error) {
	var out TapResponse
	if err := c.do(ctx, http.MethodPost, "/taps", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReverseRequest is POST /v1/rail/taps/{tap_ref}/reverse.
type ReverseRequest struct {
	Reason string `json:"reason"`
}

// ReverseResponse is what Freedom unwound.
type ReverseResponse struct {
	State        string `json:"state"`
	UnwoundUnits int64  `json:"unwound_units"`
}

// ReverseTap reports a reversed tap.
func (c *Client) ReverseTap(ctx context.Context, tapRef string, req ReverseRequest) (*ReverseResponse, error) {
	var out ReverseResponse
	if err := c.do(ctx, http.MethodPost, "/taps/"+url.PathEscape(tapRef)+"/reverse", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----------------------------------------------------------- cardholders

// HoldingSession is the last price a holding was valued at.
type HoldingSession struct {
	Date      string `json:"date"`
	PriceKobo int64  `json:"price_kobo"`
	Source    string `json:"source"`
}

// Holding is one instrument a cardholder holds.
type Holding struct {
	Symbol             string          `json:"symbol"`
	LegalName          string          `json:"legal_name"`
	TradingName        string          `json:"trading_name"`
	Units              int64           `json:"units"`
	Shares             string          `json:"shares"`
	SellableUnits      int64           `json:"sellable_units"`
	LockedUnits        int64           `json:"locked_units"`
	NextUnlock         string          `json:"next_unlock"`
	CostKobo           int64           `json:"cost_kobo"`
	ReferencePriceKobo int64           `json:"reference_price_kobo"`
	ValueKobo          int64           `json:"value_kobo"`
	ChangeBPS          int64           `json:"change_bps"`
	Lots               json.RawMessage `json:"lots"` // a count in the list, the lots themselves in the detail
	LastSession        *HoldingSession `json:"last_session"`
	Prices             json.RawMessage `json:"prices,omitempty"` // detail only
}

// HoldingsResponse is GET /v1/rail/cardholders/{ref}/holdings.
type HoldingsResponse struct {
	CardholderRef  string    `json:"cardholder_ref"`
	AsOf           string    `json:"as_of"`
	TotalValueKobo int64     `json:"total_value_kobo"`
	TotalCostKobo  int64     `json:"total_cost_kobo"`
	Holdings       []Holding `json:"holdings"`
}

// Holdings lists everything a cardholder holds.
func (c *Client) Holdings(ctx context.Context, cardholderRef string) (*HoldingsResponse, error) {
	var out HoldingsResponse
	if err := c.do(ctx, http.MethodGet, "/cardholders/"+url.PathEscape(cardholderRef)+"/holdings", nil, &out); err != nil {
		return nil, err
	}
	if out.Holdings == nil {
		out.Holdings = []Holding{}
	}
	return &out, nil
}

// Lot is one acquisition inside a holding.
type Lot struct {
	Units            int64  `json:"units"`
	CostKobo         int64  `json:"cost_kobo"`
	AcquiredAt       string `json:"acquired_at"`
	TransferableFrom string `json:"transferable_from"`
	TapRef           string `json:"tap_ref,omitempty"`
}

// Holding reads one holding with its lots and price history.
func (c *Client) Holding(ctx context.Context, cardholderRef, symbol string) (*Holding, error) {
	var out Holding
	path := "/cardholders/" + url.PathEscape(cardholderRef) + "/holdings/" + url.PathEscape(symbol)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ActivityRow is one buyback intent, allocated or pending.
type ActivityRow struct {
	TapRef      string  `json:"tap_ref"`
	Symbol      *string `json:"symbol"`
	FundingKobo int64   `json:"funding_kobo"`
	State       string  `json:"state"`
	Units       int64   `json:"units"`
	PriceKobo   int64   `json:"price_kobo"`
	At          string  `json:"at"`
}

// ActivityResponse is GET /v1/rail/cardholders/{ref}/activity.
//
// Freedom may answer with a bare array or an object holding one; both are
// read.
type ActivityResponse struct {
	Activity []ActivityRow
}

func (a *ActivityResponse) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "[") {
		return json.Unmarshal(b, &a.Activity)
	}
	var obj struct {
		Activity []ActivityRow `json:"activity"`
		Intents  []ActivityRow `json:"intents"`
		Items    []ActivityRow `json:"items"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	switch {
	case obj.Activity != nil:
		a.Activity = obj.Activity
	case obj.Intents != nil:
		a.Activity = obj.Intents
	default:
		a.Activity = obj.Items
	}
	return nil
}

// Activity lists a cardholder's intents, newest first.
func (c *Client) Activity(ctx context.Context, cardholderRef string, limit int) ([]ActivityRow, error) {
	path := "/cardholders/" + url.PathEscape(cardholderRef) + "/activity"
	if limit > 0 {
		path += "?limit=" + fmt.Sprint(limit)
	}
	var out ActivityResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	if out.Activity == nil {
		out.Activity = []ActivityRow{}
	}
	return out.Activity, nil
}

// ---------------------------------------------------------------- market

// CloseBuyback is what the close did for one instrument's buyback.
type CloseBuyback struct {
	Intents  int    `json:"intents"`
	Units    int64  `json:"units"`
	Retried  int    `json:"retried"`
	Escrowed int    `json:"escrowed"`
	Refusal  string `json:"refusal,omitempty"`
}

// CloseRow is one instrument's close.
type CloseRow struct {
	Symbol       string       `json:"symbol"`
	InstrumentID string       `json:"instrument_id"`
	State        string       `json:"state"`
	PriceKobo    int64        `json:"price_kobo"`
	MatchedUnits int64        `json:"matched_units"`
	Buyback      CloseBuyback `json:"buyback"`
	Error        string       `json:"error,omitempty"`
}

// CloseMarket runs the close for every listed instrument. sessionDate is
// YYYY-MM-DD, or empty for today. Idempotent on Freedom's side.
//
// Not called by anything in this API in normal operation -- Freedom closes
// its own market on a schedule -- but the integration test needs a session
// with a price before a tap can allocate, and an operator may need it.
func (c *Client) CloseMarket(ctx context.Context, sessionDate string) ([]CloseRow, error) {
	var in map[string]string
	if sessionDate != "" {
		in = map[string]string{"session_date": sessionDate}
	}
	var out struct {
		Instruments []CloseRow `json:"instruments"`
	}
	if in == nil {
		// An empty object rather than no body: the rail reads the body
		// only when there is one, and either is accepted.
		in = map[string]string{}
	}
	if err := c.do(ctx, http.MethodPost, "/market/close", in, &out); err != nil {
		return nil, err
	}
	return out.Instruments, nil
}

// TodayInstrument is one instrument's standing today.
type TodayInstrument struct {
	Symbol             string `json:"symbol"`
	SessionState       string `json:"session_state"`
	ReferencePriceKobo int64  `json:"reference_price_kobo"`
	Halted             bool   `json:"halted"`
}

// Today is GET /v1/rail/market/today.
type Today struct {
	BusinessDate string            `json:"business_date"`
	IsTrading    bool              `json:"is_trading"`
	Phase        string            `json:"phase"`
	Instruments  []TodayInstrument `json:"instruments"`
}

// MarketToday reads the market's standing today.
func (c *Client) MarketToday(ctx context.Context) (*Today, error) {
	var out Today
	if err := c.do(ctx, http.MethodGet, "/market/today", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
