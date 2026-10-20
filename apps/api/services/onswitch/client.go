// Package onswitch is the client for Switch, the provider that turns USDC on
// Base into naira in a Nigerian bank account.
//
// A payout is a deposit, not an order: initiate names the bank and the exact
// naira to deliver, Switch answers with a single-use address and the USDC it
// wants there, the USDC is sent, and confirm hands Switch the transaction.
// Status is then read until the payment is completed, or failed and refunded
// to the refund address.
//
// Every response is wrapped as {success, message, timestamp, data}. A body
// that does not decode into the shape asked for is an error, never a zero
// value: a payment whose deposit amount reads as 0 must not look payable.
package onswitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Asset is the only thing Switch is ever sent from here: native USDC on Base.
const Asset = "base:usdc"

// Statuses a payment reports. The spec lists the first four; Switch's webhook
// page adds the last three. Anything else is unknown and is never mapped by
// guessing.
const (
	StatusAwaitingDeposit = "AWAITING_DEPOSIT"
	StatusProcessing      = "PROCESSING"
	StatusCompleted       = "COMPLETED"
	StatusFailed          = "FAILED"
	StatusScheduled       = "SCHEDULED"
	StatusReversed        = "REVERSED"
	StatusBlocked         = "BLOCKED"
)

// Client calls Switch's API.
type Client struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

// New builds a client with a 30s timeout.
func New(baseURL, serviceKey string) *Client {
	return &Client{
		BaseURL:    strings.TrimSuffix(baseURL, "/"),
		ServiceKey: serviceKey,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Side is one side of a quote or payment.
type Side struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// Quote is a price for one payout.
type Quote struct {
	Rate        float64 `json:"rate"`
	Source      Side    `json:"source"`
	Destination Side    `json:"destination"`
}

// Deposit is where Switch wants the USDC, and how much.
type Deposit struct {
	Address string  `json:"address"`
	Amount  float64 `json:"amount"`
	Asset   string  `json:"asset"`
}

// Payment is one payout as Switch holds it.
type Payment struct {
	Reference   string  `json:"reference"`
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	Source      Side    `json:"source"`
	Destination Side    `json:"destination"`
	Deposit     Deposit `json:"deposit"`
}

// Beneficiary is the bank account a payout pays.
type Beneficiary struct {
	HolderType    string `json:"holder_type"`
	HolderName    string `json:"holder_name"`
	AccountNumber string `json:"account_number"`
	BankCode      string `json:"bank_code"`
}

// QuoteRequest prices a payout. ExactOutput true means Amount is the naira
// delivered; false means it is the USDC sold.
type QuoteRequest struct {
	Amount      float64 `json:"amount"`
	Country     string  `json:"country"`
	Currency    string  `json:"currency"`
	Asset       string  `json:"asset"`
	Channel     string  `json:"channel"`
	ExactOutput bool    `json:"exact_output"`
}

// InitiateRequest opens a payout. Reference must be a UUID; Switch keys the
// payment on it.
type InitiateRequest struct {
	QuoteRequest
	Beneficiary   Beneficiary `json:"beneficiary"`
	Reference     string      `json:"reference"`
	RefundAddress string      `json:"refund_address"`
	Narration     string      `json:"narration,omitempty"`
}

// Quote prices a payout without opening it.
func (c *Client) Quote(ctx context.Context, r QuoteRequest) (Quote, error) {
	var q Quote
	if err := c.call(ctx, http.MethodPost, "/offramp/quote", nil, r, &q); err != nil {
		return Quote{}, err
	}
	if q.Rate <= 0 || q.Source.Amount <= 0 || q.Destination.Amount <= 0 {
		return Quote{}, fmt.Errorf("switch: quote has no usable price: %+v", q)
	}
	return q, nil
}

// Initiate opens a payout and returns where to send the USDC.
func (c *Client) Initiate(ctx context.Context, r InitiateRequest) (Payment, error) {
	var p Payment
	if err := c.call(ctx, http.MethodPost, "/offramp/initiate", nil, r, &p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// Confirm tells Switch which transaction carried the deposit. Safe to repeat.
func (c *Client) Confirm(ctx context.Context, reference, txHash string) error {
	body := map[string]string{"reference": reference, "hash": txHash}
	return c.call(ctx, http.MethodPost, "/payment/confirm", nil, body, nil)
}

// Status reads a payment by its reference.
func (c *Client) Status(ctx context.Context, reference string) (Payment, error) {
	var p Payment
	q := url.Values{"reference": {reference}}
	if err := c.call(ctx, http.MethodGet, "/payment/status", q, nil, &p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// AccountName asks the bank, through Switch, whose account a number is.
// Empty means the bank answered with no name.
func (c *Client) AccountName(ctx context.Context, country, bankCode, accountNumber string) (string, error) {
	body := map[string]any{
		"country": country,
		"beneficiary": map[string]string{
			"account_number": accountNumber,
			"bank_code":      bankCode,
		},
	}
	var out struct {
		AccountName *string `json:"account_name"`
	}
	if err := c.call(ctx, http.MethodPost, "/institution/lookup", nil, body, &out); err != nil {
		return "", err
	}
	if out.AccountName == nil {
		return "", nil
	}
	return strings.TrimSpace(*out.AccountName), nil
}

// ErrNotConfigured means there is no service key to call with.
var ErrNotConfigured = errors.New("switch: SWITCH_SERVICE_KEY is not set")

// Error is a call Switch answered and refused.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("switch: http %d: %s", e.Status, e.Message)
}

type envelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) call(ctx context.Context, method, path string, query url.Values, in, out any) error {
	if c == nil || c.ServiceKey == "" {
		return ErrNotConfigured
	}
	endpoint := c.BaseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("switch: encode %s: %w", path, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-service-key", c.ServiceKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("switch: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("switch: read %s: %w", path, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &Error{Status: resp.StatusCode, Message: truncate(string(raw))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Success {
		return &Error{Status: resp.StatusCode, Message: env.Message}
	}
	if out == nil {
		return nil
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("switch: %s answered with no data", path)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("switch: decode %s: %w", path, err)
	}
	return nil
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
