-- Card taps settle through Switch: a payout opened per round, funded by a
-- transfer of the cardholder's USDC to a single-use deposit address.
--
-- One row per payout, written after Switch opens it and before any money
-- moves. card_tap_settlements says where a tap stands now and is cleared for
-- each new round; this keeps every round. The deposit address is what the
-- deposit watcher checks: a payout Switch cannot complete is refunded to the
-- cardholder's account, and USDC arriving from an address we paid is that
-- refund, not a deposit to credit a second time.
BEGIN;

CREATE TABLE offramp_payments (
    reference       text PRIMARY KEY,
    tap_id          uuid NOT NULL REFERENCES card_taps(id) ON DELETE CASCADE,
    round           int  NOT NULL CHECK (round >= 0),
    deposit_address text NOT NULL,
    sell_micro      bigint NOT NULL CHECK (sell_micro > 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tap_id, round)
);

CREATE INDEX offramp_payments_deposit_address ON offramp_payments (deposit_address);

COMMIT;
