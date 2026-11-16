package repository

// Ollama Cloud 平台已移除，但账号列表的 SQL 仍需要这个平台过滤片段。
//
// 保留空集合形式：WHERE 条件永远不命中，等价于「没有 Ollama 账号」。
const ollamaCloudUsagePlatformsSQL = "'__none__'"

// ollamaCloudBaseURLMatchesSQL 判定账号 base_url 是否指向 Ollama 官方主机。
// 平台已移除，SQL 片段恒为 false，等价于「没有 Ollama 账号」。
func ollamaCloudBaseURLMatchesSQL(_ string) string { return "false" }
