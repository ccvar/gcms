package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeLogoScale(t *testing.T) {
	cases := map[string]string{
		"":     "",
		"1":    "",
		"1.0":  "",
		"1.00": "",
		"0.8":  "0.8",
		"0.85": "0.85",
		".5":   "0.5",
		"2":    "2",
		"0.1":  "0.3", // 钳到下限
		"5":    "2",   // 钳到上限
		"-1":   "0.3",
		"abc":  "",
		"NaN":  "",
		"1.2":  "1.2",
		"0.87": "0.85", // 量化到 0.05 步进（表单 step 一致）
		"0.98": "",     // 量化后等于 1 → 不缩放
		"1.23": "1.25",
	}
	for in, want := range cases {
		if got := normalizeLogoScale(in); got != want {
			t.Fatalf("normalizeLogoScale(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLogoScaleRender Logo 缩放全链路：设置生效 → 页眉/页脚出 transform:scale；
// 值为 1 或未设置时不出内联缩放。
func TestLogoScaleRender(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("site.logo", "/uploads/logo.png"); err != nil {
		t.Fatalf("set logo: %v", err)
	}
	if err := s.store.SetSetting("site.logo_scale", "0.8"); err != nil {
		t.Fatalf("set scale: %v", err)
	}
	body := getBodyForLogoScale(t, s, "/zh")
	if !strings.Contains(body, "transform:scale(0.8)") {
		t.Fatalf("home missing logo scale style: %s", excerptAround(body, "brand-logo"))
	}

	// 恢复 1 → 不再输出缩放（直写 store 需手动清公共页缓存）
	if err := s.store.SetSetting("site.logo_scale", "1"); err != nil {
		t.Fatalf("reset scale: %v", err)
	}
	s.clearGeneratedCaches()
	body = getBodyForLogoScale(t, s, "/zh")
	if strings.Contains(body, "transform:scale(") {
		t.Fatalf("scale=1 should not emit transform: %s", excerptAround(body, "transform:scale"))
	}
}

// TestLogoScaleAdminSave 后台保存归一化：0.85 存原值，1 存空，越界钳制。
func TestLogoScaleAdminSave(t *testing.T) {
	s := newTestPublicServer(t, "")
	save := func(scale string) {
		t.Helper()
		form := url.Values{
			"site_name":       {"测试站"},
			"site_logo":       {"/uploads/logo.png"},
			"site_logo_scale": {scale},
		}
		req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/settings/site", form)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("save status = %d, body = %s", w.Code, w.Body.String())
		}
	}
	save("0.85")
	if got := s.store.Setting("site.logo_scale"); got != "0.85" {
		t.Fatalf("scale saved = %q, want 0.85", got)
	}
	save("1")
	if got := s.store.Setting("site.logo_scale"); got != "" {
		t.Fatalf("scale=1 should store empty, got %q", got)
	}
	save("9")
	if got := s.store.Setting("site.logo_scale"); got != "2" {
		t.Fatalf("scale out of range should clamp to 2, got %q", got)
	}
}

func getBodyForLogoScale(t *testing.T, s *Server, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, w.Code)
	}
	return w.Body.String()
}

func excerptAround(body, needle string) string {
	i := strings.Index(body, needle)
	if i < 0 {
		return "(needle not found)"
	}
	start := i - 120
	if start < 0 {
		start = 0
	}
	end := i + 240
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}
