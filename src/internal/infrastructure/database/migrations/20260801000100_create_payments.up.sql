-- Immutable payment ledger, append-only: a row is written once, so the balance
-- on `deployments` can always be re-derived from here.
--
-- Partitioned from day one because 100,000 notifications/minute is ~144M rows
-- per day, and converting a live multi-billion-row heap later is a migration
-- nobody wants to run. The key is transaction_reference, hashed, because:
--   * a unique index on a partitioned table must include the partition key,
--     so hashing on the reference keeps idempotency global, not per-window;
--   * every dedup probe is an equality match, so the planner prunes to one
--     partition and the hot path stays an index lookup on a 1/16-sized btree;
--   * writes spread evenly, avoiding the right-edge contention a time-based
--     key would create at 1,700 inserts/second.
CREATE TABLE IF NOT EXISTS payments (
    transaction_reference VARCHAR(128) NOT NULL,
    uuid                  UUID         NOT NULL DEFAULT gen_random_uuid(),
    customer_id           VARCHAR(64)  NOT NULL,

    -- NULL for unmatched credits, which are recorded rather than discarded.
    deployment_id         BIGINT       REFERENCES deployments (id),

    -- Exact decimals, at the same precision as the deployment they apply to.
    currency              VARCHAR(8)     NOT NULL,
    amount                NUMERIC(24, 6) NOT NULL,
    applied_amount        NUMERIC(24, 6) NOT NULL DEFAULT 0,
    excess_amount         NUMERIC(24, 6) NOT NULL DEFAULT 0,

    -- Taken inside the statement that moved the money, so any row audits alone.
    balance_before        NUMERIC(24, 6) NOT NULL DEFAULT 0,
    balance_after         NUMERIC(24, 6) NOT NULL DEFAULT 0,

    outcome               VARCHAR(20)  NOT NULL,
    provider_status       VARCHAR(32)  NOT NULL,
    transaction_date      TIMESTAMPTZ  NOT NULL,
    received_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    raw_payload           JSONB        NOT NULL,

    -- The idempotency guarantee. Every other dedup mechanism (cache, probe) is
    -- an optimisation in front of this constraint; only this is authoritative.
    CONSTRAINT payments_pkey PRIMARY KEY (transaction_reference),

    CONSTRAINT payments_outcome_check
        CHECK (outcome IN ('applied', 'unmatched', 'ignored')),
    CONSTRAINT payments_amount_check  CHECK (amount > 0),
    CONSTRAINT payments_applied_check CHECK (applied_amount >= 0 AND excess_amount >= 0),
    CONSTRAINT payments_split_check   CHECK (applied_amount + excess_amount <= amount)
) PARTITION BY HASH (transaction_reference);

CREATE TABLE IF NOT EXISTS payments_p00 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 0);
CREATE TABLE IF NOT EXISTS payments_p01 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 1);
CREATE TABLE IF NOT EXISTS payments_p02 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 2);
CREATE TABLE IF NOT EXISTS payments_p03 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 3);
CREATE TABLE IF NOT EXISTS payments_p04 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 4);
CREATE TABLE IF NOT EXISTS payments_p05 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 5);
CREATE TABLE IF NOT EXISTS payments_p06 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 6);
CREATE TABLE IF NOT EXISTS payments_p07 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 7);
CREATE TABLE IF NOT EXISTS payments_p08 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 8);
CREATE TABLE IF NOT EXISTS payments_p09 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 9);
CREATE TABLE IF NOT EXISTS payments_p10 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 10);
CREATE TABLE IF NOT EXISTS payments_p11 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 11);
CREATE TABLE IF NOT EXISTS payments_p12 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 12);
CREATE TABLE IF NOT EXISTS payments_p13 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 13);
CREATE TABLE IF NOT EXISTS payments_p14 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 14);
CREATE TABLE IF NOT EXISTS payments_p15 PARTITION OF payments FOR VALUES WITH (MODULUS 16, REMAINDER 15);

-- Reconciliation: "what did this customer send, newest first". No route serves
-- it, but support and finance ask it of the ledger directly.
CREATE INDEX IF NOT EXISTS idx_payments_customer_date
    ON payments (customer_id, transaction_date DESC);

CREATE INDEX IF NOT EXISTS idx_payments_deployment
    ON payments (deployment_id)
    WHERE deployment_id IS NOT NULL;

-- The reconciliation queue: credits with nowhere to go. Partial, so it stays
-- small as the ledger grows.
CREATE INDEX IF NOT EXISTS idx_payments_unmatched
    ON payments (received_at DESC)
    WHERE outcome = 'unmatched';

COMMENT ON TABLE  payments IS 'Append-only ledger of every inbound payment notification';
COMMENT ON COLUMN payments.transaction_reference IS 'Provider reference; the idempotency key for the whole service';
COMMENT ON COLUMN payments.raw_payload IS 'Original provider payload, retained verbatim for dispute resolution';
