package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/store"
)

const (
	compositionPreviewTokenVersion = 1
	compositionPreviewTokenKind    = "composition_revision"
)

type compositionPreviewClaims struct {
	Version          int    `json:"v"`
	Kind             string `json:"kind"`
	SiteID           int64  `json:"site_id,omitempty"`
	PageID           int64  `json:"page_id"`
	ProjectID        int64  `json:"project_id"`
	RevisionID       int64  `json:"revision_id"`
	BuildID          int64  `json:"build_id,omitempty"`
	ManifestHash     string `json:"manifest_hash"`
	DataSnapshotHash string `json:"data_snapshot_hash"`
	RenderHash       string `json:"render_hash"`
	Runtime          string `json:"runtime"`
	Expires          int64  `json:"exp"`
}

func (s *Server) registerCompositionPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /page-assets/{assetID}/{sha256}", s.servePublishedCompositionAsset)
	mux.HandleFunc("POST /api/forms/contact", s.submitCompositionContact)
	mux.HandleFunc(
		"GET /preview/page-compositions/{projectID}/{revisionID}",
		s.frontendCompositionPreview,
	)
	mux.HandleFunc(
		"GET /preview/page-composition-assets/{projectID}/{revisionID}/{assetID}/{sha256}",
		s.servePreviewCompositionAsset,
	)
}

func compositionPageDiagnostics(values []CompositionDiagnostic) []pageValidationDiagnostic {
	out := make([]pageValidationDiagnostic, 0, len(values))
	for _, value := range values {
		out = append(out, pageValidationDiagnostic{
			Level: value.Level, Code: value.Code, Path: value.Path, Message: value.Message,
		})
	}
	return out
}

// pageRevisionValidation is the publication-wide validation entry point. It
// upgrades composition projects to the full runtime check while retaining the
// existing validation contract for standard/app revisions.
func (s *Server) pageRevisionValidation(
	ctx context.Context,
	project *store.PageProject,
	revision *store.PageProjectRevision,
) pageValidationResult {
	if project == nil || revision == nil || project.Mode != store.PageModeComposition ||
		revision.RevisionKind != store.PageRevisionComposition {
		return basicPageRevisionValidation(project, revision)
	}
	build, err := s.ValidateCompositionBuild(
		ctx, project, revision, CompositionBindingPublishedOnly,
	)
	result := pageValidationResult{
		RevisionID: revision.ID, RuntimeVersion: compositionRuntimeVersion,
		Diagnostics: []pageValidationDiagnostic{},
	}
	if build != nil {
		result.ManifestHash = build.ManifestHash
		result.DataSnapshotHash = build.DataSnapshotHash
		result.RenderHash = build.RenderHash
		result.Diagnostics = compositionPageDiagnostics(build.Diagnostics)
		result.Valid = build.Valid && err == nil
	}
	if build == nil && err != nil {
		result.Diagnostics = compositionPageDiagnostics(compositionDiagnosticsFromError(err))
	}
	return result
}

func decodeOptionalCompositionJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
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
	if err := decodeCompositionStrict(raw, dst); err != nil {
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 格式错误或包含未知字段。")
		return false
	}
	return true
}

func (s *Server) createCompositionBuild(
	w http.ResponseWriter,
	r *http.Request,
	project *store.PageProject,
	requestID string,
) {
	var input pageRevisionTargetInput
	if !decodeOptionalCompositionJSON(w, r, &input) {
		return
	}
	if input.BuildID != 0 {
		apiError(w, http.StatusBadRequest, "build_id_not_allowed", "创建构建时不能指定 build_id。")
		return
	}
	revision, ok := s.revisionForValidation(w, project, input.RevisionID)
	if !ok {
		return
	}
	buildResult, err := s.ValidateCompositionBuild(
		r.Context(), project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		if errors.Is(err, store.ErrPageInvalid) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "composition_invalid", "message": "自由页面未通过构建校验。",
				"validation": buildResult,
			})
			return
		}
		apiError(w, http.StatusInternalServerError, "composition_build_failed", "自由页面构建校验失败。")
		return
	}

	diagnostics, marshalErr := json.Marshal(buildResult.Diagnostics)
	if marshalErr != nil {
		apiError(w, http.StatusInternalServerError, "composition_build_failed", "序列化构建诊断失败。")
		return
	}
	requestHash, err := canonicalPageBuildRequestHash(pageBuildRequestIdentity{
		SchemaVersion: 1, ProjectID: project.ID, RevisionID: revision.ID,
		Mode: project.Mode, RevisionKind: revision.RevisionKind,
		RuntimeVersion: compositionRuntimeVersion, ManifestHash: buildResult.ManifestHash,
		ArtifactHash: buildResult.RenderHash, DataSnapshotHash: buildResult.DataSnapshotHash,
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, "composition_build_failed", "生成构建请求摘要失败。")
		return
	}
	now := time.Now()
	build, created, replayed, err := s.store.CreatePageBuildIdempotent(store.CreatePageBuildIdempotentInput{
		CreatePageBuildInput: store.CreatePageBuildInput{
			ProjectID: project.ID, RevisionID: revision.ID, Status: store.PageBuildReady,
			ArtifactRef:  "composition:ssr/" + buildResult.RenderHash,
			ArtifactHash: buildResult.RenderHash, DiagnosticsJSON: string(diagnostics),
			RuntimeVersion: compositionRuntimeVersion, StartedAt: now, FinishedAt: now,
		},
		RequestID: requestID, RequestHash: requestHash,
	})
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if created {
		s.recordAutomationLog(nil, "build", "page_composition_build", build.ID,
			fmt.Sprintf("生成自由页面构建 %d（请求 %s）", build.ID, requestID))
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", project.ETag())
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{
		"build": build, "created": created,
		"manifest_hash":      buildResult.ManifestHash,
		"data_snapshot_hash": buildResult.DataSnapshotHash,
	})
}

func (s *Server) compositionPreviewSignature(encodedPayload string) (string, error) {
	secret, err := s.previewSigningSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("composition-page-preview\x00"))
	_, _ = mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) signCompositionPreviewClaims(claims compositionPreviewClaims) (string, error) {
	if claims.Version != compositionPreviewTokenVersion ||
		claims.Kind != compositionPreviewTokenKind ||
		claims.PageID <= 0 || claims.ProjectID <= 0 || claims.RevisionID <= 0 ||
		claims.BuildID < 0 ||
		!validCompositionSHA256(claims.ManifestHash) ||
		!validCompositionSHA256(claims.DataSnapshotHash) ||
		!validCompositionSHA256(claims.RenderHash) ||
		claims.Runtime != compositionRuntimeVersion || claims.Expires <= 0 {
		return "", errors.New("invalid composition preview claims")
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	signature, err := s.compositionPreviewSignature(payload)
	if err != nil {
		return "", err
	}
	return payload + "." + signature, nil
}

func (s *Server) verifyCompositionPreviewToken(token string) (compositionPreviewClaims, string) {
	if token == "" || len(token) > 12<<10 {
		return compositionPreviewClaims{}, "invalid"
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return compositionPreviewClaims{}, "invalid"
	}
	want, err := s.compositionPreviewSignature(parts[0])
	if err != nil || !hmac.Equal([]byte(want), []byte(parts[1])) {
		return compositionPreviewClaims{}, "invalid"
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return compositionPreviewClaims{}, "invalid"
	}
	var claims compositionPreviewClaims
	if err := decodeCompositionStrict(raw, &claims); err != nil {
		return compositionPreviewClaims{}, "invalid"
	}
	if claims.Version != compositionPreviewTokenVersion ||
		claims.Kind != compositionPreviewTokenKind ||
		claims.SiteID != s.platformSiteID ||
		claims.PageID <= 0 || claims.ProjectID <= 0 || claims.RevisionID <= 0 ||
		claims.BuildID < 0 ||
		!validCompositionSHA256(claims.ManifestHash) ||
		!validCompositionSHA256(claims.DataSnapshotHash) ||
		!validCompositionSHA256(claims.RenderHash) ||
		claims.Runtime != compositionRuntimeVersion {
		return compositionPreviewClaims{}, "invalid"
	}
	if !time.Now().Before(time.Unix(claims.Expires, 0)) {
		return compositionPreviewClaims{}, "expired"
	}
	return claims, ""
}

func (s *Server) createCompositionPreviewURL(
	w http.ResponseWriter,
	r *http.Request,
	project *store.PageProject,
) {
	var input pageRevisionTargetInput
	if !decodeOptionalCompositionJSON(w, r, &input) {
		return
	}
	if input.RevisionID < 0 || input.BuildID < 0 {
		apiError(w, http.StatusBadRequest, "invalid_preview_target",
			"revision_id 与 build_id 不能为负数。")
		return
	}
	revision, ok := s.revisionForValidation(w, project, input.RevisionID)
	if !ok {
		return
	}
	build, err := s.ValidateCompositionBuild(
		r.Context(), project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		if errors.Is(err, store.ErrPageInvalid) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "composition_invalid", "message": "自由页面未通过预览校验。",
				"validation": build,
			})
			return
		}
		apiError(w, http.StatusInternalServerError, "composition_preview_failed", "自由页面预览校验失败。")
		return
	}
	var selectedBuild *store.PageBuild
	if input.BuildID > 0 {
		selectedBuild, err = s.store.GetPageBuild(input.BuildID)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if selectedBuild == nil || selectedBuild.ProjectID != project.ID ||
			selectedBuild.RevisionID != revision.ID {
			apiError(w, http.StatusNotFound, "build_not_found", "页面构建不存在。")
			return
		}
		if selectedBuild.Status != store.PageBuildReady ||
			selectedBuild.RuntimeVersion != compositionRuntimeVersion {
			apiError(w, http.StatusConflict, "build_not_ready", "目标构建尚未就绪。")
			return
		}
		if selectedBuild.ArtifactHash != build.RenderHash {
			apiError(w, http.StatusConflict, "build_stale",
				"真实数据或主题上下文已变化，请重新构建后再预览。")
			return
		}
	}
	expires := time.Now().Add(frontendPreviewTTL)
	token, err := s.signCompositionPreviewClaims(compositionPreviewClaims{
		Version: compositionPreviewTokenVersion, Kind: compositionPreviewTokenKind,
		SiteID: s.platformSiteID, PageID: project.PostID, ProjectID: project.ID,
		RevisionID: revision.ID, BuildID: input.BuildID, ManifestHash: build.ManifestHash,
		DataSnapshotHash: build.DataSnapshotHash, RenderHash: build.RenderHash,
		Runtime: compositionRuntimeVersion, Expires: expires.Unix(),
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, "sign_failed", "生成自由页面预览票据失败。")
		return
	}
	base, sitePrefixed := s.frontendPreviewBase(r)
	previewPath := fmt.Sprintf("/preview/page-compositions/%d/%d?token=%s",
		project.ID, revision.ID, url.QueryEscape(token))
	if sitePrefixed {
		previewPath = fmt.Sprintf("/preview/sites/%d/page-compositions/%d/%d?token=%s",
			s.platformSiteID, project.ID, revision.ID, url.QueryEscape(token))
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusCreated, map[string]any{
		"preview_url": absWithBase(base, previewPath),
		"project_id":  project.ID, "revision_id": revision.ID, "build_id": input.BuildID,
		"manifest_hash": build.ManifestHash, "data_snapshot_hash": build.DataSnapshotHash,
		"render_hash":     build.RenderHash,
		"runtime_version": compositionRuntimeVersion, "data_scope": "published",
		"expires_at": apiTime(expires), "ttl_seconds": int64(frontendPreviewTTL.Seconds()),
	})
}

func compositionPathID(r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(r.PathValue(name)), 10, 64)
	return value, err == nil && value > 0
}

func (s *Server) frontendCompositionPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	projectID, ok := compositionPathID(r, "projectID")
	if !ok {
		s.notFound(w, r)
		return
	}
	revisionID, ok := compositionPathID(r, "revisionID")
	if !ok {
		s.notFound(w, r)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	claims, state := s.verifyCompositionPreviewToken(token)
	if state == "expired" {
		http.Error(w, "自由页面预览链接已过期，请重新生成。", http.StatusGone)
		return
	}
	if state != "" || claims.ProjectID != projectID || claims.RevisionID != revisionID {
		s.notFound(w, r)
		return
	}
	rendered, err := s.RenderCompositionRevisionPreview(
		r, projectID, revisionID, CompositionBindingPublishedOnly,
	)
	if err != nil {
		if errors.Is(err, store.ErrPageInvalid) {
			http.Error(w, "自由页面预览数据已变化，请重新校验并生成链接。", http.StatusGone)
			return
		}
		s.serverError(w, err)
		return
	}
	if rendered.Page.ID != claims.PageID || rendered.Build.ManifestHash != claims.ManifestHash ||
		rendered.Build.DataSnapshotHash != claims.DataSnapshotHash ||
		rendered.Build.RenderHash != claims.RenderHash ||
		rendered.Build.RuntimeVersion != claims.Runtime {
		http.Error(w, "自由页面修订已变化，请重新生成预览链接。", http.StatusGone)
		return
	}
	if claims.BuildID > 0 {
		build, err := s.store.GetPageBuild(claims.BuildID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if build == nil || build.ProjectID != projectID || build.RevisionID != revisionID ||
			build.Status != store.PageBuildReady ||
			build.RuntimeVersion != compositionRuntimeVersion ||
			build.ArtifactHash != rendered.Build.RenderHash {
			http.Error(w, "自由页面构建已失效，请重新构建并生成预览链接。", http.StatusGone)
			return
		}
	}
	for _, asset := range rendered.Build.Assets {
		previewPath := fmt.Sprintf(
			"/preview/page-composition-assets/%d/%d/%d/%s?token=%s",
			projectID, revisionID, asset.ID, asset.SHA256, url.QueryEscape(token),
		)
		if s.platformSiteID > 0 && s.platformRuntimePool() != nil {
			previewPath = fmt.Sprintf(
				"/preview/sites/%d/page-composition-assets/%d/%d/%d/%s?token=%s",
				s.platformSiteID, projectID, revisionID, asset.ID, asset.SHA256,
				url.QueryEscape(token),
			)
		}
		rendered.DocumentHTML = bytes.ReplaceAll(
			rendered.DocumentHTML, []byte(asset.URL), []byte(previewPath),
		)
	}
	s.WriteCompositionPage(w, rendered, http.StatusOK)
}

func compositionManifestReferencesAsset(
	manifest *CompositionManifest,
	assetID int64,
	hash string,
) bool {
	found := false
	if manifest == nil || assetID <= 0 || !validCompositionSHA256(hash) {
		return false
	}
	walkCompositionSections(manifest.Sections, func(section *CompositionSection, _ string) {
		if section.Media != nil && section.Media.AssetID == assetID &&
			hmac.Equal([]byte(section.Media.SHA256), []byte(hash)) {
			found = true
		}
	})
	return found
}

func (s *Server) compositionAssetForProject(
	projectID, revisionID, assetID int64,
	hash string,
) (*store.PageAsset, error) {
	project, err := s.store.GetPageProject(projectID)
	if err != nil {
		return nil, err
	}
	if project == nil || project.Mode != store.PageModeComposition {
		return nil, store.ErrPageProjectNotFound
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil {
		return nil, err
	}
	if revision == nil || revision.ProjectID != project.ID ||
		revision.RevisionKind != store.PageRevisionComposition {
		return nil, store.ErrPageRevisionNotFound
	}
	manifest, _, _, err := NormalizeCompositionManifest([]byte(revision.ManifestJSON))
	if err != nil {
		return nil, err
	}
	if !compositionManifestReferencesAsset(manifest, assetID, hash) {
		return nil, os.ErrNotExist
	}
	asset, err := s.store.GetPageAsset(assetID)
	if err != nil {
		return nil, err
	}
	if asset == nil || asset.ProjectID != project.ID ||
		!hmac.Equal([]byte(asset.SHA256), []byte(hash)) {
		return nil, os.ErrNotExist
	}
	return asset, nil
}

func (s *Server) readCompositionAsset(asset *store.PageAsset) ([]byte, error) {
	if asset == nil || asset.ProjectID <= 0 || !validCompositionSHA256(asset.SHA256) {
		return nil, os.ErrNotExist
	}
	ref := strings.TrimSpace(asset.StorageRef)
	if ref == "" || strings.Contains(ref, `\`) || strings.Contains(ref, "://") ||
		path.IsAbs(ref) || path.Clean(ref) != ref {
		return nil, os.ErrNotExist
	}
	expected := "page-projects/" + strconv.FormatInt(asset.ProjectID, 10) + "/"
	if !strings.HasPrefix(ref, expected) {
		return nil, os.ErrNotExist
	}
	privateRoot := s.store.PageProjectStorageDir()
	if strings.TrimSpace(privateRoot) == "" {
		return nil, os.ErrNotExist
	}
	dataRoot := filepath.Dir(privateRoot)
	rootReal, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(dataRoot, filepath.FromSlash(ref))
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, os.ErrNotExist
	}
	targetReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil, os.ErrNotExist
	}
	rel, err := filepath.Rel(rootReal, targetReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, os.ErrNotExist
	}
	limit := pagePlatformServerLimits().MaxAssetBytes
	if info.Size() < 0 || info.Size() > limit || info.Size() != asset.ByteSize {
		return nil, os.ErrInvalid
	}
	file, err := os.Open(targetReal)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(raw)) != asset.ByteSize || int64(len(raw)) > limit {
		return nil, os.ErrInvalid
	}
	sum := sha256.Sum256(raw)
	if !hmac.Equal([]byte(hex.EncodeToString(sum[:])), []byte(asset.SHA256)) {
		return nil, os.ErrInvalid
	}
	return raw, nil
}

func serveCompositionAssetBytes(
	w http.ResponseWriter,
	r *http.Request,
	raw []byte,
	mediaType string,
	preview bool,
) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if preview {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	} else {
		w.Header().Set("Cache-Control", uploadCacheControl)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(raw)
	}
}

func (s *Server) servePublishedCompositionAsset(w http.ResponseWriter, r *http.Request) {
	assetID, ok := compositionPathID(r, "assetID")
	if !ok {
		s.notFound(w, r)
		return
	}
	hash := strings.TrimSpace(r.PathValue("sha256"))
	asset, err := s.store.GetPageAsset(assetID)
	if err != nil || asset == nil || !hmac.Equal([]byte(asset.SHA256), []byte(hash)) {
		s.notFound(w, r)
		return
	}
	project, err := s.store.GetPageProject(asset.ProjectID)
	if err != nil || project == nil || project.PublishedRevisionID <= 0 {
		s.notFound(w, r)
		return
	}
	asset, err = s.compositionAssetForProject(
		project.ID, project.PublishedRevisionID, assetID, hash,
	)
	if err != nil {
		s.notFound(w, r)
		return
	}
	raw, err := s.readCompositionAsset(asset)
	if err != nil {
		s.notFound(w, r)
		return
	}
	serveCompositionAssetBytes(w, r, raw, asset.MediaType, false)
}

func (s *Server) servePreviewCompositionAsset(w http.ResponseWriter, r *http.Request) {
	projectID, ok := compositionPathID(r, "projectID")
	if !ok {
		s.notFound(w, r)
		return
	}
	revisionID, ok := compositionPathID(r, "revisionID")
	if !ok {
		s.notFound(w, r)
		return
	}
	assetID, ok := compositionPathID(r, "assetID")
	if !ok {
		s.notFound(w, r)
		return
	}
	hash := strings.TrimSpace(r.PathValue("sha256"))
	claims, state := s.verifyCompositionPreviewToken(strings.TrimSpace(r.URL.Query().Get("token")))
	if state == "expired" {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "自由页面素材预览已过期。", http.StatusGone)
		return
	}
	if state != "" || claims.ProjectID != projectID || claims.RevisionID != revisionID {
		s.notFound(w, r)
		return
	}
	asset, err := s.compositionAssetForProject(projectID, revisionID, assetID, hash)
	if err != nil {
		s.notFound(w, r)
		return
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil || revision == nil {
		s.notFound(w, r)
		return
	}
	_, _, manifestHash, err := NormalizeCompositionManifest([]byte(revision.ManifestJSON))
	if err != nil || manifestHash != claims.ManifestHash {
		http.Error(w, "自由页面修订已变化，请重新生成预览链接。", http.StatusGone)
		return
	}
	raw, err := s.readCompositionAsset(asset)
	if err != nil {
		s.notFound(w, r)
		return
	}
	serveCompositionAssetBytes(w, r, raw, asset.MediaType, true)
}
