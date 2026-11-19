package service

import (
	"fmt"
	"strings"
	"testing"
)

// 这些用例守住「画像必须自洽」这条底线：
//   - 使用者都在国内：时区东八区、简体中文
//   - 字体集必须属于对应系统（Mac 不会装 Microsoft YaHei）
//   - WebGL 必须与芯片/显卡一致（M2 Pro 不会报 Apple M4）
//   - 主机名符合系统命名习惯（Mac 不会叫 DESKTOP-XXXXXXX）
//   - macOS 机型只用真实内置面板分辨率
func TestWorkBuddyProfileRealism(t *testing.T) {
	macScreens := map[string]bool{}
	for _, s := range workBuddyMacScreens {
		macScreens[s] = true
	}

	counts := map[string]int{}
	for i := 0; i < 500; i++ {
		p := workBuddyDeriveProfile(fmt.Sprintf("realism-%d", i), 0)

		if p.Timezone != "Asia/Shanghai" {
			t.Fatalf("时区必须是 Asia/Shanghai，实际 %q", p.Timezone)
		}
		if p.Locale != "zh-CN" {
			t.Fatalf("语言必须是 zh-CN，实际 %q", p.Locale)
		}
		if len(p.Fonts) == 0 {
			t.Fatal("字体集为空")
		}

		switch p.OS {
		case "MacIntel":
			counts["mac"]++
			if strings.HasPrefix(p.Hostname, "DESKTOP-") {
				t.Fatalf("macOS 主机名不可能是 %q", p.Hostname)
			}
			if !macScreens[p.Screen] {
				t.Fatalf("macOS 使用了非内置屏分辨率 %q", p.Screen)
			}
			if !strings.Contains(p.WebGLRenderer, p.CPUModel) {
				t.Fatalf("WebGL %q 与芯片 %q 不一致", p.WebGLRenderer, p.CPUModel)
			}
			for _, f := range p.Fonts {
				if strings.Contains(f, "Microsoft YaHei") || strings.Contains(f, "SimSun") {
					t.Fatalf("macOS 不可能有 Windows 字体 %q", f)
				}
			}
			if p.Disk < 256 {
				t.Fatalf("macOS 存储容量不合理: %d", p.Disk)
			}
		case "Win32":
			counts["win"]++
			if !strings.HasPrefix(p.Hostname, "DESKTOP-") {
				t.Fatalf("Windows 主机名不可能是 %q", p.Hostname)
			}
			for _, f := range p.Fonts {
				if strings.Contains(f, "PingFang") || strings.Contains(f, "Menlo") {
					t.Fatalf("Windows 不可能有 macOS 字体 %q", f)
				}
			}
			if !strings.Contains(p.WebGLRenderer, gpuShortName(p.GPU)) {
				t.Fatalf("WebGL %q 与显卡 %q 不一致", p.WebGLRenderer, p.GPU)
			}
		case "Linux x86_64":
			counts["linux"]++
			if strings.HasPrefix(p.Hostname, "DESKTOP-") {
				t.Fatalf("Linux 主机名不可能是 %q", p.Hostname)
			}
		default:
			t.Fatalf("未知系统族: %q", p.OS)
		}
	}

	for _, family := range []string{"mac", "win"} {
		if counts[family] == 0 {
			t.Fatalf("%s 族在 500 次派生中一次都没出现", family)
		}
	}
}

// gpuShortName 取出 renderer 里会出现的型号片段：
// "NVIDIA GeForce RTX 3060 / 12 GB" -> "RTX 3060"，
// "Intel UHD Graphics 730 / 共享内存" -> "UHD Graphics 730"。
func gpuShortName(gpu string) string {
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

// Mac 各机型的内存/核心必须落在官方档位上。
func TestWorkBuddyMacSpecsAreOfficial(t *testing.T) {
	official := map[string]struct {
		cores  []int
		memory []int
	}{
		"Apple M1":      {[]int{8}, []int{8, 16}},
		"Apple M2":      {[]int{8, 10}, []int{8, 16, 24}},
		"Apple M3":      {[]int{8, 10}, []int{8, 16, 24}},
		"Apple M3 Pro":  {[]int{11, 12}, []int{18, 36}},
		"Apple M4":      {[]int{8, 10}, []int{16, 24, 32}},
		"Apple M4 Pro":  {[]int{12, 14}, []int{24, 48}},
		"Apple M4 Max":  {[]int{14, 16}, []int{36, 64}},
	}

	for _, cpu := range workBuddyMacCPUs {
		spec, ok := official[cpu.Model]
		if !ok {
			t.Fatalf("未知 Apple 芯片: %s", cpu.Model)
		}
		for _, c := range cpu.Cores {
			if !containsInt(spec.cores, c) {
				t.Fatalf("%s 不存在 %d 核版本", cpu.Model, c)
			}
		}
		for _, m := range cpu.Memory {
			if !containsInt(spec.memory, m) {
				t.Fatalf("%s 不存在 %dGB 内存版本", cpu.Model, m)
			}
		}
		if len(cpu.Screens) == 0 || len(cpu.Disks) == 0 {
			t.Fatalf("%s 缺少机型屏幕/容量约束", cpu.Model)
		}
	}
}

func containsInt(list []int, v int) bool {
	for _, i := range list {
		if i == v {
			return true
		}
	}
	return false
}
