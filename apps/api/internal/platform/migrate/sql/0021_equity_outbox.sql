-- What still has to be told to the equity market.
--
-- A tap is charged in one transaction and must never wait on Freedom: the
-- cardholder is at a till. So the tap's transaction writes a row here instead
-- of making a call, and a worker delivers it afterwards with backoff. A tap
-- without its row cannot exist (same transaction), and a row without its tap
-- cannot exist either, so nothing has to reconcile the two.
--
-- Freedom is idempotent on tap_ref, so a delivery that timed out after being
-- applied is simply repeated and answered with the same record.

BEGIN;

CREATE TABLE equity_outbox (
    -- Serial, so delivery order is insertion order: a tap is always
    -- delivered before its reversal.
    id           bigserial PRIMARY KEY,

    kind         text NOT NULL CHECK (kind IN ('tap', 'reverse')),
    -- The tap this is about. Not a foreign key: this is a queue, and the
    -- tap it names is a financial record that must not be locked or held
    -- up by it.
    tap_id       uuid NOT NULL,

    -- The rail request body, exactly as it will be sent.
    payload      jsonb NOT NULL,

    -- pending: not yet acknowledged by Freedom. delivered: acknowledged;
    -- response holds what it said. failed: given up after repeated
    -- refusals; last_error says why and an operator has to look.
    state        text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivered', 'failed')),
    attempts     int  NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error   text,
    -- When the next attempt may be made. Pushed out exponentially on failure.
    next_at      timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    -- Freedom's answer, verbatim. For a tap this carries the allocation
    -- (symbol, units, price), which is what the read model shows a merchant.
    response     jsonb,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- One charge and at most one reversal per tap.
CREATE UNIQUE INDEX equity_outbox_tap_kind ON equity_outbox (tap_id, kind);

-- The worker's query: what is due, oldest first.
CREATE INDEX equity_outbox_due ON equity_outbox (next_at, id) WHERE state = 'pending';

COMMIT;
