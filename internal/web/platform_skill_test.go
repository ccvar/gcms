package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
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
	for _, needle := range []string{"fetchSites", "resolveSite", "extractSite", `cmd === "capabilities"`, `"/control/capabilities"`, `cmd === "sites"`, `"/sites/"`, "--site",
		`cmd === "control-sites"`, `cmd === "site-create-plan"`, `cmd === "site-create"`, `cmd === "themes"`, `cmd === "theme-plan"`,
		`cmd === "domains-plan"`, `cmd === "security-status"`, `cmd === "category-delete-plan"`, `cmd === "category-delete"`,
		`"categories.delete"`, `cmd === "navigation-delete-plan"`, `cmd === "navigation-delete"`, `"navigation.delete"`,
		`cmd === "site-profile"`, `cmd === "site-profile-update"`, `cmd === "navigation"`, `cmd === "navigation-update"`,
		`cmd === "pin"`, `"/featured/"`} {
		if !strings.Contains(cli, needle) {
			t.Fatalf("gcms.js missing %q", needle)
		}
	}
	for _, forbidden := range []string{`cmd === "unlock"`, "passwordFromArg", "GCMS_ADMIN_PASSWORD"} {
		if strings.Contains(cli, forbidden) {
			t.Fatalf("gcms.js must not collect admin passwords; found %q", forbidden)
		}
	}
	if skill := entries[platformSkillFolder+"/SKILL.md"]; !strings.Contains(skill, "AI 不得") || !strings.Contains(skill, "Pilot UI") ||
		!strings.Contains(skill, "category-delete-plan") || !strings.Contains(skill, "navigation-delete-plan") {
		t.Fatalf("SKILL.md missing UI-only password boundary")
	}
	controlSpec := entries[platformSkillFolder+"/references/control-api.json"]
	if !strings.Contains(controlSpec, `"/control/sites/{siteId}"`) || !strings.Contains(controlSpec, controlIdempotencyHeader) {
		t.Fatalf("control-api.json missing management contract: %q", controlSpec)
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

func TestPlatformCLIControlledDeleteCommands(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping controlled-delete CLI test")
	}
	const (
		prefix      = "/api/platform/v1"
		unlockToken = "gcmsu_test_unlock"
	)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == prefix+"/control/sites" {
			_, _ = io.WriteString(w, `{"items":[{"id":12,"slug":"blog","name":"Blog"}]}`)
			return
		}
		var operation string
		switch r.URL.Path {
		case prefix + "/control/sites/12/categories/posts/42":
			operation = "categories.delete"
		case prefix + "/control/sites/12/navigation/0":
			operation = "navigation.delete"
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"unexpected_path"}`)
			return
		}
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = io.WriteString(w, `{"error":"unexpected_method"}`)
			return
		}
		if r.URL.Query().Get("dry_run") == "1" {
			if r.Header.Get(controlConfirmHeader) != "" || r.Header.Get(controlIdempotencyHeader) != "" ||
				r.Header.Get(controlUnlockHeader) != "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"plan_must_not_send_mutation_headers"}`)
				return
			}
			_, _ = io.WriteString(w, `{"dry_run":true,"operation":"`+operation+`","impact_revision":"plan-revision-123"}`)
			return
		}
		if r.Header.Get(controlConfirmHeader) != operation ||
			r.Header.Get(controlIdempotencyHeader) == "" ||
			r.Header.Get(controlUnlockHeader) != unlockToken {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"missing_control_headers"}`)
			return
		}
		if operation == "navigation.delete" && r.URL.Query().Get("expected_url") != "/pricing" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"missing_expected_url"}`)
			return
		}
		if r.URL.Query().Get("expected_revision") != "plan-revision-123" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"missing_expected_revision"}`)
			return
		}
		_, _ = io.WriteString(w, `{"deleted":true,"operation":"`+operation+`"}`)
	})
	ts := httptest.NewServer(h)
	defer ts.Close()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "gcms.js")
	if err := os.WriteFile(scriptPath, []byte(platformSkillScript()), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	run := func(args ...string) (string, string, error) {
		cmd := exec.Command(node, append([]string{scriptPath}, args...)...)
		cmd.Env = append(os.Environ(),
			"GCMS_API_BASE="+ts.URL+prefix,
			"GCMS_API_KEY=gcmsp_test_controlled_delete",
			"GCMS_CONTROL_UNLOCK_TOKEN="+unlockToken,
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "category plan",
			args: []string{"category-delete-plan", "--site", "blog", "posts", "42"},
			want: `"dry_run": true`,
		},
		{
			name: "category apply",
			args: []string{"category-delete", "--site", "blog", "posts", "42", "--expected-revision", "plan-revision-123", "--confirm", "true", "--request-id", "delete-category-42"},
			want: `"operation": "categories.delete"`,
		},
		{
			name: "navigation plan",
			args: []string{"navigation-delete-plan", "--site", "blog", "0"},
			want: `"dry_run": true`,
		},
		{
			name: "navigation apply",
			args: []string{"navigation-delete", "--site", "blog", "0", "--expected-url", "/pricing", "--expected-revision", "plan-revision-123", "--confirm", "true", "--request-id", "delete-navigation-0"},
			want: `"operation": "navigation.delete"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, err := run(tc.args...)
			if err != nil {
				t.Fatalf("command failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output missing %q: %s", tc.want, out)
			}
		})
	}

	_, errOut, err := run("category-delete", "--site", "blog", "posts", "42", "--expected-revision", "plan-revision-123")
	if err == nil || !strings.Contains(errOut, "--confirm true") {
		t.Fatalf("category delete without confirmation should fail: err=%v stderr=%s", err, errOut)
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
	if !names[platformSkillFolder+"/scripts/gcms.js"] || !names[platformSkillFolder+"/references/control-api.json"] || !names[platformSkillFolder+"/.env.example"] {
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
		"posts:read,posts:write,posts:categories,posts:pin,links:read,links:write,links:categories,links:pin,languages:read,media:write,site:read,site:write,navigation:read,control:read,sites:create,themes:read,themes:apply", nil, time.Time{}); err != nil {
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

	// capabilities: platform-control contract is discoverable without selecting a site.
	out, errOut, err := run("capabilities")
	if err != nil {
		t.Fatalf("capabilities failed: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, `"phase": "control-v1"`) || !strings.Contains(out, `"sites.create"`) {
		t.Fatalf("capabilities output unexpected: %s", out)
	}

	// control-sites uses the management discovery endpoint and can include disabled members.
	out, errOut, err = run("control-sites")
	if err != nil {
		t.Fatalf("control-sites failed: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, `"slug": "blog"`) {
		t.Fatalf("control-sites output unexpected: %s", out)
	}

	// A real management write must round-trip through the generated CLI's plan,
	// confirmation and idempotency parsing. Replaying the same request-id must
	// return the original result instead of creating a duplicate site.
	childBody := `{"slug":"cli-child","name":"CLI Child"}`
	out, errOut, err = run("site-create-plan", childBody)
	if err != nil {
		t.Fatalf("site-create-plan failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"seed_mode": "empty"`) || !strings.Contains(out, `"management_automation_enabled": true`) {
		t.Fatalf("site-create-plan output unexpected: %s", out)
	}
	out, errOut, err = run("site-create", childBody, "--confirm", "true", "--request-id", "cli-create-001")
	if err != nil {
		t.Fatalf("site-create failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.Contains(out, `"slug": "cli-child"`) || !strings.Contains(out, `"created": true`) {
		t.Fatalf("site-create output unexpected: %s", out)
	}
	replayed, replayErrOut, replayErr := run("site-create", childBody, "--confirm", "true", "--request-id", "cli-create-001")
	if replayErr != nil || replayed != out {
		t.Fatalf("site-create replay mismatch: err=%v\nstdout: %s\nstderr: %s", replayErr, replayed, replayErrOut)
	}
	out, errOut, err = run("theme-plan", "--site", "cli-child", "magazine")
	if err != nil || !strings.Contains(out, `"theme": "magazine"`) || !strings.Contains(out, `"dry_run": true`) {
		t.Fatalf("theme-plan failed: err=%v\nstdout: %s\nstderr: %s", err, out, errOut)
	}

	// sites: discovery must list blog (all-mode key sees every enabled+automation site).
	out, errOut, err = run("sites")
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
	var createdPost struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal([]byte(out), &createdPost); err != nil || createdPost.Item.ID == 0 {
		t.Fatalf("decode created post: err=%v output=%s", err, out)
	}

	// pin posts on → off: the platform skill must route to the per-site featured
	// endpoint and preserve the dedicated posts:pin authorization contract.
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{value: "on", want: true},
		{value: "off", want: false},
	} {
		out, errOut, err = run("pin", "--site", "blog", "posts", strconv.FormatInt(createdPost.Item.ID, 10), tc.value)
		if err != nil {
			t.Fatalf("pin posts %s failed: %v\nstdout: %s\nstderr: %s", tc.value, err, out, errOut)
		}
		var got struct {
			Item apiContentItem `json:"item"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decode pin posts %s output: %v\n%s", tc.value, err, out)
		}
		if got.Item.Featured != tc.want {
			t.Fatalf("pin posts %s featured = %v, want %v; output=%s", tc.value, got.Item.Featured, tc.want, out)
		}
	}

	// Links use a distinct links:pin scope and the same CLI branch. Exercise both
	// transitions so neither collection can silently lose the platform route.
	out, errOut, err = run("create", "links", "--site", "blog", `{"title":"CLI Live Link","lang":"zh","status":"draft","link_url":"https://example.com"}`)
	if err != nil {
		t.Fatalf("create link failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	var createdLink struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal([]byte(out), &createdLink); err != nil || createdLink.Item.ID == 0 {
		t.Fatalf("decode created link: err=%v output=%s", err, out)
	}
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{value: "on", want: true},
		{value: "off", want: false},
	} {
		out, errOut, err = run("pin", "--site", "blog", "links", strconv.FormatInt(createdLink.Item.ID, 10), tc.value)
		if err != nil {
			t.Fatalf("pin links %s failed: %v\nstdout: %s\nstderr: %s", tc.value, err, out, errOut)
		}
		var got struct {
			Item apiContentItem `json:"item"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decode pin links %s output: %v\n%s", tc.value, err, out)
		}
		if got.Item.Featured != tc.want {
			t.Fatalf("pin links %s featured = %v, want %v; output=%s", tc.value, got.Item.Featured, tc.want, out)
		}
	}

	// list drafts on blog: the created post must appear (proves /sites/{id} prefixing round-trips).
	out, _, err = run("list", "posts", "--site", "blog", "--lang", "zh", "--status", "draft")
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(out, "CLI Live Draft") {
		t.Fatalf("list did not include the created draft: %s", out)
	}

	// site-profile roundtrip: localized copy and global homepage display settings share
	// the same command, while the latter stay at the PATCH top level.
	out, errOut, err = run("site-profile-update", "--site", "blog", `{"lang":"zh","hero_title":"CLI E2E Hero","home_links_limit":5,"home_posts_per_page":9}`)
	if err != nil {
		t.Fatalf("site-profile-update failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	out, errOut, err = run("site-profile", "--site", "blog")
	if err != nil {
		t.Fatalf("site-profile failed: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "CLI E2E Hero") ||
		!strings.Contains(out, `"home_links_limit": 5`) ||
		!strings.Contains(out, `"home_posts_per_page": 9`) {
		t.Fatalf("site-profile did not reflect the update: %s", out)
	}

	// theme-options: 主题配置槽契约（site:read）——CLI 实跑，覆盖 --site 前缀与端点契约。
	out, errOut, err = run("theme-options", "--site", "blog")
	if err != nil {
		t.Fatalf("theme-options failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.Contains(out, `"slots"`) || !strings.Contains(out, `"hero.visual"`) || !strings.Contains(out, `"layout"`) {
		t.Fatalf("theme-options output unexpected: %s", out)
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
