package v1

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/rates"
	"github.com/usezoracle/tapp/api/storage"
	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// BalanceHandler answers what somebody holds.
type BalanceHandler struct {
	User func(*gin.Context) (uuid.UUID, bool)
	// DB is the pool to read from; nil means storage.Pool. Set by tests.
	DB *pgxpool.Pool
}

func (h *BalanceHandler) db() *pgxpool.Pool {
	if h.DB != nil {
		return h.DB
	}
	return storage.Pool
}

// currencyBalance is one currency's position, split by what it can be used for.
type currencyBalance struct {
	Currency money.Currency `json:"currency"`

	// Available is spendable now. Escrow is committed to a handover that has
	// not completed. They are reported separately because collapsing them into
	// one figure is how a user comes to believe money is theirs to spend when
	// somebody else is already relying on it.
	Available money.Amount `json:"available"`
	Escrow    money.Amount `json:"escrow"`
}

type balancesResponse struct {
	Balances []currencyBalance `json:"balances"`

	// Total is everything the caller holds, expressed in one currency.
	//
	// Nil when no rate is available, and the client must then show the
	// per-currency figures alone rather than a total. There is deliberately no
	// fallback rate: a total assembled from a stale or invented number is a
	// wrong answer to "how much do I have", which is the one question this
	// endpoint exists to answer.
	Total *totalBalance `json:"total,omitempty"`
}

// totalBalance is the summed position, and enough provenance to be honest
// about what it is.
type totalBalance struct {
	// Amount is the sum of every currency's AVAILABLE balance, converted.
	// Escrow is excluded for the same reason it is reported separately above:
	// folding committed money into a headline is how somebody comes to believe
	// it is theirs to spend.
	Amount money.Amount `json:"amount"`

	// Converted says at least one currency had to be converted to produce
	// this, so the client can mark it as approximate. A total that happens to
	// need no conversion is exact and should not be hedged.
	Converted bool `json:"converted"`

	// Rates names the mid-market price used per pair, so a figure somebody
	// disputes can be traced rather than argued about.
	Rates map[string]string `json:"rates,omitempty"`
}

// Balances returns every currency the caller holds.
//
// Every supported currency is returned, including the ones at zero. A client
// that only ever hears about currencies with a non-zero balance has no way to
// show somebody that USD exists and they could hold some -- and a balance
// appearing out of nowhere on first deposit reads as a bug.
func (h *BalanceHandler) Balances(ctx *gin.Context) {
	user, ok := h.User(ctx)
	if !ok {
		return
	}

	out := make([]currencyBalance, 0, len(money.SupportedCurrencies()))
	for _, c := range money.SupportedCurrencies() {
		row, err := readBalance(ctx.Request.Context(), user, c)
		if err != nil {
			// One unreadable currency makes the whole answer wrong, and a
			// partial list of balances is worse than no list: it is
			// indistinguishable from a real balance of zero.
			logger.Errorf("balances: %v", err)
			u.APIResponse(ctx, http.StatusInternalServerError, "error",
				"We could not read your balances just now.", nil)
			return
		}
		out = append(out, row)
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Balances retrieved",
		balancesResponse{Balances: out, Total: totalIn(ctx.Request.Context(), out, money.NGN)})
}

// totalIn sums every available balance into one currency.
//
// Returns nil rather than a partial figure when any leg cannot be priced. A
// total missing one of two currencies is indistinguishable from a real total,
// and would quietly under-report what somebody has.
//
// The MID-MARKET rate is used, not a tradeable quote. A quote carries the
// spread, and showing a holding net of a fee that has not been charged
// under-reports it; what is being answered here is "what is this worth", not
// "what would I get for it".
func totalIn(ctx context.Context, rows []currencyBalance, into money.Currency) *totalBalance {
	sum := money.Zero(into)
	converted := false
	used := map[string]string{}

	quoter := SharedQuoter()
	for _, row := range rows {
		if row.Available.IsZero() {
			continue
		}
		if row.Available.Currency() == into {
			next, err := sum.Add(row.Available)
			if err != nil {
				return nil
			}
			sum = next
			continue
		}
		if quoter == nil {
			return nil
		}
		pair := rates.Pair{Base: row.Available.Currency(), Quote: into}
		rate, err := quoter.Engine.Market(ctx, pair)
		if err != nil {
			logger.Errorf("balances: no rate for %s: %v", pair, err)
			return nil
		}
		minor := decimal.NewFromInt(row.Available.Minor()).Mul(rate.Mid).IntPart()
		next, err := sum.Add(money.New(minor, into))
		if err != nil {
			return nil
		}
		sum = next
		converted = true
		used[pair.String()] = rate.Mid.String()
	}

	total := &totalBalance{Amount: sum, Converted: converted}
	if converted {
		total.Rates = used
	}
	return total
}

func readBalance(ctx context.Context, user uuid.UUID, c money.Currency) (currencyBalance, error) {
	owner := ledger.User(user)

	available, err := ledger.Balance(ctx, storage.Pool, owner, ledger.KindAvailable, c)
	if err != nil {
		return currencyBalance{}, err
	}
	escrow, err := ledger.Balance(ctx, storage.Pool, owner, ledger.KindEscrow, c)
	if err != nil {
		return currencyBalance{}, err
	}
	return currencyBalance{Currency: c, Available: available, Escrow: escrow}, nil
}

// activityQuery is the paging contract for the movement feed.
type activityQuery struct {
	Limit  int    `form:"limit"`
	Cursor string `form:"cursor"`
}

// activityMovement is a ledger movement as the app sees it: the ledger's own
// fields, plus who took the money when it was a tap.
type activityMovement struct {
	ledger.Movement
	// Merchant is where the card was spent. Present on tap movements, null on
	// everything else.
	Merchant *merchantView `json:"merchant"`
}

type activityPage struct {
	Movements  []activityMovement `json:"movements"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

// Activity returns the caller's movements, newest first.
//
// Derived from the ledger entries themselves, so the feed and the balance
// above it are the same rows read two ways and cannot disagree. Its
// predecessor read the user's on-chain transaction list from an RPC provider,
// which described what happened on a chain rather than what happened to their
// money.
//
// Tap movements are then named: one query for the whole page collects the
// taps' merchants, so a page of thirty taps costs two queries, not thirty-one.
func (h *BalanceHandler) Activity(ctx *gin.Context) {
	user, ok := h.User(ctx)
	if !ok {
		return
	}

	var q activityQuery
	if err := ctx.ShouldBindQuery(&q); err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid request", u.GetErrorData(err))
		return
	}

	page, err := ledger.History(ctx.Request.Context(), h.db(),
		ledger.User(user), q.Limit, q.Cursor)
	if err != nil {
		var bad ledger.ErrBadCursor
		if errors.As(err, &bad) {
			u.APIResponse(ctx, http.StatusBadRequest, "error",
				"That page reference is not one we issued.",
				map[string]any{"code": "bad_cursor"})
			return
		}
		logger.Errorf("activity: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"We could not read your activity just now.", nil)
		return
	}

	out, err := nameTapMerchants(ctx.Request.Context(), h.db(), page)
	if err != nil {
		logger.Errorf("activity: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error",
			"We could not read your activity just now.", nil)
		return
	}
	u.APIResponse(ctx, http.StatusOK, "success", "Activity retrieved", out)
}

// nameTapMerchants decorates a page's tap movements with their merchant.
func nameTapMerchants(ctx context.Context, q ledger.Querier, page *ledger.Page) (activityPage, error) {
	var taps []uuid.UUID
	for _, m := range page.Movements {
		if m.RefType == "tap" && m.RefID != nil {
			taps = append(taps, *m.RefID)
		}
	}
	named, err := merchantsOfTaps(ctx, q, taps)
	if err != nil {
		return activityPage{}, err
	}
	out := activityPage{Movements: make([]activityMovement, 0, len(page.Movements)), NextCursor: page.NextCursor}
	for _, m := range page.Movements {
		am := activityMovement{Movement: m}
		if m.RefType == "tap" && m.RefID != nil {
			if mv, ok := named[*m.RefID]; ok {
				am.Merchant = &mv
			}
		}
		out.Movements = append(out.Movements, am)
	}
	return out, nil
}
