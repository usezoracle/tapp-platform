package yield

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/chain/cdp"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

// Sender submits sponsored calls from a smart account. Satisfied by
// cdp.Client.
type Sender interface {
	SendCalls(ctx context.Context, account string, calls []cdp.Call, idem string) (string, error)
}

// DefaultSweepMin is the least worth a sponsored deposit: 1 USDC.
const DefaultSweepMin = 1_000_000

// staleAfter is how long a 'sending' row waits before it is sent again under
// its own key. Long enough that it is not the attempt still in flight.
const staleAfter = 2 * time.Minute

// microPerCent is how many USDC micro-units make the ledger's one cent.
const microPerCent = 10_000

// Service moves cardholders' USDC between their accounts and the vault, and
// credits what the vault earns to their ledger.
//
// Releasing and accruing run whenever a vault is configured. Sweeping -- the
// only thing that puts money in -- runs only when Sweeping is set, so turning
// yield off stops new deposits and never strands money already in the vault.
type Service struct {
	Pool   *pgxpool.Pool
	Vault  *Vault
	Sender Sender

	Sweeping bool
	// SweepMin is the least an account must hold before it is swept. Zero
	// means DefaultSweepMin.
	SweepMin int64
	// Users limits sweeping to these cardholders; empty means everyone.
	Users map[uuid.UUID]bool

	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) sweepMin() int64 {
	if s.SweepMin > 0 {
		return s.SweepMin
	}
	return DefaultSweepMin
}

// Ensure makes sure an account holds need USDC outside the vault before
// something is sent from it, releasing the shortfall from the vault when it
// does not. reference is the spend's own key, so a retried spend finds the
// release it already made rather than making another.
//
// The release is its own operation, never batched with the spend: CDP sizes
// an operation's gas itself with too little margin for a vault withdrawal
// plus a transfer, and the pair runs out of gas on chain.
func (s *Service) Ensure(ctx context.Context, account string, need int64, reference string) error {
	acct := common.HexToAddress(account)

	// The latest release made for this spend, if any. Every attempt after a
	// failed one has a key of its own (release:<spend>:<n>), so it is a new
	// operation rather than the failed one answered again.
	ref := "release:" + reference
	var last, state string
	var attempts int
	err := s.Pool.QueryRow(ctx, `
		SELECT reference, state, count(*) OVER ()
		  FROM vault_movements
		 WHERE reference = $1 OR reference LIKE $1 || ':%'
		 ORDER BY created_at DESC LIMIT 1`, ref).Scan(&last, &state, &attempts)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("yield: read release %s: %w", ref, err)
	}
	if state == "sending" {
		// Sent before and the answer lost: finish that one first. Its USDC
		// may be in the wallet already, but until it is recorded the
		// position is unknown and nothing is credited.
		if err := s.resend(ctx, last); err != nil {
			return err
		}
		state = "confirmed"
	}

	have, err := s.Vault.Wallet(ctx, acct)
	if err != nil {
		return err
	}
	if have >= need {
		return nil
	}
	short := need - have
	switch state {
	case "confirmed":
		return fmt.Errorf("yield: %s released for %s already and still holds %d of the %d it must send",
			account, reference, have, need)
	case "failed":
		ref = fmt.Sprintf("%s:%d", ref, attempts)
	}

	available, err := s.Vault.Withdrawable(ctx, acct)
	if err != nil {
		return err
	}
	if available < short {
		return fmt.Errorf("yield: %s must send %d, holds %d, and the vault can release %d of the %d short",
			account, need, have, available, short)
	}
	user, err := s.owner(ctx, account)
	if err != nil {
		return err
	}
	return s.move(ctx, user, acct, "release", short, ref)
}

// Tick finishes any operation whose answer was lost, sweeps every account
// that is idle, and credits what was earned. Each step is logged and the
// next one runs whatever the last one did.
func (s *Service) Tick(ctx context.Context) {
	if n, err := s.Resolve(ctx); err != nil {
		slog.Error("yield: resolve", "err", err)
	} else if n > 0 {
		slog.Info("yield: operations resolved", "count", n)
	}
	if s.Sweeping {
		if n, err := s.Sweep(ctx); err != nil {
			slog.Error("yield: sweep", "err", err)
		} else if n > 0 {
			slog.Info("yield: accounts swept", "count", n)
		}
	}
	if n, err := s.Accrue(ctx); err != nil {
		slog.Error("yield: accrue", "err", err)
	} else if n > 0 {
		slog.Info("yield: credited", "count", n)
	}
}

// Run ticks on a timer.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

// Resolve sends again every operation still 'sending' after staleAfter,
// under its own key, so it either lands once or is answered as it already
// did.
func (s *Service) Resolve(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT reference FROM vault_movements
		 WHERE state = 'sending' AND updated_at < $1
		 ORDER BY created_at LIMIT 20`, s.now().Add(-staleAfter))
	if err != nil {
		return 0, err
	}
	refs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	done := 0
	for _, ref := range refs {
		if err := s.resend(ctx, ref); err != nil {
			slog.Warn("yield: still unresolved", "reference", ref, "err", err)
			continue
		}
		done++
	}
	return done, nil
}

// Sweep deposits each idle account's USDC into the vault.
//
// An account is idle when nothing of its holder's is about to leave it: no
// tap waiting to fund its payout, no withdrawal waiting to send, no vault
// operation unresolved. A spend that starts after this check finds its
// money in the vault and Ensure releases it.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.user_id, a.address
		  FROM base_deposit_addresses a
		 WHERE a.retired_at IS NULL AND a.provider = 'cdp'
		   AND NOT EXISTS (
		         SELECT 1 FROM card_tap_settlements st JOIN card_taps t ON t.id = st.tap_id
		          WHERE t.cardholder_id = a.user_id AND st.state = 'pending')
		   AND NOT EXISTS (
		         SELECT 1 FROM base_withdrawals w
		          WHERE w.user_id = a.user_id AND w.state IN ('pending', 'sending'))
		   AND NOT EXISTS (
		         SELECT 1 FROM vault_movements m
		          WHERE m.user_id = a.user_id AND m.state = 'sending')`)
	if err != nil {
		return 0, fmt.Errorf("yield: idle accounts: %w", err)
	}
	type idle struct {
		user    uuid.UUID
		account string
	}
	var due []idle
	for rows.Next() {
		var i idle
		if err := rows.Scan(&i.user, &i.account); err != nil {
			rows.Close()
			return 0, err
		}
		if len(s.Users) == 0 || s.Users[i.user] {
			due = append(due, i)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	swept := 0
	for _, i := range due {
		acct := common.HexToAddress(i.account)
		held, err := s.Vault.Wallet(ctx, acct)
		if err != nil {
			slog.Warn("yield: could not read wallet", "account", i.account, "err", err)
			continue
		}
		if held < s.sweepMin() {
			continue
		}
		if err := s.move(ctx, i.user, acct, "deposit", held, "deposit:"+uuid.NewString()); err != nil {
			slog.Warn("yield: sweep failed", "account", i.account, "amount", held, "err", err)
			continue
		}
		swept++
	}
	return swept, nil
}

// Accrue credits each holder what their vault position earned since it was
// last credited, at most once a UTC day, in whole cents.
//
//	principal = confirmed deposits - confirmed releases
//	earned    = vault value - principal
//	due       = earned - already credited
//
// A holder with an operation unresolved is skipped: until it is, the
// principal is not known, and a deposit counted as missing would be credited
// as yield. A position worth less than it was credited for is a loss, and is
// reported and left for an operator rather than taken back from anyone.
func (s *Service) Accrue(ctx context.Context) (int, error) {
	day := s.now().UTC().Format("2006-01-02")
	rows, err := s.Pool.Query(ctx, `
		SELECT m.user_id, array_agg(DISTINCT m.account),
		       COALESCE(SUM(m.amount_micro) FILTER (WHERE m.kind = 'deposit'), 0)
		     - COALESCE(SUM(m.amount_micro) FILTER (WHERE m.kind = 'release'), 0),
		       COALESCE((SELECT SUM(c.amount_micro) FROM yield_credits c WHERE c.user_id = m.user_id), 0)
		  FROM vault_movements m
		 WHERE m.state = 'confirmed'
		   AND NOT EXISTS (SELECT 1 FROM vault_movements p
		                    WHERE p.user_id = m.user_id AND p.state = 'sending')
		   AND NOT EXISTS (SELECT 1 FROM yield_credits c
		                    WHERE c.user_id = m.user_id AND c.day = $1::date)
		 GROUP BY m.user_id`, day)
	if err != nil {
		return 0, fmt.Errorf("yield: positions: %w", err)
	}
	type position struct {
		user      uuid.UUID
		accounts  []string
		principal int64
		credited  int64
	}
	var positions []position
	for rows.Next() {
		var p position
		if err := rows.Scan(&p.user, &p.accounts, &p.principal, &p.credited); err != nil {
			rows.Close()
			return 0, err
		}
		positions = append(positions, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	credited := 0
	for _, p := range positions {
		var value int64
		readable := true
		for _, a := range p.accounts {
			v, err := s.Vault.Value(ctx, common.HexToAddress(a))
			if err != nil {
				slog.Warn("yield: could not read position", "user", p.user, "account", a, "err", err)
				readable = false
				break
			}
			value += v
		}
		if !readable {
			continue
		}
		earned := value - p.principal
		if earned < p.credited-microPerCent {
			slog.Error("yield: position is worth less than was credited; crediting stopped for an operator",
				"user", p.user, "value", value, "principal", p.principal,
				"earned", earned, "credited", p.credited)
			continue
		}
		cents := (earned - p.credited) / microPerCent
		if cents <= 0 {
			continue
		}
		ok, err := s.credit(ctx, p.user, day, cents, value, p.principal)
		if err != nil {
			slog.Error("yield: credit failed", "user", p.user, "err", err)
			continue
		}
		if ok {
			credited++
		}
	}
	return credited, nil
}

// credit records one day's yield and posts it to the holder's ledger,
// together or not at all.
func (s *Service) credit(ctx context.Context, user uuid.UUID, day string, cents, value, principal int64) (bool, error) {
	posted := false
	err := movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO yield_credits (user_id, day, amount_micro, vault_value_micro, principal_micro)
			VALUES ($1, $2::date, $3, $4, $5)
			ON CONFLICT (user_id, day) DO NOTHING`,
			user, day, cents*microPerCent, value, principal)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if _, err := movements.YieldCredited(ctx, tx, user, money.New(cents, money.USD), day); err != nil {
			return err
		}
		posted = true
		return nil
	})
	return posted, err
}

// move records a vault operation, sends it, and records how it ended. The
// row is written first, so an operation whose answer is lost is still known
// about and is sent again under the same key.
func (s *Service) move(ctx context.Context, user uuid.UUID, account common.Address, kind string, amount int64, ref string) error {
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO vault_movements (reference, user_id, account, kind, amount_micro)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (reference) DO NOTHING`,
		ref, user, strings.ToLower(account.Hex()), kind, amount); err != nil {
		return fmt.Errorf("yield: record %s: %w", ref, err)
	}
	return s.resend(ctx, ref)
}

// resend sends a recorded operation under its own key and records the end.
// The calls are rebuilt from the row, so a resend carries exactly what the
// first send did.
func (s *Service) resend(ctx context.Context, ref string) error {
	var account, kind, state string
	var amount int64
	if err := s.Pool.QueryRow(ctx, `
		SELECT account, kind, amount_micro, state FROM vault_movements WHERE reference = $1`, ref).
		Scan(&account, &kind, &amount, &state); err != nil {
		return fmt.Errorf("yield: read %s: %w", ref, err)
	}
	if state != "sending" {
		return nil
	}
	acct := common.HexToAddress(account)
	calls, err := s.Vault.WithdrawCalls(acct, amount)
	if kind == "deposit" {
		calls, err = s.Vault.DepositCalls(acct, amount)
	}
	if err != nil {
		return err
	}

	txHash, err := s.Sender.SendCalls(ctx, account, calls, ref)
	var failed *cdp.OperationFailedError
	switch {
	case errors.As(err, &failed):
		if _, uerr := s.Pool.Exec(ctx, `
			UPDATE vault_movements SET state = 'failed', error = $2, updated_at = now()
			 WHERE reference = $1 AND state = 'sending'`, ref, err.Error()); uerr != nil {
			return uerr
		}
		return err
	case err != nil:
		// Not known to have failed: it may yet land. Left 'sending'.
		if _, uerr := s.Pool.Exec(ctx, `
			UPDATE vault_movements SET error = $2, updated_at = now()
			 WHERE reference = $1 AND state = 'sending'`, ref, err.Error()); uerr != nil {
			return uerr
		}
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE vault_movements SET state = 'confirmed', tx_hash = $2, error = NULL, updated_at = now()
		 WHERE reference = $1 AND state = 'sending'`, ref, txHash)
	return err
}

func (s *Service) owner(ctx context.Context, account string) (uuid.UUID, error) {
	var user uuid.UUID
	err := s.Pool.QueryRow(ctx,
		`SELECT user_id FROM base_deposit_addresses WHERE address = $1`,
		strings.ToLower(account)).Scan(&user)
	if err != nil {
		return uuid.Nil, fmt.Errorf("yield: owner of %s: %w", account, err)
	}
	return user, nil
}
