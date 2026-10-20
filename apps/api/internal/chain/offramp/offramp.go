// Package offramp turns a card tap into naira in a merchant's bank account,
// without the platform ever holding the money.
//
// The settlement provider is Switch. For each tap a payout is opened with
// Switch naming the merchant's bank and the exact naira they are owed; Switch
// answers with a single-use deposit address and the USDC it wants there. The
// cardholder's own smart account sends that USDC in one sponsored operation,
// and Switch pays the bank. A payout Switch cannot complete refunds to the
// cardholder's account, never to us, so the money returns to whoever funded it.
//
// That is the whole reason this exists in preference to paying merchants from
// a float: a float has to be funded, defended and reconciled, and every naira
// in it is money the platform is holding on somebody else's behalf.
package offramp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/chain/base"
	"github.com/usezoracle/tapp/api/internal/chain/cdp"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/onswitch"
)

// ErrNotConfigured means there is no provider to sell through.
var ErrNotConfigured = errors.New("offramp: no settlement provider configured")

// MinSell is the least USDC, in micro-units, Switch opens a payout for: it
// answers anything smaller with "Minimum amount per transaction is 1 USDC",
// on every attempt.
const MinSell = 1_000_000

// ErrBelowMinimum means a tap's USDC leg sells less than MinSell, so no
// payout could ever deliver it.
var ErrBelowMinimum = errors.New("offramp: the payout is below Switch's 1 USDC minimum")

// Sender submits sponsored calls from a smart account. Satisfied by cdp.Client.
type Sender interface {
	SendCalls(ctx context.Context, account string, calls []cdp.Call, idem string) (string, error)
}

// Switch is the part of the provider's API a payout needs. Satisfied by
// onswitch.Client.
type Switch interface {
	Initiate(ctx context.Context, r onswitch.InitiateRequest) (onswitch.Payment, error)
	Confirm(ctx context.Context, reference, txHash string) error
	Status(ctx context.Context, reference string) (onswitch.Payment, error)
}

// Bank is where the merchant is paid.
//
// Institution is the catalogue's code for the bank -- MONINGPC, not a NIP
// code -- and AccountName is what the bank returned when the account was
// verified, never what somebody typed.
type Bank struct {
	Institution   string
	AccountNumber string
	AccountName   string
}

// Order is one sale of a cardholder's USDC into a merchant's bank account.
type Order struct {
	// From is the cardholder's smart account: the address holding the USDC,
	// the address that signs, and the address a failed payout refunds to.
	From string

	// Deliver is exactly what the merchant receives. Switch prices the USDC
	// from it, so the merchant is paid what the ledger says they are owed.
	Deliver money.Amount

	Bank Bank

	// Reference is Switch's key for the payout, a UUID. It also keys the
	// sponsored operation, so a retry after a lost response finds the
	// payout and the transfer it already has rather than making new ones.
	Reference string
}

// Payment is a payout opened with Switch.
type Payment struct {
	Reference string
	// Address is where Switch wants the USDC; single use.
	Address common.Address
	// Sell is the USDC Switch wants there, in subunits.
	Sell int64
	// Status is Switch's, as read when the payout was opened.
	Status string
}

// Funded reports whether the deposit has already been made. A payout found
// already past AWAITING_DEPOSIT was funded by an earlier attempt whose answer
// was lost; sending again would pay twice.
func (p Payment) Funded() bool { return p.Status != onswitch.StatusAwaitingDeposit }

// Outcome is where a submitted payout stands.
type Outcome int

const (
	// Open means not finished: waiting for the deposit, or being paid.
	Open Outcome = iota
	// Fulfilled means the merchant's bank was paid.
	Fulfilled
	// Refunded means nobody was paid and the USDC goes back to the cardholder.
	Refunded
)

// Client sells a cardholder's USDC for a merchant's naira through Switch.
type Client struct {
	Sender Sender
	Switch Switch
	USDC   common.Address
}

// Open finds or opens the payout for an order.
//
// Asked for first, so an attempt that opened the payout and then lost its
// answer, or crashed before funding it, picks up the same payout rather than
// opening a second one for one tap.
func (c *Client) Open(ctx context.Context, o Order) (Payment, error) {
	if c.Switch == nil || c.Sender == nil {
		return Payment{}, ErrNotConfigured
	}
	if !o.Deliver.IsPositive() {
		return Payment{}, fmt.Errorf("offramp: nothing to deliver")
	}
	if o.Bank.Institution == "" || o.Bank.AccountNumber == "" || o.Bank.AccountName == "" {
		return Payment{}, fmt.Errorf("offramp: a payout needs a verified institution, number and name")
	}
	if existing, err := c.Switch.Status(ctx, o.Reference); err == nil {
		return payment(o, existing)
	}

	bankCode, err := onswitch.BankCode(o.Bank.Institution)
	if err != nil {
		return Payment{}, err
	}
	fiat := decimal.NewFromInt(o.Deliver.Minor()).
		Div(decimal.NewFromInt(o.Deliver.Currency().Scale())).InexactFloat64()

	opened, err := c.Switch.Initiate(ctx, onswitch.InitiateRequest{
		QuoteRequest: onswitch.QuoteRequest{
			Amount:      fiat,
			Country:     onswitch.Country,
			Currency:    string(o.Deliver.Currency()),
			Asset:       onswitch.Asset,
			Channel:     onswitch.Channel,
			ExactOutput: true,
		},
		Beneficiary: onswitch.Beneficiary{
			HolderType:    "INDIVIDUAL",
			HolderName:    o.Bank.AccountName,
			AccountNumber: o.Bank.AccountNumber,
			BankCode:      bankCode,
		},
		Reference: o.Reference,
		// Refunds go back to the cardholder, never to the platform. A payout
		// nobody completes has to return to the person whose money it was.
		RefundAddress: o.From,
		Narration:     "Tapp card settlement",
	})
	if err != nil {
		return Payment{}, fmt.Errorf("offramp: open payout: %w", err)
	}
	return payment(o, opened)
}

// payment checks that what Switch holds is the payout this order asked for.
// A payout that would deliver another amount, in another currency, or ask
// for another token is refused before any money moves.
func payment(o Order, p onswitch.Payment) (Payment, error) {
	if p.Reference != o.Reference {
		return Payment{}, fmt.Errorf("offramp: asked for payout %s, Switch answered %s", o.Reference, p.Reference)
	}
	if p.Type != "OFFRAMP" {
		return Payment{}, fmt.Errorf("offramp: payout %s is a %s", p.Reference, p.Type)
	}
	if !strings.EqualFold(p.Deposit.Asset, onswitch.Asset) {
		return Payment{}, fmt.Errorf("offramp: payout %s wants %s", p.Reference, p.Deposit.Asset)
	}
	if p.Destination.Currency != string(o.Deliver.Currency()) {
		return Payment{}, fmt.Errorf("offramp: payout %s pays %s", p.Reference, p.Destination.Currency)
	}
	delivers := decimal.NewFromFloat(p.Destination.Amount).
		Mul(decimal.NewFromInt(o.Deliver.Currency().Scale())).Round(0)
	if delivers.IntPart() != o.Deliver.Minor() {
		return Payment{}, fmt.Errorf("offramp: payout %s delivers %s, the merchant is owed %s",
			p.Reference, delivers, o.Deliver)
	}
	if !common.IsHexAddress(p.Deposit.Address) {
		return Payment{}, fmt.Errorf("offramp: payout %s has no deposit address", p.Reference)
	}
	sell := decimal.NewFromFloat(p.Deposit.Amount).Shift(6)
	if !sell.IsInteger() || sell.Sign() <= 0 || !sell.BigInt().IsInt64() {
		return Payment{}, fmt.Errorf("offramp: payout %s asks for a deposit of %v", p.Reference, p.Deposit.Amount)
	}
	return Payment{
		Reference: p.Reference,
		Address:   common.HexToAddress(p.Deposit.Address),
		Sell:      sell.IntPart(),
		Status:    p.Status,
	}, nil
}

// Fund sends the deposit from the cardholder's account and tells Switch the
// transaction. Returns the transaction hash once it is mined.
//
// The operation is keyed on the payout's reference, so a retry after a lost
// response returns the transfer already made instead of making another. A
// confirm that fails is not a failed payout: the money has moved, and the
// tracker confirms again while Switch still says it is waiting.
func (c *Client) Fund(ctx context.Context, from string, p Payment) (string, error) {
	if c.Sender == nil || c.Switch == nil {
		return "", ErrNotConfigured
	}
	transfer, err := base.PackTransfer(p.Address, big.NewInt(p.Sell))
	if err != nil {
		return "", err
	}
	txHash, err := c.Sender.SendCalls(ctx, from, []cdp.Call{{To: c.USDC, Data: transfer}}, "offramp:"+p.Reference)
	if err != nil {
		return "", err
	}
	if err := c.Switch.Confirm(ctx, p.Reference, txHash); err != nil {
		slog.Warn("offramp: could not confirm a funded payout, the tracker will retry",
			"reference", p.Reference, "tx", txHash, "err", err)
	}
	return txHash, nil
}

// Outcome asks Switch where a payout stands, and returns its status too.
//
// An unknown status is an error and changes nothing: it is never mapped by
// guessing. BLOCKED is a compliance hold that only Switch and an operator can
// lift, so it stays open, loudly.
func (c *Client) Outcome(ctx context.Context, reference, txHash string) (Outcome, string, error) {
	if c.Switch == nil {
		return Open, "", ErrNotConfigured
	}
	p, err := c.Switch.Status(ctx, reference)
	if err != nil {
		return Open, "", err
	}
	if p.Reference != reference {
		return Open, p.Status, fmt.Errorf("offramp: asked for payout %s, Switch answered %s", reference, p.Reference)
	}
	switch p.Status {
	case onswitch.StatusCompleted:
		return Fulfilled, p.Status, nil
	case onswitch.StatusFailed, onswitch.StatusReversed:
		return Refunded, p.Status, nil
	case onswitch.StatusAwaitingDeposit:
		if txHash != "" {
			if err := c.Switch.Confirm(ctx, reference, txHash); err != nil {
				slog.Warn("offramp: confirm failed", "reference", reference, "tx", txHash, "err", err)
			}
		}
		return Open, p.Status, nil
	case onswitch.StatusProcessing, onswitch.StatusScheduled:
		return Open, p.Status, nil
	case onswitch.StatusBlocked:
		slog.Error("offramp: Switch is holding a payout for compliance; an operator must follow it up",
			"reference", reference)
		return Open, p.Status, nil
	}
	return Open, p.Status, fmt.Errorf("offramp: payout %s has unknown status %q", reference, p.Status)
}
