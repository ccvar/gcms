package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

func createLegacyGuardAdvancedPage(t *testing.T, s *Server) (*store.Post, *store.PageProject, int64) {
	t.Helper()
	pageID, err := s.store.CreatePost(&store.Post{
		Type:       "page",
		Lang:       "zh",
		Slug:       "advanced-legacy-guard",
		Title:      "高级页面原始标题",
		Content:    "高级页面原始正文",
		Status:     "draft",
		EditorMode: "markdown",
		TransGroup: "advanced-legacy-guard-group",
	})
	if err != nil {
		t.Fatalf("create advanced page: %v", err)
	}
	page, err := s.store.GetPostByID(pageID)
	if err != nil || page == nil {
		t.Fatalf("get advanced page: page=%#v err=%v", page, err)
	}

	// Produce one legacy revision before the page opts into the advanced
	// project. That historical snapshot must remain readable but must not be
	// restorable through the legacy endpoint afterwards.
	page.Title = "高级页面当前标题"
	if err := s.store.UpdatePost(page); err != nil {
		t.Fatalf("seed legacy revision: %v", err)
	}
	revisions, err := s.store.PostRevisions(pageID)
	if err != nil || len(revisions) == 0 {
		t.Fatalf("list seeded revisions: len=%d err=%v", len(revisions), err)
	}
	if marked, err := s.store.SetDiscard(pageID, "等待人工确认"); err != nil || !marked {
		t.Fatalf("seed discard marker: marked=%v err=%v", marked, err)
	}

	project, err := s.store.CreatePageProject(store.CreatePageProjectInput{
		PostID:        pageID,
		Mode:          store.PageModeComposition,
		SchemaVersion: 1,
		ShellMode:     store.PageShellSite,
		CreatedBy:     store.PageOriginAdmin,
	})
	if err != nil {
		t.Fatalf("create page project: %v", err)
	}
	fresh, err := s.store.GetPostByID(pageID)
	if err != nil || fresh == nil {
		t.Fatalf("get advanced page after project creation: page=%#v err=%v", fresh, err)
	}
	return fresh, project, revisions[0].ID
}

func legacyPageAPIRequest(
	t *testing.T,
	s *Server,
	token, method, target string,
	body string,
	pageID, revisionID int64,
	handler http.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.SetPathValue("collection", "pages")
	request.SetPathValue("id", strconv.FormatInt(pageID, 10))
	if revisionID > 0 {
		request.SetPathValue("rid", strconv.FormatInt(revisionID, 10))
	}
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func assertAdvancedLegacyAPIConflict(
	t *testing.T,
	response *httptest.ResponseRecorder,
	pageID int64,
	project *store.PageProject,
) {
	t.Helper()
	if response.Code != http.StatusConflict {
		t.Fatalf("legacy mutation status = %d, want %d; body=%s",
			response.Code, http.StatusConflict, response.Body.String())
	}
	var payload struct {
		Error     string `json:"error"`
		PageID    int64  `json:"page_id"`
		ProjectID int64  `json:"project_id"`
		Mode      string `json:"mode"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode conflict response: %v; body=%s", err, response.Body.String())
	}
	if payload.Error != "advanced_page_requires_page_project_api" ||
		payload.PageID != pageID ||
		payload.ProjectID != project.ID ||
		payload.Mode != project.Mode {
		t.Fatalf("unexpected conflict payload: %#v", payload)
	}
}

func TestAdvancedPageRejectsEveryLegacyAPIMutation(t *testing.T) {
	s, token := newTestAutomationServer(t, "pages:read,pages:write,pages:publish")
	page, project, revisionID := createLegacyGuardAdvancedPage(t, s)

	cases := []struct {
		name       string
		method     string
		target     string
		body       string
		revisionID int64
		handler    http.HandlerFunc
	}{
		{
			name:    "patch",
			method:  http.MethodPatch,
			target:  "/api/admin/v1/pages/" + strconv.FormatInt(page.ID, 10),
			body:    `{"title":"不应写入的标题"}`,
			handler: s.apiUpdateContent,
		},
		{
			name:    "relink",
			method:  http.MethodPost,
			target:  "/api/admin/v1/pages/" + strconv.FormatInt(page.ID, 10) + "/relink",
			body:    `{"trans_group":"another-group"}`,
			handler: s.apiRelinkContent,
		},
		{
			name:    "discard",
			method:  http.MethodPost,
			target:  "/api/admin/v1/pages/" + strconv.FormatInt(page.ID, 10) + "/discard",
			body:    `{"reason":"不应覆盖原标记"}`,
			handler: s.apiDiscardContent,
		},
		{
			name:    "undiscard",
			method:  http.MethodDelete,
			target:  "/api/admin/v1/pages/" + strconv.FormatInt(page.ID, 10) + "/discard",
			handler: s.apiUndiscardContent,
		},
		{
			name:       "restore",
			method:     http.MethodPost,
			target:     "/api/admin/v1/pages/" + strconv.FormatInt(page.ID, 10) + "/revisions/" + strconv.FormatInt(revisionID, 10) + "/restore",
			revisionID: revisionID,
			handler:    s.apiRestoreRevision,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := legacyPageAPIRequest(
				t, s, token, tc.method, tc.target, tc.body, page.ID, tc.revisionID, tc.handler,
			)
			assertAdvancedLegacyAPIConflict(t, response, page.ID, project)
		})
	}

	after, err := s.store.GetPostByID(page.ID)
	if err != nil || after == nil {
		t.Fatalf("get page after rejected mutations: page=%#v err=%v", after, err)
	}
	if after.Title != page.Title ||
		after.TransGroup != page.TransGroup ||
		after.DiscardReason != page.DiscardReason ||
		!after.Discarded() {
		t.Fatalf("legacy API changed advanced page: before=%#v after=%#v", page, after)
	}
	revisions, err := s.store.PostRevisions(page.ID)
	if err != nil {
		t.Fatalf("list revisions after rejected mutations: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("revisions after rejected mutations = %d, want 1", len(revisions))
	}
}

func TestStandardPageLegacyAPIUpdateRemainsCompatible(t *testing.T) {
	s, token := newTestAutomationServer(t, "pages:read,pages:write")
	pageID, err := s.store.CreatePost(&store.Post{
		Type:       "page",
		Lang:       "zh",
		Slug:       "standard-legacy-compatible",
		Title:      "标准页面原始标题",
		Content:    "标准页面正文",
		Status:     "draft",
		EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create standard page: %v", err)
	}
	response := legacyPageAPIRequest(
		t,
		s,
		token,
		http.MethodPatch,
		"/api/admin/v1/pages/"+strconv.FormatInt(pageID, 10),
		`{"title":"标准页面新标题"}`,
		pageID,
		0,
		s.apiUpdateContent,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("standard page PATCH status = %d, body=%s", response.Code, response.Body.String())
	}
	updated, err := s.store.GetPostByID(pageID)
	if err != nil || updated == nil {
		t.Fatalf("get updated standard page: page=%#v err=%v", updated, err)
	}
	if updated.Title != "标准页面新标题" {
		t.Fatalf("standard page title = %q", updated.Title)
	}
	if project, err := s.store.GetPageProjectByPostID(pageID); err != nil || project != nil {
		t.Fatalf("legacy update should not create project: project=%#v err=%v", project, err)
	}
}

func TestAdvancedPageLegacyAdminMutationsAreHiddenAndRejected(t *testing.T) {
	s := newTestPublicServer(t, "")
	page, project, revisionID := createLegacyGuardAdvancedPage(t, s)
	handler := s.Handler()

	listRequest, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/pages?lang=zh", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("page list status = %d, body=%s", listResponse.Code, listResponse.Body.String())
	}
	deleteAction := `action="/admin/pages/` + strconv.FormatInt(page.ID, 10) + `/delete"`
	if strings.Contains(listResponse.Body.String(), deleteAction) {
		t.Fatalf("advanced page list exposes legacy delete action %q", deleteAction)
	}

	cases := []struct {
		name   string
		target string
		form   url.Values
	}{
		{
			name:   "save",
			target: "/admin/pages/" + strconv.FormatInt(page.ID, 10),
			form: url.Values{
				"title":  {"不应写入的标题"},
				"slug":   {page.Slug},
				"status": {"draft"},
			},
		},
		{
			name:   "delete",
			target: "/admin/pages/" + strconv.FormatInt(page.ID, 10) + "/delete",
			form:   url.Values{"lang": {"zh"}},
		},
		{
			name:   "translate",
			target: "/admin/pages/" + strconv.FormatInt(page.ID, 10) + "/translate",
			form:   url.Values{"lang": {"en"}},
		},
		{
			name:   "relink",
			target: "/admin/pages/" + strconv.FormatInt(page.ID, 10) + "/relink",
			form:   url.Values{"relink_target": {"another-group"}},
		},
		{
			name:   "restore",
			target: "/admin/revisions/" + strconv.FormatInt(revisionID, 10) + "/restore",
			form:   url.Values{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request, _ := authedAdminRequest(t, s, http.MethodPost, tc.target, tc.form)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusConflict {
				t.Fatalf("%s status = %d, want %d; body=%s",
					tc.name, response.Code, http.StatusConflict, response.Body.String())
			}
		})
	}

	after, err := s.store.GetPostByID(page.ID)
	if err != nil || after == nil {
		t.Fatalf("advanced page was deleted or unreadable: page=%#v err=%v", after, err)
	}
	if after.Title != page.Title || after.TransGroup != page.TransGroup || !after.Discarded() {
		t.Fatalf("legacy admin changed advanced page: before=%#v after=%#v", page, after)
	}
	stillProject, err := s.store.GetPageProjectByPostID(page.ID)
	if err != nil || stillProject == nil || stillProject.ID != project.ID {
		t.Fatalf("page project changed or disappeared: project=%#v err=%v", stillProject, err)
	}
	revisions, err := s.store.PostRevisions(page.ID)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("legacy admin changed revisions: len=%d err=%v", len(revisions), err)
	}
}
