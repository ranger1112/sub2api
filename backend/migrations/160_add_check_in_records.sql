CREATE TABLE IF NOT EXISTS check_in_records (
    id                BIGSERIAL PRIMARY KEY,
    user_id           BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    check_in_date     DATE NOT NULL,
    reward_amount     DECIMAL(20,8) NOT NULL DEFAULT 0,
    streak_count      INTEGER NOT NULL DEFAULT 0,
    score             DECIMAL(10,6) NOT NULL DEFAULT 0,
    recharge_snapshot DECIMAL(20,8) NOT NULL DEFAULT 0,
    usage_snapshot    DECIMAL(20,8) NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 每个用户每天最多签到一次（并发签到的乐观锁依据）
CREATE UNIQUE INDEX IF NOT EXISTS uq_check_in_records_user_date ON check_in_records(user_id, check_in_date);
CREATE INDEX IF NOT EXISTS idx_check_in_records_user_created ON check_in_records(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_check_in_records_date ON check_in_records(check_in_date);

COMMENT ON TABLE check_in_records IS '每日签到送余额记录（赠送额度仅计入 balance，不计入 total_recharged）';
COMMENT ON COLUMN check_in_records.user_id IS '签到用户ID';
COMMENT ON COLUMN check_in_records.check_in_date IS '签到日期（应用时区，DATE）';
COMMENT ON COLUMN check_in_records.reward_amount IS '本次签到赠送金额（USD）';
COMMENT ON COLUMN check_in_records.streak_count IS '当前连续签到天数';
COMMENT ON COLUMN check_in_records.score IS '奖励计算出的综合评分（审计用）';
COMMENT ON COLUMN check_in_records.recharge_snapshot IS '计算奖励时的累计充值快照（审计用）';
COMMENT ON COLUMN check_in_records.usage_snapshot IS '计算奖励时的近30天用量快照（审计用）';
