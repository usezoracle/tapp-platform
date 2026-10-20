// Account verification and gas sponsorship.
//
//	POST /v1/verify-account
//	POST /v1/gas-station/sponsor

package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	u "github.com/usezoracle/tapp/api/utils"

	fastshot "github.com/opus-domini/fast-shot"
	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/ent/fiatcurrency"
	"github.com/usezoracle/tapp/api/ent/institution"
	"github.com/usezoracle/tapp/api/ent/providerprofile"
	"github.com/usezoracle/tapp/api/services/onswitch"
	"github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/types"
	"github.com/usezoracle/tapp/api/utils/logger"
)

type sponsorTransactionRequest struct {
	TxBytes string `json:"txBytes" binding:"required"`
	Sender  string `json:"sender" binding:"required"`
}

type sponsorTransactionResponse struct {
	SponsoredTxBytes string `json:"sponsoredTxBytes"`
	SponsorSignature string `json:"sponsorSignature"`
}

// SponsorTransaction handles POST /v1/gas-station/sponsor.
func (ctrl *Controller) SponsorTransaction(ctx *gin.Context) {
	var req sponsorTransactionRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", u.GetErrorData(err))
		return
	}

	sponsoredTxBytes, sponsorSignature, err := ctrl.orderService.SponsorTransaction(ctx.Request.Context(), req.TxBytes, req.Sender)
	if err != nil {
		logger.Errorf("SponsorTransaction failed: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			fmt.Sprintf("Gas station failed: %v", err), nil)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Transaction sponsored", sponsorTransactionResponse{
		SponsoredTxBytes: sponsoredTxBytes,
		SponsorSignature: sponsorSignature,
	})
}

// VerifyAccount controller verifies an account of a given institution
func (ctrl *Controller) VerifyAccount(ctx *gin.Context) {
	var payload types.VerifyAccountRequest

	if err := ctx.ShouldBindJSON(&payload); err != nil {
		logger.Errorf("error: %v", err)
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", u.GetErrorData(err))
		return
	}

	if payload.AccountIdentifier == "" && payload.AccountIdentifierSnake != "" {
		payload.AccountIdentifier = payload.AccountIdentifierSnake
	}

	if payload.AccountIdentifier == "" {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Failed to validate payload", []types.ErrorData{{
			Field:   "AccountIdentifier",
			Message: "AccountIdentifier (accountIdentifier or account_identifier) is required",
		}})
		return
	}

	// Ask the bank, through Switch, whose account this is. Switch is what
	// pays this account later, so its answer is the one that counts: a name
	// is the verified name, and a refusal is final rather than a reason to
	// ask somewhere else.
	if switchConf := config.SwitchConfig(); switchConf.Enabled() {
		bankCode, err := onswitch.BankCode(payload.Institution)
		if err != nil {
			u.APIResponse(ctx, http.StatusBadRequest, "error", "Failed to validate payload", []types.ErrorData{{
				Field:   "Institution",
				Message: "Institution is not supported",
			}})
			return
		}
		name, err := onswitch.New(switchConf.BaseURL, switchConf.ServiceKey).
			AccountName(ctx.Request.Context(), onswitch.Country, bankCode, payload.AccountIdentifier)
		var refused *onswitch.Error
		switch {
		case err == nil && name != "":
			u.APIResponse(ctx, http.StatusOK, "success", "Account name was fetched successfully", name)
		case err == nil, errors.As(err, &refused) && refused.Status < 500:
			logger.Warnf("switch account lookup refused %s at %s: %v", payload.AccountIdentifier, payload.Institution, err)
			u.APIResponse(ctx, http.StatusBadRequest, "error", "Failed to verify account", nil)
		default:
			logger.Errorf("switch account lookup: %v", err)
			u.APIResponse(ctx, http.StatusServiceUnavailable, "error", "Failed to verify account", nil)
		}
		return
	}

	// Fallback to local database and provider profiles
	institution, err := storage.Client.Institution.
		Query().
		Where(institution.CodeEQ(payload.Institution)).
		WithFiatCurrency().
		Only(ctx)
	if err != nil {
		logger.Errorf("error: %v", err)
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Failed to validate payload", []types.ErrorData{{
			Field:   "Institution",
			Message: "Institution is not supported",
		}})
		return
	}

	// TODO: Remove this after testing non-NGN institutions
	if institution.Edges.FiatCurrency.Code != "NGN" {
		u.APIResponse(ctx, http.StatusOK, "success", "Account name was fetched successfully", "OK")
		return
	}

	providers, err := storage.Client.ProviderProfile.
		Query().
		Where(
			providerprofile.HasCurrencyWith(
				fiatcurrency.CodeEQ(institution.Edges.FiatCurrency.Code),
			),
			providerprofile.HostIdentifierNotNil(),
			providerprofile.IsActiveEQ(true),
			providerprofile.IsAvailableEQ(true),
		).
		All(ctx)
	if err != nil {
		logger.Errorf("error: %v", err)
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to verify account", err.Error())
		return
	}

	var res fastshot.Response
	var data map[string]interface{}
	for _, provider := range providers {
		res, err = fastshot.NewClient(provider.HostIdentifier).
			Config().SetTimeout(30 * time.Second).
			Build().POST("/verify_account").
			Body().AsJSON(payload).
			Send()
		if err != nil {
			continue
		}

		data, err = u.ParseJSONResponse(res.RawResponse)
		if err != nil {
			continue
		}
	}

	if err != nil {
		logger.Errorf("error: %v %v", err, data)
		u.APIResponse(ctx, http.StatusServiceUnavailable, "error", "Failed to verify account", nil)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Account name was fetched successfully", data["data"].(string))
}
