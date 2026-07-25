package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

// TestThemeCardPreviewUsesLiveHeroAndHomeOrdering ensures that the compact
// preview behind an admin theme card remains a rendering of the current site,
// rather than a synthetic page with a different featured-post selection.
func TestThemeCardPreviewUsesLiveHeroAndHomeOrdering(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)

	runtime, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil || runtime.Store == nil || runtime.server == nil {
		t.Fatal("blog runtime missing")
	}
	// The platform fixture includes sample content. Remove it so this test owns
	// both the featured selection and the chronological stream it compares.
	existing, err := runtime.Store.ListPublished("zh", 0, 100)
	if err != nil {
		t.Fatalf("list fixture posts: %v", err)
	}
	for _, post := range existing {
		if err := runtime.Store.DeletePost(post.ID); err != nil {
			t.Fatalf("remove fixture post %d: %v", post.ID, err)
		}
	}
	for key, value := range map[string]string{
		"hero.visual": "image",
		"hero.image":  "/uploads/theme-card-live-hero.webp",
	} {
		if err := runtime.Store.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	posts := []struct {
		title    string
		featured bool
		age      time.Duration
	}{
		{"主题卡置顶主推", true, 3 * time.Hour},
		{"主题卡最新普通文章", false, time.Hour},
		{"主题卡次新普通文章", false, 2 * time.Hour},
		{"主题卡置顶次条", true, 4 * time.Hour},
		{"主题卡较早普通文章", false, 5 * time.Hour},
	}
	for i, post := range posts {
		id, err := runtime.Store.CreatePost(&store.Post{
			Type:        "post",
			Lang:        "zh",
			Slug:        fmt.Sprintf("theme-card-live-%d", i),
			Title:       post.title,
			Excerpt:     post.title + "摘要",
			Content:     post.title + "正文",
			Status:      "published",
			EditorMode:  "markdown",
			PublishedAt: now.Add(-post.age),
		})
		if err != nil {
			t.Fatalf("create %s: %v", post.title, err)
		}
		if post.featured {
			if err := runtime.Store.SetFeatured(id, true); err != nil {
				t.Fatalf("feature %s: %v", post.title, err)
			}
		}
	}
	featured, err := runtime.Store.FeaturedPosts("zh", 6)
	if err != nil {
		t.Fatalf("load featured posts: %v", err)
	}
	featuredTitles := make([]string, 0, len(featured))
	for _, post := range featured {
		featuredTitles = append(featuredTitles, post.Title)
	}
	if got := strings.Join(featuredTitles, ","); got != "主题卡置顶主推,主题卡置顶次条" {
		t.Fatalf("featured seed order = %q", got)
	}
	runtime.server.clearGeneratedCaches()

	// The session must select the blog site before either admin-only preview
	// endpoint can resolve its site runtime.
	enter := httptest.NewRecorder()
	enterReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/posts", nil)
	enterReq.AddCookie(cookie)
	h.ServeHTTP(enter, enterReq)

	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test"+path, nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, body=%s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Seamless Canvas has a media Hero plus a distinct featured slot and post
	// stream, making it sensitive to both pieces of preview data preparation.
	thumbnail := get("/admin/theme-preview/seamless-canvas")
	browse := get("/admin/theme-browse/seamless-canvas/")
	hero := `src="/uploads/theme-card-live-hero.webp"`
	wantOrder := []string{
		"主题卡置顶主推",
		"主题卡最新普通文章",
		"主题卡次新普通文章",
		"主题卡置顶次条",
		"主题卡较早普通文章",
	}
	for name, body := range map[string]string{"thumbnail": thumbnail, "browse": browse} {
		if !strings.Contains(body, hero) {
			t.Errorf("%s preview omitted the live Hero image %q", name, hero)
		}
		last := -1
		positions := make([]int, 0, len(wantOrder))
		for _, title := range wantOrder {
			at := strings.Index(body, title)
			positions = append(positions, at)
			if at < 0 {
				t.Errorf("%s preview omitted %q", name, title)
				continue
			}
			if at < last {
				t.Errorf("%s preview order is not %v (positions %v)", name, wantOrder, positions)
				break
			}
			last = at
		}
	}
}

// TestThemePreviewRendersAllThemes 渲染每一个注册主题的后台预览页：
// 任何皮肤/骨架登记不一致（Themes 有但 CSS/模板缺）或模板执行错误都会在这里翻车。
func TestThemePreviewRendersAllThemes(t *testing.T) {
	_, h, ps, _, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)
	layoutSkeletons := map[string]string{
		"almanac": "al-wrap", "axis": "axis-wrap", "bands": "bands-band bands-hero",
		"bento": "bento-wrap", "bloom": "bloom-wrap", "board": "board-wrap",
		"broadcast": "bc-wrap", "catalog": "ct-wrap", "cinema": "cin-reel",
		"collage": "col-wrap", "constellation": "cst", "deck": "deck-shell",
		"desktop": "dsk-wrap", "exhibit": "ex-wrap", "feed": "fd-wrap",
		"factory-catalog": "fc-wrap", "factory-showcase": "fs-wrap",
		"factory-onepage": "fo-wrap", "factory-solutions": "fx-wrap",
		"factory-engineering": "fe-wrap", "factory-trade": "ft-wrap",
		"factory-sidebar": "fb-wrap", "factory-vision": "fv-wrap",
		"factory-herofold": "fh-wrap",
		"gazette":          "gz-wrap", "inbox": "ib-wrap", "index": "index-wrap",
		"liftoff": "lo-hero", "manual": "mn-wrap", "masonry": "ms-wrap",
		"poster": "poster-scroll", "profile": "prof-wrap", "split": "split-hero",
		"ticker": "tick-marquee", "timeline": "tl-wrap", "uptime": "up-wrap",
	}

	// 进入站点后台，让会话绑定一个站点（站点级路由需要 current site）。
	enter := httptest.NewRecorder()
	enterReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/posts", nil)
	enterReq.AddCookie(cookie)
	h.ServeHTTP(enter, enterReq)

	if len(Themes) == 0 {
		t.Fatal("Themes registry is empty")
	}
	for _, th := range Themes {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/theme-preview/"+th.ID, nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("theme %q preview status = %d", th.ID, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, `data-theme="`+th.ID+`"`) {
			t.Errorf("theme %q preview missing data-theme attr", th.ID)
		}
		wantLayout := layoutForTheme(th.ID)
		if !strings.Contains(body, `data-theme-layout="`+wantLayout+`"`) {
			t.Errorf("theme %q preview missing data-theme-layout=%q", th.ID, wantLayout)
		}
		skeleton, ok := layoutSkeletons[wantLayout]
		if strings.HasPrefix(th.ID, "field-ledger") {
			skeleton, ok = "fl-mast", true
		}
		if strings.HasPrefix(th.ID, "signal-archive") {
			skeleton, ok = "sa-hero", true
		}
		if strings.HasPrefix(th.ID, "paper-current") {
			skeleton, ok = "pc-hero", true
		}
		if strings.HasPrefix(th.ID, "night-watch") {
			skeleton, ok = "nw-hero", true
		}
		if strings.HasPrefix(th.ID, "orbit-index") {
			skeleton, ok = "oi-orbit-shell", true
		}
		if strings.HasPrefix(th.ID, "column-stage") {
			skeleton, ok = "cs-stage", true
		}
		if strings.HasPrefix(th.ID, "type-cascade") {
			skeleton, ok = "tc-cascade", true
		}
		if strings.HasPrefix(th.ID, "briefing-desk") {
			skeleton, ok = "briefing-desk-page", true
		}
		if strings.HasPrefix(th.ID, "decision-wall") {
			skeleton, ok = "decision-wall-page", true
		}
		if strings.HasPrefix(th.ID, "route-atlas") {
			skeleton, ok = "route-atlas-page", true
		}
		if strings.HasPrefix(th.ID, "answer-desk") {
			skeleton, ok = "answer-desk-page", true
		}
		if strings.HasPrefix(th.ID, "portrait-journal") {
			skeleton, ok = "portrait-journal-page", true
		}
		if strings.HasPrefix(th.ID, "casebook") {
			skeleton, ok = "casebook-page", true
		}
		if strings.HasPrefix(th.ID, "shelf-index") {
			skeleton, ok = "shelf-index-page", true
		}
		if strings.HasPrefix(th.ID, "tradeoff-sheet") {
			skeleton, ok = "tradeoff-sheet-page", true
		}
		if strings.HasPrefix(th.ID, "progress-bulletin") {
			skeleton, ok = "progress-bulletin-page", true
		}
		if strings.HasPrefix(th.ID, "margin-reading-room") {
			skeleton, ok = "margin-reading-room-page", true
		}
		if strings.HasPrefix(th.ID, "light-table") {
			skeleton, ok = "light-table-page", true
		}
		if strings.HasPrefix(th.ID, "counterpoint") {
			skeleton, ok = "counterpoint-page", true
		}
		if strings.HasPrefix(th.ID, "seamless-canvas") {
			skeleton, ok = "seamless-canvas-page", true
		}
		if strings.HasPrefix(th.ID, "night-corridor") {
			skeleton, ok = "night-corridor-page", true
		}
		if strings.HasPrefix(th.ID, "open-ascent") {
			skeleton, ok = "open-ascent-page", true
		}
		if ok {
			needle := `class="` + skeleton + `"`
			if contentThemeFamily(th.ID) != "" {
				needle = `class="` + skeleton
			}
			if !strings.Contains(body, needle) {
				t.Errorf("theme %q preview missing %s skeleton", th.ID, skeleton)
			}
		}
		for prefix, fixedLabels := range map[string][]string{
			"field-ledger":   {">FIELD LEDGER<"},
			"signal-archive": {">主题索引<", ">最新内容信号<", ">探索全部内容主题"},
			"paper-current":  {">内容目录<", ">查看完整目录"},
			"night-watch":    {">证据板<", ">最新特派<", ">EVIDENCE BOARD<"},
			"orbit-index":    {">环形索引<", ">Orbit Index<"},
			"column-stage":   {">栏幕<", ">Column Stage<"},
			"type-cascade":   {">字幕瀑布<", ">Type Cascade<"},
		} {
			if !strings.HasPrefix(th.ID, prefix) {
				continue
			}
			for _, label := range fixedLabels {
				if strings.Contains(body, label) {
					t.Errorf("theme %q still renders fixed content label %q", th.ID, label)
				}
			}
		}
		if contentThemeFamily(th.ID) != "" {
			family := contentThemeFamily(th.ID)
			brandClass := `class="ct-header-brand"`
			switch family {
			case "paper-current":
				brandClass = `class="pc-header-brand"`
			case "orbit-index":
				brandClass = `class="oi-header-brand"`
			case "column-stage":
				brandClass = `class="cs-header-brand"`
			case "type-cascade":
				brandClass = `class="tc-rail-brand"`
			case "briefing-desk", "decision-wall", "route-atlas":
				brandClass = `class="wg-header-brand"`
			case "answer-desk", "portrait-journal", "casebook",
				"shelf-index", "tradeoff-sheet", "progress-bulletin",
				"margin-reading-room", "light-table", "counterpoint",
				"seamless-canvas", "night-corridor", "open-ascent":
				brandClass = `class="ec-header-brand"`
			}
			start := strings.Index(body, brandClass)
			if start < 0 {
				t.Errorf("theme %q preview missing content-theme brand wrapper", th.ID)
			} else if end := strings.Index(body[start:], `</div>`); end >= 0 {
				brandHTML := body[start : start+end]
				if strings.Contains(brandHTML, "<small>") {
					t.Errorf("theme %q content-theme header still renders a brand subtitle", th.ID)
				}
				if family == "decision-wall" && !strings.Contains(brandHTML, `class="brand-logo"`) {
					t.Errorf("theme %q decision-wall header does not render the configured site logo", th.ID)
				}
			}
			if (family == "orbit-index" || family == "column-stage" || family == "type-cascade") && strings.Contains(body, `class="ct-palette"`) {
				t.Errorf("theme %q renders the design palette annotation in the public footer", th.ID)
			}
		}
	}

	// 皮肤 CSS 必须真的存在于被服务的 public.css：防止只登记不写皮。
	// 例外：默认主题 editorial 走 :root 基础变量；与骨架同名的"原生主题"（如 sidebar）
	// 可以骑默认调色板、只靠 data-theme-layout 布局 CSS。
	for _, th := range Themes {
		if th.ID == "editorial" || th.ID == layoutForTheme(th.ID) {
			continue
		}
		if !publicCSSHasTheme(t, h, cookie, th.ID) {
			t.Errorf("public.css missing [data-theme=%q] block", th.ID)
		}
	}
	if !strings.Contains(publicCSSCache, `[data-theme^="route-atlas"] .post-list.search-results:has(> .search-empty) { border-bottom:0; border-left:0; }`) {
		t.Error("route-atlas search empty state still inherits the list frame")
	}
	for _, want := range []string{
		`.ec-main-nav a[aria-current="page"]::after { display:none; }`,
		`position:sticky; top:0; z-index:20;`,
		`height:59px; border-top:0;`,
		`max-width:1280px; height:58px; margin-inline:auto;`,
		`.answer-desk-page { width:100%; max-width:1280px; margin-inline:auto;`,
		`.portrait-journal-page { width:100%; max-width:1280px; margin-inline:auto;`,
		`.casebook-page { width:100%; max-width:1280px; margin-inline:auto;`,
		`:is([data-theme^="answer-desk"],[data-theme^="portrait-journal"],[data-theme^="casebook"]) main:not(:has(> :is(.answer-desk-page,.portrait-journal-page,.casebook-page))) {
  padding-bottom:clamp(72px,8vw,112px);`,
		`.si-hero-media { min-height:440px; overflow:hidden; background:transparent; }`,
		`.ts-hero-media { min-height:430px; overflow:hidden; padding:24px 0 24px 24px; background:transparent; }`,
		`.pb-hero-media { min-height:300px; overflow:hidden; align-self:center; background:transparent; }`,
		`.mr-hero-media { padding:0; overflow:hidden; background:transparent; }`,
		`.lt-hero-visual { position:relative; min-height:340px; overflow:hidden; background:transparent; }`,
		`.cp-hero-media { position:relative; min-height:250px; overflow:hidden; background:transparent; }`,
	} {
		if !strings.Contains(publicCSSCache, want) {
			t.Errorf("editorial collection responsive shell missing CSS contract %q", want)
		}
	}
}

func TestLightThemePairsCoverEveryLayout(t *testing.T) {
	pairs := map[string][2]string{
		"topbar": {"paperwhite", "citrus"}, "sidebar": {"bookshop", "canal"},
		"bento": {"confetti", "icebox"}, "index": {"ledger", "signal"},
		"split": {"gallery", "coast"}, "axis": {"monument", "petal"},
		"bands": {"market", "seaside"}, "ticker": {"daytrade", "mintwire"},
		"liftoff": {"sunrise", "horizon"}, "board": {"workshop", "playbook"},
		"timeline": {"chronicle", "gardenpath"}, "deck": {"portfolio", "postcard"},
		"poster": {"atelier", "festival"}, "uptime": {"daywatch", "clinic"},
		"profile": {"peach", "skyline"}, "bloom": {"herbarium", "coralreef"},
		"desktop": {"cloudos", "candyglass"}, "cinema": {"paperfilm", "azurefilm"},
		"collage": {"cutpaper", "primary"}, "constellation": {"atlas", "mintmap"},
		"masonry": {"pinboard", "spectrum"}, "feed": {"daybook", "civic"},
		"gazette": {"broadsheet", "salmonpress"}, "manual": {"fieldguide", "bluebook"},
		"almanac": {"sunclock", "seedcalendar"}, "inbox": {"postbox", "airmail"},
		"catalog": {"apothecary", "toolroom"}, "broadcast": {"publicradio", "morningfm"},
		"exhibit": {"whitecube", "botanical"},
	}
	if len(pairs) != 29 {
		t.Fatalf("light theme layout count = %d, want 29", len(pairs))
	}

	registered := make(map[string]bool, len(Themes))
	for _, theme := range Themes {
		registered[theme.ID] = true
	}
	for layout, ids := range pairs {
		for _, id := range ids {
			if !registered[id] {
				t.Errorf("light theme %q is not registered", id)
			}
			if got := layoutForTheme(id); got != layout {
				t.Errorf("light theme %q layout = %q, want %q", id, got, layout)
			}
		}
	}
}

var publicCSSCache string

func publicCSSHasTheme(t *testing.T, h http.Handler, cookie *http.Cookie, id string) bool {
	t.Helper()
	if publicCSSCache == "" {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test/assets/css/public.css", nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("fetch public.css status = %d", rec.Code)
		}
		publicCSSCache = rec.Body.String()
	}
	return strings.Contains(publicCSSCache, `[data-theme="`+id+`"]`)
}
