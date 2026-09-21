-- Shares are issued when the merchant is paid, not when the cardholder is
-- charged.
--
-- The fee that funds a buyback is earned when the merchant receives their
-- money. A tap whose settlement later fails, or is refunded round after
-- round, has earned nothing, and shares bought against it would have to be
-- clawed back. So a tap's outbox row is still written in the tap's own
-- transaction -- nothing is ever lost -- but it starts HELD, and the worker
-- only delivers QUEUED rows. The row is queued when every leg the tap has is
-- settled (internal/equity: ReleaseIfSettled, and the worker's sweep).
--
-- A reversal of a tap the market has never heard of has nothing to unwind:
-- the held or queued row is CANCELLED instead of a reversal being sent.
--
-- 'pending' (queued for delivery) is renamed 'queued', so the outbox does
-- not share a word with the market's own 'pending' (delivered, waiting for
-- a session with a price). Every undelivered tap row is moved to held: the
-- sweep releases the ones whose taps are already settled on the first tick.

BEGIN;

ALTER TABLE equity_outbox DROP CONSTRAINT equity_outbox_state_check;

UPDATE equity_outbox SET state = 'held'   WHERE state = 'pending' AND kind = 'tap';
UPDATE equity_outbox SET state = 'queued' WHERE state = 'pending';

ALTER TABLE equity_outbox
    ALTER COLUMN state SET DEFAULT 'held',
    ADD CONSTRAINT equity_outbox_state_check
        CHECK (state IN ('held', 'queued', 'delivered', 'failed', 'cancelled'));

COMMENT ON COLUMN equity_outbox.state IS
    'held: the tap is charged but not yet settled to the merchant; never delivered. queued: due for delivery. delivered: acknowledged by Freedom; response holds what it said. failed: given up after repeated refusals; last_error says why. cancelled: reversed before the market heard of it; nothing was or will be sent, last_error says why.';

-- The worker's query: what is due, oldest first.
DROP INDEX equity_outbox_due;
CREATE INDEX equity_outbox_due ON equity_outbox (next_at, id) WHERE state = 'queued';

-- The sweep's query: what is waiting on settlement.
CREATE INDEX equity_outbox_held ON equity_outbox (tap_id) WHERE state = 'held';

COMMIT;
