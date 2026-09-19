-- The rail reports no fee for a bank transfer; it deducts ₦30.75 from the
-- merchant wallet and says nothing. Legs settled before the worker booked
-- the configured fee carry none, and the books are short by it. Record the
-- fee on those rows and post it, keyed so the posting cannot repeat.
UPDATE card_tap_ngn_settlements
   SET rail_fee_minor = 3075
 WHERE state = 'settled' AND sweep_ref IS NOT NULL AND coalesce(rail_fee_minor, 0) = 0;

WITH owed AS (
    SELECT s.tap_id, s.attempts
      FROM card_tap_ngn_settlements s
     WHERE s.state = 'settled' AND s.sweep_ref IS NOT NULL AND s.rail_fee_minor = 3075
       AND NOT EXISTS (
           SELECT 1 FROM ledger_transactions t
            WHERE t.idem_key = 'rail_fees_paid:' || s.tap_id || ':' || s.attempts || ':bank_fee_backfill')
), tx AS (
    INSERT INTO ledger_transactions (id, ref_type, ref_id, idem_key)
    SELECT gen_random_uuid(), 'rail_fees_paid', tap_id,
           'rail_fees_paid:' || tap_id || ':' || attempts || ':bank_fee_backfill'
      FROM owed
    RETURNING id
), accts AS (
    SELECT kind, id FROM ledger_accounts
     WHERE owner_kind = 'system' AND owner_id IS NULL AND currency = 'NGN' AND kind IN ('revenue', 'external')
)
INSERT INTO ledger_entries (tx_id, account_id, currency, amount_minor, reason)
SELECT tx.id, a.id, 'NGN',
       CASE a.kind WHEN 'revenue' THEN -3075 ELSE 3075 END,
       CASE a.kind WHEN 'revenue' THEN 'scheme_fee.rail_fees' ELSE 'rail.fees_charged' END
  FROM tx CROSS JOIN accts a;
