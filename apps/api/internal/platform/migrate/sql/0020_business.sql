-- A merchant's business, as registered with the equity market.
--
-- Freedom is the market: it holds the company, the listing, the instrument and
-- every share. What Tapp keeps is the merchant's own copy of what they
-- submitted and what the market decided, so the merchant app can show it
-- without a round trip and the outcome survives the market being unreachable.
-- The live cap table is never stored; it is read from Freedom on request.
--
-- One row per merchant. Freedom is idempotent on merchant_ref (the sender
-- profile id), so a second submission returns the same record and this row is
-- simply refreshed from it.

BEGIN;

CREATE TABLE merchant_businesses (
    -- The sender profile. Not a foreign key to the ent-owned table, for the
    -- same reason card_taps is not: a listing is a record that outlives the
    -- profile that made it.
    sender_id             uuid PRIMARY KEY,

    legal_name            text NOT NULL CHECK (legal_name <> ''),
    trading_name          text NOT NULL DEFAULT '',
    rc_number             text NOT NULL CHECK (rc_number <> ''),
    mcc                   text NOT NULL DEFAULT '',
    symbol                text NOT NULL CHECK (symbol <> ''),

    -- submitted: sent, no decision recorded (Freedom was unreachable or
    -- answered with neither outcome). listed: admitted; an instrument exists.
    -- rejected: a listing criterion was unmet; findings say which.
    state                 text NOT NULL CHECK (state IN ('submitted', 'listed', 'rejected')),

    -- Freedom's findings, one per criterion: [{criterion, met, detail}].
    findings              jsonb NOT NULL DEFAULT '[]'::jsonb,

    freedom_instrument_id text,
    -- Kobo. Freedom's reference price at admission.
    reference_price_minor bigint NOT NULL CHECK (reference_price_minor > 0),

    submitted_at          timestamptz NOT NULL DEFAULT now(),
    decided_at            timestamptz
);

COMMIT;
