package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

func pagePreviewTestServer(t *testing.T, scopes string) (*Server, string) {
	t.Helper()
	s := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("page-preview-test", token, prefix, scopes); err != nil {
		t.Fatalf("create automation key: %v", err)
	}
	return s, token
}

func createStandardPagePreview(t *testing.T, s *Server, token string, pageID int64) pagePreviewURLResponse {
	t.Helper()
	path := "/api/admin/v1/pages/" + strconv.FormatInt(pageID, 10) + "/preview-url"
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create page preview = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got pagePreviewURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode page preview response: %v", err)
	}
	return got
}

func pagePreviewDraft(t *testing.T, s *Server, slug string) int64 {
	t.Helper()
	id, err := s.store.CreatePost(&store.Post{
		Type:       "page",
		Lang:       "zh",
		Slug:       slug,
		Title:      "页面草稿预览",
		Excerpt:    "只在签名预览中显示的摘要",
		Content:    "预览正文\n\n## 页面章节\n\n未发布内容",
		Status:     "draft",
		EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create page draft: %v", err)
	}
	return id
}

func TestStandardPagePreviewRendersExistingTemplateWithSignedRevision(t *testing.T) {
	s, token := pagePreviewTestServer(t, "pages:read")
	id := pagePreviewDraft(t, s, "signed-page-draft")

	// Creating or opening a preview must not make the draft publicly visible.
	public := httptest.NewRecorder()
	s.Handler().ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/zh/signed-page-draft/", nil))
	if public.Code != http.StatusNotFound {
		t.Fatalf("public draft status = %d, want 404", public.Code)
	}

	got := createStandardPagePreview(t, s, token, id)
	if got.PreviewURL == "" || got.ContentRevision == "" || got.ExpiresAt == "" {
		t.Fatalf("page preview response incomplete: %#v", got)
	}
	if got.TTLSeconds != int64(frontendPreviewTTL.Seconds()) {
		t.Fatalf("ttl_seconds = %d, want %d", got.TTLSeconds, int64(frontendPreviewTTL.Seconds()))
	}
	u, err := url.Parse(got.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	if want := "/preview/pages/" + strconv.FormatInt(id, 10); u.Path != want {
		t.Fatalf("preview path = %q, want %q", u.Path, want)
	}
	claims, state := s.verifyPagePreviewToken(u.Query().Get("token"))
	if state != "" {
		t.Fatalf("verify page preview token = %q", state)
	}
	if claims.Version != standardPagePreviewTokenVersion ||
		claims.Kind != standardPagePreviewTokenKind ||
		claims.PageID != id ||
		claims.ContentRevision != got.ContentRevision ||
		claims.ProjectRevision != "" ||
		claims.BuildID != "" {
		t.Fatalf("unexpected page preview claims: %#v", claims)
	}

	page := httptest.NewRecorder()
	s.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, u.RequestURI(), nil))
	if page.Code != http.StatusOK {
		t.Fatalf("open page preview = %d, body = %s", page.Code, page.Body.String())
	}
	if page.Header().Get("Cache-Control") != "no-store" ||
		page.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("preview security headers = %#v", page.Header())
	}
	body := page.Body.String()
	for _, want := range []string{
		"content-page-signed-page-draft",
		"页面草稿预览",
		"只在签名预览中显示的摘要",
		"<h2 id=\"页面章节\">页面章节</h2>",
		`<meta name="robots" content="noindex, nofollow">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page preview missing %q: %s", want, body)
		}
	}

	again := httptest.NewRecorder()
	s.Handler().ServeHTTP(again, httptest.NewRequest(http.MethodGet, "/zh/signed-page-draft/", nil))
	if again.Code != http.StatusNotFound {
		t.Fatalf("public draft after preview = %d, want 404", again.Code)
	}
}

func TestStandardPagePreviewInvalidatesWhenContentChanges(t *testing.T) {
	s, token := pagePreviewTestServer(t, "pages:read")
	id := pagePreviewDraft(t, s, "page-revision-invalidation")
	first := createStandardPagePreview(t, s, token, id)
	firstURL, err := url.Parse(first.PreviewURL)
	if err != nil {
		t.Fatalf("parse first preview URL: %v", err)
	}

	p, err := s.store.GetPostByID(id)
	if err != nil || p == nil {
		t.Fatalf("get draft page: %v", err)
	}
	p.Title = "页面草稿预览（新修订）"
	p.Content += "\n\n修订后的内容"
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("update draft page: %v", err)
	}

	stale := httptest.NewRecorder()
	s.Handler().ServeHTTP(stale, httptest.NewRequest(http.MethodGet, firstURL.RequestURI(), nil))
	if stale.Code != http.StatusGone {
		t.Fatalf("stale page preview = %d, want 410; body = %s", stale.Code, stale.Body.String())
	}

	second := createStandardPagePreview(t, s, token, id)
	if second.ContentRevision == first.ContentRevision {
		t.Fatalf("content revision did not change: %q", first.ContentRevision)
	}
	secondURL, err := url.Parse(second.PreviewURL)
	if err != nil {
		t.Fatalf("parse second preview URL: %v", err)
	}
	fresh := httptest.NewRecorder()
	s.Handler().ServeHTTP(fresh, httptest.NewRequest(http.MethodGet, secondURL.RequestURI(), nil))
	if fresh.Code != http.StatusOK || !strings.Contains(fresh.Body.String(), "修订后的内容") {
		t.Fatalf("fresh page preview = %d, body = %s", fresh.Code, fresh.Body.String())
	}
}

func TestStandardPagePreviewRejectsExpiredTamperedAndWrongPageTokens(t *testing.T) {
	s, _ := pagePreviewTestServer(t, "pages:read")
	id := pagePreviewDraft(t, s, "page-preview-ticket-validation")
	p, err := s.store.GetPostByID(id)
	if err != nil || p == nil {
		t.Fatalf("get draft page: %v", err)
	}
	claims := pagePreviewClaims{
		Version:         standardPagePreviewTokenVersion,
		Kind:            standardPagePreviewTokenKind,
		PageID:          id,
		ContentRevision: previewRevision(p),
		Expires:         time.Now().Add(-time.Minute).Unix(),
	}
	expiredToken, err := s.signPagePreviewToken(claims)
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}
	path := "/preview/pages/" + strconv.FormatInt(id, 10) + "?token=" + url.QueryEscape(expiredToken)
	expired := httptest.NewRecorder()
	s.Handler().ServeHTTP(expired, httptest.NewRequest(http.MethodGet, path, nil))
	if expired.Code != http.StatusGone {
		t.Fatalf("expired page preview = %d, want 410", expired.Code)
	}

	claims.Expires = time.Now().Add(time.Hour).Unix()
	validToken, err := s.signPagePreviewToken(claims)
	if err != nil {
		t.Fatalf("sign valid token: %v", err)
	}
	replacement := "A"
	if strings.HasSuffix(validToken, replacement) {
		replacement = "B"
	}
	tamperedToken := validToken[:len(validToken)-1] + replacement
	tamperedPath := "/preview/pages/" + strconv.FormatInt(id, 10) + "?token=" + url.QueryEscape(tamperedToken)
	tampered := httptest.NewRecorder()
	s.Handler().ServeHTTP(tampered, httptest.NewRequest(http.MethodGet, tamperedPath, nil))
	if tampered.Code != http.StatusNotFound {
		t.Fatalf("tampered page preview = %d, want 404", tampered.Code)
	}

	wrongPagePath := "/preview/pages/" + strconv.FormatInt(id+1, 10) + "?token=" + url.QueryEscape(validToken)
	wrongPage := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongPage, httptest.NewRequest(http.MethodGet, wrongPagePath, nil))
	if wrongPage.Code != http.StatusNotFound {
		t.Fatalf("wrong-page preview = %d, want 404", wrongPage.Code)
	}
}

func TestStandardPagePreviewRequiresPageReadAndRejectsFutureProjectClaims(t *testing.T) {
	s, token := pagePreviewTestServer(t, "pages:write")
	id := pagePreviewDraft(t, s, "page-preview-scope")
	path := "/api/admin/v1/pages/" + strconv.FormatInt(id, 10) + "/preview-url"
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	denied := httptest.NewRecorder()
	s.Handler().ServeHTTP(denied, req)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("page preview without pages:read = %d, want 403", denied.Code)
	}

	p, err := s.store.GetPostByID(id)
	if err != nil || p == nil {
		t.Fatalf("get draft page: %v", err)
	}
	_, err = s.signPagePreviewToken(pagePreviewClaims{
		Version:         standardPagePreviewTokenVersion,
		Kind:            standardPagePreviewTokenKind,
		PageID:          id,
		ContentRevision: previewRevision(p),
		Expires:         time.Now().Add(time.Hour).Unix(),
		ProjectRevision: "future-project-revision",
		BuildID:         "future-build",
	})
	if err == nil {
		t.Fatalf("standard renderer accepted future project/build claims")
	}
}

func TestStandardPagePreviewTicketIsBoundToSite(t *testing.T) {
	s, _ := pagePreviewTestServer(t, "pages:read")
	s.platformSiteID = 41
	id := pagePreviewDraft(t, s, "page-preview-site-binding")
	p, err := s.store.GetPostByID(id)
	if err != nil || p == nil {
		t.Fatalf("get draft page: %v", err)
	}
	token, err := s.signPagePreviewToken(pagePreviewClaims{
		Version:         standardPagePreviewTokenVersion,
		Kind:            standardPagePreviewTokenKind,
		SiteID:          41,
		PageID:          id,
		ContentRevision: previewRevision(p),
		Expires:         time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("sign site-bound token: %v", err)
	}
	if _, state := s.verifyPagePreviewToken(token); state != "" {
		t.Fatalf("verify token for original site = %q", state)
	}
	if !s.signedPreviewMediaTokenValid(token) {
		t.Fatalf("site-bound page token should authorize its signed preview media")
	}
	s.platformSiteID = 42
	if _, state := s.verifyPagePreviewToken(token); state != "invalid" {
		t.Fatalf("verify token for another site = %q, want invalid", state)
	}
	if s.signedPreviewMediaTokenValid(token) {
		t.Fatalf("page token from another site authorized preview media")
	}
}

func TestStandardPagePreviewPlatformFallbackKeepsMediaInsideSignedSite(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_pagepreview1234567890"
	if _, err := ps.CreatePlatformKey(
		"page-preview",
		token,
		token[:13],
		platform.KeyMembershipAll,
		"pages:read",
		nil,
		time.Time{},
	); err != nil {
		t.Fatalf("create platform key: %v", err)
	}
	rt, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || rt == nil || rt.Store == nil || rt.server == nil {
		t.Fatalf("blog runtime missing")
	}

	const imageName = "private-page-preview.webp"
	if err := os.WriteFile(filepath.Join(rt.UploadDir, imageName), []byte("private-page-image"), 0o644); err != nil {
		t.Fatalf("write private page image: %v", err)
	}
	pageID, err := rt.Store.CreatePost(&store.Post{
		Type:       "page",
		Lang:       "zh",
		Slug:       "platform-page-preview",
		Title:      "平台页面草稿",
		Content:    "平台私有正文\n\n![私有图片](/uploads/" + imageName + ")",
		Status:     "draft",
		EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create platform page draft: %v", err)
	}
	rt.server.writeCloudflareStatus(CloudflareStatus{
		Status:        "success",
		LastDeployAt:  time.Now().UTC().Format(time.RFC3339),
		PrimaryDomain: "blog.test",
	})

	endpoint := "/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10) +
		"/pages/" + strconv.FormatInt(pageID, 10) + "/preview-url"
	rec := platformAPIReq(t, h, http.MethodPost, endpoint, token, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create platform page preview = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got pagePreviewURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode platform page preview response: %v", err)
	}
	wantPrefix := "https://platform.test/preview/sites/" + strconv.FormatInt(blogSite.ID, 10) +
		"/pages/" + strconv.FormatInt(pageID, 10) + "?token="
	if !strings.HasPrefix(got.PreviewURL, wantPrefix) {
		t.Fatalf("platform page preview URL = %q, want prefix %q", got.PreviewURL, wantPrefix)
	}

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, got.PreviewURL, nil))
	if page.Code != http.StatusOK {
		t.Fatalf("open platform page preview = %d, body = %s", page.Code, page.Body.String())
	}
	u, err := url.Parse(got.PreviewURL)
	if err != nil {
		t.Fatalf("parse platform page preview URL: %v", err)
	}
	mediaPath := "/preview/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/media/" +
		url.PathEscape(u.Query().Get("token")) + "/uploads/" + imageName
	if !strings.Contains(page.Body.String(), mediaPath) {
		t.Fatalf("page preview did not scope uploaded media URL: want %q", mediaPath)
	}
	media := httptest.NewRecorder()
	h.ServeHTTP(media, httptest.NewRequest(http.MethodGet, "https://platform.test"+mediaPath, nil))
	if media.Code != http.StatusOK || media.Body.String() != "private-page-image" {
		t.Fatalf("open signed page preview media = %d %q", media.Code, media.Body.String())
	}
}
