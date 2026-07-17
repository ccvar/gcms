package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestEffectiveWordCountMixed 中英混排：CJK 每字符记 1，拉丁按词记 1。
func TestEffectiveWordCountMixed(t *testing.T) {
	// 4 个 CJK 字 + 3 个拉丁词（Go / quality gate）
	if got := effectiveWordCount("用 Go 写质量 gate test"); got != 4+3 {
		t.Fatalf("mixed count = %d, want 7", got)
	}
}

// TestEffectiveWordCountPureCJK 纯 CJK：逐字符计数，标点不算。
func TestEffectiveWordCountPureCJK(t *testing.T) {
	if got := effectiveWordCount("发布质量门，硬校验。"); got != 8 {
		t.Fatalf("pure CJK count = %d, want 8", got)
	}
}

// TestStripMarkdownCounting markdown 剥离：标题/强调/链接/图片/代码围栏/列表都不虚增字数。
func TestStripMarkdownCounting(t *testing.T) {
	md := strings.Join([]string{
		"# 标题一",
		"",
		"**加粗** 和 *斜体* 以及 `行内代码`",
		"",
		"![配图](/uploads/a.webp)",
		"[锚文本](https://example.com)",
		"",
		"```go",
		"fmt.Println(\"code should not count\")",
		"```",
		"",
		"- 列表项",
		"1. 有序项",
		"",
		"---",
	}, "\n")
	text := stripMarkdown(md)
	if strings.Contains(text, "example.com") || strings.Contains(text, "/uploads/a.webp") || strings.Contains(text, "Println") {
		t.Fatalf("markdown not stripped: %q", text)
	}
	// 标题一(3) + 加粗(2) 和(1) 斜体(2) 以及(2) 行内代码(4) + 锚文本(3) + 列表项(3) + 有序项(3) = 23
	if got := effectiveWordCount(text); got != 23 {
		t.Fatalf("stripped count = %d, want 23 (text=%q)", got, text)
	}
}

// TestQualityGateFailures 逐项校验缺口消息格式。
func TestQualityGateFailures(t *testing.T) {
	p := &store.Post{Title: "短标题", Content: "太短", Excerpt: "", MetaDesc: ""}
	failures := qualityGateFailures("post", p)
	want := []string{"body_too_short (2/400)", "excerpt_missing", "meta_desc_missing", "title_too_short (3/8)"}
	if len(failures) != len(want) {
		t.Fatalf("failures = %v, want %v", failures, want)
	}
	for i := range want {
		if failures[i] != want[i] {
			t.Fatalf("failures[%d] = %q, want %q", i, failures[i], want[i])
		}
	}

	ok := &store.Post{
		Title:    "一篇足够长的合格标题",
		Excerpt:  "摘要",
		MetaDesc: "描述",
		Content:  strings.Repeat("合格正文内容。", 80), // 480 CJK 字
	}
	if got := qualityGateFailures("post", ok); len(got) != 0 {
		t.Fatalf("expected pass, got %v", got)
	}

	long := &store.Post{Title: strings.Repeat("长", 121), Excerpt: "x", MetaDesc: "x", Content: strings.Repeat("字", 400)}
	failures = qualityGateFailures("post", long)
	if len(failures) != 1 || failures[0] != "title_too_long (121/120)" {
		t.Fatalf("long title failures = %v", failures)
	}
}

// TestQualityGateProductRules 商品规则集逐分支：正文阈值 100、封面/图集至少 1、specs ≥3、
// 摘要/描述必填、标题 8~120；全部达标为空；没有规则集的类型恒通过。
func TestQualityGateProductRules(t *testing.T) {
	// 全缺：正文短 + 无摘要/描述 + 标题短 + 无图 + 无规格。
	bad := &store.Post{Type: "product", Title: "短", Content: "太短"}
	failures := qualityGateFailures("product", bad)
	want := []string{
		"body_too_short (2/100)", "excerpt_missing", "meta_desc_missing", "title_too_short (1/8)",
		"cover_or_gallery_missing (需要封面图或图集至少 1 张)", "specs_too_few (0/3)",
	}
	if len(failures) != len(want) {
		t.Fatalf("failures = %v, want %v", failures, want)
	}
	for i := range want {
		if failures[i] != want[i] {
			t.Fatalf("failures[%d] = %q, want %q", i, failures[i], want[i])
		}
	}

	// 正文 100 词即够（文章要 400）；封面为空但图集有图算过；specs 2 行仍拦。
	body := strings.Repeat("规格说明。", 30) // 150 CJK 字
	partial := &store.Post{
		Type: "product", Title: "精密加工件系列产品", Excerpt: "摘要", MetaDesc: "描述", Content: body,
		Extra: `{"gallery":["/u/a.webp"],"specs":[{"k":"型号","v":"A"},{"k":"材质","v":"B"}]}`,
	}
	failures = qualityGateFailures("product", partial)
	if len(failures) != 1 || failures[0] != "specs_too_few (2/3)" {
		t.Fatalf("partial failures = %v, want 仅 specs_too_few (2/3)", failures)
	}

	// 达标：封面 + specs 3 行。
	good := &store.Post{
		Type: "product", Title: "精密加工件系列产品", Excerpt: "摘要", MetaDesc: "描述", Content: body,
		CoverImage: "/u/cover.webp",
		Extra:      `{"specs":[{"k":"型号","v":"A"},{"k":"材质","v":"B"},{"k":"起订量","v":"500"}]}`,
	}
	if got := qualityGateFailures("product", good); len(got) != 0 {
		t.Fatalf("expected product pass, got %v", got)
	}

	// 商品正文 100~400 词之间：product 过、post 不过（阈值分型生效）。
	if got := qualityGateFailures("post", good); len(got) == 0 {
		t.Fatalf("同样正文按 post 规则应不达标（400 词）")
	}

	// 无规则集的类型：恒通过、gate 不适用。
	if got := qualityGateFailures("event", bad); got != nil {
		t.Fatalf("event 不该有规则集，got %v", got)
	}
	status := "published"
	if qualityGateApplies("event", &apiContentInput{Status: &status}, &store.Post{Status: "published"}) {
		t.Fatalf("event 不该命中质量门")
	}
	if !qualityGateApplies("product", &apiContentInput{Status: &status}, &store.Post{Status: "published"}) {
		t.Fatalf("product 显式发布应命中质量门")
	}
}

func qualityGatePostBody(t *testing.T, overrides map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"title":     "合格标题至少八个字符",
		"lang":      "zh",
		"status":    "published",
		"excerpt":   "合格摘要",
		"meta_desc": "合格描述",
		"content":   strings.Repeat("合格正文。", 100),
	}
	for k, v := range overrides {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestQualityGateOnCreate 创建即发布：不达标 422 quality_gate；达标 201；草稿不校验。
func TestQualityGateOnCreate(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:write,posts:publish")
	post := func(raw []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/posts", bytes.NewReader(raw))
		r.SetPathValue("collection", "posts")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.apiCreateContent(w, r)
		return w
	}

	// 不达标：正文过短 + 缺摘要
	w := post(qualityGatePostBody(t, map[string]any{"content": "太短", "excerpt": ""}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad publish = %d, want 422; body = %s", w.Code, w.Body.String())
	}
	var out struct {
		Error    string   `json:"error"`
		Failures []string `json:"failures"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Error != "quality_gate" || len(out.Failures) < 2 {
		t.Fatalf("gate response = %s", w.Body.String())
	}
	if !strings.HasPrefix(out.Failures[0], "body_too_short (") {
		t.Fatalf("failures[0] = %q", out.Failures[0])
	}

	// 草稿不校验
	if w := post(qualityGatePostBody(t, map[string]any{"status": "draft", "content": "太短", "excerpt": "", "slug": "draft-ok"})); w.Code != http.StatusCreated {
		t.Fatalf("draft = %d, want 201; body = %s", w.Code, w.Body.String())
	}

	// 达标发布
	if w := post(qualityGatePostBody(t, map[string]any{"slug": "pub-ok"})); w.Code != http.StatusCreated {
		t.Fatalf("good publish = %d, want 201; body = %s", w.Code, w.Body.String())
	}
}

// TestQualityGateOnUpdateStatusChange 更新把草稿改为 published 时校验；pages 不校验（仅 posts）。
func TestQualityGateOnUpdateStatusChange(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:write,posts:publish,pages:read,pages:write,pages:publish")
	postID, err := s.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "thin", Title: "一篇待发布的草稿标题", Status: "draft", Content: "太短"})
	if err != nil {
		t.Fatalf("seed post: %v", err)
	}
	patch := func(collection string, id int64, body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		ids := strconv.FormatInt(id, 10)
		r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/"+collection+"/"+ids, bytes.NewReader(raw))
		r.SetPathValue("collection", collection)
		r.SetPathValue("id", ids)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.apiUpdateContent(w, r)
		return w
	}

	if w := patch("posts", postID, map[string]any{"status": "published"}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("thin publish = %d, want 422; body = %s", w.Code, w.Body.String())
	}
	// 不改状态的草稿内容更新不拦
	if w := patch("posts", postID, map[string]any{"content": "还是太短"}); w.Code != http.StatusOK {
		t.Fatalf("draft patch = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	// 补齐后发布通过
	if w := patch("posts", postID, map[string]any{
		"status": "published", "excerpt": "摘要", "meta_desc": "描述", "content": strings.Repeat("合格正文。", 100),
	}); w.Code != http.StatusOK {
		t.Fatalf("good publish = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	// pages 不过质量门
	pageID, err := s.store.CreatePost(&store.Post{Type: "page", Lang: "zh", Slug: "thin-page", Title: "短页", Status: "draft", Content: "短"})
	if err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if w := patch("pages", pageID, map[string]any{"status": "published"}); w.Code != http.StatusOK {
		t.Fatalf("page publish = %d, want 200 (pages 不拦); body = %s", w.Code, w.Body.String())
	}
}
