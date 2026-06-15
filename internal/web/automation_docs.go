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

func (s *Server) adminDownloadAutomationSkill(w http.ResponseWriter, r *http.Request) {
	opts := automationSkillOptions{apiBase: s.absForRequest(r, "/api/admin/v1")}
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
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "GCMS Automation API",
			"version":     "1.0.0",
			"description": "开放语种、文章分类、链接分类读取，以及文章、链接、页面的自动化接口。GCMS 不调用 AI API，外部 AI 工具或自动化程序使用访问密钥调用这里的接口。",
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
			"2. 对 AI 说清楚任务，例如：检查最近 10 篇文章标题，只给建议，不要发布。",
			"3. 如果不再使用这个工具，请在后台吊销对应访问权限。",
		)
	} else {
		lines = append(lines,
			"1. 把 `gcms-content-assistant/.env.example` 复制为 `.env`。",
			"2. 填入 `GCMS_API_BASE` 和 `GCMS_API_KEY`。",
			"3. 把 `gcms-content-assistant` 文件夹交给 AI 工具读取。",
			"4. 对 AI 说清楚任务，例如：检查最近 10 篇文章标题，只给建议，不要发布。",
		)
	}
	lines = append(lines,
		"",
		"接口地址："+opts.apiBase,
		"OpenAPI 文件："+strings.TrimRight(opts.apiBase, "/")+"/openapi.json",
		"",
		"## 安全提醒",
		"",
		"- 一个访问密钥只给一个外部工具或平台使用。",
		"- 不要把真实访问密钥发到普通聊天窗口。",
		"- 默认让 AI 创建或修改草稿，发布前先人工审核。",
		"- 修改指定内容时，让 AI 先查 id，再按 id 更新。",
		"- 设置分类前，让 AI 先用 `/posts/categories` 或 `/links/categories` 查询可用分类 ID。",
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
		"description: Use this skill to operate a GCMS site through its automation API for languages, categories, posts, pages, and links. Use it when asked to inspect, create drafts, update drafts, or publish content in GCMS.",
		"---",
		"",
		"# GCMS Content Assistant",
		"",
		"你是 GCMS 网站内容助手。你可以读取语种和分类，并处理文章、页面、链接。不要增删改站点设置、分类、导航、安全、系统更新。",
		"",
		"## 连接方式",
		"",
		"- API Base: `" + apiBase + "`",
		"- OpenAPI: `references/openapi.json`",
		"- 优先从环境变量读取 `GCMS_API_BASE` 与 `GCMS_API_KEY`。",
		"- 不要在普通回复里泄露访问密钥。",
		"",
		"## 工作规则",
		"",
		"1. 修改某篇内容前，先用 `q` 或 `slug` 查到准确 `id`。",
		"2. 如果查到多个相似结果，先让用户确认。",
		"3. 需要设置分类时，先用 `GET /posts/categories?lang=...` 或 `GET /links/categories?lang=...` 查询可用分类 ID。",
		"4. 处理多语种内容时，先 `GET /languages` 查看启用语种；如果用户要求更新全部语种，先读取目标内容的 `trans_group`，再用 `lang=all&trans_group=...` 找到同组所有版本，逐条按 id 更新。",
		"5. 不要把一个语种的正文直接覆盖到其它语种，除非用户明确要求这么做。",
		"6. 默认只创建或修改草稿。",
		"7. 只有用户明确要求发布，并且访问密钥有对应资源的发布权限，才设置 `status` 为 `published` 或 `scheduled`。",
		"8. 完成后告诉用户变更了哪些内容、对应 id、语种、状态，以及建议人工复核的点。",
		"",
		"## 推荐脚本",
		"",
		"如果当前环境可以运行 Node.js，优先使用 `scripts/gcms.js`：",
		"",
		"- `node scripts/gcms.js languages`",
		"- `node scripts/gcms.js categories posts --lang zh`",
		"- `node scripts/gcms.js categories links --lang zh`",
		"- `node scripts/gcms.js list posts --lang zh --q 关键词`",
		"- `node scripts/gcms.js list posts --lang all --trans_group 分组值`",
		"- `node scripts/gcms.js get posts 123`",
		"- `node scripts/gcms.js create posts '{\"title\":\"标题\",\"content\":\"正文\",\"lang\":\"zh\",\"status\":\"draft\"}'`",
		"- `node scripts/gcms.js update posts 123 '{\"title\":\"新标题\"}'`",
		"",
		"如果不能运行脚本，根据 `references/openapi.json` 直接发 HTTP 请求。",
	}, "\n") + "\n"
}

func automationSkillAgentYAML() string {
	return strings.Join([]string{
		"display_name: GCMS Content Assistant",
		"short_description: Operate GCMS languages, categories, posts, pages, and links through the automation API.",
		"default_prompt: Check recent GCMS content for improvements, create drafts when useful, and do not publish without explicit approval.",
	}, "\n") + "\n"
}

func automationSkillEnv(apiBase, token string) string {
	return strings.Join([]string{
		"GCMS_API_BASE=" + apiBase,
		"GCMS_API_KEY=" + token,
	}, "\n") + "\n"
}

func automationSkillScript() string {
	return strings.Join([]string{
		"#!/usr/bin/env node",
		"const fs = require('fs');",
		"const path = require('path');",
		"",
		"function loadEnv(file) {",
		"  if (!fs.existsSync(file)) return;",
		"  const lines = fs.readFileSync(file, 'utf8').split(/\\r?\\n/);",
		"  for (const line of lines) {",
		"    const s = line.trim();",
		"    if (!s || s.startsWith('#')) continue;",
		"    const i = s.indexOf('=');",
		"    if (i < 0) continue;",
		"    const k = s.slice(0, i).trim();",
		"    let v = s.slice(i + 1).trim();",
		"    if ((v.startsWith('\"') && v.endsWith('\"')) || (v.startsWith(\"'\") && v.endsWith(\"'\"))) v = v.slice(1, -1);",
		"    if (!process.env[k]) process.env[k] = v;",
		"  }",
		"}",
		"",
		"loadEnv(path.resolve(process.cwd(), '.env'));",
		"loadEnv(path.resolve(__dirname, '..', '.env'));",
		"",
		"const base = (process.env.GCMS_API_BASE || '').replace(/\\/+$/, '');",
		"const key = process.env.GCMS_API_KEY || '';",
		"const collections = new Set(['posts', 'pages', 'links']);",
		"",
		"function usage() {",
		"  console.error('Usage:');",
		"  console.error('  gcms.js languages');",
		"  console.error('  gcms.js categories <posts|links> [--lang zh|all]');",
		"  console.error('  gcms.js list <posts|pages|links> [--lang zh|all] [--q text] [--slug slug] [--trans_group group] [--status draft] [--limit 20]');",
		"  console.error('  gcms.js get <posts|pages|links> <id>');",
		"  console.error('  gcms.js create <posts|pages|links> <json|@file>');",
		"  console.error('  gcms.js update <posts|pages|links> <id> <json|@file>');",
		"  process.exit(2);",
		"}",
		"",
		"function requireConfig() {",
		"  if (!base || !key) {",
		"    console.error('Missing GCMS_API_BASE or GCMS_API_KEY. Copy .env.example to .env and fill it first.');",
		"    process.exit(2);",
		"  }",
		"  if (typeof fetch !== 'function') {",
		"    console.error('This script needs Node.js 18+ with built-in fetch.');",
		"    process.exit(2);",
		"  }",
		"}",
		"",
		"function assertCollection(name) {",
		"  if (!collections.has(name)) usage();",
		"}",
		"",
		"function parseOptions(args) {",
		"  const out = {};",
		"  for (let i = 0; i < args.length; i++) {",
		"    const a = args[i];",
		"    if (!a.startsWith('--')) usage();",
		"    const key = a.slice(2);",
		"    const val = args[++i];",
		"    if (val == null) usage();",
		"    out[key] = val;",
		"  }",
		"  return out;",
		"}",
		"",
		"function bodyFromArg(arg) {",
		"  const raw = arg.startsWith('@') ? fs.readFileSync(arg.slice(1), 'utf8') : arg;",
		"  return JSON.parse(raw);",
		"}",
		"",
		"async function request(method, urlPath, body) {",
		"  requireConfig();",
		"  const headers = { Authorization: 'Bearer ' + key, Accept: 'application/json' };",
		"  const init = { method, headers };",
		"  if (body !== undefined) {",
		"    headers['Content-Type'] = 'application/json';",
		"    init.body = JSON.stringify(body);",
		"  }",
		"  const res = await fetch(base + urlPath, init);",
		"  const text = await res.text();",
		"  let data;",
		"  try { data = text ? JSON.parse(text) : {}; } catch { data = { raw: text }; }",
		"  if (!res.ok) {",
		"    console.error(JSON.stringify(data, null, 2));",
		"    process.exit(1);",
		"  }",
		"  console.log(JSON.stringify(data, null, 2));",
		"}",
		"",
		"async function main() {",
		"  const [cmd, collection, maybeID, maybeBody, ...rest] = process.argv.slice(2);",
		"  if (cmd === 'languages') {",
		"    await request('GET', '/languages');",
		"    return;",
		"  }",
		"  if (cmd === 'categories') {",
		"    assertCollection(collection);",
		"    if (collection === 'pages') usage();",
		"    const opt = parseOptions([maybeID, maybeBody, ...rest].filter(Boolean));",
		"    const qs = new URLSearchParams(opt);",
		"    await request('GET', '/' + collection + '/categories' + (qs.toString() ? '?' + qs.toString() : ''));",
		"    return;",
		"  }",
		"  assertCollection(collection);",
		"  if (cmd === 'list') {",
		"    const opt = parseOptions([maybeID, maybeBody, ...rest].filter(Boolean));",
		"    const qs = new URLSearchParams(opt);",
		"    await request('GET', '/' + collection + (qs.toString() ? '?' + qs.toString() : ''));",
		"    return;",
		"  }",
		"  if (cmd === 'get') {",
		"    if (!maybeID) usage();",
		"    await request('GET', '/' + collection + '/' + encodeURIComponent(maybeID));",
		"    return;",
		"  }",
		"  if (cmd === 'create') {",
		"    if (!maybeID) usage();",
		"    await request('POST', '/' + collection, bodyFromArg(maybeID));",
		"    return;",
		"  }",
		"  if (cmd === 'update') {",
		"    if (!maybeID || !maybeBody) usage();",
		"    await request('PATCH', '/' + collection + '/' + encodeURIComponent(maybeID), bodyFromArg(maybeBody));",
		"    return;",
		"  }",
		"  usage();",
		"}",
		"",
		"main().catch((err) => { console.error(err && err.message ? err.message : err); process.exit(1); });",
	}, "\n") + "\n"
}
