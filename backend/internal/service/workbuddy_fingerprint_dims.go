package service

import (
	"crypto/sha256"
	"strings"
)

// WorkBuddy 硬件画像的维度取值表。
//
// 画像不再是「若干个固定条目」，而是由各维度独立取值后组合：
// 维度独立相乘后组合空间达到数万量级，账号再多也不会撞号，同时每个组合
// 都是一台说得通的真实机器（CPU 与核心数、内存、GPU 自洽）。
//
// 所有取值来自真实在售配置：
//   - Apple Silicon 各代 M 系列的官方核心数/内存档位
//   - Intel 12/13/14 代与 AMD Zen3/Zen4 的常见零售 SKU
//   - 与 CPU 档次匹配的独立显卡
//   - 常见分辨率

// workBuddyOSFamily 是设备家族。
type workBuddyOSFamily string

const (
	workBuddyOSMac     workBuddyOSFamily = "mac"
	workBuddyOSWindows workBuddyOSFamily = "win"
	// workBuddyOSLinux 是支持 Linux 客户端的平台（部分渠道提供 Linux 构建）。
	workBuddyOSLinux workBuddyOSFamily = "linux"
)

// workBuddyCPUSpec 是一个 CPU 型号及其自洽的核心数/内存档位。
type workBuddyCPUSpec struct {
	Model string
	// Cores 是该型号的可选核心数（Apple 为统一核心数，Intel/AMD 为同档位）。
	Cores []int
	// Memory 是该档次机器的常见内存容量（GB）。
	Memory []int
	// GPU 是与该 CPU 搭配合理的显卡。
	GPU []string
	// Screens / Disks 是该机型可选的屏幕与容量；为空表示用家族默认表。
	Screens []string
	Disks   []int
}

// workBuddyMacVersions 是 macOS 的主要版本。
//
// 只列仍在支持窗口内的版本；过老版本会被上游看出异常。
var workBuddyMacVersions = []string{
	"15.5", "15.6", "26.0", "26.0.1", "26.1", "26.2",
}

// workBuddyWindowsVersions 是 Windows 10/11 的常见 build 号。
var workBuddyWindowsVersions = []string{
	"10.0.22631", // Win11 23H2
	"10.0.26100", // Win11 24H2
	"10.0.26200", // Win11 25H2
}

// workBuddyMacCPUs 是按代际排列的 Apple Silicon。
//
// 核心数与内存取自官方配置表；内存为统一内存，GPU 共享。
var workBuddyMacCPUs = []workBuddyCPUSpec{
	{Model: "Apple M1", Cores: []int{8}, Memory: []int{8, 16}, GPU: []string{"Apple M1 / 共享内存"},
		Screens: []string{"2560x1600"}, Disks: []int{256, 512, 1024, 2048}},
	{Model: "Apple M2", Cores: []int{8, 10}, Memory: []int{8, 16, 24}, GPU: []string{"Apple M2 / 共享内存"},
		Screens: []string{"2560x1664", "2560x1600", "2880x1864"}, Disks: []int{256, 512, 1024, 2048}},
	{Model: "Apple M3", Cores: []int{8, 10}, Memory: []int{8, 16, 24}, GPU: []string{"Apple M3 / 共享内存"},
		Screens: []string{"2560x1664", "2880x1864", "3024x1964"}, Disks: []int{512, 1024, 2048}},
	{Model: "Apple M3 Pro", Cores: []int{11, 12}, Memory: []int{18, 36}, GPU: []string{"Apple M3 Pro / 共享内存"},
		Screens: []string{"3024x1964", "3456x2234"}, Disks: []int{512, 1024, 2048}},
	{Model: "Apple M4", Cores: []int{8, 10}, Memory: []int{16, 24, 32}, GPU: []string{"Apple M4 / 共享内存"},
		Screens: []string{"2560x1664", "3024x1964"}, Disks: []int{256, 512, 1024}},
	{Model: "Apple M4 Pro", Cores: []int{12, 14}, Memory: []int{24, 48}, GPU: []string{"Apple M4 Pro / 共享内存"},
		Screens: []string{"3024x1964", "3456x2234"}, Disks: []int{512, 1024, 2048}},
	{Model: "Apple M4 Max", Cores: []int{14, 16}, Memory: []int{36, 64}, GPU: []string{"Apple M4 Max / 共享内存"},
		Screens: []string{"3024x1964", "3456x2234"}, Disks: []int{1024, 2048, 4096}},
}

// workBuddyWindowsCPUs 是 Intel 12/13/14 代与 AMD Zen3/Zen4 的常见零售型号。
//
// GPU 与 CPU 档次匹配：i5/R5 配中端卡，i7/R7 配 4060/4070，i9/R9 配 4070 以上。
var workBuddyWindowsCPUs = []workBuddyCPUSpec{
	{Model: "Intel Core i5-12400", Cores: []int{6}, Memory: []int{16, 32}, GPU: []string{"NVIDIA GeForce RTX 3060 / 12 GB"}},
	{Model: "Intel Core i5-13400", Cores: []int{10}, Memory: []int{16, 32}, GPU: []string{"NVIDIA GeForce RTX 3060 / 12 GB", "Intel UHD Graphics 730 / 共享内存"}},
	{Model: "Intel Core i5-14400F", Cores: []int{10}, Memory: []int{16, 32}, GPU: []string{"NVIDIA GeForce RTX 4060 / 8 GB"}},
	{Model: "Intel Core i7-12700", Cores: []int{12}, Memory: []int{16, 32}, GPU: []string{"NVIDIA GeForce RTX 3060 / 12 GB"}},
	{Model: "Intel Core i7-13700F", Cores: []int{16}, Memory: []int{32, 64}, GPU: []string{"NVIDIA GeForce RTX 4060 Ti / 8 GB"}},
	{Model: "Intel Core i7-14700F", Cores: []int{20}, Memory: []int{32, 64}, GPU: []string{"NVIDIA GeForce RTX 4070 / 12 GB"}},
	{Model: "Intel Core i9-13900K", Cores: []int{24}, Memory: []int{32, 64, 128}, GPU: []string{"NVIDIA GeForce RTX 4070 / 12 GB", "NVIDIA GeForce RTX 4080 / 16 GB", "NVIDIA GeForce RTX 4090 / 24 GB"}},
	{Model: "AMD Ryzen 5 5600X", Cores: []int{6}, Memory: []int{16, 32}, GPU: []string{"NVIDIA GeForce RTX 3060 / 12 GB", "AMD Radeon RX 6600 / 8 GB"}},
	{Model: "AMD Ryzen 7 5800X3D", Cores: []int{8}, Memory: []int{16, 32}, GPU: []string{"NVIDIA GeForce RTX 4060 / 8 GB"}},
	{Model: "AMD Ryzen 7 7800X3D", Cores: []int{8}, Memory: []int{32, 64}, GPU: []string{"NVIDIA GeForce RTX 4070 / 12 GB"}},
	{Model: "AMD Ryzen 9 5950X", Cores: []int{16}, Memory: []int{32, 64}, GPU: []string{"NVIDIA GeForce RTX 4070 / 12 GB", "AMD Radeon RX 6800 XT / 16 GB"}},
	{Model: "AMD Ryzen 9 7950X", Cores: []int{16}, Memory: []int{32, 64, 128}, GPU: []string{"NVIDIA GeForce RTX 4080 / 16 GB"}},
}

// workBuddyLinuxVersions 是 Linux 客户端常见的发行版与内核版本串。
var workBuddyLinuxVersions = []string{
	"Ubuntu 22.04", "Ubuntu 24.04", "Ubuntu 24.10",
	"Fedora 40", "Fedora 41", "Debian 12",
}

// workBuddyLinuxCPUs 是 Linux 工作站常见配置（与 Windows 同表，取其中零售型号）。
var workBuddyLinuxCPUs = []workBuddyCPUSpec{
	{Model: "AMD Ryzen 7 5800X3D", Cores: []int{8}, Memory: []int{16, 32}, GPU: []string{"NVIDIA GeForce RTX 4060 / 8 GB"}},
	{Model: "AMD Ryzen 9 5950X", Cores: []int{16}, Memory: []int{32, 64}, GPU: []string{"NVIDIA GeForce RTX 4070 / 12 GB"}},
	{Model: "AMD Ryzen 9 7950X", Cores: []int{16}, Memory: []int{32, 64, 128}, GPU: []string{"NVIDIA GeForce RTX 4080 / 16 GB"}},
	{Model: "Intel Core i7-13700F", Cores: []int{16}, Memory: []int{32, 64}, GPU: []string{"NVIDIA GeForce RTX 4060 Ti / 8 GB"}},
	{Model: "Intel Core i9-13900K", Cores: []int{24}, Memory: []int{32, 64, 128}, GPU: []string{"NVIDIA GeForce RTX 4090 / 24 GB"}},
}

// workBuddyMacDisks 是 Mac 的常见存储容量。
var workBuddyMacDisks = []int{256, 512, 1024, 2048}

// workBuddyWindowsDisks 是 Windows 的常见存储容量。
var workBuddyWindowsDisks = []int{512, 1024, 2048, 4096}

// workBuddyMacScreens 是 MacBook / iMac 的常见分辨率。
// 全部取自真实内置面板：13"/13.6"/15" Air、13"/14"/16" MacBook Pro。
// 不收录 4480x2520（Studio Display）——那是外接屏，配上笔记本机型就矛盾了。
var workBuddyMacScreens = []string{
	"2560x1600", // MacBook Air 13 / MacBook Pro 13
	"2560x1664", // MacBook Air 13.6
	"2880x1864", // MacBook Air 15
	"3024x1964", // MacBook Pro 14
	"3456x2234", // MacBook Pro 16
}

// workBuddyWindowsScreens 是常见的桌面分辨率。
var workBuddyWindowsScreens = []string{
	"1920x1080",
	"2560x1440",
	"3440x1440", // 带鱼屏
	"3840x2160",
}

// workBuddyTimezones 是系统时区。
//
// 这批账号的使用者都在国内，时区统一东八区：给一台「中国用户」的机器配上
// America/New_York 或 Europe/Berlin，是上游最容易识别的矛盾点。
var workBuddyTimezones = []string{"Asia/Shanghai"}

// workBuddyLocales 是系统语言与区域。
//
// 同样收敛到简体中文-中国大陆：语言/区域与「国内 IP + 东八区 + 中文字体」
// 必须是一套，混搭比固定值更容易被判定为伪造。
var workBuddyLocales = []string{"zh-CN"}

// 字体指纹按系统家族分开。
//
// 字体列表必须来自对应系统：macOS 不会装 Microsoft YaHei，Windows 也没有
// PingFang SC。三者都按国内常见环境挑选（中文界面 + 中文正文字体）。
var workBuddyFontSetsMac = [][]string{
	{"PingFang SC", "Hiragino Sans GB", "Helvetica Neue", "Menlo", "Monaco", "SF Pro Text"},
	{"PingFang SC", "Songti SC", "Heiti SC", "Menlo", "Monaco", "Arial"},
	{"PingFang SC", "Hiragino Sans GB", "STHeiti", "Menlo", "Monaco", "Helvetica"},
}

var workBuddyFontSetsWindows = [][]string{
	{"Microsoft YaHei", "SimSun", "Segoe UI", "Arial", "Consolas", "Calibri"},
	{"Microsoft YaHei", "Microsoft JhengHei", "Tahoma", "Verdana", "Consolas", "Segoe UI"},
}

var workBuddyFontSetsLinux = [][]string{
	{"Noto Sans CJK SC", "Noto Sans", "DejaVu Sans", "Liberation Sans", "Ubuntu Mono"},
	{"WenQuanYi Micro Hei", "Noto Sans CJK SC", "DejaVu Sans", "Liberation Serif", "DejaVu Sans Mono"},
}

// workBuddyFontSets 是各家族字体集数量的合计，仅用于组合空间估算。
var workBuddyFontSets = append(append(append([][]string{},
	workBuddyFontSetsMac...), workBuddyFontSetsWindows...), workBuddyFontSetsLinux...)

// workBuddyWebGL 是一组 WebGL vendor/renderer 字符串。
type workBuddyWebGL struct {
	Vendor   string
	Renderer string
}

// workBuddyWebGLForMac 由芯片型号推导 macOS 上的 WebGL renderer。
//
// Apple Silicon 上的 Chrome/Electron 走 ANGLE Metal，renderer 里带芯片名，
// 所以 M2 Pro 的机器不可能报出 Apple M4 —— 这里必须跟着 CPU 走。
func workBuddyWebGLForMac(chip string) workBuddyWebGL {
	return workBuddyWebGL{
		Vendor:   "Apple Inc.",
		Renderer: "ANGLE (Apple, ANGLE Metal Renderer: " + chip + ", Unspecified Version)",
	}
}

// workBuddyWebGLForWindows 由显卡型号推导 Windows 上的 ANGLE 字符串。
func workBuddyWebGLForWindows(gpu string) workBuddyWebGL {
	vendor := "Google Inc. (NVIDIA)"
	switch {
	case strings.Contains(gpu, "RTX 3060"):
		return workBuddyWebGL{vendor, "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	case strings.Contains(gpu, "RTX 4060 Ti"):
		return workBuddyWebGL{vendor, "ANGLE (NVIDIA, NVIDIA GeForce RTX 4060 Ti Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	case strings.Contains(gpu, "RTX 4060"):
		return workBuddyWebGL{vendor, "ANGLE (NVIDIA, NVIDIA GeForce RTX 4060 Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	case strings.Contains(gpu, "RTX 4070"):
		return workBuddyWebGL{vendor, "ANGLE (NVIDIA, NVIDIA GeForce RTX 4070 Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	case strings.Contains(gpu, "RTX 4080"):
		return workBuddyWebGL{vendor, "ANGLE (NVIDIA, NVIDIA GeForce RTX 4080 Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	case strings.Contains(gpu, "RTX 4090"):
		return workBuddyWebGL{vendor, "ANGLE (NVIDIA, NVIDIA GeForce RTX 4090 Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	case strings.Contains(gpu, "UHD Graphics 730"):
		return workBuddyWebGL{"Google Inc. (Intel)", "ANGLE (Intel, Intel(R) UHD Graphics 730 Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	case strings.Contains(gpu, "RX 6600"):
		return workBuddyWebGL{"Google Inc. (AMD)", "ANGLE (AMD, AMD Radeon RX 6600 Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	case strings.Contains(gpu, "RX 6800 XT"):
		return workBuddyWebGL{"Google Inc. (AMD)", "ANGLE (AMD, AMD Radeon RX 6800 XT Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	default:
		return workBuddyWebGL{vendor, "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)"}
	}
}

// workBuddyWebGLForLinux 由显卡型号推导 Linux 上的 renderer。
func workBuddyWebGLForLinux(gpu string) workBuddyWebGL {
	switch {
	case strings.Contains(gpu, "RTX 4060"):
		return workBuddyWebGL{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce RTX 4060/PCIe/SSE2, OpenGL 4.6)"}
	case strings.Contains(gpu, "RTX 4070"):
		return workBuddyWebGL{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce RTX 4070/PCIe/SSE2, OpenGL 4.6)"}
	case strings.Contains(gpu, "RTX 4080"):
		return workBuddyWebGL{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce RTX 4080/PCIe/SSE2, OpenGL 4.6)"}
	case strings.Contains(gpu, "RTX 4090"):
		return workBuddyWebGL{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce RTX 4090/PCIe/SSE2, OpenGL 4.6)"}
	default:
		return workBuddyWebGL{"Google Inc. (Mesa)", "ANGLE (Mesa, llvmpipe (LLVM 17.0.6, 256 bits), OpenGL 4.5)"}
	}
}

// workBuddyWebGLFor 按家族选择推导函数。
func workBuddyWebGLFor(family workBuddyOSFamily, chip, gpu string) workBuddyWebGL {
	switch family {
	case workBuddyOSMac:
		return workBuddyWebGLForMac(chip)
	case workBuddyOSLinux:
		return workBuddyWebGLForLinux(gpu)
	default:
		return workBuddyWebGLForWindows(gpu)
	}
}

// workBuddyFontSetsFor 按家族选择字体集。
func workBuddyFontSetsFor(family workBuddyOSFamily) [][]string {
	switch family {
	case workBuddyOSMac:
		return workBuddyFontSetsMac
	case workBuddyOSLinux:
		return workBuddyFontSetsLinux
	default:
		return workBuddyFontSetsWindows
	}
}

// workBuddyChromeVersions 是参与 UA 派生的 Chrome 主版本号。
//
// 只取仍在广泛使用的区间；过老版本会与「刚安装客户端」的判断冲突。
var workBuddyChromeVersions = []int{140, 141, 142, 143, 144, 145, 146}

// workBuddyDeriveProfile 从 key 与盐派生一个自洽的硬件画像。
//
// 每个维度独立取值：OS 族决定后续所有取值的候选表，CPU 决定核心数、
// 内存与显卡的可选集，保证组合出来是一台说得通的机器——不会出现
// 「M4 Max 配 8GB 内存」或「i9 配 8GB」这种真实中不存在的搭配。
//
// salt 用于撞号后重新派生：同样的 key 加不同盐得到不同画像。
func workBuddyDeriveProfile(key string, salt int) workBuddyFingerprintProfile {
	h := workBuddySHA256([]byte("wb-fp:" + key + "#" + itoa(salt)))

	// 家族按权重抽取：Mac 40% / Windows 40% / Linux 20%（部分渠道提供 Linux 客户端）。
	family := workBuddyOSMac
	switch roll := workBuddyPick16(h, 0, 10); {
	case roll < 4:
		family = workBuddyOSMac
	case roll < 8:
		family = workBuddyOSWindows
	default:
		family = workBuddyOSLinux
	}

	var cpus []workBuddyCPUSpec
	var versions []string
	var disks []int
	var screens []string
	switch family {
	case workBuddyOSMac:
		cpus = workBuddyMacCPUs
		versions = workBuddyMacVersions
		disks = workBuddyMacDisks
		screens = workBuddyMacScreens
	case workBuddyOSLinux:
		cpus = workBuddyLinuxCPUs
		versions = workBuddyLinuxVersions
		disks = workBuddyWindowsDisks
		screens = workBuddyWindowsScreens
	default:
		cpus = workBuddyWindowsCPUs
		versions = workBuddyWindowsVersions
		disks = workBuddyWindowsDisks
		screens = workBuddyWindowsScreens
	}

	// 用 16 位整数取模：单字节只有 256 种取值，对 len=7 这类表会产生
	// 明显的取模偏差（256 % 7 = 4，前 4 个值多出现一次），使组合分布不均。
	cpu := cpus[workBuddyPick16(h, 1, len(cpus))]
	cores := cpu.Cores[workBuddyPick16(h, 3, len(cpu.Cores))]
	memory := cpu.Memory[workBuddyPick16(h, 5, len(cpu.Memory))]
	gpu := cpu.GPU[workBuddyPick16(h, 7, len(cpu.GPU))]
	osVersion := versions[workBuddyPick16(h, 9, len(versions))]
	// 屏幕与磁盘按机型绑定：CPU 条目自带候选时用它的，避免「M1 Air 配 16 寸屏」。
	modelScreens, modelDisks := screens, disks
	if len(cpu.Screens) > 0 {
		modelScreens = cpu.Screens
	}
	if len(cpu.Disks) > 0 {
		modelDisks = cpu.Disks
	}
	disk := modelDisks[workBuddyPick16(h, 11, len(modelDisks))]
	screen := modelScreens[workBuddyPick16(h, 13, len(modelScreens))]
	chrome := workBuddyChromeVersions[workBuddyPick16(h, 15, len(workBuddyChromeVersions))]

	// 主机名按摘要程序化生成（DESKTOP-XXXXXXX / 用户机名），空间远大于表驱动维度。
	hostname := workBuddyHostname(h, family)
	timezone := workBuddyTimezones[workBuddyPick16(h, 17, len(workBuddyTimezones))]
	locale := workBuddyLocales[workBuddyPick16(h, 19, len(workBuddyLocales))]
	fontSet := workBuddyFontSetsFor(family)
	fonts := fontSet[workBuddyPick16(h, 21, len(fontSet))]
	webgl := workBuddyWebGLFor(family, cpu.Model, gpu)

	profile := workBuddyFingerprintProfile{
		Hostname:      hostname,
		Timezone:      timezone,
		Locale:        locale,
		Fonts:         fonts,
		WebGLVendor:   webgl.Vendor,
		WebGLRenderer: webgl.Renderer,
		CPUCores:      cores,
		MemorySize:    memory,
		GPU:           gpu,
		Disk:          disk,
		Screen:        screen,
		CPUModel:      cpu.Model,
		OSVersion:     osVersion,
		IDEVersion:    "5.5.6",
	}
	if family == workBuddyOSMac {
		profile.OS = "MacIntel"
		profile.Arch = "arm64"
		profile.UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + itoa(chrome) + ".0.0.0 Safari/537.36"
	} else if family == workBuddyOSLinux {
		profile.OS = "Linux x86_64"
		profile.Arch = "x64"
		profile.UA = "Mozilla/5.0 (X11; Linux x86_64) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + itoa(chrome) + ".0.0.0 Safari/537.36"
	} else {
		profile.OS = "Win32"
		profile.Arch = "x64"
		profile.UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + itoa(chrome) + ".0.0.0 Safari/537.36"
	}
	return profile
}

// workBuddyFingerprintCombinations 返回组合空间大小的下界，用于自检。
func workBuddyFingerprintCombinations() int {
	mac := len(workBuddyMacCPUs) * len(workBuddyMacVersions) * len(workBuddyMacDisks) * len(workBuddyMacScreens)
	win := len(workBuddyWindowsCPUs) * len(workBuddyWindowsVersions) * len(workBuddyWindowsDisks) * len(workBuddyWindowsScreens)
	linux := len(workBuddyLinuxCPUs) * len(workBuddyLinuxVersions) * len(workBuddyWindowsDisks) * len(workBuddyWindowsScreens)
	base := (mac + win + linux) * len(workBuddyChromeVersions)
	// 新增四维：时区 × 语言 × 字体集 × WebGL（主机名单独按程序化空间计）。
	extra := len(workBuddyTimezones) * len(workBuddyLocales) * len(workBuddyFontSets)
	return base * extra
}

// workBuddyHostname 由摘要生成一个稳定主机名，且必须符合该系统的命名习惯。
//
// Windows 是 "DESKTOP-" + 7 位大写字母数字；macOS 是用户机名（"MacBook-Pro"、
// "xx-MacBook-Pro"）；Linux 是小写短名。Mac 上出现 DESKTOP-XXXXXXX 是最容易被
// 一眼看穿的不真实之处。
func workBuddyHostname(h []byte, family workBuddyOSFamily) string {
	const upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const lower = "abcdefghijklmnopqrstuvwxyz0123456789"

	switch family {
	case workBuddyOSMac:
		macNames := []string{"MacBook-Pro", "MacBook-Air", "mac-mini", "MacBook-Pro-2"}
		base := macNames[workBuddyPick16(h, 2, len(macNames))]
		if workBuddyPick16(h, 4, 3) == 0 {
			buf := make([]byte, 5)
			for i := range buf {
				buf[i] = lower[(int(h[(6+i)%len(h)])+i*7)%len(lower)]
			}
			return string(buf) + "-" + base
		}
		return base
	case workBuddyOSLinux:
		buf := make([]byte, 9)
		for i := range buf {
			buf[i] = lower[(int(h[(2+i)%len(h)])+i*13)%len(lower)]
		}
		return string(buf)
	default:
		buf := make([]byte, 7)
		for i := range buf {
			buf[i] = upper[(int(h[(2+i)%len(h)])+i*11)%len(upper)]
		}
		return "DESKTOP-" + string(buf)
	}
}

// workBuddyDimensionExtras 按种子派生四项与设备无关的展示维度。
//
// Trae 与 Marvis 没有 WorkBuddy 那套完整画像，但指纹弹窗要显示同样的字段集，
// 所以用同一个种子（机器码 / openid）确定性派生：主机名、时区、语言、字体、WebGL。
func workBuddyDimensionExtras(seed string) map[string]any {
	if seed == "" {
		seed = "extras"
	}
	h := workBuddySHA256([]byte("wb-extras:" + seed))
	family := workBuddyOSWindows
	switch roll := workBuddyPick16(h, 0, 10); {
	case roll < 4:
		family = workBuddyOSMac
	case roll >= 8:
		family = workBuddyOSLinux
	}
	fontSet := workBuddyFontSetsFor(family)
	fonts := fontSet[workBuddyPick16(h, 21, len(fontSet))]
	// WebGL 必须由该家族真实存在的芯片/显卡推导，不能用空值。
	chip, gpu := "", ""
	switch family {
	case workBuddyOSMac:
		spec := workBuddyMacCPUs[workBuddyPick16(h, 25, len(workBuddyMacCPUs))]
		chip = spec.Model
	case workBuddyOSLinux:
		spec := workBuddyLinuxCPUs[workBuddyPick16(h, 25, len(workBuddyLinuxCPUs))]
		gpu = spec.GPU[workBuddyPick16(h, 27, len(spec.GPU))]
	default:
		spec := workBuddyWindowsCPUs[workBuddyPick16(h, 25, len(workBuddyWindowsCPUs))]
		gpu = spec.GPU[workBuddyPick16(h, 27, len(spec.GPU))]
	}
	webgl := workBuddyWebGLFor(family, chip, gpu)
	return map[string]any{
		"hostname":       workBuddyHostname(h, family),
		"timezone":       workBuddyTimezones[workBuddyPick16(h, 17, len(workBuddyTimezones))],
		"locale":         workBuddyLocales[workBuddyPick16(h, 19, len(workBuddyLocales))],
		"fonts":          fonts,
		"webgl_vendor":   webgl.Vendor,
		"webgl_renderer": webgl.Renderer,
	}
}

// workBuddySHA256 返回 SHA-256 摘要，供维度派生使用。
func workBuddySHA256(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// workBuddyPick16 用摘要中的两个连续字节组成 16 位整数后取模。
//
// 单字节 256 种取值对 len=7/9 这类长度会产生取模偏差，导致某些档位被
// 显著高估；16 位（65536）把偏差压到可忽略。
func workBuddyPick16(h []byte, offset, n int) int {
	if n <= 0 || len(h) < 32 {
		return 0
	}
	// 对每对字节再做一次混合：相邻摘要字节存在相关性，直接拼接取模会让
	// 不同 key 落到同一档位的概率远高于均匀分布。
	lo := int(h[offset%len(h)])
	hi := int(h[(offset+1)%len(h)])
	v := (hi<<8 | lo) ^ (offset * 0x9E37)
	return v % n
}
