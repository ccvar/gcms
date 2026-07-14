package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"cms.ccvar.com/internal/version"
)

// ===================== 平台级「薄」AI 技能包（多站，v2 P2） =====================
//
// 与站点包（gcms-content-assistant）的区别：
//   - apiBase 指向平台根 /api/platform/v1（不含 /sites/{id}），不烤任何站点清单；
//   - CLI 运行时先 `sites`（GET /sites 发现）解析可管站点，每条内容命令都要 --site <slug|id>；
//   - token 是平台密钥（gcmsp_ 前缀），一把可管多个站点。

const platformSkillFolder = "gcms-platform-assistant"

// adminDownloadPlatformSkill 下发平台薄包。GET 出无密钥模板（.env.example，gcmsp_xxx）；
// POST（带 CSRF）嵌入表单里的一次性明文平台密钥（校验 gcmsp_ 前缀）。仅平台模式可用。
func (s *Server) adminDownloadPlatformSkill(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	opts := automationSkillOptions{apiBase: s.absForPlatformRequest(r, "/api/platform/v1")}
	if r.Method == http.MethodPost {
		if _, ok := s.checkCSRF(w, r); !ok {
			return
		}
		opts.token = strings.TrimSpace(r.FormValue("token"))
		opts.name = strings.TrimSpace(r.FormValue("name"))
		opts.scopes = strings.TrimSpace(r.FormValue("scopes"))
		if opts.token == "" || !strings.HasPrefix(opts.token, "gcmsp_") {
			http.Error(w, "访问密钥无效", http.StatusBadRequest)
			return
		}
	}
	s.writePlatformSkillZip(w, opts)
}

func (s *Server) writePlatformSkillZip(w http.ResponseWriter, opts automationSkillOptions) {
	files, err := platformSkillFiles(opts)
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
	w.Header().Set("Content-Disposition", `attachment; filename="gcms-platform-ai-kit.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func platformSkillFiles(opts automationSkillOptions) ([]automationSkillFile, error) {
	// 参考用 OpenAPI：documented server 带 /sites/{siteId} 模板，说明每条路径都在站点前缀下。
	spec, err := json.MarshalIndent(automationOpenAPISpec(strings.TrimRight(opts.apiBase, "/")+"/sites/{siteId}"), "", "  ")
	if err != nil {
		return nil, err
	}
	files := []automationSkillFile{
		{name: "README.md", body: platformKitReadme(opts)},
		// 包版本标记（=服务端版本）：客户端导入/升级时读它记录版本，用于「有更新」提示。
		{name: platformSkillFolder + "/PACK_VERSION", body: version.Version + "\n"},
		{name: platformSkillFolder + "/AI助手说明.md", body: platformAssistantBriefMarkdown(opts)},
		{name: platformSkillFolder + "/SKILL.md", body: platformSkillMarkdown(opts.apiBase)},
		{name: platformSkillFolder + "/agents/openai.yaml", body: platformSkillAgentYAML()},
		{name: platformSkillFolder + "/references/openapi.json", body: string(spec) + "\n"},
		{name: platformSkillFolder + "/scripts/gcms.js", body: platformSkillScript()},
	}
	if opts.token != "" {
		files = append(files, automationSkillFile{name: platformSkillFolder + "/.env", body: automationSkillEnv(opts.apiBase, opts.token)})
	} else {
		files = append(files, automationSkillFile{name: platformSkillFolder + "/.env.example", body: automationSkillEnv(opts.apiBase, "gcmsp_xxx")})
	}
	return files, nil
}

func platformSkillAgentYAML() string {
	return strings.Join([]string{
		"display_name: GCMS Platform Assistant",
		"short_description: Manage many GCMS sites from one key: discover sites, then diagnose, audit, upload media, and edit content per --site.",
		"default_prompt: First run 'node scripts/gcms.js sites' to discover manageable sites. Every content command needs --site <slug|id>. Default to drafts; never publish without explicit approval.",
	}, "\n") + "\n"
}

func platformKitReadme(opts automationSkillOptions) string {
	lines := []string{
		"# GCMS 平台 AI 助手使用包（多站点）",
		"",
		"这个包让一把「平台密钥」通过一个入口管理多个 GCMS 站点。它不烤任何站点清单——AI 先运行发现命令 `node scripts/gcms.js sites` 拿到当前可管的站点，再对每个站点用 `--site <标识|ID>` 操作。",
		"",
		"## 包内文件",
		"",
		"- `README.md`：给人的快速使用指南。",
		"- `" + platformSkillFolder + "/AI助手说明.md`：给 AI 看的任务边界、发现流程和安全规则。",
		"- `" + platformSkillFolder + "/SKILL.md`：支持 Skill 的 AI 工具可直接读取的能力说明。",
		"- `" + platformSkillFolder + "/references/openapi.json`：单站自动化接口的 OpenAPI 描述（路径都在 `/sites/{siteId}` 前缀下）。",
		"- `" + platformSkillFolder + "/scripts/gcms.js`：命令行辅助脚本，内置站点发现与 `--site` 选择。",
		"- `" + platformSkillFolder + "/agents/openai.yaml`：可选的 agent 配置示例。",
		"- `" + platformSkillFolder + "/.env` 或 `.env.example`：本地连接配置。Finder 默认会隐藏这个文件。",
		"",
	}
	if opts.token != "" {
		name := opts.name
		if name == "" {
			name = "这个外部助手"
		}
		lines = append(lines,
			"后台已经为「"+name+"」生成平台访问密钥（gcmsp_ 前缀），并写入 `"+platformSkillFolder+"/.env`。",
			"这把密钥可以覆盖多个站点，泄露风险比单站密钥更大——只放在本地 `.env` 或受信任的 AI 编程工具里，绝不要贴进托管的聊天窗口。如果泄露，请回后台吊销，或用全局急停开关一键封停所有平台自动化。",
		)
		if opts.scopes != "" {
			lines = append(lines, "权限："+automationScopeLabels(strings.Split(opts.scopes, ",")))
		}
	} else {
		lines = append(lines,
			"这个包不包含访问密钥。请先在 GCMS 后台「站点管理 -> 平台 AI 技能包」创建一把平台密钥，再把一次性完整密钥填入 `"+platformSkillFolder+"/.env`。",
		)
	}
	lines = append(lines,
		"",
		"## 使用步骤",
		"",
		"1. 解压后保留完整目录结构，把 `"+platformSkillFolder+"` 文件夹交给 AI 工具读取。",
		"2. 让 AI 读取 `README.md`、`"+platformSkillFolder+"/AI助手说明.md`、`.env` 和 `references/openapi.json`。",
		"3. 让 AI 先运行 `node scripts/gcms.js sites` 查看可管站点，再运行 `node scripts/gcms.js doctor --site <标识>` 确认某站的连接与权限；只报告结果，不要创建或修改内容。",
		"4. 对 AI 说清楚任务时带上站点，例如：检查 `--site blog` 最近 10 篇文章标题，只给建议，不要发布。",
		"5. 需要新增站点时，无需重新下载本包：把站点加入这把密钥的授权范围（或密钥本就是「全部站点」模式），下次 `sites` 就能发现。",
		"6. 如果不再使用这个工具，请在后台吊销这把平台密钥。",
		"",
		"## 接口与认证",
		"",
		"- 平台根 API Base："+opts.apiBase,
		"- 站点发现："+strings.TrimRight(opts.apiBase, "/")+"/sites （返回该密钥可管的站点 `id`、`slug`、`name`、`capabilities`）",
		"- 单站请求：在平台根后拼 `/sites/{id}` 再接资源路径，例如 `"+strings.TrimRight(opts.apiBase, "/")+"/sites/12/posts`。",
		"- OpenAPI（单站）："+strings.TrimRight(opts.apiBase, "/")+"/sites/{id}/openapi.json",
		"- 认证方式：读取 `.env` 中的 `GCMS_API_KEY`，请求时使用 `Authorization: Bearer <GCMS_API_KEY>`。",
		"- 不要在普通聊天窗口、公开文档、日志或文章正文里暴露真实密钥。",
		"",
		"## 常用脚本命令",
		"",
		"- `node scripts/gcms.js sites`（先发现可管站点）",
		"- `node scripts/gcms.js doctor --site blog`",
		"- `node scripts/gcms.js languages --site blog`",
		"- `node scripts/gcms.js list posts --site blog --lang zh --limit 20`",
		"- `node scripts/gcms.js audit posts --site blog --lang zh`",
		"- `node scripts/gcms.js create posts --site blog '{\"title\":\"标题\",\"lang\":\"zh\",\"status\":\"draft\"}'`",
		"",
		"## 操作规则",
		"",
		"- 每条内容命令都必须带 `--site`；不带就先运行 `sites` 让用户选定目标站点，不要臆测。",
		"- 跨多个站点批量操作时，逐个站点确认后再执行，避免一次误改多个站。",
		"- 默认只创建或修改草稿，不要默认发布；只有用户明确说“发布”且密钥具备发布权限时才改状态。",
		"- 修改指定内容前，先在该站用 `q`、`slug` 或列表查到准确 `id`。",
		"- 设置内容分类前，先读取该站真实分类 ID；不要把 all-entry 当成 `category_id`。",
		"- 不要删除内容，不要改管理员账号、密码、安全设置、系统更新、Cloudflare 部署或密钥本身。",
		"- 接口返回权限不足、站点无权、分类不存在或找不到目标内容时，停止后续写入，报告错误。",
		"",
		"## 安全提醒",
		"",
		"- 平台密钥一把可管多个站点，泄露即多站失守——只放本地 `.env`，绝不贴进普通聊天窗口。",
		"- 一把平台密钥只给一个外部工具或平台使用。",
		"- 默认让 AI 创建或修改草稿，发布前先人工审核。",
		"- 需要紧急封停时，用后台的全局平台自动化急停开关，或吊销这把密钥。",
	)
	return strings.Join(lines, "\n") + "\n"
}

func platformAssistantBriefMarkdown(opts automationSkillOptions) string {
	token := opts.token
	if token == "" {
		token = "gcmsp_xxx"
	}
	base := strings.TrimRight(opts.apiBase, "/")
	name := strings.TrimSpace(opts.name)
	if name == "" {
		name = "外部 AI 助手（多站点）"
	}
	lines := []string{
		"# 给 AI 助手的说明（平台 / 多站点）",
		"",
		"用途：" + name,
		"",
		"你手上是一把 GCMS **平台密钥**，可以管理多个站点。你不知道有哪些站点——必须先发现，再逐站操作。",
		"",
		"## 连接",
		"",
		"- 平台根 API Base：`" + base + "`",
		"- 认证：`Authorization: Bearer " + token + "`（从环境变量 `GCMS_API_KEY` 读取，不要在回复里泄露）。",
		"",
		"## 第一步：发现站点",
		"",
		"- `GET " + base + "/sites` 返回 `{\"items\":[{\"id\",\"slug\",\"name\",\"capabilities\",\"api_base\"}],\"all_sites\":bool}`。",
		"- 或运行 `node scripts/gcms.js sites`。",
		"- 只有列表里的站点是你当前可管的；没出现的站点要么未授权，要么未开启自动化入口——不要臆测，直接告诉用户。",
		"",
		"## 第二步：对某个站点操作",
		"",
		"- 单站请求路径 = 平台根 + `/sites/{id}` + 资源路径。例如列文章：`GET " + base + "/sites/{id}/posts?lang=zh`。",
		"- 用脚本时，每条命令都要带 `--site <slug|id>`：`node scripts/gcms.js list posts --site blog --lang zh`。",
		"- 单站内的资源模型、分类、语种、发布规则与单站助手完全一致（见 SKILL.md 与 references/openapi.json）。",
		"",
		"## 规则",
		"",
		"- 每条内容操作都要明确目标站点；缺 `--site` 时先 `sites` 再让用户选定。",
		"- 默认只创建/修改草稿；只有用户明确要求且密钥有对应发布权限，才发布。",
		"- 修改前先在该站按 `q`/`slug`/列表查准确 `id`；设置分类先读真实分类 ID。",
		"- 不要删内容，不要碰账号、安全、更新、部署、密钥。",
		"- 跨站批量前逐站确认，避免一次误改多个站。",
		"",
		"## 使用提醒",
		"",
		"- 平台密钥可管多站，泄露风险更高：不要把真实密钥贴到普通聊天窗口。",
		"- 如果这个包泄露，请到 GCMS 后台吊销这把平台密钥，或使用全局急停开关。",
		"- 默认只创建或修改草稿，发布前请人工复核。",
	}
	return strings.Join(lines, "\n") + "\n"
}

func platformSkillMarkdown(apiBase string) string {
	base := strings.TrimRight(apiBase, "/")
	return strings.Join([]string{
		"---",
		"name: gcms-platform-assistant",
		"description: Use this skill to operate MANY GCMS sites through one platform automation key. First discover manageable sites via GET /sites, then for each site (identified by --site <slug|id>, path-prefixed with /sites/{id}) run diagnostics, audit posts/pages/links, upload media, create or update drafts, improve SEO, handle categories and multilingual content, and publish only with explicit approval and permission.",
		"---",
		"",
		"# GCMS Platform Assistant（多站点）",
		"",
		"你是 GCMS 平台内容助手。你持有一把可管理多个站点的平台密钥。核心区别：**先发现站点，再逐站操作**；每一次内容读写都要指明目标站点。不要增删改安全、系统更新、Cloudflare 部署或密钥本身。",
		"",
		"## 连接方式",
		"",
		"- 平台根 API Base: `" + base + "`",
		"- 站点发现: `GET /sites` → `{items:[{id,slug,name,capabilities,api_base}], all_sites}`",
		"- 单站请求: 平台根 + `/sites/{id}` + 资源路径（例如 `/sites/12/posts`）。",
		"- 单站 OpenAPI: `GET /sites/{id}/openapi.json`；本包 `references/openapi.json` 是单站接口的参考（server 写作 `/sites/{siteId}`）。",
		"- 优先从环境变量读取 `GCMS_API_BASE` 与 `GCMS_API_KEY`；不要在回复里泄露访问密钥。",
		"",
		"## 工作流程",
		"",
		"1. 先 `GET /sites`（或 `node scripts/gcms.js sites`）拿到可管站点；把 slug/id 记住供后续使用。",
		"2. 针对某站诊断：`node scripts/gcms.js doctor --site <slug|id>`（检查该站 OpenAPI、分类、媒体权限）。",
		"3. 之后所有读写都在 `/sites/{id}` 前缀下进行；脚本命令统一带 `--site`。",
		"4. 单站内部规则（分类、语种、trans_group、preview、pin、发布审核）与单站助手一致。",
		"",
		"## 站点选择规则",
		"",
		"- 每条内容命令都必须带 `--site`；缺失时先 `sites` 再请用户指定，绝不臆测目标站。",
		"- 发现列表之外的站点一律视为不可管——直接说明，不要尝试 `/sites/{未知id}`。",
		"- 需要跨多个站点执行同一操作时，逐站确认后再执行，并在结果里分站汇报。",
		"",
		"## 单站内容规则（与单站助手一致）",
		"",
		"1. 修改某篇内容前，先用 `q` 或 `slug` 在该站查到准确 `id`。",
		"2. 新环境或接口异常时，先 `doctor --site <slug>`。",
		"3. 查到多个相似结果先让用户确认。",
		"4. 设置内容分类只能用该站 `GET /posts/categories` / `GET /links/categories` 返回的真实分类 ID；all-entry 不是分类。",
		"5. 需要封面或正文图片时，先把图片转成 WebP（.webp），再 `POST /media` 上传，拿返回 `url` 写入字段。",
		"6. 处理多语种内容时，先 `GET /languages` 查启用语种；要更新同组内容，先读 `trans_group` 再逐条按 id 更新。",
		"6b. `trans_group` 仅创建时可设、普通 update 不改它；要给已存在内容补/改互译关联，用 `POST /{collection}/{id}/relink`，body 传 `{\"link_to_id\": <兄弟内容 id>}`（推荐）或 `{\"trans_group\": \"<组键>\"}`（每种语言一组只能有一篇）。",
		"7. 默认只创建或修改草稿。",
		"8. 只有用户明确要求发布、且密钥有对应资源发布权限时，才把 `status` 设为 `published` 或 `scheduled`；发布前优先用 `GET /posts/{id}/preview` 或 `/links/{id}/preview` 复核。",
		"9. 完成后告诉用户：改了哪个站、哪些内容、对应 id、语种、状态，以及建议人工复核的点。",
		"",
		"## 推荐脚本",
		"",
		"如果当前环境可以运行 Node.js，优先使用 `scripts/gcms.js`（每条内容命令都带 `--site`）：",
		"",
		"- `node scripts/gcms.js sites`",
		"- `node scripts/gcms.js doctor --site blog`",
		"- `node scripts/gcms.js languages --site blog --all`",
		"- `node scripts/gcms.js site-profile --site blog`（读取站点文案 / Hero / 首页标题）",
		"- `node scripts/gcms.js site-profile-update --site blog '{\"lang\":\"zh\",\"hero_title\":\"新标题\"}'`",
		"- `node scripts/gcms.js navigation --site blog`",
		"- `node scripts/gcms.js navigation-update --site blog @nav.json`",
		"- `node scripts/gcms.js categories posts --site blog --lang zh`",
		"- `node scripts/gcms.js list posts --site blog --lang zh --q 关键词`",
		"- `node scripts/gcms.js get posts --site blog 123`",
		"- `node scripts/gcms.js preview posts --site blog 123`",
		"- `node scripts/gcms.js pin posts --site blog 123 on`",
		"- `node scripts/gcms.js create posts --site blog '{\"title\":\"标题\",\"content\":\"正文\",\"lang\":\"zh\",\"status\":\"draft\"}'`",
		"- `node scripts/gcms.js update posts --site blog 123 '{\"title\":\"新标题\"}'`",
		"- `node scripts/gcms.js audit posts --site blog --lang zh --limit 50`",
		"- `node scripts/gcms.js search-stats --site blog --days 28 --limit 100`（stats:read：Search Console 搜索词表现，找排名 8~20 的词优化旧文）",
		"- `node scripts/gcms.js traffic-stats --site blog --days 7`（stats:read：GA 活跃用户/会话汇总；结果缓存 1 小时，未接入返回明确错误码）",
		"",
		"## 扩展内容类型（产品/文档/活动/图库/自定义）",
		"",
		"每个站不只有 posts/pages/links——先 `node scripts/gcms.js types --site blog` 看该站启用的扩展类型，",
		"字段 schema 即操作契约：list/get/create/update/relink/audit 对扩展集合同样可用，",
		"自定义字段放 `\"fields\": {\"字段key\": 值}`；层级类型的上级/排序放 `fields.parent`/`fields.order`。",
		"",
		"- `node scripts/gcms.js types --site blog --all`",
		"- `node scripts/gcms.js type-enable product --site blog`（type-disable 只下线前台归档与自省，API 读写不受影响）",
		"- `node scripts/gcms.js create products --site blog '{\"title\":\"入门款\",\"status\":\"draft\",\"fields\":{\"price\":199}}'`",
		"- `node scripts/gcms.js type-create --site blog '{\"key\":\"cases\",\"name\":\"案例\",\"fields\":[{\"key\":\"client\",\"label\":\"客户\",\"type\":\"text\",\"required\":true}]}'`",
		"",
		"**创建新类型前必须先把内容模型讲给用户并获得同意**；type-delete 只对空类型有效；内置类型只能启停。",
		"",
		"如果不能运行脚本，先 `GET /sites` 发现站点，再根据 `references/openapi.json` 对 `/sites/{id}/...` 直接发 HTTP 请求。",
	}, "\n") + "\n"
}

// platformSkillScript 是站点包 gcms.js 的多站分支：base 指平台根，内置 sites 发现与 --site 选择，
// 每条内容命令的路径都在 /sites/{id} 前缀下。
func platformSkillScript() string {
	return skillScriptPlatform
}
