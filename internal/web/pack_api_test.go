package web

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPISkillPackDownload 验证密钥鉴权的技能包下载：有效密钥拿到含 gcms.js 的 zip + 版本头；无密钥 401。
func TestAPISkillPackDownload(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read")

	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/skill-pack", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.apiDownloadSkillPack(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content-type = %q", got)
	}
	if w.Header().Get(packVersionHeader) == "" {
		t.Fatalf("missing %s header", packVersionHeader)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("bad zip: %v", err)
	}
	var hasScript, hasEnvExample, hasRawEnv, hasVersion bool
	for _, f := range zr.File {
		switch {
		case strings.HasSuffix(f.Name, "PACK_VERSION"):
			hasVersion = true
		case strings.HasSuffix(f.Name, "scripts/gcms.js"):
			hasScript = true
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if !strings.Contains(string(b), "relink") {
				t.Fatalf("pack gcms.js missing relink command (stale generator?)")
			}
		case strings.HasSuffix(f.Name, ".env.example"):
			hasEnvExample = true
		case strings.HasSuffix(f.Name, "/.env"):
			hasRawEnv = true
		}
	}
	if !hasScript || !hasEnvExample || !hasVersion {
		t.Fatalf("zip entries incomplete: script=%v envExample=%v version=%v", hasScript, hasEnvExample, hasVersion)
	}
	if hasRawEnv {
		t.Fatalf("API pack must NOT embed .env with key")
	}

	// 无密钥 → 401
	r2 := httptest.NewRequest(http.MethodGet, "/api/admin/v1/skill-pack", nil)
	w2 := httptest.NewRecorder()
	s.apiDownloadSkillPack(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("no-token expected 401, got %d", w2.Code)
	}
}

// TestAPISkillPackVersion 版本端点返回 JSON version；无密钥 401。
func TestAPISkillPackVersion(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read")
	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/skill-pack/version", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.apiSkillPackVersion(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version"`) {
		t.Fatalf("version status=%d body=%s", w.Code, w.Body.String())
	}
	w2 := httptest.NewRecorder()
	s.apiSkillPackVersion(w2, httptest.NewRequest(http.MethodGet, "/api/admin/v1/skill-pack/version", nil))
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("no-token expected 401, got %d", w2.Code)
	}
}
