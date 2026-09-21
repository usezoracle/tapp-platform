// Package offramp turns a card tap into naira in a merchant's bank account,
// without the platform ever holding the money.
//
// The cardholder's own smart account calls the settlement Gateway: it approves
// the USDC and creates the order in one sponsored operation, naming itself as
// the refund address. A liquidity provider watching the chain pays the
// merchant's bank and claims the USDC. Nothing is swept, nothing is pooled,
// and an order that is never filled refunds to the person who funded it rather
// than to us.
//
// That is the whole reason this exists in preference to paying merchants from
// a float: a float has to be funded, defended and reconciled, and every naira
// in it is money the platform is holding on somebody else's behalf.
package offramp

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/chain/base"
	"github.com/usezoracle/tapp/api/internal/chain/cdp"
	"github.com/usezoracle/tapp/api/internal/money"
	paycrest "github.com/usezoracle/tapp/api/services/settlement"
)

// ErrNotConfigured means no Gateway address is set, so nothing can be sold.
var ErrNotConfigured = errors.New("offramp: no settlement gateway configured")

// Sender submits sponsored calls from a smart account. Satisfied by cdp.Client.
type Sender interface {
	SendCalls(ctx context.Context, account string, calls []cdp.Call, idem string) (string, error)
}

// Keys serves the aggregator's public key. Satisfied by paycrest.Client.
type Keys interface {
	FetchPublicKey(ctx context.Context) (string, error)
}

// Reader reads the chain: a receipt for the order's id, a call for its
// outcome. Satisfied by ethclient.Client.
type Reader interface {
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// ErrOrderNotCreated means the transaction landed but created no order for
// this account: it reverted, or it was somebody else's operation in the same
// bundle. Either way nothing was sold, and the books that said otherwise
// have to be put back.
var ErrOrderNotCreated = errors.New("offramp: the transaction created no order for this account")

// ErrNotMined means the transaction has no receipt yet. Asked again later.
var ErrNotMined = errors.New("offramp: the transaction is not mined yet")

// Bank is where the merchant is paid.
//
// Institution is the aggregator's own code for the bank -- OPAYNGPC, not a
// NUBAN sort code -- and AccountName is what the bank returned when the
// account was verified, never what somebody typed.
type Bank struct {
	Institution   string
	AccountNumber string
	AccountName   string
}

// Order is one sale of a cardholder's USDC into a merchant's bank account.
type Order struct {
	// From is the cardholder's smart account: the address holding the USDC,
	// the address that signs, and the address a failed order refunds to.
	From string

	// Sell is the USDC being sold, in the token's own subunits.
	Sell *big.Int

	// Deliver is what the merchant should receive. It sets the rate rather
	// than the amount: the Gateway takes a price, and the provider fills at
	// that price or not at all.
	Deliver money.Amount

	Bank Bank

	// Reference makes the operation idempotent. The tap id serves: a retry
	// after a lost response must not create a second order for one payment.
	Reference string
}

// Client sells a cardholder's USDC for a merchant's naira.
type Client struct {
	Sender  Sender
	Keys    Keys
	Reader  Reader
	Gateway common.Address
	USDC    common.Address

	// SenderFeeBPS is the platform's skim, taken by the Gateway and paid to
	// FeeRecipient. Zero means none, and then FeeRecipient is unused.
	SenderFeeBPS int64
	FeeRecipient common.Address
}

// Create submits the order and returns the transaction hash.
//
// approve and createOrder go together in ONE operation. An approve that lands
// without its order leaves an allowance standing on a contract that can spend
// the cardholder's money; an order that lands without its approve simply
// reverts. Sending them together is what makes a retry safe.
func (c *Client) Create(ctx context.Context, o Order) (string, error) {
	if c.Gateway == (common.Address{}) {
		return "", ErrNotConfigured
	}
	if c.Sender == nil || c.Keys == nil {
		return "", ErrNotConfigured
	}
	if o.Sell == nil || o.Sell.Sign() <= 0 {
		return "", fmt.Errorf("offramp: nothing to sell")
	}
	if !o.Deliver.IsPositive() {
		return "", fmt.Errorf("offramp: nothing to deliver")
	}
	if o.Bank.Institution == "" || o.Bank.AccountNumber == "" || o.Bank.AccountName == "" {
		return "", fmt.Errorf("offramp: a payout needs a verified institution, number and name")
	}

	pem, err := c.Keys.FetchPublicKey(ctx)
	if err != nil {
		return "", fmt.Errorf("offramp: aggregator public key: %w", err)
	}

	// The recipient is encrypted to the aggregator, so the bank details are
	// readable by them and by nobody else watching the chain. It travels as a
	// call argument, which is why this is safe to put in a public
	// transaction at all.
	messageHash, err := paycrest.EncryptRecipient(paycrest.Recipient{
		AccountIdentifier: o.Bank.AccountNumber,
		AccountName:       o.Bank.AccountName,
		Institution:       o.Bank.Institution,
		Memo:              "Tapp card settlement",
	}, pem)
	if err != nil {
		return "", fmt.Errorf("offramp: encrypt recipient: %w", err)
	}

	rate, err := c.rate(o)
	if err != nil {
		return "", err
	}

	fee := big.NewInt(0)
	feeRecipient := c.FeeRecipient
	if c.SenderFeeBPS > 0 {
		fee = new(big.Int).Div(
			new(big.Int).Mul(o.Sell, big.NewInt(c.SenderFeeBPS)),
			big.NewInt(10_000),
		)
	}
	// A fee with nowhere to go is a fee the Gateway cannot pay out.
	if fee.Sign() > 0 && feeRecipient == (common.Address{}) {
		fee = big.NewInt(0)
	}

	// The Gateway pulls both the order amount and the fee, so the allowance
	// has to cover both or the call reverts on transferFrom.
	allowance := new(big.Int).Add(o.Sell, fee)

	approve, err := base.PackApprove(c.Gateway, allowance)
	if err != nil {
		return "", err
	}

	from := common.HexToAddress(o.From)
	create, err := packCreateOrder(createOrderArgs{
		Token:              c.USDC,
		Amount:             o.Sell,
		Rate:               rate,
		SenderFeeRecipient: feeRecipient,
		SenderFee:          fee,
		// Refunds go back to the cardholder, never to the platform. An order
		// nobody fills has to return to the person whose money it was.
		RefundAddress: from,
		MessageHash:   messageHash,
	})
	if err != nil {
		return "", err
	}

	return c.Sender.SendCalls(ctx, o.From, []cdp.Call{
		{To: c.USDC, Data: approve},
		{To: c.Gateway, Data: create},
	}, "offramp:"+o.Reference)
}

// rate is fiat per whole token, scaled by 100, as the Gateway's uint96 wants.
//
// Derived from the two amounts rather than taken from a rate source, so the
// price on chain is exactly the price the ledger already moved. A rate fetched
// separately here could disagree with the one the cardholder was charged at,
// and the difference would be a loss nobody booked.
func (c *Client) rate(o Order) (*big.Int, error) {
	sell := decimal.NewFromBigInt(o.Sell, 0)
	if sell.IsZero() {
		return nil, fmt.Errorf("offramp: nothing to sell")
	}
	// Both sides to whole units first: subunits per subunit is not a price
	// anybody quotes, and the scales differ between token and fiat.
	tokens := sell.Div(decimal.New(1, int32(usdcDecimals)))
	fiat := decimal.NewFromInt(o.Deliver.Minor()).
		Div(decimal.NewFromInt(o.Deliver.Currency().Scale()))

	scaled := fiat.Div(tokens).Mul(decimal.NewFromInt(100)).Round(0)
	if scaled.Sign() <= 0 {
		return nil, fmt.Errorf("offramp: %s for %s is not a usable rate", o.Deliver, o.Sell)
	}
	return scaled.BigInt(), nil
}

// usdcDecimals is USDC's own precision on Base. Six, and not read from the
// contract: a wrong value here misprices an order by a factor of a hundred,
// so it is stated rather than discovered.
const usdcDecimals = 6

// OrderID finds the order a settlement transaction created for an account.
//
// A sponsored operation is bundled: one transaction can carry several
// accounts' operations, so the log is matched on the sender as well as the
// Gateway, and on the amount when one account somehow has two. A receipt
// that shows the transaction reverted, or that carries no such log, means no
// order exists and the caller has to treat it as never sold.
func (c *Client) OrderID(ctx context.Context, txHash, from string, sell *big.Int) ([32]byte, error) {
	if c.Reader == nil {
		return [32]byte{}, ErrNotConfigured
	}
	receipt, err := c.Reader.TransactionReceipt(ctx, common.HexToHash(txHash))
	if errors.Is(err, ethereum.NotFound) {
		return [32]byte{}, ErrNotMined
	}
	if err != nil {
		return [32]byte{}, fmt.Errorf("offramp: receipt %s: %w", txHash, err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return [32]byte{}, fmt.Errorf("%w: %s reverted", ErrOrderNotCreated, txHash)
	}

	sender := common.HexToAddress(from)
	for _, l := range receipt.Logs {
		if l.Address != c.Gateway {
			continue
		}
		ev, ok, err := unpackOrderCreated(*l)
		if err != nil {
			return [32]byte{}, err
		}
		if !ok || ev.Sender != sender {
			continue
		}
		if sell != nil && ev.Amount.Cmp(sell) != 0 {
			continue
		}
		return ev.OrderID, nil
	}
	return [32]byte{}, fmt.Errorf("%w: %s", ErrOrderNotCreated, txHash)
}

// Info asks the Gateway what became of an order.
func (c *Client) Info(ctx context.Context, orderID [32]byte) (OrderInfo, error) {
	if c.Reader == nil {
		return OrderInfo{}, ErrNotConfigured
	}
	data, err := packGetOrderInfo(orderID)
	if err != nil {
		return OrderInfo{}, err
	}
	out, err := c.Reader.CallContract(ctx, ethereum.CallMsg{To: &c.Gateway, Data: data}, nil)
	if err != nil {
		return OrderInfo{}, fmt.Errorf("offramp: getOrderInfo: %w", err)
	}
	return unpackOrderInfo(out)
}
