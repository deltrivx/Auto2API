package service

// 腾讯 Marvis 的额度查询。
//
// 上游 POST /v3/marvis_get_wallet 返回令牌钱包：
//
//	{ total_tokens, used_tokens, frozen_tokens, avail_tokens, total_cost, status }
//
// 这是 /v3/* 业务接口，需要 md5 签名（算法见 marvis_protocol.go）。
// 与推理端点的鉴权方式不同：那边只要 Ual-Access-* 四件套，加了签名反而会被
// 当作用错身份而回 401。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MarvisWallet 是账号的令牌钱包。
type MarvisWallet struct {
	OpenID       string `json:"openid"`
	TotalTokens  int64  `json:"total_tokens"`
	UsedTokens   int64  `json:"used_tokens"`
	FrozenTokens int64  `json:"frozen_tokens"`
	AvailTokens  int64  `json:"avail_tokens"`
	TotalCost    int64  `json:"total_cost"`
	Status       int    `json:"status"`
}

// UsedPercent 返回已用占比（0-100）。
func (w MarvisWallet) UsedPercent() float64 {
	if w.TotalTokens <= 0 {
		return 0
	}
	return float64(w.UsedTokens) / float64(w.TotalTokens) * 100
}

// marvisInt64Field 从 JSON 里读一个可能是字符串或数字的整数。
//
// 上游把 total_tokens 这类字段发成字符串（"10000000"），
// 直接按数字解会得到 0。
func marvisInt64Field(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0
		}
		return n
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// FetchMarvisWallet 读取账号的令牌钱包。
//
// 走 /v3/* 签名路径：query 为空串、body 为 "{}"，两者都必须与签名原文一致。
func FetchMarvisWallet(ctx context.Context, account *Account) (*MarvisWallet, error) {
	if account == nil {
		return nil, fmt.Errorf("账号为空")
	}
	creds := marvisCredentialsOf(account)
	if creds.AccessToken == "" || creds.OpenID == "" {
		return nil, fmt.Errorf("账号缺少 access_token 或 openid，请重新登录")
	}

	const body = "{}"
	headers := marvisSignedHeaders(creds, body)

	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		MarvisBaseURLForAccount(account)+MarvisWalletPath, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取额度失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取额度失败（HTTP %d）: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("额度返回不是有效 JSON: %s", strings.TrimSpace(string(raw)))
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("读取额度失败（code=%d）: %s", payload.Code, payload.Msg)
	}
	d := payload.Data
	if d == nil {
		return nil, fmt.Errorf("额度返回缺少 data 字段")
	}

	w := &MarvisWallet{
		OpenID:       strings.TrimSpace(fmt.Sprint(d["openid"])),
		TotalTokens:  marvisInt64Field(d, "total_tokens"),
		UsedTokens:   marvisInt64Field(d, "used_tokens"),
		FrozenTokens: marvisInt64Field(d, "frozen_tokens"),
		AvailTokens:  marvisInt64Field(d, "avail_tokens"),
		TotalCost:    marvisInt64Field(d, "total_cost"),
		Status:       int(marvisInt64Field(d, "status")),
	}
	// 上游偶尔不给 avail_tokens，按总量减已用兜底。
	if w.AvailTokens == 0 && w.TotalTokens > 0 {
		w.AvailTokens = w.TotalTokens - w.UsedTokens
		if w.AvailTokens < 0 {
			w.AvailTokens = 0
		}
	}
	return w, nil
}
