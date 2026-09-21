package base

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

var (
	// ErrCannotSend means there is no way to move tokens out: neither a
	// signer for the holder's own account nor a treasury key.
	ErrCannotSend = errors.New("base: withdrawals are not configured")
	// ErrBadAddress means the destination is not a valid address.
	ErrBadAddress = errors.New("base: that is not a valid Base address")
)

// Withdrawals move USDC out of the treasury to a user's own address.
//
// The ledger is debited FIRST, in its own transaction, before anything is sent
// on chain. That ordering is deliberate and it is the safe one: a debit with
// no send is money we still hold and can return, whereas a send with no debit
// is money gone that nobody paid for. When the send fails, the debit is
// reversed.
// SmartAccountSender moves tokens out of a smart account. Satisfied by
// cdp.Client.
type SmartAccountSender interface {
	SweepSmartAccount(
		ctx context.Context, account string, usdc, to common.Address,
		amount *big.Int, idem string,
	) (string, error)
}

type Withdrawals struct {
	Pool  *pgxpool.Pool
	Chain *Chain

	// Addresses finds the account a user's funds actually sit in.
	Addresses *Addresses

	// SmartAccounts sends from that account, sponsored.
	//
	// Withdrawals used to be paid out of the treasury, because everything was
	// swept into it. Nothing is swept now: a person's USDC stays at their own
	// deposit address, and paying them from a treasury that no longer holds
	// customer funds would fail with nothing to send. Nil falls back to the
	// treasury, which is right only for a deployment that still pools.
	SmartAccounts SmartAccountSender
}

// canSend reports whether a withdrawal has any way out at all.
//
// Either route will do: the person's own smart account, or the treasury for a
// deployment that still pools. Requiring a treasury key specifically -- which
// this did -- refuses withdrawals a non-custodial deployment can make
// perfectly well, because it holds no customer funds to need a key for.
func (w *Withdrawals) canSend() bool {
	if w.SmartAccounts != nil && w.Addresses != nil {
		return true
	}
	return w.Chain != nil && w.Chain.CanSend()
}

// send moves the USDC, from wherever the person's money actually is.
//
// Their own smart account when there is one, sponsored, so a withdrawal costs
// them no gas and the platform never has to hold their funds to pay them. The
// treasury is the fallback for a deployment that still pools.
//
// The withdrawal id is the idempotency key. A retry after a lost response is
// the same withdrawal to CDP, not a second send of the same money.
func (w *Withdrawals) send(
	ctx context.Context, id, user uuid.UUID, micro int64, to string,
) (string, error) {
	dst := common.HexToAddress(to)
	amount := big.NewInt(micro)

	if w.SmartAccounts != nil && w.Addresses != nil {
		from, _, ok, err := w.Addresses.Current(ctx, w.Pool, user)
		if err != nil {
			return "", err
		}
		if ok {
			return w.SmartAccounts.SweepSmartAccount(
				ctx, from, w.Chain.USDC, dst, amount, "withdrawal:"+id.String())
		}
		// No current address means nowhere of theirs to send from. Falling
		// through to the treasury would pay them out of pooled funds they may
		// have no claim on beyond the ledger, so it is refused instead.
		return "", fmt.Errorf("base: %s has no deposit address to withdraw from", user)
	}

	return w.Chain.SendUSDC(ctx, w.Chain.TreasuryKey(), dst, amount)
}

// Request is a user asking to move USDC out.
type Request struct {
	UserID uuid.UUID
	Amount money.Amount
	// To is the destination address. Checked for shape only -- nothing can
	// verify that somebody controls an address they name, which is why the
	// client is expected to confirm it with them before this is called.
	To string
}

// Open debits the user and queues the send.
func (w *Withdrawals) Open(ctx context.Context, req Request) (uuid.UUID, error) {
	if !w.canSend() {
		return uuid.Nil, ErrCannotSend
	}
	if !common.IsHexAddress(req.To) {
		return uuid.Nil, ErrBadAddress
	}
	if req.Amount.Currency() != money.USD {
		return uuid.Nil, fmt.Errorf("base: only USD can be withdrawn as USDC, got %s",
			req.Amount.Currency())
	}
	if !req.Amount.IsPositive() {
		return uuid.Nil, fmt.Errorf("base: a withdrawal must be positive, got %s", req.Amount)
	}

	id := uuid.New()
	err := movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
		if _, err := movements.Withdraw(ctx, tx, req.UserID, req.Amount,
			money.Zero(money.USD), id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO base_withdrawals (id, user_id, amount_micro, to_address)
			VALUES ($1, $2, $3, $4)`,
			id, req.UserID, microFromUSD(req.Amount), strings.ToLower(req.To))
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Send submits queued withdrawals.
func (w *Withdrawals) Send(ctx context.Context) (int, error) {
	if !w.canSend() {
		return 0, nil
	}

	rows, err := w.Pool.Query(ctx, `
		UPDATE base_withdrawals SET state = 'sending', updated_at = now()
		 WHERE id IN (
			SELECT id FROM base_withdrawals
			 WHERE state = 'pending'
			 ORDER BY created_at
			 FOR UPDATE SKIP LOCKED
			 LIMIT 20)
		RETURNING id, user_id, amount_micro, to_address`)
	if err != nil {
		return 0, fmt.Errorf("base: claim withdrawals: %w", err)
	}

	type pending struct {
		id    uuid.UUID
		user  uuid.UUID
		micro int64
		to    string
	}
	var due []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.user, &p.micro, &p.to); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	sent := 0
	for _, p := range due {
		txHash, err := w.send(ctx, p.id, p.user, p.micro, p.to)
		if err != nil {
			// The send did not happen, so the debit must not stand. Returning
			// it here rather than leaving the user short is the whole reason
			// the ledger is debited first and separately.
			if rerr := w.refund(ctx, p.id, p.user, p.micro, err.Error()); rerr != nil {
				slog.Error("base: could not refund a failed withdrawal",
					"withdrawal", p.id, "err", rerr)
			}
			continue
		}

		if _, err := w.Pool.Exec(ctx, `
			UPDATE base_withdrawals SET state = 'sent', tx_hash = $2, sent_at = now(),
			       updated_at = now()
			 WHERE id = $1`, p.id, txHash); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

func (w *Withdrawals) refund(ctx context.Context, id, user uuid.UUID, micro int64, reason string) error {
	return movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
		if _, err := movements.Returned(ctx, tx, user,
			money.New(micro/10_000, money.USD), id, reason); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE base_withdrawals SET state = 'failed', last_error = $2, updated_at = now()
			 WHERE id = $1`, id, reason)
		return err
	})
}

// Run submits withdrawals on a timer.
func (w *Withdrawals) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sent, err := w.Send(ctx)
			if err != nil {
				slog.Error("base: withdrawal tick failed", "err", err)
				continue
			}
			if sent > 0 {
				slog.Info("base: sent withdrawals", "count", sent)
			}
		}
	}
}

// microFromUSD converts the ledger's cents to USDC's six decimals.
func microFromUSD(a money.Amount) int64 { return a.Minor() * 10_000 }
