package cdp

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/coinbase/cdp-sdk/go/openapi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/usezoracle/tapp/api/internal/chain/base"
)

// The wrong network with a valid signature is not rejected; it is executed on
// a chain nobody meant. So the mapping refuses what it does not know.
func TestOnlyKnownChainsMapToANetwork(t *testing.T) {
	for chain, want := range map[int64]string{8453: "base", 84532: "base-sepolia"} {
		got, err := networkFor(chain)
		if err != nil || string(got) != want {
			t.Fatalf("chain %d -> %q, %v; want %q", chain, got, err, want)
		}
	}
	for _, chain := range []int64{1, 0, -1, 10, 8454} {
		if _, err := networkFor(chain); err == nil {
			t.Fatalf("chain %d was mapped to a network; it must be refused", chain)
		}
	}
}

// CDP names: alphanumeric and hyphens, 2-36 chars, unique per project. They
// are the recovery key, so they must also be stable and distinct per user.
func TestAccountNamesFitCDPAndDistinguishUsers(t *testing.T) {
	pattern := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,34}[A-Za-z0-9]$`)
	a, b := uuid.New(), uuid.New()

	for _, name := range []string{ownerName(a), depositName(a), ownerName(b), depositName(b)} {
		if !pattern.MatchString(name) {
			t.Fatalf("%q does not satisfy CDP's account-name pattern", name)
		}
		if len(name) > 36 {
			t.Fatalf("%q is %d chars; CDP allows 36", name, len(name))
		}
	}
	if ownerName(a) != ownerName(a) || depositName(a) != depositName(a) {
		t.Fatal("names are not deterministic; they cannot serve as a recovery key")
	}
	if ownerName(a) == ownerName(b) || depositName(a) == depositName(b) {
		t.Fatal("two users produced the same name")
	}
	if ownerName(a) == depositName(a) {
		t.Fatal("owner and deposit names collide for one user")
	}
}

// A retried call must be the SAME call to CDP. That holds only if the key is
// a pure function of what is being attempted.
func TestIdempotencyKeysAreStableAndScoped(t *testing.T) {
	u := uuid.New().String()
	if idempotencyKey("owner", u) != idempotencyKey("owner", u) {
		t.Fatal("same purpose and subject produced different keys")
	}
	if idempotencyKey("owner", u) == idempotencyKey("smart-account", u) {
		t.Fatal("different purposes share a key; a retry of one would replay the other")
	}
	if idempotencyKey("owner", u) == idempotencyKey("owner", uuid.New().String()) {
		t.Fatal("different subjects share a key")
	}
	if _, err := uuid.Parse(idempotencyKey("owner", u)); err != nil {
		t.Fatalf("key is not a UUID: %v", err)
	}
}

func validConfig() Config {
	return Config{
		APIKeyID: "key-id", APIKeySecret: "secret", WalletSecret: "wallet",
		PaymasterURL: "https://paymaster.example/rpc", BaseURL: "https://api.cdp.coinbase.com/platform",
	}
}

// Every refusal names what is missing. An operator who set two of three
// secrets has to be told which one, not that "cdp failed".
func TestNewRefusesIncompleteConfigurationLoudly(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		chain  int64
		want   string
	}{
		{"missing api key", func(c *Config) { c.APIKeyID = "" }, 8453, "CDP_API_KEY_ID"},
		{"missing wallet secret", func(c *Config) { c.WalletSecret = "" }, 8453, "CDP_WALLET_SECRET"},
		{"missing paymaster", func(c *Config) { c.PaymasterURL = "" }, 8453, "not gasless"},
		{"unsupported chain", func(*Config) {}, 1, "not a network"},
		{"bad base url", func(c *Config) { c.BaseURL = "not a url" }, 8453, "not a URL"},
	}
	for _, tc := range cases {
		cfg := validConfig()
		tc.mutate(&cfg)
		_, err := New(cfg, tc.chain)
		if err == nil {
			t.Fatalf("%s: New accepted a configuration it must refuse", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not name the problem (%q)", tc.name, err, tc.want)
		}
	}
}

// The constructor must not dial: a bad configuration is reported at boot,
// and a good one is usable before the first request.
func TestNewAcceptsACompleteConfigurationWithoutDialling(t *testing.T) {
	c, err := New(validConfig(), 8453) // Base mainnet, the deployment target
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.host != "api.cdp.coinbase.com" || c.prefix != "/platform" {
		t.Fatalf("host/prefix = %q/%q; the wallet JWT path would not match what is sent", c.host, c.prefix)
	}
	if string(c.network) != "base" {
		t.Fatalf("network = %q", c.network)
	}
}

// CDP's error body carries a correlation id support can trace. Losing it
// turns a diagnosable failure into "it returned 500".
func TestAPIErrorsKeepWhatSupportNeeds(t *testing.T) {
	err := apiError(402, []byte(`{"errorType":"payment_method_required",
		"errorMessage":"add a payment method","correlationId":"41deb8d59a9dc9a7-IAD"}`))
	var api *APIError
	if !asAPIError(err, &api) {
		t.Fatalf("got %T, want *APIError", err)
	}
	if api.Status != 402 || api.Type != "payment_method_required" || api.CorrelationID != "41deb8d59a9dc9a7-IAD" {
		t.Fatalf("decoded %+v", api)
	}
	if !strings.Contains(err.Error(), "41deb8d59a9dc9a7-IAD") {
		t.Fatalf("error string drops the correlation id: %q", err)
	}

	// Garbage still yields a typed error with the status, never a panic.
	if !asAPIError(apiError(502, []byte("<html>bad gateway</html>")), &api) || api.Status != 502 {
		t.Fatal("non-JSON body did not produce a typed error")
	}
}

func asAPIError(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}

// Against the real service. Skipped unless credentials are in the
// environment; there is no mock of CDP because a mock only ever proves that
// the code agrees with itself.
//
// The chain comes from BASE_CHAIN_ID -- the same variable the running
// service uses -- not from a constant here. A test pinned to a testnet while
// the deployment is on mainnet exercises a network nobody is deploying to.
// On mainnet this creates a REAL, empty smart account in the CDP project for
// a throwaway user; it moves no funds.
func TestEnsureSmartAccountIsIdempotentAgainstCDP(t *testing.T) {
	cfg := Config{
		APIKeyID:     os.Getenv("CDP_API_KEY_ID"),
		APIKeySecret: os.Getenv("CDP_API_KEY_SECRET"),
		WalletSecret: os.Getenv("CDP_WALLET_SECRET"),
		PaymasterURL: os.Getenv("CDP_PAYMASTER_URL"),
		BaseURL:      os.Getenv("CDP_BASE_URL"),
	}
	if cfg.APIKeyID == "" || cfg.APIKeySecret == "" || cfg.WalletSecret == "" || cfg.PaymasterURL == "" {
		t.Skip("needs CDP_API_KEY_ID, CDP_API_KEY_SECRET, CDP_WALLET_SECRET, CDP_PAYMASTER_URL")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.cdp.coinbase.com/platform"
	}
	chainID, err := strconv.ParseInt(os.Getenv("BASE_CHAIN_ID"), 10, 64)
	if err != nil {
		t.Skip("needs BASE_CHAIN_ID (8453 for Base mainnet); refusing to guess a chain")
	}
	c, err := New(cfg, chainID)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	user := uuid.New()
	first, err := c.EnsureSmartAccount(context.Background(), user)
	if err != nil {
		t.Fatalf("EnsureSmartAccount: %v", err)
	}
	if !regexp.MustCompile(`^0x[0-9a-f]{40}$`).MatchString(first.Address) || first.Owner == "" || first.Name == "" {
		t.Fatalf("incomplete smart account: %+v", first)
	}

	again, err := c.EnsureSmartAccount(context.Background(), user)
	if err != nil {
		t.Fatalf("second EnsureSmartAccount: %v", err)
	}
	if again.Address != first.Address {
		t.Fatalf("second call created a different account: %s then %s", first.Address, again.Address)
	}
	t.Logf("smart account %s owned by %s (%s)", first.Address, first.Owner, first.Name)
}

// The wallet JWT is signed over a map built by hand; the request body is
// marshalled from the SDK's typed struct. CDP hashes the canonicalised body
// and compares it to the hash in the token, so if the two ever describe
// different JSON -- a renamed key, a field one side omits -- every sponsored
// operation is refused as a forged signature. This pins them together.
func TestWalletJWTBodyMatchesWhatTheClientSends(t *testing.T) {
	canonical := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var generic any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatal(err)
		}
		// Re-marshal through a generic value: Go sorts map keys, so two
		// documents with the same fields and values become byte-identical.
		out, err := json.Marshal(generic)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	// prepare-and-send: the sweep.
	usdc := common.HexToAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913")
	to := common.HexToAddress("0x00000000000000000000000000000000000000A1")
	data, err := base.PackTransfer(to, big.NewInt(100000))
	if err != nil {
		t.Fatal(err)
	}
	paymaster := "https://paymaster.example/rpc"
	signed := map[string]any{
		"calls":        []any{map[string]any{"to": usdc.Hex(), "value": "0", "data": data}},
		"network":      "base",
		"paymasterUrl": paymaster,
	}
	sent := openapi.PrepareAndSendUserOperationJSONRequestBody{
		Calls:        []openapi.EvmCall{{To: usdc.Hex(), Value: "0", Data: data}},
		Network:      openapi.EvmUserOperationNetwork("base"),
		PaymasterUrl: &paymaster,
	}
	if got, want := canonical(sent), canonical(signed); got != want {
		t.Fatalf("sweep body drifts from what is signed:\n sent:   %s\n signed: %s", got, want)
	}

	// create owner account.
	name := "tapp-owner-0123456789abcdef0123"
	signedOwner := map[string]any{"name": name}
	sentOwner := openapi.CreateEvmAccountJSONRequestBody{Name: &name}
	if got, want := canonical(sentOwner), canonical(signedOwner); got != want {
		t.Fatalf("owner body drifts from what is signed:\n sent:   %s\n signed: %s", got, want)
	}
}

// A sweep of $1.50 sat unswept because the address in the request path was
// lower case. CDP holds the same account as
// 0xB779226EE0F345b42681B981337205C918AF8c3c and answers a lower-case path
// with 404 "EVM smart account with the given address not found" -- which
// looks like an account that does not exist, not a spelling difference.
//
// Our own columns are lower case by design, so this conversion is the only
// thing standing between the two conventions.
func TestAddressesHandedToCDPAreChecksummed(t *testing.T) {
	const (
		stored = "0xb779226ee0f345b42681b981337205c918af8c3c"
		atCDP  = "0xB779226EE0F345b42681B981337205C918AF8c3c"
	)
	if got := checksummed(stored); got != atCDP {
		t.Fatalf("stored address not rendered as CDP holds it:\n got:  %s\n want: %s", got, atCDP)
	}
	// Already-checksummed input must survive untouched, so a caller that
	// happens to hold the mixed-case form is not corrupted by normalising.
	if got := checksummed(atCDP); got != atCDP {
		t.Fatalf("checksummed address changed: %s", got)
	}
}
