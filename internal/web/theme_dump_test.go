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

// appendCommerceThemePreviewIDs keeps the optional visual export in sync with
// the registry. New factory / DTC skins therefore enter QA automatically
// instead of depending on somebody remembering to extend another hand list.
func appendCommerceThemePreviewIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, th := range Themes {
		layout := layoutForTheme(th.ID)
		if !isFactoryLayout(layout) && !isDTCLayout(layout) {
			continue
		}
		if !seen[th.ID] {
			ids = append(ids, th.ID)
			seen[th.ID] = true
		}
	}
	return ids
}

// TestDumpNewThemePreviews 把近期新增皮肤的预览 HTML 落盘到 run/theme-previews/，
// 供本地静态服务截图目检。仅在 GCMS_DUMP_THEMES=1 时执行，不进日常 CI。
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
		"publicradio", "morningfm", "whitecube", "botanical",
		"field-ledger", "signal-archive", "paper-current", "night-watch",
		"orbit-index", "column-stage", "type-cascade",
		"briefing-desk", "briefing-desk-white", "briefing-desk-sage", "briefing-desk-ink",
		"decision-wall", "decision-wall-white", "decision-wall-mint", "decision-wall-carbon",
		"route-atlas", "route-atlas-white", "route-atlas-indigo", "route-atlas-moss",
		"answer-desk", "answer-desk-white", "answer-desk-dark",
		"portrait-journal", "portrait-journal-white", "portrait-journal-dark",
		"casebook", "casebook-white", "casebook-dark",
		"shelf-index", "shelf-index-white", "shelf-index-dark",
		"tradeoff-sheet", "tradeoff-sheet-white", "tradeoff-sheet-dark",
		"progress-bulletin", "progress-bulletin-white", "progress-bulletin-dark",
		"margin-reading-room", "margin-reading-room-white", "margin-reading-room-dark",
		"light-table", "light-table-white", "light-table-dark",
		"counterpoint", "counterpoint-white", "counterpoint-dark",
		"seamless-canvas", "seamless-canvas-white", "seamless-canvas-dark",
		"night-corridor", "night-corridor-white", "night-corridor-dark",
		"open-ascent", "open-ascent-white", "open-ascent-dark"}
	newIDs = appendCommerceThemePreviewIDs(newIDs)
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)
	// 可选视觉导出使用写入测试数据库的真实商品与分类记录。生产预览处理器
	// 本身不再注入任何 commerce 样例；这里显式播种仅用于让本地截图能检查
	// 商品卡、分类路由与响应式密度。
	runtime, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil || runtime.Store == nil || runtime.server == nil {
		t.Fatal("blog runtime missing")
	}
	if err := runtime.Store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Store.SeedFactoryDemoProducts(); err != nil {
		t.Fatal(err)
	}
	runtime.server.clearGeneratedCaches()
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
		// 独立内容骨架和 commerce 骨架都需按真实视口做 1:1 与响应式回归；
		// 后台卡片预览本身固定为 1120px，因此另存一份只解除预览容器约束的
		// QA 页面。结构、数据和生产 CSS 均保持不变，不把测试尺寸写进正式模板。
		layout := layoutForTheme(id)
		if contentThemeFamily(id) != "" || isFactoryLayout(layout) || isDTCLayout(layout) {
			fullStyle := `<style>
				html { width:auto !important; min-width:0 !important; min-height:100% !important; overflow:auto !important; }
				body { width:auto !important; min-height:100vh !important; margin:0 !important; overflow:hidden !important; }
			`
			// 内容主题的卡片预览会把普通顶部导航取消 sticky；只在这组主题
			// 的全视口 QA 文件中恢复。commerce 的侧栏主题本身使用 fixed
			// header，不能覆盖为 sticky，否则会把主内容推到一屏之后。
			if contentThemeFamily(id) != "" {
				fullStyle += `
				.site-header { position:sticky !important; top:0 !important; }
				.oi-footer,.cs-footer { display:grid !important; }
				.tc-footer { display:flex !important; }
				`
			}
			// 侧栏主题的缩略图需要强制 fixed 才能在 1120px 卡片里完整出现；
			// 真机宽度下必须交还主题自己的顶部导航断点，避免窄屏正文被缩略图
			// 专用竖栏覆盖。
			if layout == "sidebar" || layout == "factory-sidebar" || layout == "dtc-catalogue" || layout == "dtc-atelier" {
				fullStyle += `
				@media (max-width: 920px) {
					body { padding-left: 0 !important; }
					.site-header:not(.pc-site-header) {
						position: sticky !important;
						inset: 0 0 auto 0 !important;
						width: 100% !important;
						height: auto !important;
					}
				}
				`
			}
			fullStyle += "</style>"
			full := strings.Replace(body, "</head>", fullStyle+"</head>", 1)
			full = strings.Replace(full, `content="width=1120, initial-scale=1"`, `content="width=device-width, initial-scale=1"`, 1)
			full = strings.Replace(full, "</body>", `<script src="../../assets/js/site.js"></script></body>`, 1)
			if err := os.WriteFile(filepath.Join(dir, id+"-full.html"), []byte(full), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 新骨架还必须覆盖真实公共内页，避免只把首页做成设计稿，普通页面或文章页又退回通用皮肤。
	public := newTestPublicServer(t, "")
	for _, id := range []string{
		"orbit-index", "column-stage", "type-cascade", "briefing-desk", "decision-wall", "route-atlas",
		"answer-desk", "answer-desk-white", "answer-desk-dark",
		"portrait-journal", "portrait-journal-white", "portrait-journal-dark",
		"casebook", "casebook-white", "casebook-dark",
		"shelf-index", "shelf-index-white", "shelf-index-dark",
		"tradeoff-sheet", "tradeoff-sheet-white", "tradeoff-sheet-dark",
		"progress-bulletin", "progress-bulletin-white", "progress-bulletin-dark",
		"margin-reading-room", "margin-reading-room-white", "margin-reading-room-dark",
		"light-table", "light-table-white", "light-table-dark",
		"counterpoint", "counterpoint-white", "counterpoint-dark",
		"seamless-canvas", "seamless-canvas-white", "seamless-canvas-dark",
		"night-corridor", "night-corridor-white", "night-corridor-dark",
		"open-ascent", "open-ascent-white", "open-ascent-dark",
	} {
		if err := public.store.SetSetting("theme", id); err != nil {
			t.Fatalf("set public theme %s: %v", id, err)
		}
		public.clearGeneratedCaches()
		for _, page := range []struct{ Path, Suffix string }{
			{"/zh/about", "about"},
			{"/zh/posts/cloudflare-static-deploy/", "article"},
		} {
			rec := httptest.NewRecorder()
			public.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, page.Path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s page %s: %d", page.Suffix, id, rec.Code)
			}
			body := strings.ReplaceAll(rec.Body.String(), `href="/assets/`, `href="../../assets/`)
			body = strings.ReplaceAll(body, `src="/assets/`, `src="../../assets/`)
			if err := os.WriteFile(filepath.Join(dir, id+"-"+page.Suffix+".html"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("dumped %d previews to %s", len(newIDs), dir)
}

func TestThemeVisualExportCoversCommerceRegistry(t *testing.T) {
	got := map[string]bool{}
	for _, id := range appendCommerceThemePreviewIDs(nil) {
		got[id] = true
	}
	for _, th := range Themes {
		layout := layoutForTheme(th.ID)
		if !isFactoryLayout(layout) && !isDTCLayout(layout) {
			continue
		}
		if !got[th.ID] {
			t.Errorf("commerce theme %q (%s) missing from visual export", th.ID, layout)
		}
	}
}
