package web

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cms.ccvar.com/internal/store"
)

const (
	pageAppRuntimeVersion          = "gcms-static-app/1"
	pageAppRuntimeTokenVersion     = 1
	pageAppRuntimeTokenTTL         = 15 * time.Minute
	pageAppCapabilityApprovalTTL   = 10 * time.Minute
	pageAppRuntimeTokenHeader      = "X-GCMS-App-Token"
	pageAppPreviewShellAudience    = "preview-shell"
	pageAppPreviewAssetsAudience   = "preview-assets"
	pageAppPreviewBridgeAudience   = "preview-bridge"
	pageAppPublishedBridgeAudience = "published-bridge"
	pageAppTextEditMaxBytes        = int64(1 << 20)
)

var pageAppStorageKeyPattern = regexpMustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// regexpMustCompile is kept local to this file so the app delivery layer does
// not add mutable global initialization to the package's shared route files.
func regexpMustCompile(expr string) *regexp.Regexp {
	return regexp.MustCompile(expr)
}

type pageAppPackageForm struct {
	Raw              []byte
	BaseRevisionID   int64
	Summary          string
	ConversationID   string
	OriginalFilename string
}

type pageAppCapabilityMutation struct {
	Capability string          `json:"capability"`
	Config     json.RawMessage `json:"config"`
	Decision   string          `json:"decision,omitempty"`
}

type pageAppRuntimeClaims struct {
	Version      int    `json:"v"`
	Audience     string `json:"aud"`
	SiteID       int64  `json:"site_id,omitempty"`
	ProjectID    int64  `json:"project_id"`
	RevisionID   int64  `json:"revision_id"`
	BuildID      int64  `json:"build_id"`
	ArtifactHash string `json:"artifact_hash"`
	Runtime      string `json:"runtime"`
	Expires      int64  `json:"exp"`
	Nonce        string `json:"nonce"`
}

type pageAppAdminFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
	Editable  bool   `json:"editable"`
}

type pageAppTextEditInput struct {
	BaseRevisionID int64  `json:"base_revision_id"`
	Content        string `json:"content"`
	Summary        string `json:"summary"`
	ConversationID string `json:"conversation_id"`
}

func (s *Server) registerPageAppAPIRoutes(mux *http.ServeMux) {
	register := func(prefix string) {
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/app-package", s.apiUploadPageAppPackage)
		mux.HandleFunc("GET "+prefix+"/page-projects/{projectID}/app-files/{file...}", s.apiGetPageAppSourceFile)
		mux.HandleFunc("PUT "+prefix+"/page-projects/{projectID}/app-files/{file...}", s.apiEditPageAppSourceFile)
		mux.HandleFunc("GET "+prefix+"/page-projects/{projectID}/capabilities", s.apiListPageAppCapabilities)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/capabilities/request", s.apiRequestPageAppCapability)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/capabilities/apply", s.apiApplyPageAppCapability)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/capabilities/revoke", s.apiRevokePageAppCapability)
	}
	register("/api/admin/v1")
	register("/api/platform/v1/sites/{siteID}")
}

func (s *Server) registerPageAppPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /_gcms/page-apps/{projectID}/{revisionID}/{asset...}", s.servePublishedPageAppAsset)
	mux.HandleFunc("GET /preview/page-apps/{projectID}/{revisionID}", s.frontendPageAppPreview)
	mux.HandleFunc("GET /preview/page-app-assets/{appToken}/{projectID}/{revisionID}/{asset...}", s.servePreviewPageAppAsset)
	mux.HandleFunc("POST /_gcms/page-app-bridge/{projectID}/{revisionID}", s.servePageAppBridge)
	mux.HandleFunc("OPTIONS /_gcms/page-app-bridge/{projectID}/{revisionID}", s.servePageAppBridge)
	mux.HandleFunc("POST /preview/page-app-bridge/{projectID}/{revisionID}", s.servePageAppBridge)
}

func readPageAppPackageForm(w http.ResponseWriter, r *http.Request, limit int64) (pageAppPackageForm, error) {
	var out pageAppPackageForm
	if limit <= 0 {
		return out, pageAppInvalid("package_too_large", "", "服务端未配置有效大小限制")
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return out, pageAppInvalid("content_type_invalid", "", "Content-Type 无效")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	switch mediaType {
	case "application/zip", "application/octet-stream":
		raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			return out, pageAppInvalid("read_failed", "", err.Error())
		}
		if int64(len(raw)) > limit {
			return out, pageAppInvalid("package_too_large", "", "压缩包超过大小限制")
		}
		out.Raw = raw
		return out, nil
	case "multipart/form-data":
	default:
		return out, pageAppInvalid("content_type_unsupported", "", "仅支持 multipart/form-data 或 application/zip")
	}

	reader, err := r.MultipartReader()
	if err != nil {
		return out, pageAppInvalid("multipart_invalid", "", err.Error())
	}
	seenFile := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, pageAppInvalid("multipart_invalid", "", err.Error())
		}
		name := part.FormName()
		filename := part.FileName()
		if filename != "" {
			if seenFile || (name != "package" && name != "file") {
				_ = part.Close()
				return out, pageAppInvalid("multipart_invalid", "", "只能上传一个 package 文件")
			}
			raw, readErr := io.ReadAll(io.LimitReader(part, limit+1))
			_ = part.Close()
			if readErr != nil {
				return out, pageAppInvalid("read_failed", filename, readErr.Error())
			}
			if int64(len(raw)) > limit {
				return out, pageAppInvalid("package_too_large", filename, "压缩包超过大小限制")
			}
			out.Raw = raw
			out.OriginalFilename = path.Base(strings.ReplaceAll(filename, `\`, "/"))
			seenFile = true
			continue
		}
		value, readErr := io.ReadAll(io.LimitReader(part, 4097))
		_ = part.Close()
		if readErr != nil || len(value) > 4096 {
			return out, pageAppInvalid("multipart_invalid", name, "表单字段过长")
		}
		switch name {
		case "base_revision_id":
			if strings.TrimSpace(string(value)) == "" {
				continue
			}
			parsed, parseErr := strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
			if parseErr != nil || parsed <= 0 {
				return out, pageAppInvalid("base_revision_invalid", name, "base_revision_id 无效")
			}
			out.BaseRevisionID = parsed
		case "summary":
			out.Summary = strings.TrimSpace(string(value))
		case "conversation_id":
			out.ConversationID = strings.TrimSpace(string(value))
		case "":
		default:
			return out, pageAppInvalid("multipart_invalid", name, "未知表单字段")
		}
	}
	if !seenFile || len(out.Raw) == 0 {
		return out, pageAppInvalid("empty_package", "", "未收到应用包")
	}
	return out, nil
}

func writePageAppValidationError(w http.ResponseWriter, err error) {
	var invalid *pageAppValidationError
	if errors.As(err, &invalid) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": invalid.Code, "message": invalid.Detail, "file": invalid.File,
		})
		return
	}
	pageStoreError(w, err)
}

func writePageAppBridgeValidationError(w http.ResponseWriter, err error) {
	var invalid *pageAppValidationError
	if !errors.As(err, &invalid) {
		apiError(w, http.StatusInternalServerError, "bridge_failed", "Bridge 执行失败。")
		return
	}
	status := http.StatusBadRequest
	switch invalid.Code {
	case "bridge_capability_not_granted", "bridge_capability_not_declared",
		"bridge_capability_unavailable":
		status = http.StatusForbidden
	case "bridge_context_mismatch":
		status = http.StatusGone
	case "bridge_request_too_large":
		status = http.StatusRequestEntityTooLarge
	}
	apiError(w, status, invalid.Code, invalid.Detail)
}

func pageAppRevisionByRequest(s *store.Store, projectID int64, requestID string) (*store.PageProjectRevision, error) {
	revisions, err := s.ListPageProjectRevisions(projectID, 500)
	if err != nil {
		return nil, err
	}
	for _, revision := range revisions {
		if revision != nil && revision.RequestID == requestID {
			return revision, nil
		}
	}
	return nil, nil
}

func (s *Server) apiUploadPageAppPackage(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageAppsWrite)
	if !ok {
		return
	}
	requestID, ok := pageRequireIdempotencyKey(w, r)
	if !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "只有互动应用工程可以上传应用包。")
		return
	}
	form, err := readPageAppPackageForm(w, r, pagePlatformServerLimits().MaxAppPackageBytes)
	if err != nil {
		writePageAppValidationError(w, err)
		return
	}
	bundle, err := validatePageAppPackage(form.Raw, pagePlatformServerLimits())
	if err != nil {
		writePageAppValidationError(w, err)
		return
	}
	if bundle.Manifest.ShellMode != project.ShellMode {
		apiError(w, http.StatusUnprocessableEntity, "shell_mode_mismatch",
			"应用清单 shell_mode 必须与页面工程一致。")
		return
	}

	replay, err := pageAppRevisionByRequest(s.store, project.ID, requestID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	baseRevisionID := form.BaseRevisionID
	if replay != nil {
		baseRevisionID = replay.ParentRevisionID
	}
	if baseRevisionID == 0 {
		baseRevisionID = project.WorkingRevisionID
	}
	if !s.pageRequireRevisionMutationETag(w, r, project, requestID, baseRevisionID) {
		return
	}
	base, err := s.store.GetPageProjectRevision(baseRevisionID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if base == nil || base.ProjectID != project.ID {
		apiError(w, http.StatusNotFound, "revision_not_found", "基础修订不存在。")
		return
	}
	if replay != nil && replay.SourceHash != bundle.Hash {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "idempotency_conflict", "message": "同一幂等键已用于不同应用包。",
			"request_id": requestID,
		})
		return
	}
	sourceRef, err := persistPageAppBundle(s.store.PageProjectStorageDir(), project.ID, bundle)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "app_source_store_failed", "保存私有应用源码失败。")
		return
	}
	manifestRaw, err := json.Marshal(bundle.Manifest)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "manifest_invalid", err.Error())
		return
	}
	manifestJSON, _, err := store.CanonicalJSONHash(string(manifestRaw))
	if err != nil {
		pageStoreError(w, err)
		return
	}
	summary := form.Summary
	if summary == "" {
		summary = "上传互动应用包"
	}
	validationRaw, _ := json.Marshal(map[string]any{
		"valid": true, "runtime": pageAppRuntimeVersion,
		"source_hash": bundle.Hash, "files": len(bundle.Files),
		"bytes": bundle.TotalBytes,
	})
	revision, created, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: baseRevisionID,
		RevisionKind: store.PageRevisionApp, PageMetaJSON: base.PageMetaJSON,
		ManifestJSON: manifestJSON, SourceBundleRef: sourceRef, SourceHash: bundle.Hash,
		Origin: store.PageOriginAPI, ActorID: pageAutomationActor(auth),
		ConversationID: form.ConversationID, RequestID: requestID,
		Summary: summary, ValidationJSON: string(validationRaw),
	})
	if err != nil {
		pageStoreError(w, err)
		return
	}
	project, _ = s.store.GetPageProject(project.ID)
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	s.invalidatePageProjectDraft()
	s.recordAutomationLog(auth, "upload", "page_app_package", revision.ID,
		fmt.Sprintf("上传互动应用修订 %d（%d 个文件）", revision.ID, len(bundle.Files)))
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, func() int {
		if created {
			return http.StatusCreated
		}
		return http.StatusOK
	}(), map[string]any{
		"project": project, "working_revision": revision, "created": created,
		"source_hash": bundle.Hash, "file_count": len(bundle.Files),
		"unpacked_bytes": bundle.TotalBytes, "manifest": bundle.Manifest,
	})
}

func pageAppTextFileEditable(name string) bool {
	if name == pageAppManifestName {
		return true
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".css", ".js", ".mjs", ".json", ".md", ".txt":
		return true
	default:
		return false
	}
}

func validatePageAppTextEdit(name string, raw []byte) (string, error) {
	clean, err := validatePageAppPath(name)
	if err != nil {
		return "", err
	}
	if !pageAppTextFileEditable(clean) {
		return "", pageAppInvalid("source_file_not_editable", clean,
			"后台文本编辑器只支持 HTML、CSS、JavaScript、JSON、Markdown 和纯文本文件")
	}
	if int64(len(raw)) > pageAppTextEditMaxBytes {
		return "", pageAppInvalid("source_file_too_large", clean, "文本源码超过在线编辑大小限制")
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return "", pageAppInvalid("source_file_not_utf8", clean, "文本源码必须是无 NUL 的 UTF-8")
	}
	return clean, nil
}

func decodePageAppTextEditJSON(w http.ResponseWriter, r *http.Request, dst *pageAppTextEditInput) bool {
	const requestOverhead = int64(64 << 10)
	limit := pageAppTextEditMaxBytes*8 + requestOverhead
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_json", "读取 JSON 失败。")
		return false
	}
	if int64(len(raw)) > limit {
		apiError(w, http.StatusRequestEntityTooLarge, "body_too_large", "源码编辑请求过大。")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 格式错误。")
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 只能包含一个对象。")
		return false
	}
	return true
}

func (s *Server) createPageAppTextRevision(
	project *store.PageProject,
	base *store.PageProjectRevision,
	name string,
	content []byte,
	origin string,
	actorID string,
	conversationID string,
	requestID string,
	summary string,
) (*store.PageProjectRevision, *validatedPageAppBundle, bool, error) {
	if project == nil || base == nil || project.Mode != store.PageModeApp ||
		base.ProjectID != project.ID || base.RevisionKind != store.PageRevisionApp {
		return nil, nil, false, pageAppInvalid("page_mode_unsupported", "", "目标不是互动应用修订")
	}
	clean, err := validatePageAppTextEdit(name, content)
	if err != nil {
		return nil, nil, false, err
	}
	baseBundle, err := s.loadPageAppSource(project, base)
	if err != nil {
		return nil, nil, false, err
	}
	if _, exists := baseBundle.Files[clean]; !exists {
		return nil, nil, false, pageAppInvalid("source_file_not_found", clean, "只能在线编辑当前修订中已有的文本文件")
	}
	files := make(map[string][]byte, len(baseBundle.Files))
	for fileName, raw := range baseBundle.Files {
		files[fileName] = append([]byte(nil), raw...)
	}
	files[clean] = append([]byte(nil), content...)
	bundle, err := validateStoredPageAppFiles(files, pagePlatformServerLimits())
	if err != nil {
		return nil, nil, false, err
	}
	if bundle.Manifest.ShellMode != project.ShellMode {
		return nil, nil, false, pageAppInvalid("shell_mode_mismatch", pageAppManifestName,
			"应用清单 shell_mode 必须与页面工程一致")
	}
	sourceRef, err := persistPageAppBundle(s.store.PageProjectStorageDir(), project.ID, bundle)
	if err != nil {
		return nil, nil, false, err
	}
	manifestRaw, err := json.Marshal(bundle.Manifest)
	if err != nil {
		return nil, nil, false, err
	}
	manifestJSON, _, err := store.CanonicalJSONHash(string(manifestRaw))
	if err != nil {
		return nil, nil, false, err
	}
	if strings.TrimSpace(summary) == "" {
		summary = "编辑互动应用源码 " + clean
	}
	validationRaw, _ := json.Marshal(map[string]any{
		"valid": true, "runtime": pageAppRuntimeVersion,
		"source_hash": bundle.Hash, "files": len(bundle.Files),
		"bytes": bundle.TotalBytes, "edited_file": clean,
	})
	revision, created, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: base.ID,
		RevisionKind: store.PageRevisionApp, PageMetaJSON: base.PageMetaJSON,
		ManifestJSON: manifestJSON, SourceBundleRef: sourceRef, SourceHash: bundle.Hash,
		Origin: origin, ActorID: strings.TrimSpace(actorID),
		ConversationID: strings.TrimSpace(conversationID), RequestID: strings.TrimSpace(requestID),
		Summary: strings.TrimSpace(summary), ValidationJSON: string(validationRaw),
	})
	if err != nil {
		return nil, nil, false, err
	}
	return revision, bundle, created, nil
}

func (s *Server) apiGetPageAppSourceFile(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageAppsWrite)
	if !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "目标不是互动应用工程。")
		return
	}
	revisionID := atoi64(r.URL.Query().Get("revision_id"))
	if revisionID <= 0 {
		revisionID = project.WorkingRevisionID
	}
	name, err := validatePageAppTextEdit(r.PathValue("file"), nil)
	if err != nil {
		writePageAppValidationError(w, err)
		return
	}
	raw, mediaType, err := s.pageAppAdminSourceFile(project.ID, revisionID, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			apiError(w, http.StatusNotFound, "source_file_not_found", "源码文件不存在。")
			return
		}
		writePageAppValidationError(w, err)
		return
	}
	if _, err := validatePageAppTextEdit(name, raw); err != nil {
		writePageAppValidationError(w, err)
		return
	}
	sum := sha256.Sum256(raw)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", project.ETag())
	s.recordAutomationLog(auth, "read", "page_app_source", revisionID, "读取互动应用文本源码 "+name)
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": project.ID, "revision_id": revisionID,
		"path": name, "media_type": mediaType, "byte_size": len(raw),
		"sha256": hex.EncodeToString(sum[:]), "content": string(raw),
	})
}

func (s *Server) apiEditPageAppSourceFile(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageAppsWrite)
	if !ok {
		return
	}
	requestID, ok := pageRequireIdempotencyKey(w, r)
	if !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "目标不是互动应用工程。")
		return
	}
	var in pageAppTextEditInput
	if !decodePageAppTextEditJSON(w, r, &in) {
		return
	}
	if in.BaseRevisionID <= 0 {
		apiError(w, http.StatusUnprocessableEntity, "base_revision_required", "必须提供 base_revision_id。")
		return
	}
	if !s.pageRequireRevisionMutationETag(w, r, project, requestID, in.BaseRevisionID) {
		return
	}
	base, err := s.store.GetPageProjectRevision(in.BaseRevisionID)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if base == nil || base.ProjectID != project.ID {
		apiError(w, http.StatusNotFound, "revision_not_found", "基础修订不存在。")
		return
	}
	revision, bundle, created, err := s.createPageAppTextRevision(
		project, base, r.PathValue("file"), []byte(in.Content),
		store.PageOriginAPI, pageAutomationActor(auth), in.ConversationID,
		requestID, in.Summary,
	)
	if err != nil {
		var invalid *pageAppValidationError
		if errors.As(err, &invalid) {
			writePageAppValidationError(w, err)
		} else {
			pageStoreError(w, err)
		}
		return
	}
	project, _ = s.store.GetPageProject(project.ID)
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	s.invalidatePageProjectDraft()
	s.recordAutomationLog(auth, "edit", "page_app_source", revision.ID,
		"编辑互动应用文本源码 "+r.PathValue("file"))
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, func() int {
		if created {
			return http.StatusCreated
		}
		return http.StatusOK
	}(), map[string]any{
		"project": project, "working_revision": revision, "created": created,
		"source_hash": bundle.Hash, "file_count": len(bundle.Files),
		"unpacked_bytes": bundle.TotalBytes,
	})
}

func pageAppBundleDigest(files map[string][]byte) (map[string]string, string) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	bundleHash := sha256.New()
	hashes := make(map[string]string, len(files))
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		hashes[name] = hex.EncodeToString(sum[:])
		_, _ = io.WriteString(bundleHash, strconv.Itoa(len(name)))
		_, _ = io.WriteString(bundleHash, ":")
		_, _ = io.WriteString(bundleHash, name)
		_, _ = io.WriteString(bundleHash, ":")
		_, _ = io.WriteString(bundleHash, strconv.Itoa(len(files[name])))
		_, _ = bundleHash.Write(files[name])
	}
	return hashes, hex.EncodeToString(bundleHash.Sum(nil))
}

func validateStoredPageAppFiles(files map[string][]byte, limits pagePlatformLimits) (*validatedPageAppBundle, error) {
	if len(files) == 0 || len(files) > limits.MaxAppFiles {
		return nil, pageAppInvalid("file_count_exceeded", "", "文件数量不在允许范围内")
	}
	var total int64
	for name, content := range files {
		clean, err := validatePageAppPath(name)
		if err != nil || clean != name {
			return nil, pageAppInvalid("unsafe_path", name, "私有源码包含不安全路径")
		}
		if !pageAppFileExtensionAllowed(name) {
			return nil, pageAppInvalid("unsupported_file_extension", name, "文件类型不在白名单")
		}
		if int64(len(content)) > limits.MaxAssetBytes {
			return nil, pageAppInvalid("file_too_large", name, "单个文件超过大小限制")
		}
		total += int64(len(content))
		if total > limits.MaxAppUnpackedBytes {
			return nil, pageAppInvalid("unpacked_size_exceeded", name, "总大小超过限制")
		}
	}
	manifestBytes, ok := files[pageAppManifestName]
	if !ok {
		return nil, pageAppInvalid("manifest_missing", pageAppManifestName, "缺少应用清单")
	}
	manifest, err := decodePageAppManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	entry, ok := files[manifest.Entry]
	if !ok || path.Ext(manifest.Entry) != ".html" {
		return nil, pageAppInvalid("entry_missing", manifest.Entry, "入口 HTML 不存在")
	}
	if err := inspectPageAppFiles(files, manifest.Entry, entry); err != nil {
		return nil, err
	}
	hashes, digest := pageAppBundleDigest(files)
	return &validatedPageAppBundle{
		Manifest: manifest, Files: files, FileHashes: hashes,
		Hash: digest, TotalBytes: total,
	}, nil
}

func securePageAppStorageDir(root, ref string, projectID, revisionID int64, kind, digest string) (string, error) {
	if strings.TrimSpace(root) == "" || projectID <= 0 || digest == "" ||
		len(digest) != sha256.Size*2 || digest != strings.ToLower(digest) {
		return "", pageAppInvalid("storage_ref_invalid", "", "私有存储引用无效")
	}
	parts := strings.Split(ref, "/")
	switch kind {
	case "sources":
		if len(parts) != 3 || parts[0] != "sources" ||
			parts[1] != strconv.FormatInt(projectID, 10) || parts[2] != digest {
			return "", pageAppInvalid("storage_ref_invalid", ref, "源码引用不属于当前工程和哈希")
		}
	case "artifacts":
		if len(parts) != 4 || parts[0] != "artifacts" ||
			parts[1] != strconv.FormatInt(projectID, 10) ||
			parts[2] != strconv.FormatInt(revisionID, 10) || parts[3] != digest {
			return "", pageAppInvalid("storage_ref_invalid", ref, "产物引用不属于当前工程、修订和哈希")
		}
	default:
		return "", pageAppInvalid("storage_ref_invalid", ref, "私有存储类型无效")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(rootAbs, filepath.FromSlash(ref))
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", pageAppInvalid("storage_ref_invalid", ref, "私有存储路径越界")
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	targetReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	realRel, err := filepath.Rel(rootReal, targetReal)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return "", pageAppInvalid("storage_ref_invalid", ref, "私有存储符号链接越界")
	}
	info, err := os.Stat(targetReal)
	if err != nil || !info.IsDir() {
		return "", pageAppInvalid("storage_ref_missing", ref, "私有存储目录不存在")
	}
	return targetReal, nil
}

func readPageAppBundleDirectory(dir string, limits pagePlatformLimits) (*validatedPageAppBundle, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, filename)
		if err != nil || rel == "." {
			return err
		}
		name := filepath.ToSlash(rel)
		if _, err := validatePageAppPath(name); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!entry.IsDir() && !info.Mode().IsRegular()) {
			return pageAppInvalid("unsupported_file_type", name, "私有目录只允许普通文件")
		}
		if entry.IsDir() {
			return nil
		}
		if len(files) >= limits.MaxAppFiles {
			return pageAppInvalid("file_count_exceeded", name, "文件数量超过限制")
		}
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, limits.MaxAssetBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(len(raw)) > limits.MaxAssetBytes {
			return pageAppInvalid("file_too_large", name, "单个文件超过限制")
		}
		files[name] = raw
		return nil
	})
	if err != nil {
		return nil, err
	}
	return validateStoredPageAppFiles(files, limits)
}

func (s *Server) loadPageAppSource(project *store.PageProject, revision *store.PageProjectRevision) (*validatedPageAppBundle, error) {
	if project == nil || revision == nil || project.Mode != store.PageModeApp ||
		revision.ProjectID != project.ID || revision.RevisionKind != store.PageRevisionApp {
		return nil, pageAppInvalid("page_mode_unsupported", "", "目标不是互动应用修订")
	}
	dir, err := securePageAppStorageDir(
		s.store.PageProjectStorageDir(), revision.SourceBundleRef,
		project.ID, 0, "sources", revision.SourceHash,
	)
	if err != nil {
		return nil, err
	}
	bundle, err := readPageAppBundleDirectory(dir, pagePlatformServerLimits())
	if err != nil {
		return nil, err
	}
	if bundle.Hash != revision.SourceHash {
		return nil, pageAppInvalid("source_hash_mismatch", revision.SourceBundleRef, "私有源码哈希与修订不一致")
	}
	manifestRaw, err := json.Marshal(bundle.Manifest)
	if err != nil {
		return nil, err
	}
	manifestJSON, _, err := store.CanonicalJSONHash(string(manifestRaw))
	if err != nil {
		return nil, err
	}
	if manifestJSON != revision.ManifestJSON || bundle.Manifest.ShellMode != project.ShellMode {
		return nil, pageAppInvalid("manifest_source_mismatch", revision.SourceBundleRef, "应用清单与修订或工程外壳不一致")
	}
	return bundle, nil
}

func persistPageAppArtifact(root string, projectID, revisionID int64, bundle *validatedPageAppBundle) (string, error) {
	if strings.TrimSpace(root) == "" || projectID <= 0 || revisionID <= 0 || bundle == nil {
		return "", errors.New("invalid page app artifact input")
	}
	base := filepath.Join(root, "artifacts", strconv.FormatInt(projectID, 10), strconv.FormatInt(revisionID, 10))
	destination := filepath.Join(base, bundle.Hash)
	ref := filepath.ToSlash(filepath.Join("artifacts", strconv.FormatInt(projectID, 10), strconv.FormatInt(revisionID, 10), bundle.Hash))
	if info, err := os.Stat(destination); err == nil && info.IsDir() {
		return ref, nil
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	tempDir, err := os.MkdirTemp(base, ".building-")
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
			return ref, nil
		}
		return "", err
	}
	committed = true
	return ref, nil
}

func (s *Server) createPageAppBuild(
	w http.ResponseWriter,
	r *http.Request,
	project *store.PageProject,
	requestID string,
) {
	var in pageRevisionTargetInput
	if !decodeOptionalPageAppJSON(w, r, &in) {
		return
	}
	revision, ok := s.revisionForValidation(w, project, in.RevisionID)
	if !ok {
		return
	}
	if revision.RevisionKind != store.PageRevisionApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "目标修订不是互动应用。")
		return
	}
	bundle, err := s.loadPageAppSource(project, revision)
	if err != nil {
		writePageAppValidationError(w, err)
		return
	}
	requestHash, err := canonicalPageBuildRequestHash(pageBuildRequestIdentity{
		SchemaVersion: 1, ProjectID: project.ID, RevisionID: revision.ID,
		Mode: project.Mode, RevisionKind: revision.RevisionKind,
		RuntimeVersion: pageAppRuntimeVersion, ManifestHash: revision.ManifestHash,
		SourceHash: revision.SourceHash, ArtifactHash: bundle.Hash,
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, "app_build_failed", "生成构建请求摘要失败。")
		return
	}
	if replay, found, err := s.store.GetPageBuildCreateReceipt(
		project.ID, requestID, requestHash,
	); err != nil {
		pageStoreError(w, err)
		return
	} else if found {
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("ETag", project.ETag())
		writeJSON(w, http.StatusOK, map[string]any{"build": replay, "created": false})
		return
	}
	artifactRef, err := persistPageAppArtifact(
		s.store.PageProjectStorageDir(), project.ID, revision.ID, bundle,
	)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "app_build_failed", "写入不可变应用产物失败。")
		return
	}
	// Re-open the committed artifact and verify the same digest before a ready
	// build crosses the publication boundary. No install/build script is ever
	// executed: v1 only copies an already validated static bundle.
	artifactDir, err := securePageAppStorageDir(
		s.store.PageProjectStorageDir(), artifactRef,
		project.ID, revision.ID, "artifacts", bundle.Hash,
	)
	if err != nil {
		writePageAppValidationError(w, err)
		return
	}
	committed, err := readPageAppBundleDirectory(artifactDir, pagePlatformServerLimits())
	if err != nil || committed.Hash != bundle.Hash {
		if err == nil {
			err = pageAppInvalid("artifact_hash_mismatch", artifactRef, "构建产物哈希不一致")
		}
		writePageAppValidationError(w, err)
		return
	}
	now := time.Now()
	diagnostics, _ := json.Marshal([]map[string]any{{
		"level": "info", "code": "static_bundle_verified",
		"message": "静态应用包已重新校验；未执行安装或构建脚本。",
	}})
	build, created, replayed, err := s.store.CreatePageBuildIdempotent(store.CreatePageBuildIdempotentInput{
		CreatePageBuildInput: store.CreatePageBuildInput{
			ProjectID: project.ID, RevisionID: revision.ID, Status: store.PageBuildReady,
			ArtifactRef: artifactRef, ArtifactHash: committed.Hash,
			DiagnosticsJSON: string(diagnostics), RuntimeVersion: pageAppRuntimeVersion,
			StartedAt: now, FinishedAt: now,
		},
		RequestID: requestID, RequestHash: requestHash,
	})
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if created {
		s.recordAutomationLog(nil, "build", "page_app_build", build.ID,
			fmt.Sprintf("生成互动应用构建 %d（请求 %s）", build.ID, requestID))
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", project.ETag())
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"build": build, "created": created})
}

func decodeOptionalPageAppJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_json", "读取 JSON 失败。")
		return false
	}
	if len(raw) > 1<<20 {
		apiError(w, http.StatusRequestEntityTooLarge, "body_too_large", "请求体过大。")
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 格式错误。")
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 只能包含一个对象。")
		return false
	}
	return true
}

func (s *Server) pageAppReadyBuild(projectID, revisionID, requestedBuildID int64) (*store.PageBuild, error) {
	builds, err := s.store.ListPageBuilds(projectID, revisionID, 100)
	if err != nil {
		return nil, err
	}
	for _, build := range builds {
		if build == nil || build.Status != store.PageBuildReady ||
			build.RuntimeVersion != pageAppRuntimeVersion {
			continue
		}
		if requestedBuildID > 0 && build.ID != requestedBuildID {
			continue
		}
		if _, err := securePageAppStorageDir(
			s.store.PageProjectStorageDir(), build.ArtifactRef,
			projectID, revisionID, "artifacts", build.ArtifactHash,
		); err != nil {
			continue
		}
		return build, nil
	}
	return nil, store.ErrPageBuildNotReady
}

func (s *Server) pageAppAdminSourceFiles(projectID, revisionID int64) ([]pageAppAdminFile, error) {
	project, err := s.store.GetPageProject(projectID)
	if err != nil || project == nil {
		if err == nil {
			err = store.ErrPageProjectNotFound
		}
		return nil, err
	}
	if revisionID <= 0 {
		revisionID = project.WorkingRevisionID
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil || revision == nil || revision.ProjectID != project.ID {
		if err == nil {
			err = store.ErrPageRevisionNotFound
		}
		return nil, err
	}
	bundle, err := s.loadPageAppSource(project, revision)
	if err != nil {
		return nil, err
	}
	files := make([]pageAppAdminFile, 0, len(bundle.Files))
	for name, raw := range bundle.Files {
		files = append(files, pageAppAdminFile{
			Path: name, Size: int64(len(raw)), SHA256: bundle.FileHashes[name],
			MediaType: pageAppMediaType(name), Editable: pageAppTextFileEditable(name) &&
				int64(len(raw)) <= pageAppTextEditMaxBytes && utf8.Valid(raw) &&
				bytes.IndexByte(raw, 0) < 0,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (s *Server) pageAppAdminSourceFile(projectID, revisionID int64, name string) ([]byte, string, error) {
	project, err := s.store.GetPageProject(projectID)
	if err != nil || project == nil {
		if err == nil {
			err = store.ErrPageProjectNotFound
		}
		return nil, "", err
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil || revision == nil || revision.ProjectID != project.ID {
		if err == nil {
			err = store.ErrPageRevisionNotFound
		}
		return nil, "", err
	}
	clean, err := validatePageAppPath(name)
	if err != nil {
		return nil, "", err
	}
	bundle, err := s.loadPageAppSource(project, revision)
	if err != nil {
		return nil, "", err
	}
	raw, ok := bundle.Files[clean]
	if !ok {
		return nil, "", os.ErrNotExist
	}
	return append([]byte(nil), raw...), pageAppMediaType(clean), nil
}

func pageAppMediaType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".md", ".txt":
		return "text/plain; charset=utf-8"
	}
	if value := mime.TypeByExtension(strings.ToLower(path.Ext(name))); value != "" {
		return value
	}
	return "application/octet-stream"
}

type pageAppCapabilityDefinition struct {
	Name           string
	Runtime        string
	Grantable      bool
	RequiresBridge bool
	Description    string
}

func pageAppCapabilityDefinitions() map[string]pageAppCapabilityDefinition {
	return map[string]pageAppCapabilityDefinition{
		"input.keyboard": {
			Name: "input.keyboard", Runtime: "sandbox", Grantable: false,
			Description: "沙箱内键盘事件，不访问父页面或后台。",
		},
		"input.touch": {
			Name: "input.touch", Runtime: "sandbox", Grantable: false,
			Description: "沙箱内触屏/指针事件，不访问父页面或后台。",
		},
		"audio.playback": {
			Name: "audio.playback", Runtime: "sandbox", Grantable: false,
			Description: "仅允许用户手势触发的本地媒体播放。",
		},
		"client.storage": {
			Name: "client.storage", Runtime: "bridge", Grantable: true, RequiresBridge: true,
			Description: "由可信父页面提供、按工程隔离且限额的客户端存储。",
		},
		"content.read": {
			Name: "content.read", Runtime: "bridge", Grantable: true, RequiresBridge: true,
			Description: "只读取当前站点已发布、已启用且在授权白名单内的内容。",
		},
		"form.submit": {
			Name: "form.submit", Runtime: "unavailable", Grantable: false, RequiresBridge: true,
			Description: "表单接收服务尚未接入，当前拒绝授权。",
		},
		"external.network": {
			Name: "external.network", Runtime: "unavailable", Grantable: false, RequiresBridge: true,
			Description: "v1 不提供任意外网代理，当前拒绝授权。",
		},
	}
}

func pageAppManifestCapabilities(revision *store.PageProjectRevision) (map[string]bool, error) {
	if revision == nil || revision.RevisionKind != store.PageRevisionApp {
		return nil, pageAppInvalid("page_mode_unsupported", "", "目标不是互动应用修订")
	}
	var manifest pageAppManifest
	dec := json.NewDecoder(strings.NewReader(revision.ManifestJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, pageAppInvalid("manifest_invalid", pageAppManifestName, err.Error())
	}
	out := make(map[string]bool, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		out[capability.Name] = true
	}
	return out, nil
}

func (s *Server) pageAppPublicationDiagnostics(
	project *store.PageProject,
	revision *store.PageProjectRevision,
) []pageValidationDiagnostic {
	if project == nil || revision == nil || project.Mode != store.PageModeApp ||
		revision.RevisionKind != store.PageRevisionApp {
		return nil
	}
	declared, err := pageAppManifestCapabilities(revision)
	if err != nil {
		return []pageValidationDiagnostic{{
			Level: "error", Code: "manifest_invalid",
			Path: "manifest.capabilities", Message: err.Error(),
		}}
	}
	grants, err := s.store.ListPageCapabilityGrants(project.ID)
	if err != nil {
		return []pageValidationDiagnostic{{
			Level: "error", Code: "capability_read_failed",
			Path: "manifest.capabilities", Message: "读取应用能力授权失败。",
		}}
	}
	approved := map[string]bool{}
	for _, grant := range grants {
		if grant != nil && grant.Status == store.PageCapabilityApproved {
			approved[grant.Capability] = true
		}
	}
	definitions := pageAppCapabilityDefinitions()
	var diagnostics []pageValidationDiagnostic
	for name := range declared {
		definition, known := definitions[name]
		switch {
		case !known:
			diagnostics = append(diagnostics, pageValidationDiagnostic{
				Level: "error", Code: "capability_unknown",
				Path: "manifest.capabilities", Message: "未知应用能力：" + name,
			})
		case definition.Runtime == "unavailable":
			diagnostics = append(diagnostics, pageValidationDiagnostic{
				Level: "error", Code: "capability_unavailable",
				Path: "manifest.capabilities", Message: "当前运行时不支持应用能力：" + name,
			})
		case definition.Grantable && !approved[name]:
			diagnostics = append(diagnostics, pageValidationDiagnostic{
				Level: "error", Code: "capability_required",
				Path: "manifest.capabilities", Message: "应用能力尚未批准：" + name,
			})
		}
	}
	sort.Slice(diagnostics, func(i, j int) bool {
		return diagnostics[i].Message < diagnostics[j].Message
	})
	return diagnostics
}

func canonicalPageAppCapabilityConfig(s *Server, capability string, raw json.RawMessage) (string, error) {
	definition, ok := pageAppCapabilityDefinitions()[capability]
	if !ok {
		return "", pageAppInvalid("capability_unknown", "", capability)
	}
	if !definition.Grantable {
		return "", pageAppInvalid("capability_unavailable", "", capability+" 当前不需要或不支持授权")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return "", pageAppInvalid("capability_config_invalid", "", "config 必须是 JSON 对象")
	}
	switch capability {
	case "client.storage":
		var input struct {
			MaxBytes int `json:"max_bytes"`
		}
		if err := decodeStrictRawJSON(raw, &input); err != nil {
			return "", pageAppInvalid("capability_config_invalid", "", err.Error())
		}
		if input.MaxBytes == 0 {
			input.MaxBytes = 64 << 10
		}
		if input.MaxBytes < 1024 || input.MaxBytes > 1<<20 {
			return "", pageAppInvalid("capability_config_invalid", "", "max_bytes 必须在 1 KiB 到 1 MiB 之间")
		}
		raw, _ = json.Marshal(input)
	case "content.read":
		var input struct {
			Types    []string `json:"types"`
			MaxItems int      `json:"max_items"`
		}
		if err := decodeStrictRawJSON(raw, &input); err != nil {
			return "", pageAppInvalid("capability_config_invalid", "", err.Error())
		}
		if len(input.Types) == 0 {
			input.Types = []string{"post"}
		}
		if len(input.Types) > 10 {
			return "", pageAppInvalid("capability_config_invalid", "", "types 数量超过限制")
		}
		seen := map[string]bool{}
		types := make([]string, 0, len(input.Types))
		for _, kind := range input.Types {
			kind = strings.TrimSpace(kind)
			if kind == "post" {
				// built-in article is a public, supported source.
			} else {
				ct := s.lookupType(kind)
				if ct == nil || ct.Builtin || !s.contentTypeActive(kind) {
					return "", pageAppInvalid("capability_config_invalid", "", "内容类型未启用："+kind)
				}
			}
			if kind == "" || seen[kind] {
				return "", pageAppInvalid("capability_config_invalid", "", "types 包含空值或重复项")
			}
			seen[kind] = true
			types = append(types, kind)
		}
		sort.Strings(types)
		input.Types = types
		if input.MaxItems == 0 {
			input.MaxItems = 10
		}
		if input.MaxItems < 1 || input.MaxItems > 20 {
			return "", pageAppInvalid("capability_config_invalid", "", "max_items 必须在 1 到 20 之间")
		}
		raw, _ = json.Marshal(input)
	default:
		return "", pageAppInvalid("capability_unavailable", "", capability)
	}
	canonical, _, err := store.CanonicalJSONHash(string(raw))
	if err != nil {
		return "", err
	}
	return canonical, nil
}

func decodeStrictRawJSON(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("JSON 只能包含一个对象")
	}
	return nil
}

func (s *Server) apiListPageAppCapabilities(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "目标不是互动应用工程。")
		return
	}
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	declared := map[string]bool{}
	if revision != nil && revision.RevisionKind == store.PageRevisionApp {
		declared, err = pageAppManifestCapabilities(revision)
		if err != nil {
			writePageAppValidationError(w, err)
			return
		}
	}
	grants, err := s.store.ListPageCapabilityGrants(project.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	byName := map[string]*store.PageCapabilityGrant{}
	for _, grant := range grants {
		if grant != nil {
			byName[grant.Capability] = grant
		}
	}
	definitions := pageAppCapabilityDefinitions()
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		definition := definitions[name]
		status := "not_requested"
		config := json.RawMessage(`{}`)
		if grant := byName[name]; grant != nil {
			status = grant.Status
			config = json.RawMessage(grant.ConfigJSON)
		}
		items = append(items, map[string]any{
			"name": name, "declared": declared[name], "status": status,
			"grantable": definition.Grantable, "runtime": definition.Runtime,
			"requires_bridge": definition.RequiresBridge,
			"description":     definition.Description, "config": config,
		})
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": project.ID, "revision_id": project.WorkingRevisionID,
		"items": items,
	})
}

func pageAppCapabilityMutationDigest(operation string, projectID int64, capability, config, decision string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		operation, strconv.FormatInt(projectID, 10), capability, config, decision,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *Server) pageAppCapabilityTarget(
	w http.ResponseWriter,
	project *store.PageProject,
	capability string,
) (*store.PageProjectRevision, string, bool) {
	if project.Mode != store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "目标不是互动应用工程。")
		return nil, "", false
	}
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return nil, "", false
	}
	if revision == nil || revision.RevisionKind != store.PageRevisionApp {
		apiError(w, http.StatusConflict, "app_revision_required", "请先上传互动应用包。")
		return nil, "", false
	}
	declared, err := pageAppManifestCapabilities(revision)
	if err != nil {
		writePageAppValidationError(w, err)
		return nil, "", false
	}
	if !declared[capability] {
		apiError(w, http.StatusUnprocessableEntity, "capability_not_declared", "当前应用修订没有声明此能力。")
		return nil, "", false
	}
	return revision, capability, true
}

func (s *Server) apiRequestPageAppCapability(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageCapabilitiesRequest)
	if !ok {
		return
	}
	requestID, ok := pageRequireIdempotencyKey(w, r)
	if !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok || !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	var in pageAppCapabilityMutation
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	in.Capability = strings.TrimSpace(in.Capability)
	if _, _, ok := s.pageAppCapabilityTarget(w, project, in.Capability); !ok {
		return
	}
	config, err := canonicalPageAppCapabilityConfig(s, in.Capability, in.Config)
	if err != nil {
		writePageAppValidationError(w, err)
		return
	}
	mutationRegistry := pageAppCapabilityApprovalRegistryFor(s)
	mutationRegistry.mutation.Lock()
	defer mutationRegistry.mutation.Unlock()
	project, err = s.store.GetPageProject(project.ID)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if project == nil {
		apiError(w, http.StatusNotFound, "project_not_found", "页面工程不存在。")
		return
	}
	if !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	if _, _, ok := s.pageAppCapabilityTarget(w, project, in.Capability); !ok {
		return
	}
	digest := pageAppCapabilityMutationDigest("request", project.ID, in.Capability, config, "")
	receipt, replay, err := s.store.GetPageCapabilityMutationReceipt(project.ID, requestID, digest)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, map[string]any{
			"grant": receipt, "status": receipt.Status, "created": false,
		})
		return
	}
	current, err := s.store.GetPageCapabilityGrant(project.ID, in.Capability)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	stateChanged := current == nil || current.Status != store.PageCapabilityRequested ||
		current.ConfigJSON != config
	grant, executed, err := s.store.UpsertPageCapabilityGrantIdempotent(store.UpsertPageCapabilityGrantInput{
		ProjectID: project.ID, Capability: in.Capability, ConfigJSON: config,
		Status: store.PageCapabilityRequested, RequestedBy: pageAutomationActor(auth),
	}, "request", requestID, digest)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if !executed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	if stateChanged && executed {
		s.recordAutomationLog(auth, "request", "page_app_capability", grant.ID, "申请应用能力："+in.Capability)
	}
	w.Header().Set("ETag", project.ETag())
	status := http.StatusOK
	if stateChanged && executed {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"grant": grant, "status": grant.Status, "created": stateChanged && executed,
	})
}

type pageAppCapabilityApproval struct {
	ID         string
	SiteID     int64
	PageID     int64
	ProjectID  int64
	RevisionID int64
	Capability string
	ConfigHash string
	Decision   string
	ETag       string
	RequestID  string
	ActorID    string
	ExpiresAt  time.Time
	Used       bool
}

type pageAppCapabilityApprovalIssueResult struct {
	Token     string
	ID        string
	ExpiresAt time.Time
}

type pageAppCapabilityApprovalRegistry struct {
	mu       sync.Mutex
	mutation sync.Mutex
	grants   map[[32]byte]*pageAppCapabilityApproval
}

var pageAppCapabilityApprovals sync.Map // map[*Server]*pageAppCapabilityApprovalRegistry

func pageAppCapabilityApprovalRegistryFor(s *Server) *pageAppCapabilityApprovalRegistry {
	value, _ := pageAppCapabilityApprovals.LoadOrStore(s, &pageAppCapabilityApprovalRegistry{
		grants: map[[32]byte]*pageAppCapabilityApproval{},
	})
	return value.(*pageAppCapabilityApprovalRegistry)
}

// issuePageAppCapabilityApprovalToken is intentionally callable only by
// trusted admin/Pilot-native code. It revalidates the complete requested
// capability target; automation API callers cannot mint it.
func (s *Server) issuePageAppCapabilityApprovalToken(
	target pageApprovalConsumeInput,
	actorID string,
	ttl time.Duration,
) (pageAppCapabilityApprovalIssueResult, error) {
	project, err := s.store.GetPageProject(target.ProjectID)
	if err != nil || project == nil {
		if err == nil {
			err = store.ErrPageProjectNotFound
		}
		return pageAppCapabilityApprovalIssueResult{}, err
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" ||
		target.Operation != pageCapabilityGrant ||
		target.Decision != store.PageCapabilityApproved ||
		target.SiteID != s.platformSiteID ||
		target.PageID != project.PostID ||
		target.RevisionID != project.WorkingRevisionID ||
		target.ETag != project.ETag() ||
		strings.TrimSpace(target.RequestID) == "" ||
		strings.TrimSpace(target.Capability) == "" ||
		!validCompositionSHA256(target.ConfigHash) {
		return pageAppCapabilityApprovalIssueResult{}, errors.New("capability approval target changed")
	}
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || revision == nil || revision.ProjectID != project.ID ||
		revision.RevisionKind != store.PageRevisionApp {
		if err == nil {
			err = store.ErrPageRevisionNotFound
		}
		return pageAppCapabilityApprovalIssueResult{}, err
	}
	declared, err := pageAppManifestCapabilities(revision)
	if err != nil || !declared[target.Capability] {
		if err == nil {
			err = errors.New("capability is not declared by working revision")
		}
		return pageAppCapabilityApprovalIssueResult{}, err
	}
	definition, known := pageAppCapabilityDefinitions()[target.Capability]
	if !known || !definition.Grantable {
		return pageAppCapabilityApprovalIssueResult{}, errors.New("capability is unavailable")
	}
	grant, err := s.store.GetPageCapabilityGrant(project.ID, target.Capability)
	if err != nil || grant == nil || grant.Status != store.PageCapabilityRequested {
		if err == nil {
			err = errors.New("capability was not requested")
		}
		return pageAppCapabilityApprovalIssueResult{}, err
	}
	if store.SHA256Hex([]byte(grant.ConfigJSON)) != target.ConfigHash {
		return pageAppCapabilityApprovalIssueResult{}, errors.New("capability requested config changed")
	}
	if ttl <= 0 || ttl > pageAppCapabilityApprovalTTL {
		ttl = pageAppCapabilityApprovalTTL
	}
	secret := make([]byte, 32)
	idRaw := make([]byte, 18)
	if _, err := rand.Read(secret); err != nil {
		return pageAppCapabilityApprovalIssueResult{}, err
	}
	if _, err := rand.Read(idRaw); err != nil {
		return pageAppCapabilityApprovalIssueResult{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	id := "cap_approval_" + base64.RawURLEncoding.EncodeToString(idRaw)
	expiresAt := time.Now().Add(ttl)
	digest := sha256.Sum256([]byte(token))
	registry := pageAppCapabilityApprovalRegistryFor(s)
	registry.mu.Lock()
	registry.grants[digest] = &pageAppCapabilityApproval{
		ID: id, SiteID: target.SiteID, PageID: target.PageID,
		ProjectID: project.ID, RevisionID: target.RevisionID,
		Capability: target.Capability, ConfigHash: target.ConfigHash,
		Decision: target.Decision, ETag: target.ETag, RequestID: target.RequestID,
		ActorID: actorID, ExpiresAt: expiresAt,
	}
	registry.mu.Unlock()
	return pageAppCapabilityApprovalIssueResult{
		Token: token, ID: id, ExpiresAt: expiresAt,
	}, nil
}

func consumePageAppCapabilityApprovalToken(
	s *Server,
	token string,
	target pageApprovalConsumeInput,
	actorID string,
) (*pageAppCapabilityApproval, bool) {
	if token == "" || strings.TrimSpace(actorID) == "" {
		return nil, false
	}
	digest := sha256.Sum256([]byte(token))
	registry := pageAppCapabilityApprovalRegistryFor(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	approval := registry.grants[digest]
	if approval == nil || approval.Used || !time.Now().Before(approval.ExpiresAt) ||
		approval.SiteID != target.SiteID ||
		approval.PageID != target.PageID ||
		approval.ProjectID != target.ProjectID ||
		approval.RevisionID != target.RevisionID ||
		approval.Capability != target.Capability ||
		approval.ConfigHash != target.ConfigHash ||
		approval.Decision != target.Decision ||
		approval.ETag != target.ETag ||
		approval.RequestID != target.RequestID ||
		approval.ActorID != actorID {
		return nil, false
	}
	approval.Used = true
	copy := *approval
	return &copy, true
}

func (s *Server) apiApplyPageAppCapability(w http.ResponseWriter, r *http.Request) {
	s.applyPageAppCapability(w, r, "")
}

func (s *Server) apiRevokePageAppCapability(w http.ResponseWriter, r *http.Request) {
	s.applyPageAppCapability(w, r, store.PageCapabilityRevoked)
}

func (s *Server) applyPageAppCapability(w http.ResponseWriter, r *http.Request, forcedDecision string) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageCapabilitiesGrant)
	if !ok {
		return
	}
	requestID, ok := pageRequireIdempotencyKey(w, r)
	if !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok || !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	var in pageAppCapabilityMutation
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	in.Capability = strings.TrimSpace(in.Capability)
	decision := strings.ToLower(strings.TrimSpace(in.Decision))
	if forcedDecision != "" {
		decision = forcedDecision
	}
	if decision == "approve" {
		decision = store.PageCapabilityApproved
	}
	if decision == "deny" {
		decision = store.PageCapabilityDenied
	}
	if decision != store.PageCapabilityApproved && decision != store.PageCapabilityDenied &&
		decision != store.PageCapabilityRevoked {
		apiError(w, http.StatusUnprocessableEntity, "capability_decision_invalid", "decision 必须是 approve、deny 或 revoke。")
		return
	}
	mutationRegistry := pageAppCapabilityApprovalRegistryFor(s)
	mutationRegistry.mutation.Lock()
	defer mutationRegistry.mutation.Unlock()
	project, err := s.store.GetPageProject(project.ID)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if project == nil {
		apiError(w, http.StatusNotFound, "project_not_found", "页面工程不存在。")
		return
	}
	if !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	if project.Mode != store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "目标不是互动应用工程。")
		return
	}
	current, err := s.store.GetPageCapabilityGrant(project.ID, in.Capability)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if current == nil {
		apiError(w, http.StatusConflict, "capability_not_requested", "能力尚未申请。")
		return
	}
	definition, known := pageAppCapabilityDefinitions()[in.Capability]
	if !known || !definition.Grantable {
		apiError(w, http.StatusUnprocessableEntity, "capability_unavailable", "此能力当前不能授权。")
		return
	}
	config := current.ConfigJSON
	if len(bytes.TrimSpace(in.Config)) != 0 {
		config, err = canonicalPageAppCapabilityConfig(s, in.Capability, in.Config)
		if err != nil {
			writePageAppValidationError(w, err)
			return
		}
	}
	digest := pageAppCapabilityMutationDigest("apply", project.ID, in.Capability, config, decision)
	receipt, replay, err := s.store.GetPageCapabilityMutationReceipt(project.ID, requestID, digest)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, map[string]any{
			"grant": receipt, "status": receipt.Status, "updated": false,
		})
		return
	}
	if len(bytes.TrimSpace(in.Config)) != 0 && config != current.ConfigJSON {
		apiError(w, http.StatusConflict, "capability_config_changed", "批准时不能扩大原申请配置；请重新申请。")
		return
	}
	if decision == store.PageCapabilityApproved {
		if current.Status != store.PageCapabilityRequested {
			apiError(w, http.StatusConflict, "capability_not_requested", "只有待审批能力可以批准。")
			return
		}
		revision, _, targetOK := s.pageAppCapabilityTarget(w, project, in.Capability)
		if !targetOK {
			return
		}
		target := pageApprovalConsumeInput{
			SiteID: s.platformSiteID, PageID: project.PostID,
			ProjectID: project.ID, RevisionID: revision.ID,
			Operation: pageCapabilityGrant, ETag: project.ETag(),
			RequestID: requestID, Capability: in.Capability,
			ConfigHash: store.SHA256Hex([]byte(config)),
			Decision:   store.PageCapabilityApproved,
		}
		approvalToken, nativeState := resolveNativePageApproval(
			s, auth, strings.TrimSpace(r.Header.Get(controlUnlockHeader)), target,
		)
		if _, approved := consumePageAppCapabilityApprovalToken(
			s, approvalToken, target, pageAutomationActor(auth),
		); !approved {
			challenge, challengeErr := issueNativePageChallenge(s, auth, target)
			if challengeErr != nil {
				apiError(w, http.StatusInternalServerError, "confirmation_unavailable", "无法创建能力批准确认挑战。")
				return
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":           "capability_confirmation_required",
				"message":         "unlock_required：请在 Pilot 原生界面确认本次互动应用能力批准，后台密码不会进入对话或技能脚本。",
				"unlock_required": true, "operation": pageCapabilityGrant,
				"unlock_challenge": challenge, "unlock_state": nativeState,
				"site_id": s.platformSiteID, "page_id": project.PostID,
				"project_id": project.ID, "revision_id": revision.ID,
				"capability": in.Capability, "config_hash": target.ConfigHash,
				"decision": target.Decision, "etag": project.ETag(),
				"request_id": requestID,
				"admin_path": "/admin/pages/" + strconv.FormatInt(project.PostID, 10) + "/project",
			})
			return
		}
	}
	approvedBy := ""
	if decision == store.PageCapabilityApproved || decision == store.PageCapabilityDenied ||
		decision == store.PageCapabilityRevoked {
		approvedBy = pageAutomationActor(auth)
	}
	stateChanged := current.Status != decision || current.ConfigJSON != config
	grant, executed, err := s.store.UpsertPageCapabilityGrantIdempotent(store.UpsertPageCapabilityGrantInput{
		ProjectID: project.ID, Capability: in.Capability, ConfigJSON: config,
		Status: decision, RequestedBy: current.RequestedBy, ApprovedBy: approvedBy,
		ExpectedWorkingRevisionID: project.WorkingRevisionID,
		ExpectedCurrentStatus:     current.Status,
		ExpectedCurrentConfigJSON: current.ConfigJSON,
	}, "apply", requestID, digest)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if !executed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	if stateChanged && executed {
		s.recordAutomationLog(auth, decision, "page_app_capability", grant.ID,
			decision+" 应用能力："+in.Capability)
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusOK, map[string]any{
		"grant": grant, "status": grant.Status, "updated": stateChanged && executed,
	})
}

func (s *Server) signPageAppRuntimeClaims(claims pageAppRuntimeClaims) (string, error) {
	if claims.Version != pageAppRuntimeTokenVersion || claims.ProjectID <= 0 ||
		claims.RevisionID <= 0 || claims.BuildID <= 0 ||
		len(claims.ArtifactHash) != sha256.Size*2 ||
		claims.Runtime != pageAppRuntimeVersion || claims.Expires <= 0 ||
		strings.TrimSpace(claims.Nonce) == "" {
		return "", errors.New("invalid page app runtime claims")
	}
	switch claims.Audience {
	case pageAppPreviewShellAudience, pageAppPreviewAssetsAudience,
		pageAppPreviewBridgeAudience, pageAppPublishedBridgeAudience:
	default:
		return "", errors.New("invalid page app runtime audience")
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	secret, err := s.previewSigningSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("page-app-runtime\x00"))
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) newPageAppRuntimeClaims(
	audience string,
	project *store.PageProject,
	revision *store.PageProjectRevision,
	build *store.PageBuild,
	expires time.Time,
) (pageAppRuntimeClaims, string, error) {
	nonceRaw := make([]byte, 18)
	if _, err := rand.Read(nonceRaw); err != nil {
		return pageAppRuntimeClaims{}, "", err
	}
	claims := pageAppRuntimeClaims{
		Version: pageAppRuntimeTokenVersion, Audience: audience,
		SiteID: s.platformSiteID, ProjectID: project.ID, RevisionID: revision.ID,
		BuildID: build.ID, ArtifactHash: build.ArtifactHash,
		Runtime: build.RuntimeVersion, Expires: expires.Unix(),
		Nonce: base64.RawURLEncoding.EncodeToString(nonceRaw),
	}
	token, err := s.signPageAppRuntimeClaims(claims)
	return claims, token, err
}

func (s *Server) verifyPageAppRuntimeToken(token, audience string) (pageAppRuntimeClaims, string) {
	if token == "" || len(token) > 16<<10 {
		return pageAppRuntimeClaims{}, "invalid"
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return pageAppRuntimeClaims{}, "invalid"
	}
	secret, err := s.previewSigningSecret()
	if err != nil {
		return pageAppRuntimeClaims{}, "invalid"
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("page-app-runtime\x00"))
	_, _ = mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return pageAppRuntimeClaims{}, "invalid"
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return pageAppRuntimeClaims{}, "invalid"
	}
	var claims pageAppRuntimeClaims
	if err := decodeStrictRawJSON(raw, &claims); err != nil {
		return pageAppRuntimeClaims{}, "invalid"
	}
	if claims.Audience != audience || claims.Version != pageAppRuntimeTokenVersion ||
		claims.SiteID != s.platformSiteID || claims.Runtime != pageAppRuntimeVersion ||
		claims.ProjectID <= 0 || claims.RevisionID <= 0 || claims.BuildID <= 0 ||
		len(claims.ArtifactHash) != sha256.Size*2 || claims.Nonce == "" {
		return pageAppRuntimeClaims{}, "invalid"
	}
	if !time.Now().Before(time.Unix(claims.Expires, 0)) {
		return pageAppRuntimeClaims{}, "expired"
	}
	return claims, ""
}

func (s *Server) createPageAppPreviewURL(
	w http.ResponseWriter,
	r *http.Request,
	project *store.PageProject,
) {
	var in pageRevisionTargetInput
	if !decodeOptionalPageAppJSON(w, r, &in) {
		return
	}
	revision, ok := s.revisionForValidation(w, project, in.RevisionID)
	if !ok {
		return
	}
	if revision.RevisionKind != store.PageRevisionApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "目标修订不是互动应用。")
		return
	}
	build, err := s.pageAppReadyBuild(project.ID, revision.ID, in.BuildID)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	expires := time.Now().Add(pageAppRuntimeTokenTTL)
	_, token, err := s.newPageAppRuntimeClaims(pageAppPreviewShellAudience, project, revision, build, expires)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "sign_failed", "生成互动应用预览票据失败。")
		return
	}
	base, sitePrefixed := s.frontendPreviewBase(r)
	previewPath := fmt.Sprintf("/preview/page-apps/%d/%d?build=%d&token=%s",
		project.ID, revision.ID, build.ID, url.QueryEscape(token))
	if sitePrefixed {
		previewPath = fmt.Sprintf("/preview/sites/%d/page-apps/%d/%d?build=%d&token=%s",
			s.platformSiteID, project.ID, revision.ID, build.ID, url.QueryEscape(token))
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusCreated, map[string]any{
		"preview_url": absWithBase(base, previewPath),
		"project_id":  project.ID, "revision_id": revision.ID, "build_id": build.ID,
		"runtime_version": build.RuntimeVersion, "artifact_hash": build.ArtifactHash,
		"expires_at": apiTime(expires), "ttl_seconds": int64(pageAppRuntimeTokenTTL.Seconds()),
	})
}

func pageAppPathIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	projectID, ok := pagePathID(w, r, "projectID")
	if !ok {
		return 0, 0, false
	}
	revisionID, ok := pagePathID(w, r, "revisionID")
	if !ok {
		return 0, 0, false
	}
	return projectID, revisionID, true
}

func (s *Server) pageAppProjectRevision(
	projectID, revisionID int64,
) (*store.PageProject, *store.PageProjectRevision, error) {
	project, err := s.store.GetPageProject(projectID)
	if err != nil {
		return nil, nil, err
	}
	if project == nil || project.Mode != store.PageModeApp {
		return nil, nil, store.ErrPageProjectNotFound
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil {
		return nil, nil, err
	}
	if revision == nil || revision.ProjectID != project.ID ||
		revision.RevisionKind != store.PageRevisionApp {
		return nil, nil, store.ErrPageRevisionNotFound
	}
	return project, revision, nil
}

func setPageAppResponseHeaders(w http.ResponseWriter, preview bool) {
	for name, value := range pageAppResponseHeaders() {
		w.Header().Set(name, value)
	}
	if preview {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Del("Set-Cookie")
}

func securePageAppArtifactFile(
	s *Server,
	build *store.PageBuild,
	projectID, revisionID int64,
	name string,
) ([]byte, string, error) {
	clean, err := validatePageAppPath(name)
	if err != nil {
		return nil, "", err
	}
	dir, err := securePageAppStorageDir(
		s.store.PageProjectStorageDir(), build.ArtifactRef,
		projectID, revisionID, "artifacts", build.ArtifactHash,
	)
	if err != nil {
		return nil, "", err
	}
	target := filepath.Join(dir, filepath.FromSlash(clean))
	dirReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, "", err
	}
	targetReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil, "", os.ErrNotExist
	}
	rel, err := filepath.Rel(dirReal, targetReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", pageAppInvalid("unsafe_path", name, "应用资源路径越界")
	}
	info, err := os.Lstat(targetReal)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", os.ErrNotExist
	}
	if info.Size() > pagePlatformServerLimits().MaxAssetBytes {
		return nil, "", pageAppInvalid("file_too_large", name, "应用资源超过大小限制")
	}
	file, err := os.Open(targetReal)
	if err != nil {
		return nil, "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, pagePlatformServerLimits().MaxAssetBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, "", readErr
	}
	if closeErr != nil {
		return nil, "", closeErr
	}
	if int64(len(raw)) != info.Size() {
		return nil, "", pageAppInvalid("size_mismatch", name, "资源读取期间发生变化")
	}
	return raw, pageAppMediaType(clean), nil
}

func writePageAppAsset(w http.ResponseWriter, r *http.Request, raw []byte, mediaType string, preview bool) {
	setPageAppResponseHeaders(w, preview)
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Header().Set("Content-Disposition", "inline")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(raw)
	}
}

func (s *Server) servePublishedPageAppAsset(w http.ResponseWriter, r *http.Request) {
	projectID, revisionID, ok := pageAppPathIDs(w, r)
	if !ok {
		return
	}
	project, _, err := s.pageAppProjectRevision(projectID, revisionID)
	if err != nil || project.PublishedRevisionID != revisionID {
		http.NotFound(w, r)
		return
	}
	build, err := s.pageAppReadyBuild(projectID, revisionID, 0)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	raw, mediaType, err := securePageAppArtifactFile(s, build, projectID, revisionID, r.PathValue("asset"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writePageAppAsset(w, r, raw, mediaType, false)
}

func (s *Server) servePreviewPageAppAsset(w http.ResponseWriter, r *http.Request) {
	projectID, revisionID, ok := pageAppPathIDs(w, r)
	if !ok {
		return
	}
	claims, state := s.verifyPageAppRuntimeToken(
		strings.TrimSpace(r.PathValue("appToken")), pageAppPreviewAssetsAudience,
	)
	if state == "expired" {
		setPageAppResponseHeaders(w, true)
		http.Error(w, "互动应用预览已过期。", http.StatusGone)
		return
	}
	if state != "" || claims.ProjectID != projectID || claims.RevisionID != revisionID {
		http.NotFound(w, r)
		return
	}
	project, _, err := s.pageAppProjectRevision(projectID, revisionID)
	if err != nil || project.WorkingRevisionID != revisionID {
		setPageAppResponseHeaders(w, true)
		http.Error(w, "互动应用修订已变化，请重新生成预览。", http.StatusGone)
		return
	}
	build, err := s.pageAppReadyBuild(projectID, revisionID, claims.BuildID)
	if err != nil || build.ArtifactHash != claims.ArtifactHash {
		setPageAppResponseHeaders(w, true)
		http.Error(w, "互动应用构建已变化，请重新生成预览。", http.StatusGone)
		return
	}
	raw, mediaType, err := securePageAppArtifactFile(s, build, projectID, revisionID, r.PathValue("asset"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writePageAppAsset(w, r, raw, mediaType, true)
}

func (s *Server) frontendPageAppPreview(w http.ResponseWriter, r *http.Request) {
	projectID, revisionID, ok := pageAppPathIDs(w, r)
	if !ok {
		return
	}
	claims, state := s.verifyPageAppRuntimeToken(
		strings.TrimSpace(r.URL.Query().Get("token")), pageAppPreviewShellAudience,
	)
	if state == "expired" {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		http.Error(w, "互动应用预览已过期。", http.StatusGone)
		return
	}
	if state != "" || claims.ProjectID != projectID || claims.RevisionID != revisionID {
		http.NotFound(w, r)
		return
	}
	project, revision, err := s.pageAppProjectRevision(projectID, revisionID)
	if err != nil || project.WorkingRevisionID != revisionID {
		http.Error(w, "互动应用修订已变化，请重新生成预览。", http.StatusGone)
		return
	}
	build, err := s.pageAppReadyBuild(projectID, revisionID, claims.BuildID)
	if err != nil || build.ArtifactHash != claims.ArtifactHash {
		http.Error(w, "互动应用构建已变化，请重新生成预览。", http.StatusGone)
		return
	}
	post, err := s.store.GetPostByID(project.PostID)
	if err != nil || post == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.renderPageAppShell(w, r, post, project, revision, build, true); err != nil {
		http.Error(w, "互动应用预览失败。", http.StatusInternalServerError)
	}
}

// renderPublishedPageApp is the public-dispatch seam used by renderPageBySlug.
// It never falls back to the legacy page body once an app revision owns the
// public pointer.
func (s *Server) renderPublishedPageApp(
	w http.ResponseWriter,
	r *http.Request,
	post *store.Post,
	project *store.PageProject,
	revision *store.PageProjectRevision,
) error {
	if post == nil || project == nil || revision == nil ||
		project.PostID != post.ID || project.Mode != store.PageModeApp ||
		project.PublishedRevisionID != revision.ID || revision.ProjectID != project.ID {
		return errors.New("invalid published app context")
	}
	build, err := s.pageAppReadyBuild(project.ID, revision.ID, 0)
	if err != nil {
		return err
	}
	return s.renderPageAppShell(w, r, post, project, revision, build, false)
}

func (s *Server) renderPublishedPageAppForPost(
	w http.ResponseWriter,
	r *http.Request,
	post *store.Post,
) (bool, error) {
	if post == nil {
		return false, nil
	}
	project, err := s.store.GetPageProjectByPostID(post.ID)
	if err != nil || project == nil || project.Mode != store.PageModeApp ||
		project.PublishedRevisionID == 0 {
		return false, err
	}
	revision, err := s.store.GetPageProjectRevision(project.PublishedRevisionID)
	if err != nil {
		return true, err
	}
	if revision == nil {
		return true, store.ErrPageRevisionNotFound
	}
	return true, s.renderPublishedPageApp(w, r, post, project, revision)
}

var pageAppShellTemplate = htmltemplate.Must(htmltemplate.New("page-app-shell").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="robots" content="{{if .Preview}}noindex,nofollow{{else}}index,follow{{end}}">
<title>{{.Title}}</title>
<style>
:root{color-scheme:light dark;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
*{box-sizing:border-box}html,body{margin:0;min-height:100%;background:#fff;color:#171717}
.gcms-app-shell{min-height:100vh;display:flex;flex-direction:column}
.gcms-app-bar{height:64px;display:flex;align-items:center;padding:0 clamp(16px,4vw,56px);border-bottom:1px solid #e5e5e5;background:#fff}
.gcms-app-brand{font-weight:700;color:inherit;text-decoration:none}
.gcms-app-stage{flex:1;display:flex;min-height:0;padding:{{if .Bare}}0{{else}}clamp(12px,2vw,28px){{end}};background:#f6f6f4}
.gcms-app-frame{display:block;width:100%;min-height:{{if .Bare}}100vh{{else}}calc(100vh - 120px){{end}};border:0;background:#fff;{{if not .Bare}}border-radius:12px;box-shadow:0 1px 4px rgba(0,0,0,.08){{end}}}
.gcms-app-preview{position:fixed;z-index:10;right:12px;top:12px;padding:7px 10px;border-radius:999px;background:#171717;color:#fff;font-size:12px}
@media(max-width:720px){.gcms-app-bar{height:52px}.gcms-app-stage{padding:0}.gcms-app-frame{min-height:calc(100vh - 52px);border-radius:0}}
</style>
</head>
<body>
{{if .Preview}}<div class="gcms-app-preview">草稿预览 · 修订 {{.RevisionID}}</div>{{end}}
<main class="gcms-app-shell">
{{if .ShowHeader}}<header class="gcms-app-bar"><a class="gcms-app-brand" href="{{.HomeURL}}">{{.SiteName}}</a></header>{{end}}
<section class="gcms-app-stage">
<iframe id="gcms-page-app" class="gcms-app-frame" title="{{.Title}}" src="{{.FrameURL}}" sandbox="allow-scripts" allow="" referrerpolicy="no-referrer"></iframe>
</section>
</main>
<script>
(()=>{
"use strict";
const cfg={{.ConfigJSON}};
const frame=document.getElementById("gcms-page-app");
const seen=new Set();
const reply=(id,ok,result,error)=>frame.contentWindow.postMessage({protocol:"gcms-page-bridge/1",request_id:id,ok,result,error},"*");
window.addEventListener("message",async(event)=>{
  if(event.source!==frame.contentWindow)return;
  const msg=event.data;
  if(!msg||msg.protocol!=="gcms-page-bridge/1"||typeof msg.request_id!=="string"||seen.has(msg.request_id))return;
  seen.add(msg.request_id);if(seen.size>256)seen.delete(seen.values().next().value);
  const request={...msg,project_id:cfg.project_id,revision_id:cfg.revision_id};
  try{
    const headers={"Content-Type":"application/json"};if(cfg.bridge_token)headers["X-GCMS-App-Token"]=cfg.bridge_token;
    const response=await fetch(cfg.bridge_url,{method:"POST",credentials:"omit",cache:"no-store",headers,body:JSON.stringify(request)});
    const body=await response.json();
    if(!response.ok||!body.ok){reply(msg.request_id,false,null,body.error||"bridge_failed");return}
    if(msg.capability==="client.storage"){
      const key="gcms-app:"+cfg.project_id+":"+body.storage_key;
      if(msg.action==="get"){let value=null;try{const raw=localStorage.getItem(key);value=raw===null?null:JSON.parse(raw)}catch(_){value=null}reply(msg.request_id,true,{value},null);return}
      if(msg.action==="set"){localStorage.setItem(key,JSON.stringify(msg.payload.value));reply(msg.request_id,true,{stored:true},null);return}
      if(msg.action==="remove"){localStorage.removeItem(key);reply(msg.request_id,true,{removed:true},null);return}
    }
    reply(msg.request_id,true,body.result||{},null);
  }catch(_){reply(msg.request_id,false,null,"bridge_unavailable")}
});
})();
</script>
</body>
</html>`))

func pageAppShellContentSecurityPolicy(bridgeURL string) string {
	connectSource := "'self'"
	if parsed, err := url.Parse(strings.TrimSpace(bridgeURL)); err == nil &&
		(parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" {
		origin := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
		if !strings.ContainsAny(origin, " \t\r\n;") && origin != "" {
			connectSource += " " + origin
		}
	}
	return strings.Join([]string{
		"default-src 'none'",
		"frame-src 'self'",
		"script-src 'unsafe-inline'",
		"style-src 'unsafe-inline'",
		"connect-src " + connectSource,
		"base-uri 'none'",
		"object-src 'none'",
		"form-action 'none'",
		"frame-ancestors 'self'",
	}, "; ") + ";"
}

func pageAppShellResponseHeaders(bridgeURL string) map[string]string {
	return map[string]string{
		"Content-Security-Policy": pageAppShellContentSecurityPolicy(bridgeURL),
		"Permissions-Policy":      pageAppPermissionsPolicy(),
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	}
}

func (s *Server) renderPageAppShell(
	w http.ResponseWriter,
	r *http.Request,
	post *store.Post,
	project *store.PageProject,
	revision *store.PageProjectRevision,
	build *store.PageBuild,
	preview bool,
) error {
	var manifest pageAppManifest
	if err := decodeStrictRawJSON([]byte(revision.ManifestJSON), &manifest); err != nil {
		return err
	}
	expires := time.Now().Add(pageAppRuntimeTokenTTL)
	bridgeAudience := pageAppPublishedBridgeAudience
	if preview {
		bridgeAudience = pageAppPreviewBridgeAudience
	}
	bridgeToken := ""
	if preview {
		var err error
		_, bridgeToken, err = s.newPageAppRuntimeClaims(bridgeAudience, project, revision, build, expires)
		if err != nil {
			return err
		}
	}
	frameURL := fmt.Sprintf("/_gcms/page-apps/%d/%d/%s", project.ID, revision.ID, pathEscapeSegments(manifest.Entry))
	bridgeURL := fmt.Sprintf("/_gcms/page-app-bridge/%d/%d", project.ID, revision.ID)
	if preview {
		_, assetToken, err := s.newPageAppRuntimeClaims(pageAppPreviewAssetsAudience, project, revision, build, expires)
		if err != nil {
			return err
		}
		prefix := "/preview"
		if s.platformRuntimePool() != nil && s.platformSiteID > 0 {
			prefix = "/preview/sites/" + strconv.FormatInt(s.platformSiteID, 10)
		}
		frameURL = fmt.Sprintf("%s/page-app-assets/%s/%d/%d/%s",
			prefix, url.PathEscape(assetToken), project.ID, revision.ID, pathEscapeSegments(manifest.Entry))
		bridgeURL = fmt.Sprintf("%s/page-app-bridge/%d/%d", prefix, project.ID, revision.ID)
	} else {
		bridgeURL = s.pageAppPublishedBridgeURL(r, bridgeURL)
	}
	configJSON, err := json.Marshal(map[string]any{
		"project_id": project.ID, "revision_id": revision.ID,
		"bridge_url": bridgeURL, "bridge_token": bridgeToken,
	})
	if err != nil {
		return err
	}
	site := s.site(post.Lang)
	showHeader := project.ShellMode == store.PageShellSite
	bare := project.ShellMode == store.PageShellNone
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for name, value := range pageAppShellResponseHeaders(bridgeURL) {
		w.Header().Set(name, value)
	}
	w.Header().Del("Set-Cookie")
	if preview {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	} else {
		w.Header().Set("Cache-Control", publicPageCacheControl)
	}
	return pageAppShellTemplate.Execute(w, map[string]any{
		"Lang": post.Lang, "Title": post.Title, "Preview": preview,
		"RevisionID": revision.ID, "ShowHeader": showHeader, "Bare": bare,
		"HomeURL": "/" + post.Lang + "/", "SiteName": site.Name,
		"FrameURL": frameURL, "ConfigJSON": htmltemplate.JS(configJSON),
	})
}

func (s *Server) pageAppPublishedBridgeURL(r *http.Request, bridgePath string) string {
	cfg := s.cloudflareConfigForRequest(r)
	if r != nil {
		if exportConfig, ok := staticExportCloudflareConfig(r.Context()); ok {
			cfg = exportConfig
		}
	}
	requestHost := normalizeCloudflareDomainHost(requestHost(r))
	public := false
	for _, domain := range cfg.publicDomains() {
		if requestHost != "" && sameCloudflareDNSName(requestHost, domain.Host) {
			public = true
			break
		}
	}
	origin := strings.TrimRight(normalizeCloudflareOrigin(cfg.OriginURL), "/")
	if public && origin != "" && !sameCloudflareDNSName(baseURLHost(origin), requestHost) {
		return origin + bridgePath
	}
	return bridgePath
}

func pathEscapeSegments(value string) string {
	parts := strings.Split(value, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

type pageAppBridgeRateRegistry struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

var pageAppBridgeRates sync.Map // map[*Server]*pageAppBridgeRateRegistry

func pageAppBridgeRateRegistryFor(s *Server) *pageAppBridgeRateRegistry {
	value, _ := pageAppBridgeRates.LoadOrStore(s, &pageAppBridgeRateRegistry{
		buckets: map[string][]time.Time{},
	})
	return value.(*pageAppBridgeRateRegistry)
}

func pageAppBridgeRemote(r *http.Request) string {
	if r == nil {
		return ""
	}
	// Reuse the global trusted-proxy rule: X-Forwarded-For is accepted only
	// when the direct peer is loopback (the local Caddy deployment). A public
	// client cannot manufacture arbitrary limiter buckets with a spoofed XFF.
	return clientIP(r)
}

func allowPageAppBridgeCall(s *Server, key string, now time.Time) bool {
	registry := pageAppBridgeRateRegistryFor(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	cutoff := now.Add(-time.Minute)

	// Keep the registry bounded to keys active in the current rate window.
	// Empty and expired buckets are removed globally, not only when the same
	// client happens to call again.
	for bucketKey, bucket := range registry.buckets {
		values := bucket[:0]
		for _, value := range bucket {
			if value.After(cutoff) {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			delete(registry.buckets, bucketKey)
			continue
		}
		registry.buckets[bucketKey] = values
	}

	values := registry.buckets[key]
	if len(values) >= pagePlatformServerLimits().MaxBridgeCallsPerMinute {
		return false
	}
	values = append(values, now)
	registry.buckets[key] = values
	return true
}

func (s *Server) servePageAppBridge(w http.ResponseWriter, r *http.Request) {
	projectID, revisionID, ok := pageAppPathIDs(w, r)
	if !ok {
		return
	}
	preview := strings.HasPrefix(r.URL.Path, "/preview/")
	if !preview {
		s.setPageAppBridgeCORS(w, r)
	}
	if r.Method == http.MethodOptions {
		if preview || w.Header().Get("Access-Control-Allow-Origin") == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	audience := pageAppPublishedBridgeAudience
	if preview {
		audience = pageAppPreviewBridgeAudience
	}
	var claims pageAppRuntimeClaims
	token := strings.TrimSpace(r.Header.Get(pageAppRuntimeTokenHeader))
	if preview {
		var state string
		claims, state = s.verifyPageAppRuntimeToken(token, audience)
		if state != "" || claims.ProjectID != projectID || claims.RevisionID != revisionID {
			apiError(w, http.StatusUnauthorized, "bridge_token_invalid", "互动应用运行票据无效或已过期。")
			return
		}
	}
	project, revision, err := s.pageAppProjectRevision(projectID, revisionID)
	if err != nil {
		apiError(w, http.StatusNotFound, "revision_not_found", "互动应用修订不存在。")
		return
	}
	if preview {
		if project.WorkingRevisionID != revisionID {
			apiError(w, http.StatusGone, "bridge_context_expired", "预览修订已变化。")
			return
		}
	} else if project.PublishedRevisionID != revisionID {
		apiError(w, http.StatusGone, "bridge_context_expired", "公开修订已变化。")
		return
	}
	requestedBuildID := claims.BuildID
	build, err := s.pageAppReadyBuild(projectID, revisionID, requestedBuildID)
	if err != nil || (preview && build.ArtifactHash != claims.ArtifactHash) {
		apiError(w, http.StatusGone, "bridge_context_expired", "运行构建已变化。")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, pagePlatformServerLimits().MaxBridgeRequestBytes)
	raw, err := io.ReadAll(io.LimitReader(r.Body, pagePlatformServerLimits().MaxBridgeRequestBytes+1))
	if err != nil || int64(len(raw)) > pagePlatformServerLimits().MaxBridgeRequestBytes {
		apiError(w, http.StatusRequestEntityTooLarge, "bridge_request_too_large", "Bridge 消息超过大小限制。")
		return
	}
	grants, err := s.store.ListPageCapabilityGrants(project.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	request, err := validatePageAppBridgeRequest(raw, projectID, revisionID, grants, pagePlatformServerLimits())
	if err != nil {
		writePageAppBridgeValidationError(w, err)
		return
	}
	declared, err := pageAppManifestCapabilities(revision)
	if err != nil || !declared[request.Capability] {
		apiError(w, http.StatusForbidden, "bridge_capability_not_declared", "当前修订没有声明此能力。")
		return
	}
	definition := pageAppCapabilityDefinitions()[request.Capability]
	if !definition.Grantable || definition.Runtime != "bridge" {
		apiError(w, http.StatusForbidden, "bridge_capability_unavailable", "此能力当前没有受控运行实现。")
		return
	}
	rateKey := strings.Join([]string{
		strconv.FormatInt(projectID, 10), strconv.FormatInt(revisionID, 10),
		request.Capability, pageAppBridgeRemote(r),
	}, ":")
	if !allowPageAppBridgeCall(s, rateKey, time.Now()) {
		w.Header().Set("Retry-After", "60")
		apiError(w, http.StatusTooManyRequests, "bridge_rate_limited", "Bridge 调用超过每分钟限制。")
		return
	}
	grant, err := s.store.GetPageCapabilityGrant(projectID, request.Capability)
	if err != nil || grant == nil || grant.Status != store.PageCapabilityApproved {
		apiError(w, http.StatusForbidden, "bridge_capability_not_granted", "能力已拒绝或撤销。")
		return
	}
	result, err := s.executePageAppBridge(request, grant)
	if err != nil {
		writePageAppBridgeValidationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "request_id": request.RequestID, "result": result,
		"storage_key": result["storage_key"],
	})
}

func (s *Server) setPageAppBridgeCORS(w http.ResponseWriter, r *http.Request) {
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" {
		return
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || (origin.Scheme != "https" && origin.Scheme != "http") ||
		origin.Hostname() == "" || origin.Path != "" {
		return
	}
	host := normalizeCloudflareDomainHost(origin.Host)
	allowed := false
	for _, domain := range s.cloudflareConfigForRequest(r).publicDomains() {
		if sameCloudflareDNSName(host, domain.Host) {
			allowed = true
			break
		}
	}
	if !allowed {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", rawOrigin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Max-Age", "300")
	w.Header().Add("Vary", "Origin")
}

func (s *Server) executePageAppBridge(
	request pageAppBridgeRequest,
	grant *store.PageCapabilityGrant,
) (map[string]any, error) {
	switch request.Capability {
	case "client.storage":
		var config struct {
			MaxBytes int `json:"max_bytes"`
		}
		if err := decodeStrictRawJSON([]byte(grant.ConfigJSON), &config); err != nil {
			return nil, pageAppInvalid("bridge_config_invalid", "", err.Error())
		}
		var payload struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value,omitempty"`
		}
		if err := decodeStrictRawJSON(request.Payload, &payload); err != nil {
			return nil, pageAppInvalid("bridge_payload_invalid", "", err.Error())
		}
		if !pageAppStorageKeyPattern.MatchString(payload.Key) {
			return nil, pageAppInvalid("bridge_payload_invalid", "", "存储 key 无效")
		}
		if request.Action == "set" {
			if len(payload.Value) == 0 || !json.Valid(payload.Value) ||
				len(payload.Value) > config.MaxBytes {
				return nil, pageAppInvalid("bridge_payload_invalid", "", "存储值无效或超过授权大小")
			}
		}
		return map[string]any{
			"authorized": true, "storage_key": payload.Key,
			"max_bytes": config.MaxBytes, "action": request.Action,
		}, nil
	case "content.read":
		return s.executePageAppContentRead(request, grant)
	default:
		return nil, pageAppInvalid("bridge_capability_unavailable", "", request.Capability)
	}
}

func (s *Server) executePageAppContentRead(
	request pageAppBridgeRequest,
	grant *store.PageCapabilityGrant,
) (map[string]any, error) {
	var config struct {
		Types    []string `json:"types"`
		MaxItems int      `json:"max_items"`
	}
	if err := decodeStrictRawJSON([]byte(grant.ConfigJSON), &config); err != nil {
		return nil, pageAppInvalid("bridge_config_invalid", "", err.Error())
	}
	allowed := map[string]bool{}
	for _, kind := range config.Types {
		allowed[kind] = true
	}
	var payload struct {
		ID    int64  `json:"id,omitempty"`
		Type  string `json:"type"`
		Lang  string `json:"lang"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := decodeStrictRawJSON(request.Payload, &payload); err != nil {
		return nil, pageAppInvalid("bridge_payload_invalid", "", err.Error())
	}
	payload.Type = strings.TrimSpace(payload.Type)
	payload.Lang = strings.TrimSpace(payload.Lang)
	if !allowed[payload.Type] || !s.langEnabled(payload.Lang) {
		return nil, pageAppInvalid("bridge_payload_invalid", "", "内容类型或语种不在授权范围")
	}
	toItem := func(post *store.Post) map[string]any {
		return map[string]any{
			"id": post.ID, "type": post.Type, "title": post.Title, "slug": post.Slug,
			"excerpt": post.Excerpt, "cover_image": post.CoverImage, "lang": post.Lang,
			"url":          "/" + post.Lang + publicContentPath(post.Type, post.Slug),
			"published_at": apiTime(post.PublishedAt),
		}
	}
	if request.Action == "get" {
		if payload.ID <= 0 {
			return nil, pageAppInvalid("bridge_payload_invalid", "", "id 必填")
		}
		post, err := s.store.GetPostByID(payload.ID)
		if err != nil {
			return nil, err
		}
		if post == nil || post.Status != "published" || post.Type != payload.Type || post.Lang != payload.Lang {
			return map[string]any{"item": nil}, nil
		}
		return map[string]any{"item": toItem(post)}, nil
	}
	if request.Action != "query" {
		return nil, pageAppInvalid("bridge_action_invalid", "", request.Action)
	}
	limit := payload.Limit
	if limit == 0 || limit > config.MaxItems {
		limit = config.MaxItems
	}
	if limit < 1 {
		return nil, pageAppInvalid("bridge_payload_invalid", "", "limit 无效")
	}
	posts, err := s.store.ListPublishedByType(payload.Type, payload.Lang, 0, 0, limit)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(posts))
	for _, post := range posts {
		items = append(items, toItem(post))
	}
	return map[string]any{"items": items}, nil
}
