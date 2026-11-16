package service

// 腾讯 Marvis 的硬件指纹。
//
// Marvis 与 WorkBuddy 一样是客户端在真实机器上跑的产品：上游按设备识别
// 「同一台机器」，因此每个账号需要一套稳定、互不相同的硬件画像。
// 画像在账号创建时分配一次并写进 credentials，之后所有请求都用同一套 ——
// 中途换画像会被上游判为设备漂移。
//
// 与 WorkBuddy 的差别只在于哪些字段真的会发出去：
//
//	WorkBuddy  把 machine_id / MAC 等编进签名头
//	Marvis     请求头里只用到 osVersion 与 guid；uskey 每次现签
//
// 也就是说 Marvis 的画像目前主要用于「同一账号在管理端与上游看来是同一台机器」
// 以及在将来上游加严校验时有据可依。字段保持与 WorkBuddy 一致，前端同一个
// 弹窗就能完整展示。

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
)

// marvisFingerprintProfile 是一套可分配的硬件画像。
type marvisFingerprintProfile struct {
	OS         string // Win32 / MacIntel
	Arch       string // x64 / arm64
	OSVersion  string
	CPUCores   int
	MemorySize int // GB
	GPU        string
	Disk       int
	Screen     string
	CPUModel   string
}

// marvisFingerprintPool 是可分配的画像池。
//
// 覆盖 Windows 与 macOS 两族、不同核心数/内存/GPU/硬盘/屏幕组合。
// 分配器保证同一批账号里**画像组合互不相同**（见 marvisProfileKey），
// 因此在单一账号看来就是一台具体、稳定的机器。
var marvisFingerprintPool = []marvisFingerprintProfile{
	{
		OS: "Win32", Arch: "x64", OSVersion: "10.0.26100", CPUCores: 16, MemorySize: 32,
		GPU: "NVIDIA GeForce RTX 4070", Disk: 1024, Screen: "2560x1440",
		CPUModel: "Intel(R) Core(TM) i7-13700K",
	},
	{
		OS: "Win32", Arch: "x64", OSVersion: "10.0.22631", CPUCores: 12, MemorySize: 16,
		GPU: "NVIDIA GeForce RTX 3060", Disk: 512, Screen: "1920x1080",
		CPUModel: "Intel(R) Core(TM) i5-12400F",
	},
	{
		OS: "Win32", Arch: "x64", OSVersion: "10.0.19045", CPUCores: 8, MemorySize: 16,
		GPU: "Intel(R) UHD Graphics 730", Disk: 512, Screen: "1920x1080",
		CPUModel: "Intel(R) Core(TM) i5-10400",
	},
	{
		OS: "MacIntel", Arch: "arm64", OSVersion: "14.5", CPUCores: 10, MemorySize: 18,
		GPU: "Apple M2 Pro", Disk: 512, Screen: "3024x1964",
		CPUModel: "Apple M2 Pro",
	},
	{
		OS: "MacIntel", Arch: "arm64", OSVersion: "15.0", CPUCores: 12, MemorySize: 36,
		GPU: "Apple M3 Pro", Disk: 1024, Screen: "3456x2234",
		CPUModel: "Apple M3 Pro",
	},
	{
		OS: "Win32", Arch: "x64", OSVersion: "10.0.22621", CPUCores: 20, MemorySize: 64,
		GPU: "NVIDIA GeForce RTX 4080", Disk: 2048, Screen: "3840x2160",
		CPUModel: "AMD Ryzen 9 7950X",
	},
	{
		OS: "MacIntel", Arch: "arm64", OSVersion: "14.2", CPUCores: 8, MemorySize: 16,
		GPU: "Apple M1", Disk: 256, Screen: "2560x1600",
		CPUModel: "Apple M1",
	},
	{
		OS: "Win32", Arch: "x64", OSVersion: "10.0.22635", CPUCores: 24, MemorySize: 32,
		GPU: "NVIDIA GeForce RTX 4090", Disk: 2048, Screen: "2560x1440",
		CPUModel: "Intel(R) Core(TM) i9-14900K",
	},
	{
		OS: "Win32", Arch: "x64", OSVersion: "10.0.22621", CPUCores: 6, MemorySize: 8,
		GPU: "Intel(R) UHD Graphics 630", Disk: 256, Screen: "1600x900",
		CPUModel: "Intel(R) Core(TM) i3-9100",
	},
	{
		OS: "MacIntel", Arch: "x64", OSVersion: "13.6", CPUCores: 8, MemorySize: 32,
		GPU: "AMD Radeon Pro 5500M", Disk: 1024, Screen: "3072x1920",
		CPUModel: "Intel(R) Core(TM) i9-9880H",
	},
	{
		OS: "Win32", Arch: "x64", OSVersion: "10.0.22631", CPUCores: 14, MemorySize: 24,
		GPU: "NVIDIA GeForce RTX 4060", Disk: 1024, Screen: "2560x1600",
		CPUModel: "AMD Ryzen 7 7745HX",
	},
	{
		OS: "MacIntel", Arch: "arm64", OSVersion: "15.1", CPUCores: 14, MemorySize: 48,
		GPU: "Apple M4 Pro", Disk: 2048, Screen: "3456x2234",
		CPUModel: "Apple M4 Pro",
	},
}

// marvisProfileKey 是画像去重键（除 UA 外的全部硬件特征组合）。
func marvisProfileKey(p marvisFingerprintProfile) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d|%s|%d|%s",
		p.OS, p.Arch, p.OSVersion, p.CPUModel, p.CPUCores, p.MemorySize,
		p.GPU, p.Disk, p.Screen)
}

// marvisFingerprintMaxSalt 是撞车时的重试上限。
const marvisFingerprintMaxSalt = 1000

// marvisFingerprint 是写入账号凭据的设备画像。
//
// 字段与 WorkBuddy 保持同名同义，管理端同一个弹窗即可完整展示。
type marvisFingerprint struct {
	UID        string `json:"uid"`
	MachineID  string `json:"machine_id"`
	SessionID  string `json:"session_id"`
	MAC        string `json:"mac"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	OSVersion  string `json:"os_version"`
	CPUCores   int    `json:"cpu_cores"`
	MemorySize int    `json:"memory_size"`
	GPU        string `json:"gpu"`
	Disk       int    `json:"disk"`
	Screen     string `json:"screen"`
	CPUModel   string `json:"cpu_model"`
	// UA 是这台机器在浏览器侧的标识。推理端点实测不校验它（任何 UA 都 200），
	// 但把画像与 UA 保持一致才是「一台具体机器」的完整描述。
	UA string `json:"ua"`
}

// marvisFingerprintSalt 使 Marvis 与 WorkBuddy 在同一 uid 上得到不同画像，
// 避免两个平台共用一台「机器」的特征。
const marvisFingerprintSalt = "marvis/"

// marvisHashHex 返回 salt+key 的 SHA-1 十六进制串。
func marvisHashHex(key string) string {
	h := sha1.Sum([]byte(marvisFingerprintSalt + key))
	return hex.EncodeToString(h[:])
}

// marvisUUIDv5 按 RFC 4122 计算 UUIDv5（与 WorkBuddy 同样的手写实现）。
func marvisUUIDv5(seed string) string {
	h := sha1.Sum([]byte(marvisFingerprintSalt + seed))
	h[6] = (h[6] & 0x0f) | 0x50 // version 5
	h[8] = (h[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(h[0:4]),
		hex.EncodeToString(h[4:6]),
		hex.EncodeToString(h[6:8]),
		hex.EncodeToString(h[8:10]),
		hex.EncodeToString(h[10:16]),
	)
}

// marvisFingerprintAllocator 为一批账号分配互不冲突的指纹。
//
// 与 WorkBuddy 的分配器同一套思路：
//   - 画像按账号 key 派生的起点轮转选取，撞车就往后找下一个未占用的
//   - machineId / MAC 也登记去重，撞了就加盐重算
//
// 「同一批里互不相同」是关键：多个账号在上游看来必须是不同物理机器，
// 否则设备维度会把它们关联到一起。
type marvisFingerprintAllocator struct {
	usedMachine  map[string]bool
	usedMAC      map[string]bool
	usedProfiles map[string]bool
}

func newMarvisFingerprintAllocator() *marvisFingerprintAllocator {
	return &marvisFingerprintAllocator{
		usedMachine:  make(map[string]bool),
		usedMAC:      make(map[string]bool),
		usedProfiles: make(map[string]bool),
	}
}

// marvisAssign 为 key 分配一套指纹，并登记到已占用集合。
func (a *marvisFingerprintAllocator) marvisAssign(key string) marvisFingerprint {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "default"
	}

	// machineId：确定派生 + 撞车加盐。
	machineID := marvisUUIDv5("machine/" + key)
	salt := 0
	for a.usedMachine[machineID] && salt < marvisFingerprintMaxSalt {
		salt++
		machineID = marvisUUIDv5(fmt.Sprintf("machine/%s#%d", key, salt))
	}
	a.usedMachine[machineID] = true

	sessionID := marvisUUIDv5(fmt.Sprintf("session/%s#%d", key, salt))

	// MAC：确定派生 + 撞车加盐。
	mac := marvisMacFromKey(key)
	macSalt := 0
	for a.usedMAC[mac] && macSalt < marvisFingerprintMaxSalt {
		macSalt++
		mac = marvisMacFromKey(fmt.Sprintf("%s#%d", key, macSalt))
	}
	a.usedMAC[mac] = true

	// 画像：从 key 派生的起点开始轮转，取第一个没被占用的组合。
	profile := marvisAllocateProfile(key, a.usedProfiles)
	a.usedProfiles[marvisProfileKey(profile)] = true

	digest := marvisHashHex(key)
	return marvisFingerprint{
		UID:        digest[0:16],
		MachineID:  machineID,
		SessionID:  sessionID,
		MAC:        mac,
		OS:         profile.OS,
		Arch:       profile.Arch,
		OSVersion:  profile.OSVersion,
		CPUCores:   profile.CPUCores,
		MemorySize: profile.MemorySize,
		GPU:        profile.GPU,
		Disk:       profile.Disk,
		Screen:     profile.Screen,
		CPUModel:   profile.CPUModel,
		UA:         marvisUAForProfile(profile),
	}
}

// marvisUAForProfile 按画像的操作系统生成对应的浏览器 UA。
func marvisUAForProfile(p marvisFingerprintProfile) string {
	if p.OS == "MacIntel" {
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	}
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
}

// marvisAllocateProfile 从未占用的画像里挑一个。
func marvisAllocateProfile(key string, usedProfiles map[string]bool) marvisFingerprintProfile {
	digest := marvisHashHex(key)
	start := int(digest[0]) % len(marvisFingerprintPool)
	n := len(marvisFingerprintPool)
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		cand := marvisFingerprintPool[idx]
		if !usedProfiles[marvisProfileKey(cand)] {
			return cand
		}
	}
	// 池已占满：允许组合复用，但仍从起点连续取，行为可预测。
	return marvisFingerprintPool[start]
}

// marvisFingerprintFor 单账号场景：等价于新建一个空分配器再分配。
func marvisFingerprintFor(key string) marvisFingerprint {
	return newMarvisFingerprintAllocator().marvisAssign(key)
}

// marvisMacFromKey 从 key 派生一个合法的单播 MAC 地址。
func marvisMacFromKey(key string) string {
	return marvisMacFromDigest(marvisHashHex("mac/" + key))
}

// marvisMacFromDigest 从摘要派生一个合法的单播 MAC 地址。
//
// 第二字节的低两位清零，确保是本地管理的单播地址，不会撞上真实厂商前缀。
func marvisMacFromDigest(digest string) string {
	if len(digest) < 12 {
		return "02:00:00:00:00:01"
	}
	b := []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x00}
	for i := 0; i < 6; i++ {
		var v byte
		_, _ = fmt.Sscanf(digest[i*2:i*2+2], "%02x", &v)
		b[i] = v
	}
	b[0] = (b[0] | 0x02) &^ 0x01 // 本地管理、单播
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

// marvisFingerprintToMap 把画像转成凭据里存的结构。
func marvisFingerprintToMap(fp marvisFingerprint) map[string]any {
	return map[string]any{
		"uid":         fp.UID,
		"machine_id":  fp.MachineID,
		"session_id":  fp.SessionID,
		"mac":         fp.MAC,
		"os":          fp.OS,
		"arch":        fp.Arch,
		"os_version":  fp.OSVersion,
		"cpu_cores":   fp.CPUCores,
		"memory_size": fp.MemorySize,
		"gpu":         fp.GPU,
		"disk":        fp.Disk,
		"screen":      fp.Screen,
		"cpu_model":   fp.CPUModel,
		"ua":          fp.UA,
	}
}

// marvisFingerprintOfCredentials 从凭据里读回画像；缺失返回 nil。
func marvisFingerprintOfCredentials(creds map[string]any) map[string]any {
	if creds == nil {
		return nil
	}
	if m, ok := creds["fingerprint"].(map[string]any); ok && len(m) > 0 {
		return m
	}
	return nil
}

// ensureMarvisFingerprintCredentials 在凭据里补齐硬件指纹。
//
// 已有 machine_id 时保留原值 —— 指纹一旦分配就不能变，
// 变了上游会把同一账号当成换了机器。
// marvisAssignFingerprintConsidering 给新账号分配指纹时避开已有账号已用的组合。
//
// existing 是库里其它 Marvis 账号的 credentials，用来预先占位，
// 保证新建账号拿到的是「这台机器还没人用过」的画像。
func marvisAssignFingerprintConsidering(creds map[string]any, existing []map[string]any) map[string]any {
	alloc := newMarvisFingerprintAllocator()
	for _, other := range existing {
		if fp := marvisFingerprintOfCredentials(other); fp != nil {
			if s, _ := fp["machine_id"].(string); s != "" {
				alloc.usedMachine[s] = true
			}
			if s, _ := fp["mac"].(string); s != "" {
				alloc.usedMAC[s] = true
			}
			alloc.usedProfiles[fmt.Sprintf("%v|%v|%v|%v|%v|%v|%v|%v|%v",
				fp["os"], fp["arch"], fp["os_version"], fp["cpu_model"],
				fp["cpu_cores"], fp["memory_size"], fp["gpu"], fp["disk"], fp["screen"])] = true
		}
	}
	key := marvisAccountSeed(creds)
	fp := alloc.marvisAssign(key)
	creds["fingerprint"] = marvisFingerprintToMap(fp)
	creds["os_version"] = fp.OSVersion
	return creds
}

func ensureMarvisFingerprintCredentials(creds map[string]any) map[string]any {
	if creds == nil {
		creds = map[string]any{}
	}
	if existing := marvisFingerprintOfCredentials(creds); existing != nil {
		if s, _ := existing["machine_id"].(string); strings.TrimSpace(s) != "" {
			return creds
		}
	}
	// 以 openid 为种子：同一账号重新登录仍拿到同一套画像。
	key := ""
	if s, ok := creds["openid"].(string); ok {
		key = strings.TrimSpace(s)
	}
	if key == "" {
		if s, ok := creds["guid"].(string); ok {
			key = strings.TrimSpace(s)
		}
	}
	fp := marvisFingerprintFor(key)
	creds["fingerprint"] = marvisFingerprintToMap(fp)
	// 请求头里的 osVersion 也随之固定，保证 MarvisExt 与画像一致。
	creds["os_version"] = fp.OSVersion
	return creds
}

// MarvisFingerprintView 返回管理端指纹弹窗所需的数据。
//
// 形状与 WorkBuddy 的 WorkBuddyFingerprintOf 对齐，前端同一个组件可直接渲染。
func MarvisFingerprintView(account *Account) map[string]any {
	out := map[string]any{}
	if account == nil {
		return out
	}
	creds := account.Credentials
	fp := marvisFingerprintOfCredentials(creds)
	if fp == nil {
		// 老账号没有画像时按现有凭据补齐一份（不落库，只用于展示）。
		fp = marvisFingerprintToMap(marvisFingerprintFor(marvisAccountSeed(creds)))
	}

	str := func(k string) string {
		if v, ok := fp[k].(string); ok {
			return v
		}
		return ""
	}
	num := func(k string) int {
		switch v := fp[k].(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
		return 0
	}

	credsView := marvisCredentialsOf(account)
	out["account_id"] = account.ID
	out["name"] = account.Name
	out["platform"] = account.Platform
	out["uid"] = str("uid")
	out["machine_id"] = str("machine_id")
	out["session_id"] = str("session_id")
	out["mac"] = str("mac")
	out["os"] = str("os")
	out["arch"] = str("arch")
	out["os_version"] = str("os_version")
	out["cpu_cores"] = num("cpu_cores")
	out["memory_size"] = num("memory_size")
	out["gpu"] = str("gpu")
	out["disk"] = num("disk")
	out["screen"] = str("screen")
	out["cpu_model"] = str("cpu_model")
	// Marvis 实际发出去的身份
	out["guid"] = credsView.GUID
	out["openid"] = credsView.OpenID
	out["login_type"] = credsView.LoginType
	// UA 取画像里的那台机器；画像缺失的老账号退回协议 UA。
	out["ua"] = MarvisClientUA
	if ua := str("ua"); ua != "" {
		out["ua"] = ua
	}
	out["ide_version"] = MarvisClientVersion
	out["persisted"] = marvisFingerprintOfCredentials(creds) != nil
	return out
}

// marvisAccountSeed 给老账号派生画像时用的种子。
// marvisAccountSeed 返回账号指纹的派生种子。
//
// 优先用 openid：它是上游的账号唯一标识，同一账号重新登录仍然拿到同一套
// 画像。没有 openid 时退到 guid。
//
// ⚠ 两者都缺时**不能**退到一个固定串 —— 那样所有缺凭据的账号会拿到同一
// 台「机器」。这里的最后手段是随机值：宁可同一账号两次分配得到不同画像
// （密码学意义上的碰撞概率可忽略），也不能让不同账号共用一台机器。
func marvisAccountSeed(creds map[string]any) string {
	if creds != nil {
		for _, k := range []string{"openid", "guid"} {
			if s, ok := creds[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return "anon-" + marvisRandomHex(16)
}

// marvisAccountSeedWithID 是分配指纹时用的种子，账号已有 ID 时把 ID 一起
// 掺进去。
//
// 账号 ID 天然唯一，掺进种子后即使两个账号的 openid/guid 完全缺失，也
// 一定得到不同的画像。ID 为 0（尚未入库）时退化为纯凭据种子。
func marvisAccountSeedWithID(creds map[string]any, accountID int64) string {
	base := marvisAccountSeed(creds)
	if accountID > 0 {
		return fmt.Sprintf("%s#%d", base, accountID)
	}
	return base
}
