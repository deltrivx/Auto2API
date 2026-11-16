package service

import "strings"

// Trae（字节跳动 AI IDE）的两个站点。
//
// 站点差异全部集中在这里：认证页、续期主机、模型主机、额度接口、device_id
// 形态与额度单位。其余代码按 site 取值，不再散落站点判断。
//
// 取值对照 magpie 的 Trae 提供方（@magpie-community/opencode-trae-auth 0.2.34
// 的 SITES 表）：clientVersion 写进 x-app-version / x-ide-version，appId 是
// 两站共用的 x-app-id，brand 属于设备画像。
//
// 与 WorkBuddy 的关键差异：Trae 的 device_id / machine_id / ECDSA 密钥在登录时
// 生成并绑定账号，之后每个请求和每次续期都必须用同一套设备，否则请求会被上游
// 丢弃，续期也过不了 DeviceProof 校验。
type traeSite struct {
	// ID 是平台常量（PlatformTraeCN / PlatformTraeIntl）。
	ID string
	// Name 是展示名。
	Name string
	// AuthURL 是在浏览器打开的授权页。
	AuthURL string
	// AuthHost 是续期与额度的默认主机；国际站由账号区域覆盖。
	AuthHost string
	// ModelHost 是对话主机；国际站由账号区域覆盖。
	ModelHost string
	// PayHost 是额度接口主机；国际站由账号区域覆盖。
	PayHost string
	// DeviceIDDigits 是 device_id 的位数：国内站 16 位数字，国际站 19 位。
	DeviceIDDigits int
	// NativeUnit 是该站原生的额度单位：国内站积分，国际站美元。
	NativeUnit string
	// AppID / ClientVersion / IDEVersion 参与请求头校验。
	AppID             string
	ClientVersion     string
	ClientVersionCode string
	IDEVersion        string
	IDEVersionCode    string
	PluginVersion     string
	Brand             string
}

const (
	// traeOAuthClientID 是两站共用的授权客户端（Trae IDE 自身）。
	traeOAuthClientID = "ono9krqynydwx5"
	// traeAppID 是两站共用的 x-app-id。
	traeAppID = "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8"
	// traeChatClientID 是 Cloud-IDE 续期时优先声明的客户端（SOLO PC）。
	traeChatClientID = "en1oxy7wnw8j9n"

	// traeUnitCredits 是积分单位，traeUnitUSD 是美元单位。
	traeUnitCredits = "credits"
	traeUnitUSD     = "usd"
)

// traeCN 是国内站。
var traeCN = traeSite{
	ID:                PlatformTraeCN,
	Name:              "Trae 国内版",
	AuthURL:           "https://www.trae.cn/authorization",
	AuthHost:          "https://api.trae.cn",
	ModelHost:         "https://trae-api-cn.mchost.guru",
	PayHost:           "https://api.trae.cn",
	DeviceIDDigits:    16,
	NativeUnit:        traeUnitCredits,
	AppID:             traeAppID,
	ClientVersion:     "0.1.69",
	ClientVersionCode: "20260917",
	IDEVersion:        "3.3.65",
	IDEVersionCode:    "20260401",
	PluginVersion:     "2.3.24254",
	Brand:             "ASUS TUF Gaming A15 FA507RM_FA507RM",
}

// traeIntl 是国际站（两个区域，主机由账号的 region 决定）。
var traeIntl = traeSite{
	ID:                PlatformTraeIntl,
	Name:              "Trae 国际版",
	AuthURL:           "https://www.trae.ai/authorization",
	AuthHost:          "https://growsg-normal.trae.ai",
	ModelHost:         "https://coresg-normal.trae.ai",
	PayHost:           "https://api-sg-central.trae.ai",
	DeviceIDDigits:    19,
	NativeUnit:        traeUnitUSD,
	AppID:             traeAppID,
	ClientVersion:     "0.1.69",
	ClientVersionCode: "20260917",
	IDEVersion:        "3.5.51",
	IDEVersionCode:    "20260401",
	PluginVersion:     "2.3.62834",
	Brand:             "ASUS TUF Gaming A15 FA507RM_FA507RM",
}

// traeRegion 是国际站的一个部署区域。
type traeRegion struct {
	// ModelHost 承接对话请求。
	ModelHost string
	// AuthHost 承接续期。
	AuthHost string
	// PayHost 承接额度查询。
	PayHost string
}

// traeUsRegion 是美国部署，traeSgRegion 是新加坡部署（国际站默认）。
var (
	traeUsRegion = traeRegion{
		ModelHost: "https://coreva-normal.trae.ai",
		AuthHost:  "https://api-us-east.trae.ai",
		PayHost:   "https://api-us-east.trae.ai",
	}
	traeSgRegion = traeRegion{
		ModelHost: "https://coresg-normal.trae.ai",
		AuthHost:  "https://growsg-normal.trae.ai",
		PayHost:   "https://api-sg-central.trae.ai",
	}
)

// traeRegionFor 把账号保存的 region 收敛到已知区域：US 之外一律按 SG 处理，
// 与 magpie 一致（未知区域不下发到别的部署）。
func traeRegionFor(region string) traeRegion {
	if strings.EqualFold(strings.TrimSpace(region), "us") {
		return traeUsRegion
	}
	return traeSgRegion
}

// traeSiteForPlatform 按平台常量返回站点配置。
func traeSiteForPlatform(platform string) *traeSite {
	switch platform {
	case PlatformTraeCN:
		return &traeCN
	case PlatformTraeIntl:
		return &traeIntl
	default:
		return nil
	}
}

// IsTraePlatform 报告平台是否属于 Trae（两个站点之一）。
func IsTraePlatform(platform string) bool {
	return platform == PlatformTraeCN || platform == PlatformTraeIntl
}

// isTraeAccount 报告账号是否属于 Trae。
func isTraeAccount(account *Account) bool {
	return account != nil && IsTraePlatform(account.Platform)
}

// traeHostsFor 返回该平台/区域下实际使用的主机组。
//
// 国内站只有一个部署；国际站按账号 region 选 US 或 SG。
func (s *traeSite) traeHostsFor(region string) traeRegion {
	if s == nil {
		return traeRegion{}
	}
	if s.ID == PlatformTraeCN {
		return traeRegion{ModelHost: s.ModelHost, AuthHost: s.AuthHost, PayHost: s.PayHost}
	}
	return traeRegionFor(region)
}
