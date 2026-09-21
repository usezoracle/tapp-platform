// The session itself: rotating a refresh token, and ending it.
//
//	POST /v1/auth/refresh
//	POST /v1/auth/logout

package accounts

import (
	"net/http"

	authSvc "github.com/usezoracle/tapp/api/services/auth"

	"github.com/gin-gonic/gin"
	"github.com/usezoracle/tapp/api/types"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/token"
)

// RefreshJWT rotates the refresh token and returns a fresh (access,
// refresh) pair. The old refresh is revoked atomically with the issue.
// Replay of a revoked token revokes the whole family — the user (and
// any attacker holding a copy) must re-login.
func (ctrl *AuthController) RefreshJWT(ctx *gin.Context) {
	var payload types.RefreshJWTPayload
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error",
			"Failed to validate payload", u.GetErrorData(err))
		return
	}

	// JwtRefreshLifespan is already a Duration. Multiplying it by time.Minute
	// again overflowed int64 and wrapped to roughly 246 years, so every refresh
	// token issued was effectively permanent -- one that leaked stayed usable
	// for as long as the account existed.
	refreshTTL := authConf.JwtRefreshLifespan
	issued, user, err := authSvc.Rotate(
		ctx,
		payload.RefreshToken,
		refreshTTL,
		ctx.GetHeader("User-Agent"),
		ctx.ClientIP(),
	)
	if err != nil {
		u.APIResponse(ctx, http.StatusUnauthorized, "error", "Invalid or expired refresh token", nil)
		return
	}

	accessToken, err := token.GenerateAccessJWT(user.ID.String(), user.Scope)
	if err != nil {
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to generate access token", nil)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Successfully refreshed access token", &types.RefreshResponse{
		AccessToken:  accessToken,
		RefreshToken: issued.Raw,
	})
}

// Logout revokes the presented refresh-token family. Unauthenticated +
// idempotent — always returns 200 so:
//
//   - a client whose access JWT has expired can still sign out cleanly
//   - we don't leak whether the refresh token was valid/known
//   - hitting it without a body is safe (no-op)
//
// Rate-limited at the route level to stop someone hammering it as a
// crude revocation oracle.
func (ctrl *AuthController) Logout(ctx *gin.Context) {
	var payload types.LogoutPayload
	_ = ctx.ShouldBindJSON(&payload)

	if payload.RefreshToken != "" {
		_ = authSvc.RevokeByRaw(ctx, payload.RefreshToken)
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Logged out", nil)
}
