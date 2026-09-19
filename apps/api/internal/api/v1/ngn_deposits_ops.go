// Operator actions on naira deposit accounts.
//
// Two things went wrong with the first accounts this platform opened, and
// both are fixed here rather than by hand in the database:
//
//   - The bank name was stored as a placeholder ("Fintava partner bank"),
//     because the rail names the bank in a field the decoder did not read.
//     SetNGNDepositBank corrects a row; the read path substitutes the
//     configured bank until then.
//
//   - Fintava delivers its webhook to one URL, and that URL was another
//     system's. Credits that landed before forwarding was in place were never
//     posted. ReconcileNGNDeposit reads the wallet's balance -- STATIC_FUND
//     wallets hold what they receive until somebody transfers it out, and
//     nothing in this codebase transfers out of them, so the balance IS the
//     sum of deposits -- and posts whatever the ledger is short.
//
// All of it is reached through the admin console (controllers/admin) and
// audited there; nothing here decides who may call it.

package v1

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/baas"
	"github.com/usezoracle/tapp/api/storage"
	"github.com/usezoracle/tapp/api/utils/logger"
)

// ErrNGNAccountNotFound is returned when no row holds the account number.
var ErrNGNAccountNotFound = errors.New("ngn deposits: no such account")

// NGNAccountRow is the operator's view of a row: everything stored, plus the
// owner's email, with the bank shown exactly as it is stored -- an operator
// deciding whether to correct a row needs to see the placeholder, not the
// substitute a cardholder is shown.
type NGNAccountRow struct {
	UserID        uuid.UUID `json:"user_id"`
	Email         string    `json:"email"`
	Rail          string    `json:"rail"`
	AccountNumber string    `json:"account_number"`
	BankName      string    `json:"bank_name"`
	BankCode      string    `json:"bank_code"`
	AccountName   string    `json:"account_name"`
	RailRef       string    `json:"rail_ref"`
	WalletID      string    `json:"wallet_id"`
	CreatedAt     time.Time `json:"created_at"`
	// NeedsBankFix says the stored name is the placeholder or blank: what a
	// cardholder sees is the configured fallback, and the row should be set
	// properly.
	NeedsBankFix bool `json:"needs_bank_fix"`
}

const ngnAccountRowSelect = `
	SELECT a.user_id, coalesce(u.email, ''), a.rail, a.account_number,
	       a.bank_name, a.bank_code, a.account_name, a.rail_ref, a.wallet_id, a.created_at
	  FROM ngn_deposit_accounts a
	  LEFT JOIN users u ON u.id = a.user_id`

func scanNGNAccountRow(row pgx.Row) (*NGNAccountRow, error) {
	var r NGNAccountRow
	if err := row.Scan(&r.UserID, &r.Email, &r.Rail, &r.AccountNumber,
		&r.BankName, &r.BankCode, &r.AccountName, &r.RailRef, &r.WalletID, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.NeedsBankFix = strings.TrimSpace(r.BankName) == "" ||
		strings.EqualFold(strings.TrimSpace(r.BankName), legacyBankPlaceholder)
	return &r, nil
}

// NGNAccountByNumber reads one row.
func NGNAccountByNumber(ctx context.Context, accountNumber string) (*NGNAccountRow, error) {
	r, err := scanNGNAccountRow(storage.Pool.QueryRow(ctx,
		ngnAccountRowSelect+` WHERE a.account_number = $1`, strings.TrimSpace(accountNumber)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNGNAccountNotFound
	}
	return r, err
}

// NGNAccountsByEmail finds a person's account by the email on their user
// record. Case-insensitive: an operator is typing what somebody told them.
// One account per person, so at most one row -- a slice only because an
// exact email can, in principle, match nothing.
func NGNAccountsByEmail(ctx context.Context, email string) ([]*NGNAccountRow, error) {
	rows, err := storage.Pool.Query(ctx,
		ngnAccountRowSelect+` WHERE lower(u.email) = lower($1) ORDER BY a.created_at`,
		strings.TrimSpace(email))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NGNAccountRow{}
	for rows.Next() {
		r, err := scanNGNAccountRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetNGNDepositBank writes the bank a row should have said all along.
func SetNGNDepositBank(ctx context.Context, accountNumber, bankName, bankCode string) (*NGNAccountRow, error) {
	bankName, bankCode = strings.TrimSpace(bankName), strings.TrimSpace(bankCode)
	if bankName == "" || strings.EqualFold(bankName, legacyBankPlaceholder) {
		return nil, errors.New("ngn deposits: a real bank name is required")
	}
	tag, err := storage.Pool.Exec(ctx, `
		UPDATE ngn_deposit_accounts SET bank_name = $2, bank_code = $3
		 WHERE account_number = $1`, strings.TrimSpace(accountNumber), bankName, bankCode)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNGNAccountNotFound
	}
	return NGNAccountByNumber(ctx, accountNumber)
}

// NGNReconcileResult is what a reconciliation run found and did.
type NGNReconcileResult struct {
	AccountNumber string `json:"account_number"`
	// WalletBalance is what the rail holds for this account right now.
	WalletBalance money.Amount `json:"wallet_balance"`
	// CreditedBefore is what the ledger had already posted to the owner from
	// this rail, before this run.
	CreditedBefore money.Amount `json:"credited_before"`
	// Posted is what this run added: the shortfall, or zero.
	Posted money.Amount `json:"posted"`
	// Reference is the ledger reference of what was posted; empty when
	// nothing was.
	Reference string `json:"reference,omitempty"`
	// Note explains a zero Posted in words an operator can act on.
	Note string `json:"note,omitempty"`
	// WalletID is the handle the balance was read with, so an operator can
	// see that a row opened without one has now been filled in.
	WalletID string `json:"wallet_id"`
}

// ReconcileNGNDeposit brings the ledger up to what the rail's wallet holds.
//
// The wallet balance is the total ever deposited: STATIC_FUND wallets keep
// funds until transferred out, and nothing here transfers out of them. So the
// shortfall against what the ledger has credited from this rail is exactly
// the set of credits whose webhook never arrived, and it is posted as one
// deposit with a reference that names the account, the balance it was read
// at, and the day. Running it again finds no shortfall and posts nothing; two
// runs racing on the same day present the same reference and the ledger
// refuses the second.
//
// A balance below what was credited is reported, not corrected: it means
// money left the wallet by a path this system does not know about, and that
// is a question for a person, not a debit.
func ReconcileNGNDeposit(ctx context.Context, rail baas.Provider, accountNumber string) (*NGNReconcileResult, error) {
	row, err := NGNAccountByNumber(ctx, accountNumber)
	if err != nil {
		return nil, err
	}
	if rail == nil {
		return nil, errors.New("ngn deposits: no rail configured")
	}
	if rail.Name() != row.Rail {
		return nil, fmt.Errorf("ngn deposits: account %s was opened on %s, the configured rail is %s",
			row.AccountNumber, row.Rail, rail.Name())
	}

	walletID, err := ensureWalletID(ctx, rail, row)
	if err != nil {
		return nil, err
	}

	acct, err := rail.GetAccount(ctx, walletID)
	if err != nil {
		return nil, fmt.Errorf("ngn deposits: wallet balance of %s (wallet=%s): %w", row.AccountNumber, walletID, err)
	}
	balance, err := nairaToMinor(acct.Balance)
	if err != nil {
		return nil, err
	}

	credited, err := creditedFromRail(ctx, row.UserID, row.Rail)
	if err != nil {
		return nil, err
	}

	res := &NGNReconcileResult{
		AccountNumber:  row.AccountNumber,
		WalletBalance:  balance,
		CreditedBefore: credited,
		Posted:         money.Zero(money.NGN),
		WalletID:       walletID,
	}
	shortfall, err := balance.Sub(credited)
	if err != nil {
		return nil, err
	}
	switch {
	case shortfall.IsZero():
		res.Note = "ledger already matches the wallet"
		return res, nil
	case shortfall.IsNegative():
		res.Note = fmt.Sprintf("wallet holds %s less than the ledger has credited; nothing posted -- "+
			"money left the wallet by a path this system did not record", shortfall.Abs())
		return res, nil
	}

	reference := fmt.Sprintf("reconcile:%s:%d:%s",
		row.AccountNumber, balance.Minor(), time.Now().UTC().Format("2006-01-02"))
	posted, err := postNGNDeposit(ctx, row.UserID, row.AccountNumber, shortfall, row.Rail, reference)
	if err != nil {
		return nil, err
	}
	res.Reference = reference
	if posted {
		res.Posted = shortfall
	} else {
		res.Note = "a run with this reference already posted today"
	}
	return res, nil
}

// ensureWalletID returns the handle the rail reads balances with, finding
// and recording it for rows opened before it was stored.
func ensureWalletID(ctx context.Context, rail baas.Provider, row *NGNAccountRow) (string, error) {
	if row.WalletID != "" {
		return row.WalletID, nil
	}
	locator, ok := rail.(baas.WalletLocator)
	if !ok {
		return "", fmt.Errorf("ngn deposits: account %s has no wallet id recorded and rail %s cannot look one up",
			row.AccountNumber, rail.Name())
	}
	if row.Email == "" {
		return "", fmt.Errorf("ngn deposits: account %s has no wallet id and its owner has no email to search by",
			row.AccountNumber)
	}
	walletID, err := locator.LocateWallet(ctx, row.Email, row.AccountNumber)
	if err != nil {
		return "", fmt.Errorf("ngn deposits: locate wallet for %s: %w", row.AccountNumber, err)
	}
	if _, err := storage.Pool.Exec(ctx,
		`UPDATE ngn_deposit_accounts SET wallet_id = $2 WHERE account_number = $1 AND wallet_id = ''`,
		row.AccountNumber, walletID); err != nil {
		return "", fmt.Errorf("ngn deposits: record wallet id for %s: %w", row.AccountNumber, err)
	}
	logger.Infof("ngn deposits: recorded wallet %s for account %s", walletID, row.AccountNumber)
	row.WalletID = walletID
	return walletID, nil
}

// creditedFromRail sums what the ledger has already credited to the person
// from this rail: every deposit whose idempotency key names the rail as its
// source, which is how CreditNGNDeposit and this file both post.
func creditedFromRail(ctx context.Context, user uuid.UUID, rail string) (money.Amount, error) {
	var minor int64
	err := storage.Pool.QueryRow(ctx, `
		SELECT coalesce(sum(e.amount_minor), 0)
		  FROM ledger_entries e
		  JOIN ledger_transactions t ON t.id = e.tx_id
		  JOIN ledger_accounts a ON a.id = e.account_id
		 WHERE a.owner_id = $1 AND a.owner_kind = 'user' AND a.kind = 'available'
		   AND a.currency = 'NGN'
		   AND t.ref_type = 'deposit' AND t.idem_key LIKE 'deposit:' || $2 || ':%'
		   AND e.amount_minor > 0`, user, rail).Scan(&minor)
	if err != nil {
		return money.Amount{}, fmt.Errorf("ngn deposits: credited so far for %s: %w", user, err)
	}
	return money.New(minor, money.NGN), nil
}

// nairaToMinor converts a rail balance to kobo. Zero is a balance; a
// fraction of a kobo is not, and is refused for the same reason
// nairaFromRail refuses it.
func nairaToMinor(d decimal.Decimal) (money.Amount, error) {
	if d.IsNegative() {
		return money.Amount{}, fmt.Errorf("ngn deposits: the rail reports a negative balance %s", d)
	}
	minor := d.Mul(decimal.NewFromInt(money.NGN.Scale()))
	if !minor.Equal(minor.Truncate(0)) {
		return money.Amount{}, fmt.Errorf("ngn deposits: %s naira is not a whole number of kobo", d)
	}
	return money.New(minor.IntPart(), money.NGN), nil
}
