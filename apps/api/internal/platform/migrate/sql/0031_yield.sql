-- Cardholders' idle USDC earns in an ERC-4626 vault, from their own smart
-- account. See docs/yield.md.
--
-- vault_movements is every deposit into the vault and every release out of
-- it, keyed by the idempotency key the operation was sent under. A row is
-- written 'sending' before the operation is sent and stays so until CDP
-- reports it final: 'confirmed' when it landed, 'failed' only when CDP says
-- it did not. An answer that was lost leaves it 'sending' and it is sent
-- again under the same key. What the account put in, net of what it took
-- out, is the sum of its confirmed rows; anything above that in the vault
-- is yield.
--
-- yield_credits is what has been credited to each cardholder's ledger, at
-- most once a day, with the figures it was worked out from.
BEGIN;

CREATE TABLE vault_movements (
    reference    text PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users(id),
    account      text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('deposit', 'release')),
    amount_micro bigint NOT NULL CHECK (amount_micro > 0),
    state        text NOT NULL DEFAULT 'sending' CHECK (state IN ('sending', 'confirmed', 'failed')),
    tx_hash      text,
    error        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX vault_movements_user ON vault_movements (user_id);
CREATE INDEX vault_movements_sending ON vault_movements (state) WHERE state = 'sending';

CREATE TABLE yield_credits (
    user_id           uuid NOT NULL REFERENCES users(id),
    day               date NOT NULL,
    amount_micro      bigint NOT NULL CHECK (amount_micro > 0),
    vault_value_micro bigint NOT NULL,
    principal_micro   bigint NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day)
);

COMMIT;
