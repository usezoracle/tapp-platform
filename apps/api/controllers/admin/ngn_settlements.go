package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/internal/settlement/naira"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// NGNSettlementsController is the operator's side of naira legs: what taps
// took from naira balances, paid to merchants out of cardholders' own
// wallets. See internal/settlement/naira.
//
// The actions are fields so a test can stand in for them without a database
// or a rail; NewNGNSettlementsController wires the real ones.
type NGNSettlementsController struct {
	List  func(ctx context.Context, state string, limit int) ([]*naira.Settlement, error)
	Retry func(ctx context.Context, tapID uuid.UUID) (*naira.Settlement, error)
}

// NewNGNSettlementsController wires the controller to the real worker.
func NewNGNSettlementsController(w *naira.Worker) *NGNSettlementsController {
	return &NGNSettlementsController{
		List: func(ctx context.Context, state string, limit int) ([]*naira.Settlement, error) {
			return naira.List(ctx, w.Pool, state, limit)
		},
		Retry: w.Retry,
	}
}

type ngnSettlementView struct {
	TapID        string `json:"tap_id"`
	CardholderID string `json:"cardholder_id"`
	MerchantID   string `json:"merchant_id"`
	// The cardholder's wallet the leg is paid from.
	SourceCustomerID    string `json:"source_customer_id"`
	SourceAccountNumber string `json:"source_account_number"`
	Currency            string `json:"currency"`
	Amount              string `json:"amount"`

	Bank bankView `json:"bank"`
	// FintavaBankCode is the sort code the rail was actually given for the
	// bank, resolved from bank.institution on the last attempt. Empty
	// until an attempt resolved one.
	FintavaBankCode string `json:"fintava_bank_code,omitempty"`

	Reference string `json:"reference"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	RailRef   string `json:"rail_ref,omitempty"`
	Error     string `json:"error,omitempty"`

	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	SubmittedAt string `json:"submitted_at,omitempty"`
	SettledAt   string `json:"settled_at,omitempty"`
}

func ngnSettlementOf(s *naira.Settlement) ngnSettlementView {
	v := ngnSettlementView{
		TapID: s.TapID.String(), CardholderID: s.CardholderID.String(), MerchantID: s.MerchantID.String(),
		SourceCustomerID: s.SourceCustomerID, SourceAccountNumber: s.SourceAccountNumber,
		Currency: string(s.Amount.Currency()), Amount: plain(s.Amount),
		Bank:            bankView{Institution: s.BankCode, AccountNumber: s.AccountNumber, AccountName: s.AccountName},
		FintavaBankCode: s.FintavaBankCode,
		Reference:       s.Reference, State: s.State, Attempts: s.Attempts,
		RailRef: s.RailRef, Error: s.Error,
		CreatedAt: s.CreatedAt.UTC().Format(tsLayout),
		UpdatedAt: s.UpdatedAt.UTC().Format(tsLayout),
	}
	if s.SubmittedAt != nil {
		v.SubmittedAt = s.SubmittedAt.UTC().Format(tsLayout)
	}
	if s.SettledAt != nil {
		v.SettledAt = s.SettledAt.UTC().Format(tsLayout)
	}
	return v
}

// GetSettlements lists naira settlements, newest first.
//
//	GET /v1/admin/settlements/ngn?state=queued|submitted|settled|failed&limit=
func (c *NGNSettlementsController) GetSettlements(ctx *gin.Context) {
	state := strings.TrimSpace(ctx.Query("state"))
	switch state {
	case "", naira.Queued, naira.Submitted, naira.Settled, naira.Failed:
	default:
		u.APIResponse(ctx, http.StatusBadRequest, "error", "state must be one of queued, submitted, settled, failed", nil)
		return
	}
	_, _, limit := u.Paginate(ctx)
	rows, err := c.List(ctx.Request.Context(), state, limit)
	if err != nil {
		logger.Errorf("admin ngn settlements: list: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "could not read settlements", nil)
		return
	}
	out := make([]ngnSettlementView, 0, len(rows))
	for _, s := range rows {
		out = append(out, ngnSettlementOf(s))
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Naira settlements", gin.H{
		"state": state, "settlements": out,
	})
}

// RetrySettlement queues a failed settlement to be paid again. MONEY
// MOVEMENT: the next tick asks the rail for the transfer under the same
// reference. Audited.
//
//	POST /v1/admin/settlements/ngn/:tap_id/retry
func (c *NGNSettlementsController) RetrySettlement(ctx *gin.Context) {
	tapID, err := uuid.Parse(strings.TrimSpace(ctx.Param("tap_id")))
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "tap_id must be a uuid", nil)
		return
	}
	s, err := c.Retry(ctx.Request.Context(), tapID)
	switch {
	case errors.Is(err, naira.ErrNotFound):
		u.APIResponse(ctx, http.StatusNotFound, "error", "no naira settlement for that tap", nil)
		return
	case errors.Is(err, naira.ErrNotFailed):
		u.APIResponse(ctx, http.StatusConflict, "error", err.Error(), nil)
		return
	case errors.As(err, new(*naira.UnmappedBankError)):
		// The rail still has no code for the merchant's bank; queueing it
		// would only fail it again. The row keeps the reason.
		u.APIResponse(ctx, http.StatusUnprocessableEntity, "error", err.Error(), nil)
		return
	case err != nil:
		logger.Errorf("admin ngn settlements: retry %s: %v", tapID, err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "could not retry the settlement", nil)
		return
	}
	writeAudit(ctx, "ngn_settlement.retry", tapID.String(), map[string]any{
		"merchant_id":  s.MerchantID.String(),
		"amount_minor": s.Amount.Minor(),
		"attempts":     s.Attempts,
		"reference":    s.Reference,
	})
	u.APIResponse(ctx, http.StatusOK, "success", "Settlement queued", ngnSettlementOf(s))
}
