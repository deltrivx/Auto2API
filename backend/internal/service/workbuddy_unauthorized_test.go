package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/DeltrivX/Auto2API/internal/config"
)

type workBuddyUnauthorizedAccountRepo struct {
	AccountRepository
	paused bool
	failed bool
}

func (r *workBuddyUnauthorizedAccountRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.paused = true
	return nil
}

func (r *workBuddyUnauthorizedAccountRepo) SetError(_ context.Context, _ int64, _ string) error {
	r.failed = true
	return nil
}

func TestWorkBuddyUpstreamUnauthorizedHandling(t *testing.T) {
	tests := []struct {
		name            string
		headers         http.Header
		body            string
		hasRefreshToken bool
		wantPaused      bool
		wantFailed      bool
	}{
		{
			name: "APISIX HTML response",
			headers: http.Header{
				"Server":       []string{"APISIX"},
				"Content-Type": []string{"text/html"},
			},
			body:            "<html><h1>401 Authorization Required</h1><hr>openresty</html>",
			hasRefreshToken: true,
		},
		{
			name: "generic gateway JSON without refresh token",
			body: `{"message":"Authorization required"}`,
		},
		{
			name: "empty response",
		},
		{
			name: "bearer challenge with invalid token on empty body",
			headers: http.Header{
				"Www-Authenticate": []string{`Bearer error="invalid_token"`},
			},
			hasRefreshToken: true,
			wantPaused:      true,
		},
		{
			name:       "expired credential without refresh token",
			body:       `{"code":4100403,"msg":"登录态过期"}`,
			wantFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &workBuddyUnauthorizedAccountRepo{}
			gateway := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo, cfg: &config.Config{}}}
			account := &Account{ID: 1, Platform: PlatformWorkBuddyCN, Type: AccountTypeOAuth}
			if tt.hasRefreshToken {
				account.Credentials = map[string]any{"refresh_token": "test-refresh-token"}
			}
			stop := gateway.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusUnauthorized, tt.headers, []byte(tt.body), "test-model")
			if stop != (tt.wantPaused || tt.wantFailed) || repo.paused != tt.wantPaused || repo.failed != tt.wantFailed {
				t.Fatalf("stop=%t, paused=%t, failed=%t; want stop=%t, paused=%t, failed=%t", stop, repo.paused, repo.failed, tt.wantPaused || tt.wantFailed, tt.wantPaused, tt.wantFailed)
			}
			failure := gateway.failoverOpenAIUpstreamHTTPError(context.Background(), nil, account, &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     tt.headers,
			}, []byte(tt.body), "", "test-model")
			if failure == nil || failure.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected upstream 401, got %+v", failure)
			}
			if tt.wantPaused || tt.wantFailed {
				if !failure.ShouldRetryNextAccount() {
					t.Fatal("confirmed credential failure must use normal account failover")
				}
			} else if failure.ShouldRetryNextAccount() || failure.RetryableOnSameAccount || failure.ShouldReportAccountScheduleFailure() ||
				failure.Reason != WorkBuddyUpstreamUnauthorizedReason || failure.ClientStatusCode != http.StatusBadGateway {
				t.Fatalf("expected a request-scoped upstream rejection, got %+v", failure)
			}
		})
	}
}
