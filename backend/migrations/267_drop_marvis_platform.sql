-- 移除 Marvis 平台后，收窄 accounts / groups 的平台白名单。
--
-- 背景：266 当年把 Trae 两个站点加进白名单时，白名单里还有 marvis。
-- Marvis 已整体下线（代码、数据、语言包均移除），约束里保留该值只会让
-- 「已登记的具体平台」这一语义失真，所以这里再做一次同名约束重建。
--
-- 为什么要新开 267 而不是改 266：
--   迁移一经应用即被 checksum 校验，改动已应用的文件会让服务启动失败。
--   任何后续约束变化都必须新增迁移文件。
--
-- 幂等：先删后建，重复执行无副作用；执行前清掉任何越界数据（Marvis 已下线，
-- 这些行本就不参与调度，保留只会在统计里制造噪音）。

DELETE FROM accounts WHERE platform NOT IN ('workbuddy_cn', 'workbuddy_intl', 'trae_cn', 'trae_intl');
DELETE FROM groups WHERE platform NOT IN ('workbuddy_cn', 'workbuddy_intl', 'trae_cn', 'trae_intl', 'composite');

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_platform_supported;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_platform_supported
    CHECK (platform IN ('workbuddy_cn', 'workbuddy_intl', 'trae_cn', 'trae_intl'));

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_platform_supported;

ALTER TABLE groups
    ADD CONSTRAINT groups_platform_supported
    CHECK (platform IN ('workbuddy_cn', 'workbuddy_intl', 'trae_cn', 'trae_intl', 'composite'));
