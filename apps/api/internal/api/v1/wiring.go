package v1

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/internal/rates"
	"github.com/usezoracle/tapp/api/internal/rates/ratescfg"
	"github.com/usezoracle/tapp/api/storage"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// NewConvertHandler builds the conversion handler from configuration.
//
// It returns nil when no rate sources are configured, and the routes are then
// not registered at all. That is deliberate: an endpoint that exists but can
// never price anything answers every request with a 503, which reads as an
// outage rather than as a feature that was never switched on.
func NewConvertHandler() *ConvertHandler {
	engine, spread, err := ratescfg.FromEnv()
	if err != nil {
		logger.Errorf("rates: %v -- currency conversion is not available", err)
		return nil
	}
	if len(engine.Sources) == 0 {
		logger.Infof("rates: no FX_SOURCES configured -- currency conversion is not available")
		return nil
	}
	if len(spread) == 0 {
		logger.Errorf("rates: FX_SOURCES is set but FX_SPREADS is not -- currency conversion is not available")
		return nil
	}

	return &ConvertHandler{Quoter: SharedQuoter(), User: UserFromContext}
}

// UserFromContext resolves the authenticated caller's user id.
func UserFromContext(ctx *gin.Context) (uuid.UUID, bool) {
	value, exists := ctx.Get("user_id")
	if !exists || value == nil {
		u.APIResponse(ctx, http.StatusUnauthorized, "error", "Not authenticated", nil)
		return uuid.Nil, false
	}
	switch id := value.(type) {
	case uuid.UUID:
		return id, true
	case string:
		parsed, err := uuid.Parse(id)
		if err == nil {
			return parsed, true
		}
	}
	u.APIResponse(ctx, http.StatusUnauthorized, "error", "Not authenticated", nil)
	return uuid.Nil, false
}

// sharedQuoter is the process-wide price engine, built once.
//
// One instance, because a quote issued by one and redeemed by another would
// be redeemed against a different spread table -- and the price somebody was
// shown must be the price they get.
var sharedQuoter *rates.Quoter

// SharedQuoter returns it, building it on first use. Nil when no rate sources
// are configured, in which case a conversion cannot be priced and an order
// that needs one is refused rather than guessed at.
func SharedQuoter() *rates.Quoter {
	if sharedQuoter != nil {
		return sharedQuoter
	}
	engine, spread, err := ratescfg.FromEnv()
	if err != nil || len(engine.Sources) == 0 || len(spread) == 0 {
		return nil
	}
	sharedQuoter = &rates.Quoter{Engine: engine, Spread: spread, Pool: storage.Pool}
	return sharedQuoter
}
