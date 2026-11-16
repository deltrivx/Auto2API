-- 历史账号归属回填（加固）。
--
-- 258 已为历史账号设置过 owner_id，但当时只考虑"存在管理员"的情况。
-- 这里补齐两个边界：
--   1. 系统没有管理员时，归最早的用户，避免 owner 悬空为 0；
--   2. 仍为 0 的账号再兜底一次，保证不存在"无主账号"。

UPDATE accounts
SET owner_id = COALESCE(
    (SELECT id FROM users WHERE role = 'admin' ORDER BY id LIMIT 1),
    (SELECT id FROM users ORDER BY id LIMIT 1)
)
WHERE owner_id = 0;
