package service

import "context"

// 账号归属与凭证脱敏。
//
// 多用户共建账号池时，需要区分「谁能完整管理这个账号」：
//   - 创建者（以及管理员）可以编辑、删除、测试、刷新；
//   - 其他用户只能查看，且看到的是脱敏后的凭证。
//
// 这样既能让用户协作维护账号池，又不会把 access_token 泄露给所有人。

// maskedCredentialValue 是脱敏后的凭证占位值。
//
// 前端提交这个值时，后端会保留数据库里的原值——用户没改凭证，
// 不应该因为脱敏展示而把真凭证覆盖成 ***。
const maskedCredentialValue = "***"

// CanManageAccount 判断某用户能否完整管理该账号。
//
// 规则：
//   - 管理员：管理全部账号；
//   - 普通用户：管理自己创建的账号；
//   - owner_id 为 0（历史遗留、未回填）：只有管理员可动，
//     避免无主账号被任意普通用户接管。
func CanManageAccount(account *Account, userID int64, isAdmin bool) bool {
	if account == nil {
		return false
	}
	if isAdmin {
		return true
	}
	if account.OwnerID == 0 {
		return false
	}
	return account.OwnerID == userID
}

// SanitizeAccountCredentials 把凭证的值替换为占位符，保留键名。
//
// 保留键名的原因：前端需要知道"这个账号配置了哪些字段"才能渲染表单，
// 但值的真伪不应该暴露。返回的是副本，不影响数据库实体。
func SanitizeAccountCredentials(account *Account) {
	if account == nil || len(account.Credentials) == 0 {
		return
	}
	masked := make(map[string]any, len(account.Credentials))
	for k := range account.Credentials {
		masked[k] = maskedCredentialValue
	}
	account.Credentials = masked
}

// IsMaskedCredentials 判断提交的凭证是不是脱敏占位。
func IsMaskedCredentials(creds map[string]any) bool {
	if len(creds) == 0 {
		return false
	}
	for _, v := range creds {
		if s, ok := v.(string); ok && s == maskedCredentialValue {
			return true
		}
	}
	return false
}

// VisibleAccountsForUser 过滤出用户可见的账号（当前为全部）。
//
// 现阶段账号池对所有登录用户可见，因此这里只做脱敏处理；
// 若将来需要按归属隔离，改这一个函数即可。
func VisibleAccountsForUser(accounts []*Account, userID int64, isAdmin bool) []*Account {
	for _, acc := range accounts {
		if !CanManageAccount(acc, userID, isAdmin) {
			SanitizeAccountCredentials(acc)
		}
	}
	return accounts
}

// 确保 context 被使用（保留扩展点：将来可能按 ctx 取用户）。
var _ = context.Background

// MaskCredentials 返回凭证的脱敏副本（键名保留，值替换为占位符）。
//
// 与 SanitizeAccountCredentials 的区别：这个不改实体，只返回副本，
// 适合在构造响应时使用。
func MaskCredentials(creds map[string]any) map[string]any {
	if len(creds) == 0 {
		return creds
	}
	masked := make(map[string]any, len(creds))
	for k := range creds {
		masked[k] = maskedCredentialValue
	}
	return masked
}
