package offramp

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/usezoracle/tapp/api/internal/chain/cdp"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/onswitch"
)

const (
	cardholder = "0xb779226ee0f345b42681b981337205c918af8c3c"
	depositTo  = "0x1111111111111111111111111111111111111111"
	fundingTx  = "0x70b67e6948ea758eac499d7ac1967408f95ec961316d4f833f218671646c0f01"
	tapRef     = "5b1f0a6e-7f53-4a8e-9a3c-2f9b8d6c1e47"
)

var usdcOnBase = common.HexToAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913")

type fakeSender struct {
	account string
	calls   []cdp.Call
	idem    string
	sent    int
	err     error
}

func (f *fakeSender) SendCalls(
	_ context.Context, account string, calls []cdp.Call, idem string,
) (string, error) {
	f.sent++
	f.account, f.calls, f.idem = account, calls, idem
	if f.err != nil {
		return "", f.err
	}
	return fundingTx, nil
}

// fakeSwitch holds payouts the way Switch does: by reference, opened by
// Initiate, read by Status.
type fakeSwitch struct {
	payments  map[string]onswitch.Payment
	initiated []onswitch.InitiateRequest
	confirmed []string
	deposit   float64
	initErr   error
}

func newSwitch() *fakeSwitch {
	return &fakeSwitch{payments: map[string]onswitch.Payment{}, deposit: 1.194844}
}

func (f *fakeSwitch) Status(_ context.Context, ref string) (onswitch.Payment, error) {
	p, ok := f.payments[ref]
	if !ok {
		return onswitch.Payment{}, &onswitch.Error{Status: 404, Message: "Payment not found"}
	}
	return p, nil
}

func (f *fakeSwitch) Initiate(_ context.Context, r onswitch.InitiateRequest) (onswitch.Payment, error) {
	f.initiated = append(f.initiated, r)
	if f.initErr != nil {
		return onswitch.Payment{}, f.initErr
	}
	p := onswitch.Payment{
		Reference:   r.Reference,
		Type:        "OFFRAMP",
		Status:      onswitch.StatusAwaitingDeposit,
		Source:      onswitch.Side{Amount: f.deposit, Currency: "USDC"},
		Destination: onswitch.Side{Amount: r.Amount, Currency: r.Currency},
		Deposit:     onswitch.Deposit{Address: depositTo, Amount: f.deposit, Asset: onswitch.Asset},
	}
	f.payments[r.Reference] = p
	return p, nil
}

func (f *fakeSwitch) Confirm(_ context.Context, ref, hash string) error {
	f.confirmed = append(f.confirmed, ref+" "+hash)
	return nil
}

func (f *fakeSwitch) settle(ref, status string) {
	p := f.payments[ref]
	p.Status = status
	f.payments[ref] = p
}

func newClient(s *fakeSender, sw *fakeSwitch) *Client {
	return &Client{Sender: s, Switch: sw, USDC: usdcOnBase}
}

func order() Order {
	return Order{
		From:    cardholder,
		Deliver: money.Naira(1_592),
		Bank: Bank{
			Institution:   "OPAYNGPC",
			AccountNumber: "9034409271",
			AccountName:   "OLUMIDE SILAS OGUNDELE",
		},
		Reference: tapRef,
	}
}

func TestAPayoutIsOpenedForExactlyWhatTheMerchantIsOwed(t *testing.T) {
	sw := newSwitch()
	p, err := newClient(&fakeSender{}, sw).Open(context.Background(), order())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(sw.initiated) != 1 {
		t.Fatalf("initiated %d payouts, want 1", len(sw.initiated))
	}
	r := sw.initiated[0]
	if !r.ExactOutput || r.Amount != 1592 || r.Currency != "NGN" || r.Country != "NG" ||
		r.Channel != "BANK" || r.Asset != "base:usdc" {
		t.Errorf("initiate = %+v, want exactly ₦1,592 to an NG bank from base:usdc", r.QuoteRequest)
	}
	if r.Beneficiary.BankCode != "100004" || r.Beneficiary.AccountNumber != "9034409271" ||
		r.Beneficiary.HolderName != "OLUMIDE SILAS OGUNDELE" {
		t.Errorf("beneficiary = %+v, want OPay's Switch code and the verified name", r.Beneficiary)
	}
	if r.Reference != tapRef {
		t.Errorf("reference = %s, want %s", r.Reference, tapRef)
	}
	if p.Sell != 1_194_844 || p.Address != common.HexToAddress(depositTo) || p.Funded() {
		t.Errorf("payment = %+v, want 1194844 to the deposit address, not yet funded", p)
	}
}

// Refunds go to the person whose money it was, never to the platform.
func TestAFailedPayoutRefundsToTheCardholder(t *testing.T) {
	sw := newSwitch()
	if _, err := newClient(&fakeSender{}, sw).Open(context.Background(), order()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := sw.initiated[0].RefundAddress; got != cardholder {
		t.Errorf("refund address = %s, want the cardholder %s", got, cardholder)
	}
}

// An attempt that opened the payout and lost the answer finds it again.
func TestAnOpenedPayoutIsFoundRatherThanOpenedTwice(t *testing.T) {
	sw := newSwitch()
	c := newClient(&fakeSender{}, sw)
	if _, err := c.Open(context.Background(), order()); err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(context.Background(), order())
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if len(sw.initiated) != 1 {
		t.Errorf("initiated %d payouts for one tap", len(sw.initiated))
	}
	if p.Sell != 1_194_844 {
		t.Errorf("found payment sells %d", p.Sell)
	}
}

func TestAPayoutForAnotherAmountIsRefused(t *testing.T) {
	sw := newSwitch()
	sw.payments[tapRef] = onswitch.Payment{
		Reference: tapRef, Type: "OFFRAMP", Status: onswitch.StatusAwaitingDeposit,
		Destination: onswitch.Side{Amount: 1500, Currency: "NGN"},
		Deposit:     onswitch.Deposit{Address: depositTo, Amount: 1.1, Asset: onswitch.Asset},
	}
	_, err := newClient(&fakeSender{}, sw).Open(context.Background(), order())
	if err == nil || !strings.Contains(err.Error(), "the merchant is owed") {
		t.Fatalf("err = %v, want the amount refused", err)
	}
}

func TestABankSwitchCannotNameIsRefused(t *testing.T) {
	sw := newSwitch()
	o := order()
	o.Bank.Institution = "NOPENGPC"
	_, err := newClient(&fakeSender{}, sw).Open(context.Background(), o)
	var unmapped *onswitch.UnmappedBankError
	if !errors.As(err, &unmapped) {
		t.Fatalf("err = %v, want an unmapped bank", err)
	}
	if len(sw.initiated) != 0 {
		t.Error("a payout was opened to a bank with no Switch code")
	}
}

func TestAnIncompleteBankIsRefused(t *testing.T) {
	o := order()
	o.Bank.AccountName = ""
	if _, err := newClient(&fakeSender{}, newSwitch()).Open(context.Background(), o); err == nil {
		t.Fatal("a payout was opened without a verified name")
	}
}

func TestNoProviderMeansNoPayout(t *testing.T) {
	_, err := (&Client{Sender: &fakeSender{}}).Open(context.Background(), order())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

// Funding is one transfer of exactly what Switch asked for, to the address it
// gave, keyed on the payout so a retry cannot send twice; then confirmed.
func TestFundingIsOneTransferToTheDepositAddress(t *testing.T) {
	sw, s := newSwitch(), &fakeSender{}
	c := newClient(s, sw)
	p, err := c.Open(context.Background(), order())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := c.Fund(context.Background(), cardholder, p)
	if err != nil {
		t.Fatalf("Fund: %v", err)
	}
	if hash != fundingTx {
		t.Errorf("hash = %s", hash)
	}
	if s.account != cardholder || s.idem != "offramp:"+tapRef {
		t.Errorf("sent from %s as %q", s.account, s.idem)
	}
	if len(s.calls) != 1 || s.calls[0].To != usdcOnBase {
		t.Fatalf("calls = %+v, want one call to USDC", s.calls)
	}
	data := common.FromHex(s.calls[0].Data)
	if to := common.BytesToAddress(data[4:36]); to != common.HexToAddress(depositTo) {
		t.Errorf("transfer to %s, want the deposit address", to)
	}
	if amount := new(big.Int).SetBytes(data[36:68]).Int64(); amount != 1_194_844 {
		t.Errorf("transfer of %d, want 1194844", amount)
	}
	if len(sw.confirmed) != 1 || sw.confirmed[0] != tapRef+" "+fundingTx {
		t.Errorf("confirmed = %v", sw.confirmed)
	}
}

func TestStatusesMapToOutcomes(t *testing.T) {
	for status, want := range map[string]Outcome{
		onswitch.StatusAwaitingDeposit: Open,
		onswitch.StatusProcessing:      Open,
		onswitch.StatusScheduled:       Open,
		onswitch.StatusBlocked:         Open,
		onswitch.StatusCompleted:       Fulfilled,
		onswitch.StatusFailed:          Refunded,
		onswitch.StatusReversed:        Refunded,
	} {
		sw := newSwitch()
		c := newClient(&fakeSender{}, sw)
		if _, err := c.Open(context.Background(), order()); err != nil {
			t.Fatal(err)
		}
		sw.settle(tapRef, status)
		got, reported, err := c.Outcome(context.Background(), tapRef, fundingTx)
		if err != nil || got != want || reported != status {
			t.Errorf("%s: outcome %v (%s) err %v, want %v", status, got, reported, err, want)
		}
	}
}

// A payout still waiting for its deposit after funding is confirmed again.
func TestAWaitingPayoutIsConfirmedAgain(t *testing.T) {
	sw := newSwitch()
	c := newClient(&fakeSender{}, sw)
	if _, err := c.Open(context.Background(), order()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Outcome(context.Background(), tapRef, fundingTx); err != nil {
		t.Fatal(err)
	}
	if len(sw.confirmed) != 1 {
		t.Errorf("confirmed %d times, want once", len(sw.confirmed))
	}
}

func TestAnUnknownStatusIsNeverGuessed(t *testing.T) {
	sw := newSwitch()
	c := newClient(&fakeSender{}, sw)
	if _, err := c.Open(context.Background(), order()); err != nil {
		t.Fatal(err)
	}
	sw.settle(tapRef, "EXPIRED")
	got, _, err := c.Outcome(context.Background(), tapRef, fundingTx)
	if err == nil || got != Open {
		t.Errorf("outcome %v err %v, want Open and an error", got, err)
	}
}

// fakeLiquidity records what Fund asked to have on hand, and can refuse.
type fakeLiquidity struct {
	account, reference string
	need               int64
	err                error
}

func (f *fakeLiquidity) Ensure(_ context.Context, account string, need int64, reference string) error {
	f.account, f.need, f.reference = account, need, reference
	return f.err
}

// A cardholder whose USDC is in the yield vault has what the payout needs
// released first; one the vault cannot meet is not sent at all.
func TestFundingReleasesFromTheVaultFirst(t *testing.T) {
	sw, s, liq := newSwitch(), &fakeSender{}, &fakeLiquidity{}
	c := newClient(s, sw)
	c.Liquidity = liq
	p, err := c.Open(context.Background(), order())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fund(context.Background(), cardholder, p); err != nil {
		t.Fatalf("Fund: %v", err)
	}
	if liq.account != cardholder || liq.need != 1_194_844 || liq.reference != "offramp:"+tapRef {
		t.Errorf("ensured %d at %s for %q", liq.need, liq.account, liq.reference)
	}

	liq.err = errors.New("the vault can release 0")
	s.sent = 0
	if _, err := c.Fund(context.Background(), cardholder, p); err == nil {
		t.Fatal("funded with nothing released")
	}
	if s.sent != 0 {
		t.Error("sent a transfer the account could not cover")
	}
}
