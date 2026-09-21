// Package cdp puts deposit addresses behind Coinbase Developer Platform Smart
// Accounts.
//
// Two things change versus a derived address, and both are the point:
//
//   - The key never exists in this process. CDP generates and holds it in a
//     TEE; this service asks for signatures and never sees the material. That
//     is the custody boundary the derived scheme could not offer.
//   - A paymaster pays the gas. Sweeping needs no ETH at the address, and
//     none at the treasury either.
//
// Everything here goes through the official SDK: its auth package produces the
// two JWTs CDP requires, and its generated client is the API surface. Nothing
// is re-implemented that the vendor ships, because a hand-rolled request
// signature that is subtly wrong is indistinguishable from a forged one.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coinbase/cdp-sdk/go/auth"
	"github.com/coinbase/cdp-sdk/go/openapi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/internal/chain/base"
)

// Config is what the client needs. Every field is read from the environment
// by config.CDPConfig; nothing here has a default a secret could hide behind.
type Config struct {
	APIKeyID     string
	APIKeySecret string
	WalletSecret string
	PaymasterURL string
	BaseURL      string
}

// Client talks to CDP for one chain.
type Client struct {
	api     *openapi.ClientWithResponses
	cfg     Config
	network openapi.EvmUserOperationNetwork

	// host and prefix are taken from BaseURL and used to build the request
	// path the wallet JWT is signed over. It has to match what the generated
	// client actually sends, byte for byte, or the signature is refused.
	host   string
	prefix string
}

// networkFor maps a chain id to the name CDP uses for it.
//
// Refuses anything it does not know rather than guessing. A user operation
// sent to the wrong network with a valid signature is not rejected -- it
// is executed, on a chain nobody meant.
func networkFor(chainID int64) (openapi.EvmUserOperationNetwork, error) {
	switch chainID {
	case 8453:
		return openapi.EvmUserOperationNetwork("base"), nil
	case 84532:
		return openapi.EvmUserOperationNetwork("base-sepolia"), nil
	default:
		return "", fmt.Errorf("cdp: chain id %d is not a network CDP smart accounts support", chainID)
	}
}

// New builds a client, or refuses with a reason an operator can act on.
func New(cfg Config, chainID int64) (*Client, error) {
	if cfg.APIKeyID == "" || cfg.APIKeySecret == "" || cfg.WalletSecret == "" {
		return nil, errors.New("cdp: CDP_API_KEY_ID, CDP_API_KEY_SECRET and CDP_WALLET_SECRET are all required")
	}
	if cfg.PaymasterURL == "" {
		// Not optional. A smart account without a paymaster must hold ETH to
		// act, which is the exact thing this integration exists to remove.
		return nil, errors.New("cdp: CDP_PAYMASTER_URL is required -- without it smart accounts are not gasless")
	}
	network, err := networkFor(chainID)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("cdp: CDP_BASE_URL %q is not a URL", cfg.BaseURL)
	}

	c := &Client{cfg: cfg, network: network, host: u.Host, prefix: strings.TrimRight(u.Path, "/")}

	// The bearer token is per request: it is bound to the method, host and
	// path, and lives two minutes. Attaching it in an editor means every call
	// the generated client makes is signed, and none can be made unsigned.
	api, err := openapi.NewClientWithResponses(cfg.BaseURL,
		openapi.WithRequestEditorFn(c.bearer),
		openapi.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}))
	if err != nil {
		return nil, fmt.Errorf("cdp: client: %w", err)
	}
	c.api = api
	return c, nil
}

func (c *Client) bearer(_ context.Context, req *http.Request) error {
	jwt, err := auth.GenerateJWT(auth.JwtOptions{
		KeyID:         c.cfg.APIKeyID,
		KeySecret:     c.cfg.APIKeySecret,
		RequestMethod: req.Method,
		RequestHost:   req.URL.Host,
		RequestPath:   req.URL.Path,
	})
	if err != nil {
		return fmt.Errorf("cdp: bearer jwt: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	return nil
}

// walletJWT signs the operations that create or spend from accounts.
//
// body is the exact JSON object the request will carry. The SDK canonicalises
// it and includes its hash in the token, so the map handed here must describe
// precisely what the generated client marshals -- same keys, nothing omitted
// on one side and present on the other.
func (c *Client) walletJWT(method, path string, body map[string]any) (string, error) {
	jwt, err := auth.GenerateWalletJWT(auth.WalletJwtOptions{
		WalletSecret:  c.cfg.WalletSecret,
		RequestMethod: method,
		RequestHost:   c.host,
		RequestPath:   c.prefix + path,
		RequestData:   body,
	})
	if err != nil {
		return "", fmt.Errorf("cdp: wallet jwt: %w", err)
	}
	return jwt, nil
}

// APIError is CDP refusing a request, with enough to take to their support.
type APIError struct {
	Status        int
	Type          string
	Message       string
	CorrelationID string
}

func (e *APIError) Error() string {
	if e.CorrelationID != "" {
		return fmt.Sprintf("cdp: %s (%d): %s [correlation %s]", e.Type, e.Status, e.Message, e.CorrelationID)
	}
	return fmt.Sprintf("cdp: %s (%d): %s", e.Type, e.Status, e.Message)
}

// apiError decodes whatever CDP sent back when it did not send what we asked
// for. Every error body is the same Error schema, so one decoder serves all.
func apiError(status int, body []byte) error {
	var e openapi.Error
	if json.Unmarshal(body, &e) == nil && e.ErrorMessage != "" {
		out := &APIError{Status: status, Type: string(e.ErrorType), Message: e.ErrorMessage}
		if e.CorrelationId != nil {
			out.CorrelationID = *e.CorrelationId
		}
		return out
	}
	return &APIError{Status: status, Type: "unknown", Message: strings.TrimSpace(string(body))}
}

// Names identify accounts in the CDP project and are unique there, which
// makes them the recovery key when a run dies between creating an account
// and recording it. Alphanumeric plus hyphens, 36 characters at most: a user
// id is 32 hex characters once its hyphens are dropped, and the first twenty
// of those are eighty bits -- more than enough to keep users apart.
func ownerName(user uuid.UUID) string   { return "tapp-owner-" + hexPrefix(user) }
func depositName(user uuid.UUID) string { return "tapp-deposit-" + hexPrefix(user) }

func hexPrefix(user uuid.UUID) string {
	return strings.ReplaceAll(user.String(), "-", "")[:20]
}

// checksummed renders an address the way CDP addresses its own resources.
//
// CDP puts the smart account in the request path and matches it exactly, in
// EIP-55 mixed case. Our tables store addresses lower-cased -- the watcher
// matches log topics and deposits join on to_address, and a single canonical
// case is what makes those comparisons safe -- so every address handed to CDP
// has to be converted back on the way out. A lower-case one comes back as
// 404 "EVM smart account with the given address not found", which reads as an
// account that was never created rather than a spelling difference.
func checksummed(address string) string {
	return common.HexToAddress(address).Hex()
}

// idempotencyKey is stable for a (purpose, subject) pair, so a retried call
// is the same call to CDP and returns the same result rather than a second
// account or a second send. Derived, not stored: there is nothing to lose.
func idempotencyKey(purpose string, subject string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("tapp:cdp:"+purpose+":"+subject)).String()
}

// EnsureSmartAccount returns the user's Smart Account, creating the owner and
// the account on first use.
//
// Safe to call again after any failure. The Smart Account is looked up by
// name first, so a run that created it but died before recording it finds it
// rather than creating a second; and every create carries an idempotency key,
// so a retry of the same step returns the same account.
func (c *Client) EnsureSmartAccount(ctx context.Context, user uuid.UUID) (base.SmartAccount, error) {
	name := depositName(user)

	found, err := c.api.GetEvmSmartAccountByNameWithResponse(ctx, name)
	if err != nil {
		return base.SmartAccount{}, fmt.Errorf("cdp: look up smart account %s: %w", name, err)
	}
	if found.JSON200 != nil {
		return fromSmartAccount(found.JSON200), nil
	}
	if found.StatusCode() != http.StatusNotFound {
		return base.SmartAccount{}, apiError(found.StatusCode(), found.Body)
	}

	owner, err := c.ensureOwner(ctx, user)
	if err != nil {
		return base.SmartAccount{}, err
	}

	idem := idempotencyKey("smart-account", user.String())
	created, err := c.api.CreateEvmSmartAccountWithResponse(ctx,
		&openapi.CreateEvmSmartAccountParams{XIdempotencyKey: &idem},
		openapi.CreateEvmSmartAccountJSONRequestBody{Owners: []string{owner}, Name: &name})
	if err != nil {
		return base.SmartAccount{}, fmt.Errorf("cdp: create smart account: %w", err)
	}
	if created.JSON201 == nil {
		return base.SmartAccount{}, apiError(created.StatusCode(), created.Body)
	}
	return fromSmartAccount(created.JSON201), nil
}

func fromSmartAccount(a *openapi.EvmSmartAccount) base.SmartAccount {
	out := base.SmartAccount{Address: strings.ToLower(a.Address)}
	if len(a.Owners) > 0 {
		out.Owner = strings.ToLower(a.Owners[0])
	}
	if a.Name != nil {
		out.Name = *a.Name
	}
	return out
}

// ensureOwner returns the address of the CDP EVM account that will own and
// sign for the user's Smart Account, creating it on first use.
func (c *Client) ensureOwner(ctx context.Context, user uuid.UUID) (string, error) {
	name := ownerName(user)

	existing, err := c.api.GetEvmAccountByNameWithResponse(ctx, name)
	if err != nil {
		return "", fmt.Errorf("cdp: look up owner %s: %w", name, err)
	}
	if existing.JSON200 != nil {
		return strings.ToLower(existing.JSON200.Address), nil
	}
	if existing.StatusCode() != http.StatusNotFound {
		return "", apiError(existing.StatusCode(), existing.Body)
	}

	const path = "/v2/evm/accounts"
	body := map[string]any{"name": name}
	walletAuth, err := c.walletJWT(http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	idem := idempotencyKey("owner", user.String())

	created, err := c.api.CreateEvmAccountWithResponse(ctx,
		&openapi.CreateEvmAccountParams{XWalletAuth: &walletAuth, XIdempotencyKey: &idem},
		openapi.CreateEvmAccountJSONRequestBody{Name: &name})
	if err != nil {
		return "", fmt.Errorf("cdp: create owner: %w", err)
	}
	if created.JSON201 == nil {
		return "", apiError(created.StatusCode(), created.Body)
	}
	return strings.ToLower(created.JSON201.Address), nil
}

// SweepSmartAccount moves USDC from a Smart Account to the treasury as a
// gas-sponsored user operation, and returns the hash of the transaction that
// carried it once it is final.
//
// idem must be stable for the sweep being attempted -- the deposit id serves
// -- so a retry after a lost response does not send twice.
func (c *Client) SweepSmartAccount(
	ctx context.Context, account string, usdc, to common.Address, amount *big.Int, idem string,
) (string, error) {
	if amount == nil || amount.Sign() <= 0 {
		return "", errors.New("cdp: nothing to sweep")
	}

	// CDP addresses its smart accounts by the EIP-55 checksummed string and
	// compares it exactly: the same account in lower case is a 404, "EVM
	// smart account with the given address not found", which reads as though
	// the account was never created.
	//
	// Our own tables store addresses lower-cased on purpose -- the watcher
	// matches log topics and deposits join on to_address, and one canonical
	// case is what makes those comparisons safe. So the conversion belongs
	// here, at the boundary, rather than in the column.
	account = checksummed(account)

	data, err := base.PackTransfer(to, amount)
	if err != nil {
		return "", err
	}

	// One object feeds both the signature and the request, so they cannot
	// disagree about what is being sent.
	call := map[string]any{"to": usdc.Hex(), "value": "0", "data": data}
	body := map[string]any{
		"calls":        []any{call},
		"network":      string(c.network),
		"paymasterUrl": c.cfg.PaymasterURL,
	}
	path := "/v2/evm/smart-accounts/" + account + "/user-operations/prepare-and-send"
	walletAuth, err := c.walletJWT(http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	paymaster := c.cfg.PaymasterURL

	sent, err := c.api.PrepareAndSendUserOperationWithResponse(ctx, account,
		&openapi.PrepareAndSendUserOperationParams{XWalletAuth: &walletAuth, XIdempotencyKey: &idem},
		openapi.PrepareAndSendUserOperationJSONRequestBody{
			Calls:        []openapi.EvmCall{{To: usdc.Hex(), Value: "0", Data: data}},
			Network:      c.network,
			PaymasterUrl: &paymaster,
		})
	if err != nil {
		return "", fmt.Errorf("cdp: send user operation: %w", err)
	}
	if sent.JSON200 == nil {
		return "", apiError(sent.StatusCode(), sent.Body)
	}
	return c.waitUserOperation(ctx, account, sent.JSON200.UserOpHash)
}

// Call is one contract call in a user operation.
//
// Deliberately generic: the same sponsored path that sweeps a deposit also
// sells one, and the difference between them is calldata, not machinery.
type Call struct {
	To   common.Address
	Data string // 0x-prefixed hex, as PackTransfer and abi.Pack produce
}

// SendCalls submits calls from a smart account as one sponsored user
// operation and returns the transaction hash once it is final.
//
// One operation, not several: an approve that lands without the call it was
// granted for leaves an allowance sitting on a contract, and a call that
// lands without its approve simply reverts. Atomicity here is what makes
// "approve then spend" safe to retry.
//
// idem must be stable for the operation being attempted, so a retry after a
// lost response cannot send twice.
func (c *Client) SendCalls(
	ctx context.Context, account string, calls []Call, idem string,
) (string, error) {
	if len(calls) == 0 {
		return "", errors.New("cdp: no calls to send")
	}
	// CDP addresses smart accounts in EIP-55 and matches exactly; see
	// checksummed.
	account = checksummed(account)

	// One slice feeds both the signature and the request, so they cannot
	// disagree about what is being sent.
	signed := make([]any, 0, len(calls))
	typed := make([]openapi.EvmCall, 0, len(calls))
	for _, call := range calls {
		to := call.To.Hex()
		signed = append(signed, map[string]any{"to": to, "value": "0", "data": call.Data})
		typed = append(typed, openapi.EvmCall{To: to, Value: "0", Data: call.Data})
	}

	body := map[string]any{
		"calls":        signed,
		"network":      string(c.network),
		"paymasterUrl": c.cfg.PaymasterURL,
	}
	path := "/v2/evm/smart-accounts/" + account + "/user-operations/prepare-and-send"
	walletAuth, err := c.walletJWT(http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	paymaster := c.cfg.PaymasterURL

	sent, err := c.api.PrepareAndSendUserOperationWithResponse(ctx, account,
		&openapi.PrepareAndSendUserOperationParams{XWalletAuth: &walletAuth, XIdempotencyKey: &idem},
		openapi.PrepareAndSendUserOperationJSONRequestBody{
			Calls:        typed,
			Network:      c.network,
			PaymasterUrl: &paymaster,
		})
	if err != nil {
		return "", fmt.Errorf("cdp: send user operation: %w", err)
	}
	if sent.JSON200 == nil {
		return "", apiError(sent.StatusCode(), sent.Body)
	}
	return c.waitUserOperation(ctx, account, sent.JSON200.UserOpHash)
}

// waitUserOperation polls until the operation is final and returns the
// on-chain transaction hash. The user operation hash is not it: one
// transaction bundles many operations, and it is the transaction that a
// receipt, a block explorer and the gas recorder all key on.
func (c *Client) waitUserOperation(ctx context.Context, account, userOpHash string) (string, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		op, err := c.api.GetUserOperationWithResponse(ctx, account, userOpHash)
		if err != nil {
			return "", fmt.Errorf("cdp: poll user operation: %w", err)
		}
		if op.JSON200 == nil {
			return "", apiError(op.StatusCode(), op.Body)
		}

		switch op.JSON200.Status {
		case openapi.EvmUserOperationStatusComplete:
			if op.JSON200.TransactionHash != nil && *op.JSON200.TransactionHash != "" {
				return strings.ToLower(*op.JSON200.TransactionHash), nil
			}
			return "", fmt.Errorf("cdp: user operation %s is complete but reports no transaction hash", userOpHash)

		case openapi.EvmUserOperationStatusFailed, openapi.EvmUserOperationStatusDropped:
			reason := string(op.JSON200.Status)
			if op.JSON200.Receipts != nil {
				for _, r := range *op.JSON200.Receipts {
					if r.Revert != nil && r.Revert.Message != "" {
						reason += ": " + r.Revert.Message
					}
				}
			}
			return "", fmt.Errorf("cdp: user operation %s %s", userOpHash, reason)
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("cdp: waiting for user operation %s: %w", userOpHash, ctx.Err())
		case <-ticker.C:
		}
	}
}

// ProbeSmartAccount reports what CDP holds for a user's deposit account,
// without creating anything.
//
// Diagnostic only. It looks the account up by NAME, which is how creation
// finds an existing one, so that a failure keyed on the ADDRESS can be told
// apart from the account genuinely not existing.
func (c *Client) ProbeSmartAccount(
	ctx context.Context, user uuid.UUID,
) (status int, address, name string, err error) {
	name = depositName(user)
	found, err := c.api.GetEvmSmartAccountByNameWithResponse(ctx, name)
	if err != nil {
		return 0, "", name, fmt.Errorf("cdp: look up smart account %s: %w", name, err)
	}
	if found.JSON200 != nil {
		return found.StatusCode(), found.JSON200.Address, name, nil
	}
	return found.StatusCode(), "", name, nil
}
