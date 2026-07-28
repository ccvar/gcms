package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"cms.ccvar.com/internal/store"
)

const (
	pageAppProtocol       = "gcms-page-app/1"
	pageAppBridgeProtocol = "gcms-page-bridge/1"
	pageAppManifestName   = "app-manifest.json"
)

var (
	pageAppRequestIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	pageAppRemoteScript       = regexp.MustCompile(`(?is)<script\b[^>]*\bsrc\s*=\s*["']([^"']+)["']`)
	pageAppRemoteBase         = regexp.MustCompile(`(?is)<base\b[^>]*\bhref\s*=\s*["']([^"']+)["']`)
	pageAppServiceWorker      = regexp.MustCompile(`(?i)(navigator\s*\.\s*)?serviceWorker\s*\.\s*register\s*\(`)
	pageAppImportScripts      = regexp.MustCompile(`(?i)\bimportScripts\s*\(`)
	pageAppRemoteCSSImport    = regexp.MustCompile(`(?is)@import\s+(?:url\s*\(\s*)?["']?([^"')\s;]+)`)
	pageAppRemoteCSSResource  = regexp.MustCompile(`(?is)url\s*\(\s*["']?([^"')\s]+)`)
	pageAppRemoteModuleImport = regexp.MustCompile(`(?m)\b(?:import|export)\b[^\n;]*\bfrom\s*["']([^"']+)["']|\bimport\s*\(\s*["']([^"']+)["']\s*\)`)
)

type pageAppManifest struct {
	SchemaVersion int                 `json:"schema_version"`
	Entry         string              `json:"entry"`
	Viewport      string              `json:"viewport"`
	ShellMode     string              `json:"shell_mode"`
	Capabilities  []pageAppCapability `json:"capabilities,omitempty"`
	Dependencies  []pageAppDependency `json:"dependencies,omitempty"`
}

type pageAppCapability struct {
	Name string `json:"name"`
}

type pageAppDependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	License string `json:"license,omitempty"`
}

type validatedPageAppBundle struct {
	Manifest   pageAppManifest
	Files      map[string][]byte
	FileHashes map[string]string
	Hash       string
	TotalBytes int64
}

type pageAppValidationError struct {
	Code   string `json:"code"`
	File   string `json:"file,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (e *pageAppValidationError) Error() string {
	if e == nil {
		return "invalid page app"
	}
	if e.File != "" {
		return e.Code + ": " + e.File + ": " + e.Detail
	}
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

func pageAppInvalid(code, file, detail string) error {
	return &pageAppValidationError{Code: code, File: file, Detail: detail}
}

func validatePageAppPackage(raw []byte, limits pagePlatformLimits) (*validatedPageAppBundle, error) {
	if len(raw) == 0 {
		return nil, pageAppInvalid("empty_package", "", "应用包不能为空")
	}
	if int64(len(raw)) > limits.MaxAppPackageBytes {
		return nil, pageAppInvalid("package_too_large", "", "压缩包超过大小限制")
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, pageAppInvalid("invalid_zip", "", err.Error())
	}
	if len(reader.File) == 0 || len(reader.File) > limits.MaxAppFiles {
		return nil, pageAppInvalid("file_count_exceeded", "", "文件数量不在允许范围内")
	}

	files := make(map[string][]byte, len(reader.File))
	foldedNames := make(map[string]string, len(reader.File))
	var total int64
	for _, file := range reader.File {
		if file.Flags&0x1 != 0 {
			return nil, pageAppInvalid("encrypted_file_forbidden", file.Name, "不接受加密压缩条目")
		}
		if file.Method != zip.Store && file.Method != zip.Deflate {
			return nil, pageAppInvalid("compression_method_unsupported", file.Name, "只支持 store 或 deflate")
		}
		rawName := file.Name
		if file.FileInfo().IsDir() {
			rawName = strings.TrimSuffix(rawName, "/")
		}
		name, err := validatePageAppPath(rawName)
		if err != nil {
			return nil, err
		}
		mode := file.Mode()
		if file.FileInfo().IsDir() {
			continue
		}
		if !mode.IsRegular() || mode&os.ModeSymlink != 0 || mode&os.ModeDevice != 0 ||
			mode&os.ModeNamedPipe != 0 || mode&os.ModeSocket != 0 {
			return nil, pageAppInvalid("unsupported_file_type", name, "只允许普通文件")
		}
		if !pageAppFileExtensionAllowed(name) {
			return nil, pageAppInvalid("unsupported_file_extension", name, "文件类型不在白名单")
		}
		folded := strings.ToLower(name)
		if previous, exists := foldedNames[folded]; exists {
			return nil, pageAppInvalid("duplicate_path", name, "与 "+previous+" 冲突")
		}
		foldedNames[folded] = name

		declared := int64(file.UncompressedSize64)
		if declared > limits.MaxAssetBytes {
			return nil, pageAppInvalid("file_too_large", name, "单个文件超过大小限制")
		}
		total += declared
		if total > limits.MaxAppUnpackedBytes {
			return nil, pageAppInvalid("unpacked_size_exceeded", name, "解压后总大小超过限制")
		}
		if declared > 0 {
			compressed := int64(file.CompressedSize64)
			if compressed <= 0 || declared > compressed*int64(limits.MaxAppCompressionRatio) {
				return nil, pageAppInvalid("compression_ratio_exceeded", name, "压缩比超过限制")
			}
		}

		rc, err := file.Open()
		if err != nil {
			return nil, pageAppInvalid("read_failed", name, err.Error())
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, limits.MaxAssetBytes+1))
		closeErr := rc.Close()
		if readErr != nil {
			return nil, pageAppInvalid("read_failed", name, readErr.Error())
		}
		if closeErr != nil {
			return nil, pageAppInvalid("read_failed", name, closeErr.Error())
		}
		if int64(len(content)) > limits.MaxAssetBytes || int64(len(content)) != declared {
			return nil, pageAppInvalid("size_mismatch", name, "实际文件大小与压缩包声明不一致")
		}
		files[name] = content
	}

	manifestBytes, ok := files[pageAppManifestName]
	if !ok {
		return nil, pageAppInvalid("manifest_missing", pageAppManifestName, "缺少应用清单")
	}
	manifest, err := decodePageAppManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	entryBytes, ok := files[manifest.Entry]
	if !ok {
		return nil, pageAppInvalid("entry_missing", manifest.Entry, "清单入口文件不存在")
	}
	if path.Ext(manifest.Entry) != ".html" {
		return nil, pageAppInvalid("entry_not_html", manifest.Entry, "入口必须是 HTML")
	}
	if err := inspectPageAppFiles(files, manifest.Entry, entryBytes); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	bundleHash := sha256.New()
	fileHashes := make(map[string]string, len(files))
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		fileHashes[name] = hex.EncodeToString(sum[:])
		_, _ = io.WriteString(bundleHash, strconv.Itoa(len(name)))
		_, _ = io.WriteString(bundleHash, ":")
		_, _ = io.WriteString(bundleHash, name)
		_, _ = io.WriteString(bundleHash, ":")
		_, _ = io.WriteString(bundleHash, strconv.Itoa(len(files[name])))
		_, _ = bundleHash.Write(files[name])
	}
	return &validatedPageAppBundle{
		Manifest:   manifest,
		Files:      files,
		FileHashes: fileHashes,
		Hash:       hex.EncodeToString(bundleHash.Sum(nil)),
		TotalBytes: total,
	}, nil
}

func validatePageAppPath(raw string) (string, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.ContainsRune(raw, '\x00') ||
		strings.Contains(raw, `\`) || strings.HasPrefix(raw, "/") {
		return "", pageAppInvalid("unsafe_path", raw, "文件路径必须是 UTF-8 相对路径")
	}
	clean := path.Clean(raw)
	if clean == "." || clean != raw || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", pageAppInvalid("unsafe_path", raw, "文件路径不能包含跳转或规范化歧义")
	}
	if len(strings.Split(clean, "/")) > pagePlatformServerLimits().MaxNestingDepth {
		return "", pageAppInvalid("path_too_deep", raw, "目录嵌套超过限制")
	}
	return clean, nil
}

func pageAppFileExtensionAllowed(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".css", ".js", ".mjs", ".json", ".txt", ".md",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".svg",
		".woff", ".woff2", ".mp3", ".ogg", ".wav":
		return true
	default:
		return false
	}
}

func decodePageAppManifest(raw []byte) (pageAppManifest, error) {
	var manifest pageAppManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return manifest, pageAppInvalid("manifest_invalid", pageAppManifestName, err.Error())
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return manifest, pageAppInvalid("manifest_invalid", pageAppManifestName, "清单只能包含一个 JSON 对象")
	}
	entry, err := validatePageAppPath(strings.TrimSpace(manifest.Entry))
	if err != nil {
		return manifest, pageAppInvalid("entry_invalid", manifest.Entry, err.Error())
	}
	manifest.Entry = entry
	if manifest.SchemaVersion != 1 {
		return manifest, pageAppInvalid("manifest_version_unsupported", pageAppManifestName, "仅支持 schema_version 1")
	}
	if manifest.Viewport == "" {
		manifest.Viewport = "responsive"
	}
	if manifest.Viewport != "responsive" && manifest.Viewport != "fixed" {
		return manifest, pageAppInvalid("viewport_invalid", pageAppManifestName, "viewport 只能是 responsive 或 fixed")
	}
	if manifest.ShellMode == "" {
		manifest.ShellMode = store.PageShellSite
	}
	if manifest.ShellMode != store.PageShellSite && manifest.ShellMode != store.PageShellMinimal &&
		manifest.ShellMode != store.PageShellNone {
		return manifest, pageAppInvalid("shell_mode_invalid", pageAppManifestName, "shell_mode 不受支持")
	}
	knownCapabilities := map[string]bool{
		"client.storage": true, "input.keyboard": true, "input.touch": true,
		"audio.playback": true, "content.read": true, "form.submit": true,
		"external.network": true,
	}
	seen := map[string]bool{}
	for i := range manifest.Capabilities {
		name := strings.TrimSpace(manifest.Capabilities[i].Name)
		if !knownCapabilities[name] {
			return manifest, pageAppInvalid("capability_unknown", pageAppManifestName, name)
		}
		if seen[name] {
			return manifest, pageAppInvalid("capability_duplicate", pageAppManifestName, name)
		}
		seen[name] = true
		manifest.Capabilities[i].Name = name
	}
	for _, dependency := range manifest.Dependencies {
		if strings.TrimSpace(dependency.Name) == "" || strings.TrimSpace(dependency.Version) == "" {
			return manifest, pageAppInvalid("dependency_invalid", pageAppManifestName, "依赖名称和锁定版本不能为空")
		}
	}
	return manifest, nil
}

func inspectPageAppFiles(files map[string][]byte, entry string, entryBytes []byte) error {
	for _, match := range pageAppRemoteScript.FindAllSubmatch(entryBytes, -1) {
		if len(match) > 1 && pageAppURLIsRemote(string(match[1])) {
			return pageAppInvalid("remote_script_forbidden", entry, string(match[1]))
		}
	}
	for _, match := range pageAppRemoteBase.FindAllSubmatch(entryBytes, -1) {
		if len(match) > 1 && pageAppURLIsRemote(string(match[1])) {
			return pageAppInvalid("remote_base_forbidden", entry, string(match[1]))
		}
	}
	for name, content := range files {
		switch strings.ToLower(path.Ext(name)) {
		case ".js", ".mjs", ".html":
			if pageAppServiceWorker.Match(content) || pageAppImportScripts.Match(content) {
				return pageAppInvalid("worker_forbidden", name, "Service Worker 和 Worker 导入脚本不受支持")
			}
			for _, match := range pageAppRemoteModuleImport.FindAllSubmatch(content, -1) {
				for _, candidate := range match[1:] {
					if len(candidate) > 0 && pageAppURLIsRemote(string(candidate)) {
						return pageAppInvalid("remote_module_forbidden", name, string(candidate))
					}
				}
			}
		case ".css":
			for _, expression := range []*regexp.Regexp{pageAppRemoteCSSImport, pageAppRemoteCSSResource} {
				for _, match := range expression.FindAllSubmatch(content, -1) {
					if len(match) > 1 && pageAppURLIsRemote(string(match[1])) {
						return pageAppInvalid("remote_resource_forbidden", name, string(match[1]))
					}
				}
			}
		}
	}
	return nil
}

func pageAppURLIsRemote(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		return true
	}
	parsed, err := url.Parse(raw)
	return err != nil || parsed.IsAbs()
}

func pageAppContentSecurityPolicy() string {
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'self' 'unsafe-inline' blob:",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"media-src 'self' data: blob:",
		"connect-src 'none'",
		"worker-src 'none'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'self'",
	}, "; ") + ";"
}

func pageAppPermissionsPolicy() string {
	return strings.Join([]string{
		"accelerometer=()", "ambient-light-sensor=()", "autoplay=()", "camera=()",
		"clipboard-read=()", "clipboard-write=()", "display-capture=()", "encrypted-media=()",
		"fullscreen=()", "geolocation=()", "gyroscope=()", "magnetometer=()",
		"microphone=()", "midi=()", "payment=()", "publickey-credentials-get=()",
		"screen-wake-lock=()", "serial=()", "usb=()", "web-share=()",
	}, ", ")
}

func pageAppResponseHeaders() map[string]string {
	return map[string]string{
		"Content-Security-Policy": pageAppContentSecurityPolicy(),
		"Permissions-Policy":      pageAppPermissionsPolicy(),
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		// The app document deliberately receives an opaque origin from
		// sandbox="allow-scripts". Its own signed/static subresources must
		// therefore be embeddable by that opaque origin; the URL ticket and
		// CSP remain the authorization and network boundaries.
		"Cross-Origin-Resource-Policy": "cross-origin",
	}
}

func pageAppIframeAttributes() map[string]string {
	return map[string]string{
		"sandbox":        "allow-scripts",
		"allow":          "",
		"referrerpolicy": "no-referrer",
	}
}

type pageAppBridgeRequest struct {
	Protocol   string          `json:"protocol"`
	RequestID  string          `json:"request_id"`
	ProjectID  int64           `json:"project_id"`
	RevisionID int64           `json:"revision_id"`
	Capability string          `json:"capability"`
	Action     string          `json:"action"`
	Payload    json.RawMessage `json:"payload"`
}

func validatePageAppBridgeRequest(
	raw []byte,
	expectedProjectID, expectedRevisionID int64,
	grants []*store.PageCapabilityGrant,
	limits pagePlatformLimits,
) (pageAppBridgeRequest, error) {
	var request pageAppBridgeRequest
	if int64(len(raw)) > limits.MaxBridgeRequestBytes {
		return request, pageAppInvalid("bridge_request_too_large", "", "消息超过大小限制")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		return request, pageAppInvalid("bridge_request_invalid", "", err.Error())
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return request, pageAppInvalid("bridge_request_invalid", "", "消息只能包含一个 JSON 对象")
	}
	if request.Protocol != pageAppBridgeProtocol {
		return request, pageAppInvalid("bridge_protocol_invalid", "", request.Protocol)
	}
	if !pageAppRequestIDPattern.MatchString(request.RequestID) {
		return request, pageAppInvalid("bridge_request_id_invalid", "", request.RequestID)
	}
	if request.ProjectID != expectedProjectID || request.RevisionID != expectedRevisionID {
		return request, pageAppInvalid("bridge_context_mismatch", "", "工程或修订上下文不匹配")
	}
	if !pageAppBridgeActionAllowed(request.Capability, request.Action) {
		return request, pageAppInvalid("bridge_action_invalid", "", request.Capability+"."+request.Action)
	}
	granted := false
	for _, grant := range grants {
		if grant != nil && grant.ProjectID == expectedProjectID &&
			grant.Capability == request.Capability && grant.Status == store.PageCapabilityApproved {
			granted = true
			break
		}
	}
	if !granted {
		return request, pageAppInvalid("bridge_capability_not_granted", "", request.Capability)
	}
	if len(request.Payload) == 0 || !json.Valid(request.Payload) {
		return request, pageAppInvalid("bridge_payload_invalid", "", "payload 必须是合法 JSON")
	}
	return request, nil
}

func pageAppBridgeActionAllowed(capability, action string) bool {
	allowed := map[string]map[string]bool{
		"client.storage":   {"get": true, "set": true, "remove": true},
		"content.read":     {"query": true, "get": true},
		"form.submit":      {"submit": true},
		"external.network": {"fetch": true},
	}
	return allowed[capability][action]
}

func persistPageAppBundle(projectStorageDir string, projectID int64, bundle *validatedPageAppBundle) (string, error) {
	if strings.TrimSpace(projectStorageDir) == "" || projectID <= 0 || bundle == nil ||
		len(bundle.Hash) != sha256.Size*2 || len(bundle.Files) == 0 {
		return "", errors.New("invalid page app storage input")
	}
	base := filepath.Join(projectStorageDir, "sources", strconv.FormatInt(projectID, 10))
	destination := filepath.Join(base, bundle.Hash)
	if info, err := os.Stat(destination); err == nil && info.IsDir() {
		return filepath.ToSlash(filepath.Join("sources", strconv.FormatInt(projectID, 10), bundle.Hash)), nil
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	tempDir, err := os.MkdirTemp(base, ".incoming-")
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tempDir)
		}
	}()
	for name, content := range bundle.Files {
		clean, err := validatePageAppPath(name)
		if err != nil {
			return "", err
		}
		target := filepath.Join(tempDir, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tempDir, destination); err != nil {
		if info, statErr := os.Stat(destination); statErr == nil && info.IsDir() {
			committed = true
			_ = os.RemoveAll(tempDir)
			return filepath.ToSlash(filepath.Join("sources", strconv.FormatInt(projectID, 10), bundle.Hash)), nil
		}
		return "", fmt.Errorf("commit page app bundle: %w", err)
	}
	committed = true
	return filepath.ToSlash(filepath.Join("sources", strconv.FormatInt(projectID, 10), bundle.Hash)), nil
}
