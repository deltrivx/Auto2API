package service

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
)

// getRequestCredential 返回请求应使用的上游凭据。
//
// 原实现位于 grok_credential_failure.go，包含 Grok 专属的凭据选择与 failover
// 预算逻辑。Grok 平台移除后只剩通用路径：按账号当前 token 解析。
func (s *OpenAIGatewayService) getRequestCredential(ctx context.Context, c *gin.Context, account *Account) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if account == nil {
		return "", "", errors.New("account is nil")
	}
	return s.GetAccessToken(ctx, account)
}

// GetRequestCredential 是 getRequestCredential 的导出包装，供 handler 层调用。
func (s *OpenAIGatewayService) GetRequestCredential(ctx context.Context, c *gin.Context, account *Account) (string, string, error) {
	return s.getRequestCredential(ctx, c, account)
}

// withGrokCredentialFailureSnapshot 是已移除平台 Grok 的凭据失败快照钩子。
func withGrokCredentialFailureSnapshot(err error, _ *Account) error { return err }

// grokCredentialProxyIDsEqual 判断两个代理 ID 是否相同。
func grokCredentialProxyIDsEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// GrokCredentialMutationSnapshot 是已移除平台 Grok 的凭据变更快照。
type GrokCredentialMutationSnapshot struct {
	CredentialsJSON string
	AccessToken     string
	RefreshToken    string
	TokenVersion    int64
	ProxyID         *int64
}

// GrokCredentialReasonProxyInvalid 是已移除平台 Grok 的失败原因常量。
const GrokCredentialReasonProxyInvalid = "grok_oauth_proxy_invalid"

// GrokCredentialUnavailableClientMessage 是已移除平台 Grok 的错误文案。
const GrokCredentialUnavailableClientMessage = "grok platform has been removed"
