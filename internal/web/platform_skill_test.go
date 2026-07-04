package web

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func platformZipEntries(t *testing.T, opts automationSkillOptions) map[string]string {
	t.Helper()
	files, err := platformSkillFiles(opts)
	if err != nil {
		t.Fatalf("platformSkillFiles: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.name] = f.body
	}
	return out
}

func TestPlatformSkillFilesTokenless(t *testing.T) {
	apiBase := "https://platform.test/api/platform/v1"
	entries := platformZipEntries(t, automationSkillOptions{apiBase: apiBase})

	cli, ok := entries[platformSkillFolder+"/scripts/gcms.js"]
	if !ok {
		t.Fatalf("pack missing gcms.js; entries: %v", keysOf(entries))
	}
	// The multi-site CLI must have discovery + per-site routing, not the single-site shape.
	for _, needle := range []string{"fetchSites", "resolveSite", "extractSite", `cmd === "sites"`, `"/sites/"`, "--site",
		`cmd === "site-profile"`, `cmd === "site-profile-update"`, `cmd === "navigation"`, `cmd === "navigation-update"`} {
		if !strings.Contains(cli, needle) {
			t.Fatalf("gcms.js missing %q", needle)
		}
	}

	// Tokenless pack ships .env.example with platform-root base + gcmsp_ placeholder, no live token.
	env, ok := entries[platformSkillFolder+"/.env.example"]
	if !ok {
		t.Fatalf("tokenless pack missing .env.example; entries: %v", keysOf(entries))
	}
	if !strings.Contains(env, "GCMS_API_BASE="+apiBase) {
		t.Fatalf(".env.example base = %q, want %s", env, apiBase)
	}
	if !strings.Contains(env, "GCMS_API_KEY=gcmsp_xxx") {
		t.Fatalf(".env.example key placeholder wrong: %q", env)
	}
	if _, live := entries[platformSkillFolder+"/.env"]; live {
		t.Fatalf("tokenless pack must not ship a live .env")
	}

	// No baked site list: the base ends at /api/platform/v1, never /sites/<n>.
	if strings.Contains(env, "/sites/") {
		t.Fatalf(".env base must be the platform root, got %q", env)
	}
}

func TestPlatformSkillFilesWithToken(t *testing.T) {
	apiBase := "https://platform.test/api/platform/v1"
	token := "gcmsp_livetoken123"
	entries := platformZipEntries(t, automationSkillOptions{apiBase: apiBase, token: token, name: "多站助手"})

	env, ok := entries[platformSkillFolder+"/.env"]
	if !ok {
		t.Fatalf("tokened pack missing live .env; entries: %v", keysOf(entries))
	}
	if !strings.Contains(env, "GCMS_API_KEY="+token) {
		t.Fatalf(".env token = %q, want %s", env, token)
	}
	if _, example := entries[platformSkillFolder+"/.env.example"]; example {
		t.Fatalf("tokened pack must not also ship .env.example")
	}
	// README should carry the blast-radius warning for a multi-site key.
	readme := entries["README.md"]
	if !strings.Contains(readme, "急停") || !strings.Contains(readme, "多个站点") {
		t.Fatalf("README missing multi-site blast-radius guidance")
	}
}

func TestPlatformSkillScriptNodeCheck(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS syntax check")
	}
	dir := t.TempDir()
	for name, script := range map[string]string{
		"platform.js": platformSkillScript(),
		"persite.js":  automationSkillScript(),
	} {
		scriptPath := filepath.Join(dir, name)
		if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		out, err := exec.Command(node, "--check", scriptPath).CombinedOutput()
		if err != nil {
			t.Fatalf("node --check %s failed: %v\n%s", name, err, out)
		}
	}
}

func TestAdminDownloadPlatformSkillRoute(t *testing.T) {
	_, h, ps, _, _ := setupPlatformAutomation(t)
	if err := ps.CreateAdminSession("pskill-token", "admin", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	cookie := &http.Cookie{Name: cookieName, Value: "pskill-token"}

	// GET → tokenless zip.
	getRec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/automation/platform-skill.zip", nil)
	getReq.AddCookie(cookie)
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET platform-skill.zip status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	if ct := getRec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q", ct)
	}
	names := zipEntryNames(t, getRec.Body.Bytes())
	if !names[platformSkillFolder+"/scripts/gcms.js"] || !names[platformSkillFolder+"/.env.example"] {
		t.Fatalf("zip entries missing expected files: %v", names)
	}
	if names[platformSkillFolder+"/.env"] {
		t.Fatalf("GET pack must be tokenless")
	}

	// POST with a gcmsp_ token → live .env embedded.
	form := "token=gcmsp_posted12345&name=bot&_csrf=csrf"
	postRec := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/automation/platform-skill.zip", strings.NewReader(form))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(cookie)
	h.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST platform-skill.zip status = %d, body = %s", postRec.Code, postRec.Body.String())
	}
	env := zipEntryBody(t, postRec.Body.Bytes(), platformSkillFolder+"/.env")
	if !strings.Contains(env, "GCMS_API_KEY=gcmsp_posted12345") {
		t.Fatalf(".env did not embed posted token: %q", env)
	}

	// POST with a per-site gcms_ token → rejected (platform pack requires gcmsp_).
	badRec := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/automation/platform-skill.zip", strings.NewReader("token=gcms_wrongprefix&_csrf=csrf"))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badReq.AddCookie(cookie)
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("POST with gcms_ token status = %d, want 400", badRec.Code)
	}
}

// TestPlatformCLILiveEndToEnd runs the actual generated gcms.js with node against a live
// platform backend, proving discovery + --site prefixing + a real content flow work together.
func TestPlatformCLILiveEndToEnd(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping live CLI test")
	}
	// Local baseURL so requests to the httptest 127.0.0.1 host pass platformHostAllowed.
	_, h, ps, _, blogSite := newPlatformTestServerBase(t, "http://127.0.0.1")
	ts := httptest.NewServer(h)
	defer ts.Close()

	token := "gcmsp_liveclitoken12345"
	if _, err := ps.CreatePlatformKey("cli", token, token[:13], "all",
		"posts:read,posts:write,posts:categories,links:categories,languages:read,media:write,site:read,site:write,navigation:read", nil, time.Time{}); err != nil {
		t.Fatalf("create platform key: %v", err)
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "gcms.js")
	if err := os.WriteFile(scriptPath, []byte(platformSkillScript()), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	apiBase := ts.URL + "/api/platform/v1"
	run := func(args ...string) (string, string, error) {
		cmd := exec.Command(node, append([]string{scriptPath}, args...)...)
		cmd.Env = append(os.Environ(), "GCMS_API_BASE="+apiBase, "GCMS_API_KEY="+token)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	// sites: discovery must list blog (all-mode key sees every enabled+automation site).
	out, errOut, err := run("sites")
	if err != nil {
		t.Fatalf("sites failed: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, `"slug": "blog"`) || !strings.Contains(out, `"all_sites": true`) {
		t.Fatalf("sites output unexpected: %s", out)
	}

	// doctor --site blog: should resolve the site and pass its checks.
	out, errOut, err = run("doctor", "--site", "blog")
	if err != nil {
		t.Fatalf("doctor failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.Contains(out, `"resolve_site"`) || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("doctor output unexpected: %s", out)
	}

	// create a draft on blog via slug selector.
	out, errOut, err = run("create", "posts", "--site", "blog", `{"title":"CLI Live Draft","lang":"zh","status":"draft"}`)
	if err != nil {
		t.Fatalf("create failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.Contains(out, "CLI Live Draft") {
		t.Fatalf("create output unexpected: %s", out)
	}

	// list drafts on blog: the created post must appear (proves /sites/{id} prefixing round-trips).
	out, _, err = run("list", "posts", "--site", "blog", "--lang", "zh", "--status", "draft")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(out, "CLI Live Draft") {
		t.Fatalf("list did not include the created draft: %s", out)
	}

	// site-profile roundtrip: PATCH via CLI then read back (新站建设 depends on these commands).
	out, errOut, err = run("site-profile-update", "--site", "blog", `{"lang":"zh","hero_title":"CLI E2E Hero"}`)
	if err != nil {
		t.Fatalf("site-profile-update failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	out, errOut, err = run("site-profile", "--site", "blog")
	if err != nil {
		t.Fatalf("site-profile failed: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "CLI E2E Hero") {
		t.Fatalf("site-profile did not reflect the update: %s", out)
	}

	// navigation read must work under navigation:read.
	out, errOut, err = run("navigation", "--site", "blog")
	if err != nil {
		t.Fatalf("navigation failed: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("navigation output unexpected: %s", out)
	}

	// numeric-id selector must also work.
	out, _, err = run("languages", "--site", strconv.FormatInt(blogSite.ID, 10))
	if err != nil {
		t.Fatalf("languages by numeric id failed: %v", err)
	}
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("languages output unexpected: %s", out)
	}

	// unknown site → non-zero exit + helpful message (no accidental unscoped call).
	out, errOut, err = run("list", "posts", "--site", "does-not-exist")
	if err == nil {
		t.Fatalf("expected failure for unknown site, got stdout: %s", out)
	}
	if !strings.Contains(errOut, "Unknown site") {
		t.Fatalf("unknown-site stderr unexpected: %s", errOut)
	}

	// missing --site on a content command → non-zero exit with guidance.
	_, errOut, err = run("list", "posts")
	if err == nil {
		t.Fatalf("expected failure when --site omitted")
	}
	if !strings.Contains(errOut, "--site") {
		t.Fatalf("missing-site stderr unexpected: %s", errOut)
	}
}

// zipEntryNames returns the set of entry names in a zip archive.
func zipEntryNames(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	out := map[string]bool{}
	for _, f := range zr.File {
		out[f.Name] = true
	}
	return out
}

func zipEntryBody(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	t.Fatalf("zip entry %q not found", name)
	return ""
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
