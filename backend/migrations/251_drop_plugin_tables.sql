-- 251_drop_plugin_tables.sql
-- 移除插件功能后，删除遗留的插件表。
-- 注意：此迁移不可逆，执行前请确认已备份。
-- 表名沿用历史上的 sub2api_* 前缀（见 229_plugins.sql / 230_plugin_artifacts.sql）。

DROP TABLE IF EXISTS sub2api_plugin_bindings CASCADE;
DROP TABLE IF EXISTS sub2api_plugin_installations CASCADE;
