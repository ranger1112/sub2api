CREATE TABLE IF NOT EXISTS checkin_reward_tiers (
    id              BIGSERIAL PRIMARY KEY,
    name            VARCHAR(64) NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    match_type      VARCHAR(16) NOT NULL DEFAULT 'recharge',
    match_threshold DECIMAL(20,8) NOT NULL DEFAULT 0,
    min_reward      DECIMAL(20,8) NOT NULL DEFAULT 0,
    max_reward      DECIMAL(20,8) NOT NULL DEFAULT 0,
    base_cap        DECIMAL(20,8) NOT NULL DEFAULT 0,
    beta_min        DECIMAL(10,6) NOT NULL DEFAULT 1,
    beta_max        DECIMAL(10,6) NOT NULL DEFAULT 3,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_checkin_reward_tiers_enabled ON checkin_reward_tiers(enabled);
CREATE INDEX IF NOT EXISTS idx_checkin_reward_tiers_sort_order ON checkin_reward_tiers(sort_order);

COMMENT ON TABLE checkin_reward_tiers IS '每日签到奖励分层（按充值额或综合评分匹配，命中后覆盖全局奖励区间参数，max_reward 会被全局上限二次夹紧）';
COMMENT ON COLUMN checkin_reward_tiers.name IS '分层名称';
COMMENT ON COLUMN checkin_reward_tiers.enabled IS '是否启用';
COMMENT ON COLUMN checkin_reward_tiers.match_type IS '匹配维度: recharge | score';
COMMENT ON COLUMN checkin_reward_tiers.match_threshold IS '匹配阈值（对应维度 >= 该值时命中）';
COMMENT ON COLUMN checkin_reward_tiers.min_reward IS '该分层单次最小奖励';
COMMENT ON COLUMN checkin_reward_tiers.max_reward IS '该分层单次最大奖励（会被全局上限二次夹紧）';
COMMENT ON COLUMN checkin_reward_tiers.base_cap IS '该分层奖励区间基础上限';
COMMENT ON COLUMN checkin_reward_tiers.beta_min IS '幂律分布 beta 下界';
COMMENT ON COLUMN checkin_reward_tiers.beta_max IS '幂律分布 beta 上界';
COMMENT ON COLUMN checkin_reward_tiers.sort_order IS '排序序号（越大越优先，用于同阈值 tie-break）';
