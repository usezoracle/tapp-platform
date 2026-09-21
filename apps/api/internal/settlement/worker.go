package settlement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/baas"
)

// StaleAfter is how long a payout may sit in a non-final state before it is
// chased with the provider rather than left alone.
const StaleAfter = 2 * time.Minute

// MaxAttempts bounds retries of a temporary failure.
//
// Not unlimited: a payout that has failed twenty times is not going to succeed
// on the twenty-first, and leaving it in the queue means an operator never
// finds out. It goes to failed and the money returns.
const MaxAttempts = 10

// Worker pushes owed money out to banks.
type Worker struct {
	Pool *pgxpool.Pool
	// Rail is the bank provider. Nil means payouts cannot be delivered, and
	// the worker says so rather than marking anything failed -- an outage on
	// our side must not look like a refusal on theirs.
	Rail baas.Provider
	Now  func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Open reserves value in `payable` and queues a payout.
//
// The reservation and the payout row commit together. A reservation with no
// payout would be money owed that nothing ever delivers; a payout with no
// reservation would be a delivery of money nobody set aside.
func (w *Worker) Open(ctx context.Context, req Request) (*Payout, error) {
	var p *Payout
	err := movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
		var err error
		p, err = w.OpenIn(ctx, tx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// OpenIn is Open inside a caller's transaction.
//
// An order that debits somebody and raises a payout must do both or neither:
// the first alone is money taken and not sent, the second alone is money sent
// and not taken, and neither is recoverable by looking at the row afterwards.
func (w *Worker) OpenIn(ctx context.Context, tx pgx.Tx, req Request) (*Payout, error) {
	if err := req.Valid(); err != nil {
		return nil, err
	}

	p := &Payout{
		ID: uuid.New(), Beneficiary: req.Beneficiary, Amount: req.Amount,
		BankCode: req.BankCode, AccountNumber: req.AccountNumber,
		AccountName: req.AccountName, Narration: req.Narration,
		State: Pending, CreatedAt: w.now(),
	}

	var reserveTx uuid.UUID
	var err error

	switch req.Beneficiary.Kind {
	case Merchant:
		reserveTx, err = movements.MerchantSettled(ctx, tx, req.Beneficiary.ID, req.Amount, p.ID)
	case User:
		reserveTx, err = movements.Withdraw(ctx, tx, req.Beneficiary.ID, req.Amount,
			money.Zero(req.Amount.Currency()), p.ID)
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO payouts
			(id, beneficiary_kind, beneficiary_id, currency, amount_minor,
			 bank_code, account_number, account_name, narration, reserve_tx_id)
		VALUES ($1, $2, $3, $4::currency, $5, $6, $7, $8, $9, $10)`,
		p.ID, req.Beneficiary.Kind, req.Beneficiary.ID,
		string(req.Amount.Currency()), req.Amount.Minor(),
		req.BankCode, req.AccountNumber, req.AccountName,
		nullIfEmpty(req.Narration), reserveTx); err != nil {
		return nil, err
	}
	return p, nil
}

// Tick submits pending payouts and chases stale ones.
func (w *Worker) Tick(ctx context.Context) (submitted, chased int, err error) {
	if w.Rail == nil {
		return 0, 0, ErrNoRail
	}

	submitted, err = w.submitPending(ctx)
	if err != nil {
		return submitted, 0, err
	}
	chased, err = w.chaseStale(ctx)
	return submitted, chased, err
}

// BatchSize bounds how many payouts one tick handles, so a backlog is worked
// through steadily rather than in one long transaction.
const BatchSize = 50

// RetryBackoff is the base delay before a temporarily-failed payout is tried
// again, multiplied by the number of attempts so far.
//
// Without it, submitPending's loop re-claims a payout it just returned to
// pending and burns every attempt in a fraction of a second -- so one blip
// from the provider permanently fails a transfer that would have gone through
// a minute later. The backoff is what makes "retry" mean retry rather than
// "hammer until the attempt budget is gone".
const RetryBackoff = 30 * time.Second

// submitPending sends payouts that have not been sent.
//
// Each is claimed by moving it to `submitting` in a conditional UPDATE, so two
// workers racing cannot both send the same payout. That claim is the only
// thing standing between a horizontally scaled deployment and paying people
// twice.
func (w *Worker) submitPending(ctx context.Context) (int, error) {
	sent := 0
	for sent < BatchSize {
		p, err := w.claim(ctx)
		if err != nil {
			return sent, err
		}
		if p == nil {
			return sent, nil
		}
		if err := w.submit(ctx, p); err != nil {
			slog.Error("settlement: submit failed", "payout", p.ID, "err", err)
		}
		sent++
	}
	return sent, nil
}

// Abandon returns an undeliverable payout's money and marks it failed.
//
// For a payout that will never be sent -- a rail retired, an arrangement
// replaced -- rather than one a provider refused. The money goes back to the
// beneficiary either way, because they are owed it and the delivery did not
// happen; leaving it in `payable` would be the platform quietly keeping money
// it neither earned nor delivered.
//
// Refuses anything already confirmed. A confirmed payout moved real money, and
// returning its reservation would credit the beneficiary a second time for a
// transfer they have already received.
func (w *Worker) Abandon(ctx context.Context, id uuid.UUID, reason string) (*Payout, error) {
	if reason == "" {
		return nil, fmt.Errorf("settlement: abandoning a payout must say why")
	}

	var (
		p        Payout
		currency string
		minor    int64
	)
	err := w.Pool.QueryRow(ctx, `
		SELECT id, beneficiary_kind, beneficiary_id, currency, amount_minor, state
		  FROM payouts WHERE id = $1`, id).
		Scan(&p.ID, &p.Beneficiary.Kind, &p.Beneficiary.ID, &currency, &minor, &p.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("settlement: no payout %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("settlement: read payout: %w", err)
	}
	p.Amount = money.New(minor, money.Currency(currency))

	switch p.State {
	case Confirmed:
		return nil, fmt.Errorf("settlement: payout %s is confirmed; returning it would pay twice", id)
	case Failed:
		return nil, fmt.Errorf("settlement: payout %s has already been returned", id)
	}

	if err := w.fail(ctx, &p, reason); err != nil {
		return nil, err
	}
	return &p, nil
}

// claim takes one pending payout, exclusively.
//
// A payout that has already failed is not claimed again until its backoff has
// passed, which is what stops this loop from re-claiming what it just put
// back.
func (w *Worker) claim(ctx context.Context) (*Payout, error) {
	var (
		p         Payout
		currency  string
		minor     int64
		narration *string
	)
	err := w.Pool.QueryRow(ctx, `
		UPDATE payouts SET state = 'submitting', attempts = attempts + 1, updated_at = now()
		 WHERE id = (
			SELECT id FROM payouts
			 WHERE state = 'pending'
			   AND (attempts = 0 OR updated_at < now() - ($1::interval * attempts))
			 ORDER BY created_at
			 FOR UPDATE SKIP LOCKED
			 LIMIT 1)
		RETURNING id, beneficiary_kind, beneficiary_id, currency, amount_minor,
		          bank_code, account_number, account_name, narration, attempts`,
		RetryBackoff.String()).
		Scan(&p.ID, &p.Beneficiary.Kind, &p.Beneficiary.ID, &currency, &minor,
			&p.BankCode, &p.AccountNumber, &p.AccountName, &narration, &p.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settlement: claim payout: %w", err)
	}

	p.Amount = money.New(minor, money.Currency(currency))
	if narration != nil {
		p.Narration = *narration
	}
	p.State = Submitting
	return &p, nil
}

// submit sends one payout to the provider.
func (w *Worker) submit(ctx context.Context, p *Payout) error {
	// The bank is asked to confirm the name on the account before anything
	// moves. This is what stops money reaching a mistyped number that happens
	// to belong to somebody else, and the rail binds the transfer to the
	// enquiry so the two cannot drift apart.
	enquiry, err := w.Rail.NameEnquiry(ctx, p.BankCode, p.AccountNumber)
	if err != nil {
		return w.recordFailure(ctx, p, err)
	}
	if !strings.EqualFold(strings.TrimSpace(enquiry.AccountName), strings.TrimSpace(p.AccountName)) {
		// The account is not who it was when the payout was raised. Terminal:
		// retrying cannot make it the right account.
		return w.fail(ctx, p, fmt.Sprintf(
			"the account now belongs to %q, not %q", enquiry.AccountName, p.AccountName))
	}

	transfer, err := w.Rail.Transfer(ctx, baas.TransferRequest{
		NameEnquiryReference: enquiry.Reference,
		BeneficiaryBankCode:  p.BankCode,
		BeneficiaryAccount:   p.AccountNumber,
		Amount:               decimalFrom(p.Amount),
		Narration:            p.Narration,
		// Deterministic from the payout id, so a retry presents the same
		// reference and the rail refuses the duplicate instead of paying twice.
		PaymentReference: baas.PaymentReference("payout", p.ID.String()),
	})
	if err != nil {
		return w.recordFailure(ctx, p, err)
	}

	switch transfer.Status {
	case baas.TransferSuccess:
		return w.confirm(ctx, p, transfer.Reference)
	case baas.TransferFailed:
		return w.fail(ctx, p, transfer.Message)
	default:
		_, err := w.Pool.Exec(ctx, `
			UPDATE payouts SET state = 'sent', provider = $2, provider_ref = $3,
			       provider_session = $4, submitted_at = now(), updated_at = now()
			 WHERE id = $1`,
			p.ID, w.Rail.Name(), nullIfEmpty(transfer.Reference),
			nullIfEmpty(transfer.PaymentReference))
		return err
	}
}
