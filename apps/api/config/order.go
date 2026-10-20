package config

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"github.com/spf13/viper"
)

// OrderConfiguration type defines payment order configurations.
type OrderConfiguration struct {
	// CardFeeBPS is the platform's cut of a card payment, in basis points of
	// the amount. Taken out of what the merchant receives, never added on top,
	// so one definition of "amount" holds across cards, transfers and cash.
	//
	// Its predecessor was cardCollectionBufferBPS, a constant of 100 applied
	// inside the debit handler that bundled a 50bp fee with 50bp of FX drift
	// headroom. Drift is the rate engine's problem and is priced into a quote;
	// this is only the fee.
	CardFeeBPS int

	// Generic, chain-agnostic.
	OrderFulfillmentValidity         time.Duration
	ReceiveAddressValidity           time.Duration
	OrderRequestValidity             time.Duration
	BucketQueueRebuildInterval       int // in hours
	RefundCancellationCount          int
	PercentDeviationFromExternalRate decimal.Decimal
	PercentDeviationFromMarketRate   decimal.Decimal

	// Base — the chain this platform settles on. Same env block works for
	// Base mainnet (8453) and Base Sepolia (84532); flip BASE_CHAIN_ID
	// + BASE_GATEWAY_CONTRACT + BASE_USDC_CONTRACT + BASE_RPC_URL to
	// switch networks. USDC on Base is 6-decimal native Circle.
	BaseRpcURL                string
	BaseAggregatorAddress     string // our Base hot wallet; receives bridged USDC
	BaseGatewayContract       string // settlement Gateway proxy on the active network
	BaseUSDCContract          string // Circle USDC ERC-20 on the active network
	BaseChainID               int64  // 8453 mainnet, 84532 Sepolia
	BaseSignerKey             string // hex private key for the aggregator wallet (signs approve + createOrder)
	BaseSenderFeeBPS          int64  // sender fee skim charged on each order, in basis points (50 = 0.5%)
	BaseNativeLowThresholdWei string // big.Int as string; below this we Slack-alert ops (native = ETH on Base)
	BaseUSDCDecimals          int    // 6 on Base for Circle native USDC
}

// OrderConfig sets the order configuration
func OrderConfig() *OrderConfiguration {
	viper.SetDefault("RECEIVE_ADDRESS_VALIDITY", 30)
	viper.SetDefault("ORDER_REQUEST_VALIDITY", 120)
	viper.SetDefault("ORDER_FULFILLMENT_VALIDITY", 10)
	viper.SetDefault("BUCKET_QUEUE_REBUILD_INTERVAL", 1)
	viper.SetDefault("REFUND_CANCELLATION_COUNT", 3)
	viper.SetDefault("CARD_FEE_BPS", 50) // 0.5% of a card payment
	viper.SetDefault("NETWORK_FEE", 0.05)
	viper.SetDefault("PERCENT_DEVIATION_FROM_EXTERNAL_RATE", 0.01)
	viper.SetDefault("PERCENT_DEVIATION_FROM_MARKET_RATE", 0.1)
	viper.SetDefault("BASE_RPC_URL", "https://sepolia.base.org")           // Sepolia default; mainnet = https://mainnet.base.org
	viper.SetDefault("BASE_CHAIN_ID", 84532)                               // Base Sepolia; mainnet = 8453
	viper.SetDefault("BASE_SENDER_FEE_BPS", 50)                            // 0.5% sender fee
	viper.SetDefault("BASE_NATIVE_LOW_THRESHOLD_WEI", "10000000000000000") // 0.01 ETH (Base L2 gas is cheap)
	viper.SetDefault("BASE_USDC_DECIMALS", 6)

	return &OrderConfiguration{
		CardFeeBPS:                       viper.GetInt("CARD_FEE_BPS"),
		OrderFulfillmentValidity:         time.Duration(viper.GetInt("ORDER_FULFILLMENT_VALIDITY")) * time.Minute,
		ReceiveAddressValidity:           time.Duration(viper.GetInt("RECEIVE_ADDRESS_VALIDITY")) * time.Minute,
		OrderRequestValidity:             time.Duration(viper.GetInt("ORDER_REQUEST_VALIDITY")) * time.Second,
		BucketQueueRebuildInterval:       viper.GetInt("BUCKET_QUEUE_REBUILD_INTERVAL"),
		RefundCancellationCount:          viper.GetInt("REFUND_CANCELLATION_COUNT"),
		PercentDeviationFromExternalRate: decimal.NewFromFloat(viper.GetFloat64("PERCENT_DEVIATION_FROM_EXTERNAL_RATE")),
		PercentDeviationFromMarketRate:   decimal.NewFromFloat(viper.GetFloat64("PERCENT_DEVIATION_FROM_MARKET_RATE")),
		BaseRpcURL:                       viper.GetString("BASE_RPC_URL"),
		BaseAggregatorAddress:            viper.GetString("BASE_AGGREGATOR_ADDRESS"),
		BaseGatewayContract:              viper.GetString("BASE_GATEWAY_CONTRACT"),
		BaseUSDCContract:                 viper.GetString("BASE_USDC_CONTRACT"),
		BaseChainID:                      viper.GetInt64("BASE_CHAIN_ID"),
		BaseSignerKey:                    viper.GetString("BASE_SIGNER_KEY"),
		BaseSenderFeeBPS:                 viper.GetInt64("BASE_SENDER_FEE_BPS"),
		BaseNativeLowThresholdWei:        viper.GetString("BASE_NATIVE_LOW_THRESHOLD_WEI"),
		BaseUSDCDecimals:                 viper.GetInt("BASE_USDC_DECIMALS"),
	}
}

func init() {
	if err := SetupConfig(); err != nil {
		panic(fmt.Sprintf("config SetupConfig() error: %s", err))
	}
}
