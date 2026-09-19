-- Two columns the first version of ngn_deposit_accounts turned out to need.
--
-- bank_code: an account number and a bank name are what a person types into
-- their banking app, but the code is what a transfer is actually addressed
-- to, and it is what the admin console and any client that autofills a
-- payee need. Fintava names the bank internally ("loma") and the code comes
-- from its bank list; both are resolved at provisioning and stored here.
--
-- wallet_id: rail_ref was written as the rail's customer id, which is a
-- support handle and not what the balance endpoint takes. Fintava's wallet
-- id is a second string, and reconciliation (reading the wallet's balance to
-- post credits a lost webhook never delivered) cannot happen without it.
-- Rows opened before this column exist with '' and are filled in the first
-- time reconciliation locates the wallet by account number.
--
-- Both default to '' rather than NULL: an absent value is an ordinary state
-- of a row here, not a third answer, and every reader already treats "" as
-- "not known".

BEGIN;

ALTER TABLE ngn_deposit_accounts
    ADD COLUMN bank_code text NOT NULL DEFAULT '',
    ADD COLUMN wallet_id text NOT NULL DEFAULT '';

COMMIT;
