package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	apiv1 "github.com/usezoracle/tapp/api/internal/api/v1"
	"github.com/usezoracle/tapp/api/services/baas"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// NGNDepositsController is the operator's side of cardholders' naira deposit
// accounts: find one, fix what it says its bank is, and post the credits a
// lost webhook never delivered.
//
// The three actions are fields so a test can stand in for them without a
// database or a rail; NewNGNDepositsController wires the real ones.
type NGNDepositsController struct {
	Rail      func() baas.Provider
	ByEmail   func(ctx context.Context, email string) ([]*apiv1.NGNAccountRow, error)
	SetBank   func(ctx context.Context, accountNumber, bankName, bankCode string) (*apiv1.NGNAccountRow, error)
	Reconcile func(ctx context.Context, rail baas.Provider, accountNumber string) (*apiv1.NGNReconcileResult, error)
}

// NewNGNDepositsController wires the controller to the real store and rail.
func NewNGNDepositsController() *NGNDepositsController {
	return &NGNDepositsController{
		Rail:      baas.Default,
		ByEmail:   apiv1.NGNAccountsByEmail,
		SetBank:   apiv1.SetNGNDepositBank,
		Reconcile: apiv1.ReconcileNGNDeposit,
	}
}

// Find looks a person's account up by their email.
//
//	GET /v1/admin/deposits/ngn/accounts?email=
func (c *NGNDepositsController) Find(ctx *gin.Context) {
	email := strings.TrimSpace(ctx.Query("email"))
	if email == "" {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "email is required", nil)
		return
	}
	rows, err := c.ByEmail(ctx.Request.Context(), email)
	if err != nil {
		logger.Errorf("admin ngn deposits: find %s: %v", email, err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "could not read deposit accounts", nil)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Naira deposit accounts", gin.H{
		"email": email, "accounts": rows,
	})
}

type setBankRequest struct {
	BankName string `json:"bank_name" binding:"required"`
	BankCode string `json:"bank_code"`
}

// SetBank corrects what a row says its bank is. Audited.
//
//	POST /v1/admin/deposits/ngn/accounts/:account_number/bank
//	{"bank_name": "Loma Microfinance Bank", "bank_code": "090620"}
func (c *NGNDepositsController) SetBankName(ctx *gin.Context) {
	account := strings.TrimSpace(ctx.Param("account_number"))
	var body setBankRequest
	if err := ctx.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.BankName) == "" {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "bank_name is required", nil)
		return
	}
	row, err := c.SetBank(ctx.Request.Context(), account, body.BankName, body.BankCode)
	if errors.Is(err, apiv1.ErrNGNAccountNotFound) {
		u.APIResponse(ctx, http.StatusNotFound, "error", "no deposit account with that number", nil)
		return
	}
	if err != nil {
		logger.Errorf("admin ngn deposits: set bank on %s: %v", account, err)
		u.APIResponse(ctx, http.StatusBadRequest, "error", err.Error(), nil)
		return
	}
	writeAudit(ctx, "ngn_deposit.bank.set", account, map[string]any{
		"bank_name": row.BankName, "bank_code": row.BankCode, "user_id": row.UserID.String(),
	})
	u.APIResponse(ctx, http.StatusOK, "success", "Bank updated", row)
}

// ReconcileAccount posts whatever the rail's wallet holds that the ledger has
// not credited. MONEY MOVEMENT (a credit to a cardholder), audited with the
// figures it was computed from.
//
//	POST /v1/admin/deposits/ngn/accounts/:account_number/reconcile
func (c *NGNDepositsController) ReconcileAccount(ctx *gin.Context) {
	account := strings.TrimSpace(ctx.Param("account_number"))
	rail := c.Rail()
	if rail == nil {
		u.APIResponse(ctx, http.StatusServiceUnavailable, "error", "baas rail not configured", nil)
		return
	}
	res, err := c.Reconcile(ctx.Request.Context(), rail, account)
	if errors.Is(err, apiv1.ErrNGNAccountNotFound) {
		u.APIResponse(ctx, http.StatusNotFound, "error", "no deposit account with that number", nil)
		return
	}
	if err != nil {
		logger.Errorf("admin ngn deposits: reconcile %s: %v", account, err)
		u.APIResponse(ctx, http.StatusBadGateway, "error", err.Error(), nil)
		return
	}
	writeAudit(ctx, "ngn_deposit.reconcile", account, map[string]any{
		"rail":                  rail.Name(),
		"wallet_id":             res.WalletID,
		"wallet_balance_minor":  res.WalletBalance.Minor(),
		"credited_before_minor": res.CreditedBefore.Minor(),
		"posted_minor":          res.Posted.Minor(),
		"reference":             res.Reference,
		"note":                  res.Note,
	})
	u.APIResponse(ctx, http.StatusOK, "success", "Reconciled", res)
}
