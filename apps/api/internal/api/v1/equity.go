package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/equity"
	"github.com/usezoracle/tapp/api/internal/money"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// The equity market, as the merchant app and the cardholder app see it.
//
// Two handlers, both thin: BusinessHandler registers a merchant with the
// market and reads back what it holds; HoldingsHandler reads what a
// cardholder holds. Neither decides anything about shares -- Freedom does --
// and what they add is the translation from the rail's wire (kobo, units) to
// this API's (money.Amount, {units, shares}), and the two ids Freedom keys
// everything on: the sender profile for a merchant, the user for a holder.

// disabledMessage is what every equity endpoint says when there is no
// market configured. One sentence, the same everywhere, naming the cure.
const disabledMessage = "Equity is not enabled on this deployment (FREEDOM_BASE_URL and FREEDOM_RAIL_TOKEN are not set)"

// unreachableMessage is what a cardholder sees when the market is down.
const unreachableMessage = "The equity market is unreachable right now; try again shortly"

// units is a quantity of shares on the wire: the exact figure and a human
// one, together, for the same reason money travels with its currency.
type units struct {
	Units  int64  `json:"units"`
	Shares string `json:"shares"`
}

func unitsOf(n int64) units { return units{Units: n, Shares: equity.Shares(n)} }

func kobo(n int64) money.Amount { return money.New(n, money.NGN) }

// koboOrNull is a price that may be absent: zero on the rail means "none",
// and money.Amount{} goes out as null rather than as ₦0.00.
func koboOrNull(n int64) money.Amount {
	if n == 0 {
		return money.Amount{}
	}
	return kobo(n)
}

// nullable turns "" into null on the wire.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// writeRailError maps a failed rail call to a response.
func writeRailError(ctx *gin.Context, what string, err error) {
	switch {
	case errors.Is(err, equity.ErrDisabled):
		u.APIResponse(ctx, http.StatusNotFound, "error", disabledMessage, nil)
	case equity.StatusOf(err) == http.StatusNotFound:
		u.APIResponse(ctx, http.StatusNotFound, "error", "Not found on the equity market", nil)
	case equity.StatusOf(err) == http.StatusBadRequest || equity.StatusOf(err) == http.StatusUnprocessableEntity:
		logger.Errorf("equity: %s: %v", what, err)
		u.APIResponse(ctx, http.StatusBadRequest, "error", "The equity market refused the request", err.Error())
	case equity.IsUnreachable(err):
		logger.Errorf("equity: %s: %v", what, err)
		u.APIResponse(ctx, http.StatusServiceUnavailable, "error", unreachableMessage, nil)
	default:
		logger.Errorf("equity: %s: %v", what, err)
		u.APIResponse(ctx, http.StatusBadGateway, "error", "The equity market answered with an error", nil)
	}
}

// ------------------------------------------------------------- business

// BusinessHandler serves /v1/sender/me/business.
type BusinessHandler struct {
	Pool   *pgxpool.Pool
	Client *equity.Client
	// Merchant resolves the caller to a sender profile id.
	Merchant func(*gin.Context) (uuid.UUID, bool)
}

var (
	rcNumberRe = regexp.MustCompile(`^(RC|BN)\d+$`)
	symbolRe   = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,11}$`)
)

// businessRequest is the rail request minus the refs, which the API fills.
type businessRequest struct {
	LegalName   string `json:"legal_name"`
	TradingName string `json:"trading_name"`
	RCNumber    string `json:"rc_number"`
	MCC         string `json:"mcc"`
	Symbol      string `json:"symbol"`

	Evidence equity.Evidence `json:"evidence"`

	// ReferencePrice is money like everywhere else on this API; it is
	// turned into kobo for the rail.
	ReferencePrice        money.Amount    `json:"reference_price"`
	SharesAuthorisedUnits int64           `json:"shares_authorised_units"`
	DailyReleaseUnits     int64           `json:"daily_release_units"`
	CofundBPS             int             `json:"cofund_bps"`
	Holders               []equity.Holder `json:"holders"`
}

// validate says what is wrong, field by field, so a merchant filling in a
// form is told everything at once rather than one thing per attempt.
func (r *businessRequest) validate() map[string]string {
	problems := map[string]string{}
	r.LegalName = strings.TrimSpace(r.LegalName)
	r.TradingName = strings.TrimSpace(r.TradingName)
	r.RCNumber = strings.ToUpper(strings.TrimSpace(r.RCNumber))
	r.MCC = strings.TrimSpace(r.MCC)
	r.Symbol = strings.ToUpper(strings.TrimSpace(r.Symbol))

	if r.LegalName == "" {
		problems["legal_name"] = "required"
	}
	if !rcNumberRe.MatchString(r.RCNumber) {
		problems["rc_number"] = "must be a CAC number like RC1483920 or BN2345678"
	}
	if !symbolRe.MatchString(r.Symbol) {
		problems["symbol"] = "must be 3 to 12 characters, A-Z and 0-9, starting with a letter"
	}
	e := r.Evidence
	if e.TradingMonths < 0 {
		problems["evidence.trading_months"] = "must not be negative"
	}
	if e.SharesInIssue < 0 {
		problems["evidence.shares_in_issue"] = "must not be negative"
	}
	if e.PublicShares < 0 {
		problems["evidence.public_shares"] = "must not be negative"
	}
	if e.Holders < 0 {
		problems["evidence.holders"] = "must not be negative"
	}
	if e.TreasuryUnits < 0 {
		problems["evidence.treasury_units"] = "must not be negative"
	}
	switch {
	case r.ReferencePrice.Currency() == "":
		problems["reference_price"] = "required, as {minor, currency}"
	case r.ReferencePrice.Currency() != money.NGN:
		problems["reference_price"] = "must be in NGN"
	case !r.ReferencePrice.IsPositive():
		problems["reference_price"] = "must be greater than zero"
	}
	if r.SharesAuthorisedUnits < 0 {
		problems["shares_authorised_units"] = "must not be negative"
	}
	if r.DailyReleaseUnits < 0 {
		problems["daily_release_units"] = "must not be negative"
	}
	if r.CofundBPS < 0 || r.CofundBPS > 10_000 {
		problems["cofund_bps"] = "must be between 0 and 10000"
	}
	for i, h := range r.Holders {
		if _, err := uuid.Parse(h.CardholderRef); err != nil {
			problems["holders["+strconv.Itoa(i)+"].cardholder_ref"] = "must be a user id"
		}
		if h.Units <= 0 {
			problems["holders["+strconv.Itoa(i)+"].units"] = "must be greater than zero"
		}
	}
	return problems
}

// businessRecord is the stored row.
type businessRecord struct {
	SenderID            uuid.UUID
	LegalName           string
	TradingName         string
	RCNumber            string
	MCC                 string
	Symbol              string
	State               string
	Findings            []equity.Finding
	InstrumentID        *string
	ReferencePriceMinor int64
	SubmittedAt         time.Time
	DecidedAt           *time.Time
}

// businessView is what both POST and GET answer.
type businessView struct {
	SenderID     string           `json:"sender_id"`
	LegalName    string           `json:"legal_name"`
	TradingName  string           `json:"trading_name"`
	RCNumber     string           `json:"rc_number"`
	MCC          string           `json:"mcc"`
	Symbol       string           `json:"symbol"`
	State        string           `json:"state"` // submitted | listed | rejected
	Findings     []equity.Finding `json:"findings"`
	InstrumentID *string          `json:"instrument_id"`
	// ReferencePrice is the admission price, from the stored record.
	ReferencePrice money.Amount `json:"reference_price"`
	SubmittedAt    time.Time    `json:"submitted_at"`
	DecidedAt      *time.Time   `json:"decided_at"`

	// Live is the market's cap table right now, or null when the market
	// could not be reached (LiveError says why) or the business is not
	// listed. The stored fields above are always present.
	Live      *capTableView `json:"live"`
	LiveError *string       `json:"live_error,omitempty"`
}

type topHolderView struct {
	CardholderRef string `json:"cardholder_ref"`
	units
}

type sessionView struct {
	Date   string       `json:"date"`
	State  string       `json:"state"`
	Price  money.Amount `json:"price"`
	Volume units        `json:"volume"`
}

type capTableView struct {
	SharesAuthorised  units           `json:"shares_authorised"`
	InIssue           units           `json:"in_issue"`
	TreasuryRemaining units           `json:"treasury_remaining"`
	ReleasedToday     units           `json:"released_today"`
	DailyRelease      units           `json:"daily_release"`
	Holders           int             `json:"holders"`
	TopHolders        []topHolderView `json:"top_holders"`
	PendingFunding    money.Amount    `json:"pending_funding"`
	EscrowedFunding   money.Amount    `json:"escrowed_funding"`
	ReferencePrice    money.Amount    `json:"reference_price"`
	LastSession       *sessionView    `json:"last_session"`
	Halted            bool            `json:"halted"`
	HaltReason        string          `json:"halt_reason,omitempty"`
}

func capTableOf(d *equity.BusinessDetail) *capTableView {
	v := &capTableView{
		SharesAuthorised:  unitsOf(d.SharesAuthorised),
		InIssue:           unitsOf(d.InIssue),
		TreasuryRemaining: unitsOf(d.TreasuryRemaining),
		ReleasedToday:     unitsOf(d.ReleasedToday),
		DailyRelease:      unitsOf(d.DailyReleaseUnits),
		Holders:           d.Holders,
		TopHolders:        []topHolderView{},
		PendingFunding:    kobo(d.PendingFundingKobo),
		EscrowedFunding:   kobo(d.EscrowedFundingKobo),
		ReferencePrice:    koboOrNull(d.ReferencePriceKobo),
		Halted:            d.Halted.Halted,
		HaltReason:        d.Halted.Reason,
	}
	for _, h := range d.TopHolders {
		v.TopHolders = append(v.TopHolders, topHolderView{CardholderRef: h.CardholderRef, units: unitsOf(h.Units)})
	}
	if s := d.LastSession; s != nil {
		volume := s.VolumeUnits
		if volume == 0 {
			volume = s.MatchedUnits
		}
		v.LastSession = &sessionView{
			Date: s.Date, State: s.State, Price: koboOrNull(s.PriceKobo), Volume: unitsOf(volume),
		}
	}
	return v
}

func viewOf(r businessRecord) businessView {
	findings := r.Findings
	if findings == nil {
		findings = []equity.Finding{}
	}
	return businessView{
		SenderID: r.SenderID.String(), LegalName: r.LegalName, TradingName: r.TradingName,
		RCNumber: r.RCNumber, MCC: r.MCC, Symbol: r.Symbol, State: r.State,
		Findings: findings, InstrumentID: r.InstrumentID,
		ReferencePrice: kobo(r.ReferencePriceMinor),
		SubmittedAt:    r.SubmittedAt, DecidedAt: r.DecidedAt,
	}
}

// owner resolves the user behind a sender profile: the merchant's own
// cardholder identity, which is what founders' shares are held under.
func (h *BusinessHandler) owner(ctx context.Context, sender uuid.UUID) (uuid.UUID, error) {
	var user uuid.UUID
	err := h.Pool.QueryRow(ctx,
		`SELECT user_sender_profile FROM sender_profiles WHERE id = $1`, sender).Scan(&user)
	return user, err
}

func (h *BusinessHandler) load(ctx context.Context, sender uuid.UUID) (businessRecord, error) {
	var (
		r        businessRecord
		findings []byte
	)
	err := h.Pool.QueryRow(ctx, `
		SELECT sender_id, legal_name, trading_name, rc_number, mcc, symbol, state, findings,
		       freedom_instrument_id, reference_price_minor, submitted_at, decided_at
		  FROM merchant_businesses WHERE sender_id = $1`, sender).
		Scan(&r.SenderID, &r.LegalName, &r.TradingName, &r.RCNumber, &r.MCC, &r.Symbol, &r.State,
			&findings, &r.InstrumentID, &r.ReferencePriceMinor, &r.SubmittedAt, &r.DecidedAt)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(findings, &r.Findings); err != nil {
		return r, err
	}
	return r, nil
}

// Create registers the merchant's business with the market.
//
// POST /v1/sender/me/business. The market decides; this validates what can
// be validated without it, forwards, and keeps the answer. A rejection is a
// 200 with findings, not an error: the merchant needs to read them.
func (h *BusinessHandler) Create(ctx *gin.Context) {
	if !h.Client.Enabled() {
		u.APIResponse(ctx, http.StatusNotFound, "error", disabledMessage, nil)
		return
	}
	merchant, ok := h.Merchant(ctx)
	if !ok {
		return
	}
	var req businessRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", err.Error())
		return
	}
	if problems := req.validate(); len(problems) > 0 {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid business details", problems)
		return
	}
	owner, err := h.owner(ctx, merchant)
	if err != nil {
		logger.Errorf("equity: business owner of %s: %v", merchant, err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to resolve merchant", nil)
		return
	}
	if req.Holders == nil {
		req.Holders = []equity.Holder{}
	}

	resp, err := h.Client.CreateBusiness(ctx.Request.Context(), equity.BusinessRequest{
		MerchantRef: merchant.String(), CardholderRef: owner.String(),
		LegalName: req.LegalName, TradingName: req.TradingName, RCNumber: req.RCNumber,
		MCC: req.MCC, Symbol: req.Symbol, Evidence: req.Evidence,
		ReferencePriceKobo:    req.ReferencePrice.Minor(),
		SharesAuthorisedUnits: req.SharesAuthorisedUnits,
		DailyReleaseUnits:     req.DailyReleaseUnits,
		CofundBPS:             req.CofundBPS,
		Holders:               req.Holders,
	})
	if err != nil {
		writeRailError(ctx, "create business", err)
		return
	}

	now := time.Now()
	record := businessRecord{
		SenderID: merchant, LegalName: req.LegalName, TradingName: req.TradingName,
		RCNumber: req.RCNumber, MCC: req.MCC, Symbol: req.Symbol,
		State: "submitted", Findings: resp.Findings,
		ReferencePriceMinor: req.ReferencePrice.Minor(), SubmittedAt: now,
	}
	switch resp.State {
	case "listed", "rejected":
		record.State = resp.State
		record.DecidedAt = &now
	}
	if resp.Symbol != "" {
		record.Symbol = resp.Symbol
	}
	if resp.InstrumentID != "" {
		record.InstrumentID = &resp.InstrumentID
	}
	if resp.ReferencePriceKobo > 0 {
		record.ReferencePriceMinor = resp.ReferencePriceKobo
	}
	if record.Findings == nil {
		record.Findings = []equity.Finding{}
	}
	findings, _ := json.Marshal(record.Findings)
	if _, err := h.Pool.Exec(ctx, `
		INSERT INTO merchant_businesses
			(sender_id, legal_name, trading_name, rc_number, mcc, symbol, state, findings,
			 freedom_instrument_id, reference_price_minor, submitted_at, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (sender_id) DO UPDATE
		   SET legal_name = EXCLUDED.legal_name, trading_name = EXCLUDED.trading_name,
		       rc_number = EXCLUDED.rc_number, mcc = EXCLUDED.mcc, symbol = EXCLUDED.symbol,
		       state = EXCLUDED.state, findings = EXCLUDED.findings,
		       freedom_instrument_id = EXCLUDED.freedom_instrument_id,
		       reference_price_minor = EXCLUDED.reference_price_minor,
		       submitted_at = EXCLUDED.submitted_at, decided_at = EXCLUDED.decided_at`,
		record.SenderID, record.LegalName, record.TradingName, record.RCNumber, record.MCC,
		record.Symbol, record.State, findings, record.InstrumentID, record.ReferencePriceMinor,
		record.SubmittedAt, record.DecidedAt); err != nil {
		// The market has the listing; only our copy failed. Say so rather
		// than let the merchant submit again and get the same record back.
		logger.Errorf("equity: store business %s: %v", merchant, err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"The market accepted the business but it could not be saved; try again", nil)
		return
	}

	view := viewOf(record)
	if record.State == "listed" {
		// The listing exists; show the cap table it starts with. Not
		// fatal: the record is the answer, the cap table is a bonus.
		if detail, err := h.Client.GetBusiness(ctx.Request.Context(), merchant.String()); err == nil {
			view.Live = capTableOf(detail)
		} else {
			view.LiveError = nullable(err.Error())
		}
	}
	message := "Business listed"
	if record.State == "rejected" {
		message = "Business not admitted; see findings"
	}
	u.APIResponse(ctx, http.StatusOK, "success", message, view)
}

// Get is the stored record, with the market's live cap table merged in.
//
// GET /v1/sender/me/business. The market being down does not hide the
// record: live is null and live_error says why.
func (h *BusinessHandler) Get(ctx *gin.Context) {
	if !h.Client.Enabled() {
		u.APIResponse(ctx, http.StatusNotFound, "error", disabledMessage, nil)
		return
	}
	merchant, ok := h.Merchant(ctx)
	if !ok {
		return
	}
	record, err := h.load(ctx, merchant)
	if errors.Is(err, pgx.ErrNoRows) {
		u.APIResponse(ctx, http.StatusNotFound, "error", "No business registered for this merchant", nil)
		return
	}
	if err != nil {
		logger.Errorf("equity: load business %s: %v", merchant, err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to load business", nil)
		return
	}
	view := viewOf(record)
	if record.State == "listed" {
		detail, err := h.Client.GetBusiness(ctx.Request.Context(), merchant.String())
		if err != nil {
			logger.Warnf("equity: live cap table for %s: %v", merchant, err)
			view.LiveError = nullable(err.Error())
		} else {
			view.Live = capTableOf(detail)
		}
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Business retrieved", view)
}

type holderView struct {
	CardholderRef string       `json:"cardholder_ref"`
	Holding       units        `json:"holding"`
	Locked        units        `json:"locked"`
	Cost          money.Amount `json:"cost"`
	FirstAcquired string       `json:"first_acquired"`
}

// Holders lists everyone holding the merchant's shares.
//
// GET /v1/sender/me/business/holders?limit=&cursor=
func (h *BusinessHandler) Holders(ctx *gin.Context) {
	if !h.Client.Enabled() {
		u.APIResponse(ctx, http.StatusNotFound, "error", disabledMessage, nil)
		return
	}
	merchant, ok := h.Merchant(ctx)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(ctx.Query("limit"))
	page, err := h.Client.ListHolders(ctx.Request.Context(), merchant.String(), limit, ctx.Query("cursor"))
	if err != nil {
		writeRailError(ctx, "list holders", err)
		return
	}
	holders := make([]holderView, 0, len(page.Holders))
	for _, r := range page.Holders {
		holders = append(holders, holderView{
			CardholderRef: r.CardholderRef, Holding: unitsOf(r.Units), Locked: unitsOf(r.LockedUnits),
			Cost: kobo(r.CostKobo), FirstAcquired: r.FirstAcquired,
		})
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Holders retrieved", gin.H{
		"holders":     holders,
		"next_cursor": nullable(page.NextCursor),
	})
}

// ------------------------------------------------------------- holdings

// HoldingsHandler serves the cardholder's shares.
type HoldingsHandler struct {
	Client *equity.Client
	User   func(*gin.Context) (uuid.UUID, bool)
}

type holdingSessionView struct {
	Date   string       `json:"date"`
	Price  money.Amount `json:"price"`
	Source string       `json:"source"`
}

type lotView struct {
	units
	Cost             money.Amount `json:"cost"`
	AcquiredAt       string       `json:"acquired_at"`
	TransferableFrom string       `json:"transferable_from"`
	TapID            *string      `json:"tap_id"`
}

type holdingView struct {
	Symbol      string `json:"symbol"`
	LegalName   string `json:"legal_name"`
	TradingName string `json:"trading_name"`

	Holding    units   `json:"holding"`
	Sellable   units   `json:"sellable"`
	Locked     units   `json:"locked"`
	NextUnlock *string `json:"next_unlock"`

	Cost           money.Amount `json:"cost"`
	ReferencePrice money.Amount `json:"reference_price"`
	Value          money.Amount `json:"value"`
	// ChangeBPS is value against cost, in basis points: 156 is +1.56%.
	ChangeBPS int64 `json:"change_bps"`
	// LotCount is how many acquisitions make up the holding.
	LotCount    int                 `json:"lot_count"`
	LastSession *holdingSessionView `json:"last_session"`

	// Lots and Prices are present on the single-holding endpoint only.
	Lots   []lotView       `json:"lots,omitempty"`
	Prices json.RawMessage `json:"prices,omitempty"`
}

func holdingOf(h equity.Holding, detail bool) holdingView {
	v := holdingView{
		Symbol: h.Symbol, LegalName: h.LegalName, TradingName: h.TradingName,
		Holding: unitsOf(h.Units), Sellable: unitsOf(h.SellableUnits), Locked: unitsOf(h.LockedUnits),
		NextUnlock: nullable(h.NextUnlock),
		Cost:       kobo(h.CostKobo), ReferencePrice: koboOrNull(h.ReferencePriceKobo), Value: kobo(h.ValueKobo),
		ChangeBPS: h.ChangeBPS,
	}
	if s := h.LastSession; s != nil {
		v.LastSession = &holdingSessionView{Date: s.Date, Price: koboOrNull(s.PriceKobo), Source: s.Source}
	}
	// `lots` is a count on the list and the lots themselves on the detail.
	var count int
	var lots []equity.Lot
	if json.Unmarshal(h.Lots, &count) == nil {
		v.LotCount = count
	} else if json.Unmarshal(h.Lots, &lots) == nil {
		v.LotCount = len(lots)
	}
	if detail {
		v.Lots = make([]lotView, 0, len(lots))
		for _, l := range lots {
			v.Lots = append(v.Lots, lotView{
				units: unitsOf(l.Units), Cost: kobo(l.CostKobo),
				AcquiredAt: l.AcquiredAt, TransferableFrom: l.TransferableFrom, TapID: nullable(l.TapRef),
			})
		}
		v.Prices = h.Prices
		if len(v.Prices) == 0 {
			v.Prices = json.RawMessage("[]")
		}
	}
	return v
}

// List is everything the cardholder holds.
//
// GET /v1/me/holdings
func (h *HoldingsHandler) List(ctx *gin.Context) {
	if !h.Client.Enabled() {
		u.APIResponse(ctx, http.StatusNotFound, "error", disabledMessage, gin.H{"holdings": []holdingView{}})
		return
	}
	user, ok := h.User(ctx)
	if !ok {
		return
	}
	resp, err := h.Client.Holdings(ctx.Request.Context(), user.String())
	if err != nil {
		if equity.StatusOf(err) == http.StatusNotFound {
			// A cardholder the market has never seen holds nothing.
			u.APIResponse(ctx, http.StatusOK, "success", "Holdings retrieved", gin.H{
				"as_of": time.Now().Format("2006-01-02"), "total_value": kobo(0), "total_cost": kobo(0),
				"holdings": []holdingView{},
			})
			return
		}
		writeRailError(ctx, "holdings", err)
		return
	}
	holdings := make([]holdingView, 0, len(resp.Holdings))
	for _, hd := range resp.Holdings {
		holdings = append(holdings, holdingOf(hd, false))
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Holdings retrieved", gin.H{
		"as_of":       resp.AsOf,
		"total_value": kobo(resp.TotalValueKobo),
		"total_cost":  kobo(resp.TotalCostKobo),
		"holdings":    holdings,
	})
}

// Get is one holding with its lots and price history.
//
// GET /v1/me/holdings/:symbol
func (h *HoldingsHandler) Get(ctx *gin.Context) {
	if !h.Client.Enabled() {
		u.APIResponse(ctx, http.StatusNotFound, "error", disabledMessage, nil)
		return
	}
	user, ok := h.User(ctx)
	if !ok {
		return
	}
	symbol := strings.ToUpper(strings.TrimSpace(ctx.Param("symbol")))
	if !symbolRe.MatchString(symbol) {
		u.APIResponse(ctx, http.StatusNotFound, "error", "No such holding", nil)
		return
	}
	hd, err := h.Client.Holding(ctx.Request.Context(), user.String(), symbol)
	if err != nil {
		if equity.StatusOf(err) == http.StatusNotFound {
			u.APIResponse(ctx, http.StatusNotFound, "error", "No such holding", nil)
			return
		}
		writeRailError(ctx, "holding", err)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Holding retrieved", holdingOf(*hd, true))
}

type activityView struct {
	TapID   string       `json:"tap_id"`
	Symbol  *string      `json:"symbol"`
	Funding money.Amount `json:"funding"`
	// State: allocated | pending | escrowed.
	State  string       `json:"state"`
	Bought units        `json:"bought"`
	Price  money.Amount `json:"price"`
	At     string       `json:"at"`
}

// Activity is what the cardholder's taps have bought, newest first.
//
// GET /v1/me/equity-activity?limit=
func (h *HoldingsHandler) Activity(ctx *gin.Context) {
	if !h.Client.Enabled() {
		u.APIResponse(ctx, http.StatusNotFound, "error", disabledMessage, gin.H{"activity": []activityView{}})
		return
	}
	user, ok := h.User(ctx)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(ctx.Query("limit"))
	rows, err := h.Client.Activity(ctx.Request.Context(), user.String(), limit)
	if err != nil {
		if equity.StatusOf(err) == http.StatusNotFound {
			u.APIResponse(ctx, http.StatusOK, "success", "Activity retrieved", gin.H{"activity": []activityView{}})
			return
		}
		writeRailError(ctx, "activity", err)
		return
	}
	out := make([]activityView, 0, len(rows))
	for _, r := range rows {
		symbol := r.Symbol
		if symbol != nil && *symbol == "" {
			symbol = nil
		}
		out = append(out, activityView{
			TapID: r.TapRef, Symbol: symbol, Funding: kobo(r.FundingKobo), State: r.State,
			Bought: unitsOf(r.Units), Price: koboOrNull(r.PriceKobo), At: r.At,
		})
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Activity retrieved", gin.H{"activity": out})
}
