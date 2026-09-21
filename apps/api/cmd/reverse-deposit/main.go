// reverse-deposit takes back a credit the chain never justified.
//
// A deposit address credits whatever arrives at it. That is right for somebody
// paying in and wrong for the platform returning its own funds: money swept
// before card payments went non-custodial, sent back to the account it came
// from, arrived as an ordinary transfer and was credited a second time.
//
// The watcher refuses those now, by sender. This is for the ones already
// counted.
//
// Never for a customer's own money. What somebody deposited is theirs, and the
// guard against using this on a real deposit is that the operator has to name
// the transaction and read back who it credits before anything moves.
//
// Dry by default.
//
//	go run ./cmd/reverse-deposit -tx 0x… -reason "…"
//	go run ./cmd/reverse-deposit -tx 0x… -reason "…" -apply
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

func main() {
	tx := flag.String("tx", "", "transaction hash of the deposit to reverse")
	reason := flag.String("reason", "", "why it should never have been credited")
	apply := flag.Bool("apply", false, "actually reverse it; without it nothing changes")
	flag.Parse()

	if err := run(context.Background(), *tx, *reason, *apply); err != nil {
		fmt.Fprintln(os.Stderr, "reverse-deposit:", err)
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

func run(ctx context.Context, txHash, reason string, apply bool) error {
	if txHash == "" || reason == "" {
		return fmt.Errorf("both -tx and -reason are required")
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
		id, user        uuid.UUID
		from, to, state string
		micro, logIdx   int64
		email           string
	)
	err = pool.QueryRow(ctx, `
		SELECT d.id, d.user_id, d.from_address, d.to_address, d.amount_micro,
		       d.log_index, d.state, coalesce(u.email, '')
		  FROM base_deposits d LEFT JOIN users u ON u.id = d.user_id
		 WHERE lower(d.tx_hash) = lower($1)`, txHash).
		Scan(&id, &user, &from, &to, &micro, &logIdx, &state, &email)
	if err != nil {
		return fmt.Errorf("read deposit %s: %w", txHash, err)
	}

	amount := money.New(micro/10_000, money.USD)

	// Everything an operator needs to see that this is the right row, before
	// anything moves. Reversing the wrong one takes a customer's money.
	fmt.Printf("deposit    %s\n", id)
	fmt.Printf("tx         %s (log %d)\n", txHash, logIdx)
	fmt.Printf("from       %s\n", from)
	fmt.Printf("to         %s\n", to)
	fmt.Printf("credited   %s to %s\n", amount, email)
	fmt.Printf("state      %s\n\n", state)

	if state != "credited" {
		return fmt.Errorf("deposit is %q, not credited; there is nothing to take back", state)
	}

	balance, err := ledger.Balance(ctx, pool, ledger.User(user), ledger.KindAvailable, money.USD)
	if err != nil {
		return err
	}
	fmt.Printf("their balance is %s; after reversing it would be ", balance)
	if after, err := balance.Sub(amount); err == nil {
		fmt.Printf("%s\n\n", after)
	} else {
		fmt.Printf("(cannot compute: %v)\n\n", err)
	}

	if !apply {
		fmt.Println("dry run — nothing changed. re-run with -apply to reverse it")
		return nil
	}

	reference := fmt.Sprintf("%s:%d", strings.ToLower(txHash), logIdx)
	err = movements.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := movements.DepositReversed(
			ctx, tx, user, amount, "base", reference, reason); err != nil {
			return err
		}
		// The row is marked, not deleted. A deposit that vanishes is a
		// transfer on chain that this system has no record of ever seeing,
		// and the next re-scan of those blocks would credit it again.
		_, err := tx.Exec(ctx, `
			UPDATE base_deposits
			   SET state = 'failed', last_error = $2
			 WHERE id = $1`, id, "reversed: "+reason)
		return err
	})
	if err != nil {
		return err
	}

	after, err := ledger.Balance(ctx, pool, ledger.User(user), ledger.KindAvailable, money.USD)
	if err != nil {
		return err
	}
	fmt.Printf("reversed. %s now holds %s\n", email, after)
	return nil
}
