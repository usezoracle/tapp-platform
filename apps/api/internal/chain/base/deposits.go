package base

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

// USDCDecimals is what USDC uses on chain. The ledger's USD minor unit is
// cents, so a conversion happens once, at credit time, and nowhere else.
const USDCDecimals = 6

// Confirmations before a deposit is credited.
//
// Base is an L2 with fast blocks and a sequencer that can reorg. Crediting on
// the first sighting would mean crediting deposits that later never happened,
// and the money would already have been spent by then.
const DefaultConfirmations = 12

// Transfer is one observed USDC movement into a deposit address.
type Transfer struct {
	TxHash      string
	LogIndex    uint64
	From        string
	To          string
	AmountMicro int64
	BlockNumber uint64
}

// Deposits records observed transfers and credits confirmed ones.
type Deposits struct {
	Pool          *pgxpool.Pool
	Addresses     *Addresses
	Confirmations uint64

	// Treasury is the platform's own address. Transfers FROM it into a
	// deposit address are returns, not deposits, and crediting them counts
	// the same money twice. Zero disables the check.
	Treasury common.Address

	// Gateway is the settlement contract a card tap sells USDC to. An order
	// nobody fills is refunded from it to the cardholder's account, and that
	// arrival is not a deposit either: the tap already spent this money, and
	// the settler will sell it again or the merchant is owed it. See Record.
	Gateway common.Address
}

func (d *Deposits) confirmations() uint64 {
	if d.Confirmations == 0 {
		return DefaultConfirmations
	}
	return d.Confirmations
}

// Record notes a transfer that has been seen on chain.
//
// Idempotent on (tx_hash, log_index), which is the chain's own identity for
// the event. That is what makes a restarted watcher safe: re-scanning blocks
// it already processed finds the rows and does nothing, rather than crediting
// somebody twice for one payment.
func (d *Deposits) Record(ctx context.Context, t Transfer) error {
	user, _, err := d.Addresses.Owner(ctx, t.To)
	if errors.Is(err, pgx.ErrNoRows) {
		// Not one of ours. Somebody else's transfer in a block we scanned.
		return nil
	}
	if err != nil {
		return err
	}
	if t.AmountMicro <= 0 {
		return nil
	}

	// Money coming back from our own treasury is not a deposit.
	//
	// A deposit address credits whatever arrives at it, which is right for a
	// stranger paying somebody and wrong for us returning what we took. When
	// funds swept before the platform went non-custodial were sent back to
	// their owners, the watcher saw ordinary USDC transfers into watched
	// addresses and credited them a second time -- the same money counted
	// twice, once when it arrived and once when it was returned.
	//
	// Comparing the sender is the whole guard: nobody else's payment can
	// arrive from an address whose key we hold.
	//
	// The Gateway is the other such address. A refund from it is the
	// cardholder's own USDC coming back from an order a provider declined;
	// crediting it would hand them a second balance for money the ledger
	// already charged at the till, on top of the merchant they still owe.
	if d.Treasury != (common.Address{}) &&
		strings.EqualFold(t.From, d.Treasury.Hex()) {
		return nil
	}
	if d.Gateway != (common.Address{}) &&
		strings.EqualFold(t.From, d.Gateway.Hex()) {
		return nil
	}

	_, err = d.Pool.Exec(ctx, `
		INSERT INTO base_deposits
			(user_id, tx_hash, log_index, from_address, to_address, amount_micro, block_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tx_hash, log_index) DO NOTHING`,
		user, strings.ToLower(t.TxHash), t.LogIndex,
		strings.ToLower(t.From), strings.ToLower(t.To), t.AmountMicro, t.BlockNumber)
	if err != nil {
		return fmt.Errorf("base: record deposit: %w", err)
	}
	return nil
}

// CreditConfirmed posts every deposit that now has enough confirmations.
//
// The ledger entry and the state change commit together. A deposit marked
// credited without its entry would be money the person never received; an
// entry without the mark would be credited again on the next pass.
func (d *Deposits) CreditConfirmed(ctx context.Context, head uint64) (int, error) {
	minConfirmations := d.confirmations()
	if head < minConfirmations {
		return 0, nil
	}
	safeBlock := head - minConfirmations

	rows, err := d.Pool.Query(ctx, `
		SELECT id, user_id, amount_micro, tx_hash, log_index
		  FROM base_deposits
		 WHERE state = 'seen' AND block_number <= $1
		 LIMIT 200`, safeBlock)
	if err != nil {
		return 0, fmt.Errorf("base: find confirmed deposits: %w", err)
	}

	type pending struct {
		id     uuid.UUID
		user   uuid.UUID
		micro  int64
		txHash string
		logIdx int64
	}
	var due []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.user, &p.micro, &p.txHash, &p.logIdx); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	credited := 0
	for _, p := range due {
		amount := usdFromMicro(p.micro)
		if !amount.IsPositive() {
			// Less than a cent. Recording it as credited with no entry would
			// be a lie; leaving it seen forever would retry it every pass.
			if _, err := d.Pool.Exec(ctx, `
				UPDATE base_deposits SET state = 'failed', last_error = $2 WHERE id = $1`,
				p.id, "amount is below one cent"); err != nil {
				return credited, err
			}
			continue
		}

		reference := fmt.Sprintf("%s:%d", p.txHash, p.logIdx)
		err := movements.InTx(ctx, d.Pool, func(tx pgx.Tx) error {
			ledgerTx, err := movements.Deposit(ctx, tx, p.user, amount, "base", reference)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				UPDATE base_deposits
				   SET state = 'credited', ledger_tx_id = $2, credited_at = now()
				 WHERE id = $1 AND state = 'seen'`, p.id, ledgerTx)
			return err
		})
		if err != nil {
			return credited, fmt.Errorf("base: credit deposit %s: %w", p.id, err)
		}
		credited++
	}
	return credited, nil
}

// usdFromMicro converts USDC's six decimals to the ledger's cents.
//
// Truncating rather than rounding, deliberately: rounding up would credit
// somebody a cent that never arrived, and across enough deposits that is money
// the platform has to find from somewhere.
func usdFromMicro(micro int64) money.Amount {
	const microPerCent = 10_000 // 1e6 / 1e2
	return money.New(micro/microPerCent, money.USD)
}
