// reverse-tap refunds a card payment in full.
//
// The same operation a merchant performs from their own app, driven from here
// for a tap whose merchant cannot reach it -- one taken during testing, or
// under an arrangement since replaced.
//
// It goes through the service the app calls, so a reversal from here and one
// from a till leave the books in the same shape: the cardholder is made whole
// including the fee, the merchant's claim is withdrawn, and the reason is
// recorded against the tap.
//
// Dry by default.
//
//	go run ./cmd/reverse-tap -id <uuid> -reason "…"
//	go run ./cmd/reverse-tap -id <uuid> -reason "…" -apply
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/internal/card/tap"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/money"
)

func main() {
	id := flag.String("id", "", "tap to reverse")
	reason := flag.String("reason", "", "why it is being refunded")
	apply := flag.Bool("apply", false, "actually refund; without it nothing changes")
	flag.Parse()

	if err := run(context.Background(), *id, *reason, *apply); err != nil {
		fmt.Fprintln(os.Stderr, "reverse-tap:", err)
		os.Exit(1)
	}
}

func target(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return "(unparseable DSN)"
	}
	return u.Host + strings.TrimSuffix(u.Path, "/")
}

func run(ctx context.Context, id, reason string, apply bool) error {
	if id == "" || reason == "" {
		return fmt.Errorf("both -id and -reason are required")
	}
	tapID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("%q is not a tap id", id)
	}

	dsn := config.DBConfig()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	fmt.Printf("database: %s\n\n", target(dsn))

	var (
		cardholder, merchant  uuid.UUID
		currency, email       string
		amountMinor, feeMinor int64
		reversals             int
	)
	err = pool.QueryRow(ctx, `
		SELECT t.cardholder_id, t.merchant_id, t.currency, t.amount_minor, t.fee_minor,
		       coalesce(u.email, ''),
		       (SELECT count(*) FROM card_tap_reversals r WHERE r.tap_id = t.id)
		  FROM card_taps t LEFT JOIN users u ON u.id = t.cardholder_id
		 WHERE t.id = $1`, tapID).
		Scan(&cardholder, &merchant, &currency, &amountMinor, &feeMinor, &email, &reversals)
	if err != nil {
		return fmt.Errorf("read tap %s: %w", tapID, err)
	}

	c := money.Currency(currency)
	amount, fee := money.New(amountMinor, c), money.New(feeMinor, c)

	fmt.Printf("tap        %s\n", tapID)
	fmt.Printf("cardholder %s\n", email)
	fmt.Printf("merchant   %s\n", merchant)
	fmt.Printf("amount     %s (fee %s)\n\n", amount, fee)

	if reversals > 0 {
		return fmt.Errorf("this tap has already been reversed; refunding again would pay twice")
	}

	before, err := ledger.Balance(ctx, pool, ledger.User(cardholder), ledger.KindAvailable, c)
	if err != nil {
		return err
	}
	owed, err := ledger.Balance(ctx, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, c)
	if err != nil {
		return err
	}
	fmt.Printf("cardholder holds %s; the merchant is owed %s\n\n", before, owed)

	if !apply {
		fmt.Printf("dry run — nothing changed. %s would go back to the cardholder "+
			"and the merchant's claim withdrawn.\n", amount)
		fmt.Println("re-run with -apply to refund")
		return nil
	}

	// Driven through the service so this and a merchant-initiated refund are
	// the same operation. A reversal assembled here by hand could differ in
	// what it restores -- the fee especially -- and two ways of refunding that
	// disagree is worse than one that is awkward to reach.
	svc := &tap.Service{Pool: pool}
	if err := svc.Reverse(ctx, merchant, tapID, reason); err != nil {
		return err
	}

	after, err := ledger.Balance(ctx, pool, ledger.User(cardholder), ledger.KindAvailable, c)
	if err != nil {
		return err
	}
	stillOwed, err := ledger.Balance(ctx, pool, ledger.Merchant(merchant), ledger.KindMerchantPayable, c)
	if err != nil {
		return err
	}
	fmt.Printf("refunded. %s now holds %s; the merchant is owed %s\n", email, after, stillOwed)
	return nil
}
