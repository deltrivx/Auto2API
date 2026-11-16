-- WorkBuddy 硬件指纹全局登记表。
--
-- 画像由各维度组合派生（os/版本/CPU/核心/内存/GPU/硬盘/屏幕/Chrome 版本），
-- 组合空间数万量级。这张表保证跨进程、跨批次、跨重启的唯一性：
--   profile_key 唯一约束 —— 同一硬件组合只能被一个账号占用
--   uid 唯一约束         —— 同一账号只有一条登记
--
-- 账号首次分配指纹时写入；已有账号的指纹不受影响。
CREATE TABLE IF NOT EXISTS workbuddy_fingerprint_registry (
    account_id  BIGINT       PRIMARY KEY,
    profile_key VARCHAR(255) NOT NULL,
    uid         VARCHAR(128) NOT NULL,
    salt        INTEGER      NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS workbuddy_fingerprint_registry_profile_key
    ON workbuddy_fingerprint_registry (profile_key);

CREATE UNIQUE INDEX IF NOT EXISTS workbuddy_fingerprint_registry_uid
    ON workbuddy_fingerprint_registry (uid);
