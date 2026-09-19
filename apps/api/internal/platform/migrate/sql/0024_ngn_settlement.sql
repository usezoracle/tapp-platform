-- Settling a tap from the cardholder's own naira.
--
-- A tap is paid for out of whatever the cardholder holds. Until now every tap
-- was settled the same way regardless: the cardholder's USDC was sold on
-- chain and a liquidity provider paid the merchant. A cardholder who funded
-- their balance by naira transfer has no USDC to sell -- the tap charged them
-- and the merchant was never paid.
--
-- The merchant is paid from the cardholder's own assets, per leg:
--
--   - the naira the tap spent from their balance is paid to the merchant's
--     bank straight out of the cardholder's own wallet at the rail;
--   - the naira the tap had to BUY by converting USDC is paid, as before, by
--     selling that USDC on chain.
--
-- One tap can have both legs. The tap records the split it made, and each
-- leg has its own row, its own reference at its rail, and its own state.

BEGIN;

-- ------------------------------------------------------------ the split

-- What paid for the tap, in the tap's currency: how much came from a balance
-- already held in it, and how much was bought by converting the funding
-- currency. They sum to the amount charged.
--
-- Every tap that existed before this column converted the whole amount, so
-- the defaults describe them correctly; new taps write both explicitly.
ALTER TABLE card_taps
    ADD COLUMN funded_ngn_minor  bigint NOT NULL DEFAULT 0 CHECK (funded_ngn_minor >= 0),
    ADD COLUMN funded_usdc_minor bigint NOT NULL DEFAULT 0 CHECK (funded_usdc_minor >= 0),
    ADD COLUMN funding_source    text   NOT NULL DEFAULT 'usdc'
        CHECK (funding_source IN ('ngn', 'usdc', 'mixed'));

UPDATE card_taps SET funded_usdc_minor = amount_minor;

ALTER TABLE card_taps
    ADD CONSTRAINT card_taps_funding_sums
        CHECK (funded_ngn_minor + funded_usdc_minor = amount_minor);

COMMENT ON COLUMN card_taps.funding_source IS
    'ngn: paid from a naira balance. usdc: paid by converting USDC. mixed: both. The amounts are in funded_ngn_minor and funded_usdc_minor.';

-- ------------------------------------------------------------ the USDC leg

-- The on-chain order now delivers this leg's share of what the merchant is
-- owed, not necessarily all of it. NULL means the whole of it, which is what
-- every order before legs existed delivered.
ALTER TABLE card_tap_settlements
    ADD COLUMN deliver_minor bigint CHECK (deliver_minor > 0);

COMMENT ON COLUMN card_tap_settlements.deliver_minor IS
    'What this leg delivers to the merchant, in the tap currency. NULL: the whole of the tap less the fee (orders from before taps had legs).';

-- ----------------------------------------------------------- the naira leg

CREATE TABLE card_tap_ngn_settlements (
    tap_id         uuid PRIMARY KEY REFERENCES card_taps(id) ON DELETE CASCADE,
    cardholder_id  uuid NOT NULL,
    merchant_id    uuid NOT NULL,

    -- The cardholder's wallet at the rail that pays: the customer id the
    -- rail debits by, and the account number the deposit row is keyed on.
    -- Copied at the time, so a wallet re-provisioned later does not change
    -- what an old payout says it was paid from.
    source_customer_id    text NOT NULL,
    source_account_number text NOT NULL,

    -- What this leg delivers to the merchant.
    currency       currency NOT NULL,
    amount_minor   bigint NOT NULL CHECK (amount_minor > 0),

    -- The bank the merchant had VERIFIED when the tap was made. Copied rather
    -- than joined, so a merchant changing their account afterwards does not
    -- redirect a payout that was already owed to the old one.
    bank_code      text NOT NULL,
    account_number text NOT NULL,
    account_name   text NOT NULL,

    -- The idempotency key presented to the rail: the same on every attempt,
    -- so a retry cannot pay twice.
    reference      text NOT NULL UNIQUE,

    state          text NOT NULL DEFAULT 'queued'
                   CHECK (state IN ('queued', 'submitted', 'settled', 'failed')),
    -- How many times the rail has been asked. Also the ledger's key for this
    -- attempt's discharge, so a retry is a new movement and not a replay.
    attempts       int NOT NULL DEFAULT 0 CHECK (attempts >= 0),

    -- The rail's own reference, once it has answered.
    rail_ref       text,
    error          text,

    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    submitted_at   timestamptz,
    settled_at     timestamptz
);

COMMENT ON TABLE card_tap_ngn_settlements IS
    'The naira leg of a card tap: the merchant''s bank credited from the cardholder''s own wallet at the rail.';
COMMENT ON COLUMN card_tap_ngn_settlements.state IS
    'queued: owed, nothing sent. submitted: the rail has been asked, outcome not yet known. settled: the rail confirmed the credit. failed: the rail refused; the merchant is still owed this leg and an operator must retry.';

CREATE INDEX card_tap_ngn_settlements_outstanding
    ON card_tap_ngn_settlements (created_at)
 WHERE state IN ('queued', 'submitted');

CREATE INDEX card_tap_ngn_settlements_merchant
    ON card_tap_ngn_settlements (merchant_id, created_at DESC);

-- Reconciliation reads what has left a wallet by this path.
CREATE INDEX card_tap_ngn_settlements_source
    ON card_tap_ngn_settlements (source_account_number);

COMMIT;
