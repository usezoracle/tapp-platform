-- The leg is paid from the cardholder's Fintava WALLET: POST /bank/credit
-- takes the wallet's id as sourceId (the backbone pays from its merchant
-- wallet the same way). The column held the customer id, which the rail
-- does not accept as a source; rows in flight are re-pointed at the wallet.
ALTER TABLE card_tap_ngn_settlements RENAME COLUMN source_customer_id TO source_wallet_id;

UPDATE card_tap_ngn_settlements s
   SET source_wallet_id = d.wallet_id
  FROM ngn_deposit_accounts d
 WHERE d.user_id = s.cardholder_id
   AND d.wallet_id IS NOT NULL AND d.wallet_id <> ''
   AND s.state IN ('queued', 'failed');
