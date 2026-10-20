// Package yield puts cardholders' idle USDC into an ERC-4626 vault, takes it
// back out when it is spent, and credits what the vault earned to their
// ledger balance. See docs/yield.md.
//
// Everything happens from the cardholder's own smart account: the account
// deposits, the account owns the shares, the account withdraws. Nothing is
// pooled, and the platform never holds a share.
package yield

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/usezoracle/tapp/api/internal/chain/cdp"
)

const vaultABI = `[
 {"name":"deposit","type":"function","stateMutability":"nonpayable",
  "inputs":[{"name":"assets","type":"uint256"},{"name":"receiver","type":"address"}],
  "outputs":[{"name":"shares","type":"uint256"}]},
 {"name":"withdraw","type":"function","stateMutability":"nonpayable",
  "inputs":[{"name":"assets","type":"uint256"},{"name":"receiver","type":"address"},
            {"name":"owner","type":"address"}],
  "outputs":[{"name":"shares","type":"uint256"}]},
 {"name":"balanceOf","type":"function","stateMutability":"view",
  "inputs":[{"name":"account","type":"address"}],
  "outputs":[{"name":"","type":"uint256"}]},
 {"name":"convertToAssets","type":"function","stateMutability":"view",
  "inputs":[{"name":"shares","type":"uint256"}],
  "outputs":[{"name":"","type":"uint256"}]},
 {"name":"maxWithdraw","type":"function","stateMutability":"view",
  "inputs":[{"name":"owner","type":"address"}],
  "outputs":[{"name":"","type":"uint256"}]},
 {"name":"approve","type":"function","stateMutability":"nonpayable",
  "inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],
  "outputs":[{"name":"","type":"bool"}]}
]`

var parsed = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(vaultABI))
	if err != nil {
		panic("yield: vault abi: " + err.Error())
	}
	return a
}()

// Vault is one ERC-4626 USDC vault on Base. USDC's balanceOf has the same
// shape as the vault's, so one ABI reads both.
type Vault struct {
	Client  ethereum.ContractCaller
	Address common.Address
	USDC    common.Address
}

// Value is what an account's shares are worth now, in USDC micro-units.
func (v *Vault) Value(ctx context.Context, account common.Address) (int64, error) {
	shares, err := v.read(ctx, v.Address, "balanceOf", account)
	if err != nil {
		return 0, err
	}
	if shares.Sign() == 0 {
		return 0, nil
	}
	assets, err := v.read(ctx, v.Address, "convertToAssets", shares)
	if err != nil {
		return 0, err
	}
	return micro(assets)
}

// Withdrawable is what the vault would release to an account right now. It
// can be less than Value while the vault's idle USDC is lent out.
func (v *Vault) Withdrawable(ctx context.Context, account common.Address) (int64, error) {
	max, err := v.read(ctx, v.Address, "maxWithdraw", account)
	if err != nil {
		return 0, err
	}
	return micro(max)
}

// Wallet is the USDC an account holds outside the vault.
func (v *Vault) Wallet(ctx context.Context, account common.Address) (int64, error) {
	balance, err := v.read(ctx, v.USDC, "balanceOf", account)
	if err != nil {
		return 0, err
	}
	return micro(balance)
}

// DepositCalls puts amount of an account's USDC into the vault, the shares
// to the account. Approve and deposit go in one operation: an approval that
// lands without its deposit leaves an allowance standing on the vault.
func (v *Vault) DepositCalls(account common.Address, amount int64) ([]cdp.Call, error) {
	approve, err := parsed.Pack("approve", v.Address, big.NewInt(amount))
	if err != nil {
		return nil, err
	}
	deposit, err := parsed.Pack("deposit", big.NewInt(amount), account)
	if err != nil {
		return nil, err
	}
	return []cdp.Call{
		{To: v.USDC, Data: hexData(approve)},
		{To: v.Address, Data: hexData(deposit)},
	}, nil
}

// WithdrawCalls takes amount of USDC out of the vault, from the account's
// shares, back to the account.
func (v *Vault) WithdrawCalls(account common.Address, amount int64) ([]cdp.Call, error) {
	withdraw, err := parsed.Pack("withdraw", big.NewInt(amount), account, account)
	if err != nil {
		return nil, err
	}
	return []cdp.Call{{To: v.Address, Data: hexData(withdraw)}}, nil
}

func (v *Vault) read(ctx context.Context, to common.Address, method string, args ...any) (*big.Int, error) {
	data, err := parsed.Pack(method, args...)
	if err != nil {
		return nil, err
	}
	out, err := v.Client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("yield: %s on %s: %w", method, to.Hex(), err)
	}
	values, err := parsed.Unpack(method, out)
	if err != nil || len(values) == 0 {
		return nil, fmt.Errorf("yield: unreadable %s from %s", method, to.Hex())
	}
	n, ok := values[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("yield: %s from %s is not a number", method, to.Hex())
	}
	return n, nil
}

func micro(n *big.Int) (int64, error) {
	if !n.IsInt64() {
		return 0, fmt.Errorf("yield: %s micro-USDC does not fit", n)
	}
	return n.Int64(), nil
}

func hexData(b []byte) string { return "0x" + common.Bytes2Hex(b) }
