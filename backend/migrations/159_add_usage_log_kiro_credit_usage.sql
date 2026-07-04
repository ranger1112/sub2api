-- Add usage_logs.kiro_credit_usage: Kiro 账号本次请求消耗的 credit
-- （meteringEvent.usage 累计）。
--
-- Kiro 上游不下发 token 账目,唯一真实成本口径是 credit;此列仅作观测/对账展示,
-- 不参与 token 计费(token 计费维持既有 input/output/cache 口径)。
-- 非 Kiro 账号恒为 0,不回填。

ALTER TABLE IF EXISTS usage_logs
  ADD COLUMN IF NOT EXISTS kiro_credit_usage DECIMAL(20,10) NOT NULL DEFAULT 0;
