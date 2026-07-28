package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

func pageAppZipForTest(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func validPageAppFilesForTest() map[string]string {
	return map[string]string{
		pageAppManifestName: `{"schema_version":1,"entry":"index.html","viewport":"responsive","shell_mode":"site","capabilities":[{"name":"client.storage"}]}`,
		"index.html":        `<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="styles.css"><main id="app"></main><script src="app.js"></script>`,
		"styles.css":        `body{margin:0;background:url("assets/dot.svg")}`,
		"app.js":            `document.querySelector("#app").textContent = "ready";`,
		"assets/dot.svg":    `<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"><circle cx="2" cy="2" r="1"/></svg>`,
	}
}

func TestValidatePageAppPackageAndPersistImmutableBundle(t *testing.T) {
	raw := pageAppZipForTest(t, validPageAppFilesForTest())
	bundle, err := validatePageAppPackage(raw, pagePlatformServerLimits())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Entry != "index.html" || len(bundle.Hash) != 64 || bundle.TotalBytes == 0 {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "cms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uploadDir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root := st.PageProjectStorageDir()
	if root == "" || strings.HasPrefix(root, uploadDir+string(filepath.Separator)) {
		t.Fatalf("private project root leaked into uploads: root=%q uploads=%q", root, uploadDir)
	}
	ref, err := persistPageAppBundle(root, 41, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "sources/41/"+bundle.Hash {
		t.Fatalf("storage ref = %q", ref)
	}
	stored, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ref), "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != validPageAppFilesForTest()["index.html"] {
		t.Fatal("persisted entry content changed")
	}
	if retry, err := persistPageAppBundle(root, 41, bundle); err != nil || retry != ref {
		t.Fatalf("immutable retry = %q, %v", retry, err)
	}
}

func TestValidatePageAppPackageRejectsTraversalRemoteCodeAndWorkers(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]string)
		code string
	}{
		{
			name: "zip slip",
			edit: func(files map[string]string) { files["../escape.js"] = "alert(1)" },
			code: "unsafe_path",
		},
		{
			name: "remote script",
			edit: func(files map[string]string) {
				files["index.html"] = `<script src="https://evil.example/app.js"></script>`
			},
			code: "remote_script_forbidden",
		},
		{
			name: "remote module",
			edit: func(files map[string]string) {
				files["app.js"] = `import lib from "https://evil.example/lib.js";`
			},
			code: "remote_module_forbidden",
		},
		{
			name: "service worker",
			edit: func(files map[string]string) {
				files["app.js"] = `navigator.serviceWorker.register("/sw.js")`
			},
			code: "worker_forbidden",
		},
		{
			name: "remote css",
			edit: func(files map[string]string) {
				files["styles.css"] = `@import "https://evil.example/theme.css";`
			},
			code: "remote_resource_forbidden",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := validPageAppFilesForTest()
			tc.edit(files)
			_, err := validatePageAppPackage(pageAppZipForTest(t, files), pagePlatformServerLimits())
			var validationErr *pageAppValidationError
			if !errors.As(err, &validationErr) || validationErr.Code != tc.code {
				t.Fatalf("error = %#v, want %s", err, tc.code)
			}
		})
	}
}

func TestValidatePageAppPackageRejectsSymlinkAndCompressionBomb(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		var raw bytes.Buffer
		writer := zip.NewWriter(&raw)
		for name, content := range validPageAppFilesForTest() {
			entry, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
		header := &zip.FileHeader{Name: "linked.js", Method: zip.Store}
		header.SetMode(os.ModeSymlink | 0o777)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("../outside.js")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = validatePageAppPackage(raw.Bytes(), pagePlatformServerLimits())
		var invalid *pageAppValidationError
		if !errors.As(err, &invalid) || invalid.Code != "unsupported_file_type" {
			t.Fatalf("symlink error = %#v", err)
		}
	})

	t.Run("compression bomb", func(t *testing.T) {
		files := validPageAppFilesForTest()
		files["compressed.txt"] = strings.Repeat("a", 1<<20)
		_, err := validatePageAppPackage(pageAppZipForTest(t, files), pagePlatformServerLimits())
		var invalid *pageAppValidationError
		if !errors.As(err, &invalid) || invalid.Code != "compression_ratio_exceeded" {
			t.Fatalf("compression bomb error = %#v", err)
		}
	})
}

func TestPageAppTextEditorPolicyIsUTF8SizeAndExtensionBound(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content []byte
		code    string
	}{
		{name: "binary extension", path: "asset.svg", content: []byte("<svg/>"), code: "source_file_not_editable"},
		{name: "too large", path: "app.js", content: bytes.Repeat([]byte("x"), int(pageAppTextEditMaxBytes)+1), code: "source_file_too_large"},
		{name: "invalid utf8", path: "app.js", content: []byte{0xff}, code: "source_file_not_utf8"},
		{name: "nul", path: "app.js", content: []byte("a\x00b"), code: "source_file_not_utf8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePageAppTextEdit(tc.path, tc.content)
			var invalid *pageAppValidationError
			if !errors.As(err, &invalid) || invalid.Code != tc.code {
				t.Fatalf("error = %#v, want %s", err, tc.code)
			}
		})
	}
	if clean, err := validatePageAppTextEdit("nested/app.mjs", []byte("export default 1")); err != nil ||
		clean != "nested/app.mjs" {
		t.Fatalf("valid editable source clean=%q err=%v", clean, err)
	}
}

func TestPageAppSandboxPolicyDoesNotGrantSameOriginOrNetwork(t *testing.T) {
	attrs := pageAppIframeAttributes()
	if strings.Contains(attrs["sandbox"], "allow-same-origin") {
		t.Fatalf("unsafe iframe sandbox = %q", attrs["sandbox"])
	}
	csp := pageAppContentSecurityPolicy()
	for _, required := range []string{"default-src 'none'", "connect-src 'none'", "worker-src 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, required) {
			t.Fatalf("CSP missing %q: %s", required, csp)
		}
	}
	headers := pageAppResponseHeaders()
	if headers["Content-Security-Policy"] != csp || !strings.Contains(headers["Permissions-Policy"], "camera=()") ||
		headers["X-Content-Type-Options"] != "nosniff" ||
		headers["Cross-Origin-Resource-Policy"] != "cross-origin" {
		t.Fatalf("unsafe response headers: %#v", headers)
	}
	shellCSP := pageAppShellContentSecurityPolicy(
		"https://origin.example.test/_gcms/page-app-bridge/1/2",
	)
	for _, required := range []string{
		"default-src 'none'", "frame-src 'self'", "script-src 'unsafe-inline'",
		"style-src 'unsafe-inline'",
		"connect-src 'self' https://origin.example.test",
		"base-uri 'none'", "object-src 'none'", "form-action 'none'",
	} {
		if !strings.Contains(shellCSP, required) {
			t.Fatalf("parent shell CSP missing %q: %s", required, shellCSP)
		}
	}
	if strings.Contains(shellCSP, "/_gcms/page-app-bridge") {
		t.Fatalf("parent shell CSP should authorize only the bridge origin: %s", shellCSP)
	}
}

func TestPageAppBridgeRequiresExactContextAndApprovedGrant(t *testing.T) {
	raw, err := json.Marshal(pageAppBridgeRequest{
		Protocol: pageAppBridgeProtocol, RequestID: "req-1",
		ProjectID: 7, RevisionID: 11,
		Capability: "form.submit", Action: "submit",
		Payload: json.RawMessage(`{"form_id":"lead","fields":{"email":"a@example.test"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	grants := []*store.PageCapabilityGrant{{
		ProjectID: 7, Capability: "form.submit", Status: store.PageCapabilityApproved,
	}}
	if _, err := validatePageAppBridgeRequest(raw, 7, 11, grants, pagePlatformServerLimits()); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePageAppBridgeRequest(raw, 7, 12, grants, pagePlatformServerLimits()); err == nil ||
		!strings.Contains(err.Error(), "bridge_context_mismatch") {
		t.Fatalf("context mismatch error = %v", err)
	}
	grants[0].Status = store.PageCapabilityRevoked
	if _, err := validatePageAppBridgeRequest(raw, 7, 11, grants, pagePlatformServerLimits()); err == nil ||
		!strings.Contains(err.Error(), "bridge_capability_not_granted") {
		t.Fatalf("revoked grant error = %v", err)
	}
}
