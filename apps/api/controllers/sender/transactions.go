package sender

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/ent"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/transactions"
	"github.com/usezoracle/tapp/api/storage"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// The merchant's transactions: their card taps and their offramps, read from
// the tables that hold them through internal/transactions.
//
// The shape is the one the merchant app already renders -- the legacy
// payment-order response, camelCase -- so the app that is in people's hands
// keeps working. What is behind it changed: the old handlers read
// payment_orders, a table nothing has written to since card taps went on
// chain, which is why merchants saw an empty list.

// merchantOrderResponse is the subset of the legacy PaymentOrderResponse the
// app reads, with the tap's own facts in its fields.
type merchantOrderResponse struct {
	ID         uuid.UUID `json:"id"`
	Amount     string    `json:"amount"`     // token sold (USDC), or fiat for an offramp
	AmountPaid string    `json:"amountPaid"` // same, once settled
	Token      string    `json:"token"`
	Rate       string    `json:"rate"` // fiat per token, what the order carried
	Network    string    `json:"network"`
	GatewayID  string    `json:"gatewayId"`
	TxHash     string    `json:"txHash"`
	Reference  string    `json:"reference"`
	Status     string    `json:"status"`
	SenderFee  string    `json:"senderFee"`
	Recipient  struct {
		Institution       string `json:"institution"`
		AccountIdentifier string `json:"accountIdentifier"`
		AccountName       string `json:"accountName"`
		Memo              string `json:"memo"`
		Currency          string `json:"currency"`
	} `json:"recipient"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Equity is what the equity market did with a tap, or null when the tap
	// was never sent to one. Additive: an app that does not know about it
	// ignores it.
	Equity *equityView `json:"equity"`

	// SettlementRail is what pays the merchant for a tap: "paycrest" (the
	// cardholder's USDC sold on chain), "fintava" (naira paid out of the
	// cardholder's own wallet) or "mixed" (both). Empty for an offramp.
	// Legs has each leg's own amount and status. Additive, like Equity.
	SettlementRail string    `json:"settlementRail,omitempty"`
	Legs           []legView `json:"legs,omitempty"`
}

// legView is one rail's share of a tap, for the merchant app.
type legView struct {
	Rail      string `json:"rail"` // paycrest | fintava
	Amount    string `json:"amount"`
	Status    string `json:"status"` // pending | processing | settled | failed
	Reference string `json:"reference,omitempty"`
}

func legsOf(legs []transactions.Leg) []legView {
	out := make([]legView, 0, len(legs))
	for _, l := range legs {
		out = append(out, legView{Rail: l.Rail, Amount: units(l.Amount), Status: appStatus(l.Status), Reference: l.Reference})
	}
	return out
}

// equityView is a tap's outcome on the equity market, for the merchant app.
type equityView struct {
	// State: queued | failed | escrowed | pending | allocated | reversed.
	State string `json:"state"`
	// Symbol is null when the merchant is not listed.
	Symbol *string `json:"symbol"`
	// Units are 1e-8 of a share; Shares is the same figure for a human.
	Units  int64  `json:"units"`
	Shares string `json:"shares"`
	// Price is what the shares were bought at, or null when none were.
	Price money.Amount `json:"price"`
}

func equityOf(e *transactions.Equity) *equityView {
	if e == nil {
		return nil
	}
	v := &equityView{State: e.State, Units: e.Units, Shares: e.Shares, Price: e.Price}
	if e.Symbol != "" {
		v.Symbol = &e.Symbol
	}
	return v
}

// appStatus maps the list's vocabulary onto the app's.
//
// failed becomes pending on purpose: the merchant is still owed and will be
// paid, by another order or by an operator. Telling them it was cancelled
// would be false, and the app has no word for "owed, being sorted out".
func appStatus(s string) string {
	switch s {
	case transactions.StatusFailed:
		return "pending"
	case transactions.StatusReversed:
		return "refunded"
	default:
		return s
	}
}

// merchantStatus is the inverse, for the app's filter tabs.
func merchantStatus(app string) string {
	switch app {
	case "refunded":
		return transactions.StatusReversed
	default:
		return app
	}
}

func units(a money.Amount) string {
	return decimal.New(a.Minor(), -int32(a.Currency().Exponent())).
		StringFixed(int32(a.Currency().Exponent()))
}

func merchantView(t transactions.Transaction) merchantOrderResponse {
	r := merchantOrderResponse{
		ID: t.ID, Status: appStatus(t.Status),
		GatewayID: t.OrderID, TxHash: t.TxHash, Reference: t.ID.String(),
		SenderFee: units(t.Fee),
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Equity:         equityOf(t.Equity),
		SettlementRail: t.SettlementRail, Legs: legsOf(t.Legs),
	}
	r.Recipient.Institution = t.Bank.Institution
	r.Recipient.AccountIdentifier = t.Bank.AccountNumber
	r.Recipient.AccountName = t.Bank.AccountName
	r.Recipient.Currency = string(t.Owed.Currency())
	if t.Reason != "" {
		r.Recipient.Memo = "Reversed: " + strings.ReplaceAll(t.Reason, "_", " ")
	}

	switch {
	case t.Kind == transactions.KindTap && t.SettlementRail == transactions.RailFintava:
		// Paid in naira out of the cardholder's wallet: no token was sold,
		// and the figure the app derives is the fiat itself at par. The
		// rail's reference stands where a transaction hash would.
		r.Token, r.Network = string(t.Owed.Currency()), "bank"
		r.Amount, r.Rate = units(t.Owed), "1"
		r.TxHash = t.RailRef
	case t.Kind == transactions.KindTap:
		// The app shows amount × rate as the fiat figure, so amount is the
		// USDC sold and rate is fiat per USDC -- exactly what the on-chain
		// order carried. A tap not yet priced shows the estimate.
		r.Token, r.Network = "USDC", "base"
		sold := decimal.New(t.SoldMicro, -6)
		r.Amount = sold.StringFixed(6)
		owed := decimal.New(t.Owed.Minor(), -int32(t.Owed.Currency().Exponent()))
		if sold.IsPositive() {
			r.Rate = owed.Div(sold).StringFixed(2)
		} else {
			// Nothing to sell against: show the fiat itself at par so the
			// figure the app derives is still the right one.
			r.Amount, r.Rate = owed.StringFixed(2), "1"
		}
	default:
		r.Token, r.Network = string(t.Owed.Currency()), "bank"
		r.Amount, r.Rate = units(t.Owed), "1"
	}
	if t.Status == transactions.StatusSettled {
		r.AmountPaid = r.Amount
	}
	return r
}

// party is the two ids one merchant answers to.
func party(ctx *gin.Context) (profile, user uuid.UUID, ok bool) {
	senderCtx, found := ctx.Get("sender")
	if !found {
		u.APIResponse(ctx, http.StatusUnauthorized, "error", "Invalid API key or token", nil)
		return uuid.Nil, uuid.Nil, false
	}
	sender := senderCtx.(*ent.SenderProfile)
	if err := storage.Pool.QueryRow(ctx, `
		SELECT user_sender_profile FROM sender_profiles WHERE id = $1`, sender.ID).Scan(&user); err != nil {
		logger.Errorf("sender transactions: resolve user: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to fetch transactions", nil)
		return uuid.Nil, uuid.Nil, false
	}
	return sender.ID, user, true
}

// GetPaymentOrders lists the merchant's transactions, newest first.
func (ctrl *SenderController) GetPaymentOrders(ctx *gin.Context) {
	profile, user, ok := party(ctx)
	if !ok {
		return
	}
	page, _, pageSize := u.Paginate(ctx)
	items, total, err := transactions.List(ctx, storage.Pool, transactions.Filter{
		MerchantProfile: &profile, MerchantUser: &user,
		Status: merchantStatus(strings.TrimSpace(ctx.Query("status"))),
		Page:   page, Limit: pageSize,
	})
	if err != nil {
		logger.Errorf("sender GetPaymentOrders: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to fetch payment orders", nil)
		return
	}
	orders := make([]merchantOrderResponse, 0, len(items))
	for _, t := range items {
		orders = append(orders, merchantView(t))
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Payment orders retrieved successfully", gin.H{
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
		"orders":   orders,
	})
}

// GetPaymentOrderByID is one of the merchant's transactions.
func (ctrl *SenderController) GetPaymentOrderByID(ctx *gin.Context) {
	profile, user, ok := party(ctx)
	if !ok {
		return
	}
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		u.APIResponse(ctx, http.StatusNotFound, "error", "Payment order not found", nil)
		return
	}
	t, err := transactions.Get(ctx, storage.Pool, id)
	if err == nil && t.Merchant != profile && t.Merchant != user {
		// Somebody else's. Not found, not forbidden: the id's existence is
		// not theirs to learn either.
		err = transactions.ErrNotFound
	}
	if errors.Is(err, transactions.ErrNotFound) {
		u.APIResponse(ctx, http.StatusNotFound, "error", "Payment order not found", nil)
		return
	}
	if err != nil {
		logger.Errorf("sender GetPaymentOrderByID: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to fetch payment order", nil)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "The order has been successfully retrieved", merchantView(t))
}

// Stats totals the merchant's transactions for a period.
//
// Volume is in dollars -- what the home screen prints -- as the USDC sold
// to pay the merchant's taps; an offramp's fiat is not part of it.
func (ctrl *SenderController) Stats(ctx *gin.Context) {
	profile, user, ok := party(ctx)
	if !ok {
		return
	}
	var since *time.Time
	now := time.Now().UTC()
	switch strings.ToLower(ctx.Query("period")) {
	case "", "all":
	case "today":
		t := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		since = &t
	case "week":
		t := now.AddDate(0, 0, -7)
		since = &t
	case "month":
		t := now.AddDate(0, -1, 0)
		since = &t
	default:
		u.APIResponse(ctx, http.StatusBadRequest, "error", "period must be one of: today, week, month, all", nil)
		return
	}
	s, err := transactions.StatsFor(ctx, storage.Pool, profile, user, money.NGN, since)
	if err != nil {
		logger.Errorf("sender Stats: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to fetch sender stats", nil)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Sender stats retrieved successfully", gin.H{
		"totalOrders":      s.Count,
		"totalOrderVolume": decimal.New(s.SoldMicro, -6).StringFixed(2),
		"totalFeeEarnings": units(s.Fees),
	})
}
