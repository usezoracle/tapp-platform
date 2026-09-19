package v1

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/viper"

	"github.com/usezoracle/tapp/api/ent/fiatcurrency"
	"github.com/usezoracle/tapp/api/ent/institution"
	"github.com/usezoracle/tapp/api/internal/money"
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

	resolverOnce sync.Once
	resolver     *naira.BankCodeResolver
)

// SharedBankCodeResolver translates the catalogue code a merchant's bank
// account stores into the code the naira rail knows the bank by. One per
// process: it caches both lists for an hour. Its bank list is read from
// whatever rail is configured at the time of the call, so a resolver built
// before the rail was registered still works.
//
// The catalogue is the institutions table, seeded from the aggregator's
// list: the codes merchants choose their bank by.
func SharedBankCodeResolver() *naira.BankCodeResolver {
	resolverOnce.Do(func() {
		resolver = &naira.BankCodeResolver{
			Banks: func(ctx context.Context) ([]baas.Bank, error) {
				p := baas.Default()
				if p == nil {
					return nil, baas.ErrNotConfigured
				}
				return p.ListBanks(ctx)
			},
			Institutions: func(ctx context.Context) ([]naira.Institution, error) {
				if storage.Client == nil {
					return nil, nil
				}
				rows, err := storage.Client.Institution.Query().
					Where(institution.HasFiatCurrencyWith(fiatcurrency.CodeEQ("NGN"))).
					All(ctx)
				if err != nil {
					return nil, err
				}
				out := make([]naira.Institution, 0, len(rows))
				for _, r := range rows {
					out = append(out, naira.Institution{Code: r.Code, Name: r.Name})
				}
				return out, nil
			},
		}
	})
	return resolver
}

// SharedNairaWorker is the process-wide worker for naira legs. Never nil:
// with no rail that can pay out of a wallet, its ticks report ErrNoRail and
// rows queue.
func SharedNairaWorker() *naira.Worker {
	nairaOnce.Do(func() {
		nairaWorker = &naira.Worker{
			Pool: storage.Pool, Rail: baas.Default(),
			Resolver: SharedBankCodeResolver(),
			Settled: func(ctx context.Context, q naira.Execer, tapID uuid.UUID) {
				OnLegSettled(ctx, q, tapID)
			},
			MerchantName: func(ctx context.Context, merchant uuid.UUID) string {
				return MerchantName(ctx, merchant)
			},
			// The rail's flat charges, in kobo, as observed on its wallet
			// balances; it reports neither. Override when they change.
			SweepFee: money.New(viper.GetInt64("NGN_SWEEP_FEE_KOBO"), money.NGN),
			BankFee:  money.New(viper.GetInt64("NGN_BANK_FEE_KOBO"), money.NGN),
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
