package service

import (
	"strings"
	"testing"
)

// Trae 平台注册与站点表的基本约束。
func TestTraeSites(t *testing.T) {
	cn := traeSiteForPlatform(PlatformTraeCN)
	if cn == nil || cn.ID != PlatformTraeCN {
		t.Fatalf("国内站站点查找失败: %+v", cn)
	}
	if cn.DeviceIDDigits != 16 {
		t.Errorf("国内站 device_id 应为 16 位，实际 %d", cn.DeviceIDDigits)
	}
	if cn.NativeUnit != traeUnitCredits {
		t.Errorf("国内站额度单位应为积分，实际 %q", cn.NativeUnit)
	}

	intl := traeSiteForPlatform(PlatformTraeIntl)
	if intl == nil || intl.ID != PlatformTraeIntl {
		t.Fatalf("国际站站点查找失败: %+v", intl)
	}
	if intl.DeviceIDDigits != 19 {
		t.Errorf("国际站 device_id 应为 19 位，实际 %d", intl.DeviceIDDigits)
	}
	if intl.NativeUnit != traeUnitUSD {
		t.Errorf("国际站额度单位应为美元，实际 %q", intl.NativeUnit)
	}

	if !IsTraePlatform(PlatformTraeCN) || !IsTraePlatform(PlatformTraeIntl) {
		t.Error("IsTraePlatform 应同时认两个站点")
	}
	if IsTraePlatform(PlatformMarvis) || IsTraePlatform("") {
		t.Error("IsTraePlatform 不应认其它平台")
	}
	if traeSiteForPlatform(PlatformMarvis) != nil {
		t.Error("非 Trae 平台不应返回站点")
	}
}

// 国际站按区域选主机；未知区域一律回落 SG。
func TestTraeRegionalHosts(t *testing.T) {
	site := traeSiteForPlatform(PlatformTraeIntl)

	us := site.traeHostsFor("us")
	if us.ModelHost != traeUsRegion.ModelHost {
		t.Errorf("US 账号应走 %s，实际 %s", traeUsRegion.ModelHost, us.ModelHost)
	}
	if !strings.Contains(us.PayHost, "api-us-east") {
		t.Errorf("US 账号额度接口应在 api-us-east，实际 %s", us.PayHost)
	}

	for _, region := range []string{"", "sg", "unknown"} {
		sg := site.traeHostsFor(region)
		if sg.ModelHost != traeSgRegion.ModelHost {
			t.Errorf("region=%q 应回落 SG，实际 %s", region, sg.ModelHost)
		}
	}

	// 国内站不受 region 影响。
	cn := traeSiteForPlatform(PlatformTraeCN)
	if got := cn.traeHostsFor("us"); got.ModelHost != cn.ModelHost {
		t.Errorf("国内站的主机不应随 region 变化，实际 %s", got.ModelHost)
	}
}
