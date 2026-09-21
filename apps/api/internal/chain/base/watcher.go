package base

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgxpool"
)

// transferTopic is keccak256("Transfer(address,address,uint256)"), the first
// topic of every ERC-20 transfer log.
var transferTopic = common.HexToHash(
	"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

// MaxBlockSpan bounds one query.
//
// Providers cap how many blocks a log filter may cover and answer with an
// error rather than a partial result, so a watcher that has been down for a
// day has to catch up in steps. Without this it would ask for a hundred
// thousand blocks, be refused, and never make progress.
const MaxBlockSpan = 2_000

// Watcher reads USDC transfers into deposit addresses.
type Watcher struct {
	Pool     *pgxpool.Pool
	Client   *ethclient.Client
	USDC     common.Address
	Deposits *Deposits

	// StartBlock is where to begin when there is no recorded position.
	//
	// Zero means "start at the current head", NOT genesis. The previous note
	// here assumed a provider would refuse a filter starting at block 0 and
	// so the mistake would announce itself. It does not refuse: it serves the
	// range, and the watcher walks the whole chain MaxBlockSpan at a time. On
	// Base that is roughly four days of scanning ancient blocks while every
	// real deposit sits unread, with nothing in the logs to say so.
	//
	// Head is the safe default because a deployment with no recorded position
	// has issued no addresses, so there is nothing behind it to find. An
	// operator who genuinely wants history sets an explicit block.
	StartBlock uint64
}

// CatchUpDelay paces polls while the watcher is behind.
//
// The poll interval exists to avoid hammering the RPC when there is nothing to
// do. While catching up there IS something to do, and waiting the full
// interval between consecutive MaxBlockSpan windows is what turns a gap into
// days. Base produces 30 blocks a minute; 2,000 blocks per 15s clears 8,000,
// so the watcher does converge -- at about 8,000 blocks a minute, which is
// four days across a 49-million-block gap. The timer, not the work, is the
// cost. Polling straight on makes catch-up bound by the provider instead.
const CatchUpDelay = 250 * time.Millisecond

// LagReportEvery throttles the "behind" warning so catching up does not
// produce a log line per poll.
const LagReportEvery = 30 * time.Second

// Poll reads any new blocks and records the transfers it finds.
//
// Two phases, deliberately separate: observing a transfer and crediting it are
// different decisions, and a transfer is only credited once it has enough
// blocks on top of it. Base is an L2 that can reorg, and a credited deposit
// that later never happened is money already spent.
func (w *Watcher) Poll(ctx context.Context) (found, credited int, lag uint64, err error) {
	head, err := w.Client.BlockNumber(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("base: read head: %w", err)
	}

	from, err := w.position(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	if from == 0 {
		from = w.StartBlock
		if from == 0 {
			// Never genesis. See StartBlock.
			from = head
			slog.Warn("base: no watcher position and no start block configured; "+
				"beginning at the current head",
				"head", head,
				"hint", "set BASE_START_BLOCK to scan history deliberately")
		}
	}
	if from > head {
		// Ahead of the chain. Either the provider is serving a stale head or
		// this position came from a different network; neither is something to
		// scan through.
		return 0, 0, 0, nil
	}

	to := head
	if to-from > MaxBlockSpan {
		to = from + MaxBlockSpan
	}

	// Ask only for transfers TO an address we issued.
	//
	// Filtering after the fact instead is what this used to do, and it works
	// on a quiet testnet. On mainnet, USDC is among the busiest contracts on
	// the chain: a few hundred blocks is tens of thousands of logs, and the
	// public RPC simply refuses -- "backend response too large" -- so the
	// watcher makes no progress at all and no deposit is ever credited.
	//
	// The third topic of an ERC-20 Transfer is the recipient, and a log filter
	// takes a set of accepted values per topic, so the node can do this far
	// more cheaply than we can. The per-log ownership check below stays: this
	// narrows what we fetch, it does not decide what we credit.
	watched, err := w.watchedTopics(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(watched) == 0 {
		// Nothing issued yet. Advance so the position does not fall behind the
		// head while the first person is signing up.
		if err := w.advance(ctx, to+1); err != nil {
			return 0, 0, 0, err
		}
		return 0, 0, head - to, nil
	}

	logs, err := w.Client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(from),
		ToBlock:   new(big.Int).SetUint64(to),
		Addresses: []common.Address{w.USDC},
		Topics:    [][]common.Hash{{transferTopic}, nil, watched},
	})
	if err != nil {
		return 0, 0, 0, fmt.Errorf("base: read logs %d-%d: %w", from, to, err)
	}

	for _, entry := range logs {
		transfer, ok := decodeTransfer(entry)
		if !ok {
			continue
		}
		if err := w.Deposits.Record(ctx, transfer); err != nil {
			// Stop rather than skip. Advancing the position past a transfer
			// that failed to record would lose it permanently.
			return found, 0, 0, err
		}
		found++
	}

	// The position advances only after every log in the range is recorded.
	if err := w.advance(ctx, to+1); err != nil {
		return found, 0, 0, err
	}

	credited, err = w.Deposits.CreditConfirmed(ctx, head)
	return found, credited, head - to, err
}

// decodeTransfer reads an ERC-20 Transfer log.
//
// Amounts beyond int64 are skipped rather than truncated. A USDC transfer of
// more than ninety trillion dollars is not a deposit, and silently wrapping it
// into a small positive number would be the worst possible handling.
func decodeTransfer(entry types.Log) (Transfer, bool) {
	if len(entry.Topics) != 3 || len(entry.Data) < 32 {
		return Transfer{}, false
	}
	amount := new(big.Int).SetBytes(entry.Data[:32])
	if !amount.IsInt64() || amount.Sign() <= 0 {
		return Transfer{}, false
	}

	return Transfer{
		TxHash:      strings.ToLower(entry.TxHash.Hex()),
		LogIndex:    uint64(entry.Index),
		From:        strings.ToLower(common.BytesToAddress(entry.Topics[1].Bytes()).Hex()),
		To:          strings.ToLower(common.BytesToAddress(entry.Topics[2].Bytes()).Hex()),
		AmountMicro: amount.Int64(),
		BlockNumber: entry.BlockNumber,
	}, true
}

// watchedTopics is every deposit address we have issued, as topic values.
//
// EVERY address, retired ones included. This filter is the only thing that
// decides which transfers are fetched at all, so an address left out of it is
// an address whose deposits produce no row, no log line and no alert -- the
// money leaves the sender's bank and this system never hears about it. People
// keep old addresses saved as payees long after they stop being handed out,
// so "no longer advertised" must never mean "no longer watched".
//
// Re-read each poll rather than cached: an address issued a moment ago must be
// watched on the very next pass, or somebody who funds it immediately waits an
// unbounded time to be credited.
func (w *Watcher) watchedTopics(ctx context.Context) ([]common.Hash, error) {
	rows, err := w.Pool.Query(ctx, `SELECT address FROM base_deposit_addresses`)
	if err != nil {
		return nil, fmt.Errorf("base: read watched addresses: %w", err)
	}
	defer rows.Close()

	var out []common.Hash
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, fmt.Errorf("base: read watched addresses: %w", err)
		}
		out = append(out, common.HexToHash(common.HexToAddress(addr).Hex()))
	}
	return out, rows.Err()
}

func (w *Watcher) position(ctx context.Context) (uint64, error) {
	var last int64
	if err := w.Pool.QueryRow(ctx,
		`SELECT last_block FROM base_watcher_state WHERE id = true`).Scan(&last); err != nil {
		return 0, fmt.Errorf("base: read watcher position: %w", err)
	}
	return uint64(last), nil
}

func (w *Watcher) advance(ctx context.Context, to uint64) error {
	_, err := w.Pool.Exec(ctx,
		`UPDATE base_watcher_state SET last_block = $1, updated_at = now() WHERE id = true`, to)
	if err != nil {
		return fmt.Errorf("base: advance watcher position: %w", err)
	}
	return nil
}

// Run polls until the context ends.
//
// Two things here are load-bearing, and both were learned the hard way when a
// watcher sat 49 million blocks behind for days without a word.
//
// It polls straight on while behind, rather than once per interval. The
// interval is there to avoid pointless RPC calls when the chain has produced
// nothing new; while catching up every call is useful, and pausing between
// them means falling further behind than the ground gained.
//
// It says when it is behind. Reporting only what it FOUND made a watcher that
// was looking in the wrong place indistinguishable from one that was looking
// in the right place and finding nothing -- the service looked healthy, the
// logs were clean, and the only symptom was a customer saying their money had
// not arrived.
func (w *Watcher) Run(ctx context.Context, every time.Duration) {
	var lastLagReport time.Time

	for {
		found, credited, lag, err := w.Poll(ctx)

		switch {
		case err != nil:
			slog.Error("base: poll failed", "err", err)
		default:
			if found > 0 || credited > 0 {
				slog.Info("base: deposits", "seen", found, "credited", credited)
			}
			// Behind by more than one window means catching up rather than
			// keeping up. Throttled, because catch-up is many polls.
			if lag > MaxBlockSpan && time.Since(lastLagReport) >= LagReportEvery {
				slog.Warn("base: watcher is behind the chain",
					"blocks_behind", lag,
					"note", "deposits in these blocks are not credited yet")
				lastLagReport = time.Now()
			}
		}

		delay := every
		if err == nil && lag > 0 {
			delay = CatchUpDelay
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
