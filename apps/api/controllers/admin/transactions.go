package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/card/tap"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/transactions"
	"github.com/usezoracle/tapp/api/storage"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// TransactionsController is the operator's view of every payment: card taps
// and integrator offramps in one list, with the ledger's own record of each
// as its timeline.
type TransactionsController struct{}

func NewTransactionsController() *TransactionsController { return &TransactionsController{} }

type bankView struct {
	Institution   string `json:"institution"`
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
}

type transactionView struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`

	Currency string `json:"currency"`
	Amount   string `json:"amount"` // what the payer was charged
	Fee      string `json:"fee"`
	Owed     string `json:"owed"` // what the beneficiary receives

	MerchantID      string   `json:"merchant_id"`
	CardholderID    string   `json:"cardholder_id,omitempty"`
	CardholderEmail string   `json:"cardholder_email,omitempty"`
	Bank            bankView `json:"bank"`

	// What pays the merchant: paycrest (the cardholder's USDC sold on
	// chain), fintava (naira paid out of the cardholder's own wallet), or
	// mixed (both). Empty for an offramp. Legs has each leg's own state.
	SettlementRail string    `json:"settlement_rail,omitempty"`
	Legs           []legView `json:"legs,omitempty"`

	// The sale paying for a tap with a paycrest leg.
	SoldUSDC string `json:"sold_usdc,omitempty"`
	Round    int    `json:"round"`
	OrderID  string `json:"order_id,omitempty"`
	TxHash   string `json:"tx_hash,omitempty"`
	// The bank rail's reference for a tap on the fintava rail.
	RailRef   string `json:"rail_ref,omitempty"`
	LastError string `json:"last_error,omitempty"`
	Reason    string `json:"reason,omitempty"`

	// What the equity market did with a tap; null when it was never sent
	// to one.
	Equity *equityView `json:"equity"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	SettledAt string `json:"settled_at,omitempty"`
}

// legView is one rail's share of a tap.
type legView struct {
	Rail      string `json:"rail"` // paycrest | fintava
	Amount    string `json:"amount"`
	Status    string `json:"status"` // pending | processing | settled | failed
	Reference string `json:"reference,omitempty"`
	Error     string `json:"error,omitempty"`
	SettledAt string `json:"settled_at,omitempty"`
}

func legsOf(legs []transactions.Leg) []legView {
	out := make([]legView, 0, len(legs))
	for _, l := range legs {
		v := legView{Rail: l.Rail, Amount: plain(l.Amount), Status: l.Status, Reference: l.Reference, Error: l.Error}
		if l.SettledAt != nil {
			v.SettledAt = l.SettledAt.UTC().Format(tsLayout)
		}
		out = append(out, v)
	}
	return out
}

// equityView is a tap's outcome on the equity market.
type equityView struct {
	// State: queued | failed | escrowed | pending | allocated | reversed.
	State  string       `json:"state"`
	Symbol *string      `json:"symbol"`
	Units  int64        `json:"units"` // 1e-8 of a share
	Shares string       `json:"shares"`
	Price  money.Amount `json:"price"` // null when nothing was bought
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

type entryView struct {
	Owner   string `json:"owner"`
	Account string `json:"account"`
	Amount  string `json:"amount"`
	Reason  string `json:"reason"`
}

type eventView struct {
	At      string      `json:"at"`
	Type    string      `json:"type"`
	Entries []entryView `json:"entries"`
}

type transactionDetailView struct {
	transactionView
	Events []eventView `json:"events"`
}

// plain is an amount as a number: "1592.00", no symbol or grouping, the way
// a table or a spreadsheet wants it.
func plain(a money.Amount) string {
	return decimal.New(a.Minor(), -int32(a.Currency().Exponent())).
		StringFixed(int32(a.Currency().Exponent()))
}

func view(t transactions.Transaction) transactionView {
	v := transactionView{
		ID: t.ID.String(), Kind: t.Kind, Status: t.Status,
		Currency: string(t.Amount.Currency()),
		Amount:   plain(t.Amount), Fee: plain(t.Fee), Owed: plain(t.Owed),
		MerchantID:      t.Merchant.String(),
		CardholderEmail: t.CardholderEmail,
		Bank: bankView{
			Institution: t.Bank.Institution, AccountNumber: t.Bank.AccountNumber, AccountName: t.Bank.AccountName,
		},
		SettlementRail: t.SettlementRail, Legs: legsOf(t.Legs),
		Round: t.Round, OrderID: t.OrderID, TxHash: t.TxHash, RailRef: t.RailRef,
		LastError: t.LastError, Reason: t.Reason,
		Equity:    equityOf(t.Equity),
		CreatedAt: t.CreatedAt.UTC().Format(tsLayout),
		UpdatedAt: t.UpdatedAt.UTC().Format(tsLayout),
	}
	if t.Cardholder != nil {
		v.CardholderID = t.Cardholder.String()
	}
	if t.SoldMicro > 0 {
		v.SoldUSDC = t.Sold()
	}
	if t.SettledAt != nil {
		v.SettledAt = t.SettledAt.UTC().Format(tsLayout)
	}
	return v
}

// GetTransactions lists, newest first.
//
// Query: page, limit, status, kind, merchant (a sender profile id), since
// (RFC 3339).
func (c *TransactionsController) GetTransactions(ctx *gin.Context) {
	page, _, limit := u.Paginate(ctx)
	f := transactions.Filter{
		Page: page, Limit: limit,
		Status: strings.TrimSpace(ctx.Query("status")),
		Kind:   strings.TrimSpace(ctx.Query("kind")),
	}
	if m := strings.TrimSpace(ctx.Query("merchant")); m != "" {
		id, err := uuid.Parse(m)
		if err != nil {
			u.APIResponse(ctx, http.StatusBadRequest, "error", "merchant must be a sender profile id", nil)
			return
		}
		f.MerchantProfile = &id
	}
	if s := strings.TrimSpace(ctx.Query("since")); s != "" {
		at, err := time.Parse(time.RFC3339, s)
		if err != nil {
			u.APIResponse(ctx, http.StatusBadRequest, "error", "since must be RFC 3339", nil)
			return
		}
		f.Since = &at
	}

	items, total, err := transactions.List(ctx, storage.Pool, f)
	if err != nil {
		logger.Errorf("admin GetTransactions: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "failed to list transactions", nil)
		return
	}
	views := make([]transactionView, 0, len(items))
	for _, t := range items {
		views = append(views, view(t))
	}
	u.APIResponse(ctx, http.StatusOK, "success", "transactions", gin.H{
		"total":        total,
		"page":         page,
		"count":        len(views),
		"transactions": views,
	})
}

// GetTransaction is one payment with everything the ledger recorded for it.
func (c *TransactionsController) GetTransaction(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "invalid transaction id", nil)
		return
	}
	t, err := transactions.Get(ctx, storage.Pool, id)
	if errors.Is(err, transactions.ErrNotFound) {
		u.APIResponse(ctx, http.StatusNotFound, "error", "no such transaction", nil)
		return
	}
	if err != nil {
		logger.Errorf("admin GetTransaction: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "failed to read transaction", nil)
		return
	}
	events, err := transactions.Events(ctx, storage.Pool, id)
	if err != nil {
		logger.Errorf("admin GetTransaction: events: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "failed to read transaction", nil)
		return
	}
	d := transactionDetailView{transactionView: view(t), Events: make([]eventView, 0, len(events))}
	for _, e := range events {
		ev := eventView{At: e.At.UTC().Format(tsLayout), Type: e.Type}
		for _, en := range e.Entries {
			ev.Entries = append(ev.Entries, entryView{
				Owner: en.Owner, Account: en.Account, Amount: plain(en.Amount), Reason: en.Reason,
			})
		}
		d.Events = append(d.Events, ev)
	}
	u.APIResponse(ctx, http.StatusOK, "success", "transaction", d)
}

// ReverseTap refunds a card payment in full, from the console.
//
// The same operation a merchant performs from their own app, through the
// same service, so the books end in the same shape: the cardholder is made
// whole including the fee, the merchant's claim is withdrawn, the stock the
// tap bought is unwound on the exchange, and the reason is recorded against
// the tap. MONEY MOVEMENT, audited with the operator's name.
type ReverseTapController struct {
	Svc *tap.Service
}

func (c *ReverseTapController) ReverseTap(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "That is not a transaction id", nil)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Reason) == "" {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "A reversal must say why", nil)
		return
	}
	var merchant uuid.UUID
	if err := storage.Pool.QueryRow(ctx.Request.Context(),
		`SELECT merchant_id FROM card_taps WHERE id = $1`, id).Scan(&merchant); err != nil {
		u.APIResponse(ctx, http.StatusNotFound, "error", "No such tap", nil)
		return
	}
	if err := c.Svc.Reverse(ctx.Request.Context(), merchant, id, "console: "+strings.TrimSpace(body.Reason)); err != nil {
		u.APIResponse(ctx, http.StatusConflict, "error", err.Error(), nil)
		return
	}
	writeAudit(ctx, "tap.reverse", id.String(), map[string]any{"reason": body.Reason, "merchant_id": merchant.String()})
	u.APIResponse(ctx, http.StatusOK, "success", "Reversed", map[string]any{"tap_id": id.String(), "status": "reversed"})
}
