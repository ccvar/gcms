package web

import (
	"bytes"
	"encoding/json"
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

func TestAdminCompositionLayoutControlsUseProtocolEnums(t *testing.T) {
	raw, err := os.ReadFile("../../templates/admin/page_composer.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, required := range []string{
		`value="compact"`, `value="comfortable"`, `value="wide"`, `value="full"`,
		`value="spacious"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("composer template missing protocol enum %s", required)
		}
	}
	if strings.Contains(body, `value="generous"`) {
		t.Fatal("composer template contains unsupported section_gap enum generous")
	}
	if !strings.Contains(body, "data-page-data-sources") {
		t.Fatal("composer does not receive the server data-source catalog")
	}
	script, err := os.ReadFile("../../assets/js/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, marker := range []string{
		"dataSourceByKey", "selectedSource.sorts", "selectedSource.fields",
		"selectedSource.max_items",
	} {
		if !strings.Contains(js, marker) {
			t.Fatalf("composer does not drive binding input from catalog field %q", marker)
		}
	}
	if strings.Contains(js, `"release_snapshot"`) {
		t.Fatal("composer advertises an update mode the server cannot persist")
	}
}

func TestAdminCompositionWorkbenchUsesSharedDropdownsAndContainedScrolling(t *testing.T) {
	templateRaw, err := os.ReadFile("../../templates/admin/page_composer.html")
	if err != nil {
		t.Fatal(err)
	}
	templateBody := string(templateRaw)
	for _, marker := range []string{
		`class="dropdown page-composer-select page-width-select" data-select-dropdown`,
		`data-dropdown-native data-page-width`,
		`data-dropdown-native data-page-shell`,
		`data-dropdown-native data-page-content-width`,
		`data-dropdown-native data-page-section-gap`,
		`<ul class="dd-menu" role="listbox"></ul>`,
		`{{template "page-composer-icon-desktop"}}`,
	} {
		if !strings.Contains(templateBody, marker) {
			t.Fatalf("composer shared control markup missing %q", marker)
		}
	}

	cssRaw, err := os.ReadFile("../../assets/css/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssRaw)
	for _, marker := range []string{
		"width: min(1760px, calc(100vw - 32px))",
		"height: var(--page-workbench-height)",
		"width: min(var(--page-preview-width), 100%)",
		"scrollbar-width: thin",
		".page-composer-grid.is-current-only",
		"label.page-responsive-visibility",
		"input.page-responsive-visibility-toggle",
	} {
		if !strings.Contains(css, marker) {
			t.Fatalf("composer contained workbench CSS missing %q", marker)
		}
	}
	filterStart := strings.Index(css, ".page-list-filters {")
	if filterStart < 0 {
		t.Fatal("page list filter rule missing")
	}
	filterEnd := strings.Index(css[filterStart:], "}")
	if filterEnd < 0 {
		t.Fatal("page list filter rule is incomplete")
	}
	filterRule := css[filterStart : filterStart+filterEnd]
	for _, marker := range []string{
		"padding: 0", "border: 0", "border-radius: 0", "background: transparent",
	} {
		if !strings.Contains(filterRule, marker) {
			t.Fatalf("page list filter shell still has visual chrome; missing %q in %s", marker, filterRule)
		}
	}
	if strings.Contains(css, `margin-inline: calc((min(100vw, 1800px)`) {
		t.Fatal("composer still uses viewport-breaking negative inline margins")
	}

	scriptRaw, err := os.ReadFile("../../assets/js/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptRaw)
	for _, marker := range []string{
		"function enhancePageSelect(select)",
		`select.setAttribute("data-dropdown-native", "")`,
		"window.adminInitDropdown(dropdown)",
		`window.adminRefreshDropdown(widthSelect)`,
		`dd.closest(".page-composer-grid")`,
		`visibility.setAttribute("role", "switch")`,
		`visibility.checked = !value.hidden`,
		`visibility.dataset.responsiveInverted = "true"`,
		`event.target.dataset.responsiveInverted === "true"`,
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("composer shared dropdown behavior missing %q", marker)
		}
	}
}

func TestCompositionDataSourceCatalogUsesActiveSiteTypesAndFields(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatal(err)
	}
	lang, items := s.compositionDataSourceCatalog("zh")
	if lang != "zh" {
		t.Fatalf("catalog lang = %q", lang)
	}
	byKey := map[string]compositionDataSourceDescriptor{}
	for _, item := range items {
		byKey[item.Key] = item
	}
	if byKey["post"].Key == "" || byKey["product"].Key == "" {
		t.Fatalf("active data sources missing: %+v", items)
	}
	productFields := map[string]bool{}
	defaults := 0
	for _, field := range byKey["product"].Fields {
		productFields[field.Key] = true
		if field.Default {
			defaults++
		}
	}
	if !productFields["title"] || !productFields["price"] || defaults == 0 ||
		len(byKey["product"].Sorts) == 0 ||
		byKey["product"].MaxItems != CompositionLimits.MaxBindingLimit {
		t.Fatalf("product catalog is incomplete or hard-coded client-side: %+v", byKey["product"])
	}
}

func TestAdminPageAppSourceEditFormRejectsOversizedAndUnknownInput(t *testing.T) {
	const formOverhead = int64(64 << 10)
	limit := pageAppTextEditMaxBytes*8 + formOverhead
	oversized := httptest.NewRequest(
		http.MethodPost, "/admin/pages/1/project/app-file",
		strings.NewReader("content="+strings.Repeat("x", int(limit)+1)),
	)
	oversized.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	oversizedResponse := httptest.NewRecorder()
	if parseAdminPageAppSourceEditForm(oversizedResponse, oversized) ||
		oversizedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized form accepted=%v status=%d body=%s",
			oversizedResponse.Code == http.StatusOK,
			oversizedResponse.Code, oversizedResponse.Body.String())
	}

	unknown := url.Values{
		"_etag":            {`"revision-1"`},
		"base_revision_id": {"1"},
		"path":             {"app.js"},
		"content":          {"x"},
		"unexpected":       {"value"},
	}
	unknownRequest := httptest.NewRequest(
		http.MethodPost, "/admin/pages/1/project/app-file",
		strings.NewReader(unknown.Encode()),
	)
	unknownRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unknownResponse := httptest.NewRecorder()
	if parseAdminPageAppSourceEditForm(unknownResponse, unknownRequest) ||
		unknownResponse.Code != http.StatusBadRequest ||
		!strings.Contains(unknownResponse.Body.String(), "未知字段") {
		t.Fatalf("unknown source form = %d %s", unknownResponse.Code, unknownResponse.Body.String())
	}
}

func createAdminCompositionPage(t *testing.T, s *Server, title, slug string) (*store.Post, *store.PageProject) {
	t.Helper()
	form := url.Values{
		"mode":       {store.PageModeComposition},
		"title":      {title},
		"slug":       {slug},
		"excerpt":    {"由测试输入的页面简介"},
		"shell_mode": {store.PageShellSite},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages/platform?lang=zh", form)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	pages, err := s.store.ListPages("zh")
	var page *store.Post
	for _, candidate := range pages {
		if candidate != nil && candidate.Slug == slug {
			page = candidate
			break
		}
	}
	if err != nil || page == nil {
		t.Fatalf("created page: page=%#v err=%v", page, err)
	}
	project, err := s.store.GetPageProjectByPostID(page.ID)
	if err != nil || project == nil {
		t.Fatalf("created project: project=%#v err=%v", project, err)
	}
	return page, project
}

func TestAdminCompositionComponentCatalogAddsHumanLabelsWithoutChangingProtocol(t *testing.T) {
	definitions := CompositionComponentRegistry()
	catalog := adminCompositionComponentCatalog()
	if len(catalog) != len(definitions) {
		t.Fatalf("catalog items=%d protocol definitions=%d", len(catalog), len(definitions))
	}
	byType := make(map[string]adminCompositionComponentView, len(catalog))
	for _, item := range catalog {
		byType[item.Type] = item
		if item.Label == "" || item.Description == "" ||
			item.Category == "" || item.CategoryLabel == "" {
			t.Fatalf("component lacks human metadata: %#v", item)
		}
		for _, property := range item.Properties {
			if item.PropertyLabels[property.Key] == "" {
				t.Fatalf("%s property %s lacks a human label", item.Type, property.Key)
			}
		}
	}
	if got := byType["hero.centered"]; got.Type != "hero.centered" ||
		got.Label != "顶部介绍" || got.CategoryLabel != "宣传与转化" {
		t.Fatalf("hero catalog metadata changed protocol identity: %#v", got)
	}
	if got := byType["posts.grid"]; got.Type != "posts.grid" ||
		got.Label != "文章列表" || got.PropertyLabels["empty_state"] == "" {
		t.Fatalf("posts catalog metadata incomplete: %#v", got)
	}
	if got := byType["cta.banner"]; got.Type != "cta.banner" ||
		got.Label != "下一步引导" || got.PropertyLabels["action"] != "跳转按钮" {
		t.Fatalf("cta catalog metadata incomplete: %#v", got)
	}

	raw, err := json.Marshal(byType["hero.centered"])
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`"type":"hero.centered"`,
		`"label":"顶部介绍"`,
		`"category_label":"宣传与转化"`,
		`"property_labels":`,
	} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("component catalog JSON missing %q: %s", marker, raw)
		}
	}

	catalog[0].Keywords[0] = "mutated"
	catalog[0].Properties[0].Key = "mutated"
	fresh := adminCompositionComponentCatalog()
	if fresh[0].Keywords[0] == "mutated" || fresh[0].Properties[0].Key == "mutated" {
		t.Fatal("admin component catalog did not return defensive copies")
	}
}

func TestAdminPageEditorModeDefaultsSafelyWithoutPersistence(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		projectMode string
		want        string
	}{
		{
			name:        "composition defaults to beginner mode",
			target:      "/admin/pages/42/project",
			projectMode: store.PageModeComposition,
			want:        adminPageEditorSimple,
		},
		{
			name:        "advanced composition is explicit",
			target:      "/admin/pages/42/project?editor=advanced",
			projectMode: store.PageModeComposition,
			want:        adminPageEditorAdvanced,
		},
		{
			name:        "unknown values fail closed to beginner mode",
			target:      "/admin/pages/42/project?editor=expert",
			projectMode: store.PageModeComposition,
			want:        adminPageEditorSimple,
		},
		{
			name:        "app workbench stays advanced",
			target:      "/admin/pages/42/project?editor=simple",
			projectMode: store.PageModeApp,
			want:        adminPageEditorAdvanced,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if got := adminPageEditorMode(req, tc.projectMode); got != tc.want {
				t.Fatalf("editor mode=%q want=%q", got, tc.want)
			}
		})
	}
	if got := adminPageEditorURL(42, adminPageEditorSimple); got !=
		"/admin/pages/42/project?editor=simple" {
		t.Fatalf("simple editor URL=%q", got)
	}
	if got := adminPageEditorURL(42, adminPageEditorAdvanced); got !=
		"/admin/pages/42/project?editor=advanced" {
		t.Fatalf("advanced editor URL=%q", got)
	}
}

func TestAdminLegacyCompositionProjectRendersSimpleAndAdvancedModes(t *testing.T) {
	s := newTestPublicServer(t, "")
	page, project := createAdminCompositionPage(t, s, "旧版编排页面", "legacy-composition-editor")
	beforeRevision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || beforeRevision == nil {
		t.Fatalf("working revision=%#v err=%v", beforeRevision, err)
	}
	beforeRevisions, err := s.store.ListPageProjectRevisions(project.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(beforeRevision.ManifestJSON, `"editor"`) {
		t.Fatalf("fixture unexpectedly persists editor presentation: %s", beforeRevision.ManifestJSON)
	}

	for _, tc := range []struct {
		name         string
		suffix       string
		mode         string
		switchTarget string
	}{
		{
			name:         "default beginner mode",
			mode:         adminPageEditorSimple,
			switchTarget: adminPageEditorURL(page.ID, adminPageEditorAdvanced),
		},
		{
			name:         "advanced mode remains reachable",
			suffix:       "?editor=advanced",
			mode:         adminPageEditorAdvanced,
			switchTarget: adminPageEditorURL(page.ID, adminPageEditorSimple),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := authedAdminRequest(
				t, s, http.MethodGet,
				"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project"+tc.suffix,
				nil,
			)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status=%d body=%s", tc.mode, rec.Code, rec.Body.String())
			}
			marker := `data-page-editor-mode="` + tc.mode + `"`
			if !strings.Contains(rec.Body.String(), marker) {
				t.Fatalf("%s response missing %q", tc.mode, marker)
			}
			if !strings.Contains(rec.Body.String(), "旧版编排页面") {
				t.Fatalf("%s response lost legacy page metadata", tc.mode)
			}
			if !strings.Contains(rec.Body.String(), `href="`+tc.switchTarget+`"`) {
				t.Fatalf("%s response lacks safe mode switch to %q", tc.mode, tc.switchTarget)
			}
		})
	}

	afterProject, err := s.store.GetPageProject(project.ID)
	if err != nil || afterProject == nil {
		t.Fatalf("project after reads=%#v err=%v", afterProject, err)
	}
	afterRevision, err := s.store.GetPageProjectRevision(afterProject.WorkingRevisionID)
	if err != nil || afterRevision == nil {
		t.Fatalf("revision after reads=%#v err=%v", afterRevision, err)
	}
	afterRevisions, err := s.store.ListPageProjectRevisions(project.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if afterProject.WorkingRevisionID != project.WorkingRevisionID ||
		afterProject.PublishedRevisionID != project.PublishedRevisionID ||
		afterRevision.ManifestJSON != beforeRevision.ManifestJSON ||
		len(afterRevisions) != len(beforeRevisions) {
		t.Fatalf(
			"selecting an editor presentation mutated legacy data: project=%#v revision_count=%d want=%d",
			afterProject, len(afterRevisions), len(beforeRevisions),
		)
	}
}

func TestAdminAdvancedEditorSavePreservesPresentationMode(t *testing.T) {
	s := newTestPublicServer(t, "")
	page, project := createAdminCompositionPage(t, s, "高级编排保存", "advanced-editor-save")
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || revision == nil {
		t.Fatalf("working revision=%#v err=%v", revision, err)
	}
	form := url.Values{
		"_etag":         {project.ETag()},
		"title":         {page.Title},
		"slug":          {page.Slug},
		"excerpt":       {page.Excerpt},
		"manifest_json": {revision.ManifestJSON},
		"summary":       {"高级模式回归测试"},
	}
	req, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project?editor=advanced",
		form,
	)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, "saved=1") ||
		!strings.Contains(got, "editor=advanced") {
		t.Fatalf("advanced save redirect lost presentation mode: %q", got)
	}
}

func TestAdminPageTypeChooserKeepsLegacyStandardPath(t *testing.T) {
	s := newTestPublicServer(t, "")
	req, token := authedAdminRequest(t, s, http.MethodGet, "/admin/pages/new?lang=zh", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chooser status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, marker := range []string{
		"标准页面", "自由编排页", "互动应用",
		"/admin/pages/new?mode=standard&amp;lang=zh",
	} {
		if !strings.Contains(rec.Body.String(), marker) {
			t.Fatalf("chooser missing %q", marker)
		}
	}

	legacyReq := httptest.NewRequest(http.MethodGet, "/admin/pages/new?mode=standard&lang=zh", nil)
	legacyReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	legacy := httptest.NewRecorder()
	s.Handler().ServeHTTP(legacy, legacyReq)
	if legacy.Code != http.StatusOK || !strings.Contains(legacy.Body.String(), "Markdown") {
		t.Fatalf("legacy form status=%d body=%s", legacy.Code, legacy.Body.String())
	}
}

func TestAdminPlatformPageCreateFormUsesAccessibleResponsiveLayout(t *testing.T) {
	s := newTestPublicServer(t, "")
	for _, tc := range []struct {
		mode string
		cta  string
	}{
		{mode: store.PageModeComposition, cta: "创建并进入编排工作台"},
		{mode: store.PageModeApp, cta: "创建并进入应用工作台"},
	} {
		req, _ := authedAdminRequest(
			t,
			s,
			http.MethodGet,
			"/admin/pages/new?mode="+tc.mode+"&lang=zh",
			nil,
		)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s form status=%d body=%s", tc.mode, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, marker := range []string{
			`class="page-project-create"`,
			`data-page-project-create`,
			`data-no-dirty data-no-busy`,
			`class="page-project-create-head"`,
			`class="page-create-grid"`,
			`class="field page-create-title"`,
			`for="page-create-title"`,
			`id="page-create-title"`,
			`aria-describedby="page-create-title-hint"`,
			`for="page-create-shell"`,
			`id="page-create-shell"`,
			`value="site"`,
			`value="minimal"`,
			`value="none"`,
			`class="page-create-actions"`,
			`data-page-create-submit`,
			tc.cta,
		} {
			if !strings.Contains(body, marker) {
				t.Fatalf("%s form missing %q", tc.mode, marker)
			}
		}
		for _, legacy := range []string{
			`class="page-project-create card"`,
			`class="form-grid two"`,
			`class="form-actions"`,
			`class="form-error"`,
			`autocomplete="name"`,
		} {
			if strings.Contains(body, legacy) {
				t.Fatalf("%s form retained unstyled legacy markup %q", tc.mode, legacy)
			}
		}
	}

	rawCSS, err := os.ReadFile("../../assets/css/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(rawCSS)
	for _, marker := range []string{
		".page-project-create-head",
		".page-create-grid",
		"grid-template-columns: repeat(12, minmax(0, 1fr))",
		".page-create-title",
		".page-create-buttons",
		"@media (max-width: 760px)",
		"box-shadow: 0 0 0 3px",
	} {
		if !strings.Contains(css, marker) {
			t.Fatalf("page create responsive form CSS missing %q", marker)
		}
	}
	if strings.Contains(css, "var(--card)") {
		t.Fatal("page platform CSS references undefined --card token")
	}
	if strings.Contains(css, "column-reverse") {
		t.Fatal("page create mobile actions reverse visual order away from keyboard order")
	}

	rawJS, err := os.ReadFile("../../assets/js/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(rawJS)
	for _, marker := range []string{
		`document.querySelector("[data-page-project-create]")`,
		`form.dataset.submitting === "true"`,
		`submit.setAttribute("aria-busy", "true")`,
	} {
		if !strings.Contains(js, marker) {
			t.Fatalf("page create duplicate-submit guard missing %q", marker)
		}
	}
}

func TestAdminPlatformPageCreateErrorUsesStyledFormAndPreservesValues(t *testing.T) {
	s := newTestPublicServer(t, "")
	before, err := s.store.ListPages("zh")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"mode":       {store.PageModeComposition},
		"title":      {""},
		"slug":       {"kept-slug"},
		"excerpt":    {"保留的页面简介"},
		"author":     {"保留的作者"},
		"shell_mode": {store.PageShellMinimal},
		"meta_desc":  {"保留的搜索摘要"},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages/platform?lang=zh", form)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank title status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{
		`class="form-err page-project-error" role="alert"`,
		"标题不能为空。",
		`value="kept-slug"`,
		"保留的页面简介",
		`value="保留的作者"`,
		`value="minimal" selected`,
		"保留的搜索摘要",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("validation form missing preserved marker %q", marker)
		}
	}
	after, err := s.store.ListPages("zh")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("validation error created a page: before=%d after=%d", len(before), len(after))
	}
}

func TestAdminCompositionPageCreateListSaveConflictAndPreview(t *testing.T) {
	s := newTestPublicServer(t, "")
	page, project := createAdminCompositionPage(t, s, "发布活动", "launch-event")
	if page.Status != "draft" || project.Mode != store.PageModeComposition ||
		project.WorkingRevisionID <= 0 || project.PublishedRevisionID != 0 {
		t.Fatalf("unexpected initial state page=%#v project=%#v", page, project)
	}
	initial, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || initial == nil {
		t.Fatalf("initial revision=%#v err=%v", initial, err)
	}
	validation := s.NormalizeAndValidateCompositionManifest([]byte(initial.ManifestJSON), page.Lang)
	if !validation.Valid {
		t.Fatalf("initial manifest invalid: %#v", validation.Diagnostics)
	}

	workbenchReq, _ := authedAdminRequest(
		t, s, http.MethodGet,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project?editor=advanced",
		nil,
	)
	workbench := httptest.NewRecorder()
	s.Handler().ServeHTTP(workbench, workbenchReq)
	if workbench.Code != http.StatusOK {
		t.Fatalf("workbench status=%d body=%s", workbench.Code, workbench.Body.String())
	}
	for _, marker := range []string{
		`data-page-composer`,
		`data-dropdown-native data-page-width`,
		`data-dropdown-native data-page-shell`,
		`<svg class="visual-btn-ico"`,
	} {
		if !strings.Contains(workbench.Body.String(), marker) {
			t.Fatalf("composition workbench missing %q", marker)
		}
	}

	listReq, _ := authedAdminRequest(
		t, s, http.MethodGet,
		"/admin/pages?lang=zh&q=launch&mode=composition&status=draft&origin=admin&build=idle",
		nil,
	)
	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, listReq)
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	for _, marker := range []string{"发布活动", "自由编排", "后台人工", "1 个组件"} {
		if !strings.Contains(list.Body.String(), marker) {
			t.Fatalf("filtered list missing %q", marker)
		}
	}
	for _, marker := range []string{
		`class="dropdown page-filter-dropdown page-filter-mode" data-select-dropdown`,
		`select class="dd-native" name="mode" data-dropdown-native`,
		`href="/admin/pages/` + strconv.FormatInt(page.ID, 10) + `/project/preview?revision=` +
			strconv.FormatInt(project.WorkingRevisionID, 10) + `"`,
		`action="/admin/pages/` + strconv.FormatInt(page.ID, 10) + `/project/delete"`,
		`name="_etag" value="&#34;revision-` + strconv.FormatInt(project.WorkingRevisionID, 10) + `&#34;"`,
	} {
		if !strings.Contains(list.Body.String(), marker) {
			t.Fatalf("page list missing common control or project action %q", marker)
		}
	}

	var manifest CompositionManifest
	if err := json.Unmarshal([]byte(initial.ManifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	var props map[string]any
	if err := json.Unmarshal(manifest.Sections[0].Props, &props); err != nil {
		t.Fatal(err)
	}
	props["title"] = "发布活动 · 第二版"
	manifest.Sections[0].Props, _ = json.Marshal(props)
	manifestJSON, _ := json.Marshal(manifest)
	saveForm := url.Values{
		"_etag":         {project.ETag()},
		"title":         {"发布活动"},
		"slug":          {"launch-event"},
		"excerpt":       {"新版简介"},
		"manifest_json": {string(manifestJSON)},
		"summary":       {"调整 Hero 标题"},
	}
	saveReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project", saveForm)
	saved := httptest.NewRecorder()
	s.Handler().ServeHTTP(saved, saveReq)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	current, _ := s.store.GetPageProject(project.ID)
	if current.WorkingRevisionID == project.WorkingRevisionID {
		t.Fatal("save did not advance immutable revision")
	}
	unchangedPost, _ := s.store.GetPostByID(page.ID)
	if unchangedPost.Status != "draft" || unchangedPost.Excerpt != page.Excerpt {
		t.Fatalf("draft save mutated public post: %#v", unchangedPost)
	}

	staleReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project", saveForm)
	stale := httptest.NewRecorder()
	s.Handler().ServeHTTP(stale, staleReq)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "其他操作更新") {
		t.Fatalf("stale save status=%d body=%s", stale.Code, stale.Body.String())
	}

	previewReq, _ := authedAdminRequest(
		t, s, http.MethodGet,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/preview?revision="+strconv.FormatInt(current.WorkingRevisionID, 10),
		nil,
	)
	preview := httptest.NewRecorder()
	s.Handler().ServeHTTP(preview, previewReq)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	if !strings.Contains(preview.Body.String(), "发布活动 · 第二版") ||
		preview.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("preview did not use unified immutable renderer")
	}

	publishForm := url.Values{"_etag": {current.ETag()}}
	publishReq, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/publish",
		publishForm,
	)
	published := httptest.NewRecorder()
	s.Handler().ServeHTTP(published, publishReq)
	if published.Code != http.StatusSeeOther {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body.String())
	}
	current, _ = s.store.GetPageProject(project.ID)
	publicPage, _ := s.store.GetPostByID(page.ID)
	if publicPage.Status != "published" || current.PublishedRevisionID != current.WorkingRevisionID {
		t.Fatalf("published page=%#v project=%#v", publicPage, current)
	}

	rollbackForm := url.Values{
		"_etag":       {current.ETag()},
		"revision_id": {strconv.FormatInt(initial.ID, 10)},
	}
	rollbackReq, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/rollback",
		rollbackForm,
	)
	rolledBack := httptest.NewRecorder()
	s.Handler().ServeHTTP(rolledBack, rollbackReq)
	if rolledBack.Code != http.StatusSeeOther {
		t.Fatalf("rollback status=%d body=%s", rolledBack.Code, rolledBack.Body.String())
	}
	current, _ = s.store.GetPageProject(project.ID)
	if current.PublishedRevisionID != initial.ID || current.WorkingRevisionID == initial.ID {
		t.Fatalf("rollback should switch only public pointer: %#v", current)
	}
}

func TestAdminDraftPageProjectDeleteRequiresCurrentETagAndCleansPrivateFiles(t *testing.T) {
	s := newTestPublicServer(t, "")
	page, project := createAdminCompositionPage(t, s, "待删除演示", "delete-project-demo")
	root := s.store.PageProjectStorageDir()
	projectKey := strconv.FormatInt(project.ID, 10)
	dirs := []string{
		filepath.Join(root, projectKey, "assets"),
		filepath.Join(root, "sources", projectKey, "source-hash"),
		filepath.Join(root, "artifacts", projectKey, "1", "artifact-hash"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fixture"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	target := "/admin/pages/" + strconv.FormatInt(page.ID, 10) + "/project/delete"
	_, token := authedAdminRequest(t, s, http.MethodGet, "/admin/pages?lang=zh", nil)
	noCSRFReq := httptest.NewRequest(
		http.MethodPost, target,
		strings.NewReader(url.Values{"_etag": {project.ETag()}, "lang": {"zh"}}.Encode()),
	)
	noCSRFReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	noCSRFReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	noCSRF := httptest.NewRecorder()
	s.Handler().ServeHTTP(noCSRF, noCSRFReq)
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF status=%d body=%s", noCSRF.Code, noCSRF.Body.String())
	}

	staleReq, _ := authedAdminRequest(t, s, http.MethodPost, target, url.Values{
		"_etag": {`"stale"`},
		"lang":  {"zh"},
	})
	stale := httptest.NewRecorder()
	s.Handler().ServeHTTP(stale, staleReq)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale delete status=%d body=%s", stale.Code, stale.Body.String())
	}
	if current, err := s.store.GetPostByID(page.ID); err != nil || current == nil {
		t.Fatalf("stale delete removed page: page=%#v err=%v", current, err)
	}

	deleteReq, _ := authedAdminRequest(t, s, http.MethodPost, target, url.Values{
		"_etag":  {project.ETag()},
		"lang":   {"zh"},
		"q":      {"待删除"},
		"mode":   {store.PageModeComposition},
		"status": {"draft"},
		"origin": {store.PageOriginAdmin},
		"build":  {store.PageProjectBuildIdle},
	})
	deleted := httptest.NewRecorder()
	s.Handler().ServeHTTP(deleted, deleteReq)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	wantLocation := "/admin/pages?lang=zh&status=draft&mode=composition&origin=admin&build=idle&q=" +
		url.QueryEscape("待删除")
	if got := deleted.Header().Get("Location"); got != wantLocation {
		t.Fatalf("delete redirect=%q want=%q", got, wantLocation)
	}
	if current, err := s.store.GetPostByID(page.ID); err != nil || current != nil {
		t.Fatalf("deleted page=%#v err=%v", current, err)
	}
	if current, err := s.store.GetPageProject(project.ID); err != nil || current != nil {
		t.Fatalf("deleted project=%#v err=%v", current, err)
	}
	for _, dir := range []string{
		filepath.Join(root, projectKey),
		filepath.Join(root, "sources", projectKey),
		filepath.Join(root, "artifacts", projectKey),
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("private project directory still exists: %s err=%v", dir, err)
		}
	}
}

func TestAdminPublishedPageProjectCannotBeDeleted(t *testing.T) {
	s := newTestPublicServer(t, "")
	page, project := createAdminCompositionPage(t, s, "已发布演示", "published-project-delete")
	page.Status = "published"
	if err := s.store.UpdatePost(page); err != nil {
		t.Fatal(err)
	}
	target := "/admin/pages/" + strconv.FormatInt(page.ID, 10) + "/project/delete"
	req, _ := authedAdminRequest(t, s, http.MethodPost, target, url.Values{
		"_etag": {project.ETag()},
		"lang":  {"zh"},
	})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "下线流程") {
		t.Fatalf("published delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if current, err := s.store.GetPostByID(page.ID); err != nil || current == nil {
		t.Fatalf("published delete removed page: page=%#v err=%v", current, err)
	}

	listReq, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/pages?lang=zh", nil)
	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, listReq)
	deleteAction := `action="/admin/pages/` + strconv.FormatInt(page.ID, 10) + `/project/delete"`
	if strings.Contains(list.Body.String(), deleteAction) ||
		!strings.Contains(list.Body.String(), "已发布页面需先下线后删除") {
		t.Fatalf("published project list exposed destructive action: %s", list.Body.String())
	}
}

func TestAdminAppWorkbenchUploadsBuildsAndUsesSandboxPreview(t *testing.T) {
	s := newTestPublicServer(t, "")
	create := url.Values{
		"mode":       {store.PageModeApp},
		"title":      {"互动演示"},
		"slug":       {"interactive-demo"},
		"shell_mode": {store.PageShellSite},
	}
	createReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages/platform?lang=zh", create)
	created := httptest.NewRecorder()
	s.Handler().ServeHTTP(created, createReq)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create app status=%d body=%s", created.Code, created.Body.String())
	}
	var page *store.Post
	pages, _ := s.store.ListPages("zh")
	for _, candidate := range pages {
		if candidate != nil && candidate.Slug == "interactive-demo" {
			page = candidate
			break
		}
	}
	if page == nil {
		t.Fatal("app draft page missing")
	}
	project, _ := s.store.GetPageProjectByPostID(page.ID)
	if project == nil || project.Mode != store.PageModeApp {
		t.Fatalf("app project=%#v", project)
	}

	_, token := authedAdminRequest(t, s, http.MethodGet, "/admin/pages", nil)
	dbSession, ok, err := s.store.GetAdminSession(token)
	if err != nil || !ok {
		t.Fatalf("session ok=%v err=%v", ok, err)
	}
	packageBytes := pageAppZipForTest(t, validPageAppFilesForTest())
	uploadReq := httptest.NewRequest(
		http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-package",
		bytes.NewReader(packageBytes),
	)
	uploadReq.Header.Set("Content-Type", "application/zip")
	uploadReq.Header.Set("Accept", "application/json")
	uploadReq.Header.Set("X-CSRF-Token", dbSession.CSRF)
	uploadReq.Header.Set("If-Match", project.ETag())
	uploadReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	uploaded := httptest.NewRecorder()
	s.Handler().ServeHTTP(uploaded, uploadReq)
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	project, _ = s.store.GetPageProject(project.ID)
	revision, _ := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if revision == nil || revision.SourceBundleRef == "" || revision.SourceHash == "" {
		t.Fatalf("uploaded revision=%#v", revision)
	}
	if strings.Contains(revision.SourceBundleRef, "..") || strings.HasPrefix(revision.SourceBundleRef, "/") {
		t.Fatalf("unsafe source ref %q", revision.SourceBundleRef)
	}

	buildForm := url.Values{"_etag": {project.ETag()}}
	buildReq, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-build",
		buildForm,
	)
	built := httptest.NewRecorder()
	s.Handler().ServeHTTP(built, buildReq)
	if built.Code != http.StatusCreated && built.Code != http.StatusOK {
		t.Fatalf("build status=%d body=%s", built.Code, built.Body.String())
	}
	builds, err := s.store.ListPageBuilds(project.ID, revision.ID, 10)
	if err != nil || len(builds) == 0 || builds[0].Status != store.PageBuildReady {
		t.Fatalf("builds=%#v err=%v", builds, err)
	}
	project, _ = s.store.GetPageProject(project.ID)
	listReq, _ := authedAdminRequest(
		t, s, http.MethodGet, "/admin/pages?lang=zh&mode=app&status=draft&build=ready", nil,
	)
	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, listReq)
	appPreviewURL := `/admin/pages/` + strconv.FormatInt(page.ID, 10) +
		`/project/app-preview?revision=` + strconv.FormatInt(project.WorkingRevisionID, 10)
	if list.Code != http.StatusOK ||
		!strings.Contains(list.Body.String(), `href="`+appPreviewURL+`"`) ||
		!strings.Contains(list.Body.String(),
			`action="/admin/pages/`+strconv.FormatInt(page.ID, 10)+`/project/delete"`) {
		t.Fatalf("app list is missing preview/delete actions: status=%d body=%s",
			list.Code, list.Body.String())
	}

	workbenchReq := httptest.NewRequest(
		http.MethodGet,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project",
		nil,
	)
	workbenchReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	workbench := httptest.NewRecorder()
	s.Handler().ServeHTTP(workbench, workbenchReq)
	// The admin embeds the trusted GCMS preview shell with same-origin enabled so the
	// shell can load its nested frame. The untrusted app frame inside that shell
	// remains sandboxed with allow-scripts only (covered by delivery tests).
	for _, marker := range []string{"工程文件", "构建日志", "运行能力", `sandbox="allow-scripts allow-same-origin"`, "index.html"} {
		if !strings.Contains(workbench.Body.String(), marker) {
			t.Fatalf("app workbench missing %q; status=%d", marker, workbench.Code)
		}
	}

	previewReq := httptest.NewRequest(
		http.MethodGet,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-preview?revision="+
			strconv.FormatInt(revision.ID, 10)+"&build="+strconv.FormatInt(builds[0].ID, 10),
		nil,
	)
	previewReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	preview := httptest.NewRecorder()
	s.Handler().ServeHTTP(preview, previewReq)
	if preview.Code != http.StatusTemporaryRedirect ||
		!strings.HasPrefix(preview.Header().Get("Location"), "/preview/page-apps/") {
		t.Fatalf("preview status=%d location=%q body=%s", preview.Code, preview.Header().Get("Location"), preview.Body.String())
	}

	editorReq, _ := authedAdminRequest(
		t, s, http.MethodGet,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project?edit_file=app.js",
		nil,
	)
	editor := httptest.NewRecorder()
	s.Handler().ServeHTTP(editor, editorReq)
	for _, marker := range []string{
		`id="app-source-editor"`, "校验并保存新修订",
		`document.querySelector`, `textContent =`,
	} {
		if editor.Code != http.StatusOK || !strings.Contains(editor.Body.String(), marker) {
			t.Fatalf("source editor missing %q; status=%d body=%s", marker, editor.Code, editor.Body.String())
		}
	}
	editedJS := `document.querySelector("#app").textContent = "admin edited";`
	editForm := url.Values{
		"_etag":            {project.ETag()},
		"base_revision_id": {strconv.FormatInt(revision.ID, 10)},
		"path":             {"app.js"},
		"content":          {editedJS},
		"summary":          {"后台在线编辑脚本"},
	}
	noCSRFForm := url.Values{}
	for key, values := range editForm {
		noCSRFForm[key] = append([]string(nil), values...)
	}
	noCSRFReq := httptest.NewRequest(
		http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-file",
		strings.NewReader(noCSRFForm.Encode()),
	)
	noCSRFReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	noCSRFReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	noCSRF := httptest.NewRecorder()
	s.Handler().ServeHTTP(noCSRF, noCSRFReq)
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("admin source edit without CSRF = %d %s", noCSRF.Code, noCSRF.Body.String())
	}
	duplicateForm := url.Values{}
	for key, values := range editForm {
		duplicateForm[key] = append([]string(nil), values...)
	}
	duplicateForm["_etag"] = []string{project.ETag(), project.ETag()}
	duplicateReq, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-file",
		duplicateForm,
	)
	duplicate := httptest.NewRecorder()
	s.Handler().ServeHTTP(duplicate, duplicateReq)
	if duplicate.Code != http.StatusBadRequest || !strings.Contains(duplicate.Body.String(), "重复字段") {
		t.Fatalf("duplicate source edit field = %d %s", duplicate.Code, duplicate.Body.String())
	}
	tooLargeForm := url.Values{}
	for key, values := range editForm {
		tooLargeForm[key] = append([]string(nil), values...)
	}
	tooLargeForm.Set("content", strings.Repeat("x", int(pageAppTextEditMaxBytes)+1))
	tooLargeReq, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-file",
		tooLargeForm,
	)
	tooLarge := httptest.NewRecorder()
	s.Handler().ServeHTTP(tooLarge, tooLargeReq)
	if tooLarge.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(tooLarge.Body.String(), "source_file_too_large") {
		t.Fatalf("oversized source edit = %d %s", tooLarge.Code, tooLarge.Body.String())
	}
	wrongTypeReq := httptest.NewRequest(
		http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-file",
		strings.NewReader(`{"content":"x"}`),
	)
	wrongTypeReq.Header.Set("Content-Type", "application/json")
	wrongTypeReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	wrongType := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongType, wrongTypeReq)
	if wrongType.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong source edit content type = %d %s", wrongType.Code, wrongType.Body.String())
	}
	editReq, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-file",
		editForm,
	)
	edited := httptest.NewRecorder()
	s.Handler().ServeHTTP(edited, editReq)
	if edited.Code != http.StatusSeeOther ||
		!strings.Contains(edited.Header().Get("Location"), "app_source_saved=1") {
		t.Fatalf("admin source edit status=%d location=%q body=%s",
			edited.Code, edited.Header().Get("Location"), edited.Body.String())
	}
	advanced, _ := s.store.GetPageProject(project.ID)
	if advanced.WorkingRevisionID == revision.ID {
		t.Fatal("admin source edit did not advance immutable revision")
	}
	oldRaw, _, oldErr := s.pageAppAdminSourceFile(project.ID, revision.ID, "app.js")
	newRaw, _, newErr := s.pageAppAdminSourceFile(project.ID, advanced.WorkingRevisionID, "app.js")
	if oldErr != nil || newErr != nil || string(oldRaw) == editedJS || string(newRaw) != editedJS {
		t.Fatalf("immutable admin edit old=%q new=%q oldErr=%v newErr=%v",
			oldRaw, newRaw, oldErr, newErr)
	}
	staleReq, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project/app-file",
		editForm,
	)
	staleEdit := httptest.NewRecorder()
	s.Handler().ServeHTTP(staleEdit, staleReq)
	if staleEdit.Code != http.StatusConflict ||
		!strings.Contains(staleEdit.Body.String(), "其他操作更新") {
		t.Fatalf("stale admin source edit status=%d body=%s", staleEdit.Code, staleEdit.Body.String())
	}
}
