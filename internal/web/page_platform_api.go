package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cms.ccvar.com/internal/store"
)

const (
	pageApprovalPublish  = "pages.publish"
	pageApprovalRollback = "pages.rollback"
	pageCapabilityGrant  = "page_capabilities.grant"

	pagePlatformApprovalTTL = 10 * time.Minute
)

// registerPagePlatformAPIRoutes exposes the same contract through the
// single-site and platform-prefixed entry points. Platform routing still
// authenticates and selects the site before these handlers run.
func (s *Server) registerPagePlatformAPIRoutes(mux *http.ServeMux) {
	register := func(prefix string) {
		mux.HandleFunc("GET "+prefix+"/page-projects", s.apiListPageProjects)
		mux.HandleFunc("POST "+prefix+"/page-projects", s.apiCreatePageProject)
		mux.HandleFunc("GET "+prefix+"/page-projects/{projectID}", s.apiGetPageProject)
		mux.HandleFunc("PATCH "+prefix+"/page-projects/{projectID}", s.apiUpdatePageProject)
		mux.HandleFunc("POST "+prefix+"/pages/{pageID}/convert-plan", s.apiPageConvertPlan)
		mux.HandleFunc("POST "+prefix+"/pages/{pageID}/convert", s.apiPageConvert)

		mux.HandleFunc("GET "+prefix+"/page-projects/{projectID}/revisions", s.apiListPageProjectRevisions)
		mux.HandleFunc("GET "+prefix+"/page-projects/{projectID}/revisions/{revisionID}", s.apiGetPageProjectRevision)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/revisions", s.apiCreatePageProjectRevision)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/restore", s.apiRestorePageProjectRevision)

		mux.HandleFunc("GET "+prefix+"/page-projects/{projectID}/assets", s.apiListPageProjectAssets)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/assets", s.apiCreatePageProjectAsset)
		mux.HandleFunc("DELETE "+prefix+"/page-projects/{projectID}/assets/{assetID}", s.apiDeletePageProjectAsset)

		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/validate", s.apiValidatePageProject)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/builds", s.apiCreatePageProjectBuild)
		mux.HandleFunc("GET "+prefix+"/page-projects/{projectID}/builds/{buildID}", s.apiGetPageProjectBuild)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/preview-url", s.apiCreatePageProjectPreviewURL)

		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/publish-plan", s.apiPageProjectPublishPlan)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/publish", s.apiPublishPageProject)
		mux.HandleFunc("GET "+prefix+"/page-projects/{projectID}/publications", s.apiListPageProjectPublications)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/rollback-plan", s.apiPageProjectRollbackPlan)
		mux.HandleFunc("POST "+prefix+"/page-projects/{projectID}/rollback", s.apiRollbackPageProject)
	}
	register("/api/admin/v1")
	register("/api/platform/v1/sites/{siteID}")
	s.registerCompositionAPIRoutes(mux)
	s.registerPageAppAPIRoutes(mux)
}

type pageProjectEnvelope struct {
	Project  *store.PageProject         `json:"project"`
	Revision *store.PageProjectRevision `json:"working_revision,omitempty"`
}

type pageProjectListItem struct {
	Project *store.PageProject       `json:"project"`
	Page    pageProjectPageSummary   `json:"page"`
	ETag    string                   `json:"etag"`
	Links   pageProjectListItemLinks `json:"_links"`
}

type pageProjectPageSummary struct {
	ID     int64  `json:"id"`
	Lang   string `json:"lang"`
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

type pageProjectListItemLinks struct {
	AdminPath string `json:"admin_path"`
}

type pageProjectCreateInput struct {
	PageID        int64  `json:"page_id"`
	PostID        int64  `json:"post_id"`
	Mode          string `json:"mode"`
	SchemaVersion int    `json:"schema_version"`
	ShellMode     string `json:"shell_mode"`
}

type pageRevisionCreateInput struct {
	BaseRevisionID  int64           `json:"base_revision_id"`
	PageMeta        json.RawMessage `json:"page_meta"`
	Manifest        json.RawMessage `json:"manifest"`
	StandardContent string          `json:"standard_content"`
	SourceBundleRef string          `json:"source_bundle_ref"`
	SourceHash      string          `json:"source_hash"`
	Summary         string          `json:"summary"`
	ConversationID  string          `json:"conversation_id"`
}

type pageRestoreInput struct {
	RevisionID int64  `json:"revision_id"`
	Summary    string `json:"summary"`
}

type pageAssetInput struct {
	LogicalKey string          `json:"logical_key"`
	StorageRef string          `json:"storage_ref"`
	MediaType  string          `json:"media_type"`
	ByteSize   int64           `json:"byte_size"`
	SHA256     string          `json:"sha256"`
	Origin     string          `json:"origin"`
	Provenance json.RawMessage `json:"provenance"`
	Width      int             `json:"width"`
	Height     int             `json:"height"`
}

type pageRevisionTargetInput struct {
	RevisionID int64 `json:"revision_id"`
	BuildID    int64 `json:"build_id"`
}

type pageBuildRequestIdentity struct {
	SchemaVersion    int    `json:"schema_version"`
	ProjectID        int64  `json:"project_id"`
	RevisionID       int64  `json:"revision_id"`
	Mode             string `json:"mode"`
	RevisionKind     string `json:"revision_kind"`
	RuntimeVersion   string `json:"runtime_version"`
	ManifestHash     string `json:"manifest_hash"`
	SourceHash       string `json:"source_hash,omitempty"`
	ArtifactHash     string `json:"artifact_hash"`
	DataSnapshotHash string `json:"data_snapshot_hash,omitempty"`
}

func canonicalPageBuildRequestHash(identity pageBuildRequestIdentity) (string, error) {
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	_, hash, err := store.CanonicalJSONHash(string(raw))
	return hash, err
}

type pagePublicationInput struct {
	RevisionID       int64  `json:"revision_id"`
	BuildID          int64  `json:"build_id"`
	ApprovalToken    string `json:"approval_token"`
	DataSnapshotHash string `json:"data_snapshot_hash"`
	DeploymentJobID  string `json:"deployment_job_id"`
}

type pagePublicationRequestIdentity struct {
	SchemaVersion    int    `json:"schema_version"`
	SiteID           int64  `json:"site_id"`
	ProjectID        int64  `json:"project_id"`
	Operation        string `json:"operation"`
	ActorID          string `json:"actor_id"`
	Origin           string `json:"origin"`
	ETag             string `json:"etag"`
	RevisionID       int64  `json:"revision_id"`
	BuildID          int64  `json:"build_id"`
	DataSnapshotHash string `json:"data_snapshot_hash"`
	DeploymentJobID  string `json:"deployment_job_id"`
}

func canonicalPagePublicationRequestHash(
	identity pagePublicationRequestIdentity,
) (string, error) {
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	_, hash, err := store.CanonicalJSONHash(string(raw))
	return hash, err
}

type pageValidationDiagnostic struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type pageValidationResult struct {
	Valid            bool                       `json:"valid"`
	RevisionID       int64                      `json:"revision_id"`
	RuntimeVersion   string                     `json:"runtime_version,omitempty"`
	ManifestHash     string                     `json:"manifest_hash,omitempty"`
	DataSnapshotHash string                     `json:"data_snapshot_hash,omitempty"`
	RenderHash       string                     `json:"render_hash,omitempty"`
	Diagnostics      []pageValidationDiagnostic `json:"diagnostics"`
}

func (s *Server) requirePagePlatformScope(w http.ResponseWriter, r *http.Request, scope string) (*automationAuth, bool) {
	auth, ok := s.requireAutomationToken(w, r)
	if !ok {
		return nil, false
	}
	if !pagePlatformScopeAllowed(auth.scopes, scope) {
		apiError(w, http.StatusForbidden, "missing_scope", "访问权限不足。")
		return nil, false
	}
	return auth, true
}

func pageAutomationActor(auth *automationAuth) string {
	if auth == nil {
		return ""
	}
	if auth.platform {
		return "platform-key:" + strconv.FormatInt(auth.platKeyID, 10)
	}
	if auth.key != nil {
		return "automation-key:" + strconv.FormatInt(auth.key.ID, 10)
	}
	return "automation"
}

func pagePathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(r.PathValue(name)), 10, 64)
	if err != nil || value <= 0 {
		apiError(w, http.StatusBadRequest, "bad_id", "ID 无效。")
		return 0, false
	}
	return value, true
}

func (s *Server) pageProjectByPath(w http.ResponseWriter, r *http.Request) (*store.PageProject, bool) {
	id, ok := pagePathID(w, r, "projectID")
	if !ok {
		return nil, false
	}
	project, err := s.store.GetPageProject(id)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return nil, false
	}
	if project == nil {
		apiError(w, http.StatusNotFound, "project_not_found", "页面工程不存在。")
		return nil, false
	}
	return project, true
}

func pageRequireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get(pagePlatformIdempotencyHeader))
	if key == "" {
		apiError(w, http.StatusBadRequest, "idempotency_key_required", "写操作必须提供 Idempotency-Key。")
		return "", false
	}
	if len(key) > 200 {
		apiError(w, http.StatusBadRequest, "idempotency_key_invalid", "Idempotency-Key 过长。")
		return "", false
	}
	return key, true
}

func pageRequireETag(w http.ResponseWriter, r *http.Request, current string, currentRevisionID int64) bool {
	expected := strings.TrimSpace(r.Header.Get(pagePlatformConcurrencyHeader))
	if expected == "" {
		apiError(w, http.StatusPreconditionRequired, "if_match_required", "写操作必须提供 If-Match。")
		return false
	}
	if expected != current {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":               "revision_conflict",
			"message":             "页面已被其他操作更新。",
			"expected_etag":       expected,
			"current_etag":        current,
			"current_revision_id": currentRevisionID,
		})
		return false
	}
	return true
}

// pageRequireRevisionMutationETag preserves optimistic concurrency without
// breaking a byte-for-byte retry after the first request already advanced the
// work pointer. The Store remains the authority that compares every semantic
// input before returning the original immutable revision.
func (s *Server) pageRequireRevisionMutationETag(
	w http.ResponseWriter,
	r *http.Request,
	project *store.PageProject,
	requestID string,
	declaredBaseRevisionID int64,
) bool {
	expected := strings.TrimSpace(r.Header.Get(pagePlatformConcurrencyHeader))
	if expected == "" {
		apiError(w, http.StatusPreconditionRequired, "if_match_required", "写操作必须提供 If-Match。")
		return false
	}
	if expected == project.ETag() {
		return true
	}
	revisions, err := s.store.ListPageProjectRevisions(project.ID, 500)
	if err == nil {
		for _, revision := range revisions {
			if revision.RequestID != requestID {
				continue
			}
			if expected == store.PageRevisionETag(revision.ParentRevisionID) &&
				(declaredBaseRevisionID == 0 || declaredBaseRevisionID == revision.ParentRevisionID) {
				return true
			}
			break
		}
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":               "revision_conflict",
		"message":             "页面已被其他操作更新。",
		"expected_etag":       expected,
		"current_etag":        project.ETag(),
		"current_revision_id": project.WorkingRevisionID,
	})
	return false
}

func pageStoreError(w http.ResponseWriter, err error) {
	var revisionConflict *store.PageRevisionConflictError
	var idempotencyConflict *store.PageIdempotencyConflictError
	var routeConflict *store.PageRouteConflictError
	switch {
	case errors.As(err, &revisionConflict):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":                "revision_conflict",
			"message":              "页面已被其他操作更新。",
			"expected_revision_id": revisionConflict.ExpectedRevisionID,
			"current_revision_id":  revisionConflict.CurrentRevisionID,
		})
	case errors.As(err, &idempotencyConflict):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "idempotency_conflict",
			"message":    "同一幂等键已用于不同请求。",
			"request_id": idempotencyConflict.RequestID,
		})
	case errors.As(err, &routeConflict):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "route_conflict", "message": "页面路由已被占用。",
			"lang": routeConflict.Lang, "slug": routeConflict.Slug,
		})
	case errors.Is(err, store.ErrPageProjectNotFound):
		apiError(w, http.StatusNotFound, "project_not_found", "页面工程不存在。")
	case errors.Is(err, store.ErrPageRevisionNotFound):
		apiError(w, http.StatusNotFound, "revision_not_found", "页面修订不存在。")
	case errors.Is(err, store.ErrPageBuildNotReady):
		apiError(w, http.StatusConflict, "build_not_ready", "目标修订尚未验证并构建成功。")
	case errors.Is(err, store.ErrPageProjectExists):
		apiError(w, http.StatusConflict, "project_exists", "该页面已经存在页面工程。")
	case errors.Is(err, store.ErrPagePostRequired):
		apiError(w, http.StatusUnprocessableEntity, "page_required", "目标内容不是页面。")
	case errors.Is(err, store.ErrPageInvalid):
		apiError(w, http.StatusUnprocessableEntity, "page_invalid", err.Error())
	default:
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
	}
}

func writePageProject(w http.ResponseWriter, status int, project *store.PageProject, revision *store.PageProjectRevision) {
	if project != nil {
		w.Header().Set("ETag", project.ETag())
	}
	writeJSON(w, status, pageProjectEnvelope{Project: project, Revision: revision})
}

func pageJSON(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}

func standardPageConversionETag(p *store.Post) string {
	return `"content-` + previewRevision(p) + `"`
}

func pageMetaForPost(p *store.Post) string {
	meta, err := store.PageRevisionMetaFromPost(p).CanonicalJSON()
	if err != nil {
		return "{}"
	}
	return meta
}

func (s *Server) apiPageConvertPlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsWrite); !ok {
		return
	}
	pageID, ok := pagePathID(w, r, "pageID")
	if !ok {
		return
	}
	p, err := s.store.GetPostByID(pageID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if p == nil || p.Type != "page" {
		apiError(w, http.StatusNotFound, "page_not_found", "页面不存在。")
		return
	}
	existing, err := s.store.GetPageProjectByPostID(pageID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	etag := standardPageConversionETag(p)
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, map[string]any{
		"page_id":      pageID,
		"current_etag": etag,
		"convertible":  existing == nil,
		"existing_project_id": func() int64 {
			if existing != nil {
				return existing.ID
			}
			return 0
		}(),
		"impact": map[string]any{
			"old_page_mutated":          false,
			"baseline_snapshot_created": existing == nil,
			"public_route_changed":      false,
		},
	})
}

func (s *Server) apiPageConvert(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsWrite)
	if !ok {
		return
	}
	pageID, ok := pagePathID(w, r, "pageID")
	if !ok {
		return
	}
	var in pageProjectCreateInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	in.PageID = pageID
	s.createPageProjectFromPage(w, r, auth, in)
}

func (s *Server) apiCreatePageProject(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsWrite)
	if !ok {
		return
	}
	var in pageProjectCreateInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if in.PageID <= 0 {
		in.PageID = in.PostID
	}
	s.createPageProjectFromPage(w, r, auth, in)
}

func (s *Server) apiListPageProjects(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	pageID := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("page_id")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			apiError(w, http.StatusBadRequest, "bad_page_id", "page_id 必须是正整数。")
			return
		}
		pageID = value
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang != "" && !s.langEnabled(lang) {
		apiError(w, http.StatusUnprocessableEntity, "language_invalid", "请求语种未在当前站点启用。")
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	if mode != "" && mode != store.PageModeComposition && mode != store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "mode 必须是 composition 或 app。")
		return
	}
	limit := apiIntParam(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := apiIntParam(r, "offset", 0)

	projects, err := s.store.ListPageProjects()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	matched := make([]pageProjectListItem, 0, len(projects))
	for _, project := range projects {
		if project == nil || (pageID > 0 && project.PostID != pageID) ||
			(mode != "" && project.Mode != mode) {
			continue
		}
		page, readErr := s.store.GetPostByID(project.PostID)
		if readErr != nil {
			apiError(w, http.StatusInternalServerError, "store_error", readErr.Error())
			return
		}
		if page == nil || page.Type != "page" ||
			(lang != "" && page.Lang != lang) ||
			(slug != "" && page.Slug != slug) {
			continue
		}
		matched = append(matched, pageProjectListItem{
			Project: project,
			Page: pageProjectPageSummary{
				ID: page.ID, Lang: page.Lang, Slug: page.Slug, Title: page.Title,
				Status: page.Status, URL: s.apiContentURL(page),
			},
			ETag: project.ETag(),
			Links: pageProjectListItemLinks{
				AdminPath: "/admin/pages/" + strconv.FormatInt(page.ID, 10) + "/project",
			},
		})
	}
	total := len(matched)
	if offset >= total {
		matched = []pageProjectListItem{}
	} else {
		matched = matched[offset:]
		if len(matched) > limit {
			matched = matched[:limit]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": matched, "total": total, "limit": limit, "offset": offset,
		"filters": map[string]any{
			"page_id": pageID, "lang": lang, "slug": slug, "mode": mode,
		},
	})
}

func (s *Server) createPageProjectFromPage(w http.ResponseWriter, r *http.Request, auth *automationAuth, in pageProjectCreateInput) {
	requestID, ok := pageRequireIdempotencyKey(w, r)
	if !ok {
		return
	}
	if in.PageID <= 0 {
		apiError(w, http.StatusBadRequest, "page_required", "page_id 必填。")
		return
	}
	if in.Mode != store.PageModeComposition && in.Mode != store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "page_mode_unsupported", "页面模式必须是 composition 或 app。")
		return
	}
	if in.Mode == store.PageModeApp && !pagePlatformScopeAllowed(auth.scopes, apiScopePageAppsWrite) {
		apiError(w, http.StatusForbidden, "missing_scope", "创建互动应用还需要 page_apps:write。")
		return
	}
	if in.SchemaVersion == 0 {
		in.SchemaVersion = 1
	}
	if in.SchemaVersion != 1 {
		apiError(w, http.StatusUnprocessableEntity, "manifest_invalid", "当前只支持 schema_version=1。")
		return
	}
	if in.ShellMode == "" {
		in.ShellMode = store.PageShellSite
	}
	if in.ShellMode != store.PageShellSite && in.ShellMode != store.PageShellMinimal && in.ShellMode != store.PageShellNone {
		apiError(w, http.StatusUnprocessableEntity, "page_invalid", "shell_mode 无效。")
		return
	}
	p, err := s.store.GetPostByID(in.PageID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if p == nil || p.Type != "page" {
		apiError(w, http.StatusNotFound, "page_not_found", "页面不存在。")
		return
	}
	if !pageRequireETag(w, r, standardPageConversionETag(p), 0) {
		return
	}
	existing, err := s.store.GetPageProjectByPostID(in.PageID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var project *store.PageProject
	if existing != nil {
		revision, readErr := s.store.GetPageProjectRevision(existing.WorkingRevisionID)
		if readErr == nil && revision != nil && revision.RequestID == requestID &&
			existing.Mode == in.Mode && existing.ShellMode == in.ShellMode &&
			existing.SchemaVersion == in.SchemaVersion {
			w.Header().Set("Idempotent-Replayed", "true")
			writePageProject(w, http.StatusOK, existing, revision)
			return
		}
		if existing.WorkingRevisionID == 0 && existing.Mode == in.Mode &&
			existing.ShellMode == in.ShellMode && existing.SchemaVersion == in.SchemaVersion {
			// Recover a prior process interruption between sidecar creation and
			// its first immutable baseline. The old page is still untouched.
			project = existing
		} else {
			apiError(w, http.StatusConflict, "project_exists", "该页面已经存在页面工程。")
			return
		}
	}
	if project == nil {
		project, err = s.store.CreatePageProject(store.CreatePageProjectInput{
			PostID: in.PageID, Mode: in.Mode, SchemaVersion: in.SchemaVersion,
			ShellMode: in.ShellMode, CreatedBy: store.PageOriginAPI,
		})
		if err != nil {
			pageStoreError(w, err)
			return
		}
	}
	revision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: 0,
		RevisionKind: store.PageRevisionStandardBaseline,
		PageMetaJSON: pageMetaForPost(p), ManifestJSON: "{}",
		StandardContent: p.Content, Origin: store.PageOriginAPI,
		ActorID: pageAutomationActor(auth), RequestID: requestID,
		Summary: "转换前标准页面基线", ValidationJSON: `{"valid":true,"source":"standard_page"}`,
	})
	if err != nil {
		// Store intentionally has no unsafe project deletion primitive. Leaving
		// an empty sidecar project is recoverable and never changes the old page.
		pageStoreError(w, err)
		return
	}
	project, _ = s.store.GetPageProject(project.ID)
	s.invalidatePageProjectDraft()
	s.recordAutomationLog(auth, "create", "page_project", project.ID, "创建页面工程")
	writePageProject(w, http.StatusCreated, project, revision)
}

func (s *Server) apiGetPageProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writePageProject(w, http.StatusOK, project, revision)
}

func (s *Server) apiUpdatePageProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsWrite); !ok {
		return
	}
	if _, ok := pageRequireIdempotencyKey(w, r); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok || !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	apiError(w, http.StatusNotImplemented, "project_update_not_supported",
		"工程配置当前不可原地修改；请创建新的不可变页面修订。")
}

func (s *Server) apiListPageProjectRevisions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListPageProjectRevisions(project.ID, apiIntParam(r, "limit", 100))
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "project_id": project.ID})
}

func (s *Server) apiGetPageProjectRevision(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	revisionID, ok := pagePathID(w, r, "revisionID")
	if !ok {
		return
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if revision == nil || revision.ProjectID != project.ID {
		apiError(w, http.StatusNotFound, "revision_not_found", "页面修订不存在。")
		return
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

func (s *Server) apiCreatePageProjectRevision(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsWrite)
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
	var in pageRevisionCreateInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if !s.pageRequireRevisionMutationETag(w, r, project, requestID, in.BaseRevisionID) {
		return
	}
	if in.BaseRevisionID != project.WorkingRevisionID {
		revisions, _ := s.store.ListPageProjectRevisions(project.ID, 500)
		replay := false
		for _, revision := range revisions {
			if revision.RequestID == requestID && revision.ParentRevisionID == in.BaseRevisionID {
				replay = true
				break
			}
		}
		if replay {
			// CreatePageProjectRevision performs the full semantic equality
			// check before consulting the now-advanced work pointer.
		} else {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "revision_conflict", "message": "base_revision_id 不是当前工作修订。",
				"expected_revision_id": in.BaseRevisionID,
				"current_revision_id":  project.WorkingRevisionID,
			})
			return
		}
	}
	if project.Mode == store.PageModeApp && !pagePlatformScopeAllowed(auth.scopes, apiScopePageAppsWrite) {
		apiError(w, http.StatusForbidden, "missing_scope", "修改互动应用还需要 page_apps:write。")
		return
	}
	if project.Mode == store.PageModeApp {
		apiError(w, http.StatusUnprocessableEntity, "app_package_required",
			"互动应用修订只能由 app-package 上传端点创建，不能提交客户端 source_bundle_ref。")
		return
	}
	validationJSON := "{}"
	if project.Mode == store.PageModeComposition {
		if strings.TrimSpace(in.StandardContent) != "" ||
			strings.TrimSpace(in.SourceBundleRef) != "" ||
			strings.TrimSpace(in.SourceHash) != "" {
			apiError(w, http.StatusUnprocessableEntity, "composition_payload_invalid",
				"自由编排修订不能携带标准正文或应用源码引用。")
			return
		}
		var meta store.PageRevisionMeta
		if err := decodeCompositionStrict(in.PageMeta, &meta); err != nil ||
			strings.TrimSpace(meta.Lang) == "" {
			apiError(w, http.StatusUnprocessableEntity, "page_meta_invalid",
				"页面元数据无效或包含未知字段。")
			return
		}
		if !s.langEnabled(meta.Lang) {
			apiError(w, http.StatusUnprocessableEntity, "language_invalid",
				"页面语种未在当前站点启用。")
			return
		}
		validation := s.NormalizeAndValidateCompositionManifest(in.Manifest, meta.Lang)
		if !validation.Valid {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "composition_invalid", "message": "自由页面 Manifest 未通过校验。",
				"validation": validation,
			})
			return
		}
		in.Manifest = json.RawMessage(validation.CanonicalJSON)
		raw, _ := json.Marshal(map[string]any{
			"valid": true, "manifest_hash": validation.ManifestHash,
			"diagnostics": validation.Diagnostics,
		})
		validationJSON = string(raw)
	}
	revision, created, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: in.BaseRevisionID,
		RevisionKind: project.Mode, PageMetaJSON: pageJSON(in.PageMeta, ""),
		ManifestJSON: pageJSON(in.Manifest, ""), StandardContent: in.StandardContent,
		SourceBundleRef: strings.TrimSpace(in.SourceBundleRef), SourceHash: strings.TrimSpace(in.SourceHash),
		Origin: store.PageOriginAPI, ActorID: pageAutomationActor(auth),
		ConversationID: strings.TrimSpace(in.ConversationID), RequestID: requestID,
		Summary: strings.TrimSpace(in.Summary), ValidationJSON: validationJSON,
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
	s.recordAutomationLog(auth, "create", "page_revision", revision.ID, "创建页面工程修订")
	writePageProject(w, func() int {
		if created {
			return http.StatusCreated
		}
		return http.StatusOK
	}(), project, revision)
}

func (s *Server) apiRestorePageProjectRevision(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsWrite)
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
	var in pageRestoreInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	// Restore advances the working pointer, so an exact retry must reuse the
	// immutable parent recorded by the first result instead of treating that
	// result as a new base. CreatePageProjectRevision remains the authority
	// that compares the copied target, actor, summary, and all other semantic
	// fields before returning the original revision.
	baseRevisionID := project.WorkingRevisionID
	revisions, err := s.store.ListPageProjectRevisions(project.ID, 500)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	for _, revision := range revisions {
		if revision != nil && revision.RequestID == requestID {
			baseRevisionID = revision.ParentRevisionID
			break
		}
	}
	if !s.pageRequireRevisionMutationETag(w, r, project, requestID, baseRevisionID) {
		return
	}
	target, err := s.store.GetPageProjectRevision(in.RevisionID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if target == nil || target.ProjectID != project.ID {
		apiError(w, http.StatusNotFound, "revision_not_found", "要恢复的修订不存在。")
		return
	}
	if project.Mode == store.PageModeApp && !pagePlatformScopeAllowed(auth.scopes, apiScopePageAppsWrite) {
		apiError(w, http.StatusForbidden, "missing_scope", "恢复互动应用还需要 page_apps:write。")
		return
	}
	kind := target.RevisionKind
	if kind == store.PageRevisionStandardBaseline {
		kind = project.Mode
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		summary = fmt.Sprintf("恢复到修订 %d", target.ID)
	}
	revision, created, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: baseRevisionID,
		RevisionKind: kind, PageMetaJSON: target.PageMetaJSON,
		ManifestJSON: target.ManifestJSON, StandardContent: target.StandardContent,
		SourceBundleRef: target.SourceBundleRef, SourceHash: target.SourceHash,
		Origin: store.PageOriginRestore, ActorID: pageAutomationActor(auth),
		RequestID: requestID, Summary: summary, ValidationJSON: target.ValidationJSON,
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
	s.recordAutomationLog(auth, "restore", "page_revision", revision.ID, summary)
	writePageProject(w, func() int {
		if created {
			return http.StatusCreated
		}
		return http.StatusOK
	}(), project, revision)
}

func (s *Server) apiListPageProjectAssets(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListPageAssets(project.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "project_id": project.ID})
}

func validPageAssetMetadata(projectID int64, in *pageAssetInput, existing []*store.PageAsset) error {
	if in == nil {
		return errors.New("资源元数据不能为空")
	}
	in.LogicalKey = strings.TrimSpace(in.LogicalKey)
	in.StorageRef = strings.TrimSpace(in.StorageRef)
	in.MediaType = strings.ToLower(strings.TrimSpace(in.MediaType))
	in.SHA256 = strings.ToLower(strings.TrimSpace(in.SHA256))
	if in.Origin == "" {
		in.Origin = "upload"
	}
	if in.LogicalKey == "" || in.LogicalKey == "." || len(in.LogicalKey) > 240 ||
		!utf8.ValidString(in.LogicalKey) || strings.HasPrefix(in.LogicalKey, "/") ||
		path.Clean(in.LogicalKey) != in.LogicalKey || strings.Contains(in.LogicalKey, "..") ||
		strings.IndexFunc(in.LogicalKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errors.New("logical_key 必须是安全的相对路径")
	}
	wantPrefix := "page-projects/" + strconv.FormatInt(projectID, 10) + "/"
	if !strings.HasPrefix(in.StorageRef, wantPrefix) || strings.Contains(in.StorageRef, `\`) ||
		path.Clean(in.StorageRef) != in.StorageRef || strings.Contains(in.StorageRef, "://") {
		return errors.New("storage_ref 必须位于当前工程的 page-projects/<id>/ 路径")
	}
	allowedMedia := map[string]bool{
		"image/png": true, "image/jpeg": true, "image/webp": true,
		"image/gif": true,
	}
	if !allowedMedia[in.MediaType] {
		return errors.New("media_type 不在页面资源白名单中")
	}
	limits := pagePlatformServerLimits()
	if in.ByteSize < 0 || in.ByteSize > limits.MaxAssetBytes {
		return errors.New("资源大小超过服务端限制")
	}
	decoded, err := hex.DecodeString(in.SHA256)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("sha256 必须是小写 SHA-256 十六进制摘要")
	}
	if len(existing) >= limits.MaxAssets {
		for _, asset := range existing {
			if asset != nil && asset.SHA256 == in.SHA256 {
				return nil
			}
		}
		return errors.New("工程资源数量超过服务端限制")
	}
	var total int64
	for _, asset := range existing {
		if asset != nil && asset.SHA256 == in.SHA256 {
			return nil
		}
		total += asset.ByteSize
	}
	if total+in.ByteSize > limits.MaxProjectAssetBytes {
		return errors.New("工程资源总大小超过服务端限制")
	}
	return nil
}

func (s *Server) apiCreatePageProjectAsset(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePageAssetsWrite)
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
	s.createCompositionAssetUpload(w, r, project, auth, requestID)
}

func (s *Server) apiDeletePageProjectAsset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageAssetsWrite); !ok {
		return
	}
	if _, ok := pageRequireIdempotencyKey(w, r); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok || !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	assetID, ok := pagePathID(w, r, "assetID")
	if !ok {
		return
	}
	asset, err := s.store.GetPageAsset(assetID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if asset == nil || asset.ProjectID != project.ID {
		apiError(w, http.StatusNotFound, "asset_not_found", "页面资源不存在。")
		return
	}
	apiError(w, http.StatusNotImplemented, "asset_delete_not_supported",
		"当前版本没有可验证引用关系的资源删除事务，因此拒绝删除。")
}

func basicPageRevisionValidation(project *store.PageProject, revision *store.PageProjectRevision) pageValidationResult {
	out := pageValidationResult{RevisionID: revision.ID, Diagnostics: []pageValidationDiagnostic{}}
	add := func(code, field, message string) {
		out.Diagnostics = append(out.Diagnostics, pageValidationDiagnostic{
			Level: "error", Code: code, Path: field, Message: message,
		})
	}
	if project == nil || revision == nil || revision.ProjectID != project.ID {
		add("revision_not_found", "", "修订不属于当前工程。")
		out.Valid = false
		return out
	}
	if int64(len(revision.ManifestJSON)) > pagePlatformServerLimits().MaxManifestBytes {
		add("manifest_too_large", "manifest", "Manifest 超过服务端大小限制。")
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal([]byte(revision.ManifestJSON), &manifest); err != nil || manifest == nil {
		add("manifest_invalid", "manifest", "Manifest 必须是 JSON 对象。")
	}
	if revision.RevisionKind != store.PageRevisionStandardBaseline &&
		revision.RevisionKind != project.Mode {
		add("revision_kind_mismatch", "revision_kind", "修订类型与工程模式不一致。")
	}
	if revision.RevisionKind == store.PageRevisionComposition {
		var version int
		if raw := manifest["schema_version"]; len(raw) == 0 {
			add("schema_version_required", "manifest.schema_version", "自由编排 Manifest 缺少 schema_version。")
		} else if json.Unmarshal(raw, &version) != nil || version != 1 {
			add("schema_version_unsupported", "manifest.schema_version", "当前只支持 schema_version=1。")
		}
		if raw := manifest["sections"]; len(raw) != 0 {
			var sections []json.RawMessage
			if json.Unmarshal(raw, &sections) != nil {
				add("sections_invalid", "manifest.sections", "sections 必须是数组。")
			} else if len(sections) > pagePlatformServerLimits().MaxSections {
				add("sections_limit", "manifest.sections", "sections 数量超过服务端限制。")
			}
		}
	}
	if revision.RevisionKind == store.PageRevisionApp &&
		(revision.SourceBundleRef == "" || revision.SourceHash == "") {
		add("app_source_required", "source_bundle_ref", "互动应用修订必须绑定不可变源码包。")
	}
	out.Valid = len(out.Diagnostics) == 0
	return out
}

func (s *Server) revisionForValidation(w http.ResponseWriter, project *store.PageProject, revisionID int64) (*store.PageProjectRevision, bool) {
	if revisionID <= 0 {
		revisionID = project.WorkingRevisionID
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return nil, false
	}
	if revision == nil || revision.ProjectID != project.ID {
		apiError(w, http.StatusNotFound, "revision_not_found", "页面修订不存在。")
		return nil, false
	}
	return revision, true
}

func (s *Server) apiValidatePageProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsBuild); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok || !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	var in pageRevisionTargetInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	revision, ok := s.revisionForValidation(w, project, in.RevisionID)
	if !ok {
		return
	}
	result := s.pageRevisionValidation(r.Context(), project, revision)
	status := http.StatusOK
	if !result.Valid {
		status = http.StatusUnprocessableEntity
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, status, result)
}

func (s *Server) apiCreatePageProjectBuild(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsBuild); !ok {
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
	if project.Mode == store.PageModeApp {
		s.createPageAppBuild(w, r, project, requestID)
		return
	}
	if project.Mode == store.PageModeComposition {
		s.createCompositionBuild(w, r, project, requestID)
		return
	}
	// A build marked ready is a publication trust boundary. Until a sandboxed
	// composition/app builder supplies an immutable artifact, creating a fake
	// ready build here would let unvalidated code reach PublishPageProject.
	apiError(w, http.StatusNotImplemented, "build_runtime_unavailable",
		"页面构建运行时尚未接入，不能伪造 ready 构建。")
}

func (s *Server) apiGetPageProjectBuild(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	buildID, ok := pagePathID(w, r, "buildID")
	if !ok {
		return
	}
	build, err := s.store.GetPageBuild(buildID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if build == nil || build.ProjectID != project.ID {
		apiError(w, http.StatusNotFound, "build_not_found", "页面构建不存在。")
		return
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusOK, map[string]any{"build": build})
}

func (s *Server) apiCreatePageProjectPreviewURL(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePagePreviewRead); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok || !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	if project.Mode == store.PageModeApp {
		s.createPageAppPreviewURL(w, r, project)
		return
	}
	if project.Mode == store.PageModeComposition {
		s.createCompositionPreviewURL(w, r, project)
		return
	}
	apiError(w, http.StatusNotImplemented, "project_preview_runtime_unavailable",
		"自由页面预览运行时尚未接入；标准页面仍可使用 pages/{id}/preview-url。")
}

func pageReadyBuild(builds []*store.PageBuild, revisionID int64) *store.PageBuild {
	for _, build := range builds {
		if build.RevisionID == revisionID && build.Status == store.PageBuildReady {
			return build
		}
	}
	return nil
}

// pagePublicationBuild resolves the exact immutable build that may be
// published. Composition builds include the resolved live-data snapshot in
// their artifact hash, so a build that predates a binding change is stale even
// though its database status is still "ready".
func (s *Server) pagePublicationBuild(
	project *store.PageProject,
	revision *store.PageProjectRevision,
	requestedBuildID int64,
	validation pageValidationResult,
) (*store.PageBuild, string, error) {
	if project == nil || revision == nil {
		return nil, "build_not_ready", nil
	}
	if revision.RevisionKind == store.PageRevisionStandardBaseline {
		return nil, "", nil
	}
	if project.Mode == store.PageModeApp && revision.RevisionKind == store.PageRevisionApp {
		build, err := s.pageAppReadyBuild(project.ID, revision.ID, requestedBuildID)
		if errors.Is(err, store.ErrPageBuildNotReady) {
			return nil, "build_not_ready", nil
		}
		return build, "", err
	}
	if project.Mode != store.PageModeComposition ||
		revision.RevisionKind != store.PageRevisionComposition {
		return nil, "build_not_ready", nil
	}
	builds, err := s.store.ListPageBuilds(project.ID, revision.ID, 100)
	if err != nil {
		return nil, "", err
	}
	sawReady := false
	for _, build := range builds {
		if build == nil || build.RevisionID != revision.ID ||
			build.Status != store.PageBuildReady ||
			build.RuntimeVersion != compositionRuntimeVersion {
			continue
		}
		if requestedBuildID > 0 && build.ID != requestedBuildID {
			continue
		}
		sawReady = true
		if validation.RenderHash != "" && build.ArtifactHash == validation.RenderHash {
			return build, "", nil
		}
	}
	if sawReady {
		return nil, "build_stale", nil
	}
	return nil, "build_not_ready", nil
}

func pagePublicationBuildDiagnostic(code string) pageValidationDiagnostic {
	switch code {
	case "build_stale":
		return pageValidationDiagnostic{
			Level: "error", Code: code,
			Message: "已构建产物对应的数据快照已变化，请重新构建后再发布。",
		}
	default:
		return pageValidationDiagnostic{
			Level: "error", Code: "build_not_ready",
			Message: "目标修订没有与当前内容快照一致的 ready 构建。",
		}
	}
}

func (s *Server) pageRevisionWasPublished(projectID, revisionID int64) (bool, error) {
	publications, err := s.store.ListPagePublications(projectID, 500)
	if err != nil {
		return false, err
	}
	for _, publication := range publications {
		if publication.RevisionID == revisionID &&
			publication.Status == store.PagePublicationPublished {
			return true, nil
		}
	}
	return false, nil
}

// 页面发布/回滚只有会改变真实线上入口时才需要后台密码。
// 单站模式本身就是公开站点；平台模式下，默认站始终在线，非默认站则以
// 已启用的站点域名为准。无法确认状态时保持 fail-closed。
func (s *Server) pagePublicationRequiresNativeApproval() (bool, error) {
	if s.platform == nil || s.platformSiteID <= 0 {
		return true, nil
	}
	site, ok, err := s.platform.GetSite(s.platformSiteID)
	if err != nil {
		return true, err
	}
	if !ok || site == nil {
		return true, fmt.Errorf("站点不存在")
	}
	return s.controlSiteRequiresUnlock(site)
}

func (s *Server) publicationPlan(w http.ResponseWriter, r *http.Request, operation string) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePagesPublish); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok || !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	var in pageRevisionTargetInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if in.RevisionID <= 0 {
		if operation == pageApprovalPublish {
			in.RevisionID = project.WorkingRevisionID
		} else {
			apiError(w, http.StatusBadRequest, "revision_required", "回滚预检必须指定 revision_id。")
			return
		}
	}
	revision, ok := s.revisionForValidation(w, project, in.RevisionID)
	if !ok {
		return
	}
	validation := s.pageRevisionValidation(r.Context(), project, revision)
	if diagnostics := s.pageAppPublicationDiagnostics(project, revision); len(diagnostics) != 0 {
		validation.Diagnostics = append(validation.Diagnostics, diagnostics...)
		validation.Valid = false
	}
	canPublish := validation.Valid
	var build *store.PageBuild
	if revision.RevisionKind != store.PageRevisionStandardBaseline {
		resolvedBuild, buildState, err := s.pagePublicationBuild(
			project, revision, in.BuildID, validation,
		)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		build = resolvedBuild
		if buildState != "" {
			canPublish = false
			validation.Diagnostics = append(
				validation.Diagnostics,
				pagePublicationBuildDiagnostic(buildState),
			)
		}
	}
	if operation == pageApprovalPublish && revision.ID != project.WorkingRevisionID {
		canPublish = false
		validation.Diagnostics = append(validation.Diagnostics, pageValidationDiagnostic{
			Level: "error", Code: "not_working_revision", Message: "发布只能指向当前工作修订。",
		})
	}
	if operation == pageApprovalRollback {
		wasPublished, err := s.pageRevisionWasPublished(project.ID, revision.ID)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if !wasPublished {
			canPublish = false
			validation.Diagnostics = append(validation.Diagnostics, pageValidationDiagnostic{
				Level: "error", Code: "revision_not_published", Message: "只能回滚到历史发布过的修订。",
			})
		}
	}
	buildID := int64(0)
	if build != nil {
		buildID = build.ID
	}
	requiresApproval, err := s.pagePublicationRequiresNativeApproval()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "publication_live_status_failed", "无法确认站点是否已上线，未生成发布预检。")
		return
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusOK, map[string]any{
		"operation": operation, "project_id": project.ID, "page_id": project.PostID,
		"revision_id": revision.ID, "build_id": buildID, "can_execute": canPublish,
		"requires_approval_token": requiresApproval, "approval_token_issued": false,
		"validation": validation,
		"impact": map[string]any{
			"public_revision_from":    project.PublishedRevisionID,
			"public_revision_to":      revision.ID,
			"public_route_may_change": true,
		},
	})
}

func (s *Server) apiPageProjectPublishPlan(w http.ResponseWriter, r *http.Request) {
	s.publicationPlan(w, r, pageApprovalPublish)
}

func (s *Server) apiPageProjectRollbackPlan(w http.ResponseWriter, r *http.Request) {
	s.publicationPlan(w, r, pageApprovalRollback)
}

func (s *Server) apiPublishPageProject(w http.ResponseWriter, r *http.Request) {
	s.publishPageProject(w, r, pageApprovalPublish)
}

func (s *Server) apiRollbackPageProject(w http.ResponseWriter, r *http.Request) {
	s.publishPageProject(w, r, pageApprovalRollback)
}

func (s *Server) publishPageProject(w http.ResponseWriter, r *http.Request, operation string) {
	auth, ok := s.requirePagePlatformScope(w, r, apiScopePagesPublish)
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
	var in pagePublicationInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	action := store.PagePublicationPublish
	if operation == pageApprovalRollback {
		action = store.PagePublicationRollback
	}
	actorID := pageAutomationActor(auth)
	requestHash, err := canonicalPagePublicationRequestHash(
		pagePublicationRequestIdentity{
			SchemaVersion: 1,
			SiteID:        s.platformSiteID,
			ProjectID:     project.ID,
			Operation:     operation,
			ActorID:       actorID,
			Origin:        store.PageOriginAPI,
			ETag:          strings.TrimSpace(r.Header.Get(pagePlatformConcurrencyHeader)),
			RevisionID:    in.RevisionID,
			BuildID:       in.BuildID,
			DataSnapshotHash: strings.TrimSpace(
				in.DataSnapshotHash,
			),
			DeploymentJobID: strings.TrimSpace(in.DeploymentJobID),
		},
	)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "request_hash_failed",
			"无法计算页面发布请求摘要。")
		return
	}

	// Resolve a committed automation receipt before consulting any mutable
	// project, live-binding, route, build-selection, approval, or unlock state.
	// The persisted request hash includes the original If-Match and normalized
	// target body, so only a byte-for-byte semantic retry can take this path.
	replayed, found, err := s.store.ReplayPagePublication(
		project.ID, requestID, requestHash, actorID, store.PageOriginAPI, action,
	)
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if found {
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, map[string]any{
			"publication": replayed, "created": false,
		})
		return
	}
	if !pageRequireETag(w, r, project.ETag(), project.WorkingRevisionID) {
		return
	}
	if in.RevisionID <= 0 {
		if operation == pageApprovalPublish {
			in.RevisionID = project.WorkingRevisionID
		} else {
			apiError(w, http.StatusBadRequest, "revision_required", "回滚必须指定 revision_id。")
			return
		}
	}
	revision, ok := s.revisionForValidation(w, project, in.RevisionID)
	if !ok {
		return
	}
	validation := s.pageRevisionValidation(r.Context(), project, revision)
	if diagnostics := s.pageAppPublicationDiagnostics(project, revision); len(diagnostics) != 0 {
		validation.Diagnostics = append(validation.Diagnostics, diagnostics...)
		validation.Valid = false
	}
	if !validation.Valid {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "manifest_invalid", "message": "目标修订未通过页面工程校验。",
			"validation": validation,
		})
		return
	}
	build, buildState, err := s.pagePublicationBuild(
		project, revision, in.BuildID, validation,
	)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if buildState != "" {
		diagnostic := pagePublicationBuildDiagnostic(buildState)
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": buildState, "message": diagnostic.Message,
			"validation": validation,
		})
		return
	}
	if build != nil {
		in.BuildID = build.ID
	}
	serverSnapshotHash := strings.TrimSpace(validation.DataSnapshotHash)
	if requestedSnapshotHash := strings.TrimSpace(in.DataSnapshotHash); requestedSnapshotHash != "" &&
		requestedSnapshotHash != serverSnapshotHash {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":                       "data_snapshot_conflict",
			"message":                     "data_snapshot_hash 与服务端对目标修订的实时校验结果不一致，请重新预检。",
			"expected_data_snapshot_hash": serverSnapshotHash,
			"provided_data_snapshot_hash": requestedSnapshotHash,
			"validation":                  validation,
		})
		return
	}
	if operation == pageApprovalPublish && revision.ID != project.WorkingRevisionID {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "revision_conflict", "message": "发布只能指向当前工作修订。",
			"expected_revision_id": revision.ID, "current_revision_id": project.WorkingRevisionID,
		})
		return
	}
	if operation == pageApprovalRollback {
		wasPublished, err := s.pageRevisionWasPublished(project.ID, revision.ID)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if !wasPublished {
			apiError(w, http.StatusConflict, "revision_not_published", "只能回滚到历史发布过的修订。")
			return
		}
	}
	approvalTarget := pageApprovalConsumeInput{
		SiteID: s.platformSiteID, PageID: project.PostID, ProjectID: project.ID,
		RevisionID: in.RevisionID, BuildID: in.BuildID,
		Operation: operation, ETag: project.ETag(),
		DataSnapshotHash: serverSnapshotHash,
		RequestID:        requestID,
	}
	requiresApproval, err := s.pagePublicationRequiresNativeApproval()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "publication_live_status_failed", "无法确认站点是否已上线，未执行页面操作。")
		return
	}
	approvalID := ""
	if requiresApproval {
		approvalToken := strings.TrimSpace(in.ApprovalToken)
		nativeState := ""
		if approvalToken == "" {
			approvalToken, nativeState = resolveNativePageApproval(
				s, auth, strings.TrimSpace(r.Header.Get(controlUnlockHeader)), approvalTarget,
			)
		}
		approval, state := consumePageApprovalToken(s, approvalToken, approvalTarget)
		if state != "" {
			challenge, challengeErr := issueNativePageChallenge(s, auth, approvalTarget)
			if challengeErr != nil {
				apiError(w, http.StatusInternalServerError, "confirmation_unavailable", "无法创建页面原生确认挑战。")
				return
			}
			message := "unlock_required：请在 Pilot 原生界面确认本次页面" +
				map[bool]string{true: "回滚", false: "发布"}[operation == pageApprovalRollback] +
				"，后台密码不会进入对话或技能脚本。"
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "publish_confirmation_required", "message": message,
				"unlock_required": true, "operation": operation,
				"unlock_challenge": challenge, "unlock_state": nativeState,
				"site_id": s.platformSiteID, "project_id": project.ID,
				"page_id": project.PostID, "revision_id": in.RevisionID,
				"build_id": in.BuildID, "etag": project.ETag(),
				"data_snapshot_hash": serverSnapshotHash,
				"request_id":         requestID,
				"admin_path":         "/admin/pages/" + strconv.FormatInt(project.PostID, 10) + "/project",
			})
			return
		}
		approvalID = approval.ID
	}
	s.pagePublicationMu.Lock()
	publication, created, err := s.store.PublishPageProject(store.PublishPageProjectInput{
		ProjectID: project.ID, RevisionID: in.RevisionID, BuildID: in.BuildID,
		ExpectedWorkingRevisionID: project.WorkingRevisionID, Action: action,
		ApprovalID: approvalID, ActorID: actorID,
		Origin: store.PageOriginAPI, RequestID: requestID,
		RequestHash:      requestHash,
		DataSnapshotHash: serverSnapshotHash,
		DeliveryStatus:   s.initialPagePublicationDeliveryStatus(),
		DeploymentJobID:  strings.TrimSpace(in.DeploymentJobID),
	})
	s.pagePublicationMu.Unlock()
	if err != nil {
		pageStoreError(w, err)
		return
	}
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		s.recordAutomationLog(auth, action, "page_publication", publication.ID,
			fmt.Sprintf("%s 页面修订 %d", action, in.RevisionID))
		s.invalidatePageProjectPublication()
	}
	writeJSON(w, func() int {
		if created {
			return http.StatusCreated
		}
		return http.StatusOK
	}(), map[string]any{"publication": publication, "created": created})
}

func (s *Server) apiListPageProjectPublications(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	project, ok := s.pageProjectByPath(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListPagePublications(project.ID, apiIntParam(r, "limit", 100))
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "project_id": project.ID})
}

type pageApproval struct {
	ID               string
	SiteID           int64
	PageID           int64
	ProjectID        int64
	RevisionID       int64
	Operation        string
	ActorID          string
	ETag             string
	DataSnapshotHash string
	ExpiresAt        time.Time
	Used             bool
	UsedByRequestID  string
}

type pageApprovalRegistry struct {
	mu     sync.Mutex
	grants map[[32]byte]*pageApproval
}

var pageApprovalRegistries sync.Map // map[*Server]*pageApprovalRegistry

func approvalRegistryFor(s *Server) *pageApprovalRegistry {
	value, _ := pageApprovalRegistries.LoadOrStore(s, &pageApprovalRegistry{
		grants: map[[32]byte]*pageApproval{},
	})
	return value.(*pageApprovalRegistry)
}

type pageApprovalIssueResult struct {
	ApprovalToken    string    `json:"approval_token"`
	ApprovalID       string    `json:"approval_id"`
	SiteID           int64     `json:"site_id,omitempty"`
	PageID           int64     `json:"page_id"`
	ProjectID        int64     `json:"project_id"`
	RevisionID       int64     `json:"revision_id"`
	Operation        string    `json:"operation"`
	ActorID          string    `json:"actor_id"`
	ETag             string    `json:"etag"`
	DataSnapshotHash string    `json:"data_snapshot_hash,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// issuePageApprovalToken is intentionally not an automation API handler.
// Authenticated GCMS admin/Pilot native confirmation UI may call this helper
// after presenting the impact plan. The opaque secret is random, short-lived,
// revision-bound and stored only as a SHA-256 digest.
func (s *Server) issuePageApprovalToken(projectID, revisionID int64, operation, actorID string, ttl time.Duration) (pageApprovalIssueResult, error) {
	if operation != pageApprovalPublish && operation != pageApprovalRollback {
		return pageApprovalIssueResult{}, errors.New("unsupported page approval operation")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return pageApprovalIssueResult{}, errors.New("page approval actor is required")
	}
	if ttl <= 0 || ttl > pagePlatformApprovalTTL {
		ttl = pagePlatformApprovalTTL
	}
	project, err := s.store.GetPageProject(projectID)
	if err != nil {
		return pageApprovalIssueResult{}, err
	}
	if project == nil {
		return pageApprovalIssueResult{}, store.ErrPageProjectNotFound
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil {
		return pageApprovalIssueResult{}, err
	}
	if revision == nil || revision.ProjectID != project.ID {
		return pageApprovalIssueResult{}, store.ErrPageRevisionNotFound
	}
	if operation == pageApprovalPublish && project.WorkingRevisionID != revisionID {
		return pageApprovalIssueResult{}, &store.PageRevisionConflictError{
			ExpectedRevisionID: revisionID, CurrentRevisionID: project.WorkingRevisionID,
		}
	}
	if operation == pageApprovalRollback {
		wasPublished, err := s.pageRevisionWasPublished(project.ID, revision.ID)
		if err != nil {
			return pageApprovalIssueResult{}, err
		}
		if !wasPublished {
			return pageApprovalIssueResult{}, errors.New("rollback target was never published")
		}
	}
	validation := s.pageRevisionValidation(context.Background(), project, revision)
	if !validation.Valid {
		return pageApprovalIssueResult{}, store.ErrPageInvalid
	}
	dataSnapshotHash := strings.TrimSpace(validation.DataSnapshotHash)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return pageApprovalIssueResult{}, err
	}
	idRaw := make([]byte, 18)
	if _, err := rand.Read(idRaw); err != nil {
		return pageApprovalIssueResult{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	id := "approval_" + base64.RawURLEncoding.EncodeToString(idRaw)
	expires := time.Now().Add(ttl)
	digest := sha256.Sum256([]byte(token))
	grant := &pageApproval{
		ID: id, SiteID: s.platformSiteID, PageID: project.PostID,
		ProjectID: project.ID, RevisionID: revision.ID, Operation: operation,
		ActorID: actorID, ETag: project.ETag(),
		DataSnapshotHash: dataSnapshotHash, ExpiresAt: expires,
	}
	registry := approvalRegistryFor(s)
	registry.mu.Lock()
	registry.grants[digest] = grant
	registry.mu.Unlock()
	return pageApprovalIssueResult{
		ApprovalToken: token, ApprovalID: id, SiteID: s.platformSiteID,
		PageID: project.PostID, ProjectID: project.ID, RevisionID: revision.ID,
		Operation: operation, ActorID: grant.ActorID, ETag: grant.ETag,
		DataSnapshotHash: dataSnapshotHash, ExpiresAt: expires,
	}, nil
}

type pageApprovalConsumeInput struct {
	SiteID           int64
	PageID           int64
	ProjectID        int64
	RevisionID       int64
	BuildID          int64
	Operation        string
	ETag             string
	RequestID        string
	DataSnapshotHash string
	Capability       string
	ConfigHash       string
	Decision         string
}

func consumePageApprovalToken(s *Server, token string, input pageApprovalConsumeInput) (*pageApproval, string) {
	if s == nil || token == "" || input.RequestID == "" {
		return nil, "missing"
	}
	digest := sha256.Sum256([]byte(token))
	registry := approvalRegistryFor(s)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	grant := registry.grants[digest]
	if grant == nil {
		return nil, "invalid"
	}
	if time.Now().After(grant.ExpiresAt) {
		delete(registry.grants, digest)
		return nil, "expired"
	}
	if grant.SiteID != input.SiteID || grant.PageID != input.PageID ||
		grant.ProjectID != input.ProjectID || grant.RevisionID != input.RevisionID ||
		grant.Operation != input.Operation || grant.ETag != input.ETag ||
		grant.DataSnapshotHash != input.DataSnapshotHash {
		return nil, "mismatch"
	}
	if grant.Used {
		if grant.UsedByRequestID == input.RequestID {
			copy := *grant
			return &copy, ""
		}
		return nil, "used"
	}
	grant.Used = true
	grant.UsedByRequestID = input.RequestID
	copy := *grant
	return &copy, ""
}
