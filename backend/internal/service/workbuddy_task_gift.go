package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 新手礼包：每个账号一次，走 codebuddy 域而不是成长中心域。
//
// 端点来自 AutoBuddy 的实测实现（gateway/vendor/workbuddy_daily.py）：
//   POST https://www.codebuddy.cn/billing/meter/claim-gift {}
// 返回 {code, msg, data:{credit}}；已领取时 code != 0 且 msg 含「已领取」。
// 原 /billing/meter/claim-compensation 接口上游已下线（固定 400），不要使用。
const workBuddyCodeBuddyBase = "https://www.codebuddy.cn"

func workBuddyGiftClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

// workBuddyClaimGift 领取新手礼包，返回获得的积分。
func workBuddyClaimGift(ctx context.Context, account *Account) (float64, error) {
	if account == nil {
		return 0, fmt.Errorf("账号为空")
	}
	token := account.workBuddyTokenOf()
	if token.Access == "" {
		return 0, fmt.Errorf("账号缺少 access_token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		workBuddyCodeBuddyBase+"/billing/meter/claim-gift", bytes.NewReader([]byte("{}")))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if uid := account.workBuddyUIDOf(); uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	req.Header.Set("X-Domain", "copilot.tencent.com")
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", "CLI/2.63.2 CodeBuddy/2.63.2")
	req.Header.Set("Origin", workBuddyCodeBuddyBase)
	req.Header.Set("Referer", workBuddyCodeBuddyBase+"/")

	resp, err := workBuddyGiftClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}

	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, fmt.Errorf("上游返回非 JSON（HTTP %d）: %s", resp.StatusCode, truncateRunes(string(raw), 160))
	}
	if env.Code != 0 {
		msg := env.Msg
		if msg == "" {
			msg = truncateRunes(string(raw), 120)
		}
		// 已领取是正常终态，交给上层显示为「已领取」，不当失败处理。
		if strings.Contains(msg, "已领") || strings.Contains(strings.ToLower(msg), "already") ||
			strings.Contains(msg, "重复领取") || strings.Contains(msg, "领取过") {
			return 0, fmt.Errorf("已领取过新手礼包")
		}
		return 0, fmt.Errorf("上游错误 %d: %s", env.Code, msg)
	}
	return workBuddyTaskFloat(env.Data, "credit"), nil
}
