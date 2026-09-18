package v1

import (
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/usezoracle/tapp/api/internal/card/auth"
	"github.com/usezoracle/tapp/api/internal/card/tap"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// writeTapError maps a refusal onto a status and a stable code.
//
// The distinction that matters: a refusal is the system working and gets a 4xx
// with a code the merchant app can act on; a fault is the system failing and
// gets a 5xx with nothing but an apology. Reporting a declined card as a 500
// sends a merchant to support over a customer with no money in their account.
//
// Codes are stable strings, because the merchant app branches on them to
// decide whether to show a PIN pad, a QR, or "try another card".
func writeTapError(ctx *gin.Context, err error) {
	type mapping struct {
		target error
		status int
		code   string
		msg    string
	}

	for _, m := range []mapping{
		{tap.ErrCardUnknown, http.StatusNotFound, "card_unrecognized",
			"This card is not recognised."},
		{tap.ErrCardUnavailable, http.StatusConflict, "card_unavailable",
			"This card is locked. It will unlock automatically, or the cardholder can reset it in their app."},
		{tap.ErrNotLinked, http.StatusConflict, "card_not_linked",
			"This card has not been set up yet."},
		{tap.ErrNonceInvalid, http.StatusUnauthorized, "challenge_invalid",
			"This payment attempt expired. Start again."},
		{tap.ErrAmountChanged, http.StatusBadRequest, "amount_mismatch",
			"The amount does not match what was authorised."},
		{tap.ErrTokenStale, http.StatusForbidden, "card_resync_required",
			"This card needs to be re-synced by its holder."},
		{auth.ErrWrongPIN, http.StatusForbidden, "pin_invalid",
			"Incorrect PIN."},
		{auth.ErrNoPIN, http.StatusConflict, "pin_not_set",
			"This card has no PIN set. Its holder must finish setting it up."},
		{tap.ErrStepUpRequired, http.StatusAccepted, "step_up_pending",
			"Waiting for the cardholder to approve this payment."},
		{tap.ErrDailyLimitReached, http.StatusPaymentRequired, "daily_limit_exceeded",
			"This card has reached its daily limit."},
		{movements.ErrInsufficientFunds, http.StatusPaymentRequired, "insufficient_funds",
			"There is not enough on this card."},
		{tap.ErrIdentityLimitReached, http.StatusPaymentRequired, "identity_limit_exceeded",
			"This amount is above what the cardholder's verification allows."},
		{tap.ErrCannotPrice, http.StatusServiceUnavailable, "rate_unavailable",
			"Cannot price this payment right now. Try again in a moment."},
		{tap.ErrTapUnknown, http.StatusNotFound, "tap_not_found",
			"No such payment."},
		{tap.ErrRepeatTap, http.StatusConflict, "tap_repeated",
			"This card was already charged this amount here a moment ago. If this is a separate payment, lift the card off the phone, wait a few seconds, and tap again."},
	} {
		if errors.Is(err, m.target) {
			// The detail goes in the log, not the response: "₦8,000 available,
			// ₦12,000 needed" tells the merchant the cardholder's balance.
			logger.Errorf("tap refused (%s): %v", m.code, err)
			u.APIResponse(ctx, m.status, "error", m.msg, map[string]any{"code": m.code})
			return
		}
	}

	logger.Errorf("tap failed: %v", err)
	u.APIResponse(ctx, http.StatusInternalServerError, "error",
		"Something went wrong. The card has not been charged.", nil)
}

// parseCardAndAmount validates the two fields every card request carries.
func parseCardAndAmount(ctx *gin.Context, uidHashHex, amountText, currency string) ([]byte, money.Amount, bool) {
	uidHash, ok := decodeHex(ctx, uidHashHex, "card_uid_hash")
	if !ok {
		return nil, money.Amount{}, false
	}
	if len(uidHash) != 32 {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"card_uid_hash must be a 32-byte sha256 in hex", nil)
		return nil, money.Amount{}, false
	}

	if currency == "" {
		// NGN is the only currency a card is denominated in today. This is a
		// default for an absent field, not a fallback for an invalid one: an
		// unsupported currency is refused below rather than quietly becoming
		// naira.
		currency = string(money.NGN)
	}
	c := money.Currency(currency)
	if err := c.Valid(); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", err.Error(), nil)
		return nil, money.Amount{}, false
	}

	minor, err := tap.ParseAmount(amountText, c)
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", err.Error(), nil)
		return nil, money.Amount{}, false
	}
	amount := money.New(minor, c)
	if !amount.IsPositive() {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "The amount must be positive", nil)
		return nil, money.Amount{}, false
	}
	return uidHash, amount, true
}

func decodeHex(ctx *gin.Context, s, field string) ([]byte, bool) {
	b, err := hex.DecodeString(s)
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", field+" must be hex", nil)
		return nil, false
	}
	return b, true
}
