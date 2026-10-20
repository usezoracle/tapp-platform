package yield

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/chain/cdp"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/platform/migrate"
)

var (
	vaultAddr = common.HexToAddress("0xeE8F4eC5672F09119b96Ab6fB59C27E1b7e44b61")
	usdcAddr  = common.HexToAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913")
)

// chain is Base as far as the vault is concerned: wallets, shares, a share
// price, and CDP's idempotency keys. A send lands once per key; sending a key
// again answers what it did the first time.
type chain struct {
	wallet map[common.Address]int64
	shares map[common.Address]int64
	// price is assets per share, in millionths: 1_000_000 is 1:1.
	price int64
	// liquid caps maxWithdraw; negative means no cap.
	liquid int64

	landed map[string]string
	sends  int
	// fail answers the next sends with these errors, in order, without
	// landing them.
	fail []error
	// lose lands the next send and answers with an error, as a lost response.
	lose bool
}

func newChain() *chain {
	return &chain{
		wallet: map[common.Address]int64{}, shares: map[common.Address]int64{},
		price: 1_000_000, liquid: -1, landed: map[string]string{},
	}
}

func (c *chain) assets(shares int64) int64 { return shares * c.price / 1_000_000 }

func (c *chain) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	method, err := parsed.MethodById(msg.Data[:4])
	if err != nil {
		return nil, err
	}
	args, err := method.Inputs.Unpack(msg.Data[4:])
	if err != nil {
		return nil, err
	}
	var out int64
	switch method.Name {
	case "balanceOf":
		who := args[0].(common.Address)
		if *msg.To == usdcAddr {
			out = c.wallet[who]
		} else {
			out = c.shares[who]
		}
	case "convertToAssets":
		out = c.assets(args[0].(*big.Int).Int64())
	case "maxWithdraw":
		out = c.assets(c.shares[args[0].(common.Address)])
		if c.liquid >= 0 && c.liquid < out {
			out = c.liquid
		}
	}
	return method.Outputs.Pack(big.NewInt(out))
}

func (c *chain) SendCalls(_ context.Context, account string, calls []cdp.Call, idem string) (string, error) {
	if hash, ok := c.landed[idem]; ok {
		return hash, nil
	}
	if len(c.fail) > 0 {
		err := c.fail[0]
		c.fail = c.fail[1:]
		return "", err
	}
	acct := common.HexToAddress(account)
	for _, call := range calls {
		data := common.FromHex(call.Data)
		method, err := parsed.MethodById(data[:4])
		if err != nil {
			return "", err
		}
		args, _ := method.Inputs.Unpack(data[4:])
		switch method.Name {
		case "deposit":
			amount := args[0].(*big.Int).Int64()
			c.wallet[acct] -= amount
			c.shares[acct] += amount * 1_000_000 / c.price
		case "withdraw":
			amount := args[0].(*big.Int).Int64()
			c.shares[acct] -= (amount*1_000_000 + c.price - 1) / c.price
			c.wallet[acct] += amount
		}
	}
	c.sends++
	hash := fmt.Sprintf("0x%064x", c.sends)
	c.landed[idem] = hash
	if c.lose {
		c.lose = false
		return "", errors.New("cdp: poll user operation: connection reset")
	}
	return hash, nil
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
		t.Skipf("no test database (%v)", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database (%v)", err)
	}
	if err := migrate.Up(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	// Resolve sends every stale 'sending' row there is, so one a crashed
	// run left behind would be sent through this test's chain.
	if _, err := pool.Exec(ctx, `DELETE FROM vault_movements WHERE state = 'sending'`); err != nil {
		pool.Close()
		t.Fatalf("clear vault movements: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type world struct {
	pool    *pgxpool.Pool
	chain   *chain
	svc     *Service
	user    uuid.UUID
	account common.Address
	now     time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	w := &world{
		pool: pool, chain: newChain(), user: uuid.New(),
		now: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC),
	}
	w.account = common.BytesToAddress(w.user[:])
	w.svc = &Service{
		Pool:     pool,
		Vault:    &Vault{Client: w.chain, Address: vaultAddr, USDC: usdcAddr},
		Sender:   w.chain,
		Sweeping: true,
		Users:    map[uuid.UUID]bool{w.user: true},
		Now:      func() time.Time { return w.now },
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email) VALUES ($1, $2)`,
		w.user, w.user.String()+"@t.local"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO base_deposit_addresses (user_id, address, provider, owner_address, account_name)
		VALUES ($1, $2, 'cdp', $3, $4)`,
		w.user, strings.ToLower(w.account.Hex()), "0x"+strings.Repeat("1", 40), "acct-"+w.user.String()); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) movements(t *testing.T) (deposits, releases int64, sending int) {
	t.Helper()
	if err := w.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(amount_micro) FILTER (WHERE kind = 'deposit' AND state = 'confirmed'), 0),
		       COALESCE(SUM(amount_micro) FILTER (WHERE kind = 'release' AND state = 'confirmed'), 0),
		       count(*) FILTER (WHERE state = 'sending')
		  FROM vault_movements WHERE user_id = $1`, w.user).Scan(&deposits, &releases, &sending); err != nil {
		t.Fatal(err)
	}
	return
}

func (w *world) usd(t *testing.T) int64 {
	t.Helper()
	b, err := ledger.Balance(context.Background(), w.pool, ledger.User(w.user), ledger.KindAvailable, money.USD)
	if err != nil {
		t.Fatal(err)
	}
	return b.Minor()
}

// ref makes a spend's reference unique to this world, as a real one is
// (a payout's UUID, a withdrawal's id); a fixed string would find the rows an
// earlier run left.
func (w *world) ref(spend string) string { return spend + ":" + w.user.String() }

// credits is how many days this world's cardholder has been credited. Accrue
// works through every position in the database, other tests' too, so its
// count is not this one's.
func (w *world) credits(t *testing.T) int {
	t.Helper()
	var n int
	if err := w.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM yield_credits WHERE user_id = $1`, w.user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnIdleAccountIsSweptIntoTheVault(t *testing.T) {
	w := newWorld(t)
	w.chain.wallet[w.account] = 5_000_000

	if _, err := w.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.chain.wallet[w.account] != 0 || w.chain.shares[w.account] != 5_000_000 {
		t.Errorf("wallet %d shares %d, want everything in the vault", w.chain.wallet[w.account], w.chain.shares[w.account])
	}
	if d, _, sending := w.movements(t); d != 5_000_000 || sending != 0 {
		t.Errorf("deposits %d sending %d, want 5000000 confirmed", d, sending)
	}
}

func TestASweepWaitsForWhatIsAboutToLeave(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	// Below the minimum.
	w.chain.wallet[w.account] = 900_000
	if _, err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if w.chain.sends != 0 {
		t.Fatal("swept less than the minimum")
	}

	// A withdrawal waiting to send.
	w.chain.wallet[w.account] = 5_000_000
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO base_withdrawals (id, user_id, amount_micro, to_address)
		VALUES ($1, $2, 1000000, $3)`, uuid.New(), w.user, "0x"+strings.Repeat("2", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if w.chain.sends != 0 {
		t.Fatal("swept an account with a withdrawal pending")
	}
	if _, err := w.pool.Exec(ctx, `UPDATE base_withdrawals SET state = 'sent' WHERE user_id = $1`, w.user); err != nil {
		t.Fatal(err)
	}

	// Not on the list.
	w.svc.Users = map[uuid.UUID]bool{uuid.New(): true}
	if _, err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if w.chain.sends != 0 {
		t.Fatal("swept a cardholder who is not on the list")
	}

	// Switched off.
	w.svc.Users, w.svc.Sweeping = nil, false
	w.svc.Tick(ctx)
	if w.chain.sends != 0 {
		t.Fatal("swept with sweeping off")
	}
}

// A spend finds its shortfall released first, as its own operation, and a
// retry of the same spend does not release again.
func TestASpendReleasesOnlyItsShortfall(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.chain.wallet[w.account] = 500_000
	w.chain.shares[w.account] = 5_000_000

	if err := w.svc.Ensure(ctx, w.account.Hex(), 1_100_325, w.ref("offramp:tap-1")); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if w.chain.wallet[w.account] != 1_100_325 {
		t.Errorf("wallet %d, want exactly the 1100325 to send", w.chain.wallet[w.account])
	}
	if _, r, _ := w.movements(t); r != 600_325 {
		t.Errorf("released %d, want the 600325 short", r)
	}
	if err := w.svc.Ensure(ctx, w.account.Hex(), 1_100_325, w.ref("offramp:tap-1")); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if w.chain.sends != 1 {
		t.Errorf("%d operations, want 1", w.chain.sends)
	}

	// Enough on hand: nothing moves.
	if err := w.svc.Ensure(ctx, w.account.Hex(), 1_000_000, w.ref("withdrawal:w-1")); err != nil || w.chain.sends != 1 {
		t.Errorf("Ensure with enough on hand: err=%v sends=%d", err, w.chain.sends)
	}
}

// A release whose answer is lost is left 'sending' and finished under its own
// key, so it lands once.
func TestALostReleaseIsFinishedNotRepeated(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.chain.shares[w.account] = 5_000_000
	w.chain.lose = true

	if err := w.svc.Ensure(ctx, w.account.Hex(), 2_000_000, w.ref("offramp:tap-2")); err == nil {
		t.Fatal("a lost answer was reported as success")
	}
	if _, _, sending := w.movements(t); sending != 1 {
		t.Fatalf("%d rows sending, want the lost one", sending)
	}
	// The settler retries the spend.
	if err := w.svc.Ensure(ctx, w.account.Hex(), 2_000_000, w.ref("offramp:tap-2")); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if w.chain.sends != 1 || w.chain.wallet[w.account] != 2_000_000 {
		t.Errorf("sends %d wallet %d, want one release of 2000000", w.chain.sends, w.chain.wallet[w.account])
	}
	if _, r, sending := w.movements(t); r != 2_000_000 || sending != 0 {
		t.Errorf("released %d sending %d", r, sending)
	}
}

// A release CDP reports failed is recorded failed, and the next attempt is
// a new operation.
func TestAFailedReleaseIsTriedAgainAsANewOperation(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.chain.shares[w.account] = 5_000_000
	w.chain.fail = []error{&cdp.OperationFailedError{UserOpHash: "0x1", Reason: "FAILED"}}

	if err := w.svc.Ensure(ctx, w.account.Hex(), 2_000_000, w.ref("offramp:tap-3")); err == nil {
		t.Fatal("a failed release was reported as success")
	}
	if err := w.svc.Ensure(ctx, w.account.Hex(), 2_000_000, w.ref("offramp:tap-3")); err != nil {
		t.Fatalf("retry: %v", err)
	}
	var states []string
	rows, _ := w.pool.Query(ctx, `SELECT reference || '=' || state FROM vault_movements WHERE user_id = $1 ORDER BY created_at`, w.user)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		states = append(states, s)
	}
	rows.Close()
	ref := "release:" + w.ref("offramp:tap-3")
	want := []string{ref + "=failed", ref + ":1=confirmed"}
	if strings.Join(states, ",") != strings.Join(want, ",") {
		t.Errorf("rows %v, want %v", states, want)
	}
}

func TestAReleaseTheVaultCannotMeetIsRefusedUnsent(t *testing.T) {
	w := newWorld(t)
	w.chain.shares[w.account] = 5_000_000
	w.chain.liquid = 1_000_000
	err := w.svc.Ensure(context.Background(), w.account.Hex(), 2_000_000, w.ref("offramp:tap-4"))
	if err == nil || !strings.Contains(err.Error(), "can release 1000000") {
		t.Fatalf("err = %v, want the vault's limit", err)
	}
	if w.chain.sends != 0 {
		t.Error("sent a release the vault could not meet")
	}
}

// What the vault earned is credited in whole cents, once a day, the fraction
// carried; a loss stops crediting and takes nothing back.
func TestEarningsAreCreditedOnceADayInWholeCents(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.chain.wallet[w.account] = 5_000_000
	if _, err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	// 5 USDC grows by 0.0123: one cent, 0.0023 carried.
	w.chain.price = 1_002_460
	if _, err := w.svc.Accrue(ctx); err != nil || w.credits(t) != 1 {
		t.Fatalf("accrue: credits=%d err=%v", w.credits(t), err)
	}
	if got := w.usd(t); got != 1 {
		t.Errorf("balance %d cents, want 1", got)
	}
	// Again the same day: nothing.
	w.chain.price = 1_004_000
	if _, _ = w.svc.Accrue(ctx); w.credits(t) != 1 {
		t.Errorf("credited %d times on one day", w.credits(t))
	}

	// The next day: 0.02 earned in all, 0.01 credited, so one more cent.
	w.now = w.now.Add(24 * time.Hour)
	if _, err := w.svc.Accrue(ctx); err != nil || w.credits(t) != 2 {
		t.Fatalf("next day: credits=%d err=%v", w.credits(t), err)
	}
	if got := w.usd(t); got != 2 {
		t.Errorf("balance %d cents, want 2", got)
	}

	// A release is not yield: the value falls by what left, and so does the
	// principal.
	if err := w.svc.Ensure(ctx, w.account.Hex(), 1_000_000, w.ref("withdrawal:w-2")); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(24 * time.Hour)
	if _, _ = w.svc.Accrue(ctx); w.credits(t) != 2 {
		t.Errorf("a release was credited as yield")
	}

	// The vault loses value: nothing credited, nothing taken.
	w.chain.price = 990_000
	w.now = w.now.Add(24 * time.Hour)
	if _, _ = w.svc.Accrue(ctx); w.credits(t) != 2 || w.usd(t) != 2 {
		t.Errorf("after a loss: credits %d, balance %d", w.credits(t), w.usd(t))
	}
}

// Until a lost operation is resolved the principal is unknown, and nothing is
// credited: a deposit counted as missing would be paid out as yield.
func TestNothingIsCreditedWhileAnOperationIsUnresolved(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.chain.wallet[w.account] = 5_000_000
	if _, err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	w.chain.wallet[w.account] = 3_000_000
	w.chain.lose = true
	if _, err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	w.chain.price = 1_010_000
	if _, _ = w.svc.Accrue(ctx); w.credits(t) != 0 {
		t.Fatal("credited with a deposit unresolved")
	}

	// Resolved later, under its own key: one deposit, then the credit.
	w.now = w.now.Add(staleAfter + time.Minute)
	if _, err := w.svc.Resolve(ctx); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if d, _, _ := w.movements(t); d != 8_000_000 || w.chain.sends != 2 {
		t.Errorf("deposits %d sends %d, want 8000000 in two operations", d, w.chain.sends)
	}
	if _, err := w.svc.Accrue(ctx); err != nil || w.credits(t) != 1 {
		t.Fatalf("accrue: credits=%d err=%v", w.credits(t), err)
	}
}
