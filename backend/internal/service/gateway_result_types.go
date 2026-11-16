package service

// 共享的网关结果类型。
//
// 这些类型原先定义在 Antigravity 的平台专属文件里，但被账号测试、网关转发、
// 计费等共享链路引用。Antigravity 平台移除后把它们迁到这里，避免共享代码
// 依赖已删除的平台文件。

// TestConnectionImage 是连接测试返回的图片。
type TestConnectionImage struct {
	ImageURL string
	MimeType string
}

// TestConnectionResult 是连接测试的结果。
type TestConnectionResult struct {
	Text        string                // 响应文本
	Images      []TestConnectionImage // 响应图片（图片生成模型）
	MappedModel string                // 实际使用的模型
}
