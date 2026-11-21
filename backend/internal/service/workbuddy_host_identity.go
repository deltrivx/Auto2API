package service

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// WorkBuddy 身份对齐 magpie（@magpie-community/opencode-workbuddy-auth）。
//
// magpie 不发任何「拼出来的」硬件指纹头：它的聊天请求只带身份与 IDE 头，
// 设备 id 只出现在事件上报里，且取的是本机真实值的一种哈希：
//
//	machineId = sha256("magpie-workbuddy|<hostname>|<user>")
//
// 上报里的 os / arch / osVersion / cpuModel / cpuCores / memorySize / timezone
// 也全部来自真实主机（os.platform、arch()、release()、os.cpus()、totalmem()）。
//
// 这里照做：不再生成模拟的 Windows 画像、MAC、字体、WebGL、屏幕、磁盘，
// 全部换成运行它的这台机器的真实值。

type workBuddyHostIdentity struct {
	MachineID  string
	Hostname   string
	OS         string
	Arch       string
	OSVersion  string
	CPUModel   string
	CPUCores   int
	MemorySize int64 // 字节，与 magpie 的 totalmem() 一致
	Timezone   string
}

var (
	hostIdentityOnce sync.Once
	hostIdentity     workBuddyHostIdentity
)

// workBuddyHost 返回本机身份（只读一次）。
func workBuddyHost() workBuddyHostIdentity {
	hostIdentityOnce.Do(func() {
		host, _ := os.Hostname()
		username := ""
		if u, err := user.Current(); err == nil {
			username = u.Username
		}
		sum := sha256.Sum256([]byte("magpie-workbuddy|" + host + "|" + username))
		hostIdentity = workBuddyHostIdentity{
			MachineID:  hex.EncodeToString(sum[:]),
			Hostname:   host,
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			OSVersion:  workBuddyHostOSVersion(),
			CPUModel:   workBuddyHostCPUModel(),
			CPUCores:   runtime.NumCPU(),
			MemorySize: workBuddyHostMemoryBytes(),
			Timezone:   workBuddyHostTimezone(),
		}
	})
	return hostIdentity
}

// workBuddyHostOSVersion 读发行版版本号，读不到时退回内核版本。
func workBuddyHostOSVersion() string {
	if f, err := os.Open("/etc/os-release"); err == nil {
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if v, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
				return strings.Trim(v, `"`)
			}
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// workBuddyHostCPUModel 取 /proc/cpuinfo 的 model name。
func workBuddyHostCPUModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "model name"); ok {
			if i := strings.Index(v, ":"); i >= 0 {
				return strings.TrimSpace(v[i+1:])
			}
		}
	}
	return ""
}

// workBuddyHostMemoryBytes 取 MemTotal（KB → 字节）。
func workBuddyHostMemoryBytes() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}

// workBuddyHostTimezone 取容器/主机的时区名。
func workBuddyHostTimezone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return tz
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(b)); tz != "" {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return strings.TrimPrefix(target[i:], "zoneinfo/")
		}
	}
	return ""
}
