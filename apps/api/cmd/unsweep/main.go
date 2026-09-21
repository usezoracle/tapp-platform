// unsweep returns USDC the treasury swept back to the deposit address it came
// from.
//
// Card payments are settled non-custodially now: a tap sells the cardholder's
// own USDC to the settlement gateway from their own smart account. Anything
// the sweeper pooled into the treasury before that change is money its owner
// can see in their balance and cannot spend, because the account it would be
// sold from is empty.
//
// Deliberately explicit. It takes the address and the amount as arguments
// rather than working them out, because the amounts are few, they are known,
// and a command that decides for itself which customer funds to move is not
// one to write for a handful of rows.
//
//	go run ./cmd/unsweep -to 0x… -usdc 1.50          # report
//	go run ./cmd/unsweep -to 0x… -usdc 1.50 -apply   # send
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/shopspring/decimal"
	"github.com/spf13/viper"

	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/internal/chain/base"
)

func main() {
	to := flag.String("to", "", "deposit address to return the USDC to")
	amount := flag.String("usdc", "", "amount in whole USDC, e.g. 1.50")
	apply := flag.Bool("apply", false, "actually send; without it nothing moves")
	flag.Parse()

	if err := run(context.Background(), *to, *amount, *apply); err != nil {
		fmt.Fprintln(os.Stderr, "unsweep:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, to, amount string, apply bool) error {
	if to == "" || amount == "" {
		return fmt.Errorf("both -to and -usdc are required")
	}
	if !common.IsHexAddress(to) {
		return fmt.Errorf("%q is not an address", to)
	}
	dst := common.HexToAddress(to)

	whole, err := decimal.NewFromString(amount)
	if err != nil || whole.Sign() <= 0 {
		return fmt.Errorf("%q is not an amount of USDC", amount)
	}
	micro := whole.Mul(decimal.New(1, 6)).Round(0).BigInt()

	conf := config.OrderConfig()
	rpc := viper.GetString("BASE_RPC_URL")
	usdc := viper.GetString("BASE_USDC_CONTRACT")
	key := viper.GetString("BASE_TREASURY_KEY")
	if rpc == "" || usdc == "" || key == "" {
		return fmt.Errorf("BASE_RPC_URL, BASE_USDC_CONTRACT and BASE_TREASURY_KEY must all be set")
	}

	chain, err := base.NewChain(ctx, rpc, usdc, key, conf.BaseChainID)
	if err != nil {
		return err
	}
	if !chain.CanSend() {
		return fmt.Errorf("no treasury key, so nothing can be sent")
	}

	// Read both balances before deciding anything. Sending more than the
	// treasury holds fails on chain and costs the gas anyway, and a caller
	// who mistyped an amount should be told before that happens.
	held, err := chain.USDCBalance(ctx, chain.Treasury)
	if err != nil {
		return fmt.Errorf("read treasury balance: %w", err)
	}
	before, err := chain.USDCBalance(ctx, dst)
	if err != nil {
		return fmt.Errorf("read destination balance: %w", err)
	}

	fmt.Printf("treasury %s holds %s USDC\n", chain.Treasury, format(held))
	fmt.Printf("%s holds %s USDC\n", dst, format(before))
	fmt.Printf("returning %s USDC\n\n", format(micro))

	if held.Cmp(micro) < 0 {
		return fmt.Errorf("treasury holds %s USDC, which is less than the %s asked for",
			format(held), format(micro))
	}

	if !apply {
		fmt.Println("dry run — nothing moved. re-run with -apply to send")
		return nil
	}

	txHash, err := chain.SendUSDC(ctx, chain.TreasuryKey(), dst, micro)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	fmt.Printf("sent: %s\n", txHash)

	// Wait for it, so the operator learns here whether it worked rather than
	// from a block explorer.
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	ok, err := chain.WaitMined(waitCtx, txHash)
	if err != nil {
		return fmt.Errorf("the transaction was sent but could not be confirmed here: %w", err)
	}
	if !ok {
		return fmt.Errorf("transaction %s reverted", txHash)
	}

	// Read the destination back until it reflects the transfer.
	//
	// A receipt means the block exists, not that the node answering the next
	// call has applied it -- public RPCs are a pool, and the one that serves
	// this read may be a block behind. Reporting 0.00 for a transfer that
	// succeeded is worse than saying nothing: an operator who believes it
	// failed re-runs it, and sends the money twice.
	after := before
	for i := 0; i < 10; i++ {
		after, err = chain.USDCBalance(ctx, dst)
		if err != nil {
			return err
		}
		if after.Cmp(before) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if after.Cmp(before) <= 0 {
		// The receipt said success, so the money moved; this node just has
		// not caught up. Say exactly that rather than implying a failure.
		fmt.Printf("sent and mined in %s, but this RPC still reports %s USDC at %s.\n"+
			"The transfer succeeded -- do NOT re-run it. Check a block explorer.\n",
			txHash, format(after), dst)
		return nil
	}
	fmt.Printf("confirmed. %s now holds %s USDC\n", dst, format(after))
	return nil
}

func format(micro *big.Int) string {
	return decimal.NewFromBigInt(micro, 0).Div(decimal.New(1, 6)).StringFixed(2)
}
