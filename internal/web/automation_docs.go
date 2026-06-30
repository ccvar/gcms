package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"cms.ccvar.com/internal/i18n"
)

type automationSkillFile struct {
	name string
	body string
}

type automationSkillOptions struct {
	apiBase string
	token   string
	name    string
	scopes  string
}

type automationCollection struct {
	path  string
	label string
	kind  string
}

var automationCollections = []automationCollection{
	{path: "posts", label: "文章", kind: "post"},
	{path: "links", label: "链接", kind: "link"},
	{path: "pages", label: "页面", kind: "page"},
}

func (s *Server) apiOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(automationOpenAPISpec(s.absForRequest(r, "/api/admin/v1")))
}

func (s *Server) apiPlatformOpenAPI(w http.ResponseWriter, r *http.Request) {
	siteID := strings.TrimSpace(r.PathValue("siteID"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(automationOpenAPISpec(s.absForPlatformRequest(r, "/api/platform/v1/sites/"+siteID)))
}

func (s *Server) adminDownloadAutomationSkill(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	opts := automationSkillOptions{apiBase: s.automationBaseURL(r, sess.currentSiteID)}
	if r.Method == http.MethodPost {
		if _, ok := s.checkCSRF(w, r); !ok {
			return
		}
		opts.token = strings.TrimSpace(r.FormValue("token"))
		opts.name = strings.TrimSpace(r.FormValue("name"))
		opts.scopes = strings.TrimSpace(r.FormValue("scopes"))
		if opts.token == "" || !strings.HasPrefix(opts.token, "gcms_") {
			http.Error(w, "访问密钥无效", http.StatusBadRequest)
			return
		}
	}
	s.writeAutomationSkillZip(w, opts)
}

func (s *Server) adminDownloadAutomationStarter(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	opts := automationSkillOptions{apiBase: s.automationBaseURL(r, sess.currentSiteID)}
	if r.Method == http.MethodPost {
		if _, ok := s.checkCSRF(w, r); !ok {
			return
		}
		opts.token = strings.TrimSpace(r.FormValue("token"))
		opts.name = strings.TrimSpace(r.FormValue("name"))
		opts.scopes = strings.TrimSpace(r.FormValue("scopes"))
		if opts.token == "" || !strings.HasPrefix(opts.token, "gcms_") {
			http.Error(w, "访问密钥无效", http.StatusBadRequest)
			return
		}
	}
	s.writeAutomationStarterZip(w, opts)
}

func (s *Server) writeAutomationSkillZip(w http.ResponseWriter, opts automationSkillOptions) {
	files, err := automationSkillFiles(opts)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range files {
		h := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		h.SetMode(0o644)
		fw, err := zw.CreateHeader(h)
		if err != nil {
			_ = zw.Close()
			s.serverError(w, err)
			return
		}
		if _, err := fw.Write([]byte(file.body)); err != nil {
			_ = zw.Close()
			s.serverError(w, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="gcms-ai-assistant-kit.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func automationSkillFiles(opts automationSkillOptions) ([]automationSkillFile, error) {
	spec, err := json.MarshalIndent(automationOpenAPISpec(opts.apiBase), "", "  ")
	if err != nil {
		return nil, err
	}
	files := []automationSkillFile{
		{name: "README.md", body: automationKitReadme(opts)},
		{name: "gcms-content-assistant/AI助手说明.md", body: automationAssistantBriefMarkdown(opts)},
		{name: "gcms-content-assistant/SKILL.md", body: automationSkillMarkdown(opts.apiBase)},
		{name: "gcms-content-assistant/agents/openai.yaml", body: automationSkillAgentYAML()},
		{name: "gcms-content-assistant/references/openapi.json", body: string(spec) + "\n"},
		{name: "gcms-content-assistant/scripts/gcms.js", body: automationSkillScript()},
	}
	if opts.token != "" {
		files = append(files, automationSkillFile{name: "gcms-content-assistant/.env", body: automationSkillEnv(opts.apiBase, opts.token)})
	} else {
		files = append(files, automationSkillFile{name: "gcms-content-assistant/.env.example", body: automationSkillEnv(opts.apiBase, "gcms_xxx")})
	}
	return files, nil
}

func (s *Server) writeAutomationStarterZip(w http.ResponseWriter, opts automationSkillOptions) {
	files, err := automationStarterFiles(opts)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range files {
		h := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		h.SetMode(0o644)
		fw, err := zw.CreateHeader(h)
		if err != nil {
			_ = zw.Close()
			s.serverError(w, err)
			return
		}
		if _, err := fw.Write([]byte(file.body)); err != nil {
			_ = zw.Close()
			s.serverError(w, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="gcms-site-starter-kit.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func automationStarterFiles(opts automationSkillOptions) ([]automationSkillFile, error) {
	spec, err := json.MarshalIndent(automationOpenAPISpec(opts.apiBase), "", "  ")
	if err != nil {
		return nil, err
	}
	connection, err := json.MarshalIndent(map[string]any{
		"api_base": opts.apiBase,
		"token":    nonEmpty(opts.token, "gcms_xxx"),
		"scopes":   opts.scopes,
		"note":     "真实密钥只放在本地环境或受信任的 AI 编程工具里，不要发到普通聊天窗口。",
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	files := []automationSkillFile{
		{name: "README.md", body: automationStarterReadme(opts)},
		{name: "gcms-site-starter/给AI的任务说明.md", body: automationStarterBriefMarkdown(opts)},
		{name: "gcms-site-starter/SKILL.md", body: automationStarterSkillMarkdown(opts.apiBase)},
		{name: "gcms-site-starter/新站需求向导.md", body: automationStarterWizardMarkdown()},
		{name: "gcms-site-starter/站点需求模板.md", body: automationStarterRequirementsTemplate()},
		{name: "gcms-site-starter/第一步-让AI出规划.md", body: automationStarterPlanPrompt()},
		{name: "gcms-site-starter/第二步-审核后写入草稿.md", body: automationStarterWritePrompt()},
		{name: "gcms-site-starter/工作流.md", body: automationStarterWorkflowMarkdown()},
		{name: "gcms-site-starter/示例提示词.md", body: automationStarterPromptExamples(opts)},
		{name: "gcms-site-starter/connection.json", body: string(connection) + "\n"},
		{name: "gcms-site-starter/references/openapi.json", body: string(spec) + "\n"},
	}
	if opts.token != "" {
		files = append(files, automationSkillFile{name: "gcms-site-starter/.env", body: automationSkillEnv(opts.apiBase, opts.token)})
	} else {
		files = append(files, automationSkillFile{name: "gcms-site-starter/.env.example", body: automationSkillEnv(opts.apiBase, "gcms_xxx")})
	}
	return files, nil
}

func automationOpenAPISpec(apiBase string) map[string]any {
	paths := map[string]any{
		"/languages": map[string]any{
			"get": automationLanguagesOperation(),
		},
		"/site-profile": map[string]any{
			"get":   automationSiteProfileGetOperation(),
			"patch": automationSiteProfileUpdateOperation(),
		},
		"/navigation": map[string]any{
			"get":   automationNavigationGetOperation(),
			"patch": automationNavigationUpdateOperation(),
		},
		"/media": map[string]any{
			"post": automationMediaUploadOperation(),
		},
	}
	for _, col := range automationCollections {
		if col.path == "posts" || col.path == "links" {
			paths["/"+col.path+"/categories"] = map[string]any{
				"get":  automationCategoryListOperation(col),
				"post": automationCategoryCreateOperation(col),
			}
			paths["/"+col.path+"/categories/all-entry"] = map[string]any{
				"get":   automationCategoryAllEntryGetOperation(col),
				"patch": automationCategoryAllEntryUpdateOperation(col),
			}
			paths["/"+col.path+"/categories/{id}"] = map[string]any{
				"patch": automationCategoryUpdateOperation(col),
			}
		}
		paths["/"+col.path] = map[string]any{
			"get":  automationListOperation(col),
			"post": automationCreateOperation(col),
		}
		paths["/"+col.path+"/{id}"] = map[string]any{
			"get":   automationGetOperation(col),
			"patch": automationUpdateOperation(col),
		}
		if col.path == "posts" || col.path == "links" {
			paths["/"+col.path+"/{id}/preview"] = map[string]any{
				"get": automationPreviewOperation(col),
			}
			paths["/"+col.path+"/{id}/preview-url"] = map[string]any{
				"post": automationPreviewURLOperation(col),
			}
		}
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "GCMS Automation API",
			"version":     "1.0.0",
			"description": "开放语种、站点文案、导航菜单、分类、媒体上传、文章与链接草稿预览，以及文章、链接、页面的自动化接口。GCMS 不调用 AI API，外部 AI 工具或自动化程序使用访问密钥调用这里的接口。",
		},
		"servers": []map[string]string{{"url": apiBase}},
		"security": []map[string][]string{
			{"bearerAuth": []string{}},
			{"apiKeyAuth": []string{}},
		},
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "GCMS Access Token"},
				"apiKeyAuth": map[string]any{"type": "apiKey", "in": "header", "name": "X-GCMS-API-Key"},
			},
			"schemas": automationOpenAPISchemas(),
		},
	}
}

func automationLanguagesOperation() map[string]any {
	return map[string]any{
		"summary":     "列出启用语种",
		"description": "只读接口。用于知道默认语种、启用语种，以及多语种内容更新时需要覆盖哪些语种。",
		"operationId": "listLanguages",
		"tags":        []string{"语种"},
		"responses":   automationResponses("LanguageListResponse"),
	}
}

func automationSiteProfileGetOperation() map[string]any {
	return map[string]any{
		"summary":     "读取站点文案",
		"description": "读取每个启用语种的站点名称、标语、描述、首页 Hero 文案、首页区块标题、页脚说明和默认作者。新站初始化时先读取再覆盖。",
		"operationId": "getSiteProfile",
		"tags":        []string{"站点初始化"},
		"responses":   automationResponses("SiteProfileResponse"),
	}
}

func automationSiteProfileUpdateOperation() map[string]any {
	return map[string]any{
		"summary":     "更新站点文案",
		"description": "按语种更新站点基础文案和首页文案。可传单个语种对象，也可传 items 数组批量更新。默认语种的站点名称不能为空。",
		"operationId": "updateSiteProfile",
		"tags":        []string{"站点初始化"},
		"requestBody": automationJSONBody("SiteProfilePatch"),
		"responses":   automationResponses("SiteProfileResponse"),
	}
}

func automationNavigationGetOperation() map[string]any {
	return map[string]any{
		"summary":     "读取导航菜单",
		"description": "读取前台页眉导航的顺序、URL 和各语种显示文字。",
		"operationId": "getNavigation",
		"tags":        []string{"站点初始化"},
		"responses":   automationResponses("NavigationResponse"),
	}
}

func automationNavigationUpdateOperation() map[string]any {
	return map[string]any{
		"summary":     "更新导航菜单",
		"description": "覆盖保存前台页眉导航。站内路径用 / 开头；外部链接必须使用完整 http://、https:// 或 mailto:。",
		"operationId": "updateNavigation",
		"tags":        []string{"站点初始化"},
		"requestBody": automationJSONBody("NavigationInput"),
		"responses":   automationResponses("NavigationResponse"),
	}
}

func automationMediaUploadOperation() map[string]any {
	return map[string]any{
		"summary":     "上传媒体",
		"description": "接收 multipart/form-data 的 file 字段，上传成功后返回可写入 cover_image 或正文 Markdown 的 URL。大小上限 8MB，支持 jpg、png、gif、webp、svg、ico、avif。",
		"operationId": "uploadMedia",
		"tags":        []string{"媒体"},
		"requestBody": automationMultipartFileBody(),
		"responses":   automationResponses("MediaUploadResponse"),
	}
}

func automationCategoryCreateOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "创建" + col.label + "分类",
		"description": "新站初始化时可按语种创建分类。slug 留空时会根据分类名生成，并自动避开重复。",
		"operationId": "create" + automationOperationSuffix(col.kind+"Category"),
		"tags":        []string{col.label},
		"requestBody": automationJSONBody("CategoryInput"),
		"responses":   automationResponses("CategoryItemResponse"),
	}
}

func automationCategoryUpdateOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "更新" + col.label + "分类",
		"description": "修改分类名称、slug、描述或互译分组。不要删除分类；如不确定分类 ID，先调用分类列表接口。",
		"operationId": "update" + automationOperationSuffix(col.kind+"Category"),
		"tags":        []string{col.label},
		"parameters":  []map[string]any{automationIDParam()},
		"requestBody": automationJSONBody("CategoryInput"),
		"responses":   automationResponses("CategoryItemResponse"),
	}
}

func automationCategoryListOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "列出" + col.label + "分类",
		"description": "只读接口。只返回可写入 category_id 的真实分类。列表页的“全部入口”不是分类，需使用 /" + col.path + "/categories/all-entry 单独读取或更新。",
		"operationId": "list" + automationOperationSuffix(col.kind+"Categories"),
		"tags":        []string{col.label},
		"parameters": []map[string]any{
			{"name": "lang", "in": "query", "schema": map[string]any{"type": "string", "default": "zh"}, "description": "分类语种。传 all 可返回所有语种的分类。"},
		},
		"responses": automationResponses("CategoryListResponse"),
	}
}

func automationCategoryAllEntryGetOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "读取" + col.label + "全部入口",
		"description": col.label + "全部入口用于控制前台总列表页的标题、描述、访问路径和“全部”筛选按钮。它不是可写入 category_id 的真实分类。",
		"operationId": "get" + automationOperationSuffix(col.kind+"CategoryAllEntry"),
		"tags":        []string{col.label},
		"parameters": []map[string]any{
			{"name": "lang", "in": "query", "schema": map[string]any{"type": "string", "default": "zh"}, "description": "入口语种。传 all 可返回所有语种入口。"},
		},
		"responses": automationResponses("CategoryAllEntryResponse"),
	}
}

func automationCategoryAllEntryUpdateOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "更新" + col.label + "全部入口",
		"description": "更新总列表页标题、描述、slug 和“全部”筛选按钮文案。可传单个语种对象，也可传 items 数组批量更新；不要把真实分类写到这里。",
		"operationId": "update" + automationOperationSuffix(col.kind+"CategoryAllEntry"),
		"tags":        []string{col.label},
		"requestBody": automationJSONBody("CategoryAllEntryPatch"),
		"responses":   automationResponses("CategoryAllEntryResponse"),
	}
}

func automationListOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "列出" + col.label,
		"description": "修改某篇内容前，建议先用 q 或 slug 查到准确 id；如果结果相似，应让用户确认。",
		"operationId": "list" + automationOperationSuffix(col.path),
		"tags":        []string{col.label},
		"parameters": []map[string]any{
			{"name": "lang", "in": "query", "schema": map[string]any{"type": "string", "default": "zh"}, "description": "内容语种。传 all 可跨语种查询；传 trans_group 且省略 lang 时默认等同 all。"},
			{"name": "status", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"draft", "published", "scheduled"}}, "description": "按状态筛选"},
			{"name": "q", "in": "query", "schema": map[string]any{"type": "string"}, "description": "按标题、摘要、正文等关键词查找"},
			{"name": "slug", "in": "query", "schema": map[string]any{"type": "string"}, "description": "按 slug 精确查找"},
			{"name": "trans_group", "in": "query", "schema": map[string]any{"type": "string"}, "description": "互译分组。用于查找同一内容的所有语种版本。"},
			{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "default": 20, "minimum": 1, "maximum": 100}},
			{"name": "offset", "in": "query", "schema": map[string]any{"type": "integer", "default": 0, "minimum": 0}},
		},
		"responses": automationResponses("ContentListResponse"),
	}
}

func automationCreateOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "创建" + col.label,
		"description": "默认创建草稿。发布、定时发布或修改已发布内容需要访问密钥拥有对应资源的发布权限。",
		"operationId": "create" + automationOperationSuffix(col.path),
		"tags":        []string{col.label},
		"requestBody": automationJSONBody("ContentInput"),
		"responses":   automationResponses("ContentItemResponse"),
	}
}

func automationGetOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "读取" + col.label,
		"operationId": "get" + automationOperationSuffix(col.path),
		"tags":        []string{col.label},
		"parameters":  []map[string]any{automationIDParam()},
		"responses":   automationResponses("ContentItemResponse"),
	}
}

func automationUpdateOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "更新" + col.label,
		"description": "先查到准确 id 再更新。没有发布权限时，只能修改草稿。",
		"operationId": "update" + automationOperationSuffix(col.path),
		"tags":        []string{col.label},
		"parameters":  []map[string]any{automationIDParam()},
		"requestBody": automationJSONBody("ContentInput"),
		"responses":   automationResponses("ContentItemResponse"),
	}
}

func automationPreviewOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "预览" + col.label + "草稿",
		"description": "读取文章或链接的预览结果，返回内容字段、渲染后的正文 HTML、目录、正式 URL 和短期前台预览 URL。用于发布前复核草稿，权限同读取接口。",
		"operationId": "preview" + automationOperationSuffix(col.path),
		"tags":        []string{col.label},
		"parameters":  []map[string]any{automationIDParam()},
		"responses":   automationResponses("ContentPreviewResponse"),
	}
}

func automationPreviewURLOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "生成" + col.label + "前台预览链接",
		"description": "生成短期有效的签名前台预览 URL。打开后使用真实前台模板渲染草稿，不需要登录后台；页面强制 noindex 且不缓存。",
		"operationId": "create" + automationOperationSuffix(col.path) + "PreviewURL",
		"tags":        []string{col.label},
		"parameters":  []map[string]any{automationIDParam()},
		"responses":   automationResponses("PreviewURLResponse"),
	}
}

func automationOperationSuffix(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func automationIDParam() map[string]any {
	return map[string]any{
		"name": "id", "in": "path", "required": true,
		"schema":      map[string]any{"type": "integer", "minimum": 1},
		"description": "内容 ID。不要只凭标题猜测，先用列表接口查找。",
	}
}

func automationJSONBody(schema string) map[string]any {
	return map[string]any{
		"required": true,
		"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + schema}},
		},
	}
}

func automationMultipartFileBody() map[string]any {
	return map[string]any{
		"required": true,
		"content": map[string]any{
			"multipart/form-data": map[string]any{
				"schema": map[string]any{
					"type":     "object",
					"required": []string{"file"},
					"properties": map[string]any{
						"file": map[string]any{"type": "string", "format": "binary"},
					},
				},
			},
		},
	}
}

func automationResponses(schema string) map[string]any {
	return map[string]any{
		"200": map[string]any{
			"description": "OK",
			"content": map[string]any{
				"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + schema}},
			},
		},
		"201": map[string]any{
			"description": "Created",
			"content": map[string]any{
				"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/" + schema}},
			},
		},
		"400": map[string]any{"description": "请求参数错误", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/APIError"}}}},
		"401": map[string]any{"description": "缺少或无效的访问密钥", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/APIError"}}}},
		"403": map[string]any{"description": "访问权限不足", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/APIError"}}}},
		"404": map[string]any{"description": "内容不存在", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/APIError"}}}},
	}
}

func automationOpenAPISchemas() map[string]any {
	return map[string]any{
		"LanguageListResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"default": map[string]any{"type": "string"},
				"items": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"code":    map[string]any{"type": "string"},
							"name":    map[string]any{"type": "string"},
							"tag":     map[string]any{"type": "string"},
							"default": map[string]any{"type": "boolean"},
						},
					},
				},
			},
		},
		"CategoryListResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/components/schemas/CategoryItem"},
				},
				"lang": map[string]any{"type": "string"},
				"kind": map[string]any{"type": "string", "enum": []string{"post", "link"}},
			},
		},
		"CategoryItem": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "integer"},
				"slug":        map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"lang":        map[string]any{"type": "string"},
				"trans_group": map[string]any{"type": "string"},
				"kind":        map[string]any{"type": "string", "enum": []string{"post", "link"}},
				"count":       map[string]any{"type": "integer", "description": "该分类下已发布内容数量"},
			},
		},
		"CategoryInput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lang":        map[string]any{"type": "string", "description": "分类语种，留空使用默认语种。"},
				"slug":        map[string]any{"type": "string", "description": "留空时由名称自动生成；重复时会自动追加序号。"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"trans_group": map[string]any{"type": "string", "description": "多语种分类的关联分组。"},
			},
		},
		"CategoryItemResponse": map[string]any{
			"type":       "object",
			"properties": map[string]any{"item": map[string]any{"$ref": "#/components/schemas/CategoryItem"}},
		},
		"CategoryAllEntryResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/components/schemas/CategoryAllEntry"},
				},
				"lang": map[string]any{"type": "string"},
				"kind": map[string]any{"type": "string", "enum": []string{"post", "link"}},
			},
		},
		"CategoryAllEntry": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":        map[string]any{"type": "string", "enum": []string{"post", "link"}},
				"lang":        map[string]any{"type": "string"},
				"title":       map[string]any{"type": "string", "description": "前台总列表页标题。"},
				"description": map[string]any{"type": "string", "description": "前台总列表页描述。"},
				"label":       map[string]any{"type": "string", "description": "列表筛选里“全部”按钮的文案。"},
				"slug":        map[string]any{"type": "string", "description": "总列表入口 slug；文章默认 category，链接默认 links。"},
				"path":        map[string]any{"type": "string", "description": "总列表入口路径，可用于导航菜单 URL。"},
				"purpose":     map[string]any{"type": "string", "description": "该入口的作用说明。"},
				"selectable":  map[string]any{"type": "boolean", "description": "始终为 false；不能作为内容的 category_id。"},
			},
		},
		"CategoryAllEntryPatch": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{
					"type":        "array",
					"description": "批量更新多个语种。也可以直接在顶层传入单个语种字段。",
					"items":       map[string]any{"$ref": "#/components/schemas/CategoryAllEntryInput"},
				},
				"lang":        map[string]any{"type": "string", "description": "语种，留空使用默认语种；不能传 all。"},
				"title":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"label":       map[string]any{"type": "string", "description": "“全部”筛选按钮文案。"},
				"slug":        map[string]any{"type": "string", "description": "留空或无效时回到默认 slug。"},
			},
		},
		"CategoryAllEntryInput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lang":        map[string]any{"type": "string", "description": "语种，留空使用默认语种。"},
				"title":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"label":       map[string]any{"type": "string"},
				"slug":        map[string]any{"type": "string"},
			},
		},
		"SiteProfileResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"default": map[string]any{"type": "string", "description": "默认语种。"},
				"items":   map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/SiteProfileItem"}},
			},
		},
		"SiteProfileItem": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lang":                map[string]any{"type": "string"},
				"name":                map[string]any{"type": "string", "description": "站点名称。"},
				"tagline":             map[string]any{"type": "string", "description": "站点标语。"},
				"description":         map[string]any{"type": "string", "description": "站点描述。"},
				"keywords":            map[string]any{"type": "string"},
				"hero_eyebrow":        map[string]any{"type": "string"},
				"hero_title":          map[string]any{"type": "string"},
				"hero_description":    map[string]any{"type": "string"},
				"footer_note":         map[string]any{"type": "string"},
				"home_featured_title": map[string]any{"type": "string"},
				"home_links_title":    map[string]any{"type": "string"},
				"home_latest_title":   map[string]any{"type": "string"},
				"default_post_author": map[string]any{"type": "string"},
				"default_link_author": map[string]any{"type": "string"},
				"logo":                map[string]any{"type": "string", "description": "站点 Logo URL。更新需要 brand:assets:write。"},
				"favicon":             map[string]any{"type": "string", "description": "浏览器图标 URL。更新需要 brand:assets:write。"},
				"share_image":         map[string]any{"type": "string", "description": "默认分享图 URL。更新需要 brand:assets:write。"},
			},
		},
		"SiteProfilePatch": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{
					"type":        "array",
					"description": "批量更新多个语种。也可以直接在顶层传入单个语种字段。",
					"items":       map[string]any{"$ref": "#/components/schemas/SiteProfileItem"},
				},
				"lang":                map[string]any{"type": "string"},
				"name":                map[string]any{"type": "string"},
				"tagline":             map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
				"keywords":            map[string]any{"type": "string"},
				"hero_eyebrow":        map[string]any{"type": "string"},
				"hero_title":          map[string]any{"type": "string"},
				"hero_description":    map[string]any{"type": "string"},
				"footer_note":         map[string]any{"type": "string"},
				"home_featured_title": map[string]any{"type": "string"},
				"home_links_title":    map[string]any{"type": "string"},
				"home_latest_title":   map[string]any{"type": "string"},
				"default_post_author": map[string]any{"type": "string"},
				"default_link_author": map[string]any{"type": "string"},
				"logo":                map[string]any{"type": "string", "description": "站点 Logo URL。更新需要 brand:assets:write。"},
				"favicon":             map[string]any{"type": "string", "description": "浏览器图标 URL。更新需要 brand:assets:write。"},
				"share_image":         map[string]any{"type": "string", "description": "默认分享图 URL。更新需要 brand:assets:write。"},
			},
		},
		"NavigationResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"default":   map[string]any{"type": "string"},
				"languages": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"items":     map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/NavigationItem"}},
			},
		},
		"NavigationInput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/NavigationItem"}},
			},
			"required": []string{"items"},
		},
		"NavigationItem": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "站内路径（/ 开头）或完整外部链接。优先使用标准入口：/、/category、/category/{slug}、/links、/links/cat/{slug}、/{page-slug}、/search；不要把已存在的文章分类或链接分类写成自定义页面路径。"},
				"labels": map[string]any{
					"type":                 "object",
					"description":          "各语种菜单文字，例如 {\"zh\":\"首页\",\"en\":\"Home\"}。",
					"additionalProperties": map[string]any{"type": "string"},
				},
			},
			"required": []string{"url", "labels"},
		},
		"MediaUploadResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":  map[string]any{"type": "string", "description": "可用于 cover_image 或 Markdown 图片的公开访问路径。"},
				"name": map[string]any{"type": "string", "description": "保存后的文件名。"},
				"size": map[string]any{"type": "integer", "description": "写入字节数。"},
			},
		},
		"ContentListResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items":       map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/ContentItem"}},
				"limit":       map[string]any{"type": "integer"},
				"offset":      map[string]any{"type": "integer"},
				"lang":        map[string]any{"type": "string"},
				"q":           map[string]any{"type": "string"},
				"slug":        map[string]any{"type": "string"},
				"trans_group": map[string]any{"type": "string"},
			},
		},
		"ContentItemResponse": map[string]any{
			"type":       "object",
			"properties": map[string]any{"item": map[string]any{"$ref": "#/components/schemas/ContentItem"}},
		},
		"ContentPreviewResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"preview": map[string]any{"$ref": "#/components/schemas/ContentPreview"},
			},
		},
		"ContentPreview": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"item":        map[string]any{"$ref": "#/components/schemas/ContentItem"},
				"preview_url": map[string]any{"type": "string", "description": "当前 API 预览接口地址。调用时仍需传入访问密钥。"},
				"frontend_preview_url": map[string]any{
					"type":        "string",
					"description": "短期有效的前台预览地址。可直接在浏览器打开，使用真实前台模板渲染草稿。",
				},
				"frontend_preview_expires_at": map[string]any{"type": "string", "description": "前台预览地址过期时间。"},
				"public_url":                  map[string]any{"type": "string", "description": "内容发布后的正式前台地址。草稿状态下不一定可公开访问。"},
				"content_html":                map[string]any{"type": "string", "description": "正文 Markdown 渲染后的 HTML，便于 AI 或外部工具做发布前复核。"},
				"toc": map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/components/schemas/ContentHeading"},
				},
				"robots": map[string]any{"type": "string", "example": "noindex, nofollow"},
			},
		},
		"PreviewURLResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"preview_url": map[string]any{"type": "string"},
				"expires_at":  map[string]any{"type": "string"},
				"ttl_seconds": map[string]any{"type": "integer"},
			},
		},
		"ContentHeading": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"level": map[string]any{"type": "integer"},
				"id":    map[string]any{"type": "string"},
				"text":  map[string]any{"type": "string"},
			},
		},
		"ContentInput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lang":         map[string]any{"type": "string", "description": "内容语种，如 zh、en"},
				"slug":         map[string]any{"type": "string"},
				"title":        map[string]any{"type": "string"},
				"excerpt":      map[string]any{"type": "string"},
				"content":      map[string]any{"type": "string", "description": "正文 Markdown"},
				"meta_desc":    map[string]any{"type": "string"},
				"keywords":     map[string]any{"type": "string"},
				"cover_image":  map[string]any{"type": "string"},
				"author":       map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string", "enum": []string{"draft", "published", "scheduled"}, "default": "draft"},
				"editor_mode":  map[string]any{"type": "string", "enum": []string{"markdown", "rich"}, "default": "markdown"},
				"link_url":     map[string]any{"type": "string", "description": "链接类型需要的目标 URL"},
				"trans_group":  map[string]any{"type": "string"},
				"category_id":  map[string]any{"type": "integer", "nullable": true},
				"published_at": map[string]any{"type": "string", "format": "date-time"},
			},
		},
		"ContentItem": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":           map[string]any{"type": "integer"},
				"type":         map[string]any{"type": "string", "enum": []string{"post", "page", "link"}},
				"lang":         map[string]any{"type": "string"},
				"slug":         map[string]any{"type": "string"},
				"title":        map[string]any{"type": "string"},
				"excerpt":      map[string]any{"type": "string"},
				"content":      map[string]any{"type": "string"},
				"meta_desc":    map[string]any{"type": "string"},
				"keywords":     map[string]any{"type": "string"},
				"cover_image":  map[string]any{"type": "string"},
				"author":       map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string", "enum": []string{"draft", "published", "scheduled"}},
				"featured":     map[string]any{"type": "boolean"},
				"editor_mode":  map[string]any{"type": "string"},
				"link_url":     map[string]any{"type": "string"},
				"trans_group":  map[string]any{"type": "string"},
				"category_id":  map[string]any{"type": "integer", "nullable": true},
				"category":     map[string]any{"$ref": "#/components/schemas/CategoryItem"},
				"url":          map[string]any{"type": "string"},
				"published_at": map[string]any{"type": "string", "format": "date-time"},
				"created_at":   map[string]any{"type": "string", "format": "date-time"},
				"updated_at":   map[string]any{"type": "string", "format": "date-time"},
			},
		},
		"APIError": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"error":   map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"},
			},
		},
	}
}

func automationScopeLabels(scopes []string) string {
	return strings.Join(automationScopeBadges(strings.Join(scopes, ",")), "；")
}

func automationScopeBadges(scopes string) []string {
	m := apiScopeMap(scopes)
	var out []string
	if m["languages:read"] {
		out = append(out, "语种：读取")
	}
	if m["media:write"] {
		out = append(out, "媒体：上传")
	}
	if m[apiScopeSiteRead] || m[apiScopeSiteWrite] {
		actions := []string{}
		if m[apiScopeSiteRead] {
			actions = append(actions, "读取")
		}
		if m[apiScopeSiteWrite] {
			actions = append(actions, "修改")
		}
		out = append(out, "站点文案："+strings.Join(actions, "、"))
	}
	if m[apiScopeBrandAssetsWrite] {
		out = append(out, "品牌资产：修改")
	}
	if m[apiScopeNavigationRead] || m[apiScopeNavigationWrite] {
		actions := []string{}
		if m[apiScopeNavigationRead] {
			actions = append(actions, "读取")
		}
		if m[apiScopeNavigationWrite] {
			actions = append(actions, "修改")
		}
		out = append(out, "导航菜单："+strings.Join(actions, "、"))
	}
	if labels := automationActionLabels(m, "content"); len(labels) > 0 {
		out = append(out, "全部内容："+strings.Join(labels, "、"))
	}
	for _, col := range automationCollections {
		if labels := automationActionLabels(m, col.path); len(labels) > 0 {
			out = append(out, col.label+"："+strings.Join(labels, "、"))
		}
	}
	if len(out) == 0 {
		return []string{"读取内容"}
	}
	return out
}

func automationActionLabels(scopes map[string]bool, resource string) []string {
	var labels []string
	for _, action := range []struct {
		key   string
		label string
	}{
		{"read", "读取"},
		{"categories", "获取分类"},
		{"categories:write", "写分类"},
		{"write", "写草稿"},
		{"publish", "发布"},
	} {
		if scopes[resource+":"+action.key] {
			labels = append(labels, action.label)
		}
	}
	return labels
}

func automationScopeBadgesAdmin(scopes string, admin *i18n.AdminTr) []string {
	m := apiScopeMap(scopes)
	colon := "："
	sep := "、"
	if admin != nil && admin.Lang.Code != "zh" {
		colon = ": "
		sep = ", "
	}
	var out []string
	if m["languages:read"] {
		out = append(out, adminUI(admin, "admin.settings.automation.languages", "语种")+colon+adminUI(admin, "admin.settings.automation.read", "读取"))
	}
	if m["media:write"] {
		out = append(out, adminUI(admin, "admin.settings.automation.media", "媒体")+colon+adminUI(admin, "admin.settings.automation.media_upload", "上传媒体"))
	}
	if m[apiScopeSiteRead] || m[apiScopeSiteWrite] {
		labels := []string{}
		if m[apiScopeSiteRead] {
			labels = append(labels, adminUI(admin, "admin.settings.automation.read", "读取"))
		}
		if m[apiScopeSiteWrite] {
			labels = append(labels, adminUI(admin, "admin.settings.automation.write", "修改"))
		}
		out = append(out, adminUI(admin, "admin.settings.automation.site_profile", "站点文案")+colon+strings.Join(labels, sep))
	}
	if m[apiScopeBrandAssetsWrite] {
		out = append(out, adminUI(admin, "admin.settings.automation.brand_assets", "品牌资产")+colon+adminUI(admin, "admin.settings.automation.write", "修改"))
	}
	if m[apiScopeNavigationRead] || m[apiScopeNavigationWrite] {
		labels := []string{}
		if m[apiScopeNavigationRead] {
			labels = append(labels, adminUI(admin, "admin.settings.automation.read", "读取"))
		}
		if m[apiScopeNavigationWrite] {
			labels = append(labels, adminUI(admin, "admin.settings.automation.write", "修改"))
		}
		out = append(out, adminUI(admin, "admin.settings.automation.navigation", "导航菜单")+colon+strings.Join(labels, sep))
	}
	if labels := automationActionLabelsAdmin(m, "content", admin); len(labels) > 0 {
		out = append(out, adminUI(admin, "admin.settings.automation.content", "全部内容")+colon+strings.Join(labels, sep))
	}
	for _, col := range automationCollections {
		if labels := automationActionLabelsAdmin(m, col.path, admin); len(labels) > 0 {
			out = append(out, automationCollectionLabelAdmin(col.path, col.label, admin)+colon+strings.Join(labels, sep))
		}
	}
	if len(out) == 0 {
		return []string{adminUI(admin, "admin.settings.automation.read_content", "读取内容")}
	}
	return out
}

func automationCollectionLabelAdmin(path, fallback string, admin *i18n.AdminTr) string {
	switch path {
	case "posts":
		return adminUI(admin, "admin.nav.posts", fallback)
	case "links":
		return adminUI(admin, "admin.nav.links", fallback)
	case "pages":
		return adminUI(admin, "admin.nav.pages", fallback)
	default:
		return fallback
	}
}

func automationActionLabelsAdmin(scopes map[string]bool, resource string, admin *i18n.AdminTr) []string {
	var labels []string
	for _, action := range []struct {
		key      string
		i18nKey  string
		fallback string
	}{
		{"read", "admin.settings.automation.read", "读取"},
		{"categories", "admin.settings.automation.read_categories", "获取分类"},
		{"categories:write", "admin.settings.automation.write_categories", "写分类"},
		{"write", "admin.settings.automation.write_draft", "写草稿"},
		{"publish", "admin.settings.automation.publish", "发布"},
	} {
		if scopes[resource+":"+action.key] {
			labels = append(labels, adminUI(admin, action.i18nKey, action.fallback))
		}
	}
	return labels
}

func automationStarterReadme(opts automationSkillOptions) string {
	lines := []string{
		"# GCMS 新站 AI 技能包",
		"",
		"这个包用于让 Codex、Claude Code、Cursor 等 AI 编程工具帮你准备一个新站的基础内容。GCMS 不调用 AI API，AI 只通过你授权的自动化接口写入站点文案、导航、分类、页面、文章和链接。",
		"",
		"## 包内文件",
		"",
		"- `gcms-site-starter/.env` 或 `.env.example`：本地密钥文件，包含 `GCMS_API_BASE` 和 `GCMS_API_KEY`。",
		"- `gcms-site-starter/给AI的任务说明.md`：交给 AI 读取的边界、流程和写入规则。",
		"- `gcms-site-starter/SKILL.md`：支持 skills 的 AI 工具可读取的技能说明。",
		"- `gcms-site-starter/新站需求向导.md`：给小白用户填写的简化问题，只问网站方向、受众、语种和内容边界。",
		"- `gcms-site-starter/站点需求模板.md`：给用户填写的网站方向、语种、栏目和语气要求。",
		"- `gcms-site-starter/第一步-让AI出规划.md`：只读检查和生成规划的提示词，不会写入。",
		"- `gcms-site-starter/第二步-审核后写入草稿.md`：用户确认规划后，才让 AI 分批写入草稿的提示词。",
		"- `gcms-site-starter/工作流.md`：从需求到写入草稿的标准步骤。",
		"- `gcms-site-starter/示例提示词.md`：可以直接复制给 AI 的提示词。",
		"- `gcms-site-starter/references/openapi.json`：接口描述文件。",
		"",
	}
	if opts.token != "" {
		name := strings.TrimSpace(opts.name)
		if name == "" {
			name = "新站初始化助手"
		}
		lines = append(lines,
			"这个包已经写入访问密钥，只给「"+name+"」使用。",
			"如果这个包或密钥泄露，请回到 GCMS 后台吊销对应的访问权限。",
		)
		if opts.scopes != "" {
			lines = append(lines, "当前权限："+automationScopeLabels(strings.Split(opts.scopes, ",")))
		}
	} else {
		lines = append(lines,
			"这个包不包含访问密钥。请先在 GCMS 后台「设置 -> 自动化接口」创建访问权限，并勾选“新站初始化”相关权限，再把密钥填到 `.env`。",
		)
	}
	lines = append(lines,
		"",
		"## 推荐使用方式",
		"",
		"1. 先填写 `新站需求向导.md`；如果你已经很清楚网站结构，再补充 `站点需求模板.md`。",
		"2. 把整个 `gcms-site-starter` 文件夹交给 AI 工具读取。",
		"3. 先复制 `第一步-让AI出规划.md` 里的提示词，让 AI 只读检查并输出规划，不要写入。",
		"4. 人工审核规划：确认会新增什么、会修改什么、哪些内容保持草稿。",
		"5. 规划确认后，再复制 `第二步-审核后写入草稿.md`，让 AI 分批写入：站点文案 -> 分类 -> 页面 -> 导航 -> 文章/链接。",
		"6. 默认所有文章、页面和链接先保存为草稿；只有明确要求并且密钥有发布权限时才发布。",
		"",
		"接口地址："+opts.apiBase,
		"",
		"## 安全边界",
		"",
		"- 不要把真实密钥发到普通聊天窗口。",
		"- 不允许修改管理员账号、密码、安全设置、系统更新、Cloudflare 配置或 API Key 本身。",
		"- 不允许删除内容。",
		"- 修改导航、站点文案或分类前，必须先读取现有配置。",
		"- 所有通过媒体接口上传的图片资源，上传前必须先转成 WebP（.webp）格式。",
		"- 第一轮只允许生成规划；用户没有明确确认前，不允许写入。",
		"- 写文章时不能只生成几段泛泛短文；每篇文章都要有搜索意图、结构大纲、封面或配图计划。",
		"- 写入后让 AI 汇总每个语种的变更、内容 ID、草稿 URL 或预览方式。",
	)
	return strings.Join(lines, "\n") + "\n"
}

func automationStarterBriefMarkdown(opts automationSkillOptions) string {
	token := nonEmpty(opts.token, "gcms_xxx")
	return strings.Join([]string{
		"# 给 AI 的任务说明",
		"",
		"你是 GCMS 新站初始化助手。你的任务是根据用户提供的网站方向和启用语种，帮助准备一个可上线的基础内容站。",
		"",
		"## 连接方式",
		"",
		"- API Base: `" + opts.apiBase + "`",
		"- API Key: `" + token + "`",
		"- OpenAPI: `references/openapi.json`",
		"- 优先读取 `.env` 或 `connection.json`，不要在普通回复中泄露密钥。",
		"",
		"## 你可以做什么",
		"",
		"- 读取启用语种。",
		"- 读取和更新站点基础文案、Hero 文案、首页分区标题、默认作者。",
		"- 读取和更新导航菜单。",
		"- 读取和更新文章/链接的“全部入口”（列表页标题、描述、入口路径和“全部”筛选按钮）。",
		"- 创建或更新文章分类、链接分类。",
		"- 创建文章、页面和链接草稿。",
		"- 将用户提供的图片先转成 WebP（.webp），再上传并把返回 URL 用于封面或正文。",
		"- 如果密钥包含品牌资产权限，且用户明确要求，可以更新站点 Logo、浏览器图标和分享图。",
		"",
		"## 不能做什么",
		"",
		"- 不要修改安全、系统更新、Cloudflare 部署、评论配置、管理员账号和 API Key。",
		"- 没有明确要求或没有品牌资产权限时，不要替换站点 Logo、浏览器图标和分享图。",
		"- 不要删除任何内容。",
		"- 不要默认发布内容；除非用户明确说“可以发布”，并且当前密钥拥有发布权限。",
		"- 不要把一个语种的正文机械翻译覆盖到其他语种；每个语种都要符合对应读者习惯。",
		"",
		"## 工作原则",
		"",
		"1. 第一轮只能规划，不能写入。即使用户需求很明确，也要先输出规划给用户审核。",
		"2. 先读取 `/languages`，确认默认语种和启用语种。",
		"3. 先读取 `/site-profile`、`/navigation`、`/posts/categories/all-entry?lang=all`、`/links/categories/all-entry?lang=all`、`/posts/categories?lang=all`、`/links/categories?lang=all`，了解当前状态。",
		"4. 规划必须包含：定位、导航、首页文案、文章/链接总入口、真实分类、页面、文章、链接、每个语种的差异、敏感表达风险和写入范围。",
		"5. 用户明确确认后再写入。",
		"6. 写入内容时保持 `status: draft`。",
		"7. 多语种内容使用同一个 `trans_group` 关联。",
		"8. 每次批量写入后报告已创建或更新的 id、slug、语种和状态。",
		"",
		"## 文章质量与配图标准",
		"",
		"- 规划阶段必须给每篇文章列出：搜索意图、目标读者、主关键词、建议标题、摘要、所属分类、建议字数、封面图来源和正文配图需求。",
		"- 普通教程、产品解释、对比和 SEO/GEO 文章不能写成短说明。中文建议不少于 1200 字，英文建议不少于 800 words；FAQ、公告或索引页可以更短，但必须在规划里说明原因。",
		"- 正文至少包含 3 个二级标题，并提供真实场景、步骤、清单、对比、常见问题或下一步行动；不要只写导语加几条空泛 bullet。",
		"- 配图不是装饰。真实操作场景优先使用系统截图；产品能力说明优先使用真实前台或后台截图；概念解释可以使用简洁图解；不要使用和主题无关的抽象图。",
		"- 如果有媒体上传权限和可用素材，先把所有图片转成 WebP（.webp），再用 `POST /media` 上传，把返回的 `url` 写入 `cover_image` 或正文 Markdown 图片。",
		"- 如果缺少图片素材、没有媒体权限或无法得到真实截图，不要伪造截图；先把文章保持草稿，并在最终清单里标注“需要补图”。",
		"- 写完每篇文章后，用读取或预览接口自检：`cover_image`、摘要、SEO 描述、关键词、分类、正文结构和正文配图是否满足规划；不满足就先修正，再报告。",
		"",
		"## 内容模型边界",
		"",
		"- 文章 `posts`：适合教程、资讯、案例、观点和 SEO/GEO 内容；可选择一个真实文章分类 `category_id`。",
		"- 链接 `links`：适合资源导航、产品展示、外部工具和带详情页的目标网址；可选择一个真实链接分类 `category_id`，并必须有 `link_url`。",
		"- 页面 `pages`：适合关于、功能、价格、FAQ、联系等固定页面；页面没有分类，也不使用 `category_id`。",
		"- 文章分类/链接分类：是内容可选择的真实分类，用 `/posts/categories`、`/links/categories` 创建或更新，返回的 `id` 才能写入内容的 `category_id`。",
		"- 全部入口：`/posts/categories/all-entry` 和 `/links/categories/all-entry` 只控制前台总列表页标题、描述、路径和“全部”筛选按钮；它不是分类，不能写入 `category_id`。",
		"",
		"## 推荐写入顺序",
		"",
		"1. `PATCH /site-profile` 写站点名、标语、描述、Hero、首页标题和默认作者。",
		"2. 如需改列表页文案，`PATCH /posts/categories/all-entry` 和 `PATCH /links/categories/all-entry` 更新总入口。",
		"3. `POST /posts/categories` 和 `POST /links/categories` 建真实分类。",
		"4. `POST /pages` 建首页以外的基础页面草稿。",
		"5. `PATCH /navigation` 写菜单顺序和各语种菜单文字，URL 必须引用全部入口返回的 `path`、真实分类 slug、页面 slug 或外链。",
		"6. `POST /posts` 建 6-12 篇基础文章草稿。",
		"7. `POST /links` 建资源链接草稿。",
		"8. 用列表接口复核缺项，再给用户检查清单。",
		"",
		"## 导航 URL 规则",
		"",
		"- 首页写 `/`。",
		"- 文章分类总页优先使用 `GET /posts/categories/all-entry?lang=默认语种` 返回的 `path`；单个文章分类写 `/category/{slug}`。",
		"- 链接总页优先使用 `GET /links/categories/all-entry?lang=默认语种` 返回的 `path`；单个链接分类写 `/links/cat/{slug}`。",
		"- 页面写 `/{page-slug}`，搜索写 `/search`。",
		"- 外部网站写完整 `https://...`。",
		"- 不要把已经创建的文章分类、链接分类或页面写成随意的“自定义站内路径”。",
	}, "\n") + "\n"
}

func automationStarterWizardMarkdown() string {
	return strings.Join([]string{
		"# 新站需求向导",
		"",
		"这份向导适合不熟悉 CMS、API、slug 和导航结构的用户。你只需要用自然语言回答下面的问题；不确定的地方写“不确定”，让 AI 先给建议。",
		"",
		"## 怎么填写",
		"",
		"- 不用填写上面的标题和说明，从下面「1. 网站是做什么的？」开始填。",
		"- 把答案写在每一行冒号后面，例如：`- 一句话说明：这是一个面向中小企业的产品官网`。",
		"- 不会填就写“不确定”，不需要就写“不需要”。",
		"- 尽量不要删除问题，AI 会根据这些问题判断新站边界。",
		"",
		"## 1. 网站是做什么的？",
		"",
		"- 一句话说明：",
		"- 面向谁：",
		"- 希望用户看完后做什么：",
		"",
		"示例：这是一个面向中小企业的产品官网，希望用户了解产品能力并留下咨询线索。",
		"",
		"## 2. 网站需要哪些语种？",
		"",
		"- 启用语种：例如中文、英文",
		"- 默认语种：例如中文",
		"- 不同语种是否需要不同表达：是 / 否 / 不确定",
		"",
		"## 3. 网站应该是什么感觉？",
		"",
		"- 语气：专业 / 轻松 / 极简 / 教程型 / 销售型 / 可信赖 / 其他",
		"- 品牌关键词：",
		"- 明确不能出现的表达：",
		"",
		"## 4. 希望 AI 帮你做到什么程度？",
		"",
		"- 只出规划，不写入：是 / 否",
		"- 允许修改首页文案：是 / 否",
		"- 允许修改导航：是 / 否",
		"- 允许创建文章分类和链接分类：是 / 否",
		"- 允许创建页面、文章和链接草稿：是 / 否",
		"- 允许发布内容：默认否；只有非常明确时才写“允许发布哪些内容”。",
		"",
		"## 5. 第一批内容规模",
		"",
		"- 希望第一批页面：例如关于、功能、FAQ、开始使用",
		"- 希望第一批文章数量：",
		"- 文章深度偏好：标准教程 / 深度 SEO / 简短说明 / 不确定",
		"- 每篇文章是否需要封面图：是 / 否 / 不确定",
		"- 哪些文章需要正文配图或系统截图：",
		"- 希望第一批资源链接数量：",
		"- 是否需要 FAQ、教程、对比、案例：",
		"",
		"## 6. SEO/GEO 边界",
		"",
		"- 想覆盖的关键词：",
		"- 想避免的关键词：",
		"- 目标地区或市场：",
		"- 是否有合规要求：例如金融、医疗、法律、隐私、版权、品牌授权",
		"",
		"## 7. 素材",
		"",
		"- 品牌素材（默认只供规划参考；如果要让 AI 替换站点 Logo、浏览器图标或分享图，请明确写出来，并确认密钥有品牌资产权限）：",
		"- 可用于文章封面或正文的图片素材位置：",
		"- 如果图片不是 WebP，是否允许 AI 先转成 WebP 后再上传：默认允许",
		"- 产品截图或案例素材：",
		"- 真实操作截图是否可以由 AI 截取或上传：是 / 否 / 不确定",
		"- 如果缺少图片，是否允许先写草稿并在复核清单里标注“需要补图”：是 / 否",
		"- 已有文案或资料：",
		"",
		"## 给 AI 的边界提醒",
		"",
		"- 第一轮只允许输出规划，不允许写入。",
		"- 所有新增页面、文章和链接默认保存为草稿。",
		"- 不允许删除内容。",
		"- 不允许修改后台安全、管理员账号、系统更新、Cloudflare、评论和 API Key。",
		"- 没有明确要求或没有品牌资产权限时，不允许替换站点 Logo、浏览器图标和分享图。",
		"- 所有图片上传前必须先转成 WebP（.webp）。",
		"- 需要发布时，必须再次获得明确授权。",
	}, "\n") + "\n"
}

func automationStarterRequirementsTemplate() string {
	return strings.Join([]string{
		"# 站点需求模板",
		"",
		"如果你已经填过 `新站需求向导.md`，这里可以补充更具体的栏目、关键词和素材要求。暂时不确定的地方可以写“不确定”，让 AI 先给建议。",
		"",
		"## 基础信息",
		"",
		"- 网站名称：",
		"- 网站面向谁：",
		"- 网站主要目的：产品官网 / 技术文档 / 资源导航 / 教程科普 / 企业展示 / 其他",
		"- 希望用户看完后做什么：",
		"- 启用语种：例如中文、英文",
		"- 默认语种：",
		"",
		"## 内容调性",
		"",
		"- 品牌关键词：",
		"- 不想出现的表达：",
		"- 语气：专业 / 轻松 / 极简 / 销售型 / 教程型",
		"- 竞品或参考网站：",
		"",
		"## 页面与导航",
		"",
		"- 需要哪些导航：",
		"- 是否需要关于页、功能页、价格页、联系页、文档页：",
		"- 是否已有固定 URL 或 slug：",
		"- 哪些现有导航不能改：",
		"",
		"## 文章与链接",
		"",
		"- 希望有哪些文章分类：",
		"- 希望有哪些链接分类：",
		"- 希望第一批准备多少篇文章：",
		"- 希望第一批准备哪些资源链接：",
		"- 哪些已有内容不能改：",
		"",
		"## 文章质量与配图",
		"",
		"- 文章深度：标准教程 / 深度 SEO / 简短说明 / 不确定",
		"- 每篇文章希望解决的问题或搜索意图：",
		"- 每篇文章是否必须有封面图：",
		"- 正文中是否需要步骤截图、产品截图或图解：",
		"- 可用图片文件、截图或素材位置：",
		"- 图片处理要求：上传前必须转成 WebP（.webp）",
		"- 如果缺少图片：暂停询问 / 先写草稿并标记需要补图",
		"",
		"## SEO/GEO",
		"",
		"- 想覆盖的关键词：",
		"- 想避免的关键词：",
		"- 目标地区或市场：",
		"- 是否需要 FAQ、对比、教程、案例等内容：",
		"",
		"## 素材",
		"",
		"- 品牌素材/图片文件位置（默认只供规划参考；如需替换站点 Logo、浏览器图标或分享图，请明确写出来，并确认密钥有品牌资产权限）：",
		"- 产品截图或案例素材：",
		"- 已有文案或资料：",
		"",
		"## 写入规则",
		"",
		"- 第一轮是否只允许规划：是",
		"- 允许 AI 写入哪些内容：站点文案 / 导航 / 分类 / 页面 / 文章 / 链接 / 媒体 / 品牌资产",
		"- 默认内容状态：草稿",
		"- 是否允许发布：否",
	}, "\n") + "\n"
}

func automationStarterPlanPrompt() string {
	return strings.Join([]string{
		"# 第一步：让 AI 出规划",
		"",
		"把下面这段提示词复制给 AI。这个阶段只做只读检查和规划，不会写入 GCMS。",
		"",
		"```text",
		"请读取当前文件夹里的 GCMS 新站 AI 技能包，重点阅读：新站需求向导.md、站点需求模板.md、给AI的任务说明.md、工作流.md 和 references/openapi.json。",
		"",
		"本轮只允许做只读检查和规划，不允许创建、修改、删除或发布任何内容。",
		"",
		"请先调用以下接口了解当前站点：",
		"- GET /languages",
		"- GET /site-profile",
		"- GET /navigation",
		"- GET /posts/categories/all-entry?lang=all",
		"- GET /links/categories/all-entry?lang=all",
		"- GET /posts/categories?lang=all",
		"- GET /links/categories?lang=all",
		"",
		"然后基于我填写的需求，输出一份《新站内容规划》，必须包含：",
		"1. 网站定位和目标用户",
		"2. 每个启用语种的表达策略",
		"3. 建议导航和 URL",
		"4. 首页文案结构",
		"5. 文章/链接总入口，以及真实文章分类和链接分类",
		"6. 基础页面清单",
		"7. 第一批文章选题：每篇都要写明搜索意图、目标读者、主关键词、建议字数、封面图来源和正文配图需求",
		"8. 文章质量与配图规则：哪些需要真实截图，哪些可以用图解，哪些缺素材需要用户补充",
		"9. 第一批资源链接方向",
		"10. 会新增什么、会修改什么、哪些内容保持草稿",
		"11. 风险提醒，包括品牌、金融、医疗、法律、版权、隐私和夸大承诺",
		"",
		"最后给出“建议执行清单”，等待我确认。没有我的明确确认前，不要写入。",
		"",
		"如果我反馈规划不满意，只根据反馈继续调整规划；仍然不要写入、不要创建草稿。",
		"```",
	}, "\n") + "\n"
}

func automationStarterWritePrompt() string {
	return strings.Join([]string{
		"# 第二步：审核后写入草稿",
		"",
		"只有当你已经审核并确认 AI 给出的规划后，才使用这份提示词。默认只写草稿，不发布。",
		"",
		"```text",
		"我已审核并确认刚才的新站内容规划。请按规划分批写入 GCMS。",
		"",
		"写入规则：",
		"- 先再次读取 /languages、/site-profile、/navigation、/posts/categories/all-entry?lang=all、/links/categories/all-entry?lang=all、/posts/categories?lang=all、/links/categories?lang=all。",
		"- 按顺序写入：站点文案 -> 文章/链接总入口 -> 真实分类 -> 页面 -> 导航 -> 文章 -> 链接。",
		"- 写导航时只使用标准 URL：首页 `/`；文章分类总页和链接总页优先使用 all-entry 返回的 `path`；单分类 `/category/{slug}`；链接分类 `/links/cat/{slug}`；页面 `/{page-slug}`；搜索 `/search`；外链用完整 `https://...`。",
		"- `/posts/categories/all-entry` 与 `/links/categories/all-entry` 只改列表页标题、描述、入口路径和“全部”筛选按钮；不要把它们当成真实分类，也不要把它们写入 category_id。",
		"- 不要把已经创建的文章分类、链接分类或页面写成随意的自定义站内路径。",
		"- 所有页面、文章和链接默认 status=draft。",
		"- 多语种对应内容必须使用同一个 trans_group。",
		"- 不要删除任何内容。",
		"- 不要修改安全、管理员账号、系统更新、Cloudflare、评论和 API Key。",
		"- 没有明确要求或没有品牌资产权限时，不要替换站点 Logo、浏览器图标和分享图。",
		"- 不要发布，除非我单独明确说“发布哪些内容”。",
		"",
		"文章写入要求：",
		"- 创建文章前先确认规划中已有搜索意图、目标读者、主关键词、建议字数、封面图来源和正文配图需求；缺失时先补齐规划并说明。",
		"- 中文标准文章建议不少于 1200 字，英文标准文章建议不少于 800 words；短 FAQ、公告或索引页可以例外，但要在复核清单里说明为什么短。",
		"- 正文至少包含 3 个二级标题，必须有场景、步骤、清单、对比、FAQ 或下一步行动中的至少 2 类内容。",
		"- 有图片素材和媒体权限时，先把图片转成 WebP（.webp），再 `POST /media` 上传；文章必须写入 `cover_image`，需要展示操作步骤时，在正文合适位置插入 Markdown 图片。",
		"- 不要伪造真实截图。没有素材或权限时，文章保持草稿，并在最终清单里标注“需要补图”。",
		"- 每篇文章写入后用读取或预览接口复核：`cover_image`、摘要、SEO 描述、关键词、分类、正文结构、正文配图和字数是否达标；不达标先修正。",
		"",
		"每完成一批，请回读接口确认，并汇总：类型、id、语种、标题、slug、状态、是否需要我人工复核。",
		"如果发现权限不足、语种缺失、slug 冲突或素材缺失，请先停下来说明问题，不要绕过限制。",
		"如果我反馈草稿不满意，只修改我指出的部分；不要顺手改其他模块，更不要发布。",
		"```",
	}, "\n") + "\n"
}

func automationStarterWorkflowMarkdown() string {
	return strings.Join([]string{
		"# 新站初始化工作流",
		"",
		"这个工作流专门把“规划”和“写入”拆开，避免 AI 在用户还没看懂边界时直接改站点。",
		"",
		"## 第零步：用户填写简单需求",
		"",
		"- 优先填写 `新站需求向导.md`。",
		"- 如果用户熟悉网站结构，再补充 `站点需求模板.md`。",
		"- 不确定的地方不要猜，先在规划里给建议。",
		"",
		"## 第一步：只读检查",
		"",
		"- 读取 `/languages`。",
		"- 读取 `/site-profile`。",
		"- 读取 `/navigation`。",
		"- 读取 `/posts/categories/all-entry?lang=all` 和 `/links/categories/all-entry?lang=all`。",
		"- 读取 `/posts/categories?lang=all` 和 `/links/categories?lang=all`。",
		"- 如需避免重复，读取现有 `/posts`、`/pages`、`/links`。",
		"",
		"## 第二步：生成规划",
		"",
		"输出一份计划，至少包含：",
		"",
		"- 每个语种的站点名、标语、描述、Hero 文案。",
		"- 导航菜单及 URL。",
		"- 文章/链接总入口文案和路径。",
		"- 真实文章分类、链接分类。",
		"- 基础页面清单。",
		"- 第一批文章清单，并写明每篇文章的搜索意图、目标读者、主关键词、建议字数、封面图来源和正文配图需求。",
		"- 图片素材处理方式：所有要上传的图片都必须转成 WebP（.webp）。",
		"- 第一批资源链接清单。",
		"- 哪些内容保持草稿，哪些需要用户确认。",
		"- 明确列出“会修改”和“不会修改”的边界。",
		"- 标出合规、品牌、版权和夸大承诺风险。",
		"",
		"## 第三步：用户确认",
		"",
		"没有确认前不要写入。用户确认后，按模块分批写入，便于回滚和检查。",
		"",
		"用户只说“继续”“可以”时，先确认是否允许写入；只有明确说“按规划写入草稿”时才开始调用写接口。",
		"",
		"如果用户对规划不满意，继续只调整规划，不要写入。可以让用户用自然语言指出问题，例如“导航太多”“文章方向太泛”“英文不要直译中文”。",
		"",
		"## 第四步：写入",
		"",
		"- 站点文案：`PATCH /site-profile`。",
		"- 文章/链接总入口：`PATCH /posts/categories/all-entry`、`PATCH /links/categories/all-entry`。",
		"- 真实分类：`POST /posts/categories`、`POST /links/categories`。",
		"- 页面：`POST /pages`，默认 `draft`。",
		"- 导航：`PATCH /navigation`，只引用标准入口或已经确定的分类、页面 slug。",
		"- 文章：`POST /posts`，默认 `draft`。",
		"- 链接：`POST /links`，默认 `draft`。",
		"",
		"## 第四点五步：文章质量与配图验收",
		"",
		"每篇文章写入后先自检，再交给用户看：",
		"",
		"- 是否有明确搜索意图、摘要、SEO 描述、关键词和分类。",
		"- 是否有 `cover_image`；没有时是否已经说明缺素材或缺权限。",
		"- 已上传图片是否都是 WebP（.webp）格式。",
		"- 真实操作类文章是否使用系统截图或用户素材，不使用无关装饰图。",
		"- 正文是否有至少 3 个二级标题，并覆盖场景、步骤、清单、对比、FAQ 或下一步行动。",
		"- 是否明显太短；太短时先补充案例、步骤、FAQ 或对比，再报告。",
		"",
		"## 第五步：复核",
		"",
		"- 检查每条内容的标题、slug、摘要、SEO 描述、关键词、分类、封面、正文结构。",
		"- 多语种内容检查 `trans_group` 是否一致。",
		"- 输出人工复核清单，不要自行发布。",
		"- 如需发布，等待用户再次明确发布范围。",
		"- 如果用户对草稿不满意，只修改指定内容，修改后重新输出复核清单。",
	}, "\n") + "\n"
}

func automationStarterPromptExamples(opts automationSkillOptions) string {
	return strings.Join([]string{
		"# 示例提示词",
		"",
		"## 从零规划",
		"",
		"请读取这个文件夹里的 GCMS 新站 AI 技能包和我填写的 `新站需求向导.md`。请先读取当前站点、语种、导航和分类，只输出新站内容规划，不要写入。",
		"",
		"## 小白用户填写后规划",
		"",
		"我已经填写了 `新站需求向导.md`，但不确定导航、分类和文章怎么设计。请先根据我的回答和当前站点结构，输出一份可审核的新站规划。不要写入。",
		"",
		"## 确认后写入草稿",
		"",
		"按刚才确认的规划写入 GCMS。先更新站点文案，再创建分类和页面，最后用真实 slug 写导航、文章和链接。所有内容保持草稿，不要发布。完成后列出每条内容的 id、slug、语种和状态。",
		"",
		"## 要求深度文章和配图",
		"",
		"第一批文章不要写成短说明。每篇文章先列搜索意图、目标读者、主关键词、建议字数、封面图来源和正文配图需求；有素材时先转成 WebP 再上传图片并写入 cover_image，教程类文章正文里要插入真实截图。",
		"",
		"## 缺图时先标注",
		"",
		"如果没有合适图片或没有媒体上传权限，不要伪造截图。先创建草稿，并在完成清单里标注哪些文章需要补图、建议补什么图。",
		"",
		"## 规划不满意时继续调整",
		"",
		"不要写入。请根据我的反馈重做规划：导航精简到 5 个以内，文章选题更偏产品转化，英文内容不要直译中文。重新输出规划和建议执行清单。",
		"",
		"## 草稿不满意时局部修改",
		"",
		"不要发布，也不要重做全站。请只修改我指出的部分：重写首页 Hero 文案，保留导航和分类；把这 3 篇文章标题改得更像搜索用户会问的问题。修改后列出变化清单。",
		"",
		"## 只做中文站",
		"",
		"请把这个 GCMS 初始化为中文技术文档站。主题围绕“低成本部署、内容维护、SEO/GEO、自动化运营”。先生成规划，确认后只写中文草稿。",
		"",
		"## 多语种站",
		"",
		"请为中文和英文分别写站点文案、导航、分类和基础内容。英文不要直译中文，要面向海外用户表达。对应内容用同一个 trans_group 关联。",
		"",
		"## 资源导航站",
		"",
		"请把这个 GCMS 初始化为一个资源导航站。先创建链接分类和链接草稿，再创建 3 篇说明文章。每条链接要有摘要、正文介绍、SEO 描述和合适分类。",
		"",
		"## 只检查不写入",
		"",
		"请读取当前站点配置、导航、分类、文章、页面和链接，评估是否适合作为一个新站演示内容。只输出问题和优化建议，不要写入。",
		"",
		"接口地址：" + opts.apiBase,
	}, "\n") + "\n"
}

func automationStarterSkillMarkdown(apiBase string) string {
	return strings.Join([]string{
		"---",
		"name: gcms-site-starter",
		"description: Use this skill to initialize a new GCMS site from a human brief: inspect languages and existing state, plan site positioning, navigation, categories, pages, posts, and links, then write multilingual starter content as drafts through the GCMS automation API. Do not publish or modify system/security settings without explicit approval.",
		"---",
		"",
		"# GCMS Site Starter",
		"",
		"你是 GCMS 新站初始化助手。你帮助用户把一个空站或演示站整理成可检查的新站基础内容：站点文案、首页文案、导航、文章分类、链接分类、页面、文章和链接。",
		"",
		"## 连接方式",
		"",
		"- API Base: `" + apiBase + "`",
		"- OpenAPI: `references/openapi.json`",
		"- 优先从 `.env` 或环境变量读取 `GCMS_API_BASE` 与 `GCMS_API_KEY`。",
		"- 不要在普通回复里泄露访问密钥。",
		"",
		"## 标准流程",
		"",
		"1. 优先读取 `新站需求向导.md`；如有 `站点需求模板.md`，一并读取。",
		"2. 调用 `/languages`、`/site-profile`、`/navigation`、`/posts/categories/all-entry?lang=all`、`/links/categories/all-entry?lang=all`、`/posts/categories?lang=all`、`/links/categories?lang=all` 做只读检查。",
		"3. 第一轮只输出完整规划，不要马上写入。",
		"4. 规划要列出会新增、会修改和不会触碰的内容，并提示合规、品牌、版权、隐私和夸大承诺风险。",
		"5. 用户明确确认后再分批写入：站点文案 -> 文章/链接总入口 -> 真实分类 -> 页面 -> 导航 -> 文章 -> 链接。",
		"6. 所有内容默认 `status: draft`。",
		"7. 如果用户对规划或草稿不满意，只按反馈调整对应部分，不要扩散修改范围。",
		"8. 完成后列出每条内容的 id、slug、语种、状态和需要人工复核的点。",
		"",
		"## 文章质量与配图",
		"",
		"- 规划每篇文章时写清搜索意图、目标读者、主关键词、建议字数、封面图来源和正文配图需求。",
		"- 中文标准文章建议不少于 1200 字，英文标准文章建议不少于 800 words；短内容必须说明原因。",
		"- 正文至少包含 3 个二级标题，并提供真实场景、步骤、清单、对比、FAQ 或下一步行动。",
		"- 有媒体权限和素材时，先把图片转成 WebP（.webp），再 `POST /media` 上传图片，把返回 URL 写入 `cover_image` 或正文 Markdown 图片。",
		"- 真实操作类文章优先使用系统截图；不要伪造截图或使用无关装饰图。",
		"- 缺素材或缺权限时，保持草稿并在复核清单里标注“需要补图”。",
		"",
		"## 内容模型边界",
		"",
		"- 文章 `posts` 用于教程、资讯、案例、观点和 SEO/GEO 内容；可写真实文章分类的 `category_id`。",
		"- 链接 `links` 用于资源导航、产品展示、外部工具和带详情页的目标网址；必须写 `link_url`，可写真实链接分类的 `category_id`。",
		"- 页面 `pages` 用于关于、功能、价格、FAQ、联系等固定内容；没有分类，不写 `category_id`。",
		"- `/posts/categories` 和 `/links/categories` 返回真实分类；只有这些分类的 `id` 可以写入内容的 `category_id`。",
		"- `/posts/categories/all-entry` 和 `/links/categories/all-entry` 是总列表入口，控制列表页标题、描述、路径和“全部”筛选按钮；它们不是分类。",
		"",
		"## 边界",
		"",
		"- 不要删除内容。",
		"- 不要修改管理员账号、密码、安全设置、系统更新、Cloudflare 部署、评论配置或 API Key。",
		"- 没有明确要求或没有品牌资产权限时，不要替换站点 Logo、浏览器图标或分享图。",
		"- 写导航时只使用标准 URL：首页 `/`；文章/链接总页优先使用 all-entry 返回的 `path`；单个文章分类 `/category/{slug}`；链接分类 `/links/cat/{slug}`；页面 `/{page-slug}`；搜索 `/search`；外链用完整 `https://...`。",
		"- 不要默认发布内容；只有用户明确要求并且访问密钥具备发布权限时才发布。",
		"- 多语种内容不要机械直译；要根据目标读者调整表达，并使用同一个 `trans_group` 关联同组内容。",
		"- 修改已有内容时，先查到准确 id，再按 id 更新。",
	}, "\n") + "\n"
}

func automationKitReadme(opts automationSkillOptions) string {
	lines := []string{
		"# GCMS AI 助手使用包",
		"",
		"这个包给 Codex、Claude Code、Cursor 等能读取文件并运行脚本的 AI 工具使用，也可给开发者参考接口说明。",
		"",
		"## 包内文件",
		"",
		"- `gcms-content-assistant/.env`：密钥文件，包含 `GCMS_API_BASE` 和 `GCMS_API_KEY`。",
		"- `gcms-content-assistant/AI助手说明.md`：给 AI 工具看的任务边界、认证方式和操作规则。",
		"- `gcms-content-assistant/SKILL.md`：支持 skills 的 AI 工具可读取的技能说明。",
		"- `gcms-content-assistant/references/openapi.json`：OpenAPI 接口描述文件。",
		"- `gcms-content-assistant/scripts/gcms.js`：可选的命令行调用脚本。",
		"",
	}
	if opts.token != "" {
		name := opts.name
		if name == "" {
			name = "这个外部助手"
		}
		lines = append(lines,
			"这个包已经写入访问密钥，只给「"+name+"」使用。",
			"如果这个包或密钥泄露，请回到 GCMS 后台吊销对应的访问权限。",
		)
		if opts.scopes != "" {
			lines = append(lines, "权限："+automationScopeLabels(strings.Split(opts.scopes, ",")))
		}
	} else {
		lines = append(lines,
			"这个包不包含访问密钥。请先在 GCMS 后台「设置 -> 自动化接口」创建访问权限，再把创建成功后显示的密钥填到 `gcms-content-assistant/.env`。",
		)
	}
	lines = append(lines,
		"",
		"## 使用步骤",
		"",
	)
	if opts.token != "" {
		lines = append(lines,
			"1. 把 `gcms-content-assistant` 文件夹交给 AI 工具读取。",
			"2. 先运行 `node scripts/gcms.js doctor` 检查连接、OpenAPI 和权限。",
			"3. 对 AI 说清楚任务，例如：检查最近 10 篇文章标题，只给建议，不要发布。",
			"4. 如果不再使用这个工具，请在后台吊销对应访问权限。",
		)
	} else {
		lines = append(lines,
			"1. 把 `gcms-content-assistant/.env.example` 复制为 `.env`。",
			"2. 填入 `GCMS_API_BASE` 和 `GCMS_API_KEY`。",
			"3. 把 `gcms-content-assistant` 文件夹交给 AI 工具读取。",
			"4. 先运行 `node scripts/gcms.js doctor` 检查连接、OpenAPI 和权限。",
			"5. 对 AI 说清楚任务，例如：检查最近 10 篇文章标题，只给建议，不要发布。",
		)
	}
	lines = append(lines,
		"",
		"## 可以直接这样对 AI 说",
		"",
		"交代任务时尽量说清楚：目标资源、语种、范围、动作、素材、不能改的字段、是否允许发布、期望输出格式。",
		"",
		"- 先运行 `node scripts/gcms.js doctor` 检查连接、OpenAPI、分类读取和媒体上传权限；只报告结果，不要创建或修改内容。",
		"- 帮我规划一个资料库网站：先读取语种、站点文案、导航、文章/链接总入口和真实分类，说明文章、链接、页面各自承担什么内容，再给出导航和分类建议；第一轮不要写入。",
		"- 帮我调整文章总列表页：把“全部文章入口”的标题、描述、slug 和“全部”筛选按钮改得更适合教程站；先读取 `/posts/categories/all-entry?lang=all`，确认后再更新。",
		"- 检查最近 50 篇中文文章，重点看标题、摘要、SEO 描述、关键词、分类、封面图是否缺失；只输出问题列表和建议，不要修改。",
		"- 深度检查最近 20 个页面，逐条读取正文，找出缺正文、缺封面、SEO 描述太弱或标题不清楚的页面；按优先级列出 ID、标题、问题和建议。",
		"- 根据我提供的资料创建一篇中文文章草稿；先查询文章分类并选择合适的 `category_id`，有封面图时先转成 WebP 再上传媒体，并把返回 URL 写入 `cover_image`；状态保持 `draft`。",
		"- 把标题包含某个关键词的文章摘要和 SEO 描述优化一下；先用 `q` 或 `slug` 找到准确 ID，多个结果先让我确认；不要改正文、slug 或发布时间。",
		"- 把我提供的图片先转成 WebP，再上传到媒体接口，拿返回 URL 后插入到指定文章正文的合适位置；保留原有正文结构。",
		"- 创建一条链接草稿，链接地址是我给的 URL；先查询链接分类并写入合适的 `category_id`，补充摘要、正文介绍、SEO 描述和封面图。",
		"- 先读取启用语种，再读取目标内容的 `trans_group`，找出同组中文和英文版本；分别按各自语言优化标题、摘要和 SEO 描述。",
		"- 发布前复核指定草稿是否具备发布条件，包括标题、slug、摘要、SEO 描述、关键词、分类、封面图、正文结构和多语种关联；只给意见，不要发布。",
		"- 发布前调用 `GET /posts/{id}/preview` 或 `GET /links/{id}/preview`，检查草稿渲染后的正文 HTML、目录和正式 URL；需要浏览器复核时调用 `POST /posts/{id}/preview-url` 或 `POST /links/{id}/preview-url` 生成短期前台预览链接。",
		"- 只有我明确说“发布这篇”时，才回读目标 ID 和当前状态，确认具备 `publish` 权限后改为 `published`；完成后报告 ID、语种、URL 和改动字段。",
		"- 如果接口返回权限不足、分类不存在、图片上传失败或找不到目标内容，停止后续写入动作，把错误、已完成步骤和需要补充的信息列出来。",
		"",
		"接口地址："+opts.apiBase,
		"OpenAPI 文件："+strings.TrimRight(opts.apiBase, "/")+"/openapi.json",
		"",
		"## 安全提醒",
		"",
		"- 一个访问密钥只给一个外部工具或平台使用。",
		"- 不要把真实访问密钥发到普通聊天窗口。",
		"- 第一次接入、改过权限或接口异常时，先运行 `node scripts/gcms.js doctor`。",
		"- 默认让 AI 创建或修改草稿，发布前先人工审核。",
		"- 修改指定内容时，让 AI 先查 id，再按 id 更新。",
		"- 发布前复核文章或链接草稿时，让 AI 用 `/posts/{id}/preview` 或 `/links/{id}/preview` 查看渲染后的正文 HTML；需要打开真实前台页面时，用 `/posts/{id}/preview-url` 或 `/links/{id}/preview-url` 生成短期签名链接。",
		"- 设置内容分类前，让 AI 先用 `/posts/categories` 或 `/links/categories` 查询真实分类 ID；不要把 all-entry 当成 `category_id`。",
		"- 调整文章或链接列表页标题、描述、入口路径和“全部”筛选按钮时，让 AI 使用 `/posts/categories/all-entry` 或 `/links/categories/all-entry`。",
		"- 设置封面或正文图片前，让 AI 先把图片转成 WebP（.webp），再用 `POST /media` 上传文件，拿返回的 `url` 再写入 `cover_image` 或 Markdown 图片。",
		"- 更新全部语种时，让 AI 先用 `/languages` 确认启用语种，再按 `trans_group` 找到同组内容，逐条更新各语种 id。",
	)
	return strings.Join(lines, "\n") + "\n"
}

func automationAssistantBriefMarkdown(opts automationSkillOptions) string {
	token := opts.token
	if token == "" {
		token = "gcms_xxx"
	}
	scopes := []string(nil)
	if opts.scopes != "" {
		scopes = strings.Split(opts.scopes, ",")
	}
	brief := automationAIBrief(opts.apiBase, token, scopes)
	name := strings.TrimSpace(opts.name)
	if name == "" {
		name = "外部 AI 助手"
	}
	return strings.Join([]string{
		"# 给 AI 助手的说明",
		"",
		"用途：" + name,
		"",
		brief,
		"",
		"## 使用提醒",
		"",
		"- 不要把真实密钥贴到普通聊天窗口。",
		"- 如果这个包泄露，请到 GCMS 后台吊销或重新生成对应访问密钥。",
		"- 默认只创建或修改草稿，发布前请人工复核。",
	}, "\n") + "\n"
}

func automationSkillMarkdown(apiBase string) string {
	return strings.Join([]string{
		"---",
		"name: gcms-content-assistant",
		"description: Use this skill to operate a GCMS site through its automation API for standard content operations: run connection and permission diagnostics; audit posts, pages, and links; upload media; create or update drafts; improve SEO metadata; handle categories and multilingual content; and publish only with explicit approval and permission.",
		"---",
		"",
		"# GCMS Content Assistant",
		"",
		"你是 GCMS 网站内容助手。你可以读取语种和分类、上传媒体，并处理文章、页面、链接。需要规划导航或分类时，必须区分文章、链接、页面、真实分类和“全部入口”。不要增删改安全、系统更新。",
		"",
		"## 连接方式",
		"",
		"- API Base: `" + apiBase + "`",
		"- OpenAPI: `references/openapi.json`",
		"- 优先从环境变量读取 `GCMS_API_BASE` 与 `GCMS_API_KEY`。",
		"- 不要在普通回复里泄露访问密钥。",
		"",
		"## 任务模式",
		"",
		"- `doctor`：检查配置、OpenAPI、分类读取和媒体权限。",
		"- `audit`：只检查内容并报告问题，不写入。",
		"- `draft`：创建草稿，默认 `status` 为 `draft`。",
		"- `update`：先找到准确 id，再按字段更新。",
		"- `media`：先把用户提供的图片转成 WebP（.webp），再上传文件，把返回 URL 用于封面或正文图片。",
		"- `multilingual`：先查语种和 `trans_group`，逐条处理各语种版本。",
		"- `publish-review`：发布前复核；只有用户明确要求且权限允许才发布。",
		"- `preview`：发布前读取文章或链接预览，检查渲染后的正文 HTML、目录和正式 URL。",
		"- `preview-url`：生成短期有效的前台预览链接，用真实前台模板复核草稿。",
		"",
		"## 工作规则",
		"",
		"1. 修改某篇内容前，先用 `q` 或 `slug` 查到准确 `id`。",
		"2. 新环境、权限变更或接口异常时，先运行 `node scripts/gcms.js doctor`。",
		"3. 如果查到多个相似结果，先让用户确认。",
		"4. 规划导航、分类或站点结构时，先读取 `/posts/categories/all-entry?lang=all`、`/links/categories/all-entry?lang=all`、`/posts/categories?lang=all`、`/links/categories?lang=all`。",
		"5. 需要设置内容分类时，只能用 `GET /posts/categories?lang=...` 或 `GET /links/categories?lang=...` 返回的真实分类 ID 写入 `category_id`。",
		"6. 需要调整文章/链接总列表页标题、描述、路径或“全部”筛选按钮时，使用 `PATCH /posts/categories/all-entry` 或 `PATCH /links/categories/all-entry`；all-entry 不是分类。",
		"7. 需要封面或正文图片时，先把图片转成 WebP（.webp），再用 `POST /media` 上传文件，拿返回的 `url` 再写入 `cover_image` 或 Markdown 图片。",
		"8. 处理多语种内容时，先 `GET /languages` 查看启用语种；如果用户要求更新全部语种，先读取目标内容的 `trans_group`，再用 `lang=all&trans_group=...` 找到同组所有版本，逐条按 id 更新。",
		"9. 不要把一个语种的正文直接覆盖到其它语种，除非用户明确要求这么做。",
		"10. 默认只创建或修改草稿。",
		"11. 只有用户明确要求发布，并且访问密钥有对应资源的发布权限，才设置 `status` 为 `published` 或 `scheduled`。",
		"12. 发布前优先用 `GET /posts/{id}/preview` 或 `GET /links/{id}/preview` 复核草稿渲染结果；需要浏览器复核时再生成 `preview-url`。",
		"13. 完成后告诉用户变更了哪些内容、对应 id、语种、状态，以及建议人工复核的点。",
		"",
		"## 内容模型边界",
		"",
		"- 文章 `posts`：教程、资讯、案例、观点、SEO/GEO 内容；可选择真实文章分类。",
		"- 链接 `links`：资源导航、产品展示、外部工具；必须有 `link_url`，可选择真实链接分类。",
		"- 页面 `pages`：关于、功能、价格、FAQ、联系等固定页面；没有分类。",
		"- 真实分类：`/posts/categories`、`/links/categories`，返回的 `id` 才能写入内容。",
		"- 全部入口：`/posts/categories/all-entry`、`/links/categories/all-entry`，只控制总列表页文案、路径和筛选按钮。",
		"",
		"## 推荐脚本",
		"",
		"如果当前环境可以运行 Node.js，优先使用 `scripts/gcms.js`：",
		"",
		"- `node scripts/gcms.js doctor`",
		"- `node scripts/gcms.js languages`",
		"- `node scripts/gcms.js upload ./cover.webp`",
		"- `node scripts/gcms.js categories posts --lang zh`",
		"- `node scripts/gcms.js categories links --lang zh`",
		"- `node scripts/gcms.js category-entry posts --lang all`",
		"- `node scripts/gcms.js update-category-entry posts '{\"lang\":\"zh\",\"title\":\"教程\",\"label\":\"全部\"}'`",
		"- `node scripts/gcms.js list posts --lang zh --q 关键词`",
		"- `node scripts/gcms.js list posts --lang all --trans_group 分组值`",
		"- `node scripts/gcms.js get posts 123`",
		"- `node scripts/gcms.js preview posts 123`",
		"- `node scripts/gcms.js preview-url posts 123`",
		"- `node scripts/gcms.js preview links 123`",
		"- `node scripts/gcms.js create posts '{\"title\":\"标题\",\"content\":\"正文\",\"lang\":\"zh\",\"status\":\"draft\"}'`",
		"- `node scripts/gcms.js update posts 123 '{\"title\":\"新标题\"}'`",
		"- `node scripts/gcms.js audit posts --lang zh --limit 50`",
		"- `node scripts/gcms.js audit pages --lang zh --limit 20 --deep true`",
		"",
		"如果不能运行脚本，根据 `references/openapi.json` 直接发 HTTP 请求。",
	}, "\n") + "\n"
}

func automationSkillAgentYAML() string {
	return strings.Join([]string{
		"display_name: GCMS Content Assistant",
		"short_description: Diagnose, audit, preview drafts, upload media, and optimize GCMS content through the automation API.",
		"default_prompt: Run doctor, audit recent GCMS content for improvements, create drafts when useful, preview posts or links before publishing, and do not publish without explicit approval.",
	}, "\n") + "\n"
}

func automationSkillEnv(apiBase, token string) string {
	return strings.Join([]string{
		"GCMS_API_BASE=" + apiBase,
		"GCMS_API_KEY=" + token,
	}, "\n") + "\n"
}

func automationSkillScript() string {
	return `#!/usr/bin/env node
const fs = require("fs");
const path = require("path");

function loadEnv(file) {
  if (!fs.existsSync(file)) return;
  const lines = fs.readFileSync(file, "utf8").split(/\r?\n/);
  for (const line of lines) {
    const s = line.trim();
    if (!s || s.startsWith("#")) continue;
    const i = s.indexOf("=");
    if (i < 0) continue;
    const k = s.slice(0, i).trim();
    let v = s.slice(i + 1).trim();
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) {
      v = v.slice(1, -1);
    }
    if (!process.env[k]) process.env[k] = v;
  }
}

loadEnv(path.resolve(process.cwd(), ".env"));
loadEnv(path.resolve(__dirname, "..", ".env"));

const base = (process.env.GCMS_API_BASE || "").replace(/\/+$/, "");
const key = process.env.GCMS_API_KEY || "";
const collections = new Set(["posts", "pages", "links"]);

function usage(code = 2) {
  const out = code === 0 ? console.log : console.error;
  out("Usage:");
  out("  gcms.js help");
  out("  gcms.js doctor");
  out("  gcms.js languages");
  out("  gcms.js upload <file>");
  out("  gcms.js categories <posts|links> [--lang zh|all]");
  out("  gcms.js category-entry <posts|links> [--lang zh|all]");
  out("  gcms.js update-category-entry <posts|links> <json|@file>");
  out("  gcms.js list <posts|pages|links> [--lang zh|all] [--q text] [--slug slug] [--trans_group group] [--status draft] [--limit 20]");
  out("  gcms.js get <posts|pages|links> <id>");
  out("  gcms.js preview <posts|links> <id>");
  out("  gcms.js preview-url <posts|links> <id>");
  out("  gcms.js create <posts|pages|links> <json|@file>");
  out("  gcms.js update <posts|pages|links> <id> <json|@file>");
  out("  gcms.js audit <posts|pages|links> [--lang zh|all] [--limit 50] [--deep true]");
  process.exit(code);
}

function requireConfig() {
  if (!base || !key) {
    console.error("Missing GCMS_API_BASE or GCMS_API_KEY. Set environment variables or create .env.");
    process.exit(2);
  }
  if (typeof fetch !== "function") {
    console.error("This script needs Node.js 18+ with built-in fetch.");
    process.exit(2);
  }
}

function assertCollection(name) {
  if (!collections.has(name)) usage();
}

function parseOptions(args) {
  const out = {};
  for (let i = 0; i < args.length; i++) {
    const a = args[i];
    if (!a.startsWith("--")) usage();
    const k = a.slice(2);
    const v = args[++i];
    if (v == null || v.startsWith("--")) usage();
    out[k] = v;
  }
  return out;
}

function bodyFromArg(arg) {
  const raw = arg.startsWith("@") ? fs.readFileSync(arg.slice(1), "utf8") : arg;
  return JSON.parse(raw);
}

function mimeFromFile(file) {
  switch (path.extname(file).toLowerCase()) {
    case ".jpg":
    case ".jpeg":
      return "image/jpeg";
    case ".png":
      return "image/png";
    case ".gif":
      return "image/gif";
    case ".webp":
      return "image/webp";
    case ".svg":
      return "image/svg+xml";
    case ".ico":
      return "image/x-icon";
    case ".avif":
      return "image/avif";
    default:
      return "application/octet-stream";
  }
}

function mediaBodyFromFile(file) {
  if (typeof FormData !== "function" || typeof Blob !== "function") {
    console.error("Upload needs Node.js 18+ with FormData and Blob.");
    process.exit(2);
  }
  const bytes = fs.readFileSync(file);
  const form = new FormData();
  form.append("file", new Blob([bytes], { type: mimeFromFile(file) }), path.basename(file));
  return form;
}

function mediaProbeBody() {
  if (typeof FormData !== "function" || typeof Blob !== "function") {
    console.error("Doctor needs Node.js 18+ with FormData and Blob.");
    process.exit(2);
  }
  const form = new FormData();
  form.append("file", new Blob(["permission probe"], { type: "text/plain" }), "doctor.txt");
  return form;
}

async function rawRequest(method, urlPath, body) {
  requireConfig();
  const headers = { Authorization: "Bearer " + key, Accept: "application/json" };
  const init = { method, headers };
  if (body !== undefined) {
    if (typeof FormData !== "undefined" && body instanceof FormData) {
      init.body = body;
    } else {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
  }
  const res = await fetch(base + urlPath, init);
  const text = await res.text();
  let data;
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { raw: text };
  }
  return { ok: res.ok, status: res.status, data };
}

async function request(method, urlPath, body) {
  const result = await rawRequest(method, urlPath, body);
  const { ok, data } = result;
  if (!ok) {
    console.error(JSON.stringify(data, null, 2));
    process.exit(1);
  }
  return data;
}

function print(data) {
  console.log(JSON.stringify(data, null, 2));
}

function boolOption(value) {
  return value === true || value === "true" || value === "1" || value === "yes";
}

function auditItems(collection, data, options = {}) {
  const items = Array.isArray(data.items) ? data.items : [];
  const issues = [];
  for (const item of items) {
    const missing = [];
    if (!item.title) missing.push("title");
    if (!item.slug) missing.push("slug");
    if (!item.excerpt) missing.push("excerpt");
    if (!item.meta_desc) missing.push("meta_desc");
    if (!item.keywords) missing.push("keywords");
    if (collection !== "pages" && !item.category_id) missing.push("category_id");
    if (collection === "links" && !item.link_url) missing.push("link_url");
    if (!item.cover_image) missing.push("cover_image");
    if (options.deep && !item.content) missing.push("content");
    if (missing.length) {
      issues.push({
        id: item.id,
        type: item.type,
        lang: item.lang,
        status: item.status,
        slug: item.slug,
        title: item.title,
        missing
      });
    }
  }
  return {
    checked: items.length,
    issue_count: issues.length,
    issues
  };
}

async function auditCollection(collection, opt) {
  const deep = boolOption(opt.deep);
  delete opt.deep;
  if (!opt.limit) opt.limit = "50";
  const qs = new URLSearchParams(opt);
  const data = await request("GET", "/" + collection + (qs.toString() ? "?" + qs.toString() : ""));
  if (!deep) return auditItems(collection, data);
  const detailed = [];
  for (const item of Array.isArray(data.items) ? data.items : []) {
    const got = await request("GET", "/" + collection + "/" + encodeURIComponent(item.id));
    detailed.push(got.item || item);
  }
  return auditItems(collection, { items: detailed }, { deep: true });
}

async function doctor() {
  const result = {
    base,
    node: process.version,
    checks: []
  };
  const add = (name, ok, detail = {}) => {
    result.checks.push({ name, ok, ...detail });
  };
  if (!base) add("config_base", false, { message: "Missing GCMS_API_BASE" });
  else add("config_base", true);
  if (!key) add("config_key", false, { message: "Missing GCMS_API_KEY" });
  else add("config_key", true);
  if (typeof fetch !== "function") add("node_fetch", false, { message: "Node.js 18+ is required" });
  else add("node_fetch", true);
  if (!base || !key || typeof fetch !== "function") {
    result.ok = false;
    print(result);
    process.exit(1);
  }

  try {
    const openapi = await rawRequest("GET", "/openapi.json");
    add("openapi", openapi.ok, { status: openapi.status });
    if (openapi.ok) {
      const paths = openapi.data && openapi.data.paths ? openapi.data.paths : {};
      const schemas = openapi.data && openapi.data.components && openapi.data.components.schemas ? openapi.data.components.schemas : {};
      add("openapi_media_path", !!(paths["/media"] && paths["/media"].post));
      add("openapi_media_schema", !!schemas.MediaUploadResponse);
      add("openapi_post_preview_path", !!(paths["/posts/{id}/preview"] && paths["/posts/{id}/preview"].get));
      add("openapi_link_preview_path", !!(paths["/links/{id}/preview"] && paths["/links/{id}/preview"].get));
      add("openapi_preview_schema", !!schemas.ContentPreviewResponse && !!schemas.ContentPreview);
      add("openapi_post_all_entry_path", !!(paths["/posts/categories/all-entry"] && paths["/posts/categories/all-entry"].get && paths["/posts/categories/all-entry"].patch));
      add("openapi_link_all_entry_path", !!(paths["/links/categories/all-entry"] && paths["/links/categories/all-entry"].get && paths["/links/categories/all-entry"].patch));
      add("openapi_all_entry_schema", !!schemas.CategoryAllEntryResponse && !!schemas.CategoryAllEntryPatch);
    }
  } catch (err) {
    add("openapi", false, { message: err.message });
  }

  try {
    const languages = await rawRequest("GET", "/languages");
    const items = languages.data && Array.isArray(languages.data.items) ? languages.data.items : [];
    add("languages", languages.ok, { status: languages.status, count: items.length, default: languages.data && languages.data.default });
  } catch (err) {
    add("languages", false, { message: err.message });
  }

  for (const name of ["posts", "links"]) {
    try {
      const cats = await rawRequest("GET", "/" + name + "/categories?lang=zh");
      const items = cats.data && Array.isArray(cats.data.items) ? cats.data.items : [];
      add(name + "_categories", cats.ok, { status: cats.status, count: items.length });
    } catch (err) {
      add(name + "_categories", false, { message: err.message });
    }
    try {
      const entry = await rawRequest("GET", "/" + name + "/categories/all-entry?lang=zh");
      const item = entry.data && Array.isArray(entry.data.items) ? entry.data.items[0] : null;
      add(name + "_category_all_entry", entry.ok, { status: entry.status, path: item && item.path });
    } catch (err) {
      add(name + "_category_all_entry", false, { message: err.message });
    }
  }

  try {
    const media = await rawRequest("POST", "/media", mediaProbeBody());
    const mediaOK = media.status === 400 && media.data && media.data.error === "bad_type";
    add("media_write_permission", mediaOK, { status: media.status, error: media.data && media.data.error });
  } catch (err) {
    add("media_write_permission", false, { message: err.message });
  }

  result.ok = result.checks.every((check) => check.ok);
  print(result);
  process.exit(result.ok ? 0 : 1);
}

async function main() {
  const [cmd, collection, ...rest] = process.argv.slice(2);
  if (!cmd || cmd === "help" || cmd === "--help" || cmd === "-h") usage(0);

  if (cmd === "doctor") {
    await doctor();
    return;
  }

  if (cmd === "languages") {
    print(await request("GET", "/languages"));
    return;
  }

  if (cmd === "upload") {
    const [file] = [collection, ...rest];
    if (!file) usage();
    print(await request("POST", "/media", mediaBodyFromFile(file)));
    return;
  }

  if (cmd === "categories") {
    assertCollection(collection);
    if (collection === "pages") usage();
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", "/" + collection + "/categories" + (qs.toString() ? "?" + qs.toString() : "")));
    return;
  }

  if (cmd === "category-entry") {
    assertCollection(collection);
    if (collection === "pages") usage();
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", "/" + collection + "/categories/all-entry" + (qs.toString() ? "?" + qs.toString() : "")));
    return;
  }

  if (cmd === "update-category-entry") {
    assertCollection(collection);
    if (collection === "pages") usage();
    const [body] = rest;
    if (!body) usage();
    print(await request("PATCH", "/" + collection + "/categories/all-entry", bodyFromArg(body)));
    return;
  }

  assertCollection(collection);

  if (cmd === "list") {
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", "/" + collection + (qs.toString() ? "?" + qs.toString() : "")));
    return;
  }

  if (cmd === "get") {
    const [id] = rest;
    if (!id) usage();
    print(await request("GET", "/" + collection + "/" + encodeURIComponent(id)));
    return;
  }

  if (cmd === "preview") {
    const [id] = rest;
    if (!id || collection === "pages") usage();
    print(await request("GET", "/" + collection + "/" + encodeURIComponent(id) + "/preview"));
    return;
  }

  if (cmd === "preview-url") {
    const [id] = rest;
    if (!id || collection === "pages") usage();
    print(await request("POST", "/" + collection + "/" + encodeURIComponent(id) + "/preview-url"));
    return;
  }

  if (cmd === "create") {
    const [body] = rest;
    if (!body) usage();
    print(await request("POST", "/" + collection, bodyFromArg(body)));
    return;
  }

  if (cmd === "update") {
    const [id, body] = rest;
    if (!id || !body) usage();
    print(await request("PATCH", "/" + collection + "/" + encodeURIComponent(id), bodyFromArg(body)));
    return;
  }

  if (cmd === "audit") {
    const opt = parseOptions(rest);
    print(await auditCollection(collection, opt));
    return;
  }

  usage();
}

main().catch((err) => {
  console.error(err && err.message ? err.message : err);
  process.exit(1);
});
`
}
