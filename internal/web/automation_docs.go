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

func automationOpenAPISpec(apiBase string) map[string]any {
	paths := map[string]any{
		"/languages": map[string]any{
			"get": automationLanguagesOperation(),
		},
		"/media": map[string]any{
			"post": automationMediaUploadOperation(),
		},
	}
	for _, col := range automationCollections {
		if col.path == "posts" || col.path == "links" {
			paths["/"+col.path+"/categories"] = map[string]any{
				"get": automationCategoryListOperation(col),
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
			"description": "开放语种、文章分类、链接分类读取、媒体上传、文章与链接草稿预览，以及文章、链接、页面的自动化接口。GCMS 不调用 AI API，外部 AI 工具或自动化程序使用访问密钥调用这里的接口。",
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

func automationCategoryListOperation(col automationCollection) map[string]any {
	return map[string]any{
		"summary":     "列出" + col.label + "分类",
		"description": "只读接口。用于拿到可用分类 ID，创建或更新内容时可写入 category_id。",
		"operationId": "list" + automationOperationSuffix(col.kind+"Categories"),
		"tags":        []string{col.label},
		"parameters": []map[string]any{
			{"name": "lang", "in": "query", "schema": map[string]any{"type": "string", "default": "zh"}, "description": "分类语种。传 all 可返回所有语种的分类。"},
		},
		"responses": automationResponses("CategoryListResponse"),
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
		{"write", "admin.settings.automation.write_draft", "写草稿"},
		{"publish", "admin.settings.automation.publish", "发布"},
	} {
		if scopes[resource+":"+action.key] {
			labels = append(labels, adminUI(admin, action.i18nKey, action.fallback))
		}
	}
	return labels
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
		"- 检查最近 50 篇中文文章，重点看标题、摘要、SEO 描述、关键词、分类、封面图是否缺失；只输出问题列表和建议，不要修改。",
		"- 深度检查最近 20 个页面，逐条读取正文，找出缺正文、缺封面、SEO 描述太弱或标题不清楚的页面；按优先级列出 ID、标题、问题和建议。",
		"- 根据我提供的资料创建一篇中文文章草稿；先查询文章分类并选择合适的 `category_id`，有封面图时先上传媒体并把返回 URL 写入 `cover_image`；状态保持 `draft`。",
		"- 把标题包含某个关键词的文章摘要和 SEO 描述优化一下；先用 `q` 或 `slug` 找到准确 ID，多个结果先让我确认；不要改正文、slug 或发布时间。",
		"- 把我提供的图片上传到媒体接口，拿返回 URL 后插入到指定文章正文的合适位置；保留原有正文结构。",
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
		"- 设置分类前，让 AI 先用 `/posts/categories` 或 `/links/categories` 查询可用分类 ID。",
		"- 设置封面或正文图片前，让 AI 先用 `POST /media` 上传文件，拿返回的 `url` 再写入 `cover_image` 或 Markdown 图片。",
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
		"你是 GCMS 网站内容助手。你可以读取语种和分类、上传媒体，并处理文章、页面、链接。不要增删改站点设置、分类、导航、安全、系统更新。",
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
		"- `media`：上传用户提供的文件，把返回 URL 用于封面或正文图片。",
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
		"4. 需要设置分类时，先用 `GET /posts/categories?lang=...` 或 `GET /links/categories?lang=...` 查询可用分类 ID。",
		"5. 需要封面或正文图片时，先用 `POST /media` 上传文件，拿返回的 `url` 再写入 `cover_image` 或 Markdown 图片。",
		"6. 处理多语种内容时，先 `GET /languages` 查看启用语种；如果用户要求更新全部语种，先读取目标内容的 `trans_group`，再用 `lang=all&trans_group=...` 找到同组所有版本，逐条按 id 更新。",
		"7. 不要把一个语种的正文直接覆盖到其它语种，除非用户明确要求这么做。",
		"8. 默认只创建或修改草稿。",
		"9. 只有用户明确要求发布，并且访问密钥有对应资源的发布权限，才设置 `status` 为 `published` 或 `scheduled`。",
		"10. 发布前优先用 `GET /posts/{id}/preview` 或 `GET /links/{id}/preview` 复核草稿渲染结果；需要浏览器复核时再生成 `preview-url`。",
		"11. 完成后告诉用户变更了哪些内容、对应 id、语种、状态，以及建议人工复核的点。",
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
