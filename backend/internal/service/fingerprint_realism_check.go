package service

import (
	"fmt"
	"strings"
)

// FingerprintRealismIssue 描述一条画像自洽性违规。
type FingerprintRealismIssue struct {
	Sample string // 触发的抽样键
	Field  string // 出问题的字段
	Detail string // 具体原因
}

// ValidateFingerprintRealism 抽样校验派生画像是否自洽，返回全部违规项。
//
// 这里的规则对应上游最容易识别的几类矛盾：
//   - 时区/语言与「国内用户」不一致
//   - 字体集与操作系统不匹配（macOS 出现 Microsoft YaHei）
//   - WebGL 与芯片/显卡不匹配（M2 Pro 报 Apple M4）
//   - 主机名不符合系统命名习惯（macOS 出现 DESKTOP-XXXXXXX）
//   - macOS 使用非内置面板分辨率，或芯片/内存/核心数不在官方档位
//
// 生产路径不调用它；用途是把「画像必须说得通」变成可执行的检查，
// 供独立测试包或排障时调用。
func ValidateFingerprintRealism(samples int) []FingerprintRealismIssue {
	if samples <= 0 {
		samples = 200
	}

	var issues []FingerprintRealismIssue
	add := func(sample, field, detail string) {
		issues = append(issues, FingerprintRealismIssue{Sample: sample, Field: field, Detail: detail})
	}

	allowedScreens := make(map[string]bool, len(workBuddyMacScreens))
	for _, s := range workBuddyMacScreens {
		allowedScreens[s] = true
	}

	for i := 0; i < samples; i++ {
		key := fmt.Sprintf("realism-%d", i)
		p := workBuddyDeriveProfile(key, 0)

		if p.Timezone != "Asia/Shanghai" {
			add(key, "timezone", p.Timezone)
		}
		if p.Locale != "zh-CN" {
			add(key, "locale", p.Locale)
		}
		if len(p.Fonts) == 0 {
			add(key, "fonts", "empty")
			continue
		}

		switch p.OS {
		case "MacIntel":
			if strings.HasPrefix(p.Hostname, "DESKTOP-") {
				add(key, "hostname", p.Hostname)
			}
			if !allowedScreens[p.Screen] {
				add(key, "screen", p.Screen)
			}
			if !strings.Contains(p.WebGLRenderer, p.CPUModel) {
				add(key, "webgl", p.WebGLRenderer+" vs "+p.CPUModel)
			}
			for _, f := range p.Fonts {
				if strings.Contains(f, "Microsoft YaHei") || strings.Contains(f, "SimSun") {
					add(key, "fonts", f)
					break
				}
			}
		case "Win32":
			if !strings.HasPrefix(p.Hostname, "DESKTOP-") {
				add(key, "hostname", p.Hostname)
			}
			for _, f := range p.Fonts {
				if strings.Contains(f, "PingFang") || strings.Contains(f, "Menlo") {
					add(key, "fonts", f)
					break
				}
			}
			if !strings.Contains(p.WebGLRenderer, fingerprintGPUToken(p.GPU)) {
				add(key, "webgl", p.WebGLRenderer+" vs "+p.GPU)
			}
		case "Linux x86_64":
			if strings.HasPrefix(p.Hostname, "DESKTOP-") {
				add(key, "hostname", p.Hostname)
			}
		default:
			add(key, "os", p.OS)
		}

		if len(issues) >= 50 {
			break
		}
	}

	return issues
}

// fingerprintGPUToken 截取 renderer 中会出现的型号片段：
// "NVIDIA GeForce RTX 3060 / 12 GB" -> "RTX 3060"
func fingerprintGPUToken(gpu string) string {
	if i := strings.Index(gpu, " /"); i > 0 {
		gpu = gpu[:i]
	}
	for _, marker := range []string{"RTX ", "RX ", "UHD Graphics "} {
		if i := strings.Index(gpu, marker); i >= 0 {
			return gpu[i:]
		}
	}
	return gpu
}

// FingerprintMacSpecIssue 返回 macOS 机型规格表里不符合官方档位的项。
func FingerprintMacSpecIssue() []string {
	official := map[string]struct {
		cores  []int
		memory []int
	}{
		"Apple M1":     {[]int{8}, []int{8, 16}},
		"Apple M2":     {[]int{8, 10}, []int{8, 16, 24}},
		"Apple M3":     {[]int{8, 10}, []int{8, 16, 24}},
		"Apple M3 Pro": {[]int{11, 12}, []int{18, 36}},
		"Apple M4":     {[]int{8, 10}, []int{16, 24, 32}},
		"Apple M4 Pro": {[]int{12, 14}, []int{24, 48}},
		"Apple M4 Max": {[]int{14, 16}, []int{36, 64}},
	}

	var issues []string
	for _, cpu := range workBuddyMacCPUs {
		spec, ok := official[cpu.Model]
		if !ok {
			issues = append(issues, "未知芯片: "+cpu.Model)
			continue
		}
		for _, c := range cpu.Cores {
			if !intIn(spec.cores, c) {
				issues = append(issues, fmt.Sprintf("%s 不存在 %d 核版本", cpu.Model, c))
			}
		}
		for _, m := range cpu.Memory {
			if !intIn(spec.memory, m) {
				issues = append(issues, fmt.Sprintf("%s 不存在 %dGB 版本", cpu.Model, m))
			}
		}
		if len(cpu.Screens) == 0 || len(cpu.Disks) == 0 {
			issues = append(issues, cpu.Model+" 缺少屏幕/容量约束")
		}
	}
	return issues
}

func intIn(list []int, v int) bool {
	for _, i := range list {
		if i == v {
			return true
		}
	}
	return false
}

// ValidateDimensionExtras 检查展示维度（主机名/字体/WebGL/时区/语言）与系统族一致。
//
// Trae / Marvis 的指纹弹窗用的是这套派生维度，如果与账号自身 OS 不符，
// 会出现「Windows 机器显示 Mac 主机名」这种一眼假的情况。
func ValidateDimensionExtras() []string {
	var issues []string
	families := []workBuddyOSFamily{workBuddyOSMac, workBuddyOSWindows, workBuddyOSLinux}
	for _, fam := range families {
		for i := 0; i < 50; i++ {
			ex := workBuddyDimensionExtras(fmt.Sprintf("seed-%d", i), fam)
			host, _ := ex["hostname"].(string)
			fonts, _ := ex["fonts"].([]string)
			webgl, _ := ex["webgl_renderer"].(string)
			tz, _ := ex["timezone"].(string)
			loc, _ := ex["locale"].(string)

			if tz != "Asia/Shanghai" || loc != "zh-CN" {
				issues = append(issues, fmt.Sprintf("%s 时区/语言异常: %s %s", fam, tz, loc))
			}
			switch fam {
			case workBuddyOSMac:
				if strings.HasPrefix(host, "DESKTOP-") {
					issues = append(issues, "mac 主机名: "+host)
				}
				if !strings.Contains(webgl, "Apple") {
					issues = append(issues, "mac WebGL: "+webgl)
				}
				for _, f := range fonts {
					if strings.Contains(f, "Microsoft YaHei") || strings.Contains(f, "SimSun") {
						issues = append(issues, "mac 字体: "+f)
						break
					}
				}
			case workBuddyOSWindows:
				if !strings.HasPrefix(host, "DESKTOP-") {
					issues = append(issues, "win 主机名: "+host)
				}
				if strings.Contains(webgl, "Apple") {
					issues = append(issues, "win WebGL: "+webgl)
				}
			case workBuddyOSLinux:
				if strings.HasPrefix(host, "DESKTOP-") {
					issues = append(issues, "linux 主机名: "+host)
				}
				if strings.Contains(webgl, "Apple") {
					issues = append(issues, "linux WebGL: "+webgl)
				}
			}
			if len(issues) > 20 {
				return issues
			}
		}
	}
	return issues
}
