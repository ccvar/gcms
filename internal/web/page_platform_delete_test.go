package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

const adminAdvancedPageDeleteRouteSuffix = "/project/delete"

func adminAdvancedPageDeletePath(pageID int64) string {
	return "/admin/pages/" + strconv.FormatInt(pageID, 10) +
		adminAdvancedPageDeleteRouteSuffix
}

func TestAdminAdvancedPageDeleteRequiresCSRFAndCurrentETag(t *testing.T) {
	s := newTestPublicServer(t, "")
	page, project := createAdminCompositionPage(
		t, s, "待删除高级页面", "advanced-delete-preconditions",
	)
	handler := s.Handler()

	_, token := authedAdminRequest(t, s, http.MethodGet, "/admin/pages", nil)
	missingCSRF := httptest.NewRequest(
		http.MethodPost,
		adminAdvancedPageDeletePath(page.ID),
		strings.NewReader(url.Values{
			"_etag": {project.ETag()},
			"lang":  {page.Lang},
		}.Encode()),
	)
	missingCSRF.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingCSRF.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	missingCSRFResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingCSRFResponse, missingCSRF)
	if missingCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf(
			"missing CSRF status=%d, want %d; body=%s",
			missingCSRFResponse.Code,
			http.StatusForbidden,
			missingCSRFResponse.Body.String(),
		)
	}

	staleRequest, _ := authedAdminRequest(
		t,
		s,
		http.MethodPost,
		adminAdvancedPageDeletePath(page.ID),
		url.Values{
			"_etag": {store.PageRevisionETag(project.WorkingRevisionID + 1000)},
			"lang":  {page.Lang},
		},
	)
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf(
			"stale ETag status=%d, want %d; body=%s",
			staleResponse.Code,
			http.StatusConflict,
			staleResponse.Body.String(),
		)
	}
	if after, err := s.store.GetPostByID(page.ID); err != nil || after == nil {
		t.Fatalf("precondition failure deleted page: page=%#v err=%v", after, err)
	}
	if after, err := s.store.GetPageProject(project.ID); err != nil || after == nil {
		t.Fatalf("precondition failure deleted project: project=%#v err=%v", after, err)
	}
}

func TestAdminAdvancedPageDeleteRemovesDraftGraphAndPrivateFiles(t *testing.T) {
	s := newTestPublicServer(t, "")
	configureManualCloudflareForInvalidationTest(t, s)
	s.content["published-content"] = contentCacheEntry{}
	s.endpoints["published-sitemap"] = endpointCacheEntry{}
	s.pages["published-page"] = pageCacheEntry{}
	page, project := createAdminCompositionPage(
		t, s, "永久删除高级草稿", "advanced-delete-cascade",
	)
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || revision == nil {
		t.Fatalf("working revision=%#v err=%v", revision, err)
	}
	build, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID:       project.ID,
		RevisionID:      revision.ID,
		Status:          store.PageBuildQueued,
		DiagnosticsJSON: "[]",
		RuntimeVersion:  "composition-ssr/test",
	})
	if err != nil {
		t.Fatalf("create build: %v", err)
	}
	assetHash := strings.Repeat("a", 64)
	asset, _, err := s.store.CreatePageAsset(store.CreatePageAssetInput{
		ProjectID:      project.ID,
		LogicalKey:     "hero-image",
		StorageRef:     "page-projects/" + strconv.FormatInt(project.ID, 10) + "/assets/" + assetHash,
		MediaType:      "image/png",
		ByteSize:       4,
		SHA256:         assetHash,
		Origin:         "upload",
		ProvenanceJSON: "{}",
		Width:          1,
		Height:         1,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	grant, err := s.store.UpsertPageCapabilityGrant(store.UpsertPageCapabilityGrantInput{
		ProjectID:   project.ID,
		Capability:  "client.storage",
		ConfigJSON:  "{}",
		Status:      store.PageCapabilityRequested,
		RequestedBy: "admin",
	})
	if err != nil {
		t.Fatalf("create capability grant: %v", err)
	}
	if reservation, err := s.store.GetPageRouteReservation(project.ID); err != nil || reservation == nil {
		t.Fatalf("route reservation=%#v err=%v", reservation, err)
	}

	root := s.store.PageProjectStorageDir()
	projectID := strconv.FormatInt(project.ID, 10)
	privateRoots := []string{
		filepath.Join(root, projectID),
		filepath.Join(root, "sources", projectID),
		filepath.Join(root, "artifacts", projectID),
	}
	for _, dir := range privateRoots {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create private directory %q: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "delete-me"), []byte("private"), 0o600); err != nil {
			t.Fatalf("write private marker %q: %v", dir, err)
		}
	}

	request, _ := authedAdminRequest(
		t,
		s,
		http.MethodPost,
		adminAdvancedPageDeletePath(page.ID),
		url.Values{
			"_etag": {project.ETag()},
			"lang":  {page.Lang},
		},
	)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf(
			"delete status=%d, want %d; body=%s",
			response.Code,
			http.StatusSeeOther,
			response.Body.String(),
		)
	}
	if got, want := response.Header().Get("Location"), "/admin/pages?lang=zh"; got != want {
		t.Fatalf("delete redirect=%q, want %q", got, want)
	}
	if deleted, err := s.store.GetPostByID(page.ID); err != nil || deleted != nil {
		t.Fatalf("page not deleted: page=%#v err=%v", deleted, err)
	}
	if deleted, err := s.store.GetPageProject(project.ID); err != nil || deleted != nil {
		t.Fatalf("project did not cascade: project=%#v err=%v", deleted, err)
	}
	if deleted, err := s.store.GetPageProjectRevision(revision.ID); err != nil || deleted != nil {
		t.Fatalf("revision did not cascade: revision=%#v err=%v", deleted, err)
	}
	if deleted, err := s.store.GetPageBuild(build.ID); err != nil || deleted != nil {
		t.Fatalf("build did not cascade: build=%#v err=%v", deleted, err)
	}
	if deleted, err := s.store.GetPageAsset(asset.ID); err != nil || deleted != nil {
		t.Fatalf("asset did not cascade: asset=%#v err=%v", deleted, err)
	}
	if deleted, err := s.store.GetPageCapabilityGrant(project.ID, grant.Capability); err != nil || deleted != nil {
		t.Fatalf("capability did not cascade: grant=%#v err=%v", deleted, err)
	}
	if deleted, err := s.store.GetPageRouteReservation(project.ID); err != nil || deleted != nil {
		t.Fatalf("route reservation did not cascade: reservation=%#v err=%v", deleted, err)
	}
	s.cacheMu.RLock()
	contentCacheEntries := len(s.content)
	endpointCacheEntries := len(s.endpoints)
	pageCacheEntries := len(s.pages)
	s.cacheMu.RUnlock()
	if contentCacheEntries != 1 || endpointCacheEntries != 1 || pageCacheEntries != 1 {
		t.Fatalf(
			"draft deletion changed public caches: content=%d endpoints=%d pages=%d",
			contentCacheEntries,
			endpointCacheEntries,
			pageCacheEntries,
		)
	}
	if pending := s.store.Setting(cloudflareSyncPendingKey); pending != "" {
		t.Fatalf("draft deletion marked Cloudflare deployment pending: %q", pending)
	}
	for _, dir := range privateRoots {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("private project directory was not removed: path=%q err=%v", dir, err)
		}
	}
}

func TestAdminAdvancedPageDeleteRejectsPublishedPage(t *testing.T) {
	s := newTestPublicServer(t, "")
	// Keep the publish hook local: this test verifies deletion state, not
	// third-party IndexNow delivery.
	s.baseURL = "http://127.0.0.1:8080"
	page, project := createAdminCompositionPage(
		t, s, "已发布高级页面", "advanced-delete-published",
	)
	publishRequest, _ := authedAdminRequest(
		t,
		s,
		http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/publish",
		url.Values{"_etag": {project.ETag()}},
	)
	publishResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(publishResponse, publishRequest)
	if publishResponse.Code != http.StatusSeeOther {
		t.Fatalf(
			"publish status=%d, want %d; body=%s",
			publishResponse.Code,
			http.StatusSeeOther,
			publishResponse.Body.String(),
		)
	}
	project, err := s.store.GetPageProject(project.ID)
	if err != nil || project == nil || project.PublishedRevisionID == 0 {
		t.Fatalf("published project=%#v err=%v", project, err)
	}

	deleteRequest, _ := authedAdminRequest(
		t,
		s,
		http.MethodPost,
		adminAdvancedPageDeletePath(page.ID),
		url.Values{
			"_etag": {project.ETag()},
			"lang":  {page.Lang},
		},
	)
	deleteResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusConflict {
		t.Fatalf(
			"published delete status=%d, want %d; body=%s",
			deleteResponse.Code,
			http.StatusConflict,
			deleteResponse.Body.String(),
		)
	}
	if after, err := s.store.GetPostByID(page.ID); err != nil || after == nil ||
		after.Status != "published" {
		t.Fatalf("published delete changed page: page=%#v err=%v", after, err)
	}
	if after, err := s.store.GetPageProject(project.ID); err != nil || after == nil ||
		after.PublishedRevisionID == 0 {
		t.Fatalf("published delete changed project: project=%#v err=%v", after, err)
	}
}

func TestAdminAdvancedPageDeleteRejectsNonDraftStatus(t *testing.T) {
	s := newTestPublicServer(t, "")
	page, project := createAdminCompositionPage(
		t, s, "定时高级页面", "advanced-delete-scheduled",
	)
	page.Status = "scheduled"
	if err := s.store.UpdatePost(page); err != nil {
		t.Fatalf("mark page scheduled: %v", err)
	}

	request, _ := authedAdminRequest(
		t,
		s,
		http.MethodPost,
		adminAdvancedPageDeletePath(page.ID),
		url.Values{
			"_etag": {project.ETag()},
			"lang":  {page.Lang},
		},
	)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf(
			"scheduled delete status=%d, want %d; body=%s",
			response.Code,
			http.StatusConflict,
			response.Body.String(),
		)
	}
	if after, err := s.store.GetPostByID(page.ID); err != nil || after == nil ||
		after.Status != "scheduled" {
		t.Fatalf("scheduled delete changed page: page=%#v err=%v", after, err)
	}
	if after, err := s.store.GetPageProject(project.ID); err != nil || after == nil {
		t.Fatalf("scheduled delete changed project: project=%#v err=%v", after, err)
	}
}
