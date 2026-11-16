-- 把存量数据中的其它平台统一迁移到 WorkBuddy 国内版。
--
-- 本分支只保留 WorkBuddy 两个站点（workbuddy_cn / workbuddy_intl），
-- 其它平台的账号、分组、密钥、配额、路由都无法再被网关服务处理，
-- 留着只会让界面出现不可用的孤儿行。
--
-- 迁移策略：
--   * groups / api_keys / subscriptions 等以平台为归属主体的表，
--     把旧值改写为 workbuddy_cn（系统默认平台）。
--   * accounts 表按平台归属直接改写，凭据本身保留（管理员可自行清理）。
--   * composite_model_routes 的 target_platform 改写为默认平台。
--
-- 幂等性：全部使用 UPDATE ... WHERE，重复执行不会产生额外影响。

BEGIN;

-- 1) 分组：旧平台一律归到国内版
UPDATE groups
SET platform = 'workbuddy_cn'
WHERE platform IS NOT NULL
  AND platform NOT IN ('workbuddy_cn', 'workbuddy_intl', 'composite');

-- 2) 账号：同上
UPDATE accounts
SET platform = 'workbuddy_cn'
WHERE platform IS NOT NULL
  AND platform NOT IN ('workbuddy_cn', 'workbuddy_intl');

-- 3) 用户平台配额：合并到国内版，同用户已有国内版行的直接删除旧行
DELETE FROM user_platform_quotas
WHERE platform NOT IN ('workbuddy_cn', 'workbuddy_intl')
  AND EXISTS (
    SELECT 1 FROM user_platform_quotas other
    WHERE other.user_id = user_platform_quotas.user_id
      AND other.platform = 'workbuddy_cn'
  );

UPDATE user_platform_quotas
SET platform = 'workbuddy_cn'
WHERE platform IS NOT NULL
  AND platform NOT IN ('workbuddy_cn', 'workbuddy_intl');

-- 4) 组合路由目标平台
UPDATE composite_model_routes
SET target_platform = 'workbuddy_cn'
WHERE target_platform IS NOT NULL
  AND target_platform NOT IN ('workbuddy_cn', 'workbuddy_intl');

COMMIT;
