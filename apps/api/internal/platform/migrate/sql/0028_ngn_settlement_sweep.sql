-- The naira leg is paid in two hops. The rail's wallet-to-bank call answers
-- a customer wallet with a bare 500, whatever it is sent; its wallet-to-
-- wallet and merchant-wallet-to-bank calls work. So the leg is swept from
-- the cardholder's wallet into the platform's wallet first, and the
-- platform's wallet pays the bank. Each hop keeps its own reference.
ALTER TABLE card_tap_ngn_settlements
    -- What the cardholder's naira paid for this tap: the leg plus the
    -- scheme fee, which is what their wallet must be relieved of. The sweep
    -- is this less the rail's sender fee, so the wallet loses exactly it.
    ADD COLUMN funded_minor    bigint,
    -- The sweep, once the rail has accepted it: its reference, when, and
    -- what it charged the sender. A retry after a refused bank transfer
    -- sees the reference and does not sweep again.
    ADD COLUMN sweep_ref       text,
    ADD COLUMN swept_at        timestamptz,
    ADD COLUMN sweep_fee_minor bigint,
    -- What the rail charged the platform's wallet for the bank transfer.
    ADD COLUMN rail_fee_minor  bigint;

UPDATE card_tap_ngn_settlements s
   SET funded_minor = t.funded_ngn_minor
  FROM card_taps t
 WHERE t.id = s.tap_id;

ALTER TABLE card_tap_ngn_settlements
    ALTER COLUMN funded_minor SET NOT NULL,
    ADD CONSTRAINT card_tap_ngn_settlements_funded_covers_leg CHECK (funded_minor >= amount_minor);
