package service

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// WorkBuddy 账号硬件指纹。
//
// 移植自 AutoBuddy 的 gateway/fingerprint.py。
//
// 上游会按账号维度校验一组设备标识（machineId / sessionId / MAC / 硬件档位
// / UA）。同一账号多次登录必须给出一致的指纹，否则会被判定为设备漂移；
// 不同账号之间又必须互不相同，否则会被判定为同一设备多开。
//
// 因此这里用「确定派生 + 池化去重」两层策略：
//   - machineId / sessionId / mpMachineId / webMachineId：由 uid 经 UUIDv5
//     确定派生，同一账号恒定；撞车时加盐重派生。
//   - MAC：由 uid 经 SHA-256 派生并强制为本地管理单播地址（02:xx），
//     天然避免与真实厂商前缀冲突。
//   - 硬件档位与 UA：从固定画像池中按 key 稳定分配，并在池内去重。

// workBuddyFingerprintNamespace 是 UUIDv5 的命名空间。
//
// 取值与 AutoBuddy 一致（RFC 4122 的 DNS 命名空间），保证同一 uid 在两个
// 实现下派生出相同的 machineId —— 从 AutoBuddy 迁移过来的账号无需重新登录。
const workBuddyFingerprintNamespace = "6ba7b811-9dad-11d1-80b4-00c04fd430c8"

// workBuddyFingerprintMaxSalt 是撞车重派生时的最大尝试次数。
const workBuddyFingerprintMaxSalt = 64

// workBuddyFingerprintProfile 是一个硬件画像档位。
type workBuddyFingerprintProfile struct {
	OS         string // Win32 / MacIntel
	Arch       string // x64 / arm64
	OSVersion  string
	CPUCores   int
	MemorySize int // GB
	UA         string
	IDEVersion string
	// GPU / Disk / Screen / CPUModel 只用于管理端展示，不参与签名。
	// 留空时按 OS 套用该族的典型值（见 workBuddyProfileExtras）。
	GPU      string
	Disk     int
	Screen   string
	CPUModel string
	// 指纹扩维：主机名 / 时区 / 语言区域 / 字体集 / WebGL（仅展示与唯一性）。
	Hostname      string
	Timezone      string
	Locale        string
	Fonts         []string
	WebGLVendor   string
	WebGLRenderer string
}

// workBuddyFingerprint 是一个账号的设备画像。
type workBuddyFingerprint struct {
	UID          string `json:"uid"`
	MachineID    string `json:"machine_id"`
	SessionID    string `json:"session_id"`
	MPMachineID  string `json:"mp_machine_id"`
	WebMachineID string `json:"web_machine_id"`
	MAC          string `json:"mac"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	OSVersion    string `json:"os_version"`
	CPUCores     int    `json:"cpu_cores"`
	MemorySize   int    `json:"memory_size"`
	UA           string `json:"ua"`
	IDEVersion   string `json:"ide_version"`
	// 以下三项不参与签名，只用于管理端展示完整的设备画像
	// （与运营后台的「硬件指纹」弹窗字段对齐）。
	GPU    string `json:"gpu"`
	Disk   int    `json:"disk"`
	Screen string `json:"screen"`
	// CPUModel 是 CPU 型号（如 "Apple M4"），与管理端显示格式对齐。
	CPUModel string `json:"cpu_model"`
	// 指纹扩维：主机名 / 时区 / 语言区域 / 字体集 / WebGL，仅用于展示与唯一性。
	Hostname      string   `json:"hostname"`
	Timezone      string   `json:"timezone"`
	Locale        string   `json:"locale"`
	Fonts         []string `json:"fonts"`
	WebGLVendor   string   `json:"webgl_vendor"`
	WebGLRenderer string   `json:"webgl_renderer"`
}

// workBuddyUUIDv5 按 RFC 4122 计算 UUIDv5。
//
// Go 标准库没有内置 UUID，这里手写实现：取 SHA-1 前 16 字节，
// 设置版本位（5）与变体位（RFC 4122），再格式化为 8-4-4-4-12。
func workBuddyUUIDv5(namespace, name string) string {
	ns := workBuddyParseUUID(namespace)
	// UUIDv5 = SHA1(namespace_bytes || name_bytes)
	h := sha1Sum(append(ns, []byte(name)...))
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

// workBuddyParseUUID 把 8-4-4-4-12 形式的 UUID 解析为 16 字节。
func workBuddyParseUUID(s string) []byte {
	clean := strings.ReplaceAll(s, "-", "")
	out := make([]byte, 16)
	for i := 0; i < 16 && i*2+1 < len(clean); i++ {
		b, _ := strconv.ParseUint(clean[i*2:i*2+2], 16, 8)
		out[i] = byte(b)
	}
	return out
}

// sha1Sum 计算 SHA-1 摘要。
func sha1Sum(b []byte) []byte {
	sum := sha1.Sum(b)
	return sum[:]
}

// workBuddyStableMAC 由账号 key 稳定派生本地管理单播 MAC。
//
// 第一个字节强制为 0x02（本地管理 + 单播），避免与任何真实厂商 OUI 冲突；
// 同一 key 每次得到同一 MAC，满足上游对设备稳定性的要求。
func workBuddyStableMAC(key string) string {
	sum := sha256.Sum256([]byte("wb-mac:" + key))
	b := sum[:6]
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		b[0]&0xfe|0x02, b[1], b[2], b[3], b[4], b[5])
}

// workBuddyProfileKey 是画像的去重键（除 UA 外的硬件特征组合）。
func workBuddyProfileKey(p workBuddyFingerprintProfile) string {
	// 组合键必须覆盖所有会出现在管理端的维度，否则「CPU 型号/GPU/硬盘/屏幕
	// 不同但核心与内存相同」的两台设备会被判为重复，去重表把大部分组合
	// 提前淘汰掉，实际可用空间远小于维度相乘的理论值。
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d|%s|%d|%s",
		p.OS, p.Arch, p.OSVersion, p.CPUModel, p.CPUCores, p.MemorySize,
		p.GPU, p.Disk, p.Screen)
}

// workBuddyFingerprintAllocator 为一批账号分配互不冲突的指纹。
type workBuddyFingerprintAllocator struct {
	// accountID 用于登记表去重；0 表示尚未入库（此时退化为纯派生）。
	accountID    int64
	usedMachine  map[string]bool
	usedMAC      map[string]bool
	usedProfiles map[string]bool
	usedUAs      map[string]bool
}

func newWorkBuddyFingerprintAllocator() *workBuddyFingerprintAllocator {
	return &workBuddyFingerprintAllocator{
		usedMachine:  make(map[string]bool),
		usedMAC:      make(map[string]bool),
		usedProfiles: make(map[string]bool),
		usedUAs:      make(map[string]bool),
	}
}

// workBuddyAssign 为一个账号分配指纹，并登记到已占用集合。
//
// key 为空时退化为 "default"（单账号自用场景）。
func (a *workBuddyFingerprintAllocator) workBuddyAssign(key string) workBuddyFingerprint {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "default"
	}

	// machineId：确定派生 + 撞车加盐。
	machineID := workBuddyUUIDv5(workBuddyFingerprintNamespace, "wb-machine:"+key)
	salt := 0
	for a.usedMachine[machineID] && salt < workBuddyFingerprintMaxSalt {
		salt++
		machineID = workBuddyUUIDv5(workBuddyFingerprintNamespace,
			fmt.Sprintf("wb-machine:%s#%d", key, salt))
	}

	sessionID := workBuddyUUIDv5(workBuddyFingerprintNamespace,
		fmt.Sprintf("wb-session:%s#%d", key, salt))
	mpMachineID := workBuddyUUIDv5(workBuddyFingerprintNamespace,
		fmt.Sprintf("wb-mp-machine:%s#%d", key, salt))
	webMachineID := workBuddyUUIDv5(workBuddyFingerprintNamespace,
		fmt.Sprintf("wb-web-machine:%s#%d", key, salt))

	// MAC：稳定派生 + 撞车换盐。
	mac := workBuddyStableMAC(key)
	macSalt := 0
	for a.usedMAC[mac] && macSalt < workBuddyFingerprintMaxSalt {
		macSalt++
		mac = workBuddyStableMAC(fmt.Sprintf("%s#%d", key, macSalt))
	}

	// 画像由维度组合派生（见 workbuddy_fingerprint_dims.go）：
	// 组合空间数万量级，账号再多也不会撞号，且每个组合都是自洽的真实设备。
	profile, profileSalt := workBuddyAssignUniqueProfile(context.Background(), a.accountID, key)
	_ = profileSalt
	gpu, disk, screen, cpuModel := workBuddyProfileExtras(profile)

	a.usedMachine[machineID] = true
	a.usedMAC[mac] = true
	a.usedProfiles[workBuddyProfileKey(profile)] = true
	a.usedUAs[profile.UA] = true

	return workBuddyFingerprint{
		UID:           key,
		MachineID:     machineID,
		SessionID:     sessionID,
		MPMachineID:   mpMachineID,
		WebMachineID:  webMachineID,
		MAC:           mac,
		OS:            profile.OS,
		Arch:          profile.Arch,
		OSVersion:     profile.OSVersion,
		CPUCores:      profile.CPUCores,
		MemorySize:    profile.MemorySize,
		UA:            profile.UA,
		IDEVersion:    profile.IDEVersion,
		GPU:           gpu,
		Disk:          disk,
		Screen:        screen,
		CPUModel:      cpuModel,
		Hostname:      profile.Hostname,
		Timezone:      profile.Timezone,
		Locale:        profile.Locale,
		Fonts:         profile.Fonts,
		WebGLVendor:   profile.WebGLVendor,
		WebGLRenderer: profile.WebGLRenderer,
	}
}

// workBuddyFingerprintForAccount 为单个账号生成指纹。
//
// 入口函数：uid 优先，退化为账号 ID。同一账号每次调用返回完全相同的值。
func workBuddyFingerprintForAccount(uid string) workBuddyFingerprint {
	return newWorkBuddyFingerprintAllocator().workBuddyAssign(uid)
}

// workBuddyFingerprintHeaders 把指纹转换成 WorkBuddy 客户端发送的设备头。
//
// 这些头与 workbuddy_sign.go 的签名头一起发送；上游用它们判断设备一致性。
func (f workBuddyFingerprint) workBuddyFingerprintHeaders() map[string]string {
	return map[string]string{
		"X-Machine-Id":  f.MachineID,
		"X-Session-Id":  f.SessionID,
		"X-Device-Mac":  f.MAC,
		"X-OS":          f.OS,
		"X-Arch":        f.Arch,
		"X-OS-Version":  f.OSVersion,
		"X-CPU-Cores":   strconv.Itoa(f.CPUCores),
		"X-Memory-Size": strconv.Itoa(f.MemorySize),
	}
}

// workBuddyUASuffixPattern 匹配 UA 末尾的 Chrome 版本号，供版本递增使用。
var workBuddyUASuffixPattern = regexp.MustCompile(`Chrome/(\d+)\.0\.0\.0`)

// WorkBuddyFingerprintOf 返回账号指纹的对外视图。
//
// 同时标注指纹是否已固化在凭据里：未固化时每次按 uid 派生，值本身稳定，
// 但提示运维「这个账号还没走过一次创建流程」。
func WorkBuddyFingerprintOf(account *Account) map[string]any {
	out := map[string]any{}
	if account == nil {
		return out
	}
	fp := account.workBuddyFingerprint()

	persisted := false
	if account.Credentials != nil {
		if raw, ok := account.Credentials["fingerprint"]; ok {
			if m, ok := raw.(map[string]any); ok && workBuddyStringField(m, "machine_id") != "" {
				persisted = true
			}
		}
	}

	return map[string]any{
		"account_id":     account.ID,
		"name":           account.Name,
		"platform":       account.Platform,
		"uid":            fp.UID,
		"machine_id":     fp.MachineID,
		"session_id":     fp.SessionID,
		"mp_machine_id":  fp.MPMachineID,
		"web_machine_id": fp.WebMachineID,
		"mac":            fp.MAC,
		"os":             fp.OS,
		"arch":           fp.Arch,
		"os_version":     fp.OSVersion,
		"cpu_cores":      fp.CPUCores,
		"memory_size":    fp.MemorySize,
		"ua":             fp.UA,
		"ide_version":    fp.IDEVersion,
		"gpu":            workBuddyFingerprintGPU(fp),
		"disk":           workBuddyFingerprintDisk(fp),
		"screen":         workBuddyFingerprintScreen(fp),
		"cpu_model":      workBuddyFingerprintCPUModel(fp),
		"hostname":       fp.Hostname,
		"timezone":       fp.Timezone,
		"locale":         fp.Locale,
		"fonts":          fp.Fonts,
		"webgl_vendor":   fp.WebGLVendor,
		"webgl_renderer": fp.WebGLRenderer,
		"persisted":      persisted,
	}
}

// workBuddyProfileExtras 返回画像在某个 OS 族下的展示型硬件字段。
//
// 这些值不参与请求签名，只是让管理端的「硬件指纹」页面呈现一台完整设备，
// 避免出现大片「—」。取值按 OS 族给典型配置，同一档位的账号看到的一致。
func workBuddyProfileExtras(profile workBuddyFingerprintProfile) (gpu string, disk int, screen string, cpuModel string) {
	if profile.GPU != "" || profile.Disk != 0 || profile.Screen != "" {
		return profile.GPU, profile.Disk, profile.Screen, profile.CPUModel
	}
	if profile.CPUModel == "" {
		switch profile.OS {
		case "MacIntel":
			profile.CPUModel = "Apple M4"
		case "Linux x86_64":
			profile.CPUModel = "AMD Ryzen 7"
		default:
			profile.CPUModel = "Intel Core i7"
		}
	}
	if profile.OS == "MacIntel" {
		// Apple Silicon：统一内存由 GPU 共享。
		switch {
		case profile.MemorySize >= 32:
			return "Apple M4 Pro / 共享内存", 1024, "3456x2234", "Apple M4 Pro"
		case profile.MemorySize >= 18:
			return "Apple M4 / 共享内存", 512, "2560x1664", "Apple M4"
		default:
			return "Apple M4 / 共享内存", 512, "2560x1600", "Apple M4"
		}
	}
	// Linux 工作站：独立显卡 + 常见分辨率。
	if profile.OS == "Linux x86_64" {
		switch {
		case profile.MemorySize >= 32:
			return "NVIDIA GeForce RTX 4070 / 12 GB", 1024, "2560x1440", profile.CPUModel
		case profile.MemorySize >= 16:
			return "NVIDIA GeForce RTX 4060 / 8 GB", 512, "1920x1080", profile.CPUModel
		default:
			return "Mesa Intel Graphics / 共享内存", 512, "1920x1080", profile.CPUModel
		}
	}

	// Windows 台式/笔记本：独立显卡 + 常见分辨率。
	switch {
	case profile.MemorySize >= 32:
		return "NVIDIA GeForce RTX 4070 / 12 GB", 1024, "2560x1440", profile.CPUModel
	case profile.MemorySize >= 16:
		return "NVIDIA GeForce RTX 4060 / 8 GB", 512, "1920x1080", profile.CPUModel
	default:
		return "Intel Iris Xe Graphics / 共享内存", 512, "1920x1080", profile.CPUModel
	}
}

// 以下三个函数为展示型字段提供兜底：旧凭据里没有固化 GPU/硬盘/屏幕，
// 直接返回空会让管理端页面出现大片「—」。按画像的 OS 族给出典型值。

func workBuddyFingerprintGPU(fp workBuddyFingerprint) string {
	if fp.GPU != "" {
		return fp.GPU
	}
	gpu, _, _, _ := workBuddyProfileExtras(workBuddyFingerprintProfile{
		OS: fp.OS, Arch: fp.Arch, OSVersion: fp.OSVersion,
		CPUCores: fp.CPUCores, MemorySize: fp.MemorySize,
	})
	return gpu
}

func workBuddyFingerprintDisk(fp workBuddyFingerprint) int {
	if fp.Disk != 0 {
		return fp.Disk
	}
	_, disk, _, _ := workBuddyProfileExtras(workBuddyFingerprintProfile{
		OS: fp.OS, Arch: fp.Arch, OSVersion: fp.OSVersion,
		CPUCores: fp.CPUCores, MemorySize: fp.MemorySize,
	})
	return disk
}

func workBuddyFingerprintScreen(fp workBuddyFingerprint) string {
	if fp.Screen != "" {
		return fp.Screen
	}
	_, _, screen, _ := workBuddyProfileExtras(workBuddyFingerprintProfile{
		OS: fp.OS, Arch: fp.Arch, OSVersion: fp.OSVersion,
		CPUCores: fp.CPUCores, MemorySize: fp.MemorySize,
	})
	return screen
}

// workBuddyFingerprintCPUModel 返回 CPU 型号，缺失时按 OS 族补默认值。
func workBuddyFingerprintCPUModel(fp workBuddyFingerprint) string {
	if fp.CPUModel != "" {
		return fp.CPUModel
	}
	_, _, _, cpuModel := workBuddyProfileExtras(workBuddyFingerprintProfile{
		OS: fp.OS, Arch: fp.Arch, OSVersion: fp.OSVersion,
		CPUCores: fp.CPUCores, MemorySize: fp.MemorySize,
	})
	return cpuModel
}
