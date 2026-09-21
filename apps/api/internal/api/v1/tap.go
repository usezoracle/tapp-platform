package v1

import (
	"encoding/hex"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/internal/card/tap"
	"github.com/usezoracle/tapp/api/internal/money"
	u "github.com/usezoracle/tapp/api/utils"
)

// TapHandler serves the merchant-facing card endpoints.
//
// The handlers parse, call the service, and map the outcome. All the money
// logic lives in internal/card/tap; nothing here decides anything. The
// controller this replaces was 920 lines that did the nonce, the token, the
// PIN, the limits, the order, a chain call and the response inline, with no
// transaction anywhere.
type TapHandler struct {
	Svc *tap.Service
	// Merchant resolves the authenticated caller to a merchant id.
	Merchant func(*gin.Context) (uuid.UUID, bool)
}

type challengeRequest struct {
	CardUIDHash string `json:"card_uid_hash" form:"card_uid_hash" binding:"required"`
	Amount      string `json:"amount"        form:"amount"        binding:"required"`
	Currency    string `json:"currency"      form:"currency"`
}

type challengeResponse struct {
	Tier      string `json:"tier"`
	Nonce     string `json:"server_nonce"`
	StepUpRef string `json:"step_up_ref,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

// Challenge resolves what an amount requires and issues a single-use nonce.
func (h *TapHandler) Challenge(ctx *gin.Context) {
	var req challengeRequest
	if err := ctx.ShouldBindQuery(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", u.GetErrorData(err))
		return
	}
	merchant, ok := h.Merchant(ctx)
	if !ok {
		return
	}

	uidHash, amount, ok := parseCardAndAmount(ctx, req.CardUIDHash, req.Amount, req.Currency)
	if !ok {
		return
	}

	ch, err := h.Svc.Challenge(ctx.Request.Context(), tap.ChallengeRequest{
		CardUIDHash: uidHash, MerchantID: merchant, Amount: amount,
	})
	if err != nil {
		writeTapError(ctx, err)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Challenge issued", challengeResponse{
		Tier:      string(ch.Tier),
		Nonce:     hex.EncodeToString(ch.Nonce),
		StepUpRef: ch.StepUpRef,
		ExpiresAt: ch.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

type debitRequest struct {
	CardUIDHash string `json:"card_uid_hash" binding:"required"`
	Token       string `json:"card_token"    binding:"required"`
	Nonce       string `json:"server_nonce"  binding:"required"`
	Amount      string `json:"amount"        binding:"required"`
	Currency    string `json:"currency"`
	PINResponse string `json:"pin_response"`
	StepUpRef   string `json:"step_up_ref"`
}

type debitResponse struct {
	TapID  string `json:"tap_id"`
	Status string `json:"status"`
	// Money goes out as money.Amount ({minor, currency, display}) like every
	// other endpoint. It used to go out as Amount.String() -- "₦5,000.00" --
	// in a field the merchant app parsed as a decimal, so the app showed ₦0
	// on every receipt.
	Amount money.Amount `json:"amount"`
	Fee    money.Amount `json:"fee"`
	Tier   string       `json:"tier"`
	// NewCardToken must be written to the card, then acknowledged. Until it is,
	// the card's previous token also still works.
	NewCardToken   string       `json:"new_card_token"`
	RemainingDaily money.Amount `json:"remaining_daily"`
}

// Debit performs the payment.
func (h *TapHandler) Debit(ctx *gin.Context) {
	var req debitRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", u.GetErrorData(err))
		return
	}
	merchant, ok := h.Merchant(ctx)
	if !ok {
		return
	}

	uidHash, amount, ok := parseCardAndAmount(ctx, req.CardUIDHash, req.Amount, req.Currency)
	if !ok {
		return
	}
	cardToken, ok := decodeHex(ctx, req.Token, "card_token")
	if !ok {
		return
	}
	nonce, ok := decodeHex(ctx, req.Nonce, "server_nonce")
	if !ok {
		return
	}
	var pin []byte
	if req.PINResponse != "" {
		if pin, ok = decodeHex(ctx, req.PINResponse, "pin_response"); !ok {
			return
		}
	}

	receipt, err := h.Svc.Debit(ctx.Request.Context(), tap.Request{
		CardUIDHash: uidHash, PresentedToken: cardToken, Nonce: nonce,
		MerchantID: merchant, Amount: amount,
		PINResponse: pin, StepUpRef: req.StepUpRef,
	})
	if err != nil {
		writeTapError(ctx, err)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Card debited", debitResponse{
		TapID: receipt.TapID.String(),
		// "charged", not "settled". The cardholder has been debited and the
		// merchant is owed; the money has not reached their bank yet, and
		// saying otherwise is the claim the predecessor made about payments
		// that never moved at all.
		Status:         "charged",
		Amount:         receipt.Amount,
		Fee:            receipt.Fee,
		Tier:           string(receipt.Tier),
		NewCardToken:   hex.EncodeToString(receipt.NewToken),
		RemainingDaily: receipt.RemainingDaily,
	})
}

type ackRequest struct {
	Written bool `json:"written"`
}

// Acknowledge completes the token rotation.
func (h *TapHandler) Acknowledge(ctx *gin.Context) {
	tapID, err := uuid.Parse(ctx.Param("tap_id"))
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid tap id", nil)
		return
	}
	var req ackRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", u.GetErrorData(err))
		return
	}
	merchant, ok := h.Merchant(ctx)
	if !ok {
		return
	}

	if err := h.Svc.Acknowledge(ctx.Request.Context(), merchant, tapID, req.Written); err != nil {
		writeTapError(ctx, err)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Acknowledged", nil)
}

type reverseRequest struct {
	Reason string `json:"reason" binding:"required"`
}

// Reverse refunds a tap in full.
func (h *TapHandler) Reverse(ctx *gin.Context) {
	tapID, err := uuid.Parse(ctx.Param("tap_id"))
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid tap id", nil)
		return
	}
	var req reverseRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", u.GetErrorData(err))
		return
	}
	merchant, ok := h.Merchant(ctx)
	if !ok {
		return
	}

	if err := h.Svc.Reverse(ctx.Request.Context(), merchant, tapID, req.Reason); err != nil {
		writeTapError(ctx, err)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Reversed", nil)
}
