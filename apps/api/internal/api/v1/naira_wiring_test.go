package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/card/tap"
	"github.com/usezoracle/tapp/api/internal/chain/base"
	"github.com/usezoracle/tapp/api/internal/chain/offramp"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/rates"
	"github.com/usezoracle/tapp/api/internal/settlement/naira"
	"github.com/usezoracle/tapp/api/internal/transactions"
	"github.com/usezoracle/tapp/api/services/baas"
)

// flatRate prices every pair at one number.
type flatRate struct{ rate decimal.Decimal }

func (f flatRate) Name() string { return "flat" }
func (f flatRate) Rate(context.Context, rates.Pair) (decimal.Decimal, error) {
	return f.rate, nil
}

// nairaRailStub is a Fintava as far as the wiring needs one: it has a name.
type nairaRailStub struct{ baas.Provider }

func (nairaRailStub) Name() string { return "fintava" }

// settlementWorld is a cardholder with a naira wallet and a USDC deposit
// address, and a merchant with a verified bank -- everything both legs need.
type settlementWorld struct {
	pool                 *pgxpool.Pool
	cardholder, merchant uuid.UUID
	account              string
	settle               func(context.Context, pgx.Tx, tap.Charged) error
}

func newSettlementWorld(t *testing.T) *settlementWorld {
	t.Helper()
	pool := ngnTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS merchant_bank_accounts (
			id uuid PRIMARY KEY, created_at timestamptz, updated_at timestamptz,
			currency text, bank_code text, account_number text, account_name text,
			verified_at timestamptz, sender_profile_merchant_bank_account uuid)`); err != nil {
		t.Fatal(err)
	}

	// The three process-wide things the wiring reads, stood in for.
	prevRail, prevQuoter, prevBaaS := rail, sharedQuoter, baas.Default()
	SetRail(&BaseRail{Addresses: &base.Addresses{Pool: pool}})
	sharedQuoter = &rates.Quoter{Engine: &rates.Engine{
		Sources: []rates.Source{flatRate{decimal.RequireFromString("1500")}}, // ₦1,500 per $1
	}, Pool: pool}
	baas.SetDefault(nairaRailStub{})
	t.Cleanup(func() {
		SetRail(prevRail)
		sharedQuoter = prevQuoter
		baas.SetDefault(prevBaaS)
	})

	w := &settlementWorld{
		pool: pool, cardholder: uuid.New(), merchant: uuid.New(),
		settle: RecordTapSettlement(&offramp.Settler{Pool: pool}),
	}
	w.account = "1" + w.cardholder.String()[:9]
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(ctx, `INSERT INTO users (id, email) VALUES ($1, $2)`, w.cardholder, w.cardholder.String()+"@t.local")
	must(err)
	_, err = pool.Exec(ctx, `
		INSERT INTO ngn_deposit_accounts (user_id, rail, account_number, bank_name, account_name, rail_ref, wallet_id)
		VALUES ($1, 'fintava', $2, 'Loma', 'ADA O', 'cust-ada', 'wal-ada')`, w.cardholder, w.account)
	must(err)
	var next int64
	must(pool.QueryRow(ctx, `UPDATE base_deposit_counter SET next_index = next_index + 1 RETURNING next_index`).Scan(&next))
	_, err = pool.Exec(ctx, `
		INSERT INTO base_deposit_addresses (user_id, index, address)
		VALUES ($1, $2, $3)`, w.cardholder, next, "0x"+uuid.New().String()[:8]+uuid.New().String()[:8]+uuid.New().String()[:8]+uuid.New().String()[:8]+uuid.New().String()[:8])
	must(err)
	_, err = pool.Exec(ctx, `
		INSERT INTO merchant_bank_accounts (id, currency, bank_code, account_number, account_name,
		                                    verified_at, sender_profile_merchant_bank_account)
		VALUES ($1, 'NGN', 'OPAYNGPC', '9034409271', 'OLUMIDE SILAS OGUNDELE', now(), $2)`, uuid.New(), w.merchant)
	must(err)
	_, err = movements.Deposit(ctx, pool, w.cardholder, money.Naira(100_000), "fintava", uuid.NewString())
	must(err)
	return w
}

// charge does what the tap's transaction does: the ledger movement, the tap
// row with its split, and the settlement hook -- all or nothing.
func (w *settlementWorld) charge(t *testing.T, amount, ngn money.Amount) (uuid.UUID, error) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	fee := money.FeeFor(amount, 50)
	usdc, _ := amount.Sub(ngn)
	err := movements.InTx(ctx, w.pool, func(tx pgx.Tx) error {
		ledgerTx, err := movements.Tap(ctx, tx, w.cardholder, w.merchant, amount, fee, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO card_taps (id, card_id, cardholder_id, merchant_id, currency, amount_minor, fee_minor, tier,
			                       funding_source, funded_ngn_minor, funded_usdc_minor, ledger_tx_id, nonce)
			VALUES ($1, $2, $3, $4, 'NGN', $5, $6, 'none', $7, $8, $9, $10, $11)`,
			id, uuid.New(), w.cardholder, w.merchant, amount.Minor(), fee.Minor(),
			string(tap.Funding{NGN: ngn, USDC: usdc}.Source()), ngn.Minor(), usdc.Minor(), ledgerTx, uuid.NewString()); err != nil {
			return err
		}
		return w.settle(ctx, tx, tap.Charged{
			TapID: id, Cardholder: w.cardholder, Merchant: w.merchant,
			Amount: amount, Fee: fee, Funding: tap.Funding{NGN: ngn, USDC: usdc},
		})
	})
	return id, err
}

func (w *settlementWorld) legs(t *testing.T, id uuid.UUID) (ngnMinor, usdcMinor int64, ngnState, usdcState string) {
	t.Helper()
	ctx := context.Background()
	var n, u *int64
	var ns, us *string
	if err := w.pool.QueryRow(ctx, `
		SELECT ns.amount_minor, ns.state, st.deliver_minor, st.state
		  FROM card_taps t
		  LEFT JOIN card_tap_ngn_settlements ns ON ns.tap_id = t.id
		  LEFT JOIN card_tap_settlements st ON st.tap_id = t.id
		 WHERE t.id = $1`, id).Scan(&n, &ns, &u, &us); err != nil {
		t.Fatal(err)
	}
	if n != nil {
		ngnMinor, ngnState = *n, *ns
	}
	if u != nil {
		usdcMinor, usdcState = *u, *us
	}
	return
}

// A tap paid from a naira balance gets a naira leg out of the cardholder's
// wallet and no on-chain order; one paid by converting gets the order and no
// naira leg; one paid with both gets both, adding up to what is owed.
func TestTheSplitDecidesTheLegs(t *testing.T) {
	w := newSettlementWorld(t)
	ctx := context.Background()

	// All naira.
	id, err := w.charge(t, money.Naira(1_600), money.Naira(1_600))
	if err != nil {
		t.Fatalf("naira tap: %v", err)
	}
	ngn, usdc, ngnState, usdcState := w.legs(t, id)
	if ngn != 159_200 || ngnState != naira.Queued || usdcState != "" {
		t.Fatalf("naira tap legs: ngn=%d(%s) usdc=%d(%s); want ₦1,592 queued and no order", ngn, ngnState, usdc, usdcState)
	}
	tx, err := transactions.Get(ctx, w.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status != transactions.StatusPending || tx.SettlementRail != transactions.RailFintava || len(tx.Legs) != 1 ||
		tx.Legs[0].Rail != transactions.RailFintava || tx.Legs[0].Amount.Minor() != 159_200 {
		t.Errorf("read model: status=%s rail=%s legs=%+v", tx.Status, tx.SettlementRail, tx.Legs)
	}

	// All converted: unchanged from before -- one order for the whole of
	// what is owed.
	id, err = w.charge(t, money.Naira(1_600), money.Zero(money.NGN))
	if err != nil {
		t.Fatalf("usdc tap: %v", err)
	}
	ngn, usdc, ngnState, usdcState = w.legs(t, id)
	if ngnState != "" || usdc != 159_200 || usdcState != "pending" {
		t.Fatalf("usdc tap legs: ngn=%d(%s) usdc=%d(%s); want no naira leg and an order for ₦1,592", ngn, ngnState, usdc, usdcState)
	}
	if tx, err = transactions.Get(ctx, w.pool, id); err != nil || tx.SettlementRail != transactions.RailPaycrest || len(tx.Legs) != 1 {
		t.Errorf("read model: rail=%s legs=%+v err=%v", tx.SettlementRail, tx.Legs, err)
	}

	// Both: ₦1,000 from the balance, ₦600 bought. The fee (₦8) comes out
	// of the naira, so the wallet pays ₦992 and the order delivers ₦600.
	id, err = w.charge(t, money.Naira(1_600), money.Naira(1_000))
	if err != nil {
		t.Fatalf("mixed tap: %v", err)
	}
	ngn, usdc, ngnState, usdcState = w.legs(t, id)
	if ngn != 99_200 || ngnState != naira.Queued || usdc != 60_000 || usdcState != "pending" {
		t.Fatalf("mixed tap legs: ngn=%d(%s) usdc=%d(%s); want ₦992 and ₦600", ngn, ngnState, usdc, usdcState)
	}
	if ngn+usdc != 159_200 {
		t.Errorf("legs sum to %d, want 159200 (what the merchant is owed)", ngn+usdc)
	}
	tx, _ = transactions.Get(ctx, w.pool, id)
	if tx.Status != transactions.StatusPending || tx.SettlementRail != transactions.RailMixed || len(tx.Legs) != 2 {
		t.Fatalf("read model: status=%s rail=%s legs=%+v", tx.Status, tx.SettlementRail, tx.Legs)
	}

	// The tap is settled only when every leg is, and failed when any is.
	if _, err := w.pool.Exec(ctx, `UPDATE card_tap_ngn_settlements SET state = 'settled', settled_at = now() WHERE tap_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if tx, _ = transactions.Get(ctx, w.pool, id); tx.Status != transactions.StatusPending {
		t.Errorf("one leg settled: status=%s, want pending", tx.Status)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE card_tap_settlements SET state = 'submitted' WHERE tap_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if tx, _ = transactions.Get(ctx, w.pool, id); tx.Status != transactions.StatusProcessing {
		t.Errorf("one leg in flight: status=%s, want processing", tx.Status)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE card_tap_settlements SET state = 'fulfilled' WHERE tap_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	tx, _ = transactions.Get(ctx, w.pool, id)
	if tx.Status != transactions.StatusSettled || tx.SettledAt == nil {
		t.Errorf("both legs settled: status=%s settled_at=%v, want settled", tx.Status, tx.SettledAt)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE card_tap_ngn_settlements SET state = 'failed', error = 'refused' WHERE tap_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	tx, _ = transactions.Get(ctx, w.pool, id)
	if tx.Status != transactions.StatusFailed || tx.Legs[0].Status != transactions.StatusFailed || tx.Legs[1].Status != transactions.StatusSettled {
		t.Errorf("one leg failed: status=%s legs=%+v, want failed", tx.Status, tx.Legs)
	}
}

// A naira leg with nowhere to go, or nothing to come from, refuses the tap
// -- and the charge rolls back with it.
func TestANairaLegNobodyCanPayIsRefusedAtTheTill(t *testing.T) {
	w := newSettlementWorld(t)
	ctx := context.Background()
	taps := func() int {
		var n int
		if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM card_taps WHERE cardholder_id = $1`, w.cardholder).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The merchant's bank is unverified.
	if _, err := w.pool.Exec(ctx, `UPDATE merchant_bank_accounts SET verified_at = NULL WHERE sender_profile_merchant_bank_account = $1`, w.merchant); err != nil {
		t.Fatal(err)
	}
	if _, err := w.charge(t, money.Naira(1_600), money.Naira(1_600)); !errors.Is(err, naira.ErrNoBankAccount) {
		t.Fatalf("no verified bank: %v, want ErrNoBankAccount", err)
	}
	// A USDC-only tap does not need it (the provider pays whatever bank the
	// merchant verifies later, as before).
	if _, err := w.charge(t, money.Naira(1_600), money.Zero(money.NGN)); err != nil {
		t.Fatalf("usdc tap with an unverified bank: %v", err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE merchant_bank_accounts SET verified_at = now() WHERE sender_profile_merchant_bank_account = $1`, w.merchant); err != nil {
		t.Fatal(err)
	}

	// The cardholder has no wallet.
	if _, err := w.pool.Exec(ctx, `DELETE FROM ngn_deposit_accounts WHERE user_id = $1`, w.cardholder); err != nil {
		t.Fatal(err)
	}
	if _, err := w.charge(t, money.Naira(1_600), money.Naira(1_000)); !errors.Is(err, naira.ErrNoWallet) {
		t.Fatalf("no wallet: %v, want ErrNoWallet", err)
	}
	if n := taps(); n != 1 {
		t.Errorf("%d taps recorded, want 1 (the refused ones rolled back)", n)
	}
	// And it is a refusal the till can show.
	if _, ok := tapErrorCode(naira.ErrNoWallet); !ok {
		t.Error("ErrNoWallet has no stable code for the merchant app")
	}
	if _, ok := tapErrorCode(naira.ErrNoBankAccount); !ok {
		t.Error("ErrNoBankAccount has no stable code for the merchant app")
	}
}

// Reconciling a wallet counts what has been paid out of it: the rail's
// balance is lower by exactly the legs it paid, and the ledger must not be
// credited for money that was deposited, spent, and paid on.
func TestReconcileAddsBackWhatTheWalletPaidOut(t *testing.T) {
	w := newSettlementWorld(t)
	ctx := context.Background()

	// ₦100,000 was credited by the fixture from this rail; a ₦1,600 tap has
	// paid ₦1,592 to a merchant out of the wallet, so the rail now holds
	// ₦98,408.
	id, err := w.charge(t, money.Naira(1_600), money.Naira(1_600))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE card_tap_ngn_settlements SET state = 'settled' WHERE tap_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	rail := &stubRail{walletID: "wal-ada", balance: decimal.RequireFromString("98408")}

	res, err := ReconcileNGNDeposit(ctx, rail, w.account)
	if err != nil {
		t.Fatal(err)
	}
	if res.PaidOut.Minor() != 159_200 || res.CreditedBefore.Minor() != 10_000_000 || !res.Posted.IsZero() {
		t.Fatalf("reconcile = %+v; want ₦1,592 paid out and nothing posted", res)
	}

	// A credit the webhook missed shows up as the difference.
	rail.balance = decimal.RequireFromString("98908")
	res, err = ReconcileNGNDeposit(ctx, rail, w.account)
	if err != nil {
		t.Fatal(err)
	}
	if res.Posted.Minor() != 50_000 {
		t.Fatalf("reconcile after a missed credit = %+v; want ₦500 posted", res)
	}
}
