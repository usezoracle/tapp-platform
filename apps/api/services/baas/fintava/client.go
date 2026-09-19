// Package fintava is the Fintava (fintavapay.com) BaaS adapter — the
// second NGN rail behind baas.Provider, selectable for Route B/C.
//
// Fintava's model (fintava.readme.io):
//   - Auth: `Authorization: Bearer <api key>` per environment.
//   - Pooled money: the MERCHANT WALLET (GET /merchant/balance), paid
//     out via POST /bank/credit/merchant which accepts our own
//     CustomerReference — the idempotency/trace anchor.
//   - Permanent deposit accounts: CUSTOMERS with fundingMethod
//     STATIC_FUND (POST /create/customer; BVN+NIN+DOB+address) — each
//     gets a wallet with a NUBAN that holds funds, our LP deposit
//     account equivalent.
//   - Webhooks: x-fintava-signature = HMAC-SHA512 over the RAW body,
//     keyed with the dashboard webhook secret.
//
// The docs hide most response schemas behind a login, so every decode
// here is deliberately tolerant: alternate field names are accepted
// and unknown statuses degrade to "pending" (never to "success").
package fintava

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// DefaultLiveBaseURL / DefaultSandboxBaseURL are Fintava's documented
// environments.
const (
	DefaultLiveBaseURL    = "https://live.fintavapay.com/api/dev"
	DefaultSandboxBaseURL = "https://dev.fintavapay.com/api/dev"
)

// Client is a thin HTTP client over the Fintava REST API.
type Client struct {
	BaseURL       string
	APIKey        string
	WebhookSecret string
	HTTP          *http.Client
}

// New builds a client. Empty baseURL defaults to the LIVE environment
// (this platform's default posture).
func New(apiKey, webhookSecret, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultLiveBaseURL
	}
	return &Client{
		BaseURL:       strings.TrimRight(baseURL, "/"),
		APIKey:        apiKey,
		WebhookSecret: webhookSecret,
		HTTP:          &http.Client{Timeout: 60 * time.Second},
	}
}

// envelope is the tolerant response wrapper: Fintava responses carry
// some mix of status/statusCode/message/data.
type envelope struct {
	Status     any             `json:"status"`
	StatusCode any             `json:"statusCode"`
	Message    string          `json:"message"`
	Data       json.RawMessage `json:"data"`
}

// APIError is a non-2xx answer from Fintava.
//
// Typed rather than a formatted string because callers have to tell "the rail
// read the request and said no" from "the rail could not be reached", and
// those two mean opposite things to a person being verified: one is a
// rejection, the other is "try again in a minute". A caller that has to grep
// an error message for a status code eventually gets that wrong.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("fintava: http %d: %s", e.StatusCode, e.Message)
}

// Refused reports whether Fintava understood the request and declined it, as
// opposed to failing to answer. 4xx is the rail's verdict; 5xx is its absence.
func (e *APIError) Refused() bool { return e.StatusCode >= 400 && e.StatusCode < 500 }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("fintava: marshal: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("fintava: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("fintava: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("fintava: read body: %w", err)
	}

	var env envelope
	_ = json.Unmarshal(raw, &env) // tolerate non-envelope bodies

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := env.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		return &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
	if out != nil {
		// Prefer the data field; fall back to the whole body for
		// endpoints that respond bare.
		src := env.Data
		if len(src) == 0 || string(src) == "null" {
			src = raw
		}
		if err := json.Unmarshal(src, out); err != nil {
			return fmt.Errorf("fintava: decode %s: %w", path, err)
		}
	}
	return nil
}

// flexDecimal accepts numbers or numeric strings.
type flexDecimal struct{ decimal.Decimal }

func (f *flexDecimal) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		f.Decimal = decimal.Zero
		return nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return err
	}
	f.Decimal = d
	return nil
}

// Bank is one beneficiary bank; SortCode is what transfers/enquiries
// take.
type Bank struct {
	Name     string `json:"name"`
	BankName string `json:"bankName"`
	SortCode string `json:"sortCode"`
	Code     string `json:"code"`
}

func (b Bank) DisplayName() string {
	if b.Name != "" {
		return b.Name
	}
	return b.BankName
}

func (b Bank) BankCode() string {
	if b.SortCode != "" {
		return b.SortCode
	}
	return b.Code
}

// ListBanks returns the beneficiary bank list.
func (c *Client) ListBanks(ctx context.Context) ([]Bank, error) {
	var out []Bank
	if err := c.do(ctx, http.MethodGet, "/banks", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MerchantWallet is the merchant wallet read. Live-verified shape:
//
//	{"data":{"accountName":"octa hq","accountNumber":"0033988783",
//	         "balance":{"bookedBalance":0,"availableBalance":0}}}
//
// The wallet has its own NUBAN — usable as the float reload
// destination.
type MerchantWallet struct {
	AccountName   string `json:"accountName"`
	AccountNumber string `json:"accountNumber"`
	Balance       struct {
		BookedBalance    flexDecimal `json:"bookedBalance"`
		AvailableBalance flexDecimal `json:"availableBalance"`
	} `json:"balance"`
}

func (m MerchantWallet) Available() decimal.Decimal { return m.Balance.AvailableBalance.Decimal }
func (m MerchantWallet) Booked() decimal.Decimal    { return m.Balance.BookedBalance.Decimal }

// MerchantBalance reads the pooled merchant wallet.
func (c *Client) MerchantBalance(ctx context.Context) (*MerchantWallet, error) {
	var out MerchantWallet
	if err := c.do(ctx, http.MethodGet, "/merchant/balance", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NameEnquiryResult resolves a beneficiary.
type NameEnquiryResult struct {
	AccountName   string `json:"accountName"`
	AccountNumber string `json:"accountNumber"`
	SortCode      string `json:"sortCode"`
	BankName      string `json:"bankName"`
}

// NameEnquiry resolves the account name behind number+sortCode.
func (c *Client) NameEnquiry(ctx context.Context, accountNumber, sortCode string) (*NameEnquiryResult, error) {
	q := url.Values{"accountNumber": {accountNumber}, "sortCode": {sortCode}}
	var out NameEnquiryResult
	if err := c.do(ctx, http.MethodGet, "/name/enquiry?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TransferResult is the tolerant decode of transfer submit/status
// responses.
type TransferResult struct {
	Reference         string      `json:"reference"`
	TransactionRef    string      `json:"transactionReference"`
	CustomerReference string      `json:"customerReference"`
	Status            string      `json:"status"`
	Amount            flexDecimal `json:"amount"`
	Charges           flexDecimal `json:"charges"`
	Message           string      `json:"message"`
}

func (t TransferResult) AnyReference() string {
	if t.Reference != "" {
		return t.Reference
	}
	return t.TransactionRef
}

// MerchantTransfer pays a bank account from the MERCHANT wallet.
// customerReference is OUR deterministic reference (claim-first refs
// from the dispatcher) — Fintava echoes it on webhooks.
func (c *Client) MerchantTransfer(ctx context.Context, customerReference string, amount decimal.Decimal, accountNumber, accountName, sortCode, narration string) (*TransferResult, error) {
	body := map[string]any{
		"amount":            amount, // naira (documented float, e.g. 1000.00)
		"accountNumber":     accountNumber,
		"accountName":       accountName,
		"sortCode":          sortCode,
		"narration":         narration,
		"CustomerReference": customerReference, // sic — documented capitalised
	}
	var out TransferResult
	if err := c.do(ctx, http.MethodPost, "/bank/credit/merchant", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CustomerTransfer pays a bank account from a CUSTOMER wallet rather than
// the merchant wallet: POST /bank/credit with sourceId naming the customer.
//
// This is how a cardholder's own naira, in their STATIC_FUND deposit
// wallet, pays a merchant. The Zerocard backbone (integrations/fintava)
// sends the same body with sourceId as the customer id.
func (c *Client) CustomerTransfer(ctx context.Context, sourceID, customerReference string, amount decimal.Decimal, accountNumber, accountName, sortCode, narration string) (*TransferResult, error) {
	body := map[string]any{
		"sourceId":          sourceID,
		"amount":            amount,
		"accountNumber":     accountNumber,
		"accountName":       accountName,
		"sortCode":          sortCode,
		"narration":         narration,
		"CustomerReference": customerReference,
	}
	var out TransferResult
	if err := c.do(ctx, http.MethodPost, "/bank/credit", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TransactionByReference looks a transaction up by reference (vendor
// reference, or our CustomerReference where Fintava indexes it).
func (c *Client) TransactionByReference(ctx context.Context, ref string) (*TransferResult, error) {
	var out TransferResult
	if err := c.do(ctx, http.MethodGet, "/transaction/reference/"+url.PathEscape(ref), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateCustomerRequest opens a permanent STATIC_FUND customer wallet
// — the LP deposit account. Fintava verifies BVN/NIN itself.
type CreateCustomerRequest struct {
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	PhoneNumber string `json:"phoneNumber"`
	Email       string `json:"email"`
	Address     string `json:"address"`
	DateOfBirth string `json:"dateOfBirth"` // YYYY-MM-DD
	BVN         string `json:"bvn"`
	NIN         string `json:"nin"`
}

// BankRef is a bank as a rail names it: a field sent either as a bare string
// ("loma") or as an object ({"name":"Loma MFB","code":"090620"}). The
// backbone's pickProviderField only ever saw strings; being ready for the
// object shape costs nothing and means a schema change on their side
// degrades to "no bank" rather than a decode error that loses the account.
type BankRef struct {
	Name string
	Code string
}

func (f *BankRef) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*f = BankRef{}
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = BankRef{Name: strings.TrimSpace(v)}
		return nil
	}
	var obj struct {
		Name     string `json:"name"`
		BankName string `json:"bankName"`
		Title    string `json:"title"`
		Code     string `json:"code"`
		BankCode string `json:"bankCode"`
		SortCode string `json:"sortCode"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		// Something else entirely (a number, an array): not a bank, not an
		// error worth failing the whole customer over.
		*f = BankRef{}
		return nil
	}
	*f = BankRef{
		Name: strings.TrimSpace(firstNonEmpty(obj.Name, obj.BankName, obj.Title)),
		Code: strings.TrimSpace(firstNonEmpty(obj.SortCode, obj.BankCode, obj.Code)),
	}
	return nil
}

// Customer is the tolerant decode of a created/fetched customer with
// their wallet.
//
// Live shape of POST /create/customer (mirrored from the Zerocard
// backbone, which has seen it):
//
//	{"data": {"userInfo": {"id": "<customer id>", ...},
//	          "wallet":   {"id": "<wallet id>", "accountNumber": "...",
//	                       "accountName": "...", "serviceProvider": "loma",
//	                       "bank": "...", "fundMethod": "STATIC_FUND"}}}
//
// The bank is `wallet.serviceProvider` and/or `wallet.bank` -- never
// `bankName`, which is what this struct read for its first weeks and got
// nothing from.
type Customer struct {
	ID       string `json:"id"`
	CustID   string `json:"customerId"`
	UserInfo struct {
		ID string `json:"id"`
	} `json:"userInfo"`
	Wallet struct {
		ID              string      `json:"id"`
		AccountNumber   string      `json:"accountNumber"`
		AccountName     string      `json:"accountName"`
		BankName        string      `json:"bankName"`
		ServiceProvider BankRef     `json:"serviceProvider"`
		Bank            BankRef     `json:"bank"`
		Balance         flexDecimal `json:"availableBalance"`
	} `json:"wallet"`
	AccountNumber   string  `json:"accountNumber"` // some responses flatten
	AccountName     string  `json:"accountName"`
	BankName        string  `json:"bankName"`
	ServiceProvider BankRef `json:"serviceProvider"`
	Bank            BankRef `json:"bank"`
}

// CustomerID is Fintava's id for the person (userInfo.id on the live
// shape). Support-facing; it is not what the wallet endpoints take.
func (cu Customer) CustomerID() string {
	return firstNonEmpty(cu.CustID, cu.ID, cu.UserInfo.ID)
}

// WalletID is what /customer/wallet/balance/{id} takes.
func (cu Customer) WalletID() string { return cu.Wallet.ID }

func (cu Customer) DepositAccountNumber() string {
	if cu.Wallet.AccountNumber != "" {
		return cu.Wallet.AccountNumber
	}
	return cu.AccountNumber
}

// RawBank is the bank exactly as the rail named it -- "loma", or a bank
// object -- with whichever code it carried. Empty when the response had
// none; it is the caller's job to decide what to show then. There is
// deliberately no placeholder here any more: "Fintava partner bank" was
// shown to real people as the bank to send their money to.
func (cu Customer) RawBank() BankRef {
	for _, f := range []BankRef{
		cu.Wallet.ServiceProvider, cu.Wallet.Bank, {Name: cu.Wallet.BankName},
		cu.ServiceProvider, cu.Bank, {Name: cu.BankName},
	} {
		if f.Name != "" || f.Code != "" {
			return f
		}
	}
	return BankRef{}
}

// customerListItem is one row of GET /customers/list. The wallet hangs
// off userInfo there, not off the row.
type customerListItem struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	UserInfo struct {
		ID     string `json:"id"`
		Wallet struct {
			ID            string  `json:"id"`
			AccountNumber string  `json:"accountNumber"`
			AccountName   string  `json:"accountName"`
			Provider      BankRef `json:"serviceProvider"`
			Bank          BankRef `json:"bank"`
		} `json:"wallet"`
	} `json:"userInfo"`
}

// FindCustomerWallet looks a customer up by search term (their email)
// and returns the one whose wallet has the given account number.
//
// This exists for accounts opened before the wallet id was recorded:
// the balance endpoint wants the wallet id, the row has none, and the
// account number is the one identifier both sides agree on.
func (c *Client) FindCustomerWallet(ctx context.Context, searchTerm, accountNumber string) (*Customer, error) {
	q := url.Values{"searchTerm": {searchTerm}, "take": {"50"}}
	var rows []customerListItem
	if err := c.do(ctx, http.MethodGet, "/customers/list?"+q.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		w := r.UserInfo.Wallet
		if w.AccountNumber != accountNumber {
			continue
		}
		var cu Customer
		cu.ID = r.ID
		cu.UserInfo.ID = r.UserInfo.ID
		cu.Wallet.ID = w.ID
		cu.Wallet.AccountNumber = w.AccountNumber
		cu.Wallet.AccountName = w.AccountName
		cu.Wallet.ServiceProvider = w.Provider
		cu.Wallet.Bank = w.Bank
		return &cu, nil
	}
	return nil, fmt.Errorf("fintava: no customer matching %q holds account %s", searchTerm, accountNumber)
}

// ResolveBank turns what the rail called the bank into what a person
// should see, and the code a transfer to it needs.
//
// "loma" is what Fintava says; "Loma Microfinance Bank" and its NIP code
// are what somebody typing a transfer into their banking app needs. The
// bank list is the source of both: the raw name is matched against it
// case-insensitively, either way round, so "loma" finds "Loma
// Microfinance Bank" and "Iyin-Ekiti MFB" finds "Iyin-Ekiti Microfinance
// Bank". When the catalogue cannot be read or has no match, the raw name
// is tidied (title case, "Bank" appended) and the code is whatever the
// response carried -- never invented.
func (c *Client) ResolveBank(ctx context.Context, raw BankRef) (name, code string) {
	rawName := strings.TrimSpace(raw.Name)
	if rawName == "" {
		return "", raw.Code
	}
	if c != nil {
		if banks, err := c.ListBanks(ctx); err == nil {
			if b, ok := matchBank(banks, rawName, raw.Code); ok {
				return b.DisplayName(), firstNonEmpty(b.BankCode(), raw.Code)
			}
		}
	}
	return tidyBankName(rawName), raw.Code
}

// matchBank finds the catalogue entry for a raw provider name or code.
//
// Names are compared as sets of distinctive words -- "Loma Microfinance
// Bank" is {loma}, "Iyin-Ekiti MFB" is {iyin-ekiti} -- because a substring
// match finds "loma" inside "Diploma Bank" and a person would then be told to
// send money to the wrong bank. Equal sets win; otherwise the catalogue entry
// whose set contains the raw one with the fewest extra words.
func matchBank(banks []Bank, rawName, rawCode string) (Bank, bool) {
	// A code the response carried is the least ambiguous handle; try it first.
	if rawCode != "" {
		for _, b := range banks {
			if b.BankCode() == rawCode {
				return b, true
			}
		}
	}
	needle := distinctiveWords(rawName)
	if len(needle) == 0 {
		return Bank{}, false
	}
	var best Bank
	bestExtra := -1
	for _, b := range banks {
		have := distinctiveWords(b.DisplayName())
		if len(have) == 0 || !subset(needle, have) {
			continue
		}
		extra := len(have) - len(needle)
		if extra == 0 {
			return b, true
		}
		if bestExtra < 0 || extra < bestExtra {
			best, bestExtra = b, extra
		}
	}
	return best, bestExtra >= 0
}

// genericBankWords carry no identity: every microfinance bank has them.
var genericBankWords = map[string]bool{
	"bank": true, "banks": true, "microfinance": true, "micro": true, "finance": true,
	"mfb": true, "plc": true, "ltd": true, "limited": true, "nigeria": true, "the": true,
	"of": true, "and": true, "&": true,
}

func distinctiveWords(name string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(name)) {
		w = strings.Trim(w, ".,()")
		if w != "" && !genericBankWords[w] {
			out[w] = true
		}
	}
	return out
}

func subset(small, big map[string]bool) bool {
	for w := range small {
		if !big[w] {
			return false
		}
	}
	return true
}

// tidyBankName is the last resort: "loma" → "Loma Bank",
// "iyin-ekiti mfb" → "Iyin-Ekiti Mfb".
func tidyBankName(raw string) string {
	words := strings.Fields(strings.ToLower(raw))
	for i, w := range words {
		parts := strings.Split(w, "-")
		for j, p := range parts {
			if p != "" {
				parts[j] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		words[i] = strings.Join(parts, "-")
	}
	name := strings.Join(words, " ")
	if !strings.Contains(strings.ToLower(name), "bank") && !strings.Contains(strings.ToLower(name), "mfb") {
		name += " Bank"
	}
	return name
}

// CreateCustomer opens the permanent wallet (STATIC_FUND: funds remain
// until transferred — bank-account semantics).
func (c *Client) CreateCustomer(ctx context.Context, req CreateCustomerRequest) (*Customer, error) {
	body := map[string]any{
		"firstName":     req.FirstName,
		"lastName":      req.LastName,
		"phoneNumber":   req.PhoneNumber,
		"email":         req.Email,
		"fundingMethod": "STATIC_FUND",
		"address":       req.Address,
		"dateOfBirth":   req.DateOfBirth,
		"bvn":           req.BVN,
		"nin":           req.NIN,
	}
	var out Customer
	if err := c.do(ctx, http.MethodPost, "/create/customer", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WalletBalance reads one customer wallet.
func (c *Client) WalletBalance(ctx context.Context, walletID string) (decimal.Decimal, error) {
	// Live Fintava answers {"balance": {"bookedBalance": 100, "availableBalance": 100}};
	// an older shape put the figures at the top level. Take whichever is there.
	var out struct {
		Balance          json.RawMessage `json:"balance"`
		AvailableBalance flexDecimal     `json:"availableBalance"`
	}
	if err := c.do(ctx, http.MethodGet, "/customer/wallet/balance/"+url.PathEscape(walletID), nil, &out); err != nil {
		return decimal.Zero, err
	}
	if !out.AvailableBalance.IsZero() {
		return out.AvailableBalance.Decimal, nil
	}
	if len(out.Balance) > 0 && out.Balance[0] == '{' {
		var nested struct {
			Booked    flexDecimal `json:"bookedBalance"`
			Available flexDecimal `json:"availableBalance"`
		}
		if err := json.Unmarshal(out.Balance, &nested); err != nil {
			return decimal.Zero, fmt.Errorf("fintava: decode wallet balance: %w", err)
		}
		if !nested.Available.IsZero() {
			return nested.Available.Decimal, nil
		}
		return nested.Booked.Decimal, nil
	}
	var flat flexDecimal
	if len(out.Balance) > 0 {
		if err := json.Unmarshal(out.Balance, &flat); err != nil {
			return decimal.Zero, fmt.Errorf("fintava: decode wallet balance: %w", err)
		}
	}
	return flat.Decimal, nil
}

// -----------------------------------------------------------------------------
// Compliance: identity verification
//
// Fintava verifies identities on the same key that opens accounts, which is
// why KYC lives on this client rather than behind a second vendor. Both calls
// answer synchronously -- there is no callback to wait for and no job to poll.
// -----------------------------------------------------------------------------

// BVNRecord is what the bank holds against a Bank Verification Number.
//
// It is the authoritative spelling of somebody's name and date of birth, which
// is the point: a person's own typing is a claim, and this is the record that
// claim is checked against.
type BVNRecord struct {
	// Customer is Fintava's id for the person behind the BVN.
	Customer string `json:"customer"`
	BVN      string `json:"bvn"`

	FirstName  string `json:"first_name"`
	MiddleName string `json:"middle_name"`
	LastName   string `json:"last_name"`
	// DateOfBirth is YYYY-MM-DD.
	DateOfBirth string `json:"date_of_birth"`
	Phone       string `json:"phone_number1"`
	Gender      string `json:"gender"`

	// Image is the base64 photograph the bank holds. It is returned by the
	// endpoint and deliberately never persisted: it is biometric data about a
	// person, it is of no use once a selfie has been matched against it, and
	// the only thing keeping it would add to this system is a breach.
	Image string `json:"image"`
}

// VerifyBVN reads the bank's record for a BVN.
//
//	GET /compliance/verify/bvn?bvn=...
//
// This is a LOOKUP, not a match. It returns the record behind any valid BVN,
// including one read off somebody else's bank slip -- so the caller must
// compare the record to what the person actually claimed. See the kyc/fintava
// provider, which does exactly that.
func (c *Client) VerifyBVN(ctx context.Context, bvn string) (*BVNRecord, error) {
	q := url.Values{"bvn": {bvn}}
	var out BVNRecord
	if err := c.do(ctx, http.MethodGet, "/compliance/verify/bvn?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VerifyBVNSelfie matches a live face against the photo held behind a BVN.
//
//	POST /compliance/verify/bvn/selfie   {"bvn": "...", "image": "<base64>"}
//
// The endpoint answers with an empty body either way, so the HTTP status IS
// the answer: 2xx is a match, 4xx is not. A nil error therefore means matched,
// and an *APIError with Refused() true means it did not -- which the caller
// must not confuse with the 5xx/transport case, where nothing was decided.
func (c *Client) VerifyBVNSelfie(ctx context.Context, bvn, imageBase64 string) error {
	return c.do(ctx, http.MethodPost, "/compliance/verify/bvn/selfie", map[string]any{
		"bvn":   bvn,
		"image": imageBase64,
	}, nil)
}
