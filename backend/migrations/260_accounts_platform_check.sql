-- 平台约束：账号只能属于 WorkBuddy 两个站点。
--
-- 应用层已通过 domain.IsConcretePlatform 拦住其他平台，但数据库没有兜底：
-- 直接 INSERT 或历史迁移仍可能写入别的 platform，那类账号在网关里永远
-- 选不到，只会在调度和统计里制造噪音。这里加一道 CHECK。
--
-- 先清理任何越界数据（本分支只应有 workbuddy_cn / workbuddy_intl），
-- 若真存在说明有人绕过应用层写入，删除前它们本就不参与调度。

DELETE FROM accounts
WHERE platform NOT IN ('workbuddy_cn', 'workbuddy_intl');

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_platform_workbuddy_only;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_platform_workbuddy_only
    CHECK (platform IN ('workbuddy_cn', 'workbuddy_intl'));
