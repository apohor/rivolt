-- DC fast charge network auto-detection with user verification
-- (scratchpad contract dcfc-auto-network-contract.md v1).
--
-- network_state: 'estimated' (priced from a confident network match),
-- 'needs_confirmation' (a co-located site, a far or unmatched station,
-- or nothing matched: cost may be NULL), 'confirmed' (the user agreed
-- with the estimate). NULL for everything else: AC, Rivian-billed,
-- user-priced and legacy rows. A user edit (UpdatePricing / MarkFree)
-- sets it back to NULL with cost_source 'user'.
-- network_confidence: 0..1 at detection time.
-- network_evidence: what the detection saw (candidates and distances,
-- OSM agreement, which signal decided), kept for audit after a confirm
-- or an edit.
ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS network_state text
        CHECK (network_state IN ('estimated', 'needs_confirmation', 'confirmed')),
    ADD COLUMN IF NOT EXISTS network_confidence real,
    ADD COLUMN IF NOT EXISTS network_evidence jsonb;

-- "N to confirm" on Money: the open states only, per user.
CREATE INDEX IF NOT EXISTS charges_network_state_open
    ON charges (user_id) WHERE network_state IN ('estimated', 'needs_confirmation');
