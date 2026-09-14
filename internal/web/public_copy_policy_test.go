package web

import (
	"strings"
	"testing"
)

// Exercise the actual package builders, not only the Markdown helpers: imported
// single-site, platform and starter skills must all carry the shared rule.
func TestPublicCopyPolicyInSkillPackages(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(automationSkillOptions) ([]automationSkillFile, error)
		paths []string
	}{
		{"single", automationSkillFiles, []string{"gcms-content-assistant/SKILL.md", "gcms-content-assistant/AI助手说明.md"}},
		{"platform", platformSkillFiles, []string{platformSkillFolder + "/SKILL.md", platformSkillFolder + "/AI助手说明.md"}},
		{"starter", automationStarterFiles, []string{"gcms-site-starter/SKILL.md", "gcms-site-starter/给AI的任务说明.md", "gcms-site-starter/工作流.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, err := tc.build(automationSkillOptions{apiBase: "https://example.invalid/api/admin/v1"})
			if err != nil {
				t.Fatal(err)
			}
			byPath := make(map[string]string)
			for _, file := range files {
				byPath[file.name] = file.body
			}
			for _, path := range tc.paths {
				body := byPath[path]
				if strings.Count(body, publicCopyPolicy) != 1 {
					t.Errorf("%s must contain the shared editorial rule exactly once", path)
				}
				if strings.HasSuffix(path, "/SKILL.md") && !strings.HasPrefix(body, "---\nname:") {
					t.Errorf("%s lost skill frontmatter", path)
				}
			}
		})
	}
}
