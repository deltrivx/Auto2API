-- 平台约束放开：加入腾讯 Marvis。
--
-- 260 把 accounts.platform 锁成了 WorkBuddy 两个站点。现在产品线扩展为
-- WorkBuddy（国内/国际）+ 腾讯 Marvis 三选一，约束随之放宽。
--
-- 注意这里用同名约束重建，而不是新增一个：约束语义始终是「平台必须是
-- 已登记的具体平台」，改的只是允许集合。保留同一个名字，后续再扩平台
-- 仍然是 drop + add，不会积累一串历史约束。

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_platform_workbuddy_only;

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_platform_supported;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_platform_supported
    CHECK (platform IN ('workbuddy_cn', 'workbuddy_intl', 'marvis'));

-- 分组表的平台字段同样受限（groups.platform）。
ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_platform_workbuddy_only;

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_platform_supported;

ALTER TABLE groups
    ADD CONSTRAINT groups_platform_supported
    CHECK (platform IN ('workbuddy_cn', 'workbuddy_intl', 'marvis', 'composite'));
