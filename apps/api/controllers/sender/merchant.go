// merchant.go holds the controllers used by the Tapp Merchant mobile
// app — a small surface on top of the existing sender APIs:
//
//   POST /v1/sender/me/bank-account       SaveMerchantBankAccount
//   GET  /v1/sender/me/bank-account       GetMerchantBankAccount
//   GET  /v1/sender/me/payments/stream    StreamPayments (SSE)
//
// Auth is the same DynamicAuthMiddleware + OnlySenderMiddleware stack
// the rest of /v1/sender uses; the merchant identity is just a
// SenderProfile with an attached MerchantBankAccount.

package sender

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/ent"
	"github.com/usezoracle/tapp/api/ent/institution"
	"github.com/usezoracle/tapp/api/ent/merchantbankaccount"
	"github.com/usezoracle/tapp/api/ent/senderprofile"
	apiv1 "github.com/usezoracle/tapp/api/internal/api/v1"
	svc "github.com/usezoracle/tapp/api/services"
	"github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/types"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// -----------------------------------------------------------------------------
// Bank account: save + fetch the merchant's NGN payout account.
// -----------------------------------------------------------------------------

type saveBankAccountPayload struct {
	Currency      string `json:"currency"       binding:"required"` // e.g. "NGN"
	BankCode      string `json:"bank_code"      binding:"required"` // CBN code
	AccountNumber string `json:"account_number" binding:"required"` // 10-digit NUBAN for NGN
	AccountName   string `json:"account_name"   binding:"required"` // resolved name
}

type bankAccountResponse struct {
	ID            uuid.UUID `json:"id"`
	Currency      string    `json:"currency"`
	BankCode      string    `json:"bank_code"`
	AccountNumber string    `json:"account_number"`
	AccountName   string    `json:"account_name"`
	// FintavaBankCode is the naira rail's own code for bank_code, resolved
	// when the account was saved. Absent when the rail does not list the
	// bank; a payout to it will fail until it does.
	FintavaBankCode string     `json:"fintava_bank_code,omitempty"`
	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
}

// SaveMerchantBankAccount upserts the merchant's payout account.
// The client is expected to have already called POST /v1/verify-account
// to resolve account_name before saving — this endpoint trusts the
// supplied name but re-validates institution code + format.
func (ctrl *SenderController) SaveMerchantBankAccount(ctx *gin.Context) {
	var payload saveBankAccountPayload
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", u.GetErrorData(err))
		return
	}

	sender, ok := senderFromCtx(ctx)
	if !ok {
		return
	}

	payload.Currency = strings.ToUpper(strings.TrimSpace(payload.Currency))
	if payload.Currency != "NGN" {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Only NGN is supported in v1", types.ErrorData{
				Field: "Currency", Message: "Must be NGN",
			})
		return
	}

	// Validate institution exists and belongs to the declared currency.
	inst, err := storage.Client.Institution.
		Query().
		Where(institution.CodeEQ(payload.BankCode)).
		WithFiatCurrency().
		Only(ctx)
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", types.ErrorData{
				Field: "BankCode", Message: "Unknown bank code",
			})
		return
	}
	if inst.Edges.FiatCurrency == nil || inst.Edges.FiatCurrency.Code != payload.Currency {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", types.ErrorData{
				Field: "BankCode", Message: "Bank does not match the declared currency",
			})
		return
	}

	// NUBAN check for NGN — 10 digits exactly.
	if payload.Currency == "NGN" && !ngnAccountNumberRegex.MatchString(payload.AccountNumber) {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", types.ErrorData{
				Field: "AccountNumber", Message: "NGN accounts must be 10 digits",
			})
		return
	}

	// The code the naira rail knows this bank by, stored alongside the
	// catalogue's. Not resolving is not a reason to refuse the save -- the
	// settlement worker resolves again before every payout, and the rail's
	// list changes -- but it is logged, because a payout to this account
	// will fail until the rail lists the bank.
	fintavaCode, _, err := apiv1.SharedBankCodeResolver().FintavaCode(ctx, payload.BankCode)
	if err != nil {
		logger.Warnf("SaveMerchantBankAccount: no rail code for %s (%s): %v", inst.Name, payload.BankCode, err)
		fintavaCode = ""
	}

	now := time.Now()
	existing, err := storage.Client.MerchantBankAccount.
		Query().
		Where(merchantbankaccount.HasSenderProfileWith(senderprofile.IDEQ(sender.ID))).
		Only(ctx)

	var saved *ent.MerchantBankAccount
	switch {
	case err == nil:
		upd := existing.Update().
			SetCurrency(payload.Currency).
			SetBankCode(payload.BankCode).
			SetAccountNumber(payload.AccountNumber).
			SetAccountName(payload.AccountName).
			SetVerifiedAt(now)
		// A bank that changed to one the rail cannot pay must not keep the
		// old bank's rail code.
		if fintavaCode != "" {
			upd.SetFintavaBankCode(fintavaCode)
		} else {
			upd.ClearFintavaBankCode()
		}
		saved, err = upd.Save(ctx)
	case ent.IsNotFound(err):
		saved, err = storage.Client.MerchantBankAccount.Create().
			SetCurrency(payload.Currency).
			SetBankCode(payload.BankCode).
			SetAccountNumber(payload.AccountNumber).
			SetAccountName(payload.AccountName).
			SetNillableFintavaBankCode(nilIfEmpty(fintavaCode)).
			SetVerifiedAt(now).
			SetSenderProfile(sender).
			Save(ctx)
	default:
		logger.Errorf("SaveMerchantBankAccount: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to save bank account", nil)
		return
	}
	if err != nil {
		logger.Errorf("SaveMerchantBankAccount: persist: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to save bank account", nil)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Bank account saved",
		bankAccountResponseFromEnt(saved))
}

// GetMerchantBankAccount returns the merchant's saved payout account
// (or 404 if not yet set).
func (ctrl *SenderController) GetMerchantBankAccount(ctx *gin.Context) {
	sender, ok := senderFromCtx(ctx)
	if !ok {
		return
	}

	row, err := storage.Client.MerchantBankAccount.
		Query().
		Where(merchantbankaccount.HasSenderProfileWith(senderprofile.IDEQ(sender.ID))).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			u.APIResponse(ctx, http.StatusNotFound, "error", "Bank account not set", nil)
			return
		}
		logger.Errorf("GetMerchantBankAccount: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to fetch bank account", nil)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Bank account retrieved",
		bankAccountResponseFromEnt(row))
}

// Phone-to-phone checkout and the tap-card stub moved out of this file.
//
// The checkout used to create a one-time Sui receive address, wait for an
// on-chain deposit to it, bridge that to Base, and settle through an
// aggregator. Payer balances live in the ledger now and deposits land on Base
// directly, so a payer with a balance simply pays from it -- the same movement
// a card tap makes, and in one transaction rather than a pipeline. See
// internal/checkout.

// StreamPayments holds the HTTP connection open as a text/event-stream
// and forwards every PaymentOrder lifecycle event scoped to the authed
// SenderProfile. Bidirectional close: client disconnect ends the loop;
// server context cancel ends it too.
func (ctrl *SenderController) StreamPayments(ctx *gin.Context) {
	sender, ok := senderFromCtx(ctx)
	if !ok {
		return
	}

	// SSE headers — Set before WriteHeader / before first flush.
	ctx.Writer.Header().Set("Content-Type", "text/event-stream")
	ctx.Writer.Header().Set("Cache-Control", "no-cache")
	ctx.Writer.Header().Set("Connection", "keep-alive")
	ctx.Writer.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	ctx.Writer.WriteHeader(http.StatusOK)

	flusher, isFlusher := ctx.Writer.(http.Flusher)
	if !isFlusher {
		// Should never happen with gin's default writer, but bail gracefully.
		_, _ = io.WriteString(ctx.Writer, "event: error\ndata: streaming not supported\n\n")
		return
	}

	lastEventID := ctx.GetHeader("Last-Event-ID")
	events, replay, unsubscribe := svc.Bus().Subscribe(sender.ID, lastEventID)
	defer unsubscribe()

	// Immediately write a comment line so intermediate proxies see headers
	// + first byte and don't time out the connection on the handshake.
	_, _ = io.WriteString(ctx.Writer, ": connected\n\n")
	flusher.Flush()

	for _, ev := range replay {
		writeSSE(ctx.Writer, ev)
		flusher.Flush()
	}

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	clientGone := ctx.Request.Context().Done()
	for {
		select {
		case <-clientGone:
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(ctx.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-events:
			if !open {
				return
			}
			writeSSE(ctx.Writer, ev)
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, ev svc.PaymentEvent) {
	data, err := json.Marshal(ev.Payload)
	if err != nil {
		logger.Errorf("SSE marshal: %v", err)
		return
	}
	_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", ev.ID, ev.Name, data)
}

// -----------------------------------------------------------------------------
// Shared helpers (file-scoped, so they don't pollute the package API).
// -----------------------------------------------------------------------------

var ngnAccountNumberRegex = regexp.MustCompile(`^[0-9]{10}$`)

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func bankAccountResponseFromEnt(row *ent.MerchantBankAccount) bankAccountResponse {
	resp := bankAccountResponse{
		ID:            row.ID,
		Currency:      row.Currency,
		BankCode:      row.BankCode,
		AccountNumber: row.AccountNumber,
		AccountName:   row.AccountName,
	}
	if row.FintavaBankCode != nil {
		resp.FintavaBankCode = *row.FintavaBankCode
	}
	if row.VerifiedAt != nil {
		t := *row.VerifiedAt
		resp.VerifiedAt = &t
	}
	return resp
}

func senderFromCtx(ctx *gin.Context) (*ent.SenderProfile, bool) {
	senderCtx, ok := ctx.Get("sender")
	if !ok || senderCtx == nil {
		u.APIResponse(ctx, http.StatusUnauthorized, "error",
			"Invalid API key or token", nil)
		return nil, false
	}
	sender, ok := senderCtx.(*ent.SenderProfile)
	if !ok || sender == nil {
		u.APIResponse(ctx, http.StatusUnauthorized, "error",
			"Sender not authenticated", nil)
		return nil, false
	}
	return sender, true
}
