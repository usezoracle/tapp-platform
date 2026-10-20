package offramp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/equity"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
	"github.com/usezoracle/tapp/api/services/onswitch"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://tapp:tapp@localhost:5433/tapp?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no test database (%v); start it with `docker compose up -d postgres`", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database (%v); start it with `docker compose up -d postgres`", err)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	// merchant_bank_accounts is ent's, not a migration's, and Tick joins on
	// it. The columns the query reads are enough.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS merchant_bank_accounts (
			id uuid PRIMARY KEY, created_at timestamptz, updated_at timestamptz,
			currency text, bank_code text, account_number text, account_name text,
			verified_at timestamptz, sender_profile_merchant_bank_account uuid)`); err != nil {
		pool.Close()
		t.Fatalf("merchant_bank_accounts: %v", err)
	}
	// Track and Tick read every row there is, so a row another test left
	// behind would be resolved by this one, against this one's fakes.
	if _, err := pool.Exec(ctx, `DELETE FROM card_tap_settlements`); err != nil {
		pool.Close()
		t.Fatalf("clear settlements: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func switchSettler(pool *pgxpool.Pool) (*Settler, *fakeSwitch, *fakeSender) {
	sw, s := newSwitch(), &fakeSender{}
	return &Settler{Pool: pool, Orders: newClient(s, sw)}, sw, s
}

// pendingTap is a tap that has been charged and recorded for settlement, with
// a verified bank, and nothing sold yet.
func pendingTap(t *testing.T, pool *pgxpool.Pool, s *Settler) (tap, cardholderID, merchant uuid.UUID, owed money.Amount) {
	t.Helper()
	ctx := context.Background()
	tap, cardholderID, merchant = uuid.New(), uuid.New(), uuid.New()
	amount := money.Naira(1_600)
	fee := money.FeeFor(amount, 50)
	owed, _ = amount.Sub(fee)

	if _, err := movements.Deposit(ctx, pool, cardholderID, amount, "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	err := movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		ledgerTx, err := movements.Tap(ctx, tx, cardholderID, merchant, amount, fee, tap)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO card_taps (id, card_id, cardholder_id, merchant_id, currency,
			                       amount_minor, fee_minor, tier, funded_usdc_minor, ledger_tx_id, nonce)
			VALUES ($1, $2, $3, $4, 'NGN', $5, $6, 'none', $5, $7, $8)`,
			tap, uuid.New(), cardholderID, merchant, amount.Minor(), fee.Minor(), ledgerTx, uuid.NewString()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO merchant_bank_accounts (id, currency, bank_code, account_number, account_name,
			                                    verified_at, sender_profile_merchant_bank_account, created_at, updated_at)
			VALUES ($1, 'NGN', 'OPAYNGPC', '9034409271', 'OLUMIDE SILAS OGUNDELE', now(), $2, now(), now())`,
			uuid.New(), merchant); err != nil {
			return err
		}
		return s.Record(ctx, tx, tap, cardholder, 1_208_679, owed)
	})
	if err != nil {
		t.Fatalf("pending tap: %v", err)
	}
	return tap, cardholderID, merchant, owed
}

// submittedTap is a tap whose round-0 payout Switch has opened and the
// cardholder has funded, with the merchant's claim discharged.
func submittedTap(t *testing.T, pool *pgxpool.Pool, s *Settler, sw *fakeSwitch, status string) (tap, merchant uuid.UUID, owed money.Amount) {
	t.Helper()
	ctx := context.Background()
	tap, _, merchant, owed = pendingTap(t, pool, s)
	ref := reference(tap, 0)
	if _, err := sw.Initiate(ctx, onswitch.InitiateRequest{
		QuoteRequest: onswitch.QuoteRequest{Amount: 1592, Currency: "NGN"},
		Reference:    ref,
	}); err != nil {
		t.Fatal(err)
	}
	sw.initiated = nil
	sw.settle(ref, status)
	err := movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE card_tap_settlements
			   SET state = 'submitted', tx_hash = $2, order_id = $3, attempts = 1
			 WHERE tap_id = $1`, tap, fundingTx, ref); err != nil {
			return err
		}
		_, err := movements.MerchantSettledOnChain(ctx, tx, merchant, owed, tap, 0)
		return err
	})
	if err != nil {
		t.Fatalf("submitted tap: %v", err)
	}
	return tap, merchant, owed
}

func settlementRow(t *testing.T, pool *pgxpool.Pool, tap uuid.UUID) (state string, orderID, txHash, lastError *string, round int) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		SELECT state, order_id, tx_hash, last_error, round
		  FROM card_tap_settlements WHERE tap_id = $1`, tap).
		Scan(&state, &orderID, &txHash, &lastError, &round)
	if err != nil {
		t.Fatalf("settlement row: %v", err)
	}
	return
}

func owedTo(t *testing.T, pool *pgxpool.Pool, merchant uuid.UUID) money.Amount {
	t.Helper()
	b, err := ledger.Balance(context.Background(), pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, money.NGN)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	return b
}

func TestATapIsSoldAsOnePayoutRecordedBeforeItIsFunded(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s, sw, sender := switchSettler(pool)
	tap, _, merchant, owed := pendingTap(t, pool, s)

	if got := owedTo(t, pool, merchant); got.Minor() != owed.Minor() {
		t.Fatalf("merchant owed %s before the sale, want %s", got, owed)
	}
	created, err := s.Tick(ctx)
	if err != nil || created != 1 {
		t.Fatalf("Tick: created=%d err=%v", created, err)
	}

	state, orderID, txHash, _, _ := settlementRow(t, pool, tap)
	if state != "submitted" || orderID == nil || *orderID != tap.String() || txHash == nil || *txHash != fundingTx {
		t.Errorf("row = %s order %v tx %v, want submitted under the tap id with the funding tx", state, orderID, txHash)
	}
	var sell int64
	if err := pool.QueryRow(ctx, `SELECT sell_micro FROM card_tap_settlements WHERE tap_id = $1`, tap).Scan(&sell); err != nil {
		t.Fatal(err)
	}
	if sell != 1_194_844 {
		t.Errorf("row says %d was sold, want what Switch priced, 1194844", sell)
	}
	var address string
	var paid int64
	if err := pool.QueryRow(ctx, `
		SELECT deposit_address, sell_micro FROM offramp_payments WHERE reference = $1`, tap.String()).
		Scan(&address, &paid); err != nil {
		t.Fatalf("offramp_payments: %v", err)
	}
	if address != depositTo || paid != 1_194_844 {
		t.Errorf("payout recorded as %d to %s", paid, address)
	}
	if sender.sent != 1 || len(sw.initiated) != 1 {
		t.Errorf("sent %d transfers and opened %d payouts, want one of each", sender.sent, len(sw.initiated))
	}
	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Errorf("merchant owed %s after the sale, want nothing", got)
	}
}

// A transfer that fails moves no money and costs an attempt; the next tick
// funds the payout already opened rather than opening a second.
func TestAFailedTransferIsRetriedOnTheSamePayout(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s, sw, sender := switchSettler(pool)
	tap, _, _, _ := pendingTap(t, pool, s)

	sender.err = errors.New("paymaster unavailable")
	if created, err := s.Tick(ctx); err != nil || created != 0 {
		t.Fatalf("Tick: created=%d err=%v, want nothing submitted", created, err)
	}
	state, _, _, lastError, _ := settlementRow(t, pool, tap)
	if state != "pending" || lastError == nil || !strings.Contains(*lastError, "paymaster") {
		t.Errorf("row = %s (%v), want pending with the cause", state, lastError)
	}

	sender.err = nil
	if created, err := s.Tick(ctx); err != nil || created != 1 {
		t.Fatalf("second Tick: created=%d err=%v", created, err)
	}
	if len(sw.initiated) != 1 {
		t.Errorf("opened %d payouts for one tap", len(sw.initiated))
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT attempts FROM card_tap_settlements WHERE tap_id = $1`, tap).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

// A payout found already past waiting was funded by an attempt whose answer
// was lost. Sending again would pay Switch twice.
func TestAPayoutAlreadyFundedIsNotFundedAgain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s, sw, sender := switchSettler(pool)
	tap, _, _, _ := pendingTap(t, pool, s)
	if _, err := sw.Initiate(ctx, onswitch.InitiateRequest{
		QuoteRequest: onswitch.QuoteRequest{Amount: 1592, Currency: "NGN"},
		Reference:    tap.String(),
	}); err != nil {
		t.Fatal(err)
	}
	sw.settle(tap.String(), onswitch.StatusProcessing)

	if created, err := s.Tick(ctx); err != nil || created != 1 {
		t.Fatalf("Tick: created=%d err=%v", created, err)
	}
	if sender.sent != 0 {
		t.Errorf("sent %d transfers for a payout already funded", sender.sent)
	}
	if state, _, _, _, _ := settlementRow(t, pool, tap); state != "submitted" {
		t.Errorf("state = %s, want submitted for the tracker to follow", state)
	}
}

func TestARefundedPayoutPutsTheMerchantsClaimBackAndSellsAgain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s, sw, sender := switchSettler(pool)
	tap, merchant, owed := submittedTap(t, pool, s, sw, onswitch.StatusFailed)

	resolved, err := s.Track(ctx)
	if err != nil || resolved != 1 {
		t.Fatalf("Track: resolved=%d err=%v", resolved, err)
	}
	state, orderID, txHash, lastError, round := settlementRow(t, pool, tap)
	if state != "pending" || round != 1 {
		t.Errorf("row = %s round %d, want pending round 1", state, round)
	}
	if orderID != nil || txHash != nil {
		t.Errorf("row still names payout %v tx %v after its refund", orderID, txHash)
	}
	if lastError == nil || *lastError != "round 0 ("+tap.String()+"): Switch reports FAILED" {
		t.Errorf("last_error = %v", lastError)
	}
	if got := owedTo(t, pool, merchant); got.Minor() != owed.Minor() {
		t.Errorf("merchant owed %s after the refund, want %s", got, owed)
	}
	if resolved, err := s.Track(ctx); err != nil || resolved != 0 {
		t.Errorf("second Track: resolved=%d err=%v, want 0 and nil", resolved, err)
	}

	s.Now = func() time.Time { return time.Now() }
	if created, err := s.Tick(ctx); err != nil || created != 0 {
		t.Errorf("Tick straight after the refund: created=%d err=%v, want 0 (RetryDelay)", created, err)
	}
	s.Now = func() time.Time { return time.Now().Add(RetryDelay + time.Second) }
	if created, err := s.Tick(ctx); err != nil || created != 1 {
		t.Fatalf("Tick after the delay: created=%d err=%v", created, err)
	}
	next := reference(tap, 1)
	if next == tap.String() || uuid.Validate(next) != nil {
		t.Fatalf("round 1 reference %q, want a UUID of its own", next)
	}
	if len(sw.initiated) != 1 || sw.initiated[0].Reference != next || sender.idem != "offramp:"+next {
		t.Errorf("round 1 opened %v and sent as %q; the old reference would return the refunded payout",
			sw.initiated, sender.idem)
	}
	state, orderID, _, _, round = settlementRow(t, pool, tap)
	if state != "submitted" || round != 1 || orderID == nil || *orderID != next {
		t.Errorf("row = %s round %d order %v after the resale", state, round, orderID)
	}
	var rounds int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM offramp_payments WHERE tap_id = $1`, tap).Scan(&rounds); err != nil {
		t.Fatal(err)
	}
	if rounds != 1 {
		t.Errorf("offramp_payments holds %d rows, want round 1's", rounds)
	}
	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Errorf("merchant owed %s after the resale, want nothing", got)
	}
}

func TestACompletedPayoutIsSimplyDone(t *testing.T) {
	pool := testPool(t)
	s, sw, _ := switchSettler(pool)
	tap, merchant, _ := submittedTap(t, pool, s, sw, onswitch.StatusCompleted)

	if _, err := s.Track(context.Background()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	state, orderID, _, _, _ := settlementRow(t, pool, tap)
	if state != "fulfilled" || orderID == nil || *orderID != tap.String() {
		t.Errorf("row = %s order %v, want fulfilled under the tap id", state, orderID)
	}
	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Errorf("merchant owed %s after the payout completed", got)
	}
}

// The merchant being paid is what earns the fee that buys the cardholder's
// shares, so completion is what releases the tap to the equity market.
func TestACompletedPayoutReleasesTheTapToTheMarket(t *testing.T) {
	pool := testPool(t)
	s, sw, _ := switchSettler(pool)
	tap, _, _ := submittedTap(t, pool, s, sw, onswitch.StatusCompleted)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO equity_outbox (kind, tap_id, state, payload) VALUES ('tap', $1, 'held', '{}')`, tap); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Track(context.Background()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	var state string
	if err := pool.QueryRow(context.Background(),
		`SELECT state FROM equity_outbox WHERE tap_id = $1 AND kind = 'tap'`, tap).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != equity.StateQueued {
		t.Errorf("outbox row = %s after completion, want queued", state)
	}
}

func TestAPayoutBeingPaidIsLeftAlone(t *testing.T) {
	pool := testPool(t)
	s, sw, _ := switchSettler(pool)
	tap, merchant, _ := submittedTap(t, pool, s, sw, onswitch.StatusProcessing)

	resolved, err := s.Track(context.Background())
	if err != nil || resolved != 0 {
		t.Fatalf("Track: resolved=%d err=%v, want 0 and nil", resolved, err)
	}
	if state, _, _, _, _ := settlementRow(t, pool, tap); state != "submitted" {
		t.Errorf("state = %s, want submitted", state)
	}
	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Errorf("merchant owed %s while the payout is being paid", got)
	}
}

// A row the settlement gateway left submitted cannot be asked of Switch; it
// is said and left for an operator, never resolved by guessing.
func TestASettlementWithNoSwitchReferenceIsLeftForAnOperator(t *testing.T) {
	pool := testPool(t)
	s, sw, _ := switchSettler(pool)
	tap, merchant, _ := submittedTap(t, pool, s, sw, onswitch.StatusFailed)
	if _, err := pool.Exec(context.Background(), `
		UPDATE card_tap_settlements SET order_id = $2 WHERE tap_id = $1`,
		tap, "0x8a671d57f83991599aefdf9f849b8d3ebdd9c8f07f0415e9ae8e5bb7c95309bc"); err != nil {
		t.Fatal(err)
	}

	if resolved, err := s.Track(context.Background()); err != nil || resolved != 0 {
		t.Fatalf("Track: resolved=%d err=%v", resolved, err)
	}
	if state, _, _, _, _ := settlementRow(t, pool, tap); state != "submitted" {
		t.Errorf("state = %s, want left submitted", state)
	}
	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Errorf("merchant owed %s; nothing should have been put back", got)
	}
}

// The last round's refund leaves the tap failed, with the merchant's claim
// standing where the audit can see it and nothing more sold.
func TestTheLastRefundLeavesTheMerchantOwedAndVisible(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s, sw, _ := switchSettler(pool)
	tap, merchant, owed := submittedTap(t, pool, s, sw, onswitch.StatusFailed)
	last := reference(tap, MaxRounds-1)
	sw.payments[last] = sw.payments[tap.String()]
	p := sw.payments[last]
	p.Reference = last
	sw.payments[last] = p
	if _, err := pool.Exec(ctx, `UPDATE card_tap_settlements SET round = $2, order_id = $3 WHERE tap_id = $1`,
		tap, MaxRounds-1, last); err != nil {
		t.Fatal(err)
	}
	if err := movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := movements.MerchantSettlementRefunded(ctx, tx, merchant, owed, tap, 0, "test"); err != nil {
			return err
		}
		_, err := movements.MerchantSettledOnChain(ctx, tx, merchant, owed, tap, MaxRounds-1)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Track(ctx); err != nil {
		t.Fatalf("Track: %v", err)
	}
	state, _, _, _, round := settlementRow(t, pool, tap)
	if state != "failed" || round != MaxRounds {
		t.Errorf("row = %s round %d, want failed round %d", state, round, MaxRounds)
	}
	if got := owedTo(t, pool, merchant); got.Minor() != owed.Minor() {
		t.Errorf("merchant owed %s after giving up, want %s standing", got, owed)
	}
	s.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if created, err := s.Tick(ctx); err != nil || created != 0 {
		t.Errorf("Tick sold a failed tap: created=%d err=%v", created, err)
	}
}

// A reversed tap has nothing to pay for, and is not sold however its
// settlement row is left.
func TestAReversedTapIsNeverSold(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s, sw, sender := switchSettler(pool)
	tap, cardholderID, merchant, _ := pendingTap(t, pool, s)
	if err := movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		amount := money.Naira(1_600)
		fee := money.FeeFor(amount, 50)
		ledgerTx, err := movements.TapReversal(ctx, tx, cardholderID, merchant, amount, fee, tap, "goods_not_supplied")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO card_tap_reversals (id, tap_id, reason, ledger_tx_id)
			VALUES (gen_random_uuid(), $1, 'goods_not_supplied', $2)`, tap, ledgerTx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	created, err := s.Tick(ctx)
	if err != nil || created != 0 {
		t.Fatalf("Tick sold a reversed tap: created=%d err=%v", created, err)
	}
	if sender.sent != 0 || len(sw.initiated) != 0 {
		t.Error("a payout was opened or funded for a reversed tap")
	}
}
