package v1

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/internal/card/tap"
	"github.com/usezoracle/tapp/api/internal/equity"
	"github.com/usezoracle/tapp/api/utils/logger"
)

var (
	equityOnce   sync.Once
	equityClient *equity.Client
)

// SharedEquity is the process-wide market client, or nil when no market is
// configured.
//
// Nil is the ordinary state for a deployment without a market: taps charge,
// nothing is queued, and the equity endpoints say the feature is off. It is
// built once so the handlers, the tap hooks and the worker agree on whether
// there is a market at all.
func SharedEquity() *equity.Client {
	equityOnce.Do(func() {
		cfg := config.EquityConfig()
		if !cfg.Enabled() {
			logger.Infof("equity: no FREEDOM_BASE_URL/FREEDOM_RAIL_TOKEN -- taps will not be " +
				"reported to a market and the equity endpoints are off")
			return
		}
		equityClient = equity.New(cfg.BaseURL, cfg.RailToken)
		logger.Infof("equity: taps are reported to %s", cfg.BaseURL)
	})
	return equityClient
}

// RecordTapEquity adapts the outbox to what tap.Service calls after a
// charge. Nil when there is no market, so the tap package does nothing.
func RecordTapEquity(c *equity.Client) func(context.Context, pgx.Tx, tap.Charged) error {
	if !c.Enabled() {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, e tap.Charged) error {
		return equity.EnqueueTap(ctx, tx, equity.TapEvent{
			TapID: e.TapID, Cardholder: e.Cardholder, Merchant: e.Merchant,
			Amount: e.Amount, At: e.At,
		})
	}
}

// RecordReversalEquity is the same for a reversal: it cancels a tap the
// market has not heard of, or queues a reversal of one it has.
func RecordReversalEquity(c *equity.Client) func(context.Context, pgx.Tx, uuid.UUID, string) error {
	if !c.Enabled() {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, tapID uuid.UUID, reason string) error {
		return equity.RecordReversal(ctx, tx, tapID, reason)
	}
}

// OnLegSettled is what every settlement leg calls once it has reached its
// paid state: the tap's held outbox row is queued for the market if the tap
// is now fully settled, and left alone otherwise.
//
// The USDC leg calls it from the offramp settler. The naira leg's worker
// (internal/settlement/naira) must call it after it marks a leg settled,
// through a hook wired in naira_wiring.go. It is safe without a market --
// there are no rows to release -- and safe to call twice. A failure is
// logged and not returned: the leg IS settled, and the worker's sweep
// releases the row on its next tick anyway.
func OnLegSettled(ctx context.Context, q equity.Execer, tapID uuid.UUID) {
	if _, err := equity.ReleaseIfSettled(ctx, q, tapID); err != nil {
		logger.Errorf("equity: release tap %s after settlement: %v", tapID, err)
	}
}
