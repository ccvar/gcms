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

// TestAPIExtTypeFieldsAndIntrospection 验证：(1) /types 自省返回已启用扩展类型的字段 schema；
// (2) 通过内容 API 创建扩展类型实例时，fields 写入 extra 且响应回带 fields；(3) 详情 URL type 化。
func TestAPIExtTypeFieldsAndIntrospection(t *testing.T) {
	s, token := newTestAutomationServer(t, "products:read,products:write,products:publish")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}

	// /types 自省
	rt := httptest.NewRequest(http.MethodGet, "/api/admin/v1/types", nil)
	rt.Header.Set("Authorization", "Bearer "+token)
	wt := httptest.NewRecorder()
	s.apiContentTypes(wt, rt)
	if wt.Code != http.StatusOK {
		t.Fatalf("types status = %d, body = %s", wt.Code, wt.Body.String())
	}
	tb := wt.Body.String()
	if !strings.Contains(tb, `"key":"product"`) || !strings.Contains(tb, `"key":"price"`) || !strings.Contains(tb, `"key":"gallery"`) {
		t.Fatalf("types introspection missing product schema: %s", tb)
	}

	// 创建带自定义字段的商品
	body, _ := json.Marshal(map[string]any{
		"title":  "API 商品",
		"status": "published",
		"fields": map[string]any{"price": 299, "gallery": []string{"/u/a.webp", "/u/b.webp"}},
	})
	rc := httptest.NewRequest(http.MethodPost, "/api/admin/v1/products", bytes.NewReader(body))
	rc.SetPathValue("collection", "products")
	rc.Header.Set("Authorization", "Bearer "+token)
	wc := httptest.NewRecorder()
	s.apiCreateContent(wc, rc)
	if wc.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", wc.Code, wc.Body.String())
	}
	var created struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal(wc.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Item.Type != "product" {
		t.Fatalf("created type = %q, want product", created.Item.Type)
	}
	if created.Item.Fields == nil || created.Item.Fields["price"] == nil {
		t.Fatalf("response missing fields.price: %+v", created.Item.Fields)
	}
	if !strings.Contains(created.Item.URL, "/products/") {
		t.Fatalf("response url not type-aware: %q", created.Item.URL)
	}

	// extra 实际落库
	p, _ := s.store.GetPostByID(created.Item.ID)
	if p == nil || !strings.Contains(p.Extra, "299") || !strings.Contains(p.Extra, "/u/a.webp") {
		t.Fatalf("extra not persisted: %v", p)
	}
}

// TestAPIRelinkContent 验证"重连互译组"：把独立创建的 zh 文章并入 en 的组，
// 并覆盖语种冲突 / 不存在的组 / 缺参数三种拒绝。
func TestAPIRelinkContent(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:write,posts:publish")

	create := func(lang, slug, group string) int64 {
		// 发布质量门：API 创建即发布的 posts 需要达标的正文/摘要/描述/标题长度。
		body, _ := json.Marshal(map[string]any{
			"title": lang + " " + slug + " 互译组测试文章", "slug": slug, "lang": lang, "trans_group": group,
			"status":    "published",
			"excerpt":   "互译组重连测试用摘要。",
			"meta_desc": "互译组重连测试用 SEO 描述。",
			"content":   strings.Repeat("互译组重连测试正文。", 60),
		})
		r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/posts", bytes.NewReader(body))
		r.SetPathValue("collection", "posts")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.apiCreateContent(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s status=%d body=%s", lang, w.Code, w.Body.String())
		}
		var res struct {
			Item apiContentItem `json:"item"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return res.Item.ID
	}
	relink := func(id int64, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		ids := strconv.FormatInt(id, 10)
		r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/posts/"+ids+"/relink", bytes.NewReader(b))
		r.SetPathValue("collection", "posts")
		r.SetPathValue("id", ids)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.apiRelinkContent(w, r)
		return w
	}

	enID := create("en", "about", "en:about")
	zhID := create("zh", "guanyu", "zh:guanyu")

	// happy path：zh 并入 en 的组
	if w := relink(zhID, map[string]any{"link_to_id": enID}); w.Code != http.StatusOK {
		t.Fatalf("relink expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if p, _ := s.store.GetPostByID(zhID); p == nil || p.TransGroup != "en:about" {
		t.Fatalf("zh trans_group not relinked: %+v", p)
	}
	if trs, _ := s.store.TranslationsAll("en:about", 0); len(trs) != 2 {
		t.Fatalf("group members = %d, want 2", len(trs))
	}

	zh2 := create("zh", "guanyu2", "zh:guanyu2")
	if w := relink(zh2, map[string]any{"link_to_id": enID}); w.Code != http.StatusBadRequest {
		t.Fatalf("lang collision expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if w := relink(zh2, map[string]any{"trans_group": "nope:nope"}); w.Code != http.StatusBadRequest {
		t.Fatalf("phantom group expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if w := relink(zh2, map[string]any{}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing params expected 400, got %d", w.Code)
	}
}

// TestAPIRelinkScope 无 write 权限的密钥不能重连。
func TestAPIRelinkScope(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read")
	id, err := s.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "x", Title: "x", TransGroup: "zh:x"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	ids := strconv.FormatInt(id, 10)
	b, _ := json.Marshal(map[string]any{"trans_group": "en:y"})
	r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/posts/"+ids+"/relink", bytes.NewReader(b))
	r.SetPathValue("collection", "posts")
	r.SetPathValue("id", ids)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.apiRelinkContent(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("no write scope expected 403, got %d: %s", w.Code, w.Body.String())
	}
}
