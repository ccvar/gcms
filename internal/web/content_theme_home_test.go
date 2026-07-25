package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

// TestWeb3GuideTemplatesDoNotBakeInDemoContent 防止把设计稿里的交易所名、
// 栏目名或文章文案误写进公共模板。三套骨架只能消费既有 Site、Menu、
// Categories、Posts 与 FeatLinks 数据；编号和箭头只承担布局语义。
func TestWeb3GuideTemplatesDoNotBakeInDemoContent(t *testing.T) {
	forbidden := []string{
		"Binance", "OKX", "币安", "欧易",
		"选择交易所，不该只看注册奖励", "开户前检查", "平台指南",
		"先弄清规则，再决定在哪里注册", "从第一次比较，到安全完成注册",
		"选择平台", "核验资格", "完成注册", "最新更新",
	}
	for _, name := range []string{
		"home_briefing_desk.html",
		"home_decision_wall.html",
		"home_route_atlas.html",
	} {
		body, err := os.ReadFile(filepath.Join("..", "..", "templates", "partials", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, fixed := range forbidden {
			if strings.Contains(string(body), fixed) {
				t.Errorf("%s hardcodes demo content %q", name, fixed)
			}
		}
	}
}

// TestEditorialCollectionTemplatesDoNotBakeInDemoContent keeps the three
// editorial collection skeletons connected to CMS data instead of mockup copy.
func TestEditorialCollectionTemplatesDoNotBakeInDemoContent(t *testing.T) {
	forbidden := []string{
		"How do small teams build", "Question of the week", "Popular questions",
		"Inside the practice", "Portrait Journal", "Recent conversations",
		"Selected case", "Industry index", "Casebook",
	}
	for _, name := range []string{
		"home_answer_desk.html",
		"home_portrait_journal.html",
		"home_casebook.html",
		"home_shelf_index.html",
		"home_tradeoff_sheet.html",
		"home_progress_bulletin.html",
		"home_margin_reading_room.html",
		"home_light_table.html",
		"home_counterpoint.html",
		"home_seamless_canvas.html",
		"home_night_corridor.html",
		"home_open_ascent.html",
	} {
		body, err := os.ReadFile(filepath.Join("..", "..", "templates", "partials", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, fixed := range forbidden {
			if strings.Contains(string(body), fixed) {
				t.Errorf("%s hardcodes mockup content %q", name, fixed)
			}
		}
		for _, binding := range []string{".Site.", ".Posts"} {
			if !strings.Contains(string(body), binding) {
				t.Errorf("%s does not consume CMS binding %q", name, binding)
			}
		}
	}
}

// TestAnswerDeskHeroUsesSiteProfile verifies that the right-hand Hero slot is
// driven by the existing backend profile fields instead of theme-local assets.
func TestAnswerDeskHeroUsesSiteProfile(t *testing.T) {
	s := newTestPublicServer(t, "")
	for key, value := range map[string]string{
		"theme":                 "answer-desk",
		"site.hero_eyebrow":     "动态眉标",
		"site.hero_title":       "动态问答标题",
		"site.hero_description": "动态问答说明",
		"hero.visual":           "image",
		"hero.image":            "/uploads/answer-desk-hero.webp",
	} {
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	s.clearGeneratedCaches()

	render := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("home status = %d, body = %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	body := render()
	for _, want := range []string{
		"动态眉标", "动态问答标题", "动态问答说明",
		`src="/uploads/answer-desk-hero.webp"`,
		`action="/zh/search"`, `name="q"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("image Hero render missing %q", want)
		}
	}

	if err := s.store.SetSetting("hero.visual", "svg"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting("hero.svg", `<svg data-hero-contract="answer-desk"></svg>`); err != nil {
		t.Fatal(err)
	}
	s.clearGeneratedCaches()
	body = render()
	if !strings.Contains(body, `data-hero-contract="answer-desk"`) {
		t.Error("SVG Hero render does not consume hero.svg")
	}
	if strings.Contains(body, `/uploads/answer-desk-hero.webp`) {
		t.Error("SVG Hero render leaked the image-mode asset")
	}
}

func TestHeroFirstContentThemesFollowConfiguredVisualMode(t *testing.T) {
	s := newTestPublicServer(t, "")
	const configuredImage = "/uploads/configured-content-hero.webp"
	const configuredSVG = `<svg data-content-hero-contract="configured"></svg>`

	for _, theme := range []string{"shelf-index", "tradeoff-sheet", "light-table"} {
		t.Run(theme, func(t *testing.T) {
			if err := s.store.SetSetting("theme", theme); err != nil {
				t.Fatal(err)
			}
			if err := s.store.SetSetting("hero.image", configuredImage); err != nil {
				t.Fatal(err)
			}
			if err := s.store.SetSetting("hero.svg", configuredSVG); err != nil {
				t.Fatal(err)
			}

			render := func(visual string) string {
				t.Helper()
				if err := s.store.SetSetting("hero.visual", visual); err != nil {
					t.Fatal(err)
				}
				s.clearGeneratedCaches()
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("visual %q status = %d", visual, rec.Code)
				}
				return rec.Body.String()
			}

			defaultBody := render("")
			if !strings.Contains(defaultBody, `class="content-skeleton-hero-anim"`) ||
				!strings.Contains(defaultBody, `class="hero-svg hero-anim2"`) {
				t.Error("default visual does not render animation 2")
			}
			if strings.Contains(defaultBody, configuredImage) || strings.Contains(defaultBody, `data-content-hero-contract`) {
				t.Error("default visual leaked a dormant image or SVG setting")
			}

			anim1Body := render("anim1")
			if !strings.Contains(anim1Body, `class="content-skeleton-hero-anim"`) ||
				!strings.Contains(anim1Body, `class="hero-svg"`) ||
				strings.Contains(anim1Body, `class="hero-svg hero-anim2"`) {
				t.Error("anim1 visual does not render animation 1")
			}
			if strings.Contains(anim1Body, configuredImage) || strings.Contains(anim1Body, `data-content-hero-contract`) {
				t.Error("anim1 visual leaked a dormant image or SVG setting")
			}

			imageBody := render("image")
			if !strings.Contains(imageBody, `src="`+configuredImage+`"`) {
				t.Error("image visual does not render the configured Hero image")
			}

			svgBody := render("svg")
			if !strings.Contains(svgBody, `data-content-hero-contract="configured"`) {
				t.Error("SVG visual does not render the configured Hero SVG")
			}
			if strings.Contains(svgBody, configuredImage) {
				t.Error("SVG visual leaked the image-mode asset")
			}
		})
	}
}

func TestArticleFirstContentThemesUseFeaturedCoverByDefault(t *testing.T) {
	s := newTestPublicServer(t, "")
	const configuredImage = "/uploads/dormant-content-hero.webp"
	const featuredCover = "/uploads/featured-article-cover.webp"
	const configuredSVG = `<svg data-article-hero-contract="configured"></svg>`

	id, err := s.store.CreatePost(&store.Post{
		Type:        "post",
		Lang:        "zh",
		Slug:        "article-first-hero-contract",
		Title:       "文章型首页视觉契约",
		Excerpt:     "默认使用精选文章封面。",
		Content:     "正文",
		CoverImage:  featuredCover,
		Status:      "published",
		EditorMode:  "markdown",
		PublishedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetFeatured(id, true); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting("hero.image", configuredImage); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting("hero.svg", configuredSVG); err != nil {
		t.Fatal(err)
	}

	for _, theme := range []string{"progress-bulletin", "margin-reading-room", "counterpoint"} {
		t.Run(theme, func(t *testing.T) {
			if err := s.store.SetSetting("theme", theme); err != nil {
				t.Fatal(err)
			}

			render := func(visual string) string {
				t.Helper()
				if err := s.store.SetSetting("hero.visual", visual); err != nil {
					t.Fatal(err)
				}
				s.clearGeneratedCaches()
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("visual %q status = %d", visual, rec.Code)
				}
				return rec.Body.String()
			}

			for _, visual := range []string{"", "anim1"} {
				body := render(visual)
				if !strings.Contains(body, `src="`+featuredCover+`"`) {
					t.Errorf("visual %q does not retain the featured article cover", visual)
				}
				if strings.Contains(body, configuredImage) ||
					strings.Contains(body, `data-article-hero-contract`) ||
					strings.Contains(body, `class="content-skeleton-hero-anim"`) {
					t.Errorf("visual %q leaked an explicit asset or animation into an article-first theme", visual)
				}
			}

			imageBody := render("image")
			if !strings.Contains(imageBody, `src="`+configuredImage+`"`) {
				t.Error("image visual does not render the configured Hero image")
			}

			svgBody := render("svg")
			if !strings.Contains(svgBody, `data-article-hero-contract="configured"`) {
				t.Error("SVG visual does not render the configured Hero SVG")
			}
		})
	}
}

// TestContentThemesHonorHomePostLimitWithMultipleFeatured 锁定独立内容骨架的
// 首页数量口径：后台设置 N 条时，渲染 1 篇主推 + N-1 篇列表；除主推外的
// 置顶文章仍进入列表，且第 N+1 篇不会越界出现。
func TestContentThemesHonorHomePostLimitWithMultipleFeatured(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(homePostsPerPageKey, "6"); err != nil {
		t.Fatalf("set home posts per page: %v", err)
	}

	now := time.Now().UTC().Add(-time.Hour)
	ids := make([]int64, 7)
	for i := range ids {
		title := fmt.Sprintf("内容骨架计数文章 %02d", i+1)
		id, err := s.store.CreatePost(&store.Post{
			Type:        "post",
			Lang:        "zh",
			Slug:        fmt.Sprintf("content-theme-count-%02d", i+1),
			Title:       title,
			Excerpt:     title + "摘要",
			Content:     title + "正文",
			Status:      "published",
			EditorMode:  "markdown",
			PublishedAt: now.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("create post %d: %v", i+1, err)
		}
		ids[i] = id
	}
	// 最新 5 篇全部置顶：旧拆分会把其中 4 篇从列表中丢掉。
	for _, id := range ids[2:] {
		if err := s.store.SetFeatured(id, true); err != nil {
			t.Fatalf("feature post %d: %v", id, err)
		}
	}

	for _, theme := range []string{
		"field-ledger", "signal-archive", "paper-current", "night-watch", "orbit-index", "column-stage", "type-cascade",
		"briefing-desk", "decision-wall", "route-atlas", "answer-desk", "portrait-journal", "casebook",
		"shelf-index", "tradeoff-sheet", "progress-bulletin", "margin-reading-room", "light-table", "counterpoint",
		"seamless-canvas", "night-corridor", "open-ascent",
	} {
		t.Run(theme, func(t *testing.T) {
			if err := s.store.SetSetting("theme", theme); err != nil {
				t.Fatalf("set theme: %v", err)
			}
			s.clearGeneratedCaches()

			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("home status = %d, body = %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			firstVisible, lastVisible := 2, 7
			// 这三套媒体首屏把文章放进固定的视觉槽：夜廊保留五条导读，
			// 天阶保留三条索引和一条主推。其余内容骨架仍展示完整的 N-1 条列表。
			switch theme {
			case "night-corridor":
				lastVisible = 6
			case "open-ascent":
				firstVisible = 4
			}
			for i := 2; i <= 7; i++ {
				want := fmt.Sprintf("内容骨架计数文章 %02d", i)
				visible := strings.Contains(body, want)
				if i >= firstVisible && i <= lastVisible && !visible {
					t.Errorf("home missing %q", want)
				} else if (i < firstVisible || i > lastVisible) && visible {
					t.Errorf("home rendered out-of-slot %q", want)
				}
			}
			if strings.Contains(body, "内容骨架计数文章 01") {
				t.Error("home rendered the seventh article beyond home.posts_per_page=6")
			}
		})
	}
}

// TestPaperCurrentArticleSeparatesReadingRailFromEnding 锁定 Paper Current
// 详情页的阅读阶段：目录只跟标题/正文共用两栏，标签、翻页与相关阅读在其后；
// 防止长文滚动到底时目录被整条 article-col 带到页脚旁边。
func TestPaperCurrentArticleSeparatesReadingRailFromEnding(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "paper-current"); err != nil {
		t.Fatalf("set theme: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type:        "post",
		Lang:        "zh",
		Slug:        "paper-current-reading-rail",
		Title:       "纸上潮汐长文布局",
		Content:     "## 第一节\n\n正文内容。\n\n## 第二节\n\n更多正文。",
		Status:      "published",
		EditorMode:  "markdown",
		PublishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}
	s.clearGeneratedCaches()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/posts/paper-current-reading-rail/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("article status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="article-grid has-toc pc-article-grid"`,
		`class="article-main"`,
		`class="toc-rail"`,
		`class="article-reading"`,
		`class="article-foot"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("paper-current article missing %q", want)
		}
	}
	if strings.Count(body, `class="toc-rail"`) != 1 {
		t.Fatalf("paper-current article renders %d TOC rails, want 1", strings.Count(body, `class="toc-rail"`))
	}
	mainAt := strings.Index(body, `class="article-main"`)
	tocAt := strings.Index(body, `class="toc-rail"`)
	readingAt := strings.Index(body, `class="article-reading"`)
	footAt := strings.Index(body, `class="article-foot"`)
	if !(mainAt < tocAt && tocAt < readingAt && readingAt < footAt) {
		t.Fatalf("paper-current reading order = main:%d toc:%d reading:%d foot:%d", mainAt, tocAt, readingAt, footAt)
	}

	css := getBody(t, s.Handler(), "/assets/css/public.css", http.StatusOK)
	for _, want := range []string{
		`[data-theme^="paper-current"] .article-grid.pc-article-grid.has-toc`,
		`[data-theme^="paper-current"] .pc-article-grid.has-toc .article-reading`,
		`[data-theme^="paper-current"] .pc-article-grid.has-toc .toc`,
		`[data-theme^="paper-current"] .pc-article-grid .article-col`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("public.css missing Paper Current article contract %q", want)
		}
	}
}
