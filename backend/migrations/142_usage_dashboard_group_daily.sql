-- Group-dimensioned daily pre-aggregation for admin group usage summary.
-- Purpose: serve GetAllGroupUsageSummary (admin group list) without scanning
-- the entire usage_logs table for cumulative cost. Historical days are read
-- from this table; current day is still summed from usage_logs incrementally.
--
-- Bucket alignment follows the project's local timezone (see timezone.Name()).
-- Rows with NULL group_id in usage_logs are intentionally excluded — those
-- requests are not associated with any group and never appear in the summary.

CREATE TABLE IF NOT EXISTS usage_dashboard_group_daily (
    bucket_date           DATE             NOT NULL,
    group_id              BIGINT           NOT NULL,
    total_requests        BIGINT           NOT NULL DEFAULT 0,
    input_tokens          BIGINT           NOT NULL DEFAULT 0,
    output_tokens         BIGINT           NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT           NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT           NOT NULL DEFAULT 0,
    total_cost            DECIMAL(20, 10)  NOT NULL DEFAULT 0,
    actual_cost           DECIMAL(20, 10)  NOT NULL DEFAULT 0,
    computed_at           TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, group_id)
);

-- Lookup pattern: WHERE group_id = ? AND bucket_date < today ORDER BY bucket_date DESC
CREATE INDEX IF NOT EXISTS idx_usage_dashboard_group_daily_group_bucket
    ON usage_dashboard_group_daily (group_id, bucket_date DESC);

-- Sweep pattern (CleanupAggregates): WHERE bucket_date < cutoff_date
CREATE INDEX IF NOT EXISTS idx_usage_dashboard_group_daily_bucket_date
    ON usage_dashboard_group_daily (bucket_date DESC);

COMMENT ON TABLE usage_dashboard_group_daily IS
    'Pre-aggregated daily usage metrics per group for admin GetAllGroupUsageSummary.';
COMMENT ON COLUMN usage_dashboard_group_daily.bucket_date IS
    'Local-timezone date of the day bucket; matches timezone.Name().';
COMMENT ON COLUMN usage_dashboard_group_daily.computed_at IS
    'When the row was last computed/refreshed by DashboardAggregationService.';
