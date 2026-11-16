-- 清除登录条款功能的残留设置行。
--
-- 该功能已整体移除（设置页 Tab、登录页同意拦截、/legal 路由、解析与序列化
-- 全部删除）。代码不再读取这几个键，但它们仍留在 settings 表里，既占空间
-- 也会让运维在排查时误以为功能还在。
--
-- 迁移必须可重放：用 DELETE 而不是假设行一定存在。

DELETE FROM settings
WHERE key IN (
    'login_agreement_enabled',
    'login_agreement_mode',
    'login_agreement_updated_at',
    'login_agreement_documents'
);
