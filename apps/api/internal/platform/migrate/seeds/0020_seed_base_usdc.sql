-- Seed the Base network and its USDC token.
--
-- Without these rows GetTokenRate answers 500 "Failed to fetch token rate" for
-- every quote. The handler's first act is to look up the token by symbol, and
-- ent returns not-found as an error, which falls into the generic 500 branch --
-- so an unseeded database is indistinguishable from a broken rate provider.
-- The provider was fine; there was simply no USDC row to price.
--
-- These were never seeded because the older deployment's database was
-- populated by hand. Anything set up that way exists on exactly one machine
-- and is lost the moment a second environment appears, which is what happened
-- here: schema travelled with the binary, reference data did not.
--
-- Values match what the deposit rail already runs on, so the table cannot
-- disagree with the chain the watcher is reading:
--   chain 8453, USDC 0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913, 6 decimals
-- (see BASE_USDC_CONTRACT / BASE_USDC_DECIMALS).
--
-- The identifier is 'base', matching the name used for the CDP network, the
-- ledger rail in movements.Deposit, and the admin funding account.

INSERT INTO "networks" (
    "chain_id", "identifier", "rpc_endpoint", "is_testnet", "fee",
    "created_at", "updated_at"
)
SELECT 8453, 'base', 'https://mainnet.base.org', false, 0, now(), now()
WHERE NOT EXISTS (SELECT 1 FROM "networks" WHERE "identifier" = 'base');

-- Keyed off the network row above rather than a literal id: networks.id is an
-- identity column, so its value depends on what else has been inserted.
INSERT INTO "tokens" (
    "symbol", "contract_address", "decimals", "is_enabled", "network_tokens",
    "created_at", "updated_at"
)
SELECT 'USDC', '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', 6, true, n."id", now(), now()
  FROM "networks" n
 WHERE n."identifier" = 'base'
   AND NOT EXISTS (
        SELECT 1 FROM "tokens" t
         WHERE t."symbol" = 'USDC' AND t."network_tokens" = n."id"
   );
