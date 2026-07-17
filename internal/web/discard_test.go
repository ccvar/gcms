package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

// discardReq 直呼处理器发一次 discard/undiscard（admin v1 形状）。
func discardReq(t *testing.T, s *Server, token, collection string, id int64, method string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	ids := strconv.FormatInt(id, 10)
	r := httptest.NewRequest(method, "/api/admin/v1/"+collection+"/"+ids+"/discard", reader)
	r.SetPathValue("collection", collection)
	r.SetPathValue("id", ids)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	if method == http.MethodDelete {
		s.apiUndiscardContent(w, r)
	} else {
		s.apiDiscardContent(w, r)
	}
	return w
}

// TestAPIDiscardContract 钉住冻结契约：reason 必填 ≤200 字；只能标记草稿（非草稿 409
// not_draft）；重复标记＝更新理由（幂等）；DELETE 撤销；列表/详情带 discarded 与 discard_reason。
func TestAPIDiscardContract(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:write")

	draftID, err := s.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "waste-draft", Title: "报废契约测试草稿", Status: "draft"})
	if err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	pubID, err := s.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "live-post", Title: "已发布文章", Status: "published"})
	if err != nil {
		t.Fatalf("seed published: %v", err)
	}

	// reason 缺失 / 过长 → 400
	if w := discardReq(t, s, token, "posts", draftID, http.MethodPost, map[string]any{}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing reason = %d, want 400: %s", w.Code, w.Body.String())
	}
	if w := discardReq(t, s, token, "posts", draftID, http.MethodPost, map[string]any{"reason": strings.Repeat("长", 201)}); w.Code != http.StatusBadRequest {
		t.Fatalf("long reason = %d, want 400: %s", w.Code, w.Body.String())
	}
	// 恰 200 字可接受
	if w := discardReq(t, s, token, "posts", draftID, http.MethodPost, map[string]any{"reason": strings.Repeat("好", 200)}); w.Code != http.StatusOK {
		t.Fatalf("200-rune reason = %d, want 200: %s", w.Code, w.Body.String())
	}

	// 非草稿 → 409 not_draft（冻结契约错误码）
	w := discardReq(t, s, token, "posts", pubID, http.MethodPost, map[string]any{"reason": "不该允许"})
	var conflict struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &conflict)
	if w.Code != http.StatusConflict || conflict.Error != "not_draft" {
		t.Fatalf("published discard = %d %q, want 409 not_draft: %s", w.Code, conflict.Error, w.Body.String())
	}
	if p, _ := s.store.GetPostByID(pubID); p.Discarded() {
		t.Fatalf("published post got marked")
	}

	// 重复标记＝更新理由（幂等 200），响应带契约字段
	w = discardReq(t, s, token, "posts", draftID, http.MethodPost, map[string]any{"reason": "与旧文重复选题"})
	if w.Code != http.StatusOK {
		t.Fatalf("re-mark = %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Item.Discarded || res.Item.DiscardReason != "与旧文重复选题" || res.Item.DiscardedAt == "" {
		t.Fatalf("item contract fields wrong: %+v", res.Item)
	}

	// 列表响应带字段（草稿在列，discarded=true；raw JSON 里必须出现键本身）
	rl := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts?status=draft&lang=zh", nil)
	rl.SetPathValue("collection", "posts")
	rl.Header.Set("Authorization", "Bearer "+token)
	wl := httptest.NewRecorder()
	s.apiListContent(wl, rl)
	if wl.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", wl.Code, wl.Body.String())
	}
	lb := wl.Body.String()
	if !strings.Contains(lb, `"discarded":true`) || !strings.Contains(lb, `"discard_reason":"与旧文重复选题"`) {
		t.Fatalf("list missing discard contract fields: %s", lb)
	}

	// DELETE 撤销 → discarded=false 且键仍存在（未标记时 discarded:false / discard_reason:""）
	w = discardReq(t, s, token, "posts", draftID, http.MethodDelete, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("undiscard = %d: %s", w.Code, w.Body.String())
	}
	ub := w.Body.String()
	if !strings.Contains(ub, `"discarded":false`) || !strings.Contains(ub, `"discard_reason":""`) {
		t.Fatalf("undiscard response missing contract fields: %s", ub)
	}
	if p, _ := s.store.GetPostByID(draftID); p.Discarded() {
		t.Fatalf("undiscard not persisted")
	}
	// 幂等：再撤一次仍 200
	if w := discardReq(t, s, token, "posts", draftID, http.MethodDelete, nil); w.Code != http.StatusOK {
		t.Fatalf("undiscard twice = %d", w.Code)
	}
}

// TestAPIDiscardScope 权限口径：无 write 拒绝（403）；content:write 通配放行。
func TestAPIDiscardScope(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read")
	id, _ := s.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "scope-x", Title: "权限测试", Status: "draft"})
	if w := discardReq(t, s, token, "posts", id, http.MethodPost, map[string]any{"reason": "无权"}); w.Code != http.StatusForbidden {
		t.Fatalf("read-only discard = %d, want 403", w.Code)
	}
	if w := discardReq(t, s, token, "posts", id, http.MethodDelete, nil); w.Code != http.StatusForbidden {
		t.Fatalf("read-only undiscard = %d, want 403", w.Code)
	}

	s2, token2 := newTestAutomationServer(t, "content:write")
	id2, _ := s2.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "scope-y", Title: "通配权限测试", Status: "draft"})
	if w := discardReq(t, s2, token2, "posts", id2, http.MethodPost, map[string]any{"reason": "通配可标"}); w.Code != http.StatusOK {
		t.Fatalf("content:write discard = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestAPIPublishClearsDiscardMark 人的动作覆盖 AI 申请：API 把已标记草稿置为
// published 时标记随发布清除（响应与库内都干净）。
func TestAPIPublishClearsDiscardMark(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:write,posts:publish")
	id, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "pub-clear-api", Title: "发布清除报废标记测试",
		Excerpt: "发布清标测试摘要。", MetaDesc: "发布清标测试描述。",
		Content: strings.Repeat("发布清标测试正文。", 60), Status: "draft",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if w := discardReq(t, s, token, "posts", id, http.MethodPost, map[string]any{"reason": "AI 建议弃用"}); w.Code != http.StatusOK {
		t.Fatalf("mark = %d: %s", w.Code, w.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"status": "published"})
	ids := strconv.FormatInt(id, 10)
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/posts/"+ids, bytes.NewReader(body))
	r.SetPathValue("collection", "posts")
	r.SetPathValue("id", ids)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.apiUpdateContent(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		Item apiContentItem `json:"item"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res.Item.Status != "published" || res.Item.Discarded || res.Item.DiscardReason != "" {
		t.Fatalf("publish response still marked: %+v", res.Item)
	}
	if p, _ := s.store.GetPostByID(id); p.Discarded() {
		t.Fatalf("mark survived publish in store")
	}
}

// TestAPIDiscardExtCollection 扩展类型同样生效：products 草稿可标记/撤销，非草稿 409。
func TestAPIDiscardExtCollection(t *testing.T) {
	s, token := newTestAutomationServer(t, "products:read,products:write")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	draftID, err := s.store.CreatePost(&store.Post{Type: "product", Lang: "zh", Slug: "waste-prod", Title: "报废商品草稿", Status: "draft"})
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}
	pubID, err := s.store.CreatePost(&store.Post{Type: "product", Lang: "zh", Slug: "live-prod", Title: "在售商品", Status: "published"})
	if err != nil {
		t.Fatalf("seed live product: %v", err)
	}

	w := discardReq(t, s, token, "products", draftID, http.MethodPost, map[string]any{"reason": "型号信息全错"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"discarded":true`) {
		t.Fatalf("ext discard = %d: %s", w.Code, w.Body.String())
	}
	if w := discardReq(t, s, token, "products", pubID, http.MethodPost, map[string]any{"reason": "不该允许"}); w.Code != http.StatusConflict {
		t.Fatalf("ext published discard = %d, want 409: %s", w.Code, w.Body.String())
	}
	if w := discardReq(t, s, token, "products", draftID, http.MethodDelete, nil); w.Code != http.StatusOK {
		t.Fatalf("ext undiscard = %d: %s", w.Code, w.Body.String())
	}
}

// TestPlatformMirrorDiscardRoutes 平台镜像：/{collection}/{id}/discard 的 POST/DELETE
// 必须命中报废处理器（ServeMux 通配坑：字面尾段路由须逐条注册，钉住）。
func TestPlatformMirrorDiscardRoutes(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	_ = srv
	token := "gcmsp_discard1234567890"
	if _, err := ps.CreatePlatformKey("discard", token, token[:13], platform.KeyMembershipAll,
		"posts:read,posts:write", nil, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	prefix := "/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10)

	// 先经平台 API 建一篇草稿（拿到该站库内 id）
	body, _ := json.Marshal(map[string]any{"title": "平台镜像报废测试草稿", "lang": "zh", "status": "draft"})
	rec := platformAPIReq(t, h, http.MethodPost, prefix+"/posts", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("platform create = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Item apiContentItem `json:"item"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	db, _ := json.Marshal(map[string]any{"reason": "镜像路由验证"})
	rec = platformAPIReq(t, h, http.MethodPost, prefix+"/posts/"+strconv.FormatInt(created.Item.ID, 10)+"/discard", token, db)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"discarded":true`) {
		t.Fatalf("platform discard = %d: %s", rec.Code, rec.Body.String())
	}
	rec = platformAPIReq(t, h, http.MethodDelete, prefix+"/posts/"+strconv.FormatInt(created.Item.ID, 10)+"/discard", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"discarded":false`) {
		t.Fatalf("platform undiscard = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAdminDiscardListAndPurge 后台闭环：列表徽章 + 「待清理」筛选档 + 清空按钮 +
// 行内恢复；批量清空只删标记中的草稿。
func TestAdminDiscardListAndPurge(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()
	lang := s.defaultLang()

	marked, err := s.store.CreatePost(&store.Post{Type: "post", Lang: lang, Slug: "admin-waste", Title: "后台报废标记草稿", Status: "draft"})
	if err != nil {
		t.Fatalf("seed marked: %v", err)
	}
	plain, err := s.store.CreatePost(&store.Post{Type: "post", Lang: lang, Slug: "admin-plain", Title: "普通草稿", Status: "draft"})
	if err != nil {
		t.Fatalf("seed plain: %v", err)
	}
	if ok, _ := s.store.SetDiscard(marked, "选题与旧文重复"); !ok {
		t.Fatalf("mark failed")
	}

	// 列表页：AI 弃用徽章（含理由弹层）、待清理筛选档、清空按钮、行内恢复表单
	reqL, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/posts?lang="+lang, nil)
	wL := httptest.NewRecorder()
	h.ServeHTTP(wL, reqL)
	if wL.Code != http.StatusOK {
		t.Fatalf("list = %d", wL.Code)
	}
	lb := wL.Body.String()
	for _, want := range []string{"discard-badge", "选题与旧文重复", "status=discarded", "/admin/posts/discard-purge", "/undiscard"} {
		if !strings.Contains(lb, want) {
			t.Fatalf("admin list missing %q", want)
		}
	}

	// 待清理筛选：只见标记条目
	reqF, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/posts?lang="+lang+"&status=discarded", nil)
	wF := httptest.NewRecorder()
	h.ServeHTTP(wF, reqF)
	fb := wF.Body.String()
	if !strings.Contains(fb, "后台报废标记草稿") || strings.Contains(fb, "普通草稿") {
		t.Fatalf("discarded filter wrong: has-marked=%v has-plain=%v", strings.Contains(fb, "后台报废标记草稿"), strings.Contains(fb, "普通草稿"))
	}

	// 行内恢复（撤标记）
	reqU, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/posts/"+strconv.FormatInt(marked, 10)+"/undiscard", url.Values{"lang": {lang}})
	wU := httptest.NewRecorder()
	h.ServeHTTP(wU, reqU)
	if wU.Code != http.StatusSeeOther {
		t.Fatalf("undiscard = %d", wU.Code)
	}
	if p, _ := s.store.GetPostByID(marked); p.Discarded() {
		t.Fatalf("admin undiscard not persisted")
	}

	// 重新标记后批量清空：只删标记中的草稿，普通草稿保留
	if ok, _ := s.store.SetDiscard(marked, "再次标记"); !ok {
		t.Fatalf("re-mark failed")
	}
	reqP, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/posts/discard-purge", url.Values{"lang": {lang}})
	wP := httptest.NewRecorder()
	h.ServeHTTP(wP, reqP)
	if wP.Code != http.StatusSeeOther {
		t.Fatalf("purge = %d", wP.Code)
	}
	loc := wP.Header().Get("Location")
	if !strings.Contains(loc, "purged=1") {
		t.Fatalf("purge redirect = %q, want purged=1", loc)
	}
	if p, _ := s.store.GetPostByID(marked); p != nil {
		t.Fatalf("marked draft survived admin purge")
	}
	if p, _ := s.store.GetPostByID(plain); p == nil {
		t.Fatalf("plain draft wrongly purged")
	}
}

// TestAdminExtDiscardPurge 扩展类型后台同样生效：列表徽章 + 清空只删标记草稿 + 行内恢复。
func TestAdminExtDiscardPurge(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	h := s.Handler()
	lang := s.defaultLang()

	marked, err := s.store.CreatePost(&store.Post{Type: "product", Lang: lang, Slug: "ext-waste", Title: "报废商品草稿", Status: "draft"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	live, err := s.store.CreatePost(&store.Post{Type: "product", Lang: lang, Slug: "ext-live", Title: "在售商品", Status: "published"})
	if err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if ok, _ := s.store.SetDiscard(marked, "参数全错"); !ok {
		t.Fatalf("mark failed")
	}

	reqL, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/ext/product?lang="+lang, nil)
	wL := httptest.NewRecorder()
	h.ServeHTTP(wL, reqL)
	if wL.Code != http.StatusOK {
		t.Fatalf("ext list = %d", wL.Code)
	}
	lb := wL.Body.String()
	for _, want := range []string{"discard-badge", "参数全错", "status=discarded", "/admin/ext/product/discard-purge", "/undiscard"} {
		if !strings.Contains(lb, want) {
			t.Fatalf("ext list missing %q", want)
		}
	}

	reqP, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/product/discard-purge", url.Values{"lang": {lang}})
	wP := httptest.NewRecorder()
	h.ServeHTTP(wP, reqP)
	if wP.Code != http.StatusSeeOther {
		t.Fatalf("ext purge = %d", wP.Code)
	}
	if p, _ := s.store.GetPostByID(marked); p != nil {
		t.Fatalf("marked ext draft survived purge")
	}
	if p, _ := s.store.GetPostByID(live); p == nil {
		t.Fatalf("published ext item wrongly purged")
	}
}

// TestOpenAPIDiscardPaths OpenAPI 文档带上报废端点与契约字段（技能包 references/openapi.json 用）。
func TestOpenAPIDiscardPaths(t *testing.T) {
	spec := automationOpenAPISpec("https://example.test/api/admin/v1")
	paths, _ := spec["paths"].(map[string]any)
	entry, ok := paths["/posts/{id}/discard"].(map[string]any)
	if !ok || entry["post"] == nil || entry["delete"] == nil {
		t.Fatalf("openapi missing /posts/{id}/discard post+delete: %v", entry)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	if schemas["DiscardInput"] == nil {
		t.Fatalf("openapi missing DiscardInput schema")
	}
	item := schemas["ContentItem"].(map[string]any)["properties"].(map[string]any)
	if item["discarded"] == nil || item["discard_reason"] == nil {
		t.Fatalf("ContentItem schema missing discard fields")
	}
	// SKILL.md 教学段：discard 命令 + 旧服务端 404 降级说明（改为正文文字标注）
	md := automationSkillMarkdown("https://example.test/api/admin/v1")
	for _, want := range []string{"discard posts 123 --reason", "undiscard", "not_draft", "【建议弃用："} {
		if !strings.Contains(md, want) {
			t.Fatalf("SKILL.md missing %q", want)
		}
	}
}
