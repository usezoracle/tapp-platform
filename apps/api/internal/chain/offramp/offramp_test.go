package offramp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/usezoracle/tapp/api/internal/chain/cdp"
	"github.com/usezoracle/tapp/api/internal/money"
)

// testPubkeyPEM is generated once per run rather than pasted in: a literal
// key has to be a real one to survive parsing, and committing a real RSA key
// -- even a public half whose private half was thrown away -- invites somebody
// to assume it means something.
var testPubkeyPEM = func() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}()

type fakeSender struct {
	account string
	calls   []cdp.Call
	idem    string
}

func (f *fakeSender) SendCalls(
	_ context.Context, account string, calls []cdp.Call, idem string,
) (string, error) {
	f.account, f.calls, f.idem = account, calls, idem
	return "0xdeadbeef", nil
}

type fakeKeys struct{ pem string }

func (f fakeKeys) FetchPublicKey(context.Context) (string, error) { return f.pem, nil }

func newClient(s *fakeSender) *Client {
	return &Client{
		Sender:  s,
		Keys:    fakeKeys{pem: testPubkeyPEM},
		Gateway: common.HexToAddress("0x30F6A8457F8E42371E204a9c103f2Bd42341dD0F"),
		USDC:    common.HexToAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"),
	}
}

func order() Order {
	return Order{
		From:    "0xb779226ee0f345b42681b981337205c918af8c3c",
		Sell:    big.NewInt(1_100_000), // $1.10 in USDC subunits
		Deliver: money.Naira(1_500),    // ₦1,500.00
		Bank: Bank{
			Institution:   "OPAYNGPC",
			AccountNumber: "9034409271",
			AccountName:   "OLUMIDE SILAS OGUNDELE",
		},
		Reference: "tap-1",
	}
}

// The allowance and the call it was granted for must travel together. An
// approve that lands alone leaves a contract able to spend the cardholder's
// money; an order that lands alone reverts.
func TestApproveAndCreateOrderAreOneOperation(t *testing.T) {
	s := &fakeSender{}
	c := newClient(s)

	if _, err := c.Create(context.Background(), order()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(s.calls) != 2 {
		t.Fatalf("sent %d calls, want 2 -- approve and createOrder together", len(s.calls))
	}
	if s.calls[0].To != c.USDC {
		t.Errorf("first call goes to %s, want the token so it can be approved", s.calls[0].To)
	}
	if s.calls[1].To != c.Gateway {
		t.Errorf("second call goes to %s, want the gateway", s.calls[1].To)
	}
	// 0x095ea7b3 is approve(address,uint256).
	if !strings.HasPrefix(s.calls[0].Data, "0x095ea7b3") {
		t.Errorf("first call is not approve: %.10s", s.calls[0].Data)
	}
	// The idempotency key must carry the tap, or a retry opens a second order
	// for one payment.
	if !strings.Contains(s.idem, "tap-1") {
		t.Errorf("idempotency key %q does not identify the tap", s.idem)
	}
}

// The order refunds to the cardholder, never to the platform. An order nobody
// fills has to return to the person whose money it was -- that is what makes
// this non-custodial rather than merely indirect.
func TestAnUnfilledOrderRefundsToTheCardholder(t *testing.T) {
	s := &fakeSender{}
	c := newClient(s)
	o := order()

	if _, err := c.Create(context.Background(), o); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The refund address is the 6th argument; find it in the calldata rather
	// than re-deriving the encoding.
	from := strings.ToLower(strings.TrimPrefix(o.From, "0x"))
	if !strings.Contains(strings.ToLower(s.calls[1].Data), from) {
		t.Errorf("createOrder calldata does not name %s as the refund address", o.From)
	}
	if s.account != o.From {
		t.Errorf("sent from %s, want the cardholder's own account %s", s.account, o.From)
	}
}

// The rate is what the provider fills at, so it has to be the price the ledger
// already moved -- fiat per whole token, scaled by 100 for the uint96.
func TestTheRateIsFiatPerTokenScaledByAHundred(t *testing.T) {
	c := newClient(&fakeSender{})

	for _, tc := range []struct {
		name    string
		sell    int64
		deliver money.Amount
		want    int64
	}{
		// $1.00 buying ₦1,500.00 is 1500.00 per token -> 150000.
		{"a round dollar", 1_000_000, money.Naira(1_500), 150_000},
		// $1.10 buying ₦1,500.00 is 1363.6363... -> 136364 after rounding.
		{"an awkward amount", 1_100_000, money.Naira(1_500), 136_364},
		// Half a dollar for half the naira is the same price.
		{"scale invariance", 500_000, money.Naira(750), 150_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.rate(Order{Sell: big.NewInt(tc.sell), Deliver: tc.deliver})
			if err != nil {
				t.Fatalf("rate: %v", err)
			}
			if got.Int64() != tc.want {
				t.Errorf("rate = %d, want %d", got.Int64(), tc.want)
			}
		})
	}
}

// Unverified or missing bank details must never reach the chain: the recipient
// blob is what a provider pays against, and a wrong number does not come back.
func TestAnIncompleteBankIsRefused(t *testing.T) {
	c := newClient(&fakeSender{})
	for _, missing := range []string{"institution", "number", "name"} {
		o := order()
		switch missing {
		case "institution":
			o.Bank.Institution = ""
		case "number":
			o.Bank.AccountNumber = ""
		case "name":
			o.Bank.AccountName = ""
		}
		if _, err := c.Create(context.Background(), o); err == nil {
			t.Errorf("an order with no %s was accepted", missing)
		}
	}
}

// Without a gateway there is nothing to sell into, and saying so is better
// than sending a transaction to the zero address.
func TestNoGatewayMeansNoOrder(t *testing.T) {
	c := newClient(&fakeSender{})
	c.Gateway = common.Address{}
	if _, err := c.Create(context.Background(), order()); err != ErrNotConfigured {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}
