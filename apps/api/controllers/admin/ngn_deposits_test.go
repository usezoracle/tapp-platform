package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"

	"github.com/usezoracle/tapp/api/controllers/cards"
	"github.com/usezoracle/tapp/api/ent/adminauditlog"
	"github.com/usezoracle/tapp/api/ent/enttest"
	apiv1 "github.com/usezoracle/tapp/api/internal/api/v1"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/baas"
	"github.com/usezoracle/tapp/api/storage"
)

// ngnStubRail satisfies baas.Provider through the nil embedded interface;
// only Name is ever asked of it here.
type ngnStubRail struct{ baas.Provider }

func (ngnStubRail) Name() string { return "fintava" }

// ngnAdminRouter mounts the three deposit-account routes behind the real
// admin token middleware, over a sqlite audit log and stubbed actions.
func ngnAdminRouter(t *testing.T, c *NGNDepositsController) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	client := enttest.Open(t, "sqlite3", "file:ngnadmin_"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	prev := storage.Client
	storage.Client = client
	t.Cleanup(func() { storage.Client = prev })

	viper.Set("ADMIN_API_TOKEN", "adm-secret")
	t.Cleanup(func() { viper.Set("ADMIN_API_TOKEN", "") })

	r := gin.New()
	g := r.Group("/v1/admin/")
	g.Use(cards.AdminTokenMiddleware)
	g.GET("deposits/ngn/accounts", c.Find)
	g.POST("deposits/ngn/accounts/:account_number/bank", c.SetBankName)
	g.POST("deposits/ngn/accounts/:account_number/reconcile", c.ReconcileAccount)
	return r
}

func adminDo(r *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Admin-Token", token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestNGNDepositsAdmin_AuthAndAudit(t *testing.T) {
	var reconciled, bankSet int
	c := &NGNDepositsController{
		Rail: func() baas.Provider { return ngnStubRail{} },
		ByEmail: func(_ context.Context, email string) ([]*apiv1.NGNAccountRow, error) {
			return []*apiv1.NGNAccountRow{{Email: email, AccountNumber: "1234567890", BankName: "Fintava partner bank", NeedsBankFix: true}}, nil
		},
		SetBank: func(_ context.Context, account, name, code string) (*apiv1.NGNAccountRow, error) {
			bankSet++
			if account == "0000000000" {
				return nil, apiv1.ErrNGNAccountNotFound
			}
			return &apiv1.NGNAccountRow{AccountNumber: account, BankName: name, BankCode: code}, nil
		},
		Reconcile: func(_ context.Context, rail baas.Provider, account string) (*apiv1.NGNReconcileResult, error) {
			reconciled++
			if rail == nil || rail.Name() != "fintava" {
				t.Errorf("rail = %v", rail)
			}
			return &apiv1.NGNReconcileResult{
				AccountNumber: account, WalletBalance: money.Naira(100), CreditedBefore: money.Naira(0),
				Posted: money.Naira(100), Reference: "reconcile:" + account + ":10000:2026-09-19", WalletID: "wal-1",
			}, nil
		},
	}
	r := ngnAdminRouter(t, c)

	// No token, wrong token: refused before anything runs.
	for _, tok := range []string{"", "wrong"} {
		w := adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/1234567890/reconcile", tok, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code, "token %q", tok)
		w = adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/1234567890/bank", tok, `{"bank_name":"Loma Microfinance Bank"}`)
		assert.Equal(t, http.StatusUnauthorized, w.Code, "token %q", tok)
		w = adminDo(r, http.MethodGet, "/v1/admin/deposits/ngn/accounts?email=a@b.c", tok, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code, "token %q", tok)
	}
	assert.Equal(t, 0, reconciled)
	assert.Equal(t, 0, bankSet)

	// Find by email.
	w := adminDo(r, http.MethodGet, "/v1/admin/deposits/ngn/accounts?email=ada@example.com", "adm-secret", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"needs_bank_fix":true`)
	w = adminDo(r, http.MethodGet, "/v1/admin/deposits/ngn/accounts", "adm-secret", "")
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Set the bank: validated, applied, audited.
	w = adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/1234567890/bank", "adm-secret", `{"bank_code":"090620"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code, "bank_name is required")
	w = adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/1234567890/bank", "adm-secret",
		`{"bank_name":"Loma Microfinance Bank","bank_code":"090620"}`)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"bank_code":"090620"`)
	w = adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/0000000000/bank", "adm-secret",
		`{"bank_name":"Loma Microfinance Bank"}`)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// Reconcile: the result comes back as money.Amounts and is audited with
	// the figures it was computed from.
	w = adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/1234567890/reconcile", "adm-secret", "")
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 1, reconciled)
	assert.Contains(t, w.Body.String(), `"posted":{"minor":10000,"currency":"NGN"`)
	assert.Contains(t, w.Body.String(), `"reference":"reconcile:1234567890:10000:2026-09-19"`)

	ctx := context.Background()
	logs := storage.Client.AdminAuditLog.Query().Order(adminauditlog.ByCreatedAt()).AllX(ctx)
	if assert.Len(t, logs, 2) {
		assert.Equal(t, "ngn_deposit.bank.set", logs[0].Action)
		assert.Equal(t, "1234567890", logs[0].Target)
		assert.Equal(t, "Loma Microfinance Bank", logs[0].Detail["bank_name"])
		assert.Equal(t, "ngn_deposit.reconcile", logs[1].Action)
		assert.Equal(t, "1234567890", logs[1].Target)
		assert.EqualValues(t, 10000, logs[1].Detail["posted_minor"])
		assert.Equal(t, "reconcile:1234567890:10000:2026-09-19", logs[1].Detail["reference"])
	}
}

// Without a rail there is nothing to read a balance from; without a row
// there is nothing to reconcile. Neither is audited: nothing happened.
func TestNGNDepositsAdmin_ReconcileGates(t *testing.T) {
	c := &NGNDepositsController{
		Rail: func() baas.Provider { return nil },
		Reconcile: func(context.Context, baas.Provider, string) (*apiv1.NGNReconcileResult, error) {
			return nil, apiv1.ErrNGNAccountNotFound
		},
	}
	r := ngnAdminRouter(t, c)
	w := adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/1234567890/reconcile", "adm-secret", "")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	c.Rail = func() baas.Provider { return ngnStubRail{} }
	w = adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/1234567890/reconcile", "adm-secret", "")
	assert.Equal(t, http.StatusNotFound, w.Code)

	c.Reconcile = func(context.Context, baas.Provider, string) (*apiv1.NGNReconcileResult, error) {
		return nil, errors.New("fintava: http 502")
	}
	w = adminDo(r, http.MethodPost, "/v1/admin/deposits/ngn/accounts/1234567890/reconcile", "adm-secret", "")
	assert.Equal(t, http.StatusBadGateway, w.Code)

	assert.Equal(t, 0, storage.Client.AdminAuditLog.Query().CountX(context.Background()))
}
