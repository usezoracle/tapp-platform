package offramp

import (
	"fmt"
	"math/big"
	"reflect"
	"strings"

	ethabi "github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// gatewayABI is the part of the Gateway this package speaks.
//
// createOrder is what a tap calls. OrderCreated is how the order's id is
// learned afterwards -- createOrder returns it, but a sponsored operation
// hands back a transaction hash, not a return value, so the id has to be
// read from the event. getOrderInfo is the question asked of every submitted
// order until it answers fulfilled or refunded.
//
// The wider Gateway ABI lives in services/evm, which binds it for a wallet the
// platform signs with; here the caller is the cardholder's smart account and
// CDP does the signing, so calldata and decoding are all that is needed.
const gatewayABI = `[
  {
    "type":"function","name":"createOrder","stateMutability":"nonpayable",
    "inputs":[
      {"name":"_token","type":"address"},
      {"name":"_amount","type":"uint256"},
      {"name":"_rate","type":"uint96"},
      {"name":"_senderFeeRecipient","type":"address"},
      {"name":"_senderFee","type":"uint256"},
      {"name":"_refundAddress","type":"address"},
      {"name":"messageHash","type":"string"}
    ],
    "outputs":[{"name":"orderId","type":"bytes32"}]
  },
  {
    "type":"event","name":"OrderCreated","anonymous":false,
    "inputs":[
      {"name":"sender","type":"address","indexed":true},
      {"name":"token","type":"address","indexed":true},
      {"name":"amount","type":"uint256","indexed":true},
      {"name":"protocolFee","type":"uint256","indexed":false},
      {"name":"orderId","type":"bytes32","indexed":false},
      {"name":"rate","type":"uint256","indexed":false},
      {"name":"messageHash","type":"string","indexed":false}
    ]
  },
  {
    "type":"function","name":"getOrderInfo","stateMutability":"view",
    "inputs":[{"name":"_orderId","type":"bytes32"}],
    "outputs":[{"name":"","type":"tuple","components":[
      {"name":"sender","type":"address"},
      {"name":"token","type":"address"},
      {"name":"senderFeeRecipient","type":"address"},
      {"name":"senderFee","type":"uint256"},
      {"name":"protocolFee","type":"uint256"},
      {"name":"isFulfilled","type":"bool"},
      {"name":"isRefunded","type":"bool"},
      {"name":"refundAddress","type":"address"},
      {"name":"currentBPS","type":"uint96"},
      {"name":"amount","type":"uint256"}
    ]}]
  }
]`

var parsedGateway = func() ethabi.ABI {
	a, err := ethabi.JSON(strings.NewReader(gatewayABI))
	if err != nil {
		panic("offramp: gateway ABI: " + err.Error())
	}
	return a
}()

type createOrderArgs struct {
	Token              common.Address
	Amount             *big.Int
	Rate               *big.Int
	SenderFeeRecipient common.Address
	SenderFee          *big.Int
	RefundAddress      common.Address
	MessageHash        string
}

func packCreateOrder(a createOrderArgs) (string, error) {
	data, err := parsedGateway.Pack("createOrder",
		a.Token, a.Amount, a.Rate, a.SenderFeeRecipient, a.SenderFee,
		a.RefundAddress, a.MessageHash)
	if err != nil {
		return "", fmt.Errorf("offramp: pack createOrder: %w", err)
	}
	return "0x" + common.Bytes2Hex(data), nil
}

// orderCreatedTopic is the first topic of every OrderCreated log.
var orderCreatedTopic = parsedGateway.Events["OrderCreated"].ID

// orderCreated is the part of an OrderCreated event the tracker reads.
type orderCreated struct {
	Sender  common.Address
	Amount  *big.Int
	OrderID [32]byte
}

// unpackOrderCreated decodes one OrderCreated log, or reports that the log is
// not one. Indexed fields come from the topics, the rest from the data.
func unpackOrderCreated(l types.Log) (orderCreated, bool, error) {
	if len(l.Topics) != 4 || l.Topics[0] != orderCreatedTopic {
		return orderCreated{}, false, nil
	}
	var out orderCreated
	out.Sender = common.BytesToAddress(l.Topics[1].Bytes())
	out.Amount = new(big.Int).SetBytes(l.Topics[3].Bytes())

	var data struct {
		ProtocolFee *big.Int
		OrderId     [32]byte
		Rate        *big.Int
		MessageHash string
	}
	if err := parsedGateway.UnpackIntoInterface(&data, "OrderCreated", l.Data); err != nil {
		return orderCreated{}, false, fmt.Errorf("offramp: decode OrderCreated: %w", err)
	}
	out.OrderID = data.OrderId
	return out, true, nil
}

// OrderInfo is what the Gateway says about an order.
type OrderInfo struct {
	Fulfilled bool
	Refunded  bool
}

func packGetOrderInfo(orderID [32]byte) ([]byte, error) {
	data, err := parsedGateway.Pack("getOrderInfo", orderID)
	if err != nil {
		return nil, fmt.Errorf("offramp: pack getOrderInfo: %w", err)
	}
	return data, nil
}

func unpackOrderInfo(out []byte) (OrderInfo, error) {
	vals, err := parsedGateway.Unpack("getOrderInfo", out)
	if err != nil {
		return OrderInfo{}, fmt.Errorf("offramp: decode getOrderInfo: %w", err)
	}
	if len(vals) != 1 {
		return OrderInfo{}, fmt.Errorf("offramp: getOrderInfo returned %d values", len(vals))
	}
	// The tuple comes back as an anonymous struct; the two flags are all
	// that is asked of it, read by position rather than by binding the whole
	// shape and having to keep it in step with the contract.
	v := reflect.ValueOf(vals[0])
	if v.Kind() != reflect.Struct || v.NumField() < 7 {
		return OrderInfo{}, fmt.Errorf("offramp: getOrderInfo returned %T", vals[0])
	}
	return OrderInfo{
		Fulfilled: v.Field(5).Bool(),
		Refunded:  v.Field(6).Bool(),
	}, nil
}
