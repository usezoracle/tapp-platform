// Funding a balance by NGN bank transfer.
//
//	POST /v1/deposits/ngn/account   provision one, or return the existing one
//	GET  /v1/deposits/ngn/account   read it back
//
// The third funding route named in the README, and the naira answer to the
// question /v1/deposits/address answers for crypto: where do I send money.

package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/identity/kyc"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/baas"
	"github.com/usezoracle/tapp/api/storage"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// NGNDepositHandler serves a cardholder's own account number.
type NGNDepositHandler struct {
	Rail func() baas.Provider
	// KYC supplies the name and date of birth the rail needs, in the bank's
	// own spelling. It is also the gate: see Provision.
	KYC  *kyc.Store
	User func(*gin.Context) (uuid.UUID, bool)
}

type ngnAccountResponse struct {
	AccountNumber string `json:"account_number"`
	BankName      string `json:"bank_name"`
	AccountName   string `json:"account_name"`
	Currency      string `json:"currency"`
	// Warning mirrors the crypto side: the one mistake on this screen that
	// cannot be undone is sending from somewhere the credit cannot be
	// attributed back to this person.
	Warning string `json:"warning"`
}

// provisionNGNAccountRequest is what the rail needs to open a wallet.
//
// Fintava opens a full customer wallet rather than a pooled virtual account,
// and refuses without all of this. It is a heavier ask than a BVN alone, and
// it is the rail's requirement rather than ours -- there is no way to open the
// account with less.
//
// The name and date of birth are filled in from the verified BVN record when
// the caller leaves them blank, which is the normal case: the person already
// gave their BVN to /v1/kyc/bvn, the bank told us how their name is actually
// spelt, and asking them to type it again is both a worse form and a worse
// answer -- what a NUBAN displays to whoever pays into it should be the bank's
// spelling, not the phone keyboard's.
//
// None of it is stored here. The BVN and NIN in particular are passed straight
// through: at rest they are a liability with no use once the account exists,
// and the platform float provisioning already makes the same promise.
type provisionNGNAccountRequest struct {
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	DateOfBirth string `json:"dateOfBirth"` // YYYY-MM-DD
	Address     string `json:"address"`
	NIN         string `json:"nin"`
	BVN         string `json:"bvn"`
	Phone       string `json:"phone"` // optional; the BVN record's phone is used when absent
}

// fillFrom takes what the verified BVN record already established, for any
// field the caller left blank. What somebody typed wins when they typed it --
// a legal name can differ from a bank record for reasons that are the person's
// business, and the rail is the one that decides whether it will accept it.
func (r *provisionNGNAccountRequest) fillFrom(id *kyc.Identity) {
	if id == nil {
		return
	}
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&r.FirstName, id.FirstName},
		{&r.LastName, id.LastName},
		{&r.DateOfBirth, id.DateOfBirth},
	} {
		if strings.TrimSpace(*f.dst) == "" {
			*f.dst = strings.TrimSpace(f.src)
		}
	}
}

// missing names the fields the rail will refuse without, so the client can say
// which one to go and get rather than reporting a flat failure.
func (r provisionNGNAccountRequest) missing() []string {
	var out []string
	for _, f := range []struct {
		name string
		val  string
	}{
		{"firstName", r.FirstName}, {"lastName", r.LastName},
		{"dateOfBirth", r.DateOfBirth}, {"address", r.Address},
		{"nin", r.NIN}, {"bvn", r.BVN},
	} {
		if strings.TrimSpace(f.val) == "" {
			out = append(out, f.name)
		}
	}
	return out
}

const ngnDepositWarning = "Transfer naira to this account from any Nigerian bank. " +
	"It is yours and does not change."

// Get returns the caller's account, or 404 when they have not provisioned one.
func (h *NGNDepositHandler) Get(ctx *gin.Context) {
	user, ok := h.User(ctx)
	if !ok {
		return
	}

	acct, err := loadNGNAccount(ctx.Request.Context(), user)
	if errors.Is(err, pgx.ErrNoRows) {
		u.APIResponse(ctx, http.StatusNotFound, "error",
			"No naira account yet", map[string]any{"code": "not_provisioned"})
		return
	}
	if err != nil {
		logger.Errorf("ngn deposits: read account: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Could not read your naira account", nil)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Naira account", acct)
}

// Provision opens the account, or hands back the one that already exists.
//
// Idempotent on purpose: opening a bank account is a real side effect at the
// rail, and a client that retries a timed-out request must not end up with two
// account numbers, only one of which anybody is watching.
func (h *NGNDepositHandler) Provision(ctx *gin.Context) {
	user, ok := h.User(ctx)
	if !ok {
		return
	}

	// Already have one: return it and do not touch the rail.
	if acct, err := loadNGNAccount(ctx.Request.Context(), user); err == nil {
		u.APIResponse(ctx, http.StatusOK, "success", "Naira account", acct)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		logger.Errorf("ngn deposits: read account: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Could not read your naira account", nil)
		return
	}

	// A verified BVN first. The rail checks the BVN too, but only that it is
	// real -- it will open an account in whoever's name the number belongs to.
	// Tier 1 is where this platform established that it belongs to the person
	// asking, and an account opened without that is one whose inbound credits
	// are attributed to somebody who never proved they own it.
	profile, err := h.KYC.ProfileOf(ctx.Request.Context(), user)
	if err != nil {
		logger.Errorf("ngn deposits: read kyc profile: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Could not read your verification status", nil)
		return
	}
	if profile.Tier < kyc.TierBVN {
		u.APIResponse(ctx, http.StatusForbidden, "error",
			"Verify your BVN before opening a naira account",
			map[string]any{"code": "kyc_required", "required_tier": int(kyc.TierBVN)})
		return
	}

	var req provisionNGNAccountRequest
	_ = ctx.ShouldBindJSON(&req)
	req.fillFrom(profile.Identity)
	if missing := req.missing(); len(missing) > 0 {
		// Named distinctly rather than as a generic 400, and itemised: the
		// client has to be able to tell "we still need your NIN" from "that
		// request was malformed", because only one of them has something the
		// person can actually go and do.
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"More details are needed to open a naira account",
			map[string]any{"code": "kyc_required", "missing": missing})
		return
	}

	rail := h.Rail()
	if rail == nil {
		u.APIResponse(ctx, http.StatusServiceUnavailable, "error",
			"Naira accounts are temporarily unavailable.",
			map[string]any{"code": "rail_unavailable"})
		return
	}

	// The rail wants a reachable person: an email and a phone. The email is
	// the account's; the phone is the one the bank holds against the BVN,
	// which verification already fetched. Without either, ask rather than
	// let the rail refuse with a message nobody can act on.
	email := emailFor(ctx.Request.Context(), user)
	phone := strings.TrimSpace(profile.Identity.Phone)
	if p := strings.TrimSpace(req.Phone); p != "" {
		phone = p
	}
	var need []string
	if email == "" {
		need = append(need, "email")
	}
	if phone == "" {
		need = append(need, "phone")
	}
	if len(need) > 0 {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"More details are needed to open a naira account",
			map[string]any{"code": "kyc_required", "missing": need})
		return
	}
	created, err := rail.CreateSubAccount(ctx.Request.Context(), baas.CreateSubAccountRequest{
		ExternalReference: user.String(),
		IdentityType:      "BVN",
		IdentityNumber:    strings.TrimSpace(req.BVN),
		EmailAddress:      email,
		PhoneNumber:       phone,
		FirstName:         strings.TrimSpace(req.FirstName),
		LastName:          strings.TrimSpace(req.LastName),
		DateOfBirth:       strings.TrimSpace(req.DateOfBirth),
		Address:           strings.TrimSpace(req.Address),
		NIN:               strings.TrimSpace(req.NIN),
	})
	if err != nil {
		logger.Errorf("ngn deposits: create sub-account (rail=%s): %v", rail.Name(), err)
		u.APIResponse(ctx, http.StatusBadGateway, "error",
			"Could not open a naira account just now. Please try again.",
			map[string]any{"code": "rail_error"})
		return
	}

	acct, err := saveNGNAccount(ctx.Request.Context(), user, rail.Name(), created)
	if err != nil {
		// The account exists at the rail but we failed to record it. Say so
		// loudly: money sent to it now would arrive with nothing to attribute
		// it to.
		logger.Errorf("ngn deposits: ORPHANED account %s at rail %s for user %s: %v",
			created.AccountNumber, rail.Name(), user, err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Could not finish opening your naira account", nil)
		return
	}

	u.APIResponse(ctx, http.StatusCreated, "success", "Naira account", acct)
}

func loadNGNAccount(ctx context.Context, user uuid.UUID) (*ngnAccountResponse, error) {
	var r ngnAccountResponse
	err := storage.Pool.QueryRow(ctx, `
		SELECT account_number, bank_name, account_name
		  FROM ngn_deposit_accounts
		 WHERE user_id = $1`, user).Scan(&r.AccountNumber, &r.BankName, &r.AccountName)
	if err != nil {
		return nil, err
	}
	r.Currency = "NGN"
	r.Warning = ngnDepositWarning
	return &r, nil
}

func saveNGNAccount(
	ctx context.Context, user uuid.UUID, rail string, a *baas.Account,
) (*ngnAccountResponse, error) {
	_, err := storage.Pool.Exec(ctx, `
		INSERT INTO ngn_deposit_accounts
			(user_id, rail, account_number, bank_name, account_name, rail_ref)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) DO NOTHING`,
		user, rail, a.AccountNumber, a.BankName, a.AccountName, a.ID)
	if err != nil {
		return nil, err
	}
	return loadNGNAccount(ctx, user)
}

// emailFor is the account's email. Its predecessor also selected a
// phone_number column the users table does not have, discarded the error,
// and returned two empty strings — which the rail then refused, as a 502
// with nothing the person could do about it.
func emailFor(ctx context.Context, user uuid.UUID) string {
	var email string
	if err := storage.Pool.QueryRow(ctx,
		`SELECT coalesce(email,'') FROM users WHERE id = $1`, user).Scan(&email); err != nil {
		logger.Errorf("ngn deposits: email for %s: %v", user, err)
	}
	return strings.TrimSpace(email)
}

// -----------------------------------------------------------------------------
// The other half: money that actually arrives
// -----------------------------------------------------------------------------

// CreditNGNDeposit credits whoever owns the account number a rail says it paid
// into, and reports whether that account was one of ours.
//
// This is the half that makes the account number above mean anything. An
// account somebody can be given but whose credits nobody posts is worse than
// no account at all: the money is real, it left their bank, and this system
// has no record that it happened.
//
// Not-ours is (false, nil), not an error. The rail's webhook is shared with
// other flows -- LP deposit accounts, merchant payouts -- and every one of
// those legitimately arrives here addressed to an account this table has never
// heard of.
//
// Idempotency is the ledger's, keyed on source+reference. Rails redeliver:
// Fintava retries for 72 hours, so the second delivery of a credit must change
// nothing rather than double it.
func CreditNGNDeposit(
	ctx context.Context, accountNumber, amount, source, reference string,
) (bool, error) {
	var user uuid.UUID
	err := storage.Pool.QueryRow(ctx,
		`SELECT user_id FROM ngn_deposit_accounts WHERE account_number = $1`,
		accountNumber).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ngn deposits: owner of %s: %w", accountNumber, err)
	}

	value, err := nairaFromRail(amount)
	if err != nil {
		return true, err
	}

	if _, err := movements.Deposit(ctx, storage.Pool, user, value, source, reference); err != nil {
		if errors.Is(err, ledger.ErrDuplicate) {
			// A redelivery. The mechanism working, not a failure.
			return true, nil
		}
		return true, fmt.Errorf("ngn deposits: credit %s to %s: %w", value, user, err)
	}
	logger.Infof("💰 ngn deposit: %s → %s (account=%s ref=%s)", value, user, accountNumber, reference)
	return true, nil
}

// nairaFromRail converts the rail's decimal naira string to ledger minor units.
//
// A value with more precision than the currency has is refused rather than
// rounded. Rounding here would mean this system deciding, silently and in one
// direction, that somebody's money is a different amount than their bank said
// -- and a rail sending fractions of a kobo is a rail doing something nobody
// has understood yet, which is worth stopping for.
func nairaFromRail(amount string) (money.Amount, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil {
		return money.Amount{}, fmt.Errorf("ngn deposits: unreadable amount %q: %w", amount, err)
	}
	if !d.IsPositive() {
		return money.Amount{}, fmt.Errorf("ngn deposits: a credit must be positive, got %s", d)
	}
	minor := d.Mul(decimal.NewFromInt(money.NGN.Scale()))
	if !minor.Equal(minor.Truncate(0)) {
		return money.Amount{}, fmt.Errorf("ngn deposits: %s naira is not a whole number of kobo", d)
	}
	return money.New(minor.IntPart(), money.NGN), nil
}
