package base

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MinSweepMicro is the smallest balance worth moving.
//
// A sweep costs gas. Below this the transfer costs more than it recovers, so
// the funds are left to accumulate rather than burned moving them. One dollar.
const MinSweepMicro = 1_000_000

// sweepKeyVersion namespaces the idempotency keys sent to CDP.
//
// 2: the smart account is addressed in EIP-55 case. Keys minted under 1 were
// bound by CDP to the lower-case request and answer anything else with a 422.
const sweepKeyVersion = 2

// Sweeper moves credited deposits from derived addresses into the treasury.
//
// Pooling is the point: one key protects everything, rather than one key per
// account. Until a deposit is swept it sits at an address whose key must be
// re-derived to touch, which is fine but scattered -- and a withdrawal cannot
// be paid from money spread across a thousand addresses.
type Sweeper struct {
	Pool    *pgxpool.Pool
	Chain   *Chain
	Deriver *Deriver

	// SmartAccounts sweeps CDP-provided addresses. Nil when CDP is not
	// configured, in which case a cdp row cannot be swept and says so.
	SmartAccounts SmartAccounts

	// OnSpend records what a sweep cost, if anything is listening. Optional
	// and called after the sweep is durably recorded: a failure to note the
	// cost must never undo a sweep that actually happened, because the money
	// has moved either way and the deposit row is the thing that must not lie.
	OnSpend func(ctx context.Context, depositID uuid.UUID, txHash string)
}

// Sweep moves every credited deposit that has not been moved yet.
//
// It sweeps the address's WHOLE balance rather than the deposit amount. An
// address may have received several deposits, or dust from somewhere; moving
// the balance leaves nothing behind and means a later sweep has nothing to do,
// whereas moving exact amounts would leave a long tail of remainders that each
// cost gas to collect.
func (s *Sweeper) Sweep(ctx context.Context) (swept int, err error) {
	if s.Chain.Treasury == (common.Address{}) {
		// Nowhere to sweep into. Not an error: a read-only deployment still
		// credits deposits correctly, it just leaves them where they landed.
		return 0, nil
	}

	// Joined on the address the deposit actually landed in, not on its owner.
	// A person can hold several addresses once they have been reissued -- one
	// current, any number retired -- and joining by user_id would return every
	// one of them for a single deposit, then sweep from whichever the database
	// happened to order first. That address may hold nothing, while the one the
	// money is sitting in is never touched.
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, d.user_id, a.provider, a.index, a.address
		  FROM base_deposits d
		  JOIN base_deposit_addresses a ON a.address = d.to_address
		 WHERE d.state = 'credited'
		 LIMIT 50`)
	if err != nil {
		return 0, fmt.Errorf("base: find deposits to sweep: %w", err)
	}

	type pending struct {
		depositID uuid.UUID
		userID    uuid.UUID
		provider  string
		index     *int64
		address   string
	}
	var due []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.depositID, &p.userID, &p.provider, &p.index, &p.address); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, p := range due {
		var err error
		switch p.provider {
		case ProviderCDP:
			err = s.sweepSmartAccount(ctx, p.depositID, p.address)
		default:
			if !s.Chain.CanSend() {
				// A derived address is swept with a permit the treasury pays
				// to submit. Without its key nothing can be paid for.
				continue
			}
			if p.index == nil {
				err = fmt.Errorf("derived address %s has no index", p.address)
			} else {
				err = s.sweepOne(ctx, p.depositID, uint32(*p.index), p.address)
			}
		}
		if err != nil {
			slog.Error("base: sweep failed", "deposit", p.depositID, "err", err)
			continue
		}
		swept++
	}
	return swept, nil
}

// sweepSmartAccount moves a CDP Smart Account's balance as a sponsored user
// operation.
//
// Two things are deliberately absent. No key is derived, because there is
// none here to derive -- CDP signs. And OnSpend is not called, because the
// paymaster paid: the gas recorder books what this platform spent, and
// booking somebody else's bill as ours would overstate costs by every
// sponsored sweep. The transaction hash is still recorded on the deposit, so
// the movement is traceable on chain like any other.
func (s *Sweeper) sweepSmartAccount(ctx context.Context, depositID uuid.UUID, account string) error {
	if s.SmartAccounts == nil {
		return fmt.Errorf("deposit %s sits in a smart account but CDP is not configured", depositID)
	}

	balance, err := s.Chain.USDCBalance(ctx, common.HexToAddress(account))
	if err != nil {
		return err
	}
	if balance.Cmp(big.NewInt(MinSweepMicro)) < 0 {
		_, err := s.Pool.Exec(ctx, `
			UPDATE base_deposits SET state = 'swept', swept_at = now(),
			       last_error = 'below the sweep threshold; funds remain at the deposit address'
			 WHERE id = $1 AND state = 'credited'`, depositID)
		return err
	}

	// The deposit id is the idempotency key: a retry after a lost response
	// is the same operation to CDP, not a second send of the same funds.
	//
	// The version prefix exists because CDP binds a key to the exact request
	// it first saw, and answers the same key with a different request 422
	// rather than replaying it. When the request shape changes -- as it did
	// when the smart account started being addressed in EIP-55 case -- every
	// key minted under the old shape is permanently unusable, and the deposit
	// it belongs to can never be swept. Bumping the version retires those
	// keys deliberately instead of stranding the money behind them.
	//
	// Only bump this when the request genuinely changes. It is safe here
	// because no operation under sweepKeyVersion 1 ever reached CDP: they
	// failed at account lookup, so nothing was sent and nothing can be
	// double-sent by asking again under a new key.
	idem := fmt.Sprintf("sweep:v%d:%s", sweepKeyVersion, depositID)
	txHash, err := s.SmartAccounts.SweepSmartAccount(ctx, account, s.Chain.USDC, s.Chain.Treasury,
		balance, idem)
	if err != nil {
		_, e := s.Pool.Exec(ctx,
			`UPDATE base_deposits SET last_error = $2 WHERE id = $1`, depositID, err.Error())
		if e != nil {
			return e
		}
		return err
	}

	_, err = s.Pool.Exec(ctx, `
		UPDATE base_deposits SET state = 'swept', sweep_tx_hash = $2, swept_at = now(),
		       last_error = NULL
		 WHERE id = $1 AND state = 'credited'`, depositID, txHash)
	return err
}

func (s *Sweeper) sweepOne(ctx context.Context, depositID uuid.UUID, index uint32, address string) error {
	derived, err := s.Deriver.Address(index)
	if err != nil {
		return err
	}
	// The stored address must be what the seed derives. If it is not, the seed
	// has changed: the key would not control the address, the send would fail,
	// and recording the attempt as a sweep would lose the deposit from view.
	if !strings.EqualFold(address, derived.Hex()) {
		return fmt.Errorf("%w: index %d is stored as %s but derives %s",
			ErrAddressMismatch, index, address, derived.Hex())
	}

	balance, err := s.Chain.USDCBalance(ctx, derived)
	if err != nil {
		return err
	}
	if balance.Cmp(big.NewInt(MinSweepMicro)) < 0 {
		// Not worth the gas. Marked swept so it is not retried every pass;
		// the funds stay at the address and are picked up by a later, larger
		// sweep of the same address.
		_, err := s.Pool.Exec(ctx, `
			UPDATE base_deposits SET state = 'swept', swept_at = now(),
			       last_error = 'below the sweep threshold; funds remain at the deposit address'
			 WHERE id = $1 AND state = 'credited'`, depositID)
		return err
	}

	key, err := s.Deriver.PrivateKey(index)
	if err != nil {
		return err
	}

	// Pulled with a permit rather than pushed with a transfer.
	//
	// A push has to be signed by the deposit address, and a deposit address
	// holds only USDC -- it has no ETH and nothing funds it, so the push
	// cannot pay for itself. The permit is signed off-chain for free and the
	// treasury pays to submit it. See permit.go.
	txHash, err := s.Chain.SweepWithPermit(ctx, key, balance)
	if err != nil {
		// Recorded, not marked failed: a sweep that could not be submitted is
		// retried, because the money is still at an address we control and
		// nothing about the deposit's credit is in doubt.
		_, e := s.Pool.Exec(ctx,
			`UPDATE base_deposits SET last_error = $2 WHERE id = $1`, depositID, err.Error())
		if e != nil {
			return e
		}
		return err
	}

	if _, err := s.Pool.Exec(ctx, `
		UPDATE base_deposits SET state = 'swept', sweep_tx_hash = $2, swept_at = now(),
		       last_error = NULL
		 WHERE id = $1 AND state = 'credited'`, depositID, txHash); err != nil {
		return err
	}

	if s.OnSpend != nil {
		s.OnSpend(ctx, depositID, txHash)
	}
	return nil
}

// Run sweeps on a timer.
func (s *Sweeper) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swept, err := s.Sweep(ctx)
			if err != nil {
				slog.Error("base: sweep tick failed", "err", err)
				continue
			}
			if swept > 0 {
				slog.Info("base: swept deposits into treasury", "count", swept)
			}
		}
	}
}
