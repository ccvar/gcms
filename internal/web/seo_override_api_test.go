package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestAPISEOOverrides update 写 robots_override / canonical_override：合法值落库并回显，
// 非法 canonical 422 invalid_canonical，空串清除覆盖。
func TestAPISEOOverrides(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:write")
	id, err := s.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "seo-target", Title: "SEO 覆盖测试文章", Status: "draft"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	patch := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		ids := strconv.FormatInt(id, 10)
		r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/posts/"+ids, bytes.NewReader(raw))
		r.SetPathValue("collection", "posts")
		r.SetPathValue("id", ids)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.apiUpdateContent(w, r)
		return w
	}

	// 合法覆盖
	w := patch(map[string]any{"robots_override": "noindex, follow", "canonical_override": "https://origin.example.com/a"})
	if w.Code != http.StatusOK {
		t.Fatalf("set overrides = %d, body = %s", w.Code, w.Body.String())
	}
	var out struct {
		Item apiContentItem `json:"item"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Item.RobotsOverride != "noindex, follow" || out.Item.CanonicalOverride != "https://origin.example.com/a" {
		t.Fatalf("响应未回显覆盖：%+v", out.Item)
	}
	if p, _ := s.store.GetPostByID(id); p.RobotsOverride != "noindex, follow" || p.CanonicalOverride != "https://origin.example.com/a" {
		t.Fatalf("覆盖未落库：%+v", p)
	}

	// 非法 canonical → 422 invalid_canonical（相对路径 / 非 http(s)）
	for _, bad := range []string{"/relative/path", "ftp://example.com/x", "not a url"} {
		w := patch(map[string]any{"canonical_override": bad})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("canonical %q = %d, want 422; body = %s", bad, w.Code, w.Body.String())
		}
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &e)
		if e.Error != "invalid_canonical" {
			t.Fatalf("canonical %q error = %q, want invalid_canonical", bad, e.Error)
		}
	}

	// 空串清除覆盖
	if w := patch(map[string]any{"robots_override": "", "canonical_override": ""}); w.Code != http.StatusOK {
		t.Fatalf("clear overrides = %d", w.Code)
	}
	if p, _ := s.store.GetPostByID(id); p.RobotsOverride != "" || p.CanonicalOverride != "" {
		t.Fatalf("覆盖未清除：%+v", p)
	}
}

// TestAdminSavePreservesSEOOverrides 后台旧表单（不含 SEO 覆盖字段）保存时不能清掉 API 写入的覆盖；
// 带字段的表单则以表单值为准。
func TestAdminSavePreservesSEOOverrides(t *testing.T) {
	existing := &store.Post{Type: "post", Lang: "zh", Slug: "keep", Title: "保留测试", Status: "draft",
		RobotsOverride: "noindex, follow", CanonicalOverride: "https://origin.example.com/a"}

	// 表单没带字段 → 保留原值
	r := httptest.NewRequest(http.MethodPost, "/admin/posts/1", nil)
	r.PostForm = map[string][]string{"title": {"保留测试"}}
	next := &store.Post{Type: "post", Lang: "zh", Slug: "keep", Title: "保留测试"}
	preserveSEOOverrides(r, next, existing)
	if next.RobotsOverride != existing.RobotsOverride || next.CanonicalOverride != existing.CanonicalOverride {
		t.Fatalf("旧表单保存丢失 SEO 覆盖：%+v", next)
	}

	// 表单带字段（含清空）→ 以表单为准
	r2 := httptest.NewRequest(http.MethodPost, "/admin/posts/1", nil)
	r2.PostForm = map[string][]string{"robots_override": {""}, "canonical_override": {"https://b.example.com/x"}}
	next2 := &store.Post{Type: "post", RobotsOverride: "", CanonicalOverride: "https://b.example.com/x"}
	preserveSEOOverrides(r2, next2, existing)
	if next2.RobotsOverride != "" || next2.CanonicalOverride != "https://b.example.com/x" {
		t.Fatalf("表单值被 preserve 覆盖：%+v", next2)
	}
}
