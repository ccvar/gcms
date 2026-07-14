package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestAPISimilarContent 查重端点：read scope 即可；返回 {ok,rows:[{id,title,slug,status,lang,score}]}，
// 草稿计入，title 缺失 400。
func TestAPISimilarContent(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read")
	if _, err := s.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "similar-seed", Title: "站内查重专用测试标题", Status: "draft"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	get := func(query string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts/similar?"+query, nil)
		r.SetPathValue("collection", "posts")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.apiSimilarContent(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}

	w, out := get("title=" + url.QueryEscape("站内查重专用测试标题扩展版") + "&lang=zh")
	if w.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("similar = %d %v", w.Code, out)
	}
	rows := out["rows"].([]any)
	found := false
	for _, raw := range rows {
		row := raw.(map[string]any)
		score := row["score"].(float64)
		if score < 0 || score > 1 {
			t.Fatalf("score 越界：%v", row)
		}
		if row["slug"] == "similar-seed" {
			found = true
			if row["status"] != "draft" || row["lang"] != "zh" || row["title"] != "站内查重专用测试标题" {
				t.Fatalf("row 字段不完整：%v", row)
			}
		}
	}
	if !found {
		t.Fatalf("草稿未进入查重结果：%v", rows)
	}

	if w, out := get("lang=zh"); w.Code != http.StatusBadRequest || out["error"] != "bad_request" {
		t.Fatalf("缺 title = %d %v, want 400", w.Code, out)
	}
}

// TestAPISimilarScope 无 read scope → 403。
func TestAPISimilarScope(t *testing.T) {
	s, token := newTestAutomationServer(t, "links:read")
	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts/similar?title=abc", nil)
	r.SetPathValue("collection", "posts")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.apiSimilarContent(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("similar without scope = %d, want 403; body = %s", w.Code, w.Body.String())
	}
}

// TestSimilarRoutePrecedence 走真实 mux：/{collection}/similar 的字面 similar 段
// 必须命中查重处理器，而不是被 /{collection}/{id} 通配当成 ID 解析。
func TestSimilarRoutePrecedence(t *testing.T) {
	s := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("similar", token, prefix, "posts:read"); err != nil {
		t.Fatalf("create key: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts/similar?title="+url.QueryEscape("GCMS 内容管理"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	// 命中查重处理器 → 200 {ok:true}；被 {id} 通配吞掉会是 400 bad_id。
	if w.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("similar route = %d %v, want 200 ok", w.Code, out)
	}
}
