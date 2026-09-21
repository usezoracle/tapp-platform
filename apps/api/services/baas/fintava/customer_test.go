package fintava

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/usezoracle/tapp/api/services/baas"
)

// The live create-customer shape: the ids live under userInfo and wallet,
// and the bank is wallet.serviceProvider -- there is no bankName anywhere.
// The first version of this decoder read bankName, got nothing, and stored
// "Fintava partner bank" as the bank real people were told to pay into.
func TestCustomerDecodesLiveShape(t *testing.T) {
	var cu Customer
	if err := json.Unmarshal([]byte(`{
		"userInfo": {"id": "cust-1", "firstName": "Ada"},
		"wallet": {"id": "wal-1", "accountNumber": "1234567890", "accountName": "ADA O",
		           "serviceProvider": "loma", "fundMethod": "STATIC_FUND"}
	}`), &cu); err != nil {
		t.Fatal(err)
	}
	if cu.CustomerID() != "cust-1" || cu.WalletID() != "wal-1" || cu.DepositAccountNumber() != "1234567890" {
		t.Fatalf("ids = customer %q wallet %q account %q", cu.CustomerID(), cu.WalletID(), cu.DepositAccountNumber())
	}
	if got := cu.RawBank(); got.Name != "loma" || got.Code != "" {
		t.Fatalf("raw bank = %+v", got)
	}
}

// The bank field is tolerated as a string, an object, or absent -- and
// absent is empty, never a placeholder.
func TestRawBankShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want BankRef
	}{
		"serviceProvider string": {`{"wallet":{"serviceProvider":"iyin-ekiti"}}`, BankRef{Name: "iyin-ekiti"}},
		"bank string":            {`{"wallet":{"bank":"Loma MFB"}}`, BankRef{Name: "Loma MFB"}},
		"serviceProvider object": {`{"wallet":{"serviceProvider":{"name":"Loma Microfinance Bank","code":"090620"}}}`,
			BankRef{Name: "Loma Microfinance Bank", Code: "090620"}},
		"object with sortCode": {`{"wallet":{"bank":{"bankName":"Iyin-Ekiti MFB","sortCode":"090491"}}}`,
			BankRef{Name: "Iyin-Ekiti MFB", Code: "090491"}},
		"provider wins over bank": {`{"wallet":{"serviceProvider":"loma","bank":"other"}}`, BankRef{Name: "loma"}},
		"flattened":               {`{"serviceProvider":"loma"}`, BankRef{Name: "loma"}},
		"absent":                  {`{"wallet":{"accountNumber":"1"}}`, BankRef{}},
		"null":                    {`{"wallet":{"serviceProvider":null,"bank":null}}`, BankRef{}},
		"unexpected type":         {`{"wallet":{"serviceProvider":42}}`, BankRef{}},
	} {
		var cu Customer
		if err := json.Unmarshal([]byte(tc.body), &cu); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := cu.RawBank(); got != tc.want {
			t.Errorf("%s: raw bank = %+v, want %+v", name, got, tc.want)
		}
		if strings.Contains(cu.RawBank().Name, "partner") {
			t.Errorf("%s: placeholder leaked", name)
		}
	}
}

// bankListServer answers /banks with a small catalogue and, optionally,
// /create/customer and /customers/list.
func bankListServer(t *testing.T, customer string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/banks":
			_, _ = w.Write([]byte(`{"status":true,"data":[
				{"name":"Diploma Bank","sortCode":"000001"},
				{"name":"Loma Microfinance Bank","sortCode":"090620"},
				{"name":"Iyin-Ekiti Microfinance Bank","sortCode":"090491"},
				{"name":"Guaranty Trust Bank","sortCode":"000013"}]}`))
		case "/create/customer":
			_, _ = w.Write([]byte(`{"status":true,"data":` + customer + `}`))
		case "/customers/list":
			if r.URL.Query().Get("searchTerm") != "ada@example.com" {
				t.Errorf("searchTerm = %q", r.URL.Query().Get("searchTerm"))
			}
			_, _ = w.Write([]byte(`{"status":true,"data":[
				{"id":"c-other","email":"ada@example.com","userInfo":{"id":"c-other","wallet":{"id":"wal-other","accountNumber":"0000000000"}}},
				{"id":"c-1","email":"ada@example.com","userInfo":{"id":"c-1","wallet":{"id":"wal-1","accountNumber":"1234567890","serviceProvider":"loma"}}}
			]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// "loma" becomes the catalogue's name and code; a code the response carried
// is matched before the name; an unknown name is tidied and its code left
// empty rather than invented.
func TestResolveBank(t *testing.T) {
	c := New("k", "s", bankListServer(t, "{}").URL)
	for _, tc := range []struct {
		raw  BankRef
		name string
		code string
	}{
		{BankRef{Name: "loma"}, "Loma Microfinance Bank", "090620"},
		{BankRef{Name: "IYIN-EKITI"}, "Iyin-Ekiti Microfinance Bank", "090491"},
		{BankRef{Name: "Iyin-Ekiti Microfinance Bank"}, "Iyin-Ekiti Microfinance Bank", "090491"},
		{BankRef{Name: "whatever", Code: "000013"}, "Guaranty Trust Bank", "000013"},
		{BankRef{Name: "kredi mfb"}, "Kredi Mfb", ""},
		{BankRef{Name: "sparkle"}, "Sparkle Bank", ""},
		{BankRef{}, "", ""},
	} {
		name, code := c.ResolveBank(t.Context(), tc.raw)
		if name != tc.name || code != tc.code {
			t.Errorf("ResolveBank(%+v) = %q/%q, want %q/%q", tc.raw, name, code, tc.name, tc.code)
		}
	}
}

// End to end through the adapter: the account carries the wallet id (what
// the balance endpoint takes) separately from the customer id, and a
// resolved bank. With no bank in the response the name is EMPTY -- the
// caller's configured fallback fills it, never a placeholder.
func TestCreateSubAccountCarriesWalletAndBank(t *testing.T) {
	req := baas.CreateSubAccountRequest{
		FirstName: "Ada", LastName: "O", DateOfBirth: "1990-01-01", Address: "Lagos", NIN: "1", IdentityNumber: "2",
		EmailAddress: "ada@example.com", PhoneNumber: "080",
	}

	withBank := NewAdapter(New("k", "s", bankListServer(t, `{
		"userInfo": {"id": "cust-1"},
		"wallet": {"id": "wal-1", "accountNumber": "1234567890", "serviceProvider": "loma"}}`).URL))
	acct, err := withBank.CreateSubAccount(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if acct.ID != "cust-1" || acct.WalletID != "wal-1" || acct.AccountNumber != "1234567890" {
		t.Fatalf("account = %+v", acct)
	}
	if acct.BankName != "Loma Microfinance Bank" || acct.BankCode != "090620" {
		t.Fatalf("bank = %q/%q", acct.BankName, acct.BankCode)
	}

	noBank := NewAdapter(New("k", "s", bankListServer(t, `{
		"userInfo": {"id": "cust-2"},
		"wallet": {"id": "wal-2", "accountNumber": "2222222222"}}`).URL))
	acct, err = noBank.CreateSubAccount(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if acct.BankName != "" || acct.BankCode != "" {
		t.Fatalf("with no bank in the response, bank = %q/%q, want empty", acct.BankName, acct.BankCode)
	}
}

// A row opened before wallet ids were stored: the wallet is found by the
// account number among the customers matching the email, not by position.
func TestLocateWalletMatchesAccountNumber(t *testing.T) {
	a := NewAdapter(New("k", "s", bankListServer(t, "{}").URL))
	var _ baas.WalletLocator = a
	id, err := a.LocateWallet(t.Context(), "ada@example.com", "1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if id != "wal-1" {
		t.Fatalf("wallet = %q, want wal-1", id)
	}
	if _, err := a.LocateWallet(t.Context(), "ada@example.com", "9999999999"); err == nil {
		t.Fatal("an account nobody holds must not resolve")
	}
}
