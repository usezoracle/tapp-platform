package base

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Addresses allocates and looks up per-user deposit addresses.
type Addresses struct {
	Pool    *pgxpool.Pool
	Deriver *Deriver

	// SmartAccounts, when set, makes every NEW address a CDP Smart Account
	// instead of a derived EOA. Addresses already issued keep their provider:
	// people have them saved as payees and they may hold funds.
	SmartAccounts SmartAccounts
}

// SmartAccount is a deposit address whose key lives with CDP, not here.
type SmartAccount struct {
	Address string
	Owner   string
	Name    string
}

// SmartAccounts is what the CDP integration provides. An interface so this
// package does not import it: base is the thing being extended, and the
// extension depends on it, not the other way round.
type SmartAccounts interface {
	EnsureSmartAccount(ctx context.Context, user uuid.UUID) (SmartAccount, error)
	SweepSmartAccount(ctx context.Context, account string, usdc, to common.Address,
		amount *big.Int, idem string) (txHash string, err error)
}

// Providers name the mechanism that produced an address, and therefore how
// it is swept. These mirror the CHECK constraint on the table.
const (
	ProviderDerived = "derived"
	ProviderCDP     = "cdp"
)

// ErrAddressMismatch means the stored address does not match what the seed
// derives for that index.
//
// This is the check that catches a changed seed. If it ever fires, deposits
// are being sent to addresses this deployment cannot sweep, and continuing
// would keep showing people an address whose funds are unreachable.
var ErrAddressMismatch = errors.New("base: stored address does not match the configured seed")

// ErrNoSmartAccounts means CDP is not configured, so no address can be issued.
//
// Deliberately an error rather than a fallback to the seed. Falling back is
// how a deployment quietly goes on minting addresses under the scheme it was
// migrated off: nobody notices, because a derived address works perfectly
// until the day the seed has to be produced. An outage that says "we could not
// get you an address, do not send anything yet" is recoverable; a silently
// wrong address is not.
var ErrNoSmartAccounts = errors.New("base: CDP is not configured, so no deposit address can be issued")

// For returns a user's deposit address, issuing one on first use.
//
// Every address comes from CDP. A user still holding a seed-derived address is
// retired and reissued here, on the next read, so the migration that retired
// them in bulk is a head start rather than the only path -- a row it missed,
// or one written by an older binary mid-deploy, heals itself the first time
// anybody looks.
//
// Retiring does not unwatch. The old address keeps its row, so money sent to
// it by somebody who saved it as a payee still credits, and the seed can still
// sweep it. What changes is only which address is handed out next.
func (a *Addresses) For(ctx context.Context, user uuid.UUID) (string, error) {
	address, provider, ok, err := a.current(ctx, a.Pool, user)
	if err != nil {
		return "", err
	}
	if ok && provider == ProviderCDP {
		return address, nil
	}
	if a.SmartAccounts == nil {
		return "", ErrNoSmartAccounts
	}
	if ok {
		// A legacy derived address is still current. Retire it before issuing
		// its replacement: the partial unique index allows exactly one current
		// address per person, and the insert would otherwise lose to it.
		if err := a.retire(ctx, address); err != nil {
			return "", err
		}
	}
	return a.allocateSmartAccount(ctx, user)
}

// Current is the user's address that is still being handed out, if any.
//
// Exported for callers that need to know which account holds a person's funds
// without issuing one -- settling a card tap sells from it, and a tap must
// never be the thing that mints an address.
func (a *Addresses) Current(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, user uuid.UUID) (address, provider string, ok bool, err error) {
	return a.current(ctx, q, user)
}

// current returns the user's address that is still being handed out.
//
// Retired rows are excluded here and only here. Everything else that reads
// this table -- the watcher's filter, the ownership lookup, the sweeper --
// wants every address ever issued, because money does not stop arriving at an
// address just because it is no longer advertised.
func (a *Addresses) current(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, user uuid.UUID) (address, provider string, ok bool, err error) {
	var index *int64
	err = q.QueryRow(ctx, `
		SELECT address, provider, index
		  FROM base_deposit_addresses
		 WHERE user_id = $1 AND retired_at IS NULL`, user).
		Scan(&address, &provider, &index)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("base: read deposit address: %w", err)
	}
	return address, provider, true, nil
}

// retire stops an address being handed out, leaving it watched and sweepable.
func (a *Addresses) retire(ctx context.Context, address string) error {
	_, err := a.Pool.Exec(ctx, `
		UPDATE base_deposit_addresses SET retired_at = now()
		 WHERE address = $1 AND retired_at IS NULL`, strings.ToLower(address))
	if err != nil {
		return fmt.Errorf("base: retire address %s: %w", address, err)
	}
	return nil
}

// allocateSmartAccount asks CDP for the account, then records it.
//
// The CDP call is made OUTSIDE the database transaction. It is a network
// round trip that may take seconds, and holding a row lock across it would
// serialise every first-time deposit behind the slowest one. Two requests
// racing here both reach CDP, which is safe -- EnsureSmartAccount is
// idempotent by name -- and the second INSERT loses on the primary key and
// re-reads the winner.
func (a *Addresses) allocateSmartAccount(ctx context.Context, user uuid.UUID) (string, error) {
	acct, err := a.SmartAccounts.EnsureSmartAccount(ctx, user)
	if err != nil {
		return "", err
	}
	if !common.IsHexAddress(acct.Address) || !common.IsHexAddress(acct.Owner) || acct.Name == "" {
		return "", fmt.Errorf("base: CDP returned an incomplete smart account for %s", user)
	}

	_, err = a.Pool.Exec(ctx, `
		INSERT INTO base_deposit_addresses (user_id, provider, address, owner_address, account_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (address) DO NOTHING`,
		user, ProviderCDP, strings.ToLower(acct.Address), strings.ToLower(acct.Owner), acct.Name)
	if err != nil {
		return "", fmt.Errorf("base: record smart account: %w", err)
	}

	address, _, ok, err := a.current(ctx, a.Pool, user)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("base: smart account for %s was not recorded", user)
	}
	return address, nil
}

// The seed no longer issues addresses. The Deriver is kept because it still
// has to SPEND the ones it issued: every retired derived address is swept with
// a key derived from it, and verify below is what refuses to touch one the
// current seed does not produce.

// verify re-derives a stored address and refuses to hand out one the seed does
// not produce.
func (a *Addresses) verify(stored string, index uint32) (string, error) {
	derived, err := a.Deriver.Address(index)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(stored, derived.Hex()) {
		// The seed has changed. Every address in this table is now unsweepable
		// and showing another one would add to the pile.
		return "", fmt.Errorf("%w: index %d is stored as %s but derives %s",
			ErrAddressMismatch, index, stored, derived.Hex())
	}
	return stored, nil
}

// Owner finds which user an address belongs to.
func (a *Addresses) Owner(ctx context.Context, address string) (uuid.UUID, uint32, error) {
	var user uuid.UUID
	var index *int64
	err := a.Pool.QueryRow(ctx,
		`SELECT user_id, index FROM base_deposit_addresses WHERE address = $1`,
		strings.ToLower(address)).Scan(&user, &index)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, 0, pgx.ErrNoRows
	}
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("base: find address owner: %w", err)
	}
	if index == nil {
		return user, 0, nil // a smart account; it has no index
	}
	return user, uint32(*index), nil
}

// All returns every allocated address, for the watcher's filter.
func (a *Addresses) All(ctx context.Context) (map[string]uuid.UUID, error) {
	rows, err := a.Pool.Query(ctx, `SELECT address, user_id FROM base_deposit_addresses`)
	if err != nil {
		return nil, fmt.Errorf("base: list deposit addresses: %w", err)
	}
	defer rows.Close()

	out := map[string]uuid.UUID{}
	for rows.Next() {
		var address string
		var user uuid.UUID
		if err := rows.Scan(&address, &user); err != nil {
			return nil, err
		}
		out[strings.ToLower(address)] = user
	}
	return out, rows.Err()
}
