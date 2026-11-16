-- 账号归属：支持多用户共建账号池。
--
-- 普通用户只能编辑/删除自己创建的账号，他人账号只读（凭证脱敏）。
-- 历史账号没有创建者记录，统一归第一个管理员所有，避免被普通用户删除。

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS owner_id BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_accounts_owner ON accounts (owner_id);

-- 历史账号回填：归最早的管理员。
UPDATE accounts
SET owner_id = (
    SELECT id FROM users
    WHERE role = 'admin'
    ORDER BY id
    LIMIT 1
)
WHERE owner_id = 0;
