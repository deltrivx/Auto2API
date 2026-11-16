-- WorkBuddy 积分任务：签到记录与任务配置。
--
-- 任务明细不落库：上游每次实时返回，缓存反而会过期误导操作者。
-- 这里只存两类需要跨请求保留的数据：
--   1. 签到记录 —— 供趋势图与「今日已签 N/M」统计
--   2. 任务配置 —— 按账号覆盖全局的风险策略

CREATE TABLE IF NOT EXISTS workbuddy_checkin_log (
    id          BIGSERIAL PRIMARY KEY,
    account_id  BIGINT        NOT NULL,
    day         DATE          NOT NULL,
    checked_in  BOOLEAN       NOT NULL DEFAULT false,
    credit      NUMERIC(20,8) NOT NULL DEFAULT 0,
    streak_days INTEGER       NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    UNIQUE (account_id, day)
);

CREATE INDEX IF NOT EXISTS idx_wb_checkin_log_day ON workbuddy_checkin_log (day DESC);

CREATE TABLE IF NOT EXISTS workbuddy_task_config (
    id          BIGSERIAL PRIMARY KEY,
    -- account_id 为空表示全局默认配置。
    account_id  BIGINT,
    task_code   VARCHAR(128) NOT NULL,
    enabled     BOOLEAN      NOT NULL DEFAULT true,
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS workbuddy_task_config_scope
    ON workbuddy_task_config (COALESCE(account_id, 0), task_code);

-- 自动执行配置：每个账号一行，控制定时签到与自动领奖。
CREATE TABLE IF NOT EXISTS workbuddy_auto_task_config (
    account_id        BIGINT PRIMARY KEY,
    auto_checkin      BOOLEAN     NOT NULL DEFAULT false,
    auto_claim        BOOLEAN     NOT NULL DEFAULT false,
    include_high_risk BOOLEAN     NOT NULL DEFAULT false,
    last_checkin_at   TIMESTAMPTZ,
    last_claim_at     TIMESTAMPTZ,
    last_status       VARCHAR(32),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
