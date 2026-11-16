-- WorkBuddy 积分任务的执行台账。
--
-- 记录每一次签到 / 领奖 / 单任务执行的结果，供「执行历史」与
-- 「每个任务最近一次结果 + 成功次数」两类视图使用。
--
-- 只记结果不记过程：上游日志对运营者没有价值，反而会迅速把表撑大。
CREATE TABLE IF NOT EXISTS workbuddy_task_runs (
    id            BIGSERIAL PRIMARY KEY,
    account_id    BIGINT       NOT NULL,
    account_name  VARCHAR(255) NOT NULL DEFAULT '',
    -- kind: checkin | claim | task | auto_checkin | auto_claim
    kind          VARCHAR(32)  NOT NULL,
    task_code     VARCHAR(128) NOT NULL DEFAULT '',
    task_name     VARCHAR(255) NOT NULL DEFAULT '',
    -- result: success | failed | skipped
    result        VARCHAR(16)  NOT NULL,
    credit        NUMERIC(20,8) NOT NULL DEFAULT 0,
    streak_days   INTEGER      NOT NULL DEFAULT 0,
    detail        TEXT         NOT NULL DEFAULT '',
    occurred_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_wb_task_runs_time ON workbuddy_task_runs (occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_wb_task_runs_account ON workbuddy_task_runs (account_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_wb_task_runs_code ON workbuddy_task_runs (task_code, occurred_at DESC);
