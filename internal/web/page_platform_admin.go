package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cms.ccvar.com/internal/store"
)

type adminPageListRow struct {
	Post        *store.Post
	Project     *store.PageProject
	Mode        string
	ModeLabel   string
	StatusLabel string
	Origin      string
	OriginLabel string
	DataSummary string
	EditURL     string
}

type adminPagesView struct {
	*View
	Rows         []adminPageListRow
	ModeFilter   string
	OriginFilter string
	BuildFilter  string
}

type adminPageNewView struct {
	*View
	Mode      string
	ModeLabel string
	ShellMode string
}

const (
	adminPageEditorSimple   = "simple"
	adminPageEditorAdvanced = "advanced"
)

// adminCompositionComponentView adds human-facing authoring metadata without
// changing the component protocol used by manifests, validation or rendering.
// The embedded definition keeps the existing admin JavaScript contract intact.
type adminCompositionComponentView struct {
	CompositionComponentDefinition
	Label          string            `json:"label"`
	Description    string            `json:"description"`
	Category       string            `json:"category"`
	CategoryLabel  string            `json:"category_label"`
	Keywords       []string          `json:"keywords,omitempty"`
	PropertyLabels map[string]string `json:"property_labels,omitempty"`
}

type adminCompositionComponentCategoryView struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type adminPageProjectView struct {
	*View
	PagePost                *store.Post
	PageProject             *store.PageProject
	PageRevision            *store.PageProjectRevision
	PageMeta                store.PageRevisionMeta
	PageRevisions           []*store.PageProjectRevision
	PagePublications        []*store.PagePublication
	PageComponents          []CompositionComponentDefinition
	PageComponentCatalog    []adminCompositionComponentView
	PageComponentCategories []adminCompositionComponentCategoryView
	PageDataSources         []compositionDataSourceDescriptor
	PageManifestJSON        string
	PageManifestPretty      string
	PageMetaJSON            string
	PageETag                string
	PageModeLabel           string
	PageOriginLabel         string
	PageSaved               bool
	PagePublished           bool
	PageDiagnostics         []CompositionDiagnostic
	PageConflict            bool
	PageConflictETag        string
	PageCanCompose          bool
	PageCanPublish          bool
	PageEditorMode          string
	PageEditorSimple        bool
	PageEditorAdvanced      bool
	PageSimpleEditorURL     string
	PageAdvancedEditorURL   string
	PageAppFiles            []pageAppAdminFile
	PageAppBuilds           []*store.PageBuild
	PageAppCapabilities     []adminPageAppCapabilityView
	PageAppHasSource        bool
	PageAppReadyBuild       *store.PageBuild
	PageAppEditFile         string
	PageAppEditContent      string
	PageAppEditSHA256       string
	PageAppEditMedia        string
}

type adminPageAppCapabilityView struct {
	Name           string
	Description    string
	Status         string
	ConfigJSON     string
	Grantable      bool
	RequiresBridge bool
}

func (s *Server) registerPagePlatformAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/pages/platform", s.requireAuth(s.adminPageProjectCreate))
	mux.HandleFunc("GET /admin/pages/{id}/project", s.requireAuth(s.adminPageProjectEdit))
	mux.HandleFunc("POST /admin/pages/{id}/project", s.requireAuth(s.adminPageProjectSave))
	mux.HandleFunc("POST /admin/pages/{id}/project/delete", s.requireAuth(s.adminPageProjectDelete))
	mux.HandleFunc("POST /admin/pages/{id}/project/validate", s.requireAuth(s.adminPageProjectValidate))
	mux.HandleFunc("GET /admin/pages/{id}/project/preview", s.requireAuth(s.adminPageProjectPreview))
	mux.HandleFunc("POST /admin/pages/{id}/project/publish", s.requireAuth(s.adminPageProjectPublish))
	mux.HandleFunc("POST /admin/pages/{id}/project/restore", s.requireAuth(s.adminPageProjectRestore))
	mux.HandleFunc("POST /admin/pages/{id}/project/rollback", s.requireAuth(s.adminPageProjectRollback))
	mux.HandleFunc("POST /admin/pages/{id}/project/app-package", s.requireAuth(s.adminPageAppUpload))
	mux.HandleFunc("POST /admin/pages/{id}/project/app-build", s.requireAuth(s.adminPageAppBuild))
	mux.HandleFunc("GET /admin/pages/{id}/project/app-preview", s.requireAuth(s.adminPageAppPreview))
	mux.HandleFunc("GET /admin/pages/{id}/project/app-file", s.requireAuth(s.adminPageAppSourceFile))
	mux.HandleFunc("POST /admin/pages/{id}/project/app-file", s.requireAuth(s.adminPageAppSourceFileEdit))
	mux.HandleFunc("POST /admin/pages/{id}/project/app-capability", s.requireAuth(s.adminPageAppCapability))
}

func (s *Server) adminPageRows(
	pages []*store.Post,
	q string,
	modeFilter string,
	statusFilter string,
	originFilter string,
	buildFilter string,
) ([]adminPageListRow, error) {
	projects, err := s.store.ListPageProjects()
	if err != nil {
		return nil, err
	}
	byPost := make(map[int64]*store.PageProject, len(projects))
	for _, project := range projects {
		if project != nil {
			byPost[project.PostID] = project
		}
	}
	q = strings.ToLower(strings.TrimSpace(q))
	rows := make([]adminPageListRow, 0, len(pages))
	for _, page := range pages {
		if page == nil {
			continue
		}
		project := byPost[page.ID]
		mode := "standard"
		if project != nil {
			mode = project.Mode
		}
		if modeFilter != "" && modeFilter != "all" && mode != modeFilter {
			continue
		}
		if statusFilter != "" && statusFilter != "all" {
			if page.Status != statusFilter {
				continue
			}
		}
		if buildFilter != "" && buildFilter != "all" {
			if project == nil || project.BuildStatus != buildFilter {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(page.Title), q) &&
			!strings.Contains(strings.ToLower(page.Slug), q) {
			continue
		}
		row := adminPageListRow{
			Post:        page,
			Project:     project,
			Mode:        mode,
			ModeLabel:   adminPageModeLabel(mode),
			StatusLabel: adminPageStatusLabel(page.Status, project),
			Origin:      store.PageOriginAdmin,
			OriginLabel: adminPageOriginLabel(store.PageOriginAdmin),
			DataSummary: "静态内容",
			EditURL:     fmt.Sprintf("/admin/pages/%d/edit", page.ID),
		}
		if project != nil {
			row.EditURL = fmt.Sprintf("/admin/pages/%d/project", page.ID)
			revision, readErr := s.store.GetPageProjectRevision(project.WorkingRevisionID)
			if readErr != nil {
				return nil, readErr
			}
			if revision != nil {
				row.Origin = revision.Origin
				row.OriginLabel = adminPageOriginLabel(revision.Origin)
				row.DataSummary = adminPageRevisionSummary(revision)
			}
		}
		if originFilter != "" && originFilter != "all" && row.Origin != originFilter {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func adminPageModeLabel(mode string) string {
	switch mode {
	case store.PageModeComposition:
		return "自由编排"
	case store.PageModeApp:
		return "互动应用"
	default:
		return "标准页面"
	}
}

type adminCompositionComponentCopy struct {
	Label       string
	Description string
	Keywords    []string
}

var adminCompositionComponentCopies = map[string]adminCompositionComponentCopy{
	"hero.centered": {
		Label:       "顶部介绍",
		Description: "用醒目的标题、简介和按钮介绍页面核心内容。",
		Keywords:    []string{"首屏", "标题", "宣传", "介绍"},
	},
	"hero.split": {
		Label:       "图文宣传区",
		Description: "并排展示文案与图片，适合产品或活动主视觉。",
		Keywords:    []string{"首屏", "图文", "宣传", "产品"},
	},
	"text.rich": {
		Label:       "正文内容",
		Description: "添加支持基础排版的长文、说明或详细介绍。",
		Keywords:    []string{"文字", "正文", "说明", "Markdown"},
	},
	"media.image": {
		Label:       "图片",
		Description: "展示一张带替代文本和可选说明的图片。",
		Keywords:    []string{"图片", "照片", "配图", "媒体"},
	},
	"features.grid": {
		Label:       "功能亮点",
		Description: "用多张卡片概括产品能力、优势或服务特色。",
		Keywords:    []string{"功能", "卖点", "优势", "卡片"},
	},
	"content.cards": {
		Label:       "内容卡片",
		Description: "从当前站点选择内容并自动生成响应式卡片列表。",
		Keywords:    []string{"内容", "卡片", "列表", "动态数据"},
	},
	"posts.grid": {
		Label:       "文章列表",
		Description: "自动展示站点中已发布的文章，可控制排序与数量。",
		Keywords:    []string{"文章", "博客", "新闻", "动态数据"},
	},
	"products.grid": {
		Label:       "商品列表",
		Description: "自动展示商品信息，适合商城入口或推荐区域。",
		Keywords:    []string{"商品", "产品", "商城", "动态数据"},
	},
	"custom_content.grid": {
		Label:       "自定义内容列表",
		Description: "从启用的自定义内容类型中选择数据并生成列表。",
		Keywords:    []string{"自定义内容", "列表", "数据", "卡片"},
	},
	"faq.accordion": {
		Label:       "常见问题",
		Description: "用可展开的问题与答案帮助访客快速了解重点。",
		Keywords:    []string{"FAQ", "问答", "帮助", "折叠"},
	},
	"cta.banner": {
		Label:       "下一步引导",
		Description: "用标题、说明和跳转按钮，引导访客前往重要页面或完成下一步。",
		Keywords:    []string{"按钮", "转化", "行动号召", "横幅"},
	},
	"form.contact": {
		Label:       "联系表单",
		Description: "收集访客提交的信息，并支持明确的隐私同意。",
		Keywords:    []string{"表单", "联系", "留言", "提交"},
	},
	"layout.section": {
		Label:       "区块容器",
		Description: "把多个内容区块组合在一起，统一管理布局。",
		Keywords:    []string{"容器", "分组", "区块", "布局"},
	},
	"layout.columns": {
		Label:       "分栏布局",
		Description: "把内容排列为多栏，并自动适配平板和手机。",
		Keywords:    []string{"分栏", "多列", "布局", "响应式"},
	},
}

var adminCompositionComponentCategories = []adminCompositionComponentCategoryView{
	{Key: "marketing", Label: "宣传与转化", Description: "介绍页面重点并引导访客采取行动。"},
	{Key: "content", Label: "基础内容", Description: "添加文字、图片等静态页面内容。"},
	{Key: "data", Label: "动态内容", Description: "读取站点中的文章、商品或自定义内容。"},
	{Key: "interaction", Label: "互动与表单", Description: "收集访客输入或提供互动能力。"},
	{Key: "layout", Label: "页面布局", Description: "组织区块的分组、层级与响应式排列。"},
}

var adminCompositionPropertyLabels = map[string]string{
	"eyebrow":          "辅助标题",
	"title":            "标题",
	"description":      "描述",
	"primary_action":   "主要按钮",
	"secondary_action": "次要按钮",
	"alignment":        "内容对齐",
	"body":             "正文",
	"alt":              "图片替代文本",
	"caption":          "图片说明",
	"items":            "内容项",
	"empty_state":      "无内容时的提示",
	"show_excerpt":     "显示摘要",
	"action":           "跳转按钮",
	"fields":           "表单字段",
	"submit_label":     "提交按钮文字",
	"privacy_label":    "隐私同意文字",
	"privacy_href":     "隐私政策链接",
	"label":            "区块名称",
}

func adminCompositionComponentCatalog() []adminCompositionComponentView {
	definitions := CompositionComponentRegistry()
	categoryLabels := make(map[string]string, len(adminCompositionComponentCategories))
	categoryOrder := make(map[string]int, len(adminCompositionComponentCategories))
	for index, category := range adminCompositionComponentCategories {
		categoryLabels[category.Key] = category.Label
		categoryOrder[category.Key] = index
	}
	out := make([]adminCompositionComponentView, 0, len(definitions))
	for _, definition := range definitions {
		copy := adminCompositionComponentCopies[definition.Type]
		label := copy.Label
		if label == "" {
			label = definition.Type
		}
		propertyLabels := make(map[string]string, len(definition.Properties))
		for _, property := range definition.Properties {
			if propertyLabel := adminCompositionPropertyLabels[property.Key]; propertyLabel != "" {
				propertyLabels[property.Key] = propertyLabel
			}
		}
		out = append(out, adminCompositionComponentView{
			CompositionComponentDefinition: definition,
			Label:                          label,
			Description:                    copy.Description,
			Category:                       definition.Group,
			CategoryLabel:                  categoryLabels[definition.Group],
			Keywords:                       append([]string(nil), copy.Keywords...),
			PropertyLabels:                 propertyLabels,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		leftOrder, leftOK := categoryOrder[out[i].Category]
		rightOrder, rightOK := categoryOrder[out[j].Category]
		if !leftOK {
			leftOrder = len(categoryOrder)
		}
		if !rightOK {
			rightOrder = len(categoryOrder)
		}
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func adminPageEditorMode(r *http.Request, projectMode string) string {
	if projectMode != store.PageModeComposition {
		return adminPageEditorAdvanced
	}
	if r != nil && strings.TrimSpace(r.URL.Query().Get("editor")) == adminPageEditorAdvanced {
		return adminPageEditorAdvanced
	}
	return adminPageEditorSimple
}

func adminPageEditorURL(pageID int64, mode string) string {
	if pageID <= 0 {
		return ""
	}
	if mode != adminPageEditorAdvanced {
		mode = adminPageEditorSimple
	}
	return fmt.Sprintf("/admin/pages/%d/project?editor=%s", pageID, mode)
}

func adminPageOriginLabel(origin string) string {
	switch origin {
	case store.PageOriginPilot:
		return "Pilot"
	case store.PageOriginAPI:
		return "API"
	case store.PageOriginRestore:
		return "版本恢复"
	default:
		return "后台人工"
	}
}

func adminPageStatusLabel(status string, project *store.PageProject) string {
	if project != nil {
		switch project.BuildStatus {
		case store.PageProjectBuildValidating:
			return "构建中"
		case store.PageProjectBuildFailed:
			return "构建失败"
		}
		if status == "published" && project.PublishedRevisionID != project.WorkingRevisionID {
			return "有未发布修改"
		}
	}
	switch status {
	case "published":
		return "已发布"
	case "scheduled":
		return "定时发布"
	default:
		return "草稿"
	}
}

func adminPageRevisionSummary(revision *store.PageProjectRevision) string {
	if revision == nil {
		return "暂无修订"
	}
	if revision.RevisionKind == store.PageRevisionApp {
		if revision.SourceBundleRef == "" {
			return "应用工程待导入"
		}
		return "不可变应用工程"
	}
	if revision.RevisionKind != store.PageRevisionComposition {
		return "标准内容基线"
	}
	manifest, _, _, err := NormalizeCompositionManifest([]byte(revision.ManifestJSON))
	if err != nil || manifest == nil {
		return "结构需要修复"
	}
	sections, bindings := 0, 0
	walkCompositionSections(manifest.Sections, func(section *CompositionSection, _ string) {
		sections++
		if section.Binding != nil {
			bindings++
		}
	})
	if bindings == 0 {
		return fmt.Sprintf("%d 个组件 · 静态内容", sections)
	}
	return fmt.Sprintf("%d 个组件 · %d 个数据绑定", sections, bindings)
}

func (s *Server) showAdminPageNew(w http.ResponseWriter, r *http.Request, sess session, mode string, status int, formErr string) {
	v := s.adminView(r, "新建页面")
	s.authed(v, sess)
	v.EditLang = s.editLang(r)
	v.FormErr = formErr
	v.Edit = &store.Post{
		Type:     "page",
		Lang:     v.EditLang,
		Title:    strings.TrimSpace(r.FormValue("title")),
		Slug:     strings.TrimSpace(r.FormValue("slug")),
		Excerpt:  strings.TrimSpace(r.FormValue("excerpt")),
		MetaDesc: strings.TrimSpace(r.FormValue("meta_desc")),
		Author:   strings.TrimSpace(r.FormValue("author")),
		Status:   "draft",
	}
	v.SEO.Title = "新建页面 — " + v.Site.Name + " " + v.Admin.T("admin.brand.suffix", "后台")
	mode = strings.TrimSpace(mode)
	shellMode := strings.TrimSpace(r.FormValue("shell_mode"))
	if shellMode == "" {
		shellMode = store.PageShellSite
	}
	s.rnd.Admin(w, "page_new", status, &adminPageNewView{
		View:      v,
		Mode:      mode,
		ModeLabel: adminPageModeLabel(mode),
		ShellMode: shellMode,
	})
}

func (s *Server) adminPageProjectCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	mode := strings.TrimSpace(r.FormValue("mode"))
	if mode != store.PageModeComposition && mode != store.PageModeApp {
		s.showAdminPageNew(w, r, sess, mode, http.StatusBadRequest, "页面类型无效。")
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		s.showAdminPageNew(w, r, sess, mode, http.StatusBadRequest, "标题不能为空。")
		return
	}
	lang := s.editLang(r)
	slug := slugify(strings.TrimSpace(r.FormValue("slug")))
	if slug == "" {
		slug = slugify(title)
	}
	if slug == "" {
		slug = "page-" + strconv.FormatInt(time.Now().Unix(), 36)
	}
	slug, err := s.uniquePageSlug(lang, slug, 0)
	if err != nil {
		s.serverError(w, err)
		return
	}
	shellMode := strings.TrimSpace(r.FormValue("shell_mode"))
	if shellMode != store.PageShellMinimal && shellMode != store.PageShellNone {
		shellMode = store.PageShellSite
	}
	page := &store.Post{
		Type:       "page",
		Slug:       slug,
		Title:      title,
		Excerpt:    strings.TrimSpace(r.FormValue("excerpt")),
		MetaDesc:   strings.TrimSpace(r.FormValue("meta_desc")),
		Author:     strings.TrimSpace(r.FormValue("author")),
		Status:     "draft",
		EditorMode: "markdown",
		Lang:       lang,
	}
	postID, err := s.store.CreatePost(page)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = s.store.DeletePost(postID)
		}
	}()
	page.ID = postID
	project, err := s.store.CreatePageProject(store.CreatePageProjectInput{
		PostID:        postID,
		Mode:          mode,
		SchemaVersion: 1,
		ShellMode:     shellMode,
		CreatedBy:     store.PageOriginAdmin,
	})
	if err != nil {
		s.serverError(w, err)
		return
	}
	metaJSON, err := store.PageRevisionMetaFromPost(page).CanonicalJSON()
	if err != nil {
		s.serverError(w, err)
		return
	}
	manifestJSON := "{}"
	revisionKind := store.PageRevisionApp
	validationJSON := `{"diagnostics":[],"valid":false}`
	if mode == store.PageModeComposition {
		revisionKind = store.PageRevisionComposition
		raw, marshalErr := json.Marshal(newAdminCompositionManifest(title, page.Excerpt, shellMode))
		if marshalErr != nil {
			s.serverError(w, marshalErr)
			return
		}
		validation := s.NormalizeAndValidateCompositionManifest(raw, lang)
		if !validation.Valid {
			s.serverError(w, &CompositionValidationError{Diagnostics: validation.Diagnostics})
			return
		}
		manifestJSON = validation.CanonicalJSON
		validationJSON = adminCompositionValidationJSON(validation.Diagnostics, true)
	}
	_, _, err = s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID:      project.ID,
		BaseRevisionID: 0,
		RevisionKind:   revisionKind,
		PageMetaJSON:   metaJSON,
		ManifestJSON:   manifestJSON,
		Origin:         store.PageOriginAdmin,
		ActorID:        sess.user,
		RequestID:      adminPageRequestID("create", project.ID),
		Summary:        "在后台创建高级页面",
		ValidationJSON: validationJSON,
	})
	if err != nil {
		s.serverError(w, err)
		return
	}
	cleanup = false
	s.invalidatePageProjectDraft()
	http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/project?created=1", postID), http.StatusSeeOther)
}

func newAdminCompositionManifest(title, excerpt, shellMode string) *CompositionManifest {
	props, _ := json.Marshal(compositionHeroProps{
		Title:       title,
		Description: excerpt,
		Alignment:   "start",
	})
	return &CompositionManifest{
		SchemaVersion: CompositionManifestVersion,
		Mode:          store.PageModeComposition,
		Shell:         CompositionShell{Mode: shellMode},
		Theme:         CompositionTheme{Inherit: true, Tokens: map[string]string{}},
		Layout:        CompositionLayout{ContentMaxWidth: "wide", SectionGap: "comfortable"},
		Sections: []CompositionSection{{
			ID:    "hero",
			Type:  "hero.centered",
			Props: props,
		}},
	}
}

var adminPageRequestSequence atomic.Uint64

func adminPageRequestID(action string, projectID int64) string {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err == nil {
		return fmt.Sprintf("admin:%s:%d:%s", action, projectID,
			base64.RawURLEncoding.EncodeToString(nonce[:]))
	}
	return fmt.Sprintf("admin:%s:%d:%d-%d", action, projectID,
		time.Now().UnixNano(), adminPageRequestSequence.Add(1))
}

func adminCompositionValidationJSON(diagnostics []CompositionDiagnostic, valid bool) string {
	value := struct {
		Valid       bool                    `json:"valid"`
		Diagnostics []CompositionDiagnostic `json:"diagnostics"`
	}{
		Valid:       valid,
		Diagnostics: diagnostics,
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func (s *Server) adminPageProjectByPost(w http.ResponseWriter, r *http.Request) (*store.Post, *store.PageProject, bool) {
	postID := atoi64(r.PathValue("id"))
	page, err := s.store.GetPostByID(postID)
	if err != nil {
		s.serverError(w, err)
		return nil, nil, false
	}
	if page == nil || page.Type != "page" {
		s.notFound(w, r)
		return nil, nil, false
	}
	project, err := s.store.GetPageProjectByPostID(postID)
	if err != nil {
		s.serverError(w, err)
		return nil, nil, false
	}
	if project == nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/edit", postID), http.StatusSeeOther)
		return nil, nil, false
	}
	return page, project, true
}

func (s *Server) adminPageProjectEdit(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	s.showAdminPageProject(w, r, sess, page, project, http.StatusOK, nil, false, "")
}

func (s *Server) showAdminPageProject(
	w http.ResponseWriter,
	r *http.Request,
	sess session,
	page *store.Post,
	project *store.PageProject,
	status int,
	diagnostics []CompositionDiagnostic,
	conflict bool,
	conflictETag string,
) {
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || revision == nil {
		if err == nil {
			err = store.ErrPageRevisionNotFound
		}
		s.serverError(w, err)
		return
	}
	revisions, err := s.store.ListPageProjectRevisions(project.ID, 100)
	if err != nil {
		s.serverError(w, err)
		return
	}
	publications, err := s.store.ListPagePublications(project.ID, 30)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pretty := revision.ManifestJSON
	meta := store.PageRevisionMetaFromPost(page)
	_ = json.Unmarshal([]byte(revision.PageMetaJSON), &meta)
	var indented any
	if json.Unmarshal([]byte(revision.ManifestJSON), &indented) == nil {
		if raw, marshalErr := json.MarshalIndent(indented, "", "  "); marshalErr == nil {
			pretty = string(raw)
		}
	}
	v := s.adminView(r, "编辑高级页面")
	s.authed(v, sess)
	v.EditLang = page.Lang
	v.SEO.Title = "编辑高级页面 — " + page.Title
	v.Flash = ""
	if r.URL.Query().Get("created") == "1" {
		v.Flash = "高级页面草稿已创建。"
	}
	if r.URL.Query().Get("saved") == "1" {
		v.Flash = "已保存为新的不可变修订。"
	}
	if r.URL.Query().Get("published") == "1" {
		v.Flash = "页面已发布。"
	}
	if r.URL.Query().Get("restored") == "1" {
		v.Flash = "所选历史版本已恢复为新的工作修订。"
	}
	if r.URL.Query().Get("rolled_back") == "1" {
		v.Flash = "线上版本已回滚。"
	}
	if r.URL.Query().Get("app_uploaded") == "1" {
		v.Flash = "互动应用包已校验并保存为新的不可变修订。"
	}
	if r.URL.Query().Get("app_source_saved") == "1" {
		v.Flash = "源码已整包重新校验，并保存为新的不可变修订。"
	}
	if r.URL.Query().Get("capability_saved") == "1" {
		v.Flash = "运行能力授权已更新。"
	}
	editorMode := adminPageEditorMode(r, project.Mode)
	simpleEditorURL := ""
	if project.Mode == store.PageModeComposition {
		simpleEditorURL = adminPageEditorURL(page.ID, adminPageEditorSimple)
	}
	view := &adminPageProjectView{
		View:                    v,
		PagePost:                page,
		PageProject:             project,
		PageRevision:            revision,
		PageMeta:                meta,
		PageRevisions:           revisions,
		PagePublications:        publications,
		PageComponents:          CompositionComponentRegistry(),
		PageComponentCatalog:    adminCompositionComponentCatalog(),
		PageComponentCategories: append([]adminCompositionComponentCategoryView(nil), adminCompositionComponentCategories...),
		PageManifestJSON:        revision.ManifestJSON,
		PageManifestPretty:      pretty,
		PageMetaJSON:            revision.PageMetaJSON,
		PageETag:                project.ETag(),
		PageModeLabel:           adminPageModeLabel(project.Mode),
		PageOriginLabel:         adminPageOriginLabel(revision.Origin),
		PageSaved:               r.URL.Query().Get("saved") == "1",
		PagePublished:           project.PublishedRevisionID == revision.ID,
		PageDiagnostics:         diagnostics,
		PageConflict:            conflict,
		PageConflictETag:        conflictETag,
		PageCanCompose:          project.Mode == store.PageModeComposition,
		PageCanPublish:          project.Mode == store.PageModeComposition || project.Mode == store.PageModeApp,
		PageEditorMode:          editorMode,
		PageEditorSimple:        editorMode == adminPageEditorSimple,
		PageEditorAdvanced:      editorMode == adminPageEditorAdvanced,
		PageSimpleEditorURL:     simpleEditorURL,
		PageAdvancedEditorURL:   adminPageEditorURL(page.ID, adminPageEditorAdvanced),
	}
	if project.Mode == store.PageModeComposition {
		_, view.PageDataSources = s.compositionDataSourceCatalog(page.Lang)
	}
	if project.Mode == store.PageModeApp {
		view.PageAppHasSource = revision.SourceBundleRef != "" && revision.SourceHash != ""
		if view.PageAppHasSource {
			files, filesErr := s.pageAppAdminSourceFiles(project.ID, revision.ID)
			if filesErr != nil {
				s.serverError(w, filesErr)
				return
			}
			view.PageAppFiles = files
			editFile := strings.TrimSpace(r.URL.Query().Get("edit_file"))
			if editFile != "" {
				raw, mediaType, fileErr := s.pageAppAdminSourceFile(project.ID, revision.ID, editFile)
				if fileErr != nil {
					s.notFound(w, r)
					return
				}
				clean, validateErr := validatePageAppTextEdit(editFile, raw)
				if validateErr != nil {
					s.notFound(w, r)
					return
				}
				for _, file := range files {
					if file.Path == clean {
						view.PageAppEditFile = clean
						view.PageAppEditContent = string(raw)
						view.PageAppEditSHA256 = file.SHA256
						view.PageAppEditMedia = mediaType
						break
					}
				}
				if view.PageAppEditFile == "" {
					s.notFound(w, r)
					return
				}
			}
		}
		builds, buildsErr := s.store.ListPageBuilds(project.ID, revision.ID, 50)
		if buildsErr != nil {
			s.serverError(w, buildsErr)
			return
		}
		view.PageAppBuilds = builds
		for _, build := range builds {
			if build != nil && build.Status == store.PageBuildReady {
				view.PageAppReadyBuild = build
				break
			}
		}
		grants, grantsErr := s.store.ListPageCapabilityGrants(project.ID)
		if grantsErr != nil {
			s.serverError(w, grantsErr)
			return
		}
		grantByName := make(map[string]*store.PageCapabilityGrant, len(grants))
		for _, grant := range grants {
			if grant != nil {
				grantByName[grant.Capability] = grant
			}
		}
		declared, _ := pageAppManifestCapabilities(revision)
		definitions := pageAppCapabilityDefinitions()
		names := make([]string, 0, len(declared))
		for name := range declared {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			definition, known := definitions[name]
			if !known {
				continue
			}
			item := adminPageAppCapabilityView{
				Name:           name,
				Description:    definition.Description,
				Status:         "not_requested",
				ConfigJSON:     "{}",
				Grantable:      definition.Grantable,
				RequiresBridge: definition.RequiresBridge,
			}
			if grant := grantByName[name]; grant != nil {
				item.Status = grant.Status
				item.ConfigJSON = grant.ConfigJSON
			}
			view.PageAppCapabilities = append(view.PageAppCapabilities, item)
		}
	}
	s.rnd.Admin(w, "page_composer", status, view)
}

func (s *Server) adminPageProjectSave(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeComposition {
		http.Error(w, "此编辑器只保存自由编排页面。", http.StatusConflict)
		return
	}
	if !adminPageETagMatches(project, r.FormValue("_etag")) {
		s.showAdminPageProject(w, r, sess, page, project, http.StatusConflict, nil, true, project.ETag())
		return
	}
	validation := s.NormalizeAndValidateCompositionManifest([]byte(r.FormValue("manifest_json")), page.Lang)
	if !validation.Valid {
		s.showAdminPageProject(w, r, sess, page, project, http.StatusUnprocessableEntity, validation.Diagnostics, false, "")
		return
	}
	metaJSON, err := adminPageMetaJSON(page, r)
	if err != nil {
		s.showAdminPageProject(w, r, sess, page, project, http.StatusBadRequest, []CompositionDiagnostic{{
			Level: "error", Code: "page_meta_invalid", Message: err.Error(),
		}}, false, "")
		return
	}
	revision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID:      project.ID,
		BaseRevisionID: project.WorkingRevisionID,
		RevisionKind:   store.PageRevisionComposition,
		PageMetaJSON:   metaJSON,
		ManifestJSON:   validation.CanonicalJSON,
		Origin:         store.PageOriginAdmin,
		ActorID:        sess.user,
		RequestID:      adminPageRequestID("save", project.ID),
		Summary:        strings.TrimSpace(r.FormValue("summary")),
		ValidationJSON: adminCompositionValidationJSON(validation.Diagnostics, true),
	})
	if err != nil {
		var conflict *store.PageRevisionConflictError
		if errors.As(err, &conflict) {
			current, _ := s.store.GetPageProject(project.ID)
			s.showAdminPageProject(w, r, sess, page, current, http.StatusConflict, nil, true, current.ETag())
			return
		}
		s.serverError(w, err)
		return
	}
	s.invalidatePageProjectDraft()
	redirect := fmt.Sprintf("/admin/pages/%d/project?saved=1&revision=%d", page.ID, revision.ID)
	if adminPageEditorMode(r, project.Mode) == adminPageEditorAdvanced {
		redirect += "&editor=advanced"
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func adminPageETagMatches(project *store.PageProject, supplied string) bool {
	return project != nil && strings.TrimSpace(supplied) != "" &&
		strings.TrimSpace(supplied) == project.ETag()
}

// adminPageProjectDelete is deliberately separate from the legacy page delete
// endpoint. Advanced pages own immutable revisions, builds and private files,
// so deletion requires the current project ETag and is only allowed before the
// project has ever been published.
func (s *Server) adminPageProjectDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if !adminPageETagMatches(project, r.FormValue("_etag")) {
		http.Error(w, "页面工程已被其他操作更新，请刷新列表后重试。", http.StatusConflict)
		return
	}
	if page.Status != "draft" || project.PublishedRevisionID > 0 {
		http.Error(w, "只有从未发布的草稿页面工程可以删除；请先完成下线流程。", http.StatusConflict)
		return
	}
	publications, err := s.store.ListPagePublications(project.ID, 1)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(publications) > 0 {
		http.Error(w, "已有发布或交付记录的页面工程不能直接删除，请先完成下线流程。", http.StatusConflict)
		return
	}
	if err := s.store.DeletePost(page.ID); err != nil {
		s.serverError(w, err)
		return
	}
	if err := removePageProjectPrivateFiles(
		s.store.PageProjectStorageDir(), project.ID,
	); err != nil {
		// The database deletion already committed and remains authoritative.
		// Keep the user flow deterministic while surfacing orphan cleanup for
		// operators instead of incorrectly reporting that the page still exists.
		log.Printf("page project %d private file cleanup: %v", project.ID, err)
	}
	s.invalidatePageProjectDraft()
	http.Redirect(w, r, s.adminListRedirect("/admin/pages", r), http.StatusSeeOther)
}

func removePageProjectPrivateFiles(root string, projectID int64) error {
	root = strings.TrimSpace(root)
	if root == "" || projectID <= 0 {
		return nil
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	projectKey := strconv.FormatInt(projectID, 10)
	targets := []string{
		filepath.Join(rootAbs, projectKey),
		filepath.Join(rootAbs, "sources", projectKey),
		filepath.Join(rootAbs, "artifacts", projectKey),
	}
	var cleanupErr error
	for _, target := range targets {
		rel, relErr := filepath.Rel(rootAbs, target)
		if relErr != nil || rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if relErr == nil {
				relErr = fmt.Errorf("private storage target escapes project root")
			}
			cleanupErr = errors.Join(cleanupErr, relErr)
			continue
		}
		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(target))
	}
	return cleanupErr
}

func adminPageMetaJSON(page *store.Post, r *http.Request) (string, error) {
	if page == nil {
		return "", store.ErrPagePostRequired
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		return "", errors.New("标题不能为空")
	}
	slug := slugify(strings.TrimSpace(r.FormValue("slug")))
	if slug == "" {
		return "", errors.New("Slug 不能为空")
	}
	meta := store.PageRevisionMeta{
		Slug:              slug,
		Title:             title,
		Excerpt:           strings.TrimSpace(r.FormValue("excerpt")),
		MetaDesc:          strings.TrimSpace(r.FormValue("meta_desc")),
		Keywords:          strings.TrimSpace(r.FormValue("keywords")),
		CoverImage:        strings.TrimSpace(r.FormValue("cover_image")),
		Author:            strings.TrimSpace(r.FormValue("author")),
		Lang:              page.Lang,
		TransGroup:        page.TransGroup,
		RobotsOverride:    strings.TrimSpace(r.FormValue("robots_override")),
		CanonicalOverride: strings.TrimSpace(r.FormValue("canonical_override")),
	}
	return meta.CanonicalJSON()
}

func (s *Server) adminPageProjectValidate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeComposition {
		writeJSON(w, http.StatusConflict, map[string]any{"valid": false, "message": "仅自由编排页面支持此校验。"})
		return
	}
	if !adminPageETagMatches(project, r.FormValue("_etag")) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"valid": false, "error": "revision_conflict", "current_etag": project.ETag(),
		})
		return
	}
	result := s.NormalizeAndValidateCompositionManifest([]byte(r.FormValue("manifest_json")), page.Lang)
	status := http.StatusOK
	if !result.Valid {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{
		"valid": result.Valid, "diagnostics": result.Diagnostics,
		"canonical_manifest": result.CanonicalJSON, "manifest_hash": result.ManifestHash,
	})
}

// adminPageProjectPreview is intentionally bound to an immutable revision and
// uses the same server renderer as the public/static adapters.
func (s *Server) adminPageProjectPreview(w http.ResponseWriter, r *http.Request) {
	_, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeComposition {
		http.Error(w, "此页面类型请使用对应的隔离预览。", http.StatusConflict)
		return
	}
	revisionID := atoi64(r.URL.Query().Get("revision"))
	if revisionID == 0 {
		revisionID = project.WorkingRevisionID
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if revision == nil || revision.ProjectID != project.ID {
		s.notFound(w, r)
		return
	}
	rendered, err := s.RenderCompositionRevision(
		r, project, revision, true, CompositionBindingPublishedOnly,
	)
	if err != nil {
		http.Error(w, "页面预览校验失败："+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.WriteCompositionPage(w, rendered, http.StatusOK)
}

func (s *Server) adminPageAppUpload(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login_required"})
		return
	}
	csrf := r.Header.Get("X-CSRF-Token")
	if subtle.ConstantTimeCompare([]byte(csrf), []byte(sess.csrf)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "bad_csrf"})
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "page_mode_unsupported"})
		return
	}
	if !adminPageETagMatches(project, r.Header.Get("If-Match")) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "revision_conflict", "current_etag": project.ETag(),
		})
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
	base, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || base == nil {
		pageStoreError(w, firstAdminPageError(err, store.ErrPageRevisionNotFound))
		return
	}
	sourceRef, err := persistPageAppBundle(s.store.PageProjectStorageDir(), project.ID, bundle)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "app_source_store_failed", "message": "保存私有应用源码失败。",
		})
		return
	}
	manifestRaw, _ := json.Marshal(bundle.Manifest)
	manifestJSON, _, err := store.CanonicalJSONHash(string(manifestRaw))
	if err != nil {
		pageStoreError(w, err)
		return
	}
	validationRaw, _ := json.Marshal(map[string]any{
		"valid": true, "runtime": pageAppRuntimeVersion,
		"source_hash": bundle.Hash, "files": len(bundle.Files), "bytes": bundle.TotalBytes,
	})
	revision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID:       project.ID,
		BaseRevisionID:  project.WorkingRevisionID,
		RevisionKind:    store.PageRevisionApp,
		PageMetaJSON:    base.PageMetaJSON,
		ManifestJSON:    manifestJSON,
		SourceBundleRef: sourceRef,
		SourceHash:      bundle.Hash,
		Origin:          store.PageOriginAdmin,
		ActorID:         sess.user,
		RequestID:       adminPageRequestID("app-upload", project.ID),
		Summary:         "后台上传互动应用包",
		ValidationJSON:  string(validationRaw),
	})
	if err != nil {
		pageStoreError(w, err)
		return
	}
	project, _ = s.store.GetPageProject(project.ID)
	s.invalidatePageProjectDraft()
	w.Header().Set("ETag", project.ETag())
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "revision_id": revision.ID, "file_count": len(bundle.Files),
		"source_hash": bundle.Hash, "redirect": fmt.Sprintf("/admin/pages/%d/project?app_uploaded=1", page.ID),
	})
}

func (s *Server) adminPageAppBuild(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	_, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "page_mode_unsupported"})
		return
	}
	if !adminPageETagMatches(project, r.FormValue("_etag")) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "revision_conflict", "current_etag": project.ETag(),
		})
		return
	}
	payload, _ := json.Marshal(pageRevisionTargetInput{RevisionID: project.WorkingRevisionID})
	r.Body = io.NopCloser(strings.NewReader(string(payload)))
	r.Header.Set("Content-Type", "application/json")
	s.createPageAppBuild(w, r, project, adminPageRequestID("app-build", project.ID))
}

func (s *Server) adminPageAppPreview(w http.ResponseWriter, r *http.Request) {
	_, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		http.Error(w, "目标不是互动应用。", http.StatusConflict)
		return
	}
	revisionID := atoi64(r.URL.Query().Get("revision"))
	if revisionID == 0 {
		revisionID = project.WorkingRevisionID
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil || revision == nil || revision.ProjectID != project.ID {
		s.notFound(w, r)
		return
	}
	build, err := s.pageAppReadyBuild(project.ID, revision.ID, atoi64(r.URL.Query().Get("build")))
	if err != nil {
		http.Error(w, "应用尚无通过校验的构建产物。", http.StatusConflict)
		return
	}
	expires := time.Now().Add(pageAppRuntimeTokenTTL)
	_, token, err := s.newPageAppRuntimeClaims(pageAppPreviewShellAudience, project, revision, build, expires)
	if err != nil {
		s.serverError(w, err)
		return
	}
	previewPath := fmt.Sprintf("/preview/page-apps/%d/%d?build=%d&token=%s",
		project.ID, revision.ID, build.ID, url.QueryEscape(token))
	if s.platformSiteID > 0 {
		previewPath = fmt.Sprintf("/preview/sites/%d/page-apps/%d/%d?build=%d&token=%s",
			s.platformSiteID, project.ID, revision.ID, build.ID, url.QueryEscape(token))
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	http.Redirect(w, r, previewPath, http.StatusTemporaryRedirect)
}

func (s *Server) adminPageAppSourceFile(w http.ResponseWriter, r *http.Request) {
	_, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		s.notFound(w, r)
		return
	}
	revisionID := atoi64(r.URL.Query().Get("revision"))
	if revisionID == 0 {
		revisionID = project.WorkingRevisionID
	}
	name := strings.TrimSpace(r.URL.Query().Get("path"))
	raw, _, err := s.pageAppAdminSourceFile(project.ID, revisionID, name)
	if err != nil {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''`+url.PathEscape(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func parseAdminPageAppSourceEditForm(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		http.Error(w, "源码编辑只接受 application/x-www-form-urlencoded。", http.StatusUnsupportedMediaType)
		return false
	}
	const formOverhead = int64(64 << 10)
	r.Body = http.MaxBytesReader(w, r.Body, pageAppTextEditMaxBytes*8+formOverhead)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "源码编辑请求超过大小限制。", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "源码编辑表单格式错误。", http.StatusBadRequest)
		return false
	}
	required := map[string]bool{
		"_etag": true, "base_revision_id": true,
		"path": true, "content": true,
	}
	optional := map[string]bool{"_csrf": true, "summary": true}
	for name, values := range r.PostForm {
		if !required[name] && !optional[name] {
			http.Error(w, "源码编辑表单包含未知字段。", http.StatusBadRequest)
			return false
		}
		if len(values) != 1 {
			http.Error(w, "源码编辑表单包含重复字段。", http.StatusBadRequest)
			return false
		}
	}
	for name := range required {
		if len(r.PostForm[name]) != 1 {
			http.Error(w, "源码编辑表单缺少必要字段。", http.StatusBadRequest)
			return false
		}
	}
	return true
}

func (s *Server) adminPageAppSourceFileEdit(w http.ResponseWriter, r *http.Request) {
	if !parseAdminPageAppSourceEditForm(w, r) {
		return
	}
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp {
		http.Error(w, "目标不是互动应用。", http.StatusConflict)
		return
	}
	if !adminPageETagMatches(project, r.PostForm.Get("_etag")) {
		http.Error(w, "页面已被其他操作更新，请刷新后再编辑源码。", http.StatusConflict)
		return
	}
	baseRevisionID := atoi64(r.PostForm.Get("base_revision_id"))
	if baseRevisionID <= 0 || baseRevisionID != project.WorkingRevisionID {
		http.Error(w, "源码基础版本已变化，请刷新后再编辑。", http.StatusConflict)
		return
	}
	base, err := s.store.GetPageProjectRevision(baseRevisionID)
	if err != nil || base == nil || base.ProjectID != project.ID {
		s.notFound(w, r)
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("path"))
	content := []byte(r.PostForm.Get("content"))
	revision, _, _, err := s.createPageAppTextRevision(
		project, base, name, content,
		store.PageOriginAdmin, sess.user, "",
		adminPageRequestID("app-source-edit", project.ID),
		strings.TrimSpace(r.PostForm.Get("summary")),
	)
	if err != nil {
		var invalid *pageAppValidationError
		if errors.As(err, &invalid) {
			http.Error(w, invalid.Error(), http.StatusUnprocessableEntity)
			return
		}
		pageStoreError(w, err)
		return
	}
	s.invalidatePageProjectDraft()
	http.Redirect(w, r, fmt.Sprintf(
		"/admin/pages/%d/project?app_source_saved=1&edit_file=%s&revision=%d#app-source-editor",
		page.ID, url.QueryEscape(name), revision.ID,
	), http.StatusSeeOther)
}

func (s *Server) adminPageAppCapability(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if project.Mode != store.PageModeApp || !adminPageETagMatches(project, r.FormValue("_etag")) {
		http.Error(w, "页面版本已变化或类型不支持。", http.StatusConflict)
		return
	}
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || revision == nil {
		s.serverError(w, firstAdminPageError(err, store.ErrPageRevisionNotFound))
		return
	}
	capability := strings.TrimSpace(r.FormValue("capability"))
	declared, err := pageAppManifestCapabilities(revision)
	if err != nil || !declared[capability] {
		http.Error(w, "应用未声明此运行能力。", http.StatusUnprocessableEntity)
		return
	}
	definition, known := pageAppCapabilityDefinitions()[capability]
	if !known || !definition.Grantable {
		http.Error(w, "此运行能力不能授权。", http.StatusUnprocessableEntity)
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	status := store.PageCapabilityRevoked
	config := strings.TrimSpace(r.FormValue("config"))
	approvedBy := ""
	if action == "approve" {
		status = store.PageCapabilityApproved
		approvedBy = sess.user
		config, err = canonicalPageAppCapabilityConfig(s, capability, json.RawMessage(config))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	} else if existing, _ := s.store.GetPageCapabilityGrant(project.ID, capability); existing != nil {
		config = existing.ConfigJSON
	}
	if config == "" {
		config = "{}"
	}
	if _, err := s.store.UpsertPageCapabilityGrant(store.UpsertPageCapabilityGrantInput{
		ProjectID: project.ID, Capability: capability, ConfigJSON: config,
		Status: status, RequestedBy: sess.user, ApprovedBy: approvedBy,
	}); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/project?capability_saved=1", page.ID), http.StatusSeeOther)
}

func (s *Server) adminPageProjectPublish(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if !adminPageETagMatches(project, r.FormValue("_etag")) {
		s.showAdminPageProject(w, r, sess, page, project, http.StatusConflict, nil, true, project.ETag())
		return
	}
	revision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || revision == nil {
		s.serverError(w, firstAdminPageError(err, store.ErrPageRevisionNotFound))
		return
	}
	var (
		buildID      int64
		snapshotHash string
		diagnostics  []CompositionDiagnostic
	)
	switch project.Mode {
	case store.PageModeComposition:
		buildID, snapshotHash, diagnostics, err = s.prepareAdminCompositionPublication(r.Context(), page, project, revision)
		if err != nil {
			s.showAdminPageProject(w, r, sess, page, project, http.StatusUnprocessableEntity, diagnostics, false, "")
			return
		}
	case store.PageModeApp:
		var build *store.PageBuild
		build, err = s.pageAppReadyBuild(project.ID, revision.ID, 0)
		if err == nil {
			buildID = build.ID
		}
		if err != nil {
			http.Error(w, "应用尚未完成安全构建，不能发布。", http.StatusUnprocessableEntity)
			return
		}
	default:
		http.Error(w, "页面类型不支持高级发布。", http.StatusConflict)
		return
	}
	s.pagePublicationMu.Lock()
	_, _, err = s.store.PublishPageProject(store.PublishPageProjectInput{
		ProjectID:                 project.ID,
		RevisionID:                revision.ID,
		BuildID:                   buildID,
		ExpectedWorkingRevisionID: project.WorkingRevisionID,
		Action:                    store.PagePublicationPublish,
		ApprovalID:                "admin-session",
		ActorID:                   sess.user,
		Origin:                    store.PageOriginAdmin,
		RequestID:                 adminPageRequestID("publish", project.ID),
		DataSnapshotHash:          snapshotHash,
		DeliveryStatus:            s.initialPagePublicationDeliveryStatus(),
	})
	s.pagePublicationMu.Unlock()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.invalidatePageProjectPublication()
	if published, _ := s.store.GetPostByID(page.ID); published != nil {
		s.firePublishHooks(r, published)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/project?published=1", page.ID), http.StatusSeeOther)
}

func (s *Server) prepareAdminCompositionPublication(
	ctx context.Context,
	page *store.Post,
	project *store.PageProject,
	revision *store.PageProjectRevision,
) (int64, string, []CompositionDiagnostic, error) {
	if page == nil || project == nil || revision == nil || revision.ProjectID != project.ID ||
		revision.RevisionKind != store.PageRevisionComposition {
		return 0, "", nil, store.ErrPageInvalid
	}
	buildResult, err := s.ValidateCompositionBuild(
		ctx, project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		if buildResult == nil {
			return 0, "", nil, err
		}
		return 0, "", buildResult.Diagnostics, err
	}
	diagnostics := buildResult.Diagnostics
	builds, err := s.store.ListPageBuilds(project.ID, revision.ID, 100)
	if err != nil {
		return 0, "", diagnostics, err
	}
	for _, build := range builds {
		if build != nil && build.Status == store.PageBuildReady &&
			build.RuntimeVersion == compositionRuntimeVersion &&
			build.ArtifactHash == buildResult.RenderHash {
			return build.ID, buildResult.DataSnapshotHash, diagnostics, nil
		}
	}
	rawDiagnostics, _ := json.Marshal(diagnostics)
	now := time.Now()
	build, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID:       project.ID,
		RevisionID:      revision.ID,
		Status:          store.PageBuildReady,
		ArtifactRef:     "composition:ssr/" + buildResult.RenderHash,
		ArtifactHash:    buildResult.RenderHash,
		DiagnosticsJSON: string(rawDiagnostics),
		RuntimeVersion:  compositionRuntimeVersion,
		StartedAt:       now,
		FinishedAt:      now,
	})
	if err != nil {
		return 0, "", diagnostics, err
	}
	return build.ID, buildResult.DataSnapshotHash, diagnostics, nil
}

func (s *Server) adminPageProjectRestore(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if !adminPageETagMatches(project, r.FormValue("_etag")) {
		s.showAdminPageProject(w, r, sess, page, project, http.StatusConflict, nil, true, project.ETag())
		return
	}
	target, err := s.store.GetPageProjectRevision(atoi64(r.FormValue("revision_id")))
	if err != nil || target == nil || target.ProjectID != project.ID ||
		(target.RevisionKind != store.PageRevisionComposition && target.RevisionKind != store.PageRevisionApp) ||
		(target.RevisionKind == store.PageRevisionComposition && project.Mode != store.PageModeComposition) ||
		(target.RevisionKind == store.PageRevisionApp && project.Mode != store.PageModeApp) {
		http.Error(w, "历史修订不存在或不能恢复到此编辑器。", http.StatusBadRequest)
		return
	}
	revision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID:       project.ID,
		BaseRevisionID:  project.WorkingRevisionID,
		RevisionKind:    target.RevisionKind,
		PageMetaJSON:    target.PageMetaJSON,
		ManifestJSON:    target.ManifestJSON,
		StandardContent: target.StandardContent,
		SourceBundleRef: target.SourceBundleRef,
		SourceHash:      target.SourceHash,
		Origin:          store.PageOriginRestore,
		ActorID:         sess.user,
		RequestID:       adminPageRequestID("restore", project.ID),
		Summary:         fmt.Sprintf("从修订 #%d 恢复", target.RevisionNo),
		ValidationJSON:  target.ValidationJSON,
	})
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.invalidatePageProjectDraft()
	http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/project?restored=1&revision=%d", page.ID, revision.ID), http.StatusSeeOther)
}

func (s *Server) adminPageProjectRollback(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	page, project, ok := s.adminPageProjectByPost(w, r)
	if !ok {
		return
	}
	if !adminPageETagMatches(project, r.FormValue("_etag")) {
		s.showAdminPageProject(w, r, sess, page, project, http.StatusConflict, nil, true, project.ETag())
		return
	}
	target, err := s.store.GetPageProjectRevision(atoi64(r.FormValue("revision_id")))
	if err != nil || target == nil || target.ProjectID != project.ID ||
		(target.RevisionKind != store.PageRevisionComposition && target.RevisionKind != store.PageRevisionApp) ||
		(target.RevisionKind == store.PageRevisionComposition && project.Mode != store.PageModeComposition) ||
		(target.RevisionKind == store.PageRevisionApp && project.Mode != store.PageModeApp) {
		http.Error(w, "历史修订不存在或不能回滚。", http.StatusBadRequest)
		return
	}
	var (
		buildID      int64
		snapshotHash string
		diagnostics  []CompositionDiagnostic
	)
	if project.Mode == store.PageModeComposition {
		buildID, snapshotHash, diagnostics, err = s.prepareAdminCompositionPublication(r.Context(), page, project, target)
		if err != nil {
			s.showAdminPageProject(w, r, sess, page, project, http.StatusUnprocessableEntity, diagnostics, false, "")
			return
		}
	} else {
		build, buildErr := s.pageAppReadyBuild(project.ID, target.ID, 0)
		if buildErr != nil {
			http.Error(w, "目标应用修订没有可验证的构建产物，不能回滚。", http.StatusUnprocessableEntity)
			return
		}
		buildID = build.ID
	}
	s.pagePublicationMu.Lock()
	_, _, err = s.store.PublishPageProject(store.PublishPageProjectInput{
		ProjectID:                 project.ID,
		RevisionID:                target.ID,
		BuildID:                   buildID,
		ExpectedWorkingRevisionID: project.WorkingRevisionID,
		Action:                    store.PagePublicationRollback,
		ApprovalID:                "admin-session",
		ActorID:                   sess.user,
		Origin:                    store.PageOriginAdmin,
		RequestID:                 adminPageRequestID("rollback", project.ID),
		DataSnapshotHash:          snapshotHash,
		DeliveryStatus:            s.initialPagePublicationDeliveryStatus(),
	})
	s.pagePublicationMu.Unlock()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.invalidatePageProjectPublication()
	if published, _ := s.store.GetPostByID(page.ID); published != nil {
		s.firePublishHooks(r, published)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/project?rolled_back=1", page.ID), http.StatusSeeOther)
}

func firstAdminPageError(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
