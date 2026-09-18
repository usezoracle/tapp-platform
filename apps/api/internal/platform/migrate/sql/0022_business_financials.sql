-- The audited figures a listing is priced from, and the valuation that came
-- back.
--
-- Freedom sets the listing price itself: fair value = net assets + 1.0×
-- revenue (trailing twelve months), listing price = fair value / shares in
-- issue. The applicant no longer proposes a price, so reference_price_minor
-- may be absent until the exchange names one -- a rejected business never
-- gets one -- and its check is relaxed to allow zero, which the API renders
-- as null.
--
-- All three are kobo. Null on rows that predate this migration: nothing was
-- submitted, and zero would masquerade as a figure.

BEGIN;

ALTER TABLE merchant_businesses
    ADD COLUMN net_assets_minor bigint CHECK (net_assets_minor IS NULL OR net_assets_minor >= 0),
    ADD COLUMN revenue_minor    bigint CHECK (revenue_minor IS NULL OR revenue_minor >= 0),
    ADD COLUMN fair_value_minor bigint CHECK (fair_value_minor IS NULL OR fair_value_minor >= 0);

ALTER TABLE merchant_businesses
    DROP CONSTRAINT merchant_businesses_reference_price_minor_check,
    ADD CONSTRAINT merchant_businesses_reference_price_minor_check CHECK (reference_price_minor >= 0);

COMMENT ON COLUMN merchant_businesses.reference_price_minor IS
    'Kobo. The listing price the exchange set; 0 until it has (a rejected business never gets one).';

COMMIT;
