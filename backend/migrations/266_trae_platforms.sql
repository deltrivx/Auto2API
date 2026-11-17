-- 平台约束放开：加入字节跳动 Trae（国内站 / 国际站）。
--
-- 261 把 accounts.platform 锁成 WorkBuddy 两个站点 + 腾讯 Marvis。现在产品线
-- 再扩一条：Trae 的国内站与国际站是两套独立认证与端点，登记为两个平台。
--
-- 与 261 同样的做法：同名约束重建，语义始终是「平台必须是已登记的具体平台」，
-- 改的只是允许集合，后续再扩平台仍是 drop + add。

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_platform_supported;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_platform_supported
    CHECK (platform IN ('workbuddy_cn', 'workbuddy_intl', 'marvis', 'trae_cn', 'trae_intl'));

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_platform_supported;

ALTER TABLE groups
    ADD CONSTRAINT groups_platform_supported
    CHECK (platform IN ('workbuddy_cn', 'workbuddy_intl', 'marvis', 'trae_cn', 'trae_intl', 'composite'));
