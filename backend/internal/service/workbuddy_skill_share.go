package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WorkBuddy「分享技能给好友」任务的完成路径。
//
// 这个任务看起来需要真实社交行为，实际上是一条纯 API 链路：
//  1. 从技能市场取一个可分享的技能；
//  2. 调用 asset-share 接口创建分享渠道，拿到 share_code；
//  3. 上报两条 skill_share_channel_action 事件（link/click 与 link/success）。
//
// 来源：L0NE-6/WorkBuddy-Daily 的 t_skill_share 实现。

const (
	// workBuddySkillMarketURL 是技能市场列表接口（codebuddy 域）。
	workBuddySkillMarketURL = "https://www.codebuddy.cn/v2/operation-platform/market/skill/list"
	// workBuddySkillShareChannelURL 是创建分享渠道的接口（open 域）。
	workBuddySkillShareChannelURL = "https://open.workbuddy.cn/api/v2/open-platform/asset-shares/c2c/channels"
)

// workBuddySkill 是技能市场里的一个技能。
type workBuddySkill struct {
	ID          string
	Name        string
	Description string
}

// workBuddyMarketSkillOf 从技能市场取一个可分享的技能。
func workBuddyMarketSkillOf(ctx context.Context, account *Account) (*workBuddySkill, error) {
	token := account.workBuddyTokenOf()
	if strings.TrimSpace(token.Access) == "" {
		return nil, fmt.Errorf("账号缺少 access_token")
	}
	body := `{"page":1,"page_size":20}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workBuddySkillMarketURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if uid := account.workBuddyUIDOf(); uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	req.Header.Set("X-Domain", "www.codebuddy.cn")
	req.Header.Set("User-Agent", workBuddyReportUserAgent)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var env struct {
		Code int `json:"code"`
		Data struct {
			Skills []map[string]any `json:"skills"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("技能市场返回非 JSON")
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("技能市场错误 %d", env.Code)
	}
	for _, row := range env.Data.Skills {
		id := firstNonEmptyStr(
			workBuddyStringField(row, "skill_id"),
			workBuddyStringField(row, "id"),
		)
		if id == "" {
			continue
		}
		name := firstNonEmptyStr(
			workBuddyStringField(row, "display_name_zh"),
			workBuddyStringField(row, "name"),
			workBuddyStringField(row, "display_name_en"),
		)
		if name == "" {
			continue
		}
		return &workBuddySkill{
			ID:          id,
			Name:        name,
			Description: workBuddyStringField(row, "description"),
		}, nil
	}
	return nil, fmt.Errorf("技能市场没有可分享的技能")
}

// workBuddyCreateSkillShareChannel 创建分享渠道，返回 share_code。
func workBuddyCreateSkillShareChannel(ctx context.Context, account *Account, skill *workBuddySkill) (string, error) {
	token := account.workBuddyTokenOf()
	assetRef, _ := json.Marshal(map[string]string{"asset_id": skill.ID})
	body, _ := json.Marshal(map[string]any{
		"asset_kind":   "skill",
		"asset_source": "market",
		"asset_ref":    string(assetRef),
		"ways":         []string{"link", "wxacode", "wxapath"},
		"extra": map[string]any{
			"name":              skill.Name,
			"description":       skill.Description,
			"shareKey":          skill.ID,
			"marketplaceSource": "builtin-market",
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		workBuddySkillShareChannelURL, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if uid := account.workBuddyUIDOf(); uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	req.Header.Set("X-Domain", "copilot.tencent.com")
	req.Header.Set("User-Agent", workBuddyReportUserAgent)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("创建分享渠道失败（HTTP %d）", resp.StatusCode)
	}

	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ShareCode string `json:"share_code"`
			ChannelID string `json:"channel_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", fmt.Errorf("分享接口返回非 JSON")
	}
	if env.Code != 0 {
		return "", fmt.Errorf("分享接口错误 %d: %s", env.Code, env.Msg)
	}
	if env.Data.ShareCode == "" {
		return "", fmt.Errorf("分享接口未返回 share_code")
	}
	return env.Data.ShareCode, nil
}

// WorkBuddyRunSkillShare 执行「分享技能给好友」任务。
func WorkBuddyRunSkillShare(ctx context.Context, account *Account) error {
	skill, err := workBuddyMarketSkillOf(ctx, account)
	if err != nil {
		return err
	}
	shareCode, err := workBuddyCreateSkillShareChannel(ctx, account, skill)
	if err != nil {
		return err
	}

	// 两条事件：先 click 再 success，构成一次完整的分享动作。
	now := time.Now().UnixMilli()
	mk := func(action string) workBuddyReportEvent {
		return workBuddyReportEvent{
			Code:   "skill_share_channel_action",
			Domain: workBuddyDomainDesktop,
			Fields: map[string]any{
				"skillId":      skill.ID,
				"skillName":    skill.Name,
				"skillType":    "market",
				"action":       "link",
				"panelAction":  action,
				"panelType":    "clipboard",
				"isSuccessful": true,
				"cost":         0,
				"id":           shareCode,
				"actionAt":     now,
			},
		}
	}
	return WorkBuddyReportEvents(ctx, account, []workBuddyReportEvent{mk("click"), mk("success")})
}
