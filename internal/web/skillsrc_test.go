package web

import (
	"os"
	"testing"
)

// TestSkillScriptMirrorsInSync 锁死脚本漂移：repo 里的 skills/ 拷贝必须与
// 实际打进技能包的 embed 版本逐字节一致（历史上两者漂移过，发的和看的不是一份）。
func TestSkillScriptMirrorsInSync(t *testing.T) {
	mirror, err := os.ReadFile("../../skills/gcms-content-assistant/scripts/gcms.js")
	if err != nil {
		t.Fatalf("读取 skills/ 镜像失败: %v", err)
	}
	if string(mirror) != skillScriptSingle {
		t.Fatalf("skills/gcms-content-assistant/scripts/gcms.js 与 internal/web/skillsrc/gcms_single.js 不一致——请以 skillsrc/ 为准同步镜像")
	}
}
