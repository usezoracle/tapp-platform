-- The sort code the naira leg was actually sent under.
--
-- bank_code on a settlement is the institution code from the catalogue the
-- merchant chose their bank from ("MONINGPC" for Moniepoint). The rail that
-- pays the leg has its own codes ("090405"), and the first real payout sent
-- it the catalogue's: the rail could not resolve the account, the leg
-- failed, and the merchant was still owed. The worker now resolves the
-- rail's code before it asks for anything, and records it here so an
-- operator can see what was sent.
--
-- NULL: nothing has been sent yet, or the code could not be resolved (in
-- which case the row is failed and the error names the institution).

BEGIN;

ALTER TABLE card_tap_ngn_settlements
    ADD COLUMN fintava_bank_code text;

COMMENT ON COLUMN card_tap_ngn_settlements.fintava_bank_code IS
    'The sort code the rail was given for the beneficiary bank, resolved from bank_code on each attempt. NULL until an attempt resolved one.';

COMMIT;
