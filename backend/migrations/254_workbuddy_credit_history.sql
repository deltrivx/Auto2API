-- WorkBuddy 积分日汇总表。
--
-- 账号列表的积分单元格除了余额，还要展示近 N 天的消耗趋势。
-- 逐次调用记录只保留在内存，进程重启即丢；按天汇总落库才能画出跨重启的曲线。
--
-- 与生产环境 workbuddy_credit_history 结构一致：
--   day + consumed/earned/task_credits/checkin_credits + source
CREATE TABLE IF NOT EXISTS workbuddy_credit_history (
    id               BIGSERIAL PRIMARY KEY,
    day              DATE           NOT NULL,
    consumed         NUMERIC(20,8)  NOT NULL DEFAULT 0,
    earned           NUMERIC(20,8)  NOT NULL DEFAULT 0,
    task_credits     NUMERIC(20,8)  NOT NULL DEFAULT 0,
    source           VARCHAR(32)    NOT NULL DEFAULT 'import',
    created_at       TIMESTAMPTZ    NOT NULL DEFAULT now(),
    checkin_credits  NUMERIC(20,8)  NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_wb_credit_history_day ON workbuddy_credit_history (day DESC);
