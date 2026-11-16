package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Trae 登录服务的对外接口（管理端直接调用）。
//
// 内部实现在 trae_oauth.go / trae_api.go，这里只做导出与错误收敛，
// 与 WorkBuddy / Marvis 的服务层形状保持一致。

// TraeAuthInstruction 是生成授权链接的返回。
type TraeAuthInstruction = traeAuthInstruction

// TraeModel 是模型目录里的一条（导出别名）。
type TraeModel = traeModel

// TraeAuthResult 是一次完成的授权。
type TraeAuthResult struct {
	// Platform 回显站点。
	Platform string `json:"platform"`
	// Credentials 可直接提交给账号创建接口。
	Credentials map[string]any `json:"credentials"`
	// UID / Name 用于账号命名与展示。
	UID  string `json:"uid,omitempty"`
	Name string `json:"name,omitempty"`
	// Region 是国际站的部署区域（us / sg），国内站为空。
	Region string `json:"region,omitempty"`
	// AuthHost 是登录态声明的续期主机（企业账号可能不同于默认主机）。
	AuthHost string `json:"auth_host,omitempty"`
}

// TraeOAuthService 是 Trae 的登录与账号能力。
type TraeOAuthService struct{}

// NewTraeOAuthService 构造服务。
func NewTraeOAuthService() *TraeOAuthService { return &TraeOAuthService{} }

// StartAuth 生成授权链接，并在服务端暂存该账号的设备身份。
func (s *TraeOAuthService) StartAuth(platform string) (*TraeAuthInstruction, error) {
	return startTraeAuthorization(platform)
}

// CompleteAuth 用粘贴回来的回调地址完成授权。
//
// 设备由服务端按 state 取出，不经过浏览器；用户只需把地址栏内容原样贴回。
func (s *TraeOAuthService) CompleteAuth(state, callbackURL string) (*TraeAuthResult, error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil, errors.New("trae: state 为空，请重新生成授权链接")
	}
	session, ok := traeAuthStore.take(state)
	if !ok {
		return nil, errors.New("trae: 授权会话已过期，请重新生成链接")
	}
	parsed, err := parseTraeCallback(callbackURL)
	if err != nil {
		return nil, err
	}
	return &TraeAuthResult{
		Platform:    session.Platform,
		Credentials: parsed.credentials(session.Platform, session.Device),
		UID:         parsed.UID,
		Name:        parsed.Name,
		Region:      parsed.Region,
		AuthHost:    parsed.AuthHost,
	}, nil
}

// Models 拉取账号的模型目录。
func (s *TraeOAuthService) Models(ctx context.Context, account *Account) ([]TraeModel, error) {
	site, token, err := traeAccountContext(account)
	if err != nil {
		return nil, err
	}
	return fetchTraeModels(ctx, site, token)
}

// Credits 读取账号额度（原生单位，展示层再折算成积分）。
func (s *TraeOAuthService) Credits(ctx context.Context, account *Account) (*TraeCredits, error) {
	site, token, err := traeAccountContext(account)
	if err != nil {
		return nil, err
	}
	return fetchTraeCredits(ctx, site, token)
}

// Refresh 用 refresh token 续期，返回可直接写回账号的凭据。
func (s *TraeOAuthService) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	site, token, err := traeAccountContext(account)
	if err != nil {
		return nil, err
	}
	result, err := traeExchangeToken(ctx, traeClient, site, token)
	if err != nil {
		return nil, err
	}
	creds := map[string]any{}
	for key, value := range account.Credentials {
		creds[key] = value
	}
	creds["access_token"] = result.Access
	if result.Refresh != "" {
		creds["refresh_token"] = result.Refresh
	}
	if !result.ExpiresAt.IsZero() {
		creds["expires_at"] = result.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
		creds["expiresAt"] = result.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if result.UID != "" {
		creds["uid"] = result.UID
	}
	if result.Region != "" {
		creds["region"] = result.Region
	}
	return creds, nil
}

// RotateDevice 为账号换一套设备身份（换完必须写回凭据，否则下次请求带不上）。
func (s *TraeOAuthService) RotateDevice(account *Account) (map[string]any, error) {
	site := traeSiteForPlatform(account.Platform)
	if site == nil {
		return nil, fmt.Errorf("trae: 不支持的平台 %q", account.Platform)
	}
	device, err := newTraeDevice(site)
	if err != nil {
		return nil, err
	}
	creds := map[string]any{}
	for key, value := range account.Credentials {
		creds[key] = value
	}
	creds["device_id"] = device.DeviceID
	creds["machine_id"] = device.MachineID
	creds["device_brand"] = device.DeviceBrand
	creds["device_type"] = device.DeviceType
	creds["os_version"] = device.OSVersion
	creds["device_private_key"] = device.PrivateKeyPEM
	creds["device_public_key"] = device.PublicKeyPEM
	return creds, nil
}

// DeviceInfo 返回账号当前设备的可展示字段（不含私钥）。
func (s *TraeOAuthService) DeviceInfo(account *Account) map[string]any {
	token := traeTokenOf(account.Credentials)
	if token.Device == nil {
		return nil
	}
	return map[string]any{
		"device_id":    token.Device.DeviceID,
		"machine_id":   token.Device.MachineID,
		"device_brand": token.Device.DeviceBrand,
		"device_type":  token.Device.DeviceType,
		"os_version":   token.Device.OSVersion,
	}
}

// traeAccountContext 取出账号的站点与登录态。
func traeAccountContext(account *Account) (*traeSite, traeToken, error) {
	if account == nil {
		return nil, traeToken{}, errors.New("trae: 账号为空")
	}
	site := traeSiteForPlatform(account.Platform)
	if site == nil {
		return nil, traeToken{}, fmt.Errorf("trae: 不支持的平台 %q", account.Platform)
	}
	token := traeTokenOf(account.Credentials)
	if token.Access == "" && token.Refresh == "" {
		return nil, traeToken{}, errors.New("trae: 账号缺少登录态，请重新授权")
	}
	return site, token, nil
}
