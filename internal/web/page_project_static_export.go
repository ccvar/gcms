package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"cms.ccvar.com/internal/store"
)

// ErrPageProjectStaticExportUnavailable is deliberately distinct from a
// rendering failure: callers must report that the selected page runtime is not
// yet supported by the Cloudflare exporter rather than publishing the legacy
// standard-page HTML under a composition/app URL.
var ErrPageProjectStaticExportUnavailable = errors.New("page project static export unavailable")

type PageProjectStaticExportUnavailableError struct {
	PostID        int64
	ProjectID     int64
	RevisionID    int64
	Mode          string
	SchemaVersion int
}

func (e *PageProjectStaticExportUnavailableError) Error() string {
	return fmt.Sprintf(
		"%v: page %d project %d published revision %d uses %s schema v%d",
		ErrPageProjectStaticExportUnavailable,
		e.PostID,
		e.ProjectID,
		e.RevisionID,
		e.Mode,
		e.SchemaVersion,
	)
}

func (e *PageProjectStaticExportUnavailableError) Unwrap() error {
	return ErrPageProjectStaticExportUnavailable
}

// checkPageProjectStaticExport validates every published non-standard runtime
// before an export tree is created. Supported runtimes continue; malformed or
// unknown published pointers fail closed and can never fall back to post body.
//
// A project with no published revision is still only a private conversion
// draft, so the existing standard page remains the public source and exports
// normally. Once a project owns the public revision, however, exporting the
// standard fallback would be a false success. The real mode-specific renderer
// must replace this rejection before that mode can be advertised as supported.
func (s *Server) checkPageProjectStaticExport() error {
	projects, err := s.store.ListPageProjects()
	if err != nil {
		return err
	}
	for _, project := range projects {
		if project == nil || project.PublishedRevisionID == 0 {
			continue
		}
		if project.Mode == store.PageModeComposition {
			revision, err := s.store.GetPageProjectRevision(project.PublishedRevisionID)
			if err != nil {
				return err
			}
			if revision == nil || revision.ProjectID != project.ID ||
				revision.RevisionKind != store.PageRevisionComposition {
				return &PageProjectStaticExportUnavailableError{
					PostID: project.PostID, ProjectID: project.ID,
					RevisionID: project.PublishedRevisionID, Mode: project.Mode,
					SchemaVersion: project.SchemaVersion,
				}
			}
			if _, err := s.ValidateCompositionBuild(
				context.Background(), project, revision, CompositionBindingPublishedOnly,
			); err != nil {
				unavailable := &PageProjectStaticExportUnavailableError{
					PostID: project.PostID, ProjectID: project.ID,
					RevisionID: project.PublishedRevisionID, Mode: project.Mode,
					SchemaVersion: project.SchemaVersion,
				}
				return fmt.Errorf("%w: %v", unavailable, err)
			}
			continue
		}
		if project.Mode == store.PageModeApp {
			revision, err := s.store.GetPageProjectRevision(project.PublishedRevisionID)
			if err != nil {
				return err
			}
			if revision == nil || revision.ProjectID != project.ID ||
				revision.RevisionKind != store.PageRevisionApp {
				return &PageProjectStaticExportUnavailableError{
					PostID: project.PostID, ProjectID: project.ID,
					RevisionID: project.PublishedRevisionID, Mode: project.Mode,
					SchemaVersion: project.SchemaVersion,
				}
			}
			if _, err := s.pageAppReadyBuild(project.ID, revision.ID, 0); err != nil {
				unavailable := &PageProjectStaticExportUnavailableError{
					PostID: project.PostID, ProjectID: project.ID,
					RevisionID: project.PublishedRevisionID, Mode: project.Mode,
					SchemaVersion: project.SchemaVersion,
				}
				return fmt.Errorf("%w: %v", unavailable, err)
			}
			continue
		}
		return &PageProjectStaticExportUnavailableError{
			PostID:        project.PostID,
			ProjectID:     project.ID,
			RevisionID:    project.PublishedRevisionID,
			Mode:          project.Mode,
			SchemaVersion: project.SchemaVersion,
		}
	}
	return nil
}

// validatePageAppBridgeStaticExport rejects a static deployment when a
// published app has an approved server-backed capability but the generated
// shell cannot reach a distinct GCMS origin. Without this guard the shell
// would keep a relative /_gcms/page-app-bridge URL on the public asset host,
// whose static Worker deliberately rejects POST requests with 405.
//
// Pure client-side apps remain exportable without OriginURL: the requirement
// applies only after an approved capability actually needs the trusted bridge.
func (s *Server) validatePageAppBridgeStaticExport(cfg CloudflareConfig) error {
	projects, err := s.store.ListPageProjects()
	if err != nil {
		return err
	}
	definitions := pageAppCapabilityDefinitions()
	requiresBridge := false
	for _, project := range projects {
		if project == nil || project.Mode != store.PageModeApp ||
			project.PublishedRevisionID <= 0 {
			continue
		}
		revision, err := s.store.GetPageProjectRevision(project.PublishedRevisionID)
		if err != nil {
			return err
		}
		if revision == nil || revision.ProjectID != project.ID ||
			revision.RevisionKind != store.PageRevisionApp {
			return &PageProjectStaticExportUnavailableError{
				PostID: project.PostID, ProjectID: project.ID,
				RevisionID: project.PublishedRevisionID, Mode: project.Mode,
				SchemaVersion: project.SchemaVersion,
			}
		}
		declared, err := pageAppManifestCapabilities(revision)
		if err != nil {
			return err
		}
		grants, err := s.store.ListPageCapabilityGrants(project.ID)
		if err != nil {
			return err
		}
		for _, grant := range grants {
			if grant == nil {
				continue
			}
			definition, known := definitions[grant.Capability]
			if grant.Status == store.PageCapabilityApproved &&
				declared[grant.Capability] && known && definition.RequiresBridge {
				requiresBridge = true
				break
			}
		}
		if requiresBridge {
			break
		}
	}
	if !requiresBridge {
		return nil
	}

	origin := compositionContactOriginBase(cfg)
	if origin == "" {
		return errors.New("包含已批准 Bridge 能力的静态互动应用必须配置合法的 GCMS OriginURL")
	}
	parsedOrigin, _ := url.Parse(origin)
	if parsedOrigin == nil || parsedOrigin.Scheme != "https" {
		return errors.New("互动应用 Bridge OriginURL 必须使用 HTTPS")
	}
	originHost := baseURLHost(origin)
	for _, domain := range cfg.publicDomains() {
		if sameCloudflareDNSName(originHost, domain.Host) {
			return errors.New("互动应用 Bridge OriginURL 必须与 Cloudflare 公共域名不同")
		}
	}
	if sameCloudflareDNSName(originHost, "pages.dev") ||
		strings.HasSuffix(strings.ToLower(originHost), ".pages.dev") {
		return errors.New("互动应用 Bridge OriginURL 不能指向 Cloudflare Pages 静态源")
	}
	return nil
}

// exportPublishedCompositionAssets freezes the exact immutable asset IDs and
// hashes referenced by published manifests. The HTML itself is produced by
// the same public server renderer in exportPagePages, so dynamic bindings are
// resolved into that deployment's document rather than copied as live API
// calls.
func (s *Server) exportPublishedCompositionAssets(result *staticExportResult) error {
	projects, err := s.store.ListPageProjects()
	if err != nil {
		return err
	}
	for _, project := range projects {
		if project == nil || project.Mode != store.PageModeComposition ||
			project.PublishedRevisionID <= 0 {
			continue
		}
		revision, err := s.store.GetPageProjectRevision(project.PublishedRevisionID)
		if err != nil {
			return err
		}
		if revision == nil || revision.ProjectID != project.ID ||
			revision.RevisionKind != store.PageRevisionComposition {
			return &PageProjectStaticExportUnavailableError{
				PostID: project.PostID, ProjectID: project.ID,
				RevisionID: project.PublishedRevisionID, Mode: project.Mode,
				SchemaVersion: project.SchemaVersion,
			}
		}
		build, err := s.ValidateCompositionBuild(
			context.Background(), project, revision, CompositionBindingPublishedOnly,
		)
		if err != nil {
			return err
		}
		ids := make([]int64, 0, len(build.Assets))
		for id := range build.Assets {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			view := build.Assets[id]
			asset, err := s.store.GetPageAsset(id)
			if err != nil {
				return err
			}
			if asset == nil || asset.ProjectID != project.ID ||
				asset.SHA256 != view.SHA256 {
				return os.ErrNotExist
			}
			raw, err := s.readCompositionAsset(asset)
			if err != nil {
				return err
			}
			if err := s.exportBytes(view.URL, raw, view.MediaType, result); err != nil {
				return err
			}
		}
	}
	return nil
}

// exportPublishedPageAppArtifacts copies only server-validated, immutable
// artifacts. Source bundles and historical/unpublished builds remain private.
func (s *Server) exportPublishedPageAppArtifacts(
	result *staticExportResult,
	cfg CloudflareConfig,
) error {
	projects, err := s.store.ListPageProjects()
	if err != nil {
		return err
	}
	exported := false
	shellHeaders := map[string]map[string]string{}
	for _, project := range projects {
		if project == nil || project.Mode != store.PageModeApp ||
			project.PublishedRevisionID == 0 {
			continue
		}
		revision, err := s.store.GetPageProjectRevision(project.PublishedRevisionID)
		if err != nil {
			return err
		}
		if revision == nil || revision.ProjectID != project.ID ||
			revision.RevisionKind != store.PageRevisionApp {
			return &PageProjectStaticExportUnavailableError{
				PostID: project.PostID, ProjectID: project.ID,
				RevisionID: project.PublishedRevisionID, Mode: project.Mode,
				SchemaVersion: project.SchemaVersion,
			}
		}
		build, err := s.pageAppReadyBuild(project.ID, revision.ID, 0)
		if err != nil {
			return err
		}
		dir, err := securePageAppStorageDir(
			s.store.PageProjectStorageDir(), build.ArtifactRef,
			project.ID, revision.ID, "artifacts", build.ArtifactHash,
		)
		if err != nil {
			return err
		}
		bundle, err := readPageAppBundleDirectory(dir, pagePlatformServerLimits())
		if err != nil {
			return err
		}
		if bundle.Hash != build.ArtifactHash {
			return pageAppInvalid("artifact_hash_mismatch", build.ArtifactRef, "导出前产物哈希校验失败")
		}
		post, err := s.store.GetPostByID(project.PostID)
		if err != nil {
			return err
		}
		if post == nil || post.Type != "page" || post.Status != "published" {
			return &PageProjectStaticExportUnavailableError{
				PostID: project.PostID, ProjectID: project.ID,
				RevisionID: project.PublishedRevisionID, Mode: project.Mode,
				SchemaVersion: project.SchemaVersion,
			}
		}
		bridgePath := fmt.Sprintf("/_gcms/page-app-bridge/%d/%d", project.ID, revision.ID)
		if origin := strings.TrimRight(normalizeCloudflareOrigin(cfg.OriginURL), "/"); origin != "" {
			bridgePath = origin + bridgePath
		}
		shellRoute := "/" + strings.Trim(post.Lang, "/") +
			publicContentPath("page", post.Slug) + "*"
		shellHeaders[shellRoute] = pageAppShellResponseHeaders(bridgePath)
		names := make([]string, 0, len(bundle.Files))
		for name := range bundle.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			output := path.Join(
				"/_gcms/page-apps",
				strconv.FormatInt(project.ID, 10),
				strconv.FormatInt(revision.ID, 10),
				name,
			)
			if err := s.exportBytes(output, bundle.Files[name], pageAppMediaType(name), result); err != nil {
				return err
			}
		}
		exported = true
	}
	if !exported {
		return nil
	}
	var headers strings.Builder
	shellRoutes := make([]string, 0, len(shellHeaders))
	for route := range shellHeaders {
		shellRoutes = append(shellRoutes, route)
	}
	sort.Strings(shellRoutes)
	for _, route := range shellRoutes {
		headers.WriteString(route)
		headers.WriteByte('\n')
		names := make([]string, 0, len(shellHeaders[route]))
		for name := range shellHeaders[route] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			headers.WriteString("  ")
			headers.WriteString(name)
			headers.WriteString(": ")
			headers.WriteString(shellHeaders[route][name])
			headers.WriteByte('\n')
		}
	}
	headers.WriteString("/_gcms/page-apps/*\n")
	headerNames := make([]string, 0, len(pageAppResponseHeaders()))
	for name := range pageAppResponseHeaders() {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	for _, name := range headerNames {
		headers.WriteString("  ")
		headers.WriteString(name)
		headers.WriteString(": ")
		headers.WriteString(pageAppResponseHeaders()[name])
		headers.WriteByte('\n')
	}
	headers.WriteString("  Cache-Control: public, max-age=31536000, immutable\n")
	return s.exportBytes("/_headers", []byte(headers.String()), "text/plain; charset=utf-8", result)
}
