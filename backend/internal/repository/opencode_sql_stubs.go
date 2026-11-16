package repository

// OpenCode 平台已移除，但历史查询里仍拼接着这几个 SQL 片段。
//
// 它们必须展开成**合法且恒假**的表达式——移除该平台后这些分支不应再命中
// 任何账号，同时不能让整条 SQL 语法错误（那会让账号更新等写操作 500）：
//
//   - opencodeGoUsageMountPlatformsSQL 用在 `platform IN (...)` 中。
//     空串生成 `IN ()` 是语法错误；改为 NULL 得到 `IN (NULL)`，
//     语法合法，且 NULL 与任何值比较都不为真。
//
//   - base_url 前缀/后缀包住的是一段**文本**表达式
//     （`credentials ->> 'base_url'`）。裸拼会得到
//     `AND credentials ->> 'base_url'`，PostgreSQL 报
//     "argument of AND must be type boolean"。
//     把这些片段整体替换成常量布尔，拼接结果恒为 FALSE：
//     文本表达式作为被替换的操作数出现时，只保留其括号占位，
//     由前缀直接短路成假，不再参与类型检查。
const (
	opencodeGoUsageMountPlatformsSQL = "NULL"
	opencodeGoBaseURLMatchSQLPrefix  = "FALSE OR (NULL::text IS NOT NULL) AND ("
	opencodeGoBaseURLMatchSQLSuffix  = " IS NOT NULL)"
)
