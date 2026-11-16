-- 彻底移除软删除：删除各表的 deleted_at 列。
--
-- 背景：SoftDeleteMixin 的拦截器与钩子此前已被清空（删除即物理删除），
-- 但列本身仍留在表里，成为永不写入的死字段。它带来两个实际代价：
--   1. 每张表多一列 + 一个索引，写放大白付；
--   2. 查询里大量 `deleted_at IS NULL` 全是恒真条件，读代码时误导性强。
--
-- 本次连同 schema 里的 mixin 与所有代码引用一并删除。
--
-- ⚠ 前置条件：所有软删除行必须已被清空。执行前请确认：
--      SELECT count(*) FROM accounts WHERE deleted_at IS NOT NULL;  -- 期望 0
--   若仍有残留，本迁移会把它们一并物理删除（见下方 DELETE）。
--
-- 保留 deleted_at 的表（不在本次范围）：
--   · scoped_admin_api_keys —— 它用 deleted_at 表达「已吊销」这一业务状态，
--     与软删除机制无关；
--   · deleted_api_key_audits —— 审计归档表，列名是历史契约。

-- ① 物理删除遗留的软删除行（正常情况下是 0 行）。
--
-- 整段用 DO 块包起来并检查列是否存在：本迁移可能在手工跑过之后被应用再次
-- 执行，那时列已经没了，直接 DELETE 会以 "column does not exist" 失败并把
-- 启动卡住。迁移必须可重放。
DO $$
DECLARE
    tbl text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY[
        'user_subscriptions', 'user_platform_quotas', 'user_attribute_definitions',
        'composite_model_routes', 'api_keys', 'groups', 'proxies', 'accounts', 'users'
    ] LOOP
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = tbl AND column_name = 'deleted_at'
        ) THEN
            EXECUTE format('DELETE FROM %I WHERE deleted_at IS NOT NULL', tbl);
        END IF;
    END LOOP;
END $$;

-- ② 删除列（连同其上的索引/部分索引会自动一并移除）。
ALTER TABLE user_subscriptions          DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE user_platform_quotas        DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE user_attribute_definitions  DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE composite_model_routes      DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE api_keys                    DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE groups                      DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE proxies                     DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE accounts                    DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE users                       DROP COLUMN IF EXISTS deleted_at;

-- ③ 原先带 `WHERE deleted_at IS NULL` 的部分索引：删列时 Postgres 会连索引
--    一起删掉，但它们本身是有用的查询索引，这里按原定义重建成普通索引。
DROP INDEX IF EXISTS idx_api_keys_expires_at;
CREATE INDEX IF NOT EXISTS idx_api_keys_expires_at ON api_keys (expires_at);

DROP INDEX IF EXISTS idx_api_keys_last_used_at;
CREATE INDEX IF NOT EXISTS idx_api_keys_last_used_at ON api_keys (last_used_at);

-- ④ 原先靠 `WHERE deleted_at IS NULL` 实现「同一范围内允许多条历史、只一条有效」
--    的唯一索引。物理删除后历史行真的消失，改为无条件唯一即可。
--
--    ⚠ 这段必须在步骤 ① 之后执行：残留的软删除行会与新索引的唯一性冲突。

DROP INDEX IF EXISTS user_subscriptions_user_group_unique_active;
CREATE UNIQUE INDEX IF NOT EXISTS user_subscriptions_user_group_unique_active
    ON user_subscriptions (user_id, group_id);

DROP INDEX IF EXISTS idx_composite_model_routes_unique_active;
DROP INDEX IF EXISTS idx_users_email_dot_stripped;

-- ⑤ 其余带条件的普通索引重建为普通索引。
DROP INDEX IF EXISTS idx_groups_duplicate_operation_id_active;
CREATE INDEX IF NOT EXISTS idx_groups_duplicate_operation_id
    ON groups (duplicate_operation_id);

DROP INDEX IF EXISTS idx_user_subscriptions_user_status_expires_active;
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user_status_expires
    ON user_subscriptions (user_id, status, expires_at);

DROP INDEX IF EXISTS idx_composite_model_routes_group_enabled;
CREATE INDEX IF NOT EXISTS idx_composite_model_routes_group_enabled
    ON composite_model_routes (group_id, enabled);

DROP INDEX IF EXISTS idx_composite_model_routes_group_priority;
CREATE INDEX IF NOT EXISTS idx_composite_model_routes_group_priority
    ON composite_model_routes (group_id, priority);

-- ⑥ 纯粹的 deleted_at 索引：列已删除，索引也随之一并消失，无需重建。
