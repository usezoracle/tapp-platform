// Package transactions is the operator's and the merchant's view of money
// that moved: one list, over every kind of payment the platform carries.
//
// A read model, not a table. A card tap lives in card_taps with its
// settlement and any reversal alongside; an integrator's offramp lives in
// orders. Each already says everything about itself, and copying them into a
// third table would only give the two a way to disagree. What is needed is a
// single shape to read them through, and a status vocabulary that means the
// same thing whichever kind it describes.
package transactions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/equity"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/money"
)

// ErrNotFound means no transaction has that id.
var ErrNotFound = errors.New("transactions: not found")

// Kinds of transaction.
const (
	KindTap     = "tap"     // a card tap at a merchant's till
	KindOfframp = "offramp" // an integrator selling value for a bank payout
)

// Statuses, one vocabulary for every kind.
//
//	pending     nothing has been sent anywhere yet
//	processing  money is moving: an order is on chain, a payout is in flight
//	settled     the beneficiary has been paid
//	failed      delivery was given up on; the beneficiary is still owed and an
//	            operator has to act
//	reversed    the payment was undone in full
//	refunded    could not be delivered; value went back to the sender
//	cancelled   withdrawn before anything moved
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusSettled    = "settled"
	StatusFailed     = "failed"
	StatusReversed   = "reversed"
	StatusRefunded   = "refunded"
	StatusCancelled  = "cancelled"
)

// Bank is where the beneficiary is paid.
type Bank struct {
	Institution   string
	AccountNumber string
	AccountName   string
}

// Transaction is one payment, whichever kind.
type Transaction struct {
	ID     uuid.UUID
	Kind   string
	Status string

	CreatedAt time.Time
	UpdatedAt time.Time
	SettledAt *time.Time

	// Who. Merchant is the beneficiary's id: a sender profile for a tap, a
	// user for an offramp. Cardholder is set for taps only.
	Merchant        uuid.UUID
	Cardholder      *uuid.UUID
	CardholderEmail string

	// How much. Amount is what the payer was charged, Fee the platform's
	// cut of it, Owed what the beneficiary receives.
	Amount money.Amount
	Fee    money.Amount
	Owed   money.Amount

	// The sale that pays for it, for a tap: how much USDC, and the order
	// carrying it. Round counts the orders created for this tap.
	SoldMicro int64
	Round     int
	OrderID   string
	TxHash    string
	LastError string

	Bank   Bank
	Reason string // why it was reversed, when it was

	// Equity is what the equity market did with a tap: nil when the tap was
	// never queued for it (the feature was off, or the tap was not naira).
	Equity *Equity
}

// Equity is a tap's outcome on the equity market.
//
// State is one vocabulary over two facts -- whether Freedom has been told,
// and what it did:
//
//	queued     in the outbox, not yet acknowledged by the market
//	failed     delivery was given up on; an operator has to look
//	escrowed   delivered; the merchant is not listed, so the funding accrues
//	pending    delivered; the funding waits for a session with a price
//	allocated  shares were bought: Units of Symbol at Price
//	reversed   the tap was reversed and the market has unwound it
type Equity struct {
	State  string
	Symbol string // empty when the merchant is not listed
	Units  int64  // 1e-8 of a share
	Shares string // Units as a human figure
	Price  money.Amount
}

// Equity states.
const (
	EquityQueued    = "queued"
	EquityFailed    = "failed"
	EquityEscrowed  = "escrowed"
	EquityPending   = "pending"
	EquityAllocated = "allocated"
	EquityReversed  = "reversed"
)

// equityFrom reads a tap's market outcome from its outbox rows.
func equityFrom(tapState, reverseState *string, response []byte) *Equity {
	if tapState == nil {
		return nil
	}
	e := &Equity{State: EquityQueued}
	switch *tapState {
	case equity.StateFailed:
		e.State = EquityFailed
		return e
	case equity.StatePending:
		return e
	}

	var r equity.TapResponse
	if len(response) > 0 {
		if err := json.Unmarshal(response, &r); err != nil {
			// A delivered row whose answer cannot be read is still
			// delivered; the tap's state is known even if the detail is
			// not.
			return &Equity{State: EquityPending}
		}
	}
	if r.Symbol != nil {
		e.Symbol = *r.Symbol
	}
	e.Units = r.AllocatedUnits
	e.Shares = equity.Shares(r.AllocatedUnits)
	if r.PriceKobo > 0 {
		e.Price = money.New(r.PriceKobo, money.NGN)
	}
	switch r.IntentState {
	case "allocated":
		e.State = EquityAllocated
	case "escrowed":
		e.State = EquityEscrowed
	default:
		e.State = EquityPending
	}
	if reverseState != nil && *reverseState == equity.StateDelivered {
		e.State = EquityReversed
	}
	return e
}

// Sold is the USDC sold for a tap, as a decimal string.
func (t Transaction) Sold() string {
	return fmt.Sprintf("%d.%06d", t.SoldMicro/1_000_000, t.SoldMicro%1_000_000)
}

// Filter narrows a listing.
type Filter struct {
	// Merchant restricts to one beneficiary: the sender profile for taps,
	// and the user for offramps. Either may be nil.
	MerchantProfile *uuid.UUID
	MerchantUser    *uuid.UUID
	Status          string
	Kind            string
	Since           *time.Time

	Page  int
	Limit int
}

// listSQL reads both kinds through one shape.
//
// The tap's status is decided here, from the three tables that hold it: a
// reversal wins over everything, then the settlement's own state. A tap with
// no settlement row is one the settler was never configured for, and it is
// pending in the plain sense.
const listSQL = `
WITH all_txns AS (
    SELECT t.id, 'tap' AS kind,
           t.created_at, coalesce(st.updated_at, t.created_at) AS updated_at,
           CASE WHEN st.state = 'fulfilled' THEN st.updated_at END AS settled_at,
           t.merchant_id AS merchant, t.cardholder_id AS cardholder, u.email AS cardholder_email,
           t.currency::text AS currency, t.amount_minor, t.fee_minor,
           CASE WHEN r.id IS NOT NULL THEN 'reversed'
                WHEN st.state = 'submitted' THEN 'processing'
                WHEN st.state = 'fulfilled' THEN 'settled'
                WHEN st.state = 'failed'    THEN 'failed'
                ELSE 'pending' END AS status,
           coalesce(st.sell_micro, 0) AS sold_micro, coalesce(st.round, 0) AS round,
           st.order_id, st.tx_hash, st.last_error,
           b.bank_code, b.account_number, b.account_name, r.reason,
           eq.state AS equity_state, eqr.state AS equity_reverse_state, eq.response AS equity_response
      FROM card_taps t
      LEFT JOIN card_tap_settlements st ON st.tap_id = t.id
      LEFT JOIN card_tap_reversals   r  ON r.tap_id  = t.id
      LEFT JOIN equity_outbox eq  ON eq.tap_id  = t.id AND eq.kind  = 'tap'
      LEFT JOIN equity_outbox eqr ON eqr.tap_id = t.id AND eqr.kind = 'reverse'
      LEFT JOIN users u ON u.id = t.cardholder_id
      LEFT JOIN LATERAL (
            SELECT bank_code, account_number, account_name
              FROM merchant_bank_accounts
             WHERE sender_profile_merchant_bank_account = t.merchant_id
               AND currency = t.currency::text
             ORDER BY verified_at DESC NULLS LAST
             LIMIT 1) b ON true
    UNION ALL
    SELECT o.id, 'offramp',
           o.created_at, o.updated_at, o.settled_at,
           o.sender_id, NULL, NULL,
           o.payout_currency::text, o.payout_minor, 0,
           CASE o.state::text WHEN 'converting' THEN 'pending'
                              WHEN 'paying'     THEN 'processing'
                              ELSE o.state::text END,
           0, 0, NULL, NULL, o.failure,
           o.bank_code, o.account_number, o.account_name, NULL,
           NULL, NULL, NULL
      FROM orders o
)
SELECT id, kind, created_at, updated_at, settled_at,
       merchant, cardholder, cardholder_email,
       currency, amount_minor, fee_minor, status,
       sold_micro, round, order_id, tx_hash, last_error,
       bank_code, account_number, account_name, reason,
       equity_state, equity_reverse_state, equity_response,
       count(*) OVER () AS total
  FROM all_txns
 WHERE (($1::uuid IS NULL AND $2::uuid IS NULL)
        OR (kind = 'tap'     AND merchant = $1)
        OR (kind = 'offramp' AND merchant = $2))
   AND ($3::text = '' OR status = $3)
   AND ($4::text = '' OR kind = $4)
   AND ($5::timestamptz IS NULL OR created_at >= $5)
   AND ($6::uuid IS NULL OR id = $6)
 ORDER BY created_at DESC
 LIMIT $7 OFFSET $8`

// List pages through transactions, newest first, and says how many match.
//
// A merchant is a sender profile to their taps and a user to their offramps,
// so one beneficiary is two ids; naming both lists both, naming one lists
// that kind, naming neither lists everything.
func List(ctx context.Context, q ledger.Querier, f Filter) ([]Transaction, int, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 20
	}
	if f.Page <= 0 {
		f.Page = 1
	}
	rows, err := q.Query(ctx, listSQL,
		f.MerchantProfile, f.MerchantUser, f.Status, f.Kind, f.Since, nil,
		f.Limit, (f.Page-1)*f.Limit)
	if err != nil {
		return nil, 0, fmt.Errorf("transactions: list: %w", err)
	}
	defer rows.Close()

	var (
		out   []Transaction
		total int
	)
	for rows.Next() {
		t, n, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		total = n
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// Get reads one transaction, whoever it belongs to.
func Get(ctx context.Context, q ledger.Querier, id uuid.UUID) (Transaction, error) {
	rows, err := q.Query(ctx, listSQL, nil, nil, "", "", nil, id, 1, 0)
	if err != nil {
		return Transaction{}, fmt.Errorf("transactions: get: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Transaction{}, err
		}
		return Transaction{}, ErrNotFound
	}
	t, _, err := scan(rows)
	return t, err
}

func scan(rows pgx.Rows) (Transaction, int, error) {
	var (
		t                                Transaction
		cur                              string
		amountMinor, feeMinor            int64
		email, orderID, txHash, lastErr  *string
		bankCode, accountNo, accountName *string
		reason                           *string
		eqState, eqReverseState          *string
		eqResponse                       []byte
		total                            int
	)
	if err := rows.Scan(&t.ID, &t.Kind, &t.CreatedAt, &t.UpdatedAt, &t.SettledAt,
		&t.Merchant, &t.Cardholder, &email,
		&cur, &amountMinor, &feeMinor, &t.Status,
		&t.SoldMicro, &t.Round, &orderID, &txHash, &lastErr,
		&bankCode, &accountNo, &accountName, &reason,
		&eqState, &eqReverseState, &eqResponse,
		&total); err != nil {
		return Transaction{}, 0, fmt.Errorf("transactions: scan: %w", err)
	}
	c := money.Currency(cur)
	t.Amount = money.New(amountMinor, c)
	t.Fee = money.New(feeMinor, c)
	t.Owed = money.New(amountMinor-feeMinor, c)
	t.CardholderEmail = str(email)
	t.OrderID, t.TxHash, t.LastError = str(orderID), str(txHash), str(lastErr)
	t.Bank = Bank{Institution: str(bankCode), AccountNumber: str(accountNo), AccountName: str(accountName)}
	t.Reason = str(reason)
	t.Equity = equityFrom(eqState, eqReverseState, eqResponse)
	return t, total, nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Event is one ledger movement a transaction caused.
type Event struct {
	At      time.Time
	Type    string
	Entries []Entry
}

// Entry is one leg of an event, as the audit would read it.
type Entry struct {
	Owner   string // user, merchant, system
	Account string // available, merchant_payable, revenue, external, ...
	Amount  money.Amount
	Reason  string
}

// Events lists what the ledger recorded for a transaction, oldest first.
//
// This is the timeline: the tap, the sale, a refund, the sale again, a
// reversal. Every one of them is a ledger transaction naming this id, which
// is what makes the history complete without a log table to keep in step.
func Events(ctx context.Context, q ledger.Querier, id uuid.UUID) ([]Event, error) {
	// Movements posted in one database transaction share a created_at, so
	// the order they were posted in is read from the entries' serial ids.
	rows, err := q.Query(ctx, `
		SELECT t.id, t.ref_type, t.created_at,
		       a.owner_kind::text, a.kind::text, e.currency, e.amount_minor, e.reason
		  FROM ledger_transactions t
		  JOIN ledger_entries e ON e.tx_id = t.id
		  JOIN ledger_accounts a ON a.id = e.account_id
		 WHERE t.ref_id = $1
		 ORDER BY min(e.id) OVER (PARTITION BY t.id), e.id`, id)
	if err != nil {
		return nil, fmt.Errorf("transactions: events: %w", err)
	}
	defer rows.Close()

	var (
		out  []Event
		last uuid.UUID
	)
	for rows.Next() {
		var (
			txID       uuid.UUID
			typ, owner string
			kind, cur  string
			minor      int64
			reason     string
			at         time.Time
		)
		if err := rows.Scan(&txID, &typ, &at, &owner, &kind, &cur, &minor, &reason); err != nil {
			return nil, fmt.Errorf("transactions: events: %w", err)
		}
		if txID != last {
			out = append(out, Event{At: at, Type: typ})
			last = txID
		}
		ev := &out[len(out)-1]
		ev.Entries = append(ev.Entries, Entry{
			Owner: owner, Account: kind,
			Amount: money.New(minor, money.Currency(cur)), Reason: reason,
		})
	}
	return out, rows.Err()
}

// Stats totals a beneficiary's transactions since a moment: how many, and
// what they were charged, in the currency asked for.
type Stats struct {
	Count     int
	Volume    money.Amount // sum of Amount, in c
	Fees      money.Amount // sum of Fee, in c
	SoldMicro int64        // USDC sold to pay for them
}

func StatsFor(ctx context.Context, q ledger.Querier, profile, user uuid.UUID, c money.Currency, since *time.Time) (Stats, error) {
	var s Stats
	var volume, fees int64
	err := q.QueryRow(ctx, `
		WITH mine AS (
		    SELECT t.amount_minor, t.fee_minor, coalesce(st.sell_micro, 0) AS sold, t.created_at
		      FROM card_taps t
		      LEFT JOIN card_tap_settlements st ON st.tap_id = t.id
		      LEFT JOIN card_tap_reversals r ON r.tap_id = t.id
		     WHERE t.merchant_id = $1 AND t.currency::text = $3 AND r.id IS NULL
		    UNION ALL
		    SELECT o.payout_minor, 0, 0, o.created_at
		      FROM orders o
		     WHERE o.sender_id = $2 AND o.payout_currency::text = $3
		       AND o.state NOT IN ('refunded', 'cancelled')
		)
		SELECT count(*), coalesce(sum(amount_minor), 0), coalesce(sum(fee_minor), 0), coalesce(sum(sold), 0)
		  FROM mine
		 WHERE $4::timestamptz IS NULL OR created_at >= $4`,
		profile, user, string(c), since).Scan(&s.Count, &volume, &fees, &s.SoldMicro)
	if err != nil {
		return Stats{}, fmt.Errorf("transactions: stats: %w", err)
	}
	s.Volume, s.Fees = money.New(volume, c), money.New(fees, c)
	return s, nil
}
