// abandon-payout returns the money reserved for a payout that will never be
// delivered, and marks it failed.
//
// For a payout stranded by a change of arrangement rather than refused by a
// provider: a rail retired, a settlement model replaced. The reservation sits
// in the system's `payable` account, where it is neither the platform's nor
// reachable by the person owed it, and only something like this puts it back.
//
// It goes through the same movement the worker uses when a provider refuses on
// the merits, so an abandoned payout and a rejected one leave the books in the
// same shape. Hand-written ledger entries would not, and a ledger is only
// worth having if every posting in it came from the same few places.
//
// Dry by default.
//
//	go run ./cmd/abandon-payout -id <uuid> -reason "…"
//	go run ./cmd/abandon-payout -id <uuid> -reason "…" -apply
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
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/internal/settlement"
)

func main() {
	id := flag.String("id", "", "payout to abandon")
	reason := flag.String("reason", "", "why it will never be delivered")
	apply := flag.Bool("apply", false, "actually return the money; without it nothing changes")
	flag.Parse()

	if err := run(context.Background(), *id, *reason, *apply); err != nil {
		fmt.Fprintln(os.Stderr, "abandon-payout:", err)
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
	payoutID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("%q is not a payout id", id)
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
	// Which database, before anything moves: the DSN falls through several
	// variables and a run meant for production that misses them lands on
	// whatever is local, indistinguishably.
	fmt.Printf("database: %s\n\n", target(dsn))

	var (
		kind, beneficiary, currency, state string
		minor                              int64
	)
	err = pool.QueryRow(ctx, `
		SELECT beneficiary_kind, beneficiary_id, currency, amount_minor, state
		  FROM payouts WHERE id = $1`, payoutID).
		Scan(&kind, &beneficiary, &currency, &minor, &state)
	if err != nil {
		return fmt.Errorf("read payout %s: %w", payoutID, err)
	}
	amount := money.New(minor, money.Currency(currency))

	fmt.Printf("payout      %s\n", payoutID)
	fmt.Printf("beneficiary %s %s\n", kind, beneficiary)
	fmt.Printf("amount      %s\n", amount)
	fmt.Printf("state       %s\n\n", state)

	if !apply {
		fmt.Printf("dry run — nothing changed. %s would go back to what the %s is owed.\n",
			amount, kind)
		fmt.Println("re-run with -apply to return it")
		return nil
	}

	// Rail is nil on purpose: this returns money, it never sends any, and a
	// command that could reach a provider is one that could deliver a payout
	// somebody asked to abandon.
	w := &settlement.Worker{Pool: pool}
	p, err := w.Abandon(ctx, payoutID, reason)
	if err != nil {
		return err
	}
	fmt.Printf("returned %s to what the %s is owed; payout marked failed\n",
		p.Amount, p.Beneficiary.Kind)
	return nil
}
