// Creating an account.
//
//	POST /v1/auth/register

package accounts

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/ent"
	"github.com/usezoracle/tapp/api/ent/fiatcurrency"
	"github.com/usezoracle/tapp/api/ent/providerprofile"
	userEnt "github.com/usezoracle/tapp/api/ent/user"
	"github.com/usezoracle/tapp/api/ent/verificationtoken"
	authSvc "github.com/usezoracle/tapp/api/services/auth"
	db "github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/types"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/crypto"
	"github.com/usezoracle/tapp/api/utils/logger"
	"github.com/usezoracle/tapp/api/utils/token"
)

func (ctrl *AuthController) Register(ctx *gin.Context) {
	var payload types.RegisterPayload

	serverConf := config.ServerConfig()

	if err := ctx.ShouldBindJSON(&payload); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", u.GetErrorData(err))
		return
	}

	tx, err := db.Client.Tx(ctx)
	if err != nil {
		logger.Errorf("error: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to create new user", nil)
		return
	}

	// Check if user with email already exists
	userTmp, _ := tx.User.
		Query().
		Where(
			userEnt.EmailEQ(strings.ToLower(payload.Email)),
		).
		Only(ctx)

	if userTmp != nil {
		_ = tx.Rollback()
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"User with email already exists", nil)
		return
	}

	// Save the user
	scope := strings.Join(payload.Scopes, " ")
	// A wallet that cannot be sealed is not a wallet. Previously this logged
	// and carried on, leaving an account with no address and no signal that
	// anything had gone wrong; the user discovered it at their first deposit.
	var evmAddr, encKey string
	masterKey, wErr := crypto.MasterKey()
	if wErr == nil {
		evmAddr, encKey, wErr = crypto.GenerateEVMWallet(masterKey)
	}
	if wErr != nil {
		_ = tx.Rollback()
		logger.Errorf("Register: wallet generation: %v", wErr)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to create account", nil)
		return
	}

	userCreate := tx.User.
		Create().
		SetFirstName(payload.FirstName).
		SetLastName(payload.LastName).
		SetEmail(strings.ToLower(payload.Email)).
		SetPassword(payload.Password).
		SetScope(scope).
		SetIsEmailVerified(true).
		SetHasEarlyAccess(true)

	userCreate = userCreate.SetEvmAddress(evmAddr).SetEncryptedPrivateKey(encKey)

	user, err := userCreate.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		logger.Errorf("error: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to create new user", nil)
		return
	}

	// Issue verification OTP. We store SHA-256(code) in the DB and email
	// the 6-digit code to the user. On confirm, we hash the submitted code
	// and compare (with a per-email attempt cap — see otp_guard.go).
	rawToken, err := token.GenerateOTP()
	if err != nil {
		logger.Errorf("error: %v", err)
		_ = tx.Rollback()
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to create new user", nil)
		return
	}
	_, vtErr := tx.VerificationToken.
		Create().
		SetOwner(user).
		SetToken(token.HashToken(rawToken)).
		SetScope(verificationtoken.ScopeEmailVerification).
		SetExpiryAt(time.Now().Add(authConf.EmailVerificationLifespan)).
		Save(ctx)
	if vtErr != nil {
		logger.Errorf("error: %v", vtErr)
	}

	// Send the verification email in every deployed environment (not just prod).
	if serverConf.Environment != "local" && vtErr == nil {
		// Fresh code → clear any stale attempt counter for this email.
		clearOTPAttempts(ctx, string(verificationtoken.ScopeEmailVerification), user.Email)
		if _, err := ctrl.emailService.SendVerificationEmail(ctx, rawToken, user.Email, user.FirstName); err != nil {
			logger.Errorf("error: %v", err)
		}
	}

	scopes := payload.Scopes

	// Create a provider profile
	if u.ContainsString(scopes, "provider") {
		// Fetch currency
		if payload.Currency == "" {
			_ = tx.Rollback()
			u.APIResponse(ctx, http.StatusBadRequest, "error",
				"Currency is required for provider account", nil)
			return
		}
		currency, err := tx.FiatCurrency.
			Query().
			Where(
				fiatcurrency.IsEnabledEQ(true),
				fiatcurrency.CodeEQ(payload.Currency),
			).
			Only(ctx)
		if err != nil {
			_ = tx.Rollback()
			if ent.IsNotFound(err) {
				u.APIResponse(ctx, http.StatusBadRequest, "error",
					"Failed to validate payload", []types.ErrorData{{
						Field:   "Currency",
						Message: "Currency is not supported",
					}})
				return
			}
			logger.Errorf("error: %v", err)
			u.APIResponse(ctx, http.StatusInternalServerError, "error",
				"Failed to create new user", nil)
			return
		}

		provider, err := tx.ProviderProfile.
			Create().
			SetCurrency(currency).
			SetVisibilityMode(providerprofile.VisibilityModePrivate).
			SetUser(user).
			SetProvisionMode(providerprofile.ProvisionModeAuto).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			logger.Errorf("error: %v", err)
			u.APIResponse(ctx, http.StatusInternalServerError, "error",
				"Failed to create new user", nil)
			return
		}

		// Generate the API key using the service
		_, _, err = ctrl.apiKeyService.GenerateAPIKey(ctx, tx, nil, provider)
		if err != nil {
			_ = tx.Rollback()
			logger.Errorf("error: %v", err)
			u.APIResponse(ctx, http.StatusInternalServerError, "error",
				"Failed to create new user", nil)
			return
		}
	}

	// Create a sender profile
	if u.ContainsString(scopes, "sender") {
		sender, err := tx.SenderProfile.
			Create().
			SetUser(user).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			logger.Errorf("error: %v", err)
			u.APIResponse(ctx, http.StatusInternalServerError, "error",
				"Failed to create new user", nil)
			return
		}

		// Generate the API key using the service
		_, _, err = ctrl.apiKeyService.GenerateAPIKey(ctx, tx, sender, nil)
		if err != nil {
			_ = tx.Rollback()
			logger.Errorf("error: %v", err)
			u.APIResponse(ctx, http.StatusInternalServerError, "error",
				"Failed to create new user", nil)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		logger.Errorf("error: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to create new user", nil)
		return
	}

	var accessToken, refreshToken string
	var errJwt error
	accessToken, errJwt = token.GenerateAccessJWT(user.ID.String(), user.Scope)
	if errJwt != nil {
		logger.Errorf("error: %v", errJwt)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to generate access token on registration", nil)
		return
	}

	// JwtRefreshLifespan is already a Duration. Multiplying it by time.Minute
	// again overflowed int64 and wrapped to roughly 246 years, so every refresh
	// token issued was effectively permanent -- one that leaked stayed usable
	// for as long as the account existed.
	refreshTTL := authConf.JwtRefreshLifespan
	issued, errJwt := authSvc.IssueNewFamily(
		ctx,
		user.ID,
		refreshTTL,
		ctx.GetHeader("User-Agent"),
		ctx.ClientIP(),
	)
	if errJwt != nil {
		logger.Errorf("error: %v", errJwt)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to generate refresh token on registration", nil)
		return
	}
	refreshToken = issued.Raw

	response := &types.RegisterResponse{
		ID:           user.ID,
		CreatedAt:    user.CreatedAt,
		UpdatedAt:    user.UpdatedAt,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		Email:        user.Email,
		EVMAddress:   user.EvmAddress,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}

	u.APIResponse(ctx, http.StatusCreated, "success", "User created successfully", response)
}
