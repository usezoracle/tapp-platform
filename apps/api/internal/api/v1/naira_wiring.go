package v1

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/viper"

	"github.com/usezoracle/tapp/api/internal/settlement/naira"
	"github.com/usezoracle/tapp/api/services/baas"
	"github.com/usezoracle/tapp/api/storage"
)

// The naira leg of a tap: what the cardholder paid from a naira balance is
// paid to the merchant out of the cardholder's own wallet at the bank rail.
//
// Built on baas.Default(), the same rail the deposit accounts were opened on:
// the wallet being debited belongs to that provider, and no other can reach
// it.

var (
	nairaOnce   sync.Once
	nairaWorker *naira.Worker
)

// SharedNairaWorker is the process-wide worker for naira legs. Never nil:
// with no rail that can pay out of a wallet, its ticks report ErrNoRail and
// rows queue.
func SharedNairaWorker() *naira.Worker {
	nairaOnce.Do(func() {
		nairaWorker = &naira.Worker{
			Pool: storage.Pool, Rail: baas.Default(),
			MerchantName: func(ctx context.Context, merchant uuid.UUID) string {
				return MerchantName(ctx, merchant)
			},
		}
	})
	return nairaWorker
}

// NairaSettlementInterval is how often queued naira legs are paid.
func NairaSettlementInterval() time.Duration {
	viper.SetDefault("NGN_SETTLEMENT_INTERVAL_SECONDS", 10)
	seconds := viper.GetInt("NGN_SETTLEMENT_INTERVAL_SECONDS")
	if seconds < 5 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}
