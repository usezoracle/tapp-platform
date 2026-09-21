package v1

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/internal/card/link"
	"github.com/usezoracle/tapp/api/internal/money"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// LinkHandler serves card linking as one resumable session.
//
// Four endpoints on one resource, rather than four unrelated endpoints with no
// state between them. The client can always ask where it got to, which is what
// makes a dropped connection recoverable -- and the ceremony it would
// otherwise repeat generates a secret, writes it to a chip over NFC, and
// commits a PIN proof.
type LinkHandler struct {
	Svc  *link.Service
	User func(*gin.Context) (uuid.UUID, bool)
}

type startLinkRequest struct {
	// ActivationToken is the opaque value printed on, or written to, the card.
	ActivationToken string `json:"activation_token" binding:"required"`
}

// Start claims a card and opens a session.
func (h *LinkHandler) Start(ctx *gin.Context) {
	var req startLinkRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", u.GetErrorData(err))
		return
	}
	user, ok := h.User(ctx)
	if !ok {
		return
	}

	session, err := h.Svc.Start(ctx.Request.Context(), req.ActivationToken, user)
	if err != nil {
		writeLinkError(ctx, err)
		return
	}
	u.APIResponse(ctx, http.StatusCreated, "success", "Ready to set up your card", session)
}

// Get reports where a session has got to.
func (h *LinkHandler) Get(ctx *gin.Context) {
	sessionID, user, ok := h.session(ctx)
	if !ok {
		return
	}
	session, err := h.Svc.Get(ctx.Request.Context(), sessionID, user)
	if err != nil {
		writeLinkError(ctx, err)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Linking session", session)
}

type provisionRequest struct {
	// Anchor is HMAC(HMAC(K, PIN), "linking-anchor-v1"), computed on the
	// client. Neither K nor the PIN can be recovered from it, which is why the
	// client sends this and not its inputs.
	Anchor string `json:"pin_anchor" binding:"required"`

	PerTapLimit string `json:"per_tap_limit" binding:"required"`
	StepUpLimit string `json:"step_up_limit" binding:"required"`
	DailyLimit  string `json:"daily_limit"   binding:"required"`
}

// Provision commits the client's proofs and returns the token to write.
func (h *LinkHandler) Provision(ctx *gin.Context) {
	sessionID, user, ok := h.session(ctx)
	if !ok {
		return
	}
	var req provisionRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", u.GetErrorData(err))
		return
	}

	anchor, ok := decodeHex(ctx, req.Anchor, "pin_anchor")
	if !ok {
		return
	}

	limits, ok := h.limits(ctx, req)
	if !ok {
		return
	}

	session, err := h.Svc.Provision(ctx.Request.Context(), sessionID, user, link.Provisioning{
		Anchor: anchor, Limits: limits,
	})
	if err != nil {
		writeLinkError(ctx, err)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Write this to the card, then confirm", session)
}

type activateRequest struct {
	// CardUIDHash is sha256 of the chip's factory UID, read back off the card.
	// The raw UID is never sent: it identifies the physical card and there is
	// no reason for the server to hold it.
	CardUIDHash string `json:"card_uid_hash" binding:"required"`
	// ReadBack is what the client read off the chip after writing. Trusting
	// the write alone would leave a fraction of cards permanently unusable: an
	// NFC write reporting success without landing is common.
	ReadBack string `json:"read_back" binding:"required"`
}

// Activate completes the ceremony.
func (h *LinkHandler) Activate(ctx *gin.Context) {
	sessionID, user, ok := h.session(ctx)
	if !ok {
		return
	}
	var req activateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", u.GetErrorData(err))
		return
	}
	uidHash, ok := decodeHex(ctx, req.CardUIDHash, "card_uid_hash")
	if !ok {
		return
	}
	readBack, ok := decodeHex(ctx, req.ReadBack, "read_back")
	if !ok {
		return
	}

	session, err := h.Svc.Activate(ctx.Request.Context(), sessionID, user, link.Activation{
		UIDHash: uidHash, ReadBack: readBack,
	})
	if err != nil {
		writeLinkError(ctx, err)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Your card is ready to use", session)
}

func (h *LinkHandler) session(ctx *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	sessionID, err := uuid.Parse(ctx.Param("id"))
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid session id", nil)
		return uuid.Nil, uuid.Nil, false
	}
	user, ok := h.User(ctx)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return sessionID, user, true
}

func (h *LinkHandler) limits(ctx *gin.Context, req provisionRequest) (link.Limits, bool) {
	var out link.Limits
	for _, field := range []struct {
		name string
		text string
		into *int64
	}{
		{"per_tap_limit", req.PerTapLimit, &out.PerTapMinor},
		{"step_up_limit", req.StepUpLimit, &out.StepUpMinor},
		{"daily_limit", req.DailyLimit, &out.DailyMinor},
	} {
		minor, err := parseDecimalAmount(field.text, money.NGN)
		if err != nil {
			u.APIResponse(ctx, http.StatusBadRequest, "error",
				field.name+": "+err.Error(), nil)
			return out, false
		}
		*field.into = minor
	}
	return out, true
}

func writeLinkError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, link.ErrCardUnknown):
		u.APIResponse(ctx, http.StatusNotFound, "error",
			"We do not recognise that card.", map[string]any{"code": "card_unknown"})
	case errors.Is(err, link.ErrCardTaken):
		// Distinct from unknown on purpose: somebody holding a card that is
		// not theirs should be told, not left retrying.
		u.APIResponse(ctx, http.StatusConflict, "error",
			"That card belongs to another account.", map[string]any{"code": "card_taken"})
	case errors.Is(err, link.ErrAlreadyLive):
		u.APIResponse(ctx, http.StatusConflict, "error",
			"That card is already set up.", map[string]any{"code": "card_already_active"})
	case errors.Is(err, link.ErrUIDTaken):
		u.APIResponse(ctx, http.StatusConflict, "error",
			"That card is already registered.", map[string]any{"code": "card_registered"})
	case errors.Is(err, link.ErrSessionUnknown):
		u.APIResponse(ctx, http.StatusNotFound, "error",
			"That setup session was not found.", map[string]any{"code": "session_unknown"})
	case errors.Is(err, link.ErrWrongState):
		u.APIResponse(ctx, http.StatusConflict, "error", err.Error(),
			map[string]any{"code": "wrong_step"})
	case errors.Is(err, link.ErrLimitsInvalid):
		// The person chose these and can change them, so say which one is
		// wrong and let them. This used to reach the default branch below and
		// come back as a 500 "Something went wrong setting up your card",
		// which reads as "the system broke" -- so the one person who could fix
		// it was the one person not told what was wrong.
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			strings.TrimPrefix(err.Error(), "link: "),
			map[string]any{"code": "limits_invalid"})
	default:
		logger.Errorf("link: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Something went wrong setting up your card.", nil)
	}
}
