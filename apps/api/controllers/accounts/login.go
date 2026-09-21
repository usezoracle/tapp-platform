// Signing in.
//
//	POST /v1/auth/login

package accounts

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	userEnt "github.com/usezoracle/tapp/api/ent/user"
	authSvc "github.com/usezoracle/tapp/api/services/auth"
	db "github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/types"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/crypto"
	"github.com/usezoracle/tapp/api/utils/logger"
	"github.com/usezoracle/tapp/api/utils/token"
)

// Login controller validates the payload and creates a new user.
func (ctrl *AuthController) Login(ctx *gin.Context) {
	var payload types.LoginPayload

	if err := ctx.ShouldBindJSON(&payload); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", u.GetErrorData(err))
		return
	}

	// Fetch user by email
	user, err := db.Client.User.
		Query().
		Where(userEnt.EmailEQ(strings.ToLower(payload.Email))).
		Only(ctx)

	if err != nil {
		u.APIResponse(ctx, http.StatusUnauthorized, "error",
			"Email and password do not match any user", nil,
		)
		return
	}

	// Check if the password is correct
	passwordMatch := crypto.CheckPasswordHash(payload.Password, user.Password)
	if !passwordMatch {
		u.APIResponse(ctx, http.StatusUnauthorized, "error",
			"Email and password do not match any user", nil,
		)
		return
	}

	// Check if user has early access. Providers (LPs) are onboarded partners,
	// not part of the consumer beta, so the early-access wall never applies to
	// them — this also unblocks LP accounts created before early access was
	// granted on registration.
	environment := serverConf.Environment
	isProvider := u.ContainsString(strings.Fields(user.Scope), "provider")
	if !user.HasEarlyAccess && !isProvider && (environment == "production" || environment == "staging") {
		u.APIResponse(ctx, http.StatusUnauthorized, "error",
			"Your early access request is still pending", nil,
		)
		return
	}

	// Ensure user has an EVM wallet
	if user.EvmAddress == "" {
		var evmAddr, encKey string
		masterKey, wErr := crypto.MasterKey()
		if wErr == nil {
			evmAddr, encKey, wErr = crypto.GenerateEVMWallet(masterKey)
		}
		if wErr != nil {
			logger.Errorf("Login: wallet backfill for user %s: %v", user.ID, wErr)
			u.APIResponse(ctx, http.StatusInternalServerError, "error",
				"Failed to sign in", nil)
			return
		}
		if err := user.Update().
			SetEvmAddress(evmAddr).
			SetEncryptedPrivateKey(encKey).
			Exec(ctx); err != nil {
			logger.Errorf("Login: persist wallet backfill for user %s: %v", user.ID, err)
			u.APIResponse(ctx, http.StatusInternalServerError, "error",
				"Failed to sign in", nil)
			return
		}
		user.EvmAddress = evmAddr
	}

	// Stateless short-lived access JWT.
	accessToken, err := token.GenerateAccessJWT(user.ID.String(), user.Scope)
	if err != nil {
		logger.Errorf("error: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to create access token", nil,
		)
		return
	}

	// Stateful opaque refresh token — issued in a new family. Revocable
	// via /auth/logout, rotated on every /auth/refresh.
	// JwtRefreshLifespan is already a Duration. Multiplying it by time.Minute
	// again overflowed int64 and wrapped to roughly 246 years, so every refresh
	// token issued was effectively permanent -- one that leaked stayed usable
	// for as long as the account existed.
	refreshTTL := authConf.JwtRefreshLifespan
	issued, err := authSvc.IssueNewFamily(
		ctx,
		user.ID,
		refreshTTL,
		ctx.GetHeader("User-Agent"),
		ctx.ClientIP(),
	)
	if err != nil {
		logger.Errorf("error: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"Failed to create refresh token", nil,
		)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Successfully logged in", &types.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: issued.Raw,
		Scopes:       strings.Split(user.Scope, " "),
		EVMAddress:   user.EvmAddress,
	})
}
