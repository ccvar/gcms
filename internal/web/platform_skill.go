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
	controlSpec, err := json.MarshalIndent(platformControlOpenAPISpec(opts.apiBase), "", "  ")
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
		{name: platformSkillFolder + "/references/control-api.json", body: string(controlSpec) + "\n"},
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
		"short_description: Manage the GCMS site lifecycle, themes, domain records, and per-site content from one platform key.",
		"default_prompt: First run 'node scripts/gcms.js capabilities'. For management writes, including category or navigation deletion, always run the matching *-plan command, show its impact, and wait for explicit approval before execution. Never ask for an admin password; Pilot UI handles required unlocks. Default content to drafts; never publish without explicit approval.",
	}, "\n") + "\n"
}

func platformKitReadme(opts automationSkillOptions) string {
	lines := []string{
		"# GCMS 平台 AI 助手使用包（多站点）",
		"",
		"这个包让一把「平台密钥」通过一个入口管理 GCMS 站点生命周期、外观主题、内部域名配置和站内内容。AI 必须先读取实时能力，再运行发现命令；所有管理写操作先预检查、展示影响并等待用户明确确认。",
		"",
		"## 包内文件",
		"",
		"- `README.md`：给人的快速使用指南。",
		"- `" + platformSkillFolder + "/AI助手说明.md`：给 AI 看的任务边界、发现流程和安全规则。",
		"- `" + platformSkillFolder + "/SKILL.md`：支持 Skill 的 AI 工具可直接读取的能力说明。",
		"- `" + platformSkillFolder + "/references/openapi.json`：单站自动化接口的 OpenAPI 描述（路径都在 `/sites/{siteId}` 前缀下）。",
		"- `" + platformSkillFolder + "/references/control-api.json`：站点生命周期、主题、域名与安全状态的控制接口契约。",
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
		"2. 让 AI 读取 `README.md`、`"+platformSkillFolder+"/AI助手说明.md`、`.env`、`references/openapi.json` 和 `references/control-api.json`。",
		"3. 让 AI 先运行 `node scripts/gcms.js capabilities` 读取当前服务端的管理能力与风险契约，再运行 `node scripts/gcms.js sites` 查看可管站点。",
		"4. 运行 `node scripts/gcms.js doctor --site <标识>` 确认某站的连接与权限；只报告结果，不要创建或修改内容。",
		"5. 对 AI 说清楚任务时带上站点，例如：检查 `--site blog` 最近 10 篇文章标题，只给建议，不要发布。",
		"6. 新建站点先运行 `site-create-plan`；只有用户确认预检查结果后才能运行 `site-create`。创建站点要求密钥使用「全部站点」成员模式。",
		"7. 如果不再使用这个工具，请在后台吊销这把平台密钥。",
		"",
		"## 接口与认证",
		"",
		"- 平台根 API Base："+opts.apiBase,
		"- 站点发现："+strings.TrimRight(opts.apiBase, "/")+"/sites （返回该密钥可管的站点 `id`、`slug`、`name`、`capabilities`）",
		"- 单站请求：在平台根后拼 `/sites/{id}` 再接资源路径，例如 `"+strings.TrimRight(opts.apiBase, "/")+"/sites/12/posts`。",
		"- 管理请求：平台根后拼 `/control`，例如 `"+strings.TrimRight(opts.apiBase, "/")+"/control/sites`。管理写操作要求 `X-GCMS-Control-Confirm` 与 `Idempotency-Key`。",
		"- OpenAPI（单站）："+strings.TrimRight(opts.apiBase, "/")+"/sites/{id}/openapi.json",
		"- OpenAPI（平台控制）："+strings.TrimRight(opts.apiBase, "/")+"/control/openapi.json",
		"- 认证方式：读取 `.env` 中的 `GCMS_API_KEY`，请求时使用 `Authorization: Bearer <GCMS_API_KEY>`。",
		"- 不要在普通聊天窗口、公开文档、日志或文章正文里暴露真实密钥。",
		"",
		"## 常用脚本命令",
		"",
		"- `node scripts/gcms.js capabilities`（读取平台管理能力、风险级别和是否需要用户解锁）",
		"- `node scripts/gcms.js sites`（先发现可管站点）",
		"- `node scripts/gcms.js control-sites`（管理视图，包含当前密钥成员范围内已关闭的站点）",
		"- `node scripts/gcms.js site-create-plan '{\"slug\":\"blog\",\"name\":\"品牌博客\",\"site_kind\":\"content\"}'`（只检查，不落库；默认空站并开启后续自动化）",
		"- `node scripts/gcms.js themes`（读取可供 AI 比较的主题风格、分类与布局）",
		"- `node scripts/gcms.js theme-plan --site blog editorial`（只检查主题切换影响）",
		"- `node scripts/gcms.js domains-plan --site blog @domains.json`（只检查 GCMS 域名配置；不会改 DNS/Caddy）",
		"- `node scripts/gcms.js category-delete-plan --site blog posts 12`（只检查分类删除影响；如预检建议清理导航，可在确认后加 `--remove-navigation true`）",
		"- `node scripts/gcms.js navigation-delete-plan --site blog 2`（只检查导航项删除影响，不会删除；index 是最新导航 items 数组从 0 开始的位置）",
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
		"- 站点创建、修改、归档删除、主题、域名配置以及分类/导航删除必须先运行对应 `*-plan` 命令；把 normalized_input、impact、warnings 原样展示给用户，收到明确确认后才运行实际命令。",
		"- 创建站点不传 `seed_mode` 时默认 `empty`，不写演示内容；不传 `management_automation_enabled` 时默认开启，确保 Pilot 能继续建设。",
		"- 相同实际请求重试时复用同一个 `--request-id`；不同操作或不同输入必须换新值，防止重复建站或误重放。",
		"- domains.apply 只写内部主域名/跳转域名；需要完整公网访问时使用 public_access.apply，由 GCMS 自己调用已配置的 DNS/Cloudflare、同步 Caddy 并返回 HTTPS 验证状态。",
		"- 不要删除文章、页面、链接或商品等内容。分类和导航项只能通过受控的 `category-delete-*` / `navigation-delete-*` 流程删除，禁止用普通写接口或后台私有接口绕过预检和密码解锁。",
		"- 删除分类前必须核对预检中的关联内容、语种、导航引用与 warnings；关联内容可能变为未分类。删除导航项前先读取最新导航并使用 items 数组从 0 开始的位置作为 index，禁止凭记忆猜索引。",
		"- 不要直接改管理员账号、密码、系统更新、Cloudflare 配置或密钥本身。初始密码没有 HTTP 写入口，只能由 Pilot 通过服务器上的 GCMS 专用 CLI 设置。",
		"- **AI 不得询问、读取、记录或代为输入 GCMS 后台密码**。能力契约标记 `requires_unlock: true` 时，只能请 Pilot 原生界面向用户收集密码并完成短时授权。",
		"- 正式操作返回 `unlock_required` 时，回复必须原样包含 `unlock_required` 和对应 operation id，并请用户在 Pilot 原生密码框验证；不要换 request-id，也不要改目标。Pilot 验证后会自动重试。",
		"- `available: false` 或 `granted: false` 的控制能力不得执行，也不得猜测路由或用后台私有接口绕过。",
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
		"你手上是一把 GCMS **平台密钥**，可以在授权范围内管理站点生命周期、主题、GCMS 内部域名和内容。必须先读取实时能力，再预检查，再确认执行。",
		"",
		"## 连接",
		"",
		"- 平台根 API Base：`" + base + "`",
		"- 认证：`Authorization: Bearer " + token + "`（从环境变量 `GCMS_API_KEY` 读取，不要在回复里泄露）。",
		"",
		"## 第一步：发现站点",
		"",
		"- 先运行 `node scripts/gcms.js capabilities`，以服务端实时返回的 `available`、`granted`、`risk` 和 `requires_unlock` 为准，不要根据记忆猜测能力。",
		"- `GET " + base + "/sites` 返回 `{\"items\":[{\"id\",\"slug\",\"name\",\"capabilities\",\"api_base\"}],\"all_sites\":bool}`。",
		"- 或运行 `node scripts/gcms.js sites`。",
		"- 管理已关闭站点时用 `node scripts/gcms.js control-sites`；它只返回这把密钥的成员范围，不得尝试列表外的 ID。",
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
		"- 管理写操作必须先运行对应 `*-plan`；分类/导航删除也不例外。向用户展示影响与警告，用户明确确认后才执行，并为同一次重试复用稳定 request-id。",
		"- 主题由 AI 根据主题接口返回的真实 description/category/layout 推荐，不得编造主题 ID。",
		"- public_access.apply 才代表完整公网访问流程；必须根据返回的 dns、caddy、https 状态判断是否真正上线，不能把保存域名误报为已完成。",
		"- 不要删除文章、页面、链接或商品等内容。分类/导航项只能在预检、用户确认和 Pilot 密码解锁后通过受控删除命令处理；不得调用后台私有接口绕过。",
		"- 分类删除前逐项核对关联内容、语种和导航引用；导航删除的 index 是最新导航 items 数组从 0 开始的位置，不能根据旧列表猜测。",
		"- 不要直接碰账号密码、更新、部署或密钥。初始密码没有控制 API 写入口，只能由 Pilot 通过服务器上的 GCMS 专用 CLI 设置。",
		"- AI 不得要求用户在对话或命令行中提供后台密码。如需高风险操作，只能触发 Pilot UI 的密码解锁流程。",
		"- 收到 `unlock_required` 时原样报告该错误及 operation id，等待 Pilot 原生密码框完成验证；随后用相同 request-id 重试同一目标。",
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
		"description: Use this skill to manage GCMS site lifecycle, themes, internal domain records, and per-site content through one platform key. Read /control/capabilities first; management writes require dry-run review, explicit approval, confirmation and idempotency. Password entry remains Pilot-UI-only.",
		"---",
		"",
		"# GCMS Platform Assistant（多站点）",
		"",
		"你是 GCMS 平台运营助手。你持有一把可管理多个站点的平台密钥。核心流程是：**读能力 → 发现站点 → 预检查 → 用户确认 → 幂等执行**。你可以使用公开控制 API 管理站点、主题和 GCMS 内部域名，但不能接触后台密码，也不能绕过 Pilot 操作服务器、Cloudflare 或密钥。",
		"",
		"## 连接方式",
		"",
		"- 平台根 API Base: `" + base + "`",
		"- 站点发现: `GET /sites` → `{items:[{id,slug,name,capabilities,api_base}], all_sites}`",
		"- 控制入口: `GET /control/capabilities` 和 `/control/*`；管理已关闭站点用 `GET /control/sites`。",
		"- 写操作: 先加 `?dry_run=1` 预检查；执行时必须带 `X-GCMS-Control-Confirm: <operation>` 与稳定的 `Idempotency-Key`。",
		"- 单站请求: 平台根 + `/sites/{id}` + 资源路径（例如 `/sites/12/posts`）。",
		"- 单站 OpenAPI: `GET /sites/{id}/openapi.json`；本包 `references/openapi.json` 是单站接口的参考（server 写作 `/sites/{siteId}`）。",
		"- 控制 OpenAPI: `GET /control/openapi.json`；本包 `references/control-api.json` 固化同一份控制契约。",
		"- 优先从环境变量读取 `GCMS_API_BASE` 与 `GCMS_API_KEY`；不要在回复里泄露访问密钥。",
		"",
		"## 工作流程",
		"",
		"1. 先运行 `node scripts/gcms.js capabilities` 读取平台管理契约；只执行 `available: true` 且 `granted: true` 的能力。",
		"2. 内容运营用 `GET /sites`；站点管理用 `GET /control/sites`。只使用返回的 slug/id。",
		"3. 针对某站诊断：`node scripts/gcms.js doctor --site <slug|id>`（检查该站 OpenAPI、分类、媒体权限）。",
		"4. 站点/主题/域名写操作及分类/导航删除先运行对应 `*-plan`，展示服务端返回的 normalized_input、impact、warnings。",
		"5. 用户明确确认后才运行实际命令；同一次失败重试复用 request-id，不同输入必须换新值。",
		"6. 单站内容读写仍在 `/sites/{id}` 前缀下，规则与单站助手一致。",
		"",
		"## 站点选择规则",
		"",
		"- 每条内容命令都必须带 `--site`；缺失时先 `sites` 再请用户指定，绝不臆测目标站。",
		"- 发现列表之外的站点一律视为不可管——直接说明，不要尝试 `/sites/{未知id}`。",
		"- 需要跨多个站点执行同一操作时，逐站确认后再执行，并在结果里分站汇报。",
		"- **AI 不得询问、读取、记录或代为输入后台密码**。`requires_unlock: true` 的操作只能交给 Pilot UI 向用户收集密码并签发短时授权。",
		"- 正式命令返回 `unlock_required` 时，助手回复必须保留字样 `unlock_required` 和对应 operation id，以触发 Pilot 原生密码框；验证后用相同 request-id 重试，不改变目标。",
		"- 不得执行 `available: false` 的能力，也不得猜测后台私有路由绕过能力契约。",
		"- 控制 API 只允许读取密码状态，不提供任何初始密码写操作；遇到默认密码只能交给 Pilot 原生界面调用服务器上的 GCMS 专用 CLI。",
		"- 域名结果中的 external_requirements 是 Pilot 后续工作清单；GCMS 不负责 DNS、Caddy、Cloudflare 或证书。",
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
		"8b. 你没有删除权：发现废稿时用报废申请 `discard <collection> <id> --site <slug> --reason \"为何弃用\"`（只能标记草稿，非草稿返回 409 not_draft；`undiscard` 可撤销），删除由管理员在后台「待清理」档执行。旧服务端该端点 404 时，改为把草稿开头加「【建议弃用：理由】」文字标注并提醒管理员升级。",
		"8c. 置顶文章或链接前先找到准确 id；置顶按当前单条、单语种生效，不自动联动互译内容，草稿或未到发布时间的定时内容不会立即显示在首页。",
		"8d. 调整首页显示密度前先读取 `site-profile`；`home_links_limit`（0..24）与 `home_posts_per_page`（1..50）全站、全语种共用，只在用户明确要求时修改，链接数量为 0 会隐藏首页链接模块。",
		"9. 完成后告诉用户：改了哪个站、哪些内容、对应 id、语种、状态，以及建议人工复核的点。",
		"",
		"## 推荐脚本",
		"",
		"如果当前环境可以运行 Node.js，优先使用 `scripts/gcms.js`（每条内容命令都带 `--site`）：",
		"",
		"- `node scripts/gcms.js capabilities`（只读；不接收密码）",
		"- `node scripts/gcms.js sites`",
		"- `node scripts/gcms.js control-sites`（站点管理列表）",
		"- `node scripts/gcms.js site-create-plan '{\"slug\":\"blog\",\"name\":\"品牌博客\",\"site_kind\":\"content\"}'`（预检查；默认空站并开启后续自动化）",
		"- `node scripts/gcms.js site-create @site.json --confirm true --request-id create-blog-001`（用户确认后执行）",
		"- `node scripts/gcms.js site-update-plan --site blog '{\"status\":\"disabled\"}'`",
		"- `node scripts/gcms.js site-delete-plan --site blog`（归档删除前检查；实际删除还需 Pilot UI 短时解锁）",
		"- `node scripts/gcms.js themes`（AI 只能从真实返回的主题中推荐）",
		"- `node scripts/gcms.js theme-plan --site blog editorial`",
		"- `node scripts/gcms.js domains-plan --site blog '{\"primary_domain\":\"blog.example.com\",\"redirect_domains\":[\"www.blog.example.com\"]}'`",
		"- `node scripts/gcms.js security-status`（只读；不存在密码写入命令）",
		"- `node scripts/gcms.js category-delete-plan --site blog posts 12`（先检查关联内容、语种与导航引用，不产生删除）",
		"- `node scripts/gcms.js category-delete --site blog posts 12 --expected-revision <预检查返回值> --remove-navigation true --confirm true --request-id delete-post-cat-12-001`（只有用户同时确认移除预检列出的导航入口时才加 remove-navigation）",
		"- `node scripts/gcms.js navigation-delete-plan --site blog 2`（index 是最新导航 items 数组从 0 开始的位置；只检查）",
		"- `node scripts/gcms.js navigation-delete --site blog 2 --expected-url /pricing --expected-revision <预检查返回值> --confirm true --request-id delete-nav-2-001`（URL 与 revision 必须原样复制最新预检查结果；由 Pilot 原生弹窗完成密码解锁）",
		"- `node scripts/gcms.js doctor --site blog`",
		"- `node scripts/gcms.js languages --site blog --all`",
		"- `node scripts/gcms.js site-profile --site blog`（读取站点文案 / Hero / 首页标题及全站首页显示数量）",
		"- `node scripts/gcms.js site-profile-update --site blog '{\"lang\":\"zh\",\"hero_title\":\"新标题\"}'`",
		"- `node scripts/gcms.js site-profile-update --site blog '{\"home_links_limit\":8,\"home_posts_per_page\":6}'`（全站、全语种共用；链接为 0 时隐藏首页链接模块）",
		"- `node scripts/gcms.js theme-options --site blog`（site:read：该站当前主题声明消费的配置槽与现值——工厂主题族改 factory_* 字段前先看这里，绝不编造工厂数字；旧服务端 404 时跳过本项）",
		"- `node scripts/gcms.js navigation --site blog`",
		"- `node scripts/gcms.js navigation-update --site blog @nav.json`",
		"- `node scripts/gcms.js categories posts --site blog --lang zh`",
		"- `node scripts/gcms.js list posts --site blog --lang zh --q 关键词`",
		"- `node scripts/gcms.js get posts --site blog 123`",
		"- `node scripts/gcms.js similar posts --site blog --title \"拟发标题\"`（发文前查重：FTS 标题近似匹配，含草稿；score≥0.6 优先更新旧文）",
		"- `node scripts/gcms.js preview posts --site blog 123`",
		"- `node scripts/gcms.js pin posts --site blog 123 on`",
		"- `node scripts/gcms.js create posts --site blog '{\"title\":\"标题\",\"content\":\"正文\",\"lang\":\"zh\",\"status\":\"draft\"}'`",
		"- `node scripts/gcms.js update posts --site blog 123 '{\"title\":\"新标题\"}'`",
		"- `node scripts/gcms.js update posts --site blog 123 '{}' --robots \"noindex, follow\" --canonical https://example.com/original`（单篇 SEO 覆盖；canonical 必须是绝对 URL，否则 422）",
		"- `node scripts/gcms.js discard posts --site blog 123 --reason \"与 #42 重复选题\"`（报废申请：只给草稿打标记，删除由管理员执行）",
		"- `node scripts/gcms.js undiscard posts --site blog 123`（撤销报废标记）",
		"- `node scripts/gcms.js audit posts --site blog --lang zh --limit 50`",
		"- `node scripts/gcms.js search-stats --site blog --days 28 --limit 100`（stats:read：Search Console 搜索词表现，找排名 8~20 的词优化旧文）",
		"- `node scripts/gcms.js search-stats --site blog --days 28 --compare`（附带紧前等长区间对比：每行追加 prev_clicks/prev_impressions/prev_position，前期无数据为 null）",
		"- `node scripts/gcms.js traffic-stats --site blog --days 7`（stats:read：GA 活跃用户/会话汇总；结果缓存 1 小时，未接入返回明确错误码）",
		"- `node scripts/gcms.js page-stats --site blog --days 7 --limit 50`（stats:read：GA 按页面路径的活跃用户/会话）",
		"- `node scripts/gcms.js tg-stats --site blog`（stats:read：Telegram 频道订阅数 {ok,members}，缓存 1 小时；未配置返回 telegram_not_configured；服务端较旧没有此命令时返回 404，跳过即可）",
		"",
		"注意：通过自动化 API 把 posts 置为 published 会过「发布质量门」——正文有效长度 ≥400 字（去 Markdown 后，中文按字、英文按词）、",
		"excerpt 与 meta_desc 非空、标题 8~120 字符；不达标返回 422 `{\"error\":\"quality_gate\",\"failures\":[...]}`，按 failures 补齐后重试（草稿不校验）。",
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
		"## 分类与导航受控删除",
		"",
		"- 先读取 `capabilities`，确认 `categories.delete` 或 `navigation.delete` 同时为 `available:true`、`granted:true`、`requires_unlock:true`。",
		"- 分类删除顺序固定为：读取分类 → `category-delete-plan` → 原样展示关联内容、语种、导航引用和 warnings → 用户明确确认 → 等待 Pilot 原生密码弹窗完成短时解锁 → `category-delete`。正式命令必须原样携带预检查的 `impact_revision` 作为 `--expected-revision`；只有用户同时确认删除预检列出的导航引用时，plan 和 apply 才都加 `--remove-navigation true`。",
		"- 导航删除前先重新读取 `navigation`，使用最新 `items` 数组从 0 开始的位置作为 index；随后 `navigation-delete-plan`，展示将移除的标签和 URL，再把预检查返回的 URL 与 `impact_revision` 原样放进正式命令的 `--expected-url` / `--expected-revision`。列表变化后旧 index、URL 和 revision 立即作废，应重新预检。",
		"- `navigation` 会返回前台实际可见的导航；`source:defaults` 表示尚未保存配置但正在使用默认项，这些默认项同样可以受控删除，首次修改后会物化为显式配置。",
		"- 正式命令必须带 `--confirm true` 和 8–128 字符的稳定 `--request-id`。相同请求失败重试复用原 request-id；目标、index 或输入变化必须换新值。",
		"- AI 不得索要、读取、记录或传递后台密码。没有 `GCMS_CONTROL_UNLOCK_TOKEN` 时停止在计划阶段，并请 Pilot 原生界面完成验证；不得让用户把密码粘贴到聊天或命令行。",
		"- 删除分类不等于删除内容；必须以服务端预检返回的实际影响为准。不要顺带删除同一互译组、其它分类或导航项，除非预检明确列出且用户逐项同意。",
		"",
		"如果不能运行脚本，先 `GET /sites` 发现站点，再根据 `references/openapi.json` 对 `/sites/{id}/...` 直接发 HTTP 请求。",
	}, "\n") + "\n"
}

// platformSkillScript 是站点包 gcms.js 的多站分支：base 指平台根，内置 sites 发现与 --site 选择，
// 每条内容命令的路径都在 /sites/{id} 前缀下。
func platformSkillScript() string {
	return skillScriptPlatform
}
