package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestAdminExtHubAndToggle 验证后台「扩展」hub 列出扩展类型，并能启用/停用（写入本站设置）。
func TestAdminExtHubAndToggle(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	// hub 页列出扩展类型
	req, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/extensions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("hub status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"商品", "相册", "/products"} {
		if !strings.Contains(body, want) {
			t.Fatalf("hub missing %q", want)
		}
	}

	// 启用 product
	req2, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/toggle", url.Values{"type": {"product"}, "on": {"1"}})
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("enable status = %d, want 303", w2.Code)
	}
	if !s.contentTypeActive("product") {
		t.Fatalf("product should be active after enabling")
	}

	// 停用 product
	req3, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/toggle", url.Values{"type": {"product"}, "on": {"0"}})
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req3)
	if w3.Code != http.StatusSeeOther {
		t.Fatalf("disable status = %d, want 303", w3.Code)
	}
	if s.contentTypeActive("product") {
		t.Fatalf("product should be inactive after disabling")
	}

	// 内置类型不可通过该入口切换
	req4, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/toggle", url.Values{"type": {"post"}, "on": {"1"}})
	w4 := httptest.NewRecorder()
	h.ServeHTTP(w4, req4)
	if w4.Code != http.StatusNotFound {
		t.Fatalf("toggling builtin should 404, got %d", w4.Code)
	}
}
