package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDumpNewThemePreviews 把近期新增皮肤的预览 HTML 落盘到 run/theme-previews/，
// 供本地静态服务截图目检。仅在 GCMS_DUMP_THEMES=1 时执行（临时工具，不进 CI）。
func TestDumpNewThemePreviews(t *testing.T) {
	if os.Getenv("GCMS_DUMP_THEMES") != "1" {
		t.Skip("set GCMS_DUMP_THEMES=1 to dump")
	}
	newIDs := []string{"masonry", "darkroom", "feed", "noir", "gazette", "tabloid",
		"manual", "kernel", "almanac", "nightshift", "inbox", "midnight",
		"catalog", "nightmarket", "broadcast", "airwave", "exhibit", "afterhours",
		"paperwhite", "citrus", "bookshop", "canal", "confetti", "icebox",
		"ledger", "signal", "gallery", "coast", "monument", "petal",
		"market", "seaside", "daytrade", "mintwire", "sunrise", "horizon",
		"workshop", "playbook", "chronicle", "gardenpath", "portfolio", "postcard",
		"atelier", "festival", "daywatch", "clinic", "peach", "skyline",
		"herbarium", "coralreef", "cloudos", "candyglass", "paperfilm", "azurefilm",
		"cutpaper", "primary", "atlas", "mintmap", "pinboard", "spectrum",
		"daybook", "civic", "broadsheet", "salmonpress", "fieldguide", "bluebook",
		"sunclock", "seedcalendar", "postbox", "airmail", "apothecary", "toolroom",
		"publicradio", "morningfm", "whitecube", "botanical"}
	_, h, ps, _, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)
	enter := httptest.NewRecorder()
	enterReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/posts", nil)
	enterReq.AddCookie(cookie)
	h.ServeHTTP(enter, enterReq)

	dir := filepath.Join("..", "..", "run", "theme-previews")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range newIDs {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/theme-preview/"+id, nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("theme %s: %d", id, rec.Code)
		}
		// 资产走相对路径即可命中仓库根的 /assets（文件在 run/theme-previews/ 下，仓库根是 ../../）
		body := strings.ReplaceAll(rec.Body.String(), `href="/assets/`, `href="../../assets/`)
		body = strings.ReplaceAll(body, `src="/assets/`, `src="../../assets/`)
		if err := os.WriteFile(filepath.Join(dir, id+".html"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("dumped %d previews to %s", len(newIDs), dir)
}
