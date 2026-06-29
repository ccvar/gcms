package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

func authedAdminRequest(t *testing.T, s *Server, method, target string, form url.Values) (*http.Request, string) {
	t.Helper()
	token, err := s.sess.create("admin")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	dbSess, ok, err := s.store.GetAdminSession(token)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if form == nil {
		form = url.Values{}
	}
	form.Set("_csrf", dbSess.CSRF)
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	return req, token
}

func TestAdminSettingsSaveUsesRedirectAfterPost(t *testing.T) {
	s := newTestPublicServer(t, "")
	form := url.Values{
		"theme":        {"editorial"},
		"theme_accent": {"#9a3b2f"},
		"theme_radius": {"10"},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/settings/appearance", form)
	w := httptest.NewRecorder()

	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if got, want := w.Header().Get("Location"), "/admin/settings/appearance"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminCanCreatePageFromPagesScreen(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	listReq, token := authedAdminRequest(t, s, http.MethodGet, "/admin/pages?lang=zh", nil)
	list := httptest.NewRecorder()
	h.ServeHTTP(list, listReq)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `href="/admin/pages/new?lang=zh"`) {
		t.Fatalf("pages list should render new page entry")
	}

	newReq := httptest.NewRequest(http.MethodGet, "/admin/pages/new?lang=zh", nil)
	newReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	newPage := httptest.NewRecorder()
	h.ServeHTTP(newPage, newReq)
	if newPage.Code != http.StatusOK {
		t.Fatalf("new page status = %d, body = %s", newPage.Code, newPage.Body.String())
	}
	if !strings.Contains(newPage.Body.String(), "新建页面") || strings.Contains(newPage.Body.String(), `class="btn act-view" href="/zh/"`) {
		t.Fatalf("new page form should render page create state without premature view link")
	}

	form := url.Values{
		"title":       {"团队介绍"},
		"slug":        {"team"},
		"content":     {"这里介绍团队。"},
		"excerpt":     {"团队与服务介绍。"},
		"meta_desc":   {"团队介绍页面"},
		"keywords":    {"团队,介绍"},
		"author":      {"gcms 团队"},
		"editor_mode": {"markdown"},
	}
	createReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages?lang=zh", form)
	created := httptest.NewRecorder()
	h.ServeHTTP(created, createReq)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	location := created.Header().Get("Location")
	if !strings.HasPrefix(location, "/admin/pages/") || !strings.HasSuffix(location, "/edit?saved=1") {
		t.Fatalf("create Location = %q", location)
	}
	page, err := s.store.GetPage("zh", "team")
	if err != nil {
		t.Fatalf("get created page: %v", err)
	}
	if page == nil {
		t.Fatalf("created page missing")
	}
	if page.Type != "page" || page.Status != "published" || page.Title != "团队介绍" || page.Content != "这里介绍团队。" {
		t.Fatalf("created page = %#v", page)
	}
}

func TestAdminAutomationSecretSurvivesSettingsRedirectOnce(t *testing.T) {
	s := newTestPublicServer(t, "")
	form := url.Values{"name": {"content helper"}}
	req, token := authedAdminRequest(t, s, http.MethodPost, "/admin/settings/automation/keys", form)
	w := httptest.NewRecorder()

	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	location := w.Header().Get("Location")
	if location != "/admin/settings/automation" {
		t.Fatalf("Location = %q, want /admin/settings/automation", location)
	}

	get := httptest.NewRequest(http.MethodGet, location, nil)
	get.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	page := httptest.NewRecorder()
	s.Handler().ServeHTTP(page, get)
	if page.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), `id="new-api-secret"`) {
		t.Fatalf("redirected page should show the one-time API secret")
	}

	again := httptest.NewRequest(http.MethodGet, location, nil)
	again.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	second := httptest.NewRecorder()
	s.Handler().ServeHTTP(second, again)
	if strings.Contains(second.Body.String(), `id="new-api-secret"`) {
		t.Fatalf("one-time API secret should be consumed after first GET")
	}
}

func TestCloudflareManualSyncDisablesAutomaticDeploy(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(cloudflareAPITokenKey, "token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	if err := s.store.SetSetting(cloudflareDeployModeKey, cloudflareModeWorkerAssets); err != nil {
		t.Fatalf("set deploy mode: %v", err)
	}
	if err := s.store.SetSetting(cloudflareWorkerNameKey, "gcms-test"); err != nil {
		t.Fatalf("set worker: %v", err)
	}
	if err := s.store.SetSetting(cloudflareDomainsKey, encodeCloudflareDomains([]CloudflareDomain{{Host: "www.example.com", Primary: true}})); err != nil {
		t.Fatalf("set domains: %v", err)
	}

	form := url.Values{
		"sync_mode": {"manual"},
		"sync_time": {"03:00"},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/settings/cloudflare/sync", form)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := s.store.Setting(cloudflareSyncModeKey); got != cloudflareSyncModeManual {
		t.Fatalf("sync mode = %q, want manual", got)
	}
	if got := s.store.Setting(cloudflareAutoSyncKey); got != "0" {
		t.Fatalf("auto sync = %q, want 0", got)
	}

	s.clearGeneratedCaches()
	if got := s.store.Setting(cloudflareSyncPendingKey); got != "1" {
		t.Fatalf("sync pending = %q, want 1", got)
	}
	if got := s.store.Setting(cloudflareSyncNextAtKey); got != "" {
		t.Fatalf("sync next at = %q, want empty for manual sync", got)
	}
}

func TestAdminListStatusQuickChange(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()
	postID, err := s.store.CreatePost(&store.Post{
		Type:   "post",
		Lang:   "zh",
		Slug:   "quick-status-post",
		Title:  "Quick Status Post",
		Status: "draft",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	form := url.Values{
		"target_status": {"published"},
		"lang":          {"zh"},
		"status":        {"draft"},
		"cat":           {"docs"},
		"page":          {"2"},
	}
	publishReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/posts/"+strconv.FormatInt(postID, 10)+"/status", form)
	publish := httptest.NewRecorder()
	h.ServeHTTP(publish, publishReq)
	if publish.Code != http.StatusSeeOther {
		t.Fatalf("publish status = %d, body = %s", publish.Code, publish.Body.String())
	}
	if got, want := publish.Header().Get("Location"), "/admin/posts?lang=zh&status=draft&cat=docs&page=2"; got != want {
		t.Fatalf("publish Location = %q, want %q", got, want)
	}
	updatedPost, err := s.store.GetPostByID(postID)
	if err != nil {
		t.Fatalf("get post after publish: %v", err)
	}
	if updatedPost.Status != "published" || updatedPost.PublishedAt.IsZero() {
		t.Fatalf("post after publish = status %q published_at %v", updatedPost.Status, updatedPost.PublishedAt)
	}

	form.Set("target_status", "draft")
	form.Set("status", "published")
	draftReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/posts/"+strconv.FormatInt(postID, 10)+"/status", form)
	draft := httptest.NewRecorder()
	h.ServeHTTP(draft, draftReq)
	if draft.Code != http.StatusSeeOther {
		t.Fatalf("draft status = %d, body = %s", draft.Code, draft.Body.String())
	}
	if got, want := draft.Header().Get("Location"), "/admin/posts?lang=zh&status=published&cat=docs&page=2"; got != want {
		t.Fatalf("draft Location = %q, want %q", got, want)
	}
	updatedPost, err = s.store.GetPostByID(postID)
	if err != nil {
		t.Fatalf("get post after draft: %v", err)
	}
	if updatedPost.Status != "draft" {
		t.Fatalf("post status after draft = %q, want draft", updatedPost.Status)
	}

	future := time.Now().Add(2 * time.Hour)
	linkID, err := s.store.CreatePost(&store.Post{
		Type:        "link",
		Lang:        "zh",
		Slug:        "quick-status-link",
		Title:       "Quick Status Link",
		Status:      "draft",
		LinkURL:     "https://example.com",
		PublishedAt: future,
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	linkForm := url.Values{
		"target_status": {"scheduled"},
		"lang":          {"zh"},
		"status":        {"draft"},
		"page":          {"2"},
	}
	linkReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/links/"+strconv.FormatInt(linkID, 10)+"/status", linkForm)
	linkResp := httptest.NewRecorder()
	h.ServeHTTP(linkResp, linkReq)
	if linkResp.Code != http.StatusSeeOther {
		t.Fatalf("link status = %d, body = %s", linkResp.Code, linkResp.Body.String())
	}
	if got, want := linkResp.Header().Get("Location"), "/admin/links?lang=zh&status=draft&page=2"; got != want {
		t.Fatalf("link Location = %q, want %q", got, want)
	}
	updatedLink, err := s.store.GetPostByID(linkID)
	if err != nil {
		t.Fatalf("get link after schedule: %v", err)
	}
	if updatedLink.Status != "scheduled" || !updatedLink.PublishedAt.After(time.Now()) {
		t.Fatalf("link after schedule = status %q published_at %v", updatedLink.Status, updatedLink.PublishedAt)
	}
}

func TestAdminDeleteEnglishContentKeepsListContext(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	postID, err := s.store.CreatePost(&store.Post{
		Type:   "post",
		Lang:   "en",
		Slug:   "english-delete-post",
		Title:  "English Delete Post",
		Status: "draft",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	linkID, err := s.store.CreatePost(&store.Post{
		Type:    "link",
		Lang:    "en",
		Slug:    "english-delete-link",
		Title:   "English Delete Link With A Longer Title",
		Status:  "draft",
		LinkURL: "https://example.com/very/long/path/that/should/not/break/the/admin/actions",
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	postForm := url.Values{
		"lang":   {"en"},
		"status": {"draft"},
		"cat":    {"guides"},
		"page":   {"2"},
	}
	postReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/posts/"+strconv.FormatInt(postID, 10)+"/delete", postForm)
	postResp := httptest.NewRecorder()
	h.ServeHTTP(postResp, postReq)
	if postResp.Code != http.StatusSeeOther {
		t.Fatalf("post delete status = %d, body = %s", postResp.Code, postResp.Body.String())
	}
	if got, want := postResp.Header().Get("Location"), "/admin/posts?lang=en&status=draft&cat=guides&page=2"; got != want {
		t.Fatalf("post delete Location = %q, want %q", got, want)
	}
	if deletedPost, err := s.store.GetPostByID(postID); err != nil || deletedPost != nil {
		t.Fatalf("deleted English post should not be readable")
	}

	linkForm := url.Values{
		"lang":   {"en"},
		"status": {"draft"},
		"cat":    {"resources"},
		"page":   {"3"},
	}
	linkReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/links/"+strconv.FormatInt(linkID, 10)+"/delete", linkForm)
	linkResp := httptest.NewRecorder()
	h.ServeHTTP(linkResp, linkReq)
	if linkResp.Code != http.StatusSeeOther {
		t.Fatalf("link delete status = %d, body = %s", linkResp.Code, linkResp.Body.String())
	}
	if got, want := linkResp.Header().Get("Location"), "/admin/links?lang=en&status=draft&cat=resources&page=3"; got != want {
		t.Fatalf("link delete Location = %q, want %q", got, want)
	}
	if deletedLink, err := s.store.GetPostByID(linkID); err != nil || deletedLink != nil {
		t.Fatalf("deleted English link should not be readable")
	}
}

func TestAdminPageDeleteRemovesOnlyPages(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	pageID, err := s.store.CreatePost(&store.Post{
		Type:   "page",
		Lang:   "en",
		Slug:   "delete-page",
		Title:  "Delete Page",
		Status: "published",
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}

	listReq, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/pages?lang=en", nil)
	list := httptest.NewRecorder()
	h.ServeHTTP(list, listReq)
	if list.Code != http.StatusOK {
		t.Fatalf("pages list status = %d, body = %s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	for _, want := range []string{
		`action="/admin/pages/` + strconv.FormatInt(pageID, 10) + `/delete"`,
		`确定删除页面`,
		`name="lang" value="en"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("pages list missing %q", want)
		}
	}

	form := url.Values{"lang": {"en"}}
	deleteReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages/"+strconv.FormatInt(pageID, 10)+"/delete", form)
	deleted := httptest.NewRecorder()
	h.ServeHTTP(deleted, deleteReq)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("page delete status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if got, want := deleted.Header().Get("Location"), "/admin/pages?lang=en"; got != want {
		t.Fatalf("page delete Location = %q, want %q", got, want)
	}
	if deletedPage, err := s.store.GetPostByID(pageID); err != nil || deletedPage != nil {
		t.Fatalf("deleted page should not be readable")
	}

	postID, err := s.store.CreatePost(&store.Post{
		Type:   "post",
		Lang:   "en",
		Slug:   "not-a-page",
		Title:  "Not A Page",
		Status: "draft",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	rejectReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages/"+strconv.FormatInt(postID, 10)+"/delete", url.Values{"lang": {"en"}})
	rejected := httptest.NewRecorder()
	h.ServeHTTP(rejected, rejectReq)
	if rejected.Code != http.StatusNotFound {
		t.Fatalf("non-page delete status = %d, want %d; body = %s", rejected.Code, http.StatusNotFound, rejected.Body.String())
	}
	existingPost, err := s.store.GetPostByID(postID)
	if err != nil {
		t.Fatalf("get non-page after rejected delete: %v", err)
	}
	if existingPost == nil || existingPost.Type != "post" {
		t.Fatalf("non-page delete should not remove post, got %#v", existingPost)
	}
}

func TestAdminLinksCategoryFilter(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	toolsID, err := s.store.CreateCategory(&store.Category{
		Kind: "link",
		Lang: "zh",
		Slug: "test-tools",
		Name: "测试工具",
	})
	if err != nil {
		t.Fatalf("create tools category: %v", err)
	}
	designID, err := s.store.CreateCategory(&store.Category{
		Kind: "link",
		Lang: "zh",
		Slug: "test-design",
		Name: "设计资源",
	})
	if err != nil {
		t.Fatalf("create design category: %v", err)
	}

	if _, err := s.store.CreatePost(&store.Post{
		Type:       "link",
		Lang:       "zh",
		Slug:       "test-alpha-link",
		Title:      "Alpha Filtered Link",
		Status:     "published",
		LinkURL:    "https://alpha.example",
		CategoryID: sql.NullInt64{Int64: toolsID, Valid: true},
	}); err != nil {
		t.Fatalf("create alpha link: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type:       "link",
		Lang:       "zh",
		Slug:       "test-beta-link",
		Title:      "Beta Filtered Link",
		Status:     "published",
		LinkURL:    "https://beta.example",
		CategoryID: sql.NullInt64{Int64: designID, Valid: true},
	}); err != nil {
		t.Fatalf("create beta link: %v", err)
	}

	req, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/links?lang=zh&cat=test-tools", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`list-category-filter`,
		`Alpha Filtered Link`,
		`测试工具`,
		`/admin/links?lang=zh&status=published&cat=test-tools`,
		`name="cat" value="test-tools"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("links category page missing %q in body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Beta Filtered Link") {
		t.Fatalf("category filter should hide links from other categories")
	}
}
