-- A settlement order has an outcome, and the books have to follow it.
--
-- Until now 'submitted' was the end of the road: the order was on chain, the
-- merchant's claim was discharged, and nothing ever looked again. A liquidity
-- provider can decline to fill an order, and the Gateway then refunds the
-- cardholder's USDC. The merchant has NOT been paid, but the ledger said they
-- had. This adds the two outcomes an order actually has and the identifier
-- needed to ask the Gateway which one it got.

BEGIN;

ALTER TABLE card_tap_settlements
    DROP CONSTRAINT card_tap_settlements_state_check;

ALTER TABLE card_tap_settlements
    ADD CONSTRAINT card_tap_settlements_state_check
    CHECK (state IN ('pending', 'submitted', 'fulfilled', 'failed'));

ALTER TABLE card_tap_settlements
    -- The Gateway's own id for the order, read from the OrderCreated event of
    -- tx_hash. Null until the tracker has read it. This is what getOrderInfo
    -- is keyed on; the transaction hash alone cannot ask the question.
    ADD COLUMN order_id text,

    -- How many orders have been created for this tap. Zero is the first. A
    -- refunded order is retried as a new round, with a new idempotency
    -- reference, because the provider's idempotency would otherwise hand
    -- back the operation that was refunded rather than make a new one.
    ADD COLUMN round int NOT NULL DEFAULT 0 CHECK (round >= 0);

-- Outstanding now means either not yet on chain or on chain without an
-- outcome. Fulfilled and failed are the resting states.
DROP INDEX card_tap_settlements_outstanding;
CREATE INDEX card_tap_settlements_outstanding
    ON card_tap_settlements (created_at)
 WHERE state IN ('pending', 'submitted');

COMMENT ON COLUMN card_tap_settlements.state IS
    'pending: not yet sold. submitted: on chain, outcome unknown. fulfilled: the provider paid the merchant. failed: given up; the merchant is still owed and the claim stands in merchant_payable.';

COMMIT;
