-- Settlement orders, one per tap.
--
-- A tap is settled by selling the cardholder's own USDC to the settlement
-- gateway from their own smart account, and having a liquidity provider pay
-- the merchant's bank. That is an on-chain operation which cannot happen
-- inside the tap: the cardholder is standing at a till and a chain
-- confirmation is not something to make them wait for.
--
-- So the tap authorises against the ledger and finishes, and this records
-- whether its order has since been created. One row per tap, enforced by the
-- primary key -- one payment can only ever open one order, and a retry after a
-- lost response must find the row rather than create a second sale of the same
-- money.

BEGIN;

CREATE TABLE card_tap_settlements (
    tap_id      uuid PRIMARY KEY REFERENCES card_taps(id) ON DELETE CASCADE,

    -- The account the USDC is sold from: the cardholder's own smart account,
    -- recorded as it was at the time. An address they are later reissued away
    -- from must not change what an old order says it spent.
    from_address text NOT NULL,

    -- The token amount sold, in USDC subunits.
    sell_micro   bigint NOT NULL CHECK (sell_micro > 0),

    state        text NOT NULL DEFAULT 'pending'
                 CHECK (state IN ('pending', 'submitted', 'failed')),

    -- The transaction that carried approve + createOrder. Null until it lands.
    tx_hash      text,
    last_error   text,
    attempts     int  NOT NULL DEFAULT 0,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE card_tap_settlements IS
    'One settlement order per card tap: the cardholder''s USDC sold to the gateway, paying the merchant in fiat.';

-- What the worker looks for. Partial, because settled taps are the majority
-- and scanning them to find the few outstanding ones gets slower every day.
CREATE INDEX card_tap_settlements_outstanding
    ON card_tap_settlements (created_at)
 WHERE state <> 'submitted';

COMMIT;
