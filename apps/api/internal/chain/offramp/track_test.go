package offramp

import (
	"context"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
)

// The OrderCreated log the Gateway emitted for a real order on Base mainnet
// (tx 0x70b67e69…, block 51441847): the cardholder's account sold 1.208679
// USDC, and the order id is the second word of the data.
var (
	realGateway = common.HexToAddress("0x30F6A8457F8E42371E204a9c103f2Bd42341dD0F")
	realSender  = common.HexToAddress("0xb779226ee0f345b42681b981337205c918af8c3c")
	realOrderID = common.HexToHash("0x8a671d57f83991599aefdf9f849b8d3ebdd9c8f07f0415e9ae8e5bb7c95309bc")
	realLog     = types.Log{
		Address: realGateway,
		Topics: []common.Hash{
			common.HexToHash("0x40ccd1ceb111a3c186ef9911e1b876dc1f789ed331b86097b3b8851055b6a137"),
			common.HexToHash("0x000000000000000000000000b779226ee0f345b42681b981337205c918af8c3c"),
			common.HexToHash("0x000000000000000000000000833589fcd6edb6e08f4c7c32d4f71b54bda02913"),
			common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000127167"),
		},
		Data: common.FromHex("0x000000000000000000000000000000000000000000000000000000000000179b" +
			"8a671d57f83991599aefdf9f849b8d3ebdd9c8f07f0415e9ae8e5bb7c95309bc" +
			"0000000000000000000000000000000000000000000000000000000000020282" +
			"0000000000000000000000000000000000000000000000000000000000000080" +
			"0000000000000000000000000000000000000000000000000000000000000004" +
			"7465737400000000000000000000000000000000000000000000000000000000"),
	}
)

func TestTheOrderIdIsReadFromTheRealEvent(t *testing.T) {
	ev, ok, err := unpackOrderCreated(realLog)
	if err != nil || !ok {
		t.Fatalf("unpackOrderCreated: ok=%v err=%v", ok, err)
	}
	if ev.Sender != realSender {
		t.Errorf("sender = %s, want %s", ev.Sender, realSender)
	}
	if ev.Amount.Int64() != 1_208_679 {
		t.Errorf("amount = %s, want 1208679", ev.Amount)
	}
	if common.Hash(ev.OrderID) != realOrderID {
		t.Errorf("orderId = %x, want %s", ev.OrderID, realOrderID)
	}
}

// getOrderInfo's return value, as the Gateway encoded it for the refunded
// order: isFulfilled=0, isRefunded=1.
func orderInfoReturn(fulfilled, refunded bool) []byte {
	word := func(v uint64) []byte {
		return common.LeftPadBytes(new(big.Int).SetUint64(v).Bytes(), 32)
	}
	b := func(v bool) []byte {
		if v {
			return word(1)
		}
		return word(0)
	}
	var out []byte
	out = append(out, common.LeftPadBytes(realSender.Bytes(), 32)...) // sender
	out = append(out, word(0)...)                                     // token
	out = append(out, word(0)...)                                     // senderFeeRecipient
	out = append(out, word(0)...)                                     // senderFee
	out = append(out, word(6043)...)                                  // protocolFee
	out = append(out, b(fulfilled)...)
	out = append(out, b(refunded)...)
	out = append(out, common.LeftPadBytes(realSender.Bytes(), 32)...) // refundAddress
	out = append(out, word(0)...)                                     // currentBPS
	out = append(out, word(1_208_679)...)                             // amount
	return out
}

func TestTheOutcomeIsReadFromGetOrderInfo(t *testing.T) {
	info, err := unpackOrderInfo(orderInfoReturn(false, true))
	if err != nil {
		t.Fatalf("unpackOrderInfo: %v", err)
	}
	if info.Fulfilled || !info.Refunded {
		t.Errorf("info = %+v, want refunded and not fulfilled", info)
	}
}

// fakeReader is the chain as the tracker sees it: receipts by hash, and one
// answer to getOrderInfo.
type fakeReader struct {
	receipts map[string]*types.Receipt
	info     []byte
	calls    int
}

func (f *fakeReader) TransactionReceipt(_ context.Context, h common.Hash) (*types.Receipt, error) {
	r, ok := f.receipts[h.Hex()]
	if !ok {
		return nil, ethereum.NotFound
	}
	return r, nil
}

func (f *fakeReader) CallContract(_ context.Context, _ ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	f.calls++
	return f.info, nil
}

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
	// Track reads every submitted row there is, so a row another test left
	// behind would be resolved by this one, against this one's reader.
	if _, err := pool.Exec(ctx, `DELETE FROM card_tap_settlements`); err != nil {
		pool.Close()
		t.Fatalf("clear settlements: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// submittedTap is a tap that has been charged, recorded for settlement, sold,
// and discharged -- the state the old code left every tap in for good.
func submittedTap(t *testing.T, pool *pgxpool.Pool, s *Settler, txHash string) (tap, cardholder, merchant uuid.UUID, owed money.Amount) {
	t.Helper()
	ctx := context.Background()
	tap, cardholder, merchant = uuid.New(), uuid.New(), uuid.New()
	amount := money.Naira(1_600)
	fee := money.FeeFor(amount, 50)
	owed, _ = amount.Sub(fee)

	if _, err := movements.Deposit(ctx, pool, cardholder, amount, "bank", uuid.NewString()); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	err := movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		ledgerTx, err := movements.Tap(ctx, tx, cardholder, merchant, amount, fee, tap)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO card_taps (id, card_id, cardholder_id, merchant_id, currency,
			                       amount_minor, fee_minor, tier, funded_usdc_minor, ledger_tx_id, nonce)
			VALUES ($1, $2, $3, $4, 'NGN', $5, $6, 'none', $5, $7, $8)`,
			tap, uuid.New(), cardholder, merchant, amount.Minor(), fee.Minor(), ledgerTx, uuid.NewString()); err != nil {
			return err
		}
		if err := s.Record(ctx, tx, tap, realSender.Hex(), 1_208_679, owed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE card_tap_settlements
			   SET state = 'submitted', tx_hash = $2, attempts = 1
			 WHERE tap_id = $1`, tap, txHash); err != nil {
			return err
		}
		_, err = movements.MerchantSettledOnChain(ctx, tx, merchant, owed, tap, 0)
		return err
	})
	if err != nil {
		t.Fatalf("submitted tap: %v", err)
	}
	return tap, cardholder, merchant, owed
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

func trackingSettler(pool *pgxpool.Pool, r *fakeReader) *Settler {
	return &Settler{
		Pool: pool,
		Orders: &Client{
			Sender:  &fakeSender{},
			Keys:    fakeKeys{pem: testPubkeyPEM},
			Reader:  r,
			Gateway: realGateway,
			USDC:    common.HexToAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"),
		},
	}
}

const txA = "0x70b67e6948ea758eac499d7ac1967408f95ec961316d4f833f218671646c0f01"

func TestARefundedOrderPutsTheMerchantsClaimBackAndSellsAgain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	r := &fakeReader{
		receipts: map[string]*types.Receipt{
			txA: {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{&realLog}},
		},
		info: orderInfoReturn(false, true),
	}
	s := trackingSettler(pool, r)
	tap, _, merchant, owed := submittedTap(t, pool, s, txA)

	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Fatalf("merchant owed %s before tracking, want nothing: the sale discharged it", got)
	}

	resolved, err := s.Track(ctx)
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if resolved != 1 {
		t.Errorf("resolved = %d, want 1", resolved)
	}

	state, orderID, txHash, lastError, round := settlementRow(t, pool, tap)
	if state != "pending" || round != 1 {
		t.Errorf("row = %s round %d, want pending round 1", state, round)
	}
	if orderID != nil || txHash != nil {
		t.Errorf("row still names order %v tx %v after its refund", orderID, txHash)
	}
	if lastError == nil || *lastError != "round 0 ("+txA+"): refunded by the gateway" {
		t.Errorf("last_error = %v", lastError)
	}
	if got := owedTo(t, pool, merchant); got.Minor() != owed.Minor() {
		t.Errorf("merchant owed %s after the refund, want %s", got, owed)
	}

	// Tracking again finds nothing: the row is no longer submitted.
	if resolved, err := s.Track(ctx); err != nil || resolved != 0 {
		t.Errorf("second Track: resolved=%d err=%v, want 0 and nil", resolved, err)
	}

	// The next round is not sold at once...
	s.Now = func() time.Time { return time.Now() }
	if created, err := s.Tick(ctx); err != nil || created != 0 {
		t.Errorf("Tick straight after the refund: created=%d err=%v, want 0 (RetryDelay)", created, err)
	}
	// ...but it is once the delay has passed, under a new reference, and the
	// claim is discharged again for the new order.
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchant_bank_accounts (id, currency, bank_code, account_number, account_name,
		                                    verified_at, sender_profile_merchant_bank_account, created_at, updated_at)
		VALUES ($1, 'NGN', 'OPAYNGPC', '9034409271', 'OLUMIDE SILAS OGUNDELE', now(), $2, now(), now())`,
		uuid.New(), merchant); err != nil {
		t.Fatalf("bank: %v", err)
	}
	s.Now = func() time.Time { return time.Now().Add(RetryDelay + time.Second) }
	created, err := s.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick after the delay: %v", err)
	}
	if created != 1 {
		t.Errorf("created = %d, want 1", created)
	}
	if idem := s.Orders.Sender.(*fakeSender).idem; idem != "offramp:"+tap.String()+":r1" {
		t.Errorf("round 1 was sent as %q; a retry under the old key would return the refunded operation", idem)
	}
	state, _, _, _, round = settlementRow(t, pool, tap)
	if state != "submitted" || round != 1 {
		t.Errorf("row = %s round %d after the resale, want submitted round 1", state, round)
	}
	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Errorf("merchant owed %s after the resale, want nothing", got)
	}
}

func TestAFulfilledOrderIsSimplyDone(t *testing.T) {
	pool := testPool(t)
	r := &fakeReader{
		receipts: map[string]*types.Receipt{
			txA: {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{&realLog}},
		},
		info: orderInfoReturn(true, false),
	}
	s := trackingSettler(pool, r)
	tap, _, merchant, _ := submittedTap(t, pool, s, txA)

	if _, err := s.Track(context.Background()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	state, orderID, _, _, _ := settlementRow(t, pool, tap)
	if state != "fulfilled" {
		t.Errorf("state = %s, want fulfilled", state)
	}
	if orderID == nil || *orderID != realOrderID.Hex() {
		t.Errorf("order_id = %v, want %s", orderID, realOrderID.Hex())
	}
	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Errorf("merchant owed %s after fulfilment", got)
	}
}

func TestAnOpenOrderIsLeftAlone(t *testing.T) {
	pool := testPool(t)
	r := &fakeReader{
		receipts: map[string]*types.Receipt{
			txA: {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{&realLog}},
		},
		info: orderInfoReturn(false, false),
	}
	s := trackingSettler(pool, r)
	tap, _, merchant, _ := submittedTap(t, pool, s, txA)

	resolved, err := s.Track(context.Background())
	if err != nil || resolved != 0 {
		t.Fatalf("Track: resolved=%d err=%v, want 0 and nil", resolved, err)
	}
	state, orderID, _, _, _ := settlementRow(t, pool, tap)
	if state != "submitted" || orderID == nil {
		t.Errorf("state = %s order_id = %v, want submitted with the id learned", state, orderID)
	}
	if got := owedTo(t, pool, merchant); !got.IsZero() {
		t.Errorf("merchant owed %s while the order is open", got)
	}
}

// A transaction that reverted, or that carries no order for this account,
// sold nothing, and the claim it discharged comes back the same way.
func TestATransactionThatCreatedNoOrderCountsAsARefund(t *testing.T) {
	pool := testPool(t)
	r := &fakeReader{
		receipts: map[string]*types.Receipt{
			txA: {Status: types.ReceiptStatusFailed},
		},
	}
	s := trackingSettler(pool, r)
	tap, _, merchant, owed := submittedTap(t, pool, s, txA)

	if _, err := s.Track(context.Background()); err != nil {
		t.Fatalf("Track: %v", err)
	}
	state, _, _, lastError, round := settlementRow(t, pool, tap)
	if state != "pending" || round != 1 {
		t.Errorf("row = %s round %d, want pending round 1", state, round)
	}
	if lastError == nil || !strings.Contains(*lastError, "reverted") {
		t.Errorf("last_error = %v, want it to say the transaction reverted", lastError)
	}
	if got := owedTo(t, pool, merchant); got.Minor() != owed.Minor() {
		t.Errorf("merchant owed %s, want %s", got, owed)
	}
	if r.calls != 0 {
		t.Errorf("getOrderInfo was asked %d times about an order that does not exist", r.calls)
	}
}

func TestAnUnminedTransactionIsAskedAboutLater(t *testing.T) {
	pool := testPool(t)
	r := &fakeReader{receipts: map[string]*types.Receipt{}}
	s := trackingSettler(pool, r)
	tap, _, _, _ := submittedTap(t, pool, s, txA)

	if resolved, err := s.Track(context.Background()); err != nil || resolved != 0 {
		t.Fatalf("Track: resolved=%d err=%v", resolved, err)
	}
	if state, _, _, _, _ := settlementRow(t, pool, tap); state != "submitted" {
		t.Errorf("state = %s, want still submitted", state)
	}
}

// The last round's refund leaves the tap failed, with the merchant's claim
// standing where the audit can see it and nothing more sent to the chain.
func TestTheLastRefundLeavesTheMerchantOwedAndVisible(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	r := &fakeReader{
		receipts: map[string]*types.Receipt{
			txA: {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{&realLog}},
		},
		info: orderInfoReturn(false, true),
	}
	s := trackingSettler(pool, r)
	tap, _, merchant, owed := submittedTap(t, pool, s, txA)
	if _, err := pool.Exec(ctx, `UPDATE card_tap_settlements SET round = $2 WHERE tap_id = $1`,
		tap, MaxRounds-1); err != nil {
		t.Fatal(err)
	}
	// The books as a real history would have left them: round 0 refunded,
	// the last round sold and discharged.
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

// The amount sold is decided when the order is created, not when the tap was
// recorded, and the row says what the chain was actually asked to sell.
func TestAnOrderIsPricedWhenItIsCreated(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := trackingSettler(pool, &fakeReader{})
	var priced money.Amount
	s.Price = func(_ context.Context, owed money.Amount, estimate int64) (int64, error) {
		priced = owed
		if estimate != 1_208_679 {
			t.Errorf("estimate = %d, want what the tap recorded", estimate)
		}
		return 1_194_844, nil
	}

	tap, _, merchant, owed := submittedTap(t, pool, s, txA)
	// As a refund leaves it: pending for round 1, round 0's discharge undone.
	if _, err := pool.Exec(ctx, `
		UPDATE card_tap_settlements
		   SET state = 'pending', tx_hash = NULL, attempts = 0, round = 1,
		       updated_at = now() - interval '1 hour'
		 WHERE tap_id = $1`, tap); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchant_bank_accounts (id, currency, bank_code, account_number, account_name,
		                                    verified_at, sender_profile_merchant_bank_account, created_at, updated_at)
		VALUES ($1, 'NGN', 'OPAYNGPC', '9034409271', 'OLUMIDE SILAS OGUNDELE', now(), $2, now(), now())`,
		uuid.New(), merchant); err != nil {
		t.Fatalf("bank: %v", err)
	}
	// The books as a pending tap has them: nothing discharged yet.
	if err := movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := movements.MerchantSettlementRefunded(ctx, tx, merchant, owed, tap, 0, "test fixture")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	created, err := s.Tick(ctx)
	if err != nil || created != 1 {
		t.Fatalf("Tick: created=%d err=%v", created, err)
	}
	if priced.Minor() != owed.Minor() {
		t.Errorf("priced %s, want what the merchant is owed, %s", priced, owed)
	}

	var sell int64
	if err := pool.QueryRow(ctx, `SELECT sell_micro FROM card_tap_settlements WHERE tap_id = $1`, tap).Scan(&sell); err != nil {
		t.Fatal(err)
	}
	if sell != 1_194_844 {
		t.Errorf("row says %d was sold, want the priced 1194844", sell)
	}
	sender := s.Orders.Sender.(*fakeSender)
	if len(sender.calls) != 2 {
		t.Fatalf("sent %d calls, want approve + createOrder", len(sender.calls))
	}
	// createOrder's amount argument: selector(4) + token(32) + amount(32).
	data := common.FromHex(sender.calls[1].Data)
	if got := new(big.Int).SetBytes(data[4+32 : 4+64]).Int64(); got != 1_194_844 {
		t.Errorf("createOrder amount = %d, want the priced 1194844", got)
	}
}

func TestAnOrderThatCannotBePricedWaits(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := trackingSettler(pool, &fakeReader{})
	s.Price = func(context.Context, money.Amount, int64) (int64, error) {
		return 0, errors.New("aggregator down")
	}
	tap, _, merchant, _ := submittedTap(t, pool, s, txA)
	if _, err := pool.Exec(ctx, `
		UPDATE card_tap_settlements
		   SET state = 'pending', tx_hash = NULL, attempts = 0, round = 1,
		       updated_at = now() - interval '1 hour'
		 WHERE tap_id = $1`, tap); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchant_bank_accounts (id, currency, bank_code, account_number, account_name,
		                                    verified_at, sender_profile_merchant_bank_account, created_at, updated_at)
		VALUES ($1, 'NGN', 'OPAYNGPC', '9034409271', 'OLUMIDE SILAS OGUNDELE', now(), $2, now(), now())`,
		uuid.New(), merchant); err != nil {
		t.Fatalf("bank: %v", err)
	}

	created, err := s.Tick(ctx)
	if err != nil || created != 0 {
		t.Fatalf("Tick: created=%d err=%v, want nothing sold", created, err)
	}
	var attempts int
	var state string
	if err := pool.QueryRow(ctx, `SELECT attempts, state FROM card_tap_settlements WHERE tap_id = $1`, tap).Scan(&attempts, &state); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || state != "pending" {
		t.Errorf("row = %s after %d attempts; a failed quote should cost neither", state, attempts)
	}
	if len(s.Orders.Sender.(*fakeSender).calls) != 0 {
		t.Error("an order was sent without a price")
	}
}

// A reversed tap has nothing to pay for, and is not sold however its
// settlement row is left.
func TestAReversedTapIsNeverSold(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := trackingSettler(pool, &fakeReader{})
	tap, cardholder, merchant, owed := submittedTap(t, pool, s, txA)

	// The order refunded, the operator re-queued it, and the merchant then
	// reversed the tap from their till.
	if _, err := pool.Exec(ctx, `
		UPDATE card_tap_settlements
		   SET state = 'pending', tx_hash = NULL, attempts = 0, round = 1,
		       updated_at = now() - interval '1 hour'
		 WHERE tap_id = $1`, tap); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchant_bank_accounts (id, currency, bank_code, account_number, account_name,
		                                    verified_at, sender_profile_merchant_bank_account, created_at, updated_at)
		VALUES ($1, 'NGN', 'OPAYNGPC', '9034409271', 'OLUMIDE SILAS OGUNDELE', now(), $2, now(), now())`,
		uuid.New(), merchant); err != nil {
		t.Fatalf("bank: %v", err)
	}
	if err := movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := movements.MerchantSettlementRefunded(ctx, tx, merchant, owed, tap, 0, "test"); err != nil {
			return err
		}
		amount := money.Naira(1_600)
		fee := money.FeeFor(amount, 50)
		ledgerTx, err := movements.TapReversal(ctx, tx, cardholder, merchant, amount, fee, tap, "goods_not_supplied")
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
	if len(s.Orders.Sender.(*fakeSender).calls) != 0 {
		t.Error("an order was sent for a reversed tap")
	}
}
