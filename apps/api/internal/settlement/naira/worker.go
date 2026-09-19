package naira

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/baas"
)

// BatchSize bounds how many settlements one tick pays.
const BatchSize = 20

// StaleAfter is how long a submitted settlement may go without an answer
// before the rail is asked what happened to it.
const StaleAfter = 2 * time.Minute

// Worker pays queued naira legs out of cardholders' wallets and follows them
// to an outcome.
// Execer is what a settlement hook needs of the database: a pool or a
// transaction, whichever the caller holds.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Worker struct {
	Pool *pgxpool.Pool
	// Rail is the bank provider the wallets live on. It must implement
	// baas.WalletTransferer for anything to be paid. Nil means nothing can
	// be paid; rows queue until one is configured.
	Rail baas.Provider
	// Resolver translates the catalogue code a merchant's account stores
	// into the code the rail knows the bank by. Nil means one built on the
	// rail's own bank list and no catalogue: codes the rail lists pass
	// through, everything else fails the leg as unmapped.
	Resolver *BankCodeResolver
	// Settled is told when a leg has been paid, so whatever waits on the
	// tap being fully settled — the stock the tap buys — can be released.
	// Nil means nothing waits.
	Settled func(ctx context.Context, q Execer, tapID uuid.UUID)
	// MerchantName gives the narration the merchant sees on their statement.
	// Nil, or an empty answer, falls back to a generic one.
	MerchantName func(ctx context.Context, merchant uuid.UUID) string
	// SweepFee is what the rail charges the sender of a wallet-to-wallet
	// transfer, on top of the amount. Zero means DefaultSweepFee. The rail
	// reports what it actually charged; a difference is logged.
	SweepFee money.Amount
	// BankFee is what the rail charges the platform's wallet for a bank
	// transfer, booked when the rail's answer does not carry the fee (it
	// does not: Fintava deducts it and reports nothing). Zero means
	// DefaultBankFee.
	BankFee money.Amount
	Now     func() time.Time
}

// DefaultSweepFee and DefaultBankFee are Fintava's flat charges as observed
// on its wallet balances: ₦15 to the sender of a wallet-to-wallet transfer,
// ₦30.75 to the merchant wallet for a transfer to a bank.
var (
	DefaultSweepFee = money.New(1500, money.NGN)
	DefaultBankFee  = money.New(3075, money.NGN)
)

func (w *Worker) sweepFee() money.Amount {
	if w.SweepFee.IsPositive() {
		return w.SweepFee
	}
	return DefaultSweepFee
}

func (w *Worker) bankFee() money.Amount {
	if w.BankFee.IsPositive() {
		return w.BankFee
	}
	return DefaultBankFee
}

// ErrNoRail means no provider is configured, or the configured one cannot
// sweep a customer wallet into the platform's and pay a bank from there.
var ErrNoRail = errors.New("naira: no bank rail that can sweep a wallet and pay a bank is configured")

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Tick pays what is queued and chases what is stale. paid counts the
// settlements the rail accepted or is still deciding on -- not the ones it
// refused.
func (w *Worker) Tick(ctx context.Context) (paid, chased int, err error) {
	if _, ok := w.Rail.(baas.WalletSweeper); w.Rail == nil || !ok {
		return 0, 0, ErrNoRail
	}
	if w.Resolver == nil {
		w.Resolver = &BankCodeResolver{Banks: w.Rail.ListBanks}
	}
	paid, err = w.payQueued(ctx)
	if err != nil {
		return paid, 0, err
	}
	chased, err = w.chaseStale(ctx)
	return paid, chased, err
}

// payQueued asks the rail for each queued settlement, oldest first.
//
// A reversed tap is never paid: the cardholder has their money back and the
// merchant's claim is withdrawn, so there is nothing to deliver.
func (w *Worker) payQueued(ctx context.Context) (int, error) {
	rows, err := w.Pool.Query(ctx, settlementSelect+`
		 WHERE state = 'queued'
		   AND NOT EXISTS (SELECT 1 FROM card_tap_reversals r WHERE r.tap_id = card_tap_ngn_settlements.tap_id)
		 ORDER BY created_at
		 LIMIT $1`, BatchSize)
	if err != nil {
		return 0, fmt.Errorf("naira: find queued settlements: %w", err)
	}
	var due []*Settlement
	for rows.Next() {
		s, err := scanSettlement(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	paid := 0
	for _, s := range due {
		// The rail's code for the merchant's bank, resolved afresh on every
		// attempt so a retry after the rail lists a bank, or after the
		// catalogue is corrected, picks the fix up. Resolved BEFORE the
		// claim: a leg nobody can address is failed from queued, with
		// nothing discharged, rather than sent to the rail as a code it
		// cannot read. A list that cannot be fetched leaves the row queued
		// for the next tick.
		code, _, err := w.Resolver.FintavaCode(ctx, s.BankCode)
		if err != nil {
			var unmapped *UnmappedBankError
			if errors.As(err, &unmapped) {
				if err := w.failQueued(ctx, s, unmapped.Error()); err != nil {
					slog.Error("naira: could not fail settlement", "tap", s.TapID, "err", err)
				}
				continue
			}
			slog.Warn("naira: could not resolve bank code; leaving queued", "tap", s.TapID, "bank_code", s.BankCode, "err", err)
			continue
		}
		s.FintavaBankCode = code
		claimed, err := w.claim(ctx, s)
		if err != nil {
			slog.Error("naira: could not claim settlement", "tap", s.TapID, "err", err)
			continue
		}
		if !claimed {
			continue
		}
		if err := w.submit(ctx, s); err != nil {
			slog.Error("naira: submit failed", "tap", s.TapID, "err", err)
			continue
		}
		if s.State == Failed {
			// Refused: nothing left the wallet.
			continue
		}
		paid++
	}
	return paid, nil
}

// claim moves a settlement to submitted and discharges this leg of what the
// merchant is owed, together -- the books say the cardholder's wallet has
// paid it, as they say a provider has paid the on-chain leg the moment its
// order is submitted. The conditional update is what stops two workers
// paying the same tap: only one of them finds the row still queued.
//
// A merchant whose payable no longer covers the amount -- a tap reversed
// between record and claim, or a claim discharged by another path -- is left
// failed with the reason, and nothing is discharged.
func (w *Worker) claim(ctx context.Context, s *Settlement) (bool, error) {
	claimed := false
	err := movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
		var attempts int
		err := tx.QueryRow(ctx, `
			UPDATE card_tap_ngn_settlements
			   SET state = 'submitted', attempts = attempts + 1, error = NULL,
			       fintava_bank_code = $2,
			       submitted_at = now(), updated_at = now()
			 WHERE tap_id = $1 AND state = 'queued'
			RETURNING attempts`, s.TapID, s.FintavaBankCode).Scan(&attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		s.Attempts = attempts
		s.State = Submitted
		_, err = movements.MerchantSettledFromWallet(ctx, tx, s.MerchantID, s.Amount, s.TapID, attempts)
		if err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if errors.Is(err, movements.ErrInsufficientFunds) {
		return false, w.failQueued(ctx, s, "the merchant is no longer owed this amount: "+err.Error())
	}
	return claimed, err
}

// failQueued fails a row that was never claimed: nothing was discharged, so
// there is nothing to return to the merchant, and nothing reached the rail.
func (w *Worker) failQueued(ctx context.Context, s *Settlement, reason string) error {
	tag, err := w.Pool.Exec(ctx, `
		UPDATE card_tap_ngn_settlements
		   SET state = 'failed', error = $2, updated_at = now()
		 WHERE tap_id = $1 AND state = 'queued'`, s.TapID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		s.State, s.Error = Failed, reason
		slog.Error("naira: settlement failed before it was sent; the merchant is still owed",
			"tap", s.TapID, "owed", s.Amount.String(), "why", reason)
	}
	return nil
}

// submit pays one claimed settlement, in two hops: the tap's naira is swept
// from the cardholder's wallet into the platform's, and the platform's
// wallet pays the merchant's bank. The rail's direct wallet-to-bank call
// answers a customer wallet with a bare 500, whatever it is sent; these two
// work. A row that was swept on an earlier attempt starts at the second hop.
func (w *Worker) submit(ctx context.Context, s *Settlement) error {
	// The bank confirms the name on the account before anything moves. The
	// account was verified when the merchant saved it; this is what catches
	// it having changed hands since.
	if s.FintavaBankCode == "" {
		// Cannot happen from payQueued, which resolves first; guards any
		// other caller from sending the catalogue's code to the rail.
		return w.fail(ctx, s, (&UnmappedBankError{StoredCode: s.BankCode}).Error())
	}
	enquiry, err := w.Rail.NameEnquiry(ctx, s.FintavaBankCode, s.AccountNumber)
	if err != nil {
		return w.recordError(ctx, s, err)
	}
	if !strings.EqualFold(strings.TrimSpace(enquiry.AccountName), strings.TrimSpace(s.AccountName)) {
		return w.fail(ctx, s, fmt.Sprintf(
			"the account now belongs to %q, not %q", enquiry.AccountName, s.AccountName))
	}

	if s.SweepRef == "" {
		if err := w.sweep(ctx, s); err != nil || s.SweepRef == "" {
			return err
		}
	}
	return w.payBank(ctx, s)
}

// sweep moves what the tap's naira paid, less the rail's sender fee, from
// the cardholder's wallet into the platform's: the wallet loses exactly what
// the cardholder's ledger was debited, fee included. The platform's wallet
// then holds the leg plus what is left of the scheme fee; a tap too small
// for its scheme fee to cover the sender fee sweeps less than the leg, and
// the platform's wallet makes up the difference out of its own fee reserve.
func (w *Worker) sweep(ctx context.Context, s *Settlement) error {
	sweeper, ok := w.Rail.(baas.WalletSweeper)
	if !ok {
		return w.fail(ctx, s, ErrNoRail.Error())
	}
	fee := w.sweepFee()
	amount, err := s.Funded.Sub(fee)
	if err != nil || !amount.IsPositive() {
		return w.fail(ctx, s, fmt.Sprintf(
			"the naira share %s does not cover the rail's %s sweep fee", s.Funded, fee))
	}
	if amount.Minor() < s.Amount.Minor() {
		slog.Warn("naira: the sweep falls short of the leg; the platform's wallet makes up the difference",
			"tap", s.TapID, "sweep", amount.String(), "leg", s.Amount.String(), "fee", fee.String())
	}

	// What the wallet holds, read before anything is sent. A wallet short
	// of the sweep and its fee by more than the fee is refused here with
	// the figures in the message, rather than sent to a rail whose answer
	// may say less: money has left it by a path this system did not
	// record, and that wants an operator. Short by less is dust between
	// the ledger and the wallet -- a rounding the ledger credited that the
	// rail never held -- and the sweep takes what is there, the platform's
	// wallet making up the rest, as it does for a small tap's fees. A
	// balance that cannot be read is not a reason to hold the payment;
	// what it held is written beside any error the rail gives.
	held := "unknown"
	if acct, err := w.Rail.GetAccount(ctx, s.SourceWalletID); err != nil {
		slog.Warn("naira: could not read wallet balance before sweeping", "tap", s.TapID, "err", err)
	} else if acct != nil {
		held = "₦" + acct.Balance.StringFixed(2)
		if short := decimalOf(s.Funded).Sub(acct.Balance); short.IsPositive() {
			if short.GreaterThan(decimalOf(fee)) {
				return w.fail(ctx, s, fmt.Sprintf(
					"the wallet holds %s; the sweep needs %s (%s plus the %s fee)", held, s.Funded, amount, fee))
			}
			amount = money.New(acct.Balance.Sub(decimalOf(fee)).Shift(2).Round(0).IntPart(), s.Amount.Currency())
			if !amount.IsPositive() {
				return w.fail(ctx, s, fmt.Sprintf(
					"the wallet holds %s; the sweep needs %s (%s plus the %s fee)", held, s.Funded, amount, fee))
			}
			slog.Warn("naira: the wallet is short of what the tap paid by dust; sweeping what it holds",
				"tap", s.TapID, "held", held, "funded", s.Funded.String(), "short", "₦"+short.StringFixed(2), "sweep", amount.String())
		}
	}

	receiver, err := sweeper.PlatformWalletAccount(ctx)
	if err != nil {
		return w.recordError(ctx, s, fmt.Errorf("sweep: %w", err))
	}
	res, err := sweeper.SweepToPlatform(ctx, baas.WalletSweepRequest{
		SenderAccount:    s.SourceAccountNumber,
		ReceiverAccount:  receiver,
		Amount:           decimalOf(amount),
		Narration:        w.narration(ctx, s.MerchantID),
		PaymentReference: SweepReference(s.Reference),
	})
	if err != nil {
		return w.recordError(ctx, s, fmt.Errorf("sweep: %w (the wallet held %s for a %s sweep)", err, held, amount))
	}
	if res.Status == baas.TransferFailed {
		msg := res.Message
		if msg == "" {
			msg = "the rail refused the sweep"
		}
		return w.fail(ctx, s, "sweep: "+msg)
	}
	// Accepted, or still deciding: either way the rail has it under a
	// reference, and money that lands late lands in our own wallet.
	return w.recordSweep(ctx, s, res.Reference, res.Fees)
}

// recordSweep writes the rail's acceptance of the sweep on the row. From
// here a retry starts at the bank transfer.
func (w *Worker) recordSweep(ctx context.Context, s *Settlement, ref string, fee decimal.Decimal) error {
	if ref == "" {
		ref = SweepReference(s.Reference)
	}
	charged := w.sweepFee()
	if !fee.IsZero() {
		charged = money.New(fee.Shift(2).Round(0).IntPart(), s.Amount.Currency())
		if charged.Minor() != w.sweepFee().Minor() {
			slog.Warn("naira: the rail's sweep fee is not the one configured",
				"tap", s.TapID, "charged", charged.String(), "configured", w.sweepFee().String())
		}
	}
	_, err := w.Pool.Exec(ctx, `
		UPDATE card_tap_ngn_settlements
		   SET sweep_ref = $2, swept_at = now(), sweep_fee_minor = $3, error = NULL, updated_at = now()
		 WHERE tap_id = $1 AND state = 'submitted'`, s.TapID, ref, charged.Minor())
	if err != nil {
		return err
	}
	s.SweepRef, s.SweepFee = ref, charged
	now := w.now()
	s.SweptAt = &now
	return nil
}

// payBank pays the merchant's bank from the platform's wallet, under the
// leg's own reference.
func (w *Worker) payBank(ctx context.Context, s *Settlement) error {
	transfer, err := w.Rail.Transfer(ctx, baas.TransferRequest{
		BeneficiaryBankCode: s.FintavaBankCode,
		BeneficiaryAccount:  s.AccountNumber,
		Amount:              decimalOf(s.Amount),
		Narration:           w.narration(ctx, s.MerchantID),
		PaymentReference:    s.Reference,
	})
	if err != nil {
		return w.recordError(ctx, s, err)
	}
	return w.apply(ctx, s, transfer.Reference, transfer.Status, transfer.Message, transfer.Fees)
}

// narration is what the cardholder's statement says the money went to.
func (w *Worker) narration(ctx context.Context, merchant uuid.UUID) string {
	if w.MerchantName != nil {
		if name := strings.TrimSpace(w.MerchantName(ctx, merchant)); name != "" {
			return "Tapp: " + name
		}
	}
	return "Tapp card payment"
}

// recordError decides what a rail error means for the row.
//
// A refusal moved no money and will not stop being a refusal: the row fails
// and the claim returns to the merchant. Anything else -- a timeout, a 5xx --
// may have moved money, so the row stays submitted with the error on it and
// the chase asks the rail what happened. Neither is retried here.
func (w *Worker) recordError(ctx context.Context, s *Settlement, err error) error {
	if baas.IsRefusal(err) {
		slog.Warn("naira: the rail refused the transfer", "tap", s.TapID, "err", err)
		return w.fail(ctx, s, err.Error())
	}
	// Logged in full: the row keeps it too, but the chase may replace it
	// later, and the rail's exact words are what an operator debugs from.
	slog.Error("naira: the rail did not answer the transfer; chasing", "tap", s.TapID, "err", err)
	s.Error = err.Error()
	_, e := w.Pool.Exec(ctx, `
		UPDATE card_tap_ngn_settlements SET error = $2, updated_at = now()
		 WHERE tap_id = $1 AND state = 'submitted'`, s.TapID, err.Error())
	return e
}

// apply takes the rail's answer -- from the transfer call, a status check or
// a webhook -- and moves the row to match.
func (w *Worker) apply(ctx context.Context, s *Settlement, railRef string, status baas.TransferStatus, message string, fee decimal.Decimal) error {
	switch status {
	case baas.TransferSuccess:
		return w.settle(ctx, s, railRef, fee)
	case baas.TransferFailed:
		if message == "" {
			message = "the rail refused the transfer"
		}
		return w.fail(ctx, s, message)
	default:
		_, err := w.Pool.Exec(ctx, `
			UPDATE card_tap_ngn_settlements
			   SET rail_ref = COALESCE(NULLIF($2, ''), rail_ref), updated_at = now()
			 WHERE tap_id = $1 AND state = 'submitted'`, s.TapID, railRef)
		return err
	}
}

// settle records the rail's confirmation. The books already say this leg was
// paid, from the moment it was submitted; this is the row catching up, and
// the rail's fees for both hops coming out of the scheme fee. The state
// predicate makes a redelivered confirmation find nothing to do.
func (w *Worker) settle(ctx context.Context, s *Settlement, railRef string, fee decimal.Decimal) error {
	if railRef == "" {
		railRef = s.Reference
	}
	railFee := s.RailFee
	if !fee.IsZero() {
		railFee = money.New(fee.Shift(2).Round(0).IntPart(), s.Amount.Currency())
	}
	if !railFee.IsPositive() {
		// The rail said nothing of its fee; it charged the configured one.
		railFee = w.bankFee()
	}
	settled := false
	err := movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE card_tap_ngn_settlements
			   SET state = 'settled', rail_ref = COALESCE(rail_ref, $2), rail_fee_minor = $3, error = NULL,
			       settled_at = now(), updated_at = now()
			 WHERE tap_id = $1 AND state = 'submitted'`, s.TapID, railRef, railFee.Minor())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		settled = true
		fees, err := s.SweepFee.Add(railFee)
		if err != nil {
			return err
		}
		if fees.IsPositive() {
			if _, err := movements.RailFeesPaid(ctx, tx, fees, s.TapID, s.Attempts); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !settled {
		if s.State == Failed {
			// The rail says it paid a transfer we had given up on. The
			// claim has been returned to the merchant, so the books now
			// overstate what they are owed; nothing here can know which
			// attempt was paid, and guessing is how somebody is paid twice.
			slog.Error("naira: the rail confirmed a settlement marked failed; an operator must reconcile",
				"tap", s.TapID, "rail_ref", railRef)
		}
		return nil
	}
	s.State, s.RailFee = Settled, railFee
	if w.Settled != nil {
		w.Settled(ctx, w.Pool, s.TapID)
	}
	return nil
}

// fail is terminal for this attempt: the rail refused and no money moved.
// The claim goes back to the merchant, where the audit can see it is still
// owed, until an operator retries.
func (w *Worker) fail(ctx context.Context, s *Settlement, reason string) error {
	if s.SweepRef != "" {
		// The sweep stood: the tap's naira is in the platform's wallet,
		// and a retry pays the bank from there without sweeping again.
		reason += fmt.Sprintf(" (swept into the platform wallet under %s; a retry pays the bank from there)", s.SweepRef)
	}
	return movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE card_tap_ngn_settlements
			   SET state = 'failed', error = $2, updated_at = now()
			 WHERE tap_id = $1 AND state = 'submitted'`, s.TapID, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		_, err = movements.MerchantWalletSettlementReturned(ctx, tx, s.MerchantID, s.Amount, s.TapID, s.Attempts, reason)
		if err != nil && !errors.Is(err, ledger.ErrDuplicate) {
			return err
		}
		s.State = Failed
		slog.Error("naira: settlement failed; the merchant is still owed",
			"tap", s.TapID, "owed", s.Amount.String(), "why", reason)
		return nil
	})
}

// chaseStale asks the rail about submitted settlements that have had no
// answer for a while.
//
// One that the rail has no record of, and that never got a reference from
// it, never reached it; it is left failed rather than sent again, because
// "never reached it" is what this system believes and not what it knows.
// An operator retries it under the same reference, which the rail's own
// idempotency makes safe.
func (w *Worker) chaseStale(ctx context.Context) (int, error) {
	rows, err := w.Pool.Query(ctx, settlementSelect+`
		 WHERE state = 'submitted' AND updated_at < $1
		 ORDER BY created_at
		 LIMIT $2`, w.now().Add(-StaleAfter), BatchSize)
	if err != nil {
		return 0, fmt.Errorf("naira: find stale settlements: %w", err)
	}
	var stale []*Settlement
	for rows.Next() {
		s, err := scanSettlement(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		stale = append(stale, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	chased := 0
	for _, s := range stale {
		if err := w.chase(ctx, s); err != nil {
			return chased, err
		}
		chased++
	}
	return chased, nil
}

// chase asks the rail about whichever hop the row is waiting on.
//
// A row without a sweep reference is waiting on the sweep: one the rail has
// no record of never happened, and the row fails; one it accepted is
// recorded and the bank transfer made now. A row with one is waiting on the
// bank transfer, as before.
func (w *Worker) chase(ctx context.Context, s *Settlement) error {
	if s.SweepRef == "" {
		status, err := w.Rail.TransferStatus(ctx, SweepReference(s.Reference))
		if err != nil {
			slog.Warn("naira: could not chase sweep", "tap", s.TapID, "err", err)
			return nil
		}
		switch {
		case status.Status == baas.TransferPending && status.RawStatus == "not_found":
			return w.fail(ctx, s, w.noRecord(s, "the rail has no record of the sweep; retry from the console"))
		case status.Status == baas.TransferFailed:
			return w.fail(ctx, s, "sweep: "+orDefault(status.Message, "the rail refused the sweep"))
		default:
			if err := w.recordSweep(ctx, s, status.Reference, status.Fees); err != nil {
				return err
			}
			return w.payBank(ctx, s)
		}
	}

	ref := s.RailRef
	if ref == "" {
		ref = s.Reference
	}
	status, err := w.Rail.TransferStatus(ctx, ref)
	if err != nil {
		slog.Warn("naira: could not chase settlement", "tap", s.TapID, "err", err)
		return nil
	}
	switch {
	case status.Status == baas.TransferPending && status.RawStatus == "not_found" && s.RailRef == "":
		return w.fail(ctx, s, w.noRecord(s, "the rail has no record of this transfer; retry from the console"))
	case status.Status == baas.TransferPending:
		// Still in flight. Touch it so it is not chased every tick.
		_, err := w.Pool.Exec(ctx, `
			UPDATE card_tap_ngn_settlements SET updated_at = now() WHERE tap_id = $1`, s.TapID)
		return err
	default:
		return w.apply(ctx, s, status.Reference, status.Status, status.Message, status.Fees)
	}
}

// noRecord is the chase's verdict with the submit's own error, if there was
// one, kept in front of it: that is the useful part of the story.
func (w *Worker) noRecord(s *Settlement, verdict string) string {
	if s.Error != "" {
		return s.Error + " -- " + verdict
	}
	return verdict
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ApplyWebhook takes a rail event addressed to a tap settlement and moves the
// row to match. Events for anything else are ignored, and it reports whether
// the event was one of ours.
func (w *Worker) ApplyWebhook(ctx context.Context, ev *baas.WebhookEvent) (bool, error) {
	if ev == nil {
		return false, nil
	}
	tapID, ok := tapOf(ev.PaymentReference)
	if !ok {
		return false, nil
	}
	s, err := Get(ctx, w.Pool, tapID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	switch ev.Status {
	case baas.TransferSuccess, baas.TransferFailed:
		return true, w.apply(ctx, s, ev.ProviderRef, ev.Status, ev.RawStatus, decimal.Zero)
	default:
		return true, nil
	}
}

// Retry puts a failed settlement back in the queue. The next tick asks the
// rail again, under the same reference, and discharges the claim again as a
// new attempt.
//
// The bank code is resolved here too, so an operator retrying a leg the
// rail still has no code for is told so now, with the institution's name,
// instead of finding the row failed again on the next tick. The tick
// resolves again regardless; this is only the early answer.
func (w *Worker) Retry(ctx context.Context, tapID uuid.UUID) (*Settlement, error) {
	s, err := Get(ctx, w.Pool, tapID)
	if err != nil {
		return nil, err
	}
	if s.State != Failed {
		return nil, fmt.Errorf("%w: tap %s is %s", ErrNotFailed, tapID, s.State)
	}
	if w.Resolver == nil && w.Rail != nil {
		w.Resolver = &BankCodeResolver{Banks: w.Rail.ListBanks}
	}
	if w.Resolver != nil {
		var unmapped *UnmappedBankError
		if _, _, err := w.Resolver.FintavaCode(ctx, s.BankCode); errors.As(err, &unmapped) {
			_, _ = w.Pool.Exec(ctx, `
				UPDATE card_tap_ngn_settlements SET error = $2, updated_at = now()
				 WHERE tap_id = $1 AND state = 'failed'`, tapID, unmapped.Error())
			return nil, unmapped
		}
	}
	if _, err := w.Pool.Exec(ctx, `
		UPDATE card_tap_ngn_settlements
		   SET state = 'queued', updated_at = now()
		 WHERE tap_id = $1 AND state = 'failed'`, tapID); err != nil {
		return nil, fmt.Errorf("naira: retry: %w", err)
	}
	return Get(ctx, w.Pool, tapID)
}

// Run ticks until the context ends.
func (w *Worker) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			paid, chased, err := w.Tick(ctx)
			if err != nil {
				if !errors.Is(err, ErrNoRail) {
					slog.Error("naira: tick failed", "err", err)
				}
				continue
			}
			if paid > 0 || chased > 0 {
				slog.Info("naira: settlements", "paid", paid, "chased", chased)
			}
		}
	}
}

func decimalOf(a money.Amount) decimal.Decimal {
	return decimal.New(a.Minor(), -int32(a.Currency().Exponent()))
}
