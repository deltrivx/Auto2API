-- 修复残留引用 deleted_at 的认证缓存失效触发器。
--
-- 262 迁移删除了 users / api_keys / groups 的 deleted_at 列，但这些
-- PL/pgSQL 函数的文本里还引用着它。函数体不会随列删除而失效 —— 要等到
-- 真正执行时才报 `record "old" has no field "deleted_at"`：
--
--   touch user last active failed: pq: record "old" has no field "deleted_at"
--
-- 物理删除之后「未删除」条件恒真、「删除状态变化」恒假，因此直接做文本替换。

DO $$
DECLARE
    fn text;
    body text;
    patched text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'enqueue_user_auth_cache_invalidation',
        'enqueue_api_key_auth_cache_invalidation',
        'enqueue_allowed_group_auth_cache_invalidation',
        'enqueue_group_auth_cache_invalidation'
    ] LOOP
        SELECT prosrc INTO body FROM pg_proc WHERE proname = fn;
        IF body IS NULL OR body NOT LIKE '%deleted_at%' THEN
            CONTINUE;
        END IF;

        patched := body;
        patched := replace(patched, ' AND k.deleted_at IS NULL', '');
        patched := replace(patched, ' AND NEW.deleted_at IS NULL', '');
        patched := replace(patched, 'NEW.deleted_at IS NULL AND ', '');
        patched := replace(patched, ' OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at', '');
        patched := replace(patched, ' AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at', '');

        EXECUTE format(
            'CREATE OR REPLACE FUNCTION %I() RETURNS TRIGGER AS %L LANGUAGE plpgsql',
            fn, patched
        );
    END LOOP;
END $$;
