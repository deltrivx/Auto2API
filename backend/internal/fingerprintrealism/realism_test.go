package fingerprintrealism

import (
	"strings"
	"testing"

	"github.com/DeltrivX/Auto2API/internal/service"
)

// 独立于 service 包既有测试的守卫：那里有大量历史遗留测试无法编译，
// 直接在其中加用例会连带失败，所以把校验逻辑做成 service 的导出函数，
// 在这里单独跑。
func TestProfilesAreRealistic(t *testing.T) {
	issues := service.ValidateFingerprintRealism(500)
	if len(issues) == 0 {
		return
	}

	var b strings.Builder
	for i, it := range issues {
		if i >= 10 {
			b.WriteString("…")
			break
		}
		b.WriteString("\n  ")
		b.WriteString(it.Sample)
		b.WriteString(" / ")
		b.WriteString(it.Field)
		b.WriteString(": ")
		b.WriteString(it.Detail)
	}
	t.Fatalf("发现 %d 条画像不自洽：%s", len(issues), b.String())
}

func TestMacSpecsMatchOfficial(t *testing.T) {
	issues := service.FingerprintMacSpecIssue()
	if len(issues) == 0 {
		return
	}
	t.Fatalf("macOS 机型规格不自洽：\n  %s", strings.Join(issues, "\n  "))
}
