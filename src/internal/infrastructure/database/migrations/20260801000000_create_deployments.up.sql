-- Asset deployments: the productive asset handed to a mobility entrepreneur,
-- carrying its principal, term, and materialised repayment position.
CREATE TABLE IF NOT EXISTS deployments (
    id                     BIGSERIAL PRIMARY KEY,
    uuid                   UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    customer_id            VARCHAR(64)  NOT NULL,
    asset_id               VARCHAR(64)  NOT NULL,
    asset_type             VARCHAR(32)  NOT NULL DEFAULT 'mobility',
    virtual_account_number VARCHAR(32),

    -- Exact decimals: NUMERIC cannot drift the way a float would, and unlike
    -- integer minor units it assumes no particular number of decimal places.
    -- Currency travels with the deployment; every amount below is in it.
    currency               VARCHAR(8)     NOT NULL DEFAULT 'NGN',
    principal              NUMERIC(24, 6) NOT NULL,
    term_weeks             INTEGER        NOT NULL,

    -- Advanced only by the statement that appends the payment to the ledger.
    amount_paid            NUMERIC(24, 6) NOT NULL DEFAULT 0,
    payment_count          BIGINT         NOT NULL DEFAULT 0,

    -- Derived, so no consumer re-implements the floor-at-zero rule.
    outstanding            NUMERIC(24, 6) GENERATED ALWAYS AS (GREATEST(principal - amount_paid, 0)) STORED,

    status                 VARCHAR(20)  NOT NULL DEFAULT 'active',
    start_date             DATE         NOT NULL,
    last_payment_at        TIMESTAMPTZ,
    completed_at           TIMESTAMPTZ,
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT deployments_status_check
        CHECK (status IN ('active', 'delinquent', 'completed', 'written_off')),
    CONSTRAINT deployments_principal_check CHECK (principal > 0),
    CONSTRAINT deployments_term_check      CHECK (term_weeks BETWEEN 1 AND 520),
    -- May exceed principal: overpayment is credit, which keeps the balance
    -- update a pure addition and free of concurrency hazards.
    CONSTRAINT deployments_paid_check      CHECK (amount_paid >= 0)
);

-- Routing depends on one open deployment per customer; a partial unique index
-- makes that impossible to violate rather than a rule every writer remembers.
CREATE UNIQUE INDEX IF NOT EXISTS ux_deployments_customer_open
    ON deployments (customer_id)
    WHERE status IN ('active', 'delinquent');

CREATE INDEX IF NOT EXISTS idx_deployments_customer ON deployments (customer_id);

CREATE UNIQUE INDEX IF NOT EXISTS ux_deployments_virtual_account
    ON deployments (virtual_account_number)
    WHERE virtual_account_number IS NOT NULL;

-- Supports the arrears sweep: open deployments ordered by exposure.
CREATE INDEX IF NOT EXISTS idx_deployments_open_outstanding
    ON deployments (outstanding DESC)
    WHERE status IN ('active', 'delinquent');

COMMENT ON TABLE  deployments IS 'Productive assets deployed to entrepreneurs, with materialised repayment position';
COMMENT ON COLUMN deployments.currency    IS 'Currency every amount on this deployment and its payments is denominated in';
COMMENT ON COLUMN deployments.amount_paid IS 'Running total of applied payments; may exceed principal (credit)';
COMMENT ON COLUMN deployments.outstanding IS 'Derived: principal minus paid, floored at zero';
