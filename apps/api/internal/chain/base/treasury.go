package base

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// erc20ABI is the fragment needed to read a balance and make a transfer.
// Declared here rather than pulled from a binding package: three methods do
// not justify a code generator, and an inline fragment is auditable at a
// glance.
const erc20ABI = `[
 {"name":"balanceOf","type":"function","stateMutability":"view",
  "inputs":[{"name":"account","type":"address"}],"outputs":[{"name":"","type":"uint256"}]},
 {"name":"transfer","type":"function","stateMutability":"nonpayable",
  "inputs":[{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],
  "outputs":[{"name":"","type":"bool"}]},
 {"name":"decimals","type":"function","stateMutability":"view",
  "inputs":[],"outputs":[{"name":"","type":"uint8"}]},
 {"name":"allowance","type":"function","stateMutability":"view",
  "inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],
  "outputs":[{"name":"","type":"uint256"}]},
 {"name":"nonces","type":"function","stateMutability":"view",
  "inputs":[{"name":"owner","type":"address"}],"outputs":[{"name":"","type":"uint256"}]},
 {"name":"DOMAIN_SEPARATOR","type":"function","stateMutability":"view",
  "inputs":[],"outputs":[{"name":"","type":"bytes32"}]},
 {"name":"permit","type":"function","stateMutability":"nonpayable",
  "inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"},
            {"name":"value","type":"uint256"},{"name":"deadline","type":"uint256"},
            {"name":"v","type":"uint8"},{"name":"r","type":"bytes32"},{"name":"s","type":"bytes32"}],
  "outputs":[]},
 {"name":"approve","type":"function","stateMutability":"nonpayable",
  "inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],
  "outputs":[{"name":"","type":"bool"}]},
 {"name":"transferFrom","type":"function","stateMutability":"nonpayable",
  "inputs":[{"name":"from","type":"address"},{"name":"to","type":"address"},
            {"name":"amount","type":"uint256"}],
  "outputs":[{"name":"","type":"bool"}]}
]`

var parsedERC20 = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		panic("base: erc20 abi: " + err.Error())
	}
	return a
}()

// Chain talks to Base.
type Chain struct {
	Client  *ethclient.Client
	USDC    common.Address
	ChainID *big.Int

	// Treasury is where deposits are swept to and withdrawals are paid from.
	Treasury common.Address
	// treasuryKey signs withdrawals. Held in memory only, never persisted.
	treasuryKey *ecdsa.PrivateKey

	// Serialise submissions per signer, so reading a nonce and sending with it
	// cannot interleave. Two goroutines that both read nonce N produce one
	// transaction the network drops.
	mu sync.Mutex
}

// NewChain dials Base and prepares the treasury signer.
func NewChain(ctx context.Context, rpcURL, usdc, treasuryKeyHex string, chainID int64) (*Chain, error) {
	if rpcURL == "" {
		return nil, fmt.Errorf("base: no RPC URL")
	}
	if !common.IsHexAddress(usdc) {
		return nil, fmt.Errorf("base: %q is not a USDC address", usdc)
	}

	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("base: dial: %w", err)
	}

	c := &Chain{
		Client: client, USDC: common.HexToAddress(usdc),
		ChainID: big.NewInt(chainID),
	}

	if treasuryKeyHex != "" {
		key, err := ethcrypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(treasuryKeyHex), "0x"))
		if err != nil {
			return nil, fmt.Errorf("base: treasury key is not a valid private key: %w", err)
		}
		c.treasuryKey = key
		c.Treasury = ethcrypto.PubkeyToAddress(key.PublicKey)
	}
	return c, nil
}

// CanSend reports whether withdrawals are possible.
//
// A chain with no treasury key can still watch and credit deposits, which is
// the common configuration for a read-only replica. It simply cannot move
// money, and says so rather than failing at the moment somebody withdraws.
func (c *Chain) CanSend() bool { return c.treasuryKey != nil }

// USDCBalance reads an address's USDC balance in micro-units.
func (c *Chain) USDCBalance(ctx context.Context, addr common.Address) (*big.Int, error) {
	data, err := parsedERC20.Pack("balanceOf", addr)
	if err != nil {
		return nil, err
	}
	out, err := c.Client.CallContract(ctx, ethereumCall(c.USDC, data), nil)
	if err != nil {
		return nil, fmt.Errorf("base: read balance of %s: %w", addr, err)
	}
	values, err := parsedERC20.Unpack("balanceOf", out)
	if err != nil || len(values) == 0 {
		return nil, fmt.Errorf("base: unreadable balance for %s", addr)
	}
	balance, ok := values[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("base: balance of %s is not a number", addr)
	}
	return balance, nil
}

// SendUSDC transfers USDC from an address whose key is supplied.
//
// Used for both sweeping a deposit address into treasury and paying a
// withdrawal out of it. The nonce is read and the transaction sent under one
// lock: two goroutines that both read nonce N produce one transaction the
// network drops, and which one survives is arbitrary.
func (c *Chain) SendUSDC(
	ctx context.Context, from *ecdsa.PrivateKey, to common.Address, amountMicro *big.Int,
) (string, error) {
	if amountMicro == nil || amountMicro.Sign() <= 0 {
		return "", fmt.Errorf("base: nothing to send")
	}

	data, err := parsedERC20.Pack("transfer", to, amountMicro)
	if err != nil {
		return "", err
	}
	return c.submitCall(ctx, from, data)
}

// submitCall signs and sends one call to the USDC contract.
//
// Shared by every path that touches the token so that nonce handling, the fee
// ceiling and the gas margin have one implementation. A second copy of this
// is a second place for a stuck-transaction bug to live.
func (c *Chain) submitCall(
	ctx context.Context, from *ecdsa.PrivateKey, data []byte,
) (string, error) {
	if from == nil {
		return "", fmt.Errorf("base: no signer for this call")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	sender := ethcrypto.PubkeyToAddress(from.PublicKey)
	nonce, err := c.Client.PendingNonceAt(ctx, sender)
	if err != nil {
		return "", fmt.Errorf("base: read nonce for %s: %w", sender, err)
	}

	tip, err := c.Client.SuggestGasTipCap(ctx)
	if err != nil {
		return "", fmt.Errorf("base: suggest tip: %w", err)
	}
	head, err := c.Client.HeaderByNumber(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("base: read head: %w", err)
	}
	// Room for the base fee to double before the transaction is mined, which
	// it can across a few blocks. Too tight a cap means a transfer that sits
	// unmined and looks like a stuck payout.
	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))

	gas, err := c.Client.EstimateGas(ctx, ethereumCallFrom(sender, c.USDC, data))
	if err != nil {
		return "", fmt.Errorf("base: estimate gas: %w", err)
	}

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: c.ChainID, Nonce: nonce, To: &c.USDC,
		Gas: gas + gas/5, GasTipCap: tip, GasFeeCap: feeCap, Data: data,
	})

	signed, err := types.SignTx(tx, types.LatestSignerForChainID(c.ChainID), from)
	if err != nil {
		return "", fmt.Errorf("base: sign: %w", err)
	}
	if err := c.Client.SendTransaction(ctx, signed); err != nil {
		return "", fmt.Errorf("base: submit: %w", err)
	}
	return signed.Hash().Hex(), nil
}

// PackTransfer is ERC-20 transfer(to, amount) as hex calldata, for callers
// that submit through something other than this package's own signer -- a
// user operation, say -- and must describe the call rather than make it.
func PackTransfer(to common.Address, amount *big.Int) (string, error) {
	data, err := parsedERC20.Pack("transfer", to, amount)
	if err != nil {
		return "", fmt.Errorf("base: pack transfer: %w", err)
	}
	return "0x" + common.Bytes2Hex(data), nil
}

// PackApprove builds approve(spender, amount) calldata.
//
// Needed because the offramp Gateway pulls the tokens itself rather than
// being sent them: the allowance and the call that consumes it travel
// together in one sponsored operation, so no allowance is ever left standing.
func PackApprove(spender common.Address, amount *big.Int) (string, error) {
	data, err := parsedERC20.Pack("approve", spender, amount)
	if err != nil {
		return "", fmt.Errorf("base: pack approve: %w", err)
	}
	return "0x" + common.Bytes2Hex(data), nil
}

// TreasuryKey returns the signer for withdrawals.
func (c *Chain) TreasuryKey() *ecdsa.PrivateKey { return c.treasuryKey }

// WaitMined blocks until a transaction is mined or the context ends.
func (c *Chain) WaitMined(ctx context.Context, txHash string) (bool, error) {
	hash := common.HexToHash(txHash)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		receipt, err := c.Client.TransactionReceipt(ctx, hash)
		if err == nil {
			return receipt.Status == types.ReceiptStatusSuccessful, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

// ethereumCall builds a read-only call.
func ethereumCall(to common.Address, data []byte) ethereum.CallMsg {
	return ethereum.CallMsg{To: &to, Data: data}
}

func ethereumCallFrom(from, to common.Address, data []byte) ethereum.CallMsg {
	return ethereum.CallMsg{From: from, To: &to, Data: data}
}
