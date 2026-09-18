package routers

import (
	"strings"

	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/routers/middleware"
	"github.com/usezoracle/tapp/api/utils/logger"

	"github.com/gin-gonic/gin"
)

// trustedProxyCIDRs converts the TRUSTED_PROXIES setting into CIDRs gin accepts.
// gin requires IPs/CIDRs — passing "*" verbatim (the old behaviour) returns an
// error and crash-loops boot. "*" or empty → trust all (we sit behind a PaaS
// edge proxy and need X-Forwarded-For for real client IPs / rate-limiting);
// otherwise a comma-separated CIDR list to lock trust down.
func trustedProxyCIDRs(raw string) []string {
	if p := strings.TrimSpace(raw); p != "" && p != "*" {
		out := make([]string, 0, 4)
		for _, c := range strings.Split(p, ",") {
			if c = strings.TrimSpace(c); c != "" {
				out = append(out, c)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{"0.0.0.0/0", "::/0"}
}

// allowedOrigins lists the browser origins that may call this API: the PWA,
// the checkout site and the admin console, as configured. Local development
// adds the dev servers on either loopback name, so a fresh checkout works
// without editing config; nothing else is ever added implicitly.
//
// The admin console is its own origin because it is its own deployment -- a
// Vite SPA on Vercel, not a page of the PWA -- and it calls /v1/admin and
// /v1/cards from a browser. Without its origin here the API answers without
// Access-Control-Allow-Origin, the browser discards the response, and the
// console can only report a network error: the login screen then reads as a
// bad admin token, which is the wrong thing to go looking at.
//
// Blank entries are dropped rather than passed through. An unset
// ADMIN_BASE_URL must mean "no admin origin"; it must never become an ""
// entry that some later refactor reads as a wildcard.
func allowedOrigins(conf *config.ServerConfiguration) []string {
	configured := []string{conf.PWABaseURL, conf.CheckoutBaseURL, conf.AdminBaseURL, conf.BusinessBaseURL}
	origins := make([]string, 0, len(configured)+4)
	for _, o := range configured {
		if strings.TrimSpace(o) != "" {
			origins = append(origins, o)
		}
	}
	if conf.Environment == "local" {
		origins = append(origins,
			"http://localhost:3000", "http://127.0.0.1:3000",
			// The admin console's Vite dev server.
			"http://localhost:5173", "http://127.0.0.1:5173")
	}
	return origins
}

// Routes function registers all routes
func Routes() *gin.Engine {
	conf := config.ServerConfig()

	environment := conf.Debug
	if environment {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.RemoveExtraSlash = true
	proxies := trustedProxyCIDRs(conf.TrustedProxies)
	if err := router.SetTrustedProxies(proxies); err != nil {
		logger.Fatalf("failed to set trusted proxies %v: %v", proxies, err)
	}
	router.Use(gin.Logger())
	router.Use(gin.Recovery())
	router.Use(middleware.CORSMiddleware(allowedOrigins(conf)))

	RegisterRoutes(router) //routes register

	return router
}
