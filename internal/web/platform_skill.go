package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
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
		"",
		"如果不能运行脚本，先 `GET /sites` 发现站点，再根据 `references/openapi.json` 对 `/sites/{id}/...` 直接发 HTTP 请求。",
	}, "\n") + "\n"
}

// platformSkillScript 是站点包 gcms.js 的多站分支：base 指平台根，内置 sites 发现与 --site 选择，
// 每条内容命令的路径都在 /sites/{id} 前缀下。
func platformSkillScript() string {
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
  out("Usage: (a platform key manages many sites; every content command needs --site <slug|id>)");
  out("  gcms.js help");
  out("  gcms.js sites                       # discover manageable sites (run this first)");
  out("  gcms.js doctor [--site <slug|id>]");
  out("  gcms.js languages --site <slug|id> [--all]");
  out("  gcms.js language-create --site <slug|id> <json|@file>");
  out("  gcms.js language-enable --site <slug|id> <code> <on|off>");
  out("  gcms.js language-default --site <slug|id> <code>");
  out("  gcms.js language-catalog --site <slug|id> <code>");
  out("  gcms.js language-catalog-update --site <slug|id> <code> <json|@file>");
  out("  gcms.js site-profile --site <slug|id>");
  out("  gcms.js site-profile-update --site <slug|id> <json|@file>");
  out("  gcms.js navigation --site <slug|id>");
  out("  gcms.js navigation-update --site <slug|id> <json|@file>");
  out("  gcms.js upload --site <slug|id> <file>");
  out("  gcms.js categories --site <slug|id> <posts|links> [--lang zh|all]");
  out("  gcms.js category-entry --site <slug|id> <posts|links> [--lang zh|all]");
  out("  gcms.js update-category-entry --site <slug|id> <posts|links> <json|@file>");
  out("  gcms.js list --site <slug|id> <posts|pages|links> [--lang zh|all] [--q text] [--slug slug] [--trans_group group] [--status draft] [--limit 20]");
  out("  gcms.js get --site <slug|id> <posts|pages|links> <id>");
  out("  gcms.js preview --site <slug|id> <posts|links> <id>");
  out("  gcms.js preview-url --site <slug|id> <posts|links> <id>");
  out("  gcms.js pin --site <slug|id> <posts|links> <id> <on|off>");
  out("  gcms.js create --site <slug|id> <posts|pages|links> <json|@file>");
  out("  gcms.js update --site <slug|id> <posts|pages|links> <id> <json|@file>");
  out("  gcms.js audit --site <slug|id> <posts|pages|links> [--lang zh|all] [--limit 50] [--deep true]");
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

// extractSite pulls a global "--site <slug|id>" out of argv so the rest of the
// positional/flag parsing is identical to the single-site CLI.
function extractSite(argv) {
  const rest = [];
  let site = null;
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--site") {
      site = argv[++i];
      if (site == null) usage();
    } else {
      rest.push(argv[i]);
    }
  }
  return { site, argv: rest };
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

function parseOnOff(value) {
  if (["on", "true", "1", "yes"].includes(String(value || "").toLowerCase())) return true;
  if (["off", "false", "0", "no"].includes(String(value || "").toLowerCase())) return false;
  usage();
}

// ---- site discovery / resolution ----
let sitesCache = null;
async function fetchSites() {
  if (sitesCache) return sitesCache;
  const data = await request("GET", "/sites");
  sitesCache = Array.isArray(data.items) ? data.items : [];
  return sitesCache;
}

async function findSite(sel) {
  const sites = await fetchSites();
  return sites.find((s) => String(s.id) === String(sel)) || sites.find((s) => s.slug === sel) || null;
}

async function resolveSite(sel) {
  if (sel == null || sel === "") {
    console.error("This command needs --site <slug|id>. Run 'node gcms.js sites' to list manageable sites.");
    process.exit(2);
  }
  const hit = await findSite(sel);
  if (!hit) {
    const sites = await fetchSites();
    const avail = sites.length ? sites.map((s) => s.slug + " (#" + s.id + ")").join(", ") : "(none — this key has no manageable sites)";
    console.error("Unknown site '" + sel + "'. Manageable sites: " + avail);
    process.exit(2);
  }
  return hit.id;
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

async function auditCollection(prefix, collection, opt) {
  const deep = boolOption(opt.deep);
  delete opt.deep;
  if (!opt.limit) opt.limit = "50";
  const qs = new URLSearchParams(opt);
  const data = await request("GET", prefix + "/" + collection + (qs.toString() ? "?" + qs.toString() : ""));
  if (!deep) return auditItems(collection, data);
  const detailed = [];
  for (const item of Array.isArray(data.items) ? data.items : []) {
    const got = await request("GET", prefix + "/" + collection + "/" + encodeURIComponent(item.id));
    detailed.push(got.item || item);
  }
  return auditItems(collection, { items: detailed }, { deep: true });
}

async function doctor(siteSel) {
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

  let sites = [];
  try {
    const disc = await rawRequest("GET", "/sites");
    sites = disc.data && Array.isArray(disc.data.items) ? disc.data.items : [];
    add("discovery", disc.ok, { status: disc.status, sites: sites.length, all_sites: disc.data && disc.data.all_sites });
  } catch (err) {
    add("discovery", false, { message: err.message });
  }

  let prefix = null;
  if (siteSel != null && siteSel !== "") {
    const hit = sites.find((s) => String(s.id) === String(siteSel)) || sites.find((s) => s.slug === siteSel) || null;
    if (hit) {
      prefix = "/sites/" + hit.id;
      add("resolve_site", true, { site: siteSel, id: hit.id, slug: hit.slug });
    } else {
      add("resolve_site", false, { site: siteSel, message: "site not in manageable set" });
    }
  }

  if (prefix) {
    try {
      const openapi = await rawRequest("GET", prefix + "/openapi.json");
      add("openapi", openapi.ok, { status: openapi.status });
      if (openapi.ok) {
        const paths = openapi.data && openapi.data.paths ? openapi.data.paths : {};
        const schemas = openapi.data && openapi.data.components && openapi.data.components.schemas ? openapi.data.components.schemas : {};
        add("openapi_language_create_path", !!(paths["/languages"] && paths["/languages"].post));
        add("openapi_media_path", !!(paths["/media"] && paths["/media"].post));
        add("openapi_post_preview_path", !!(paths["/posts/{id}/preview"] && paths["/posts/{id}/preview"].get));
        add("openapi_post_featured_path", !!(paths["/posts/featured/{id}"] && paths["/posts/featured/{id}"].patch));
        add("openapi_schemas", !!schemas.LanguageItemResponse && !!schemas.ContentPreview);
      }
    } catch (err) {
      add("openapi", false, { message: err.message });
    }
    try {
      const languages = await rawRequest("GET", prefix + "/languages");
      const items = languages.data && Array.isArray(languages.data.items) ? languages.data.items : [];
      add("languages", languages.ok, { status: languages.status, count: items.length, default: languages.data && languages.data.default });
    } catch (err) {
      add("languages", false, { message: err.message });
    }
    for (const name of ["posts", "links"]) {
      try {
        const cats = await rawRequest("GET", prefix + "/" + name + "/categories?lang=zh");
        const items = cats.data && Array.isArray(cats.data.items) ? cats.data.items : [];
        add(name + "_categories", cats.ok, { status: cats.status, count: items.length });
      } catch (err) {
        add(name + "_categories", false, { message: err.message });
      }
    }
    try {
      const media = await rawRequest("POST", prefix + "/media", mediaProbeBody());
      const mediaOK = media.status === 400 && media.data && media.data.error === "bad_type";
      add("media_write_permission", mediaOK, { status: media.status, error: media.data && media.data.error });
    } catch (err) {
      add("media_write_permission", false, { message: err.message });
    }
  } else {
    add("hint", true, { message: "run with --site <slug|id> to check a specific site's OpenAPI, languages, categories, and media permission" });
  }

  result.ok = result.checks.every((check) => check.ok);
  print(result);
  process.exit(result.ok ? 0 : 1);
}

async function main() {
  const parsed = extractSite(process.argv.slice(2));
  const siteSel = parsed.site;
  const [cmd, collection, ...rest] = parsed.argv;
  if (!cmd || cmd === "help" || cmd === "--help" || cmd === "-h") usage(0);

  if (cmd === "sites" || cmd === "list-sites") {
    print(await request("GET", "/sites"));
    return;
  }

  if (cmd === "doctor") {
    await doctor(siteSel);
    return;
  }

  // Everything below operates on a single site: resolve --site, then prefix /sites/{id}.
  const siteID = await resolveSite(siteSel);
  const P = (p) => "/sites/" + siteID + p;

  if (cmd === "languages") {
    const args = [collection, ...rest].filter((a) => a != null);
    const qs = new URLSearchParams();
    if (args.includes("--all") || args.includes("--include-disabled")) qs.set("include_disabled", "true");
    if (args.includes("--catalog") || args.includes("--include-catalog")) qs.set("include_catalog", "true");
    print(await request("GET", P("/languages" + (qs.toString() ? "?" + qs.toString() : ""))));
    return;
  }

  if (cmd === "language-create") {
    const body = collection;
    if (!body) usage();
    print(await request("POST", P("/languages"), bodyFromArg(body)));
    return;
  }

  if (cmd === "language-enable") {
    const code = collection;
    const value = rest[0];
    if (!code || !value) usage();
    print(await request("PATCH", P("/languages/" + encodeURIComponent(code)), { enabled: parseOnOff(value) }));
    return;
  }

  if (cmd === "language-default") {
    const code = collection;
    if (!code) usage();
    print(await request("PATCH", P("/languages/" + encodeURIComponent(code)), { default: true }));
    return;
  }

  if (cmd === "language-catalog") {
    const code = collection;
    if (!code) usage();
    print(await request("GET", P("/languages/" + encodeURIComponent(code) + "/catalog")));
    return;
  }

  if (cmd === "language-catalog-update") {
    const code = collection;
    const body = rest[0];
    if (!code || !body) usage();
    const parsedBody = bodyFromArg(body);
    print(await request("PATCH", P("/languages/" + encodeURIComponent(code) + "/catalog"), parsedBody && Object.prototype.hasOwnProperty.call(parsedBody, "catalog") ? parsedBody : { catalog: parsedBody }));
    return;
  }

  if (cmd === "site-profile") {
    print(await request("GET", P("/site-profile")));
    return;
  }

  if (cmd === "site-profile-update") {
    const body = collection;
    if (!body) usage();
    print(await request("PATCH", P("/site-profile"), bodyFromArg(body)));
    return;
  }

  if (cmd === "navigation") {
    print(await request("GET", P("/navigation")));
    return;
  }

  if (cmd === "navigation-update") {
    const body = collection;
    if (!body) usage();
    print(await request("PATCH", P("/navigation"), bodyFromArg(body)));
    return;
  }

  if (cmd === "upload") {
    const file = collection;
    if (!file) usage();
    print(await request("POST", P("/media"), mediaBodyFromFile(file)));
    return;
  }

  if (cmd === "categories") {
    assertCollection(collection);
    if (collection === "pages") usage();
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", P("/" + collection + "/categories" + (qs.toString() ? "?" + qs.toString() : ""))));
    return;
  }

  if (cmd === "category-entry") {
    assertCollection(collection);
    if (collection === "pages") usage();
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", P("/" + collection + "/categories/all-entry" + (qs.toString() ? "?" + qs.toString() : ""))));
    return;
  }

  if (cmd === "update-category-entry") {
    assertCollection(collection);
    if (collection === "pages") usage();
    const body = rest[0];
    if (!body) usage();
    print(await request("PATCH", P("/" + collection + "/categories/all-entry"), bodyFromArg(body)));
    return;
  }

  assertCollection(collection);

  if (cmd === "list") {
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", P("/" + collection + (qs.toString() ? "?" + qs.toString() : ""))));
    return;
  }

  if (cmd === "get") {
    const id = rest[0];
    if (!id) usage();
    print(await request("GET", P("/" + collection + "/" + encodeURIComponent(id))));
    return;
  }

  if (cmd === "preview") {
    const id = rest[0];
    if (!id || collection === "pages") usage();
    print(await request("GET", P("/" + collection + "/" + encodeURIComponent(id) + "/preview")));
    return;
  }

  if (cmd === "preview-url") {
    const id = rest[0];
    if (!id || collection === "pages") usage();
    print(await request("POST", P("/" + collection + "/" + encodeURIComponent(id) + "/preview-url")));
    return;
  }

  if (cmd === "pin") {
    const id = rest[0];
    const value = rest[1];
    if (!id || value == null || collection === "pages") usage();
    print(await request("PATCH", P("/" + collection + "/featured/" + encodeURIComponent(id)), { featured: parseOnOff(value) }));
    return;
  }

  if (cmd === "create") {
    const body = rest[0];
    if (!body) usage();
    print(await request("POST", P("/" + collection), bodyFromArg(body)));
    return;
  }

  if (cmd === "update") {
    const id = rest[0];
    const body = rest[1];
    if (!id || !body) usage();
    print(await request("PATCH", P("/" + collection + "/" + encodeURIComponent(id)), bodyFromArg(body)));
    return;
  }

  if (cmd === "audit") {
    const opt = parseOptions(rest);
    print(await auditCollection(P(""), collection, opt));
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
