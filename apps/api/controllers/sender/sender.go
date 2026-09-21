package sender

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/usezoracle/tapp/api/config"
	"github.com/usezoracle/tapp/api/ent"
	"github.com/usezoracle/tapp/api/storage"

	"github.com/usezoracle/tapp/api/ent/paymentorder"
	"github.com/usezoracle/tapp/api/ent/senderprofile"
	svc "github.com/usezoracle/tapp/api/services"

	u "github.com/usezoracle/tapp/api/utils"
	"github.com/usezoracle/tapp/api/utils/logger"

	"github.com/gin-gonic/gin"
)

// SenderController is a controller type for sender endpoints
type SenderController struct {
	receiveAddressService *svc.ReceiveAddressService
}

// NewSenderController creates a new instance of SenderController
func NewSenderController() *SenderController {

	return &SenderController{
		receiveAddressService: svc.NewReceiveAddressService(),
	}
}

var serverConf = config.ServerConfig()
var orderConf = config.OrderConfig()

// Order creation moved to internal/orders.
//
// Both endpoints here created a one-time Sui receive address, waited for an
// on-chain deposit to it, bridged that to Base and handed the result to an
// aggregator to find a liquidity provider. Every stage was somewhere to get
// stuck, and an order's true state lived across four tables and a bridge
// provider's API.
//
// An order is now a composition of three things that already exist: a balance
// in the ledger, a price from a quote, and a delivery by the settlement
// worker. See internal/orders.

// CancelOrder lets the sender abandon an in-flight PaymentOrder before
// the customer pays. Allowed only on orders the merchant actually owns,
// and only while the order is still in `initiated` or `pending` state —
// once settlement begins it's too late to cancel client-side.
//
// Idempotent: cancelling an already-cancelled / expired / settled order
// is a no-op 200 with the current state surfaced in the body, so the
// merchant app's tear-down path (back button, broadcast timeout) doesn't
// have to special-case 409s.
func (ctrl *SenderController) CancelOrder(ctx *gin.Context) {
	idStr := ctx.Param("id")
	orderID, err := uuid.Parse(idStr)
	if err != nil {
		u.APIResponse(ctx, http.StatusBadRequest, "error", "Invalid order id", nil)
		return
	}

	sender, ok := ctx.Get("sender")
	if !ok || sender == nil {
		u.APIResponse(ctx, http.StatusUnauthorized, "error", "Sender profile required", nil)
		return
	}
	senderProfile := sender.(*ent.SenderProfile)

	order, err := storage.Client.PaymentOrder.
		Query().
		Where(
			paymentorder.IDEQ(orderID),
			paymentorder.HasSenderProfileWith(senderprofile.IDEQ(senderProfile.ID)),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			u.APIResponse(ctx, http.StatusNotFound, "error", "Order not found", nil)
			return
		}
		logger.Errorf("CancelOrder.query: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to cancel order", nil)
		return
	}

	// Idempotent — already in a terminal state.
	switch order.Status {
	case paymentorder.StatusCancelled, paymentorder.StatusExpired,
		paymentorder.StatusSettled, paymentorder.StatusRefunded:
		u.APIResponse(ctx, http.StatusOK, "success", "Order is already final",
			gin.H{"id": order.ID, "status": order.Status})
		return
	}

	// Active orders can be cancelled.
	updated, err := order.Update().
		SetStatus(paymentorder.StatusCancelled).
		Save(ctx)
	if err != nil {
		logger.Errorf("CancelOrder.update: %v", err)
		u.APIResponse(ctx, http.StatusInternalServerError, "error", "Failed to cancel order", nil)
		return
	}

	u.APIResponse(ctx, http.StatusOK, "success", "Order cancelled",
		gin.H{"id": updated.ID, "status": updated.Status})
}
