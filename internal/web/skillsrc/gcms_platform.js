#!/usr/bin/env node
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
const collections = new Set(["posts", "pages", "links"]); // 内置；扩展集合运行时经该站 /types 发现

const typesCacheBySite = new Map();
async function fetchTypes(P, all) {
  if (all) {
    const data = await request("GET", P("/types?all=1"));
    return Array.isArray(data.types) ? data.types : [];
  }
  const ck = P("");
  if (!typesCacheBySite.has(ck)) {
    const data = await request("GET", P("/types"));
    typesCacheBySite.set(ck, Array.isArray(data.types) ? data.types : []);
  }
  return typesCacheBySite.get(ck);
}

function usage(code = 2) {
  const out = code === 0 ? console.log : console.error;
  out("Usage: (a platform key manages many sites; every content command needs --site <slug|id>)");
  out("  gcms.js help");
  out("  gcms.js capabilities                # inspect platform-control operations, risk and unlock requirements (never handles passwords)");
  out("  gcms.js sites                       # discover manageable sites (run this first)");
  out("  gcms.js control-sites               # list sites through the management API (includes disabled sites in this key's membership)");
  out("  gcms.js control-site --site <slug|id>");
  out("  gcms.js site-create-plan <json|@file>");
  out("  gcms.js site-create <json|@file> --confirm true --request-id <stable-id>");
  out("  gcms.js site-update-plan --site <slug|id> <json|@file>");
  out("  gcms.js site-update --site <slug|id> <json|@file> --confirm true --request-id <stable-id>");
  out("  gcms.js site-delete-plan --site <slug|id>");
  out("  gcms.js site-delete --site <slug|id> --confirm true --request-id <stable-id>  # needs Pilot UI unlock");
  out("  gcms.js themes [<theme-id>]");
  out("  gcms.js theme-current --site <slug|id>");
  out("  gcms.js theme-plan --site <slug|id> <theme-id|rollback>");
  out("  gcms.js theme-apply --site <slug|id> <theme-id|rollback> --confirm true --request-id <stable-id>");
  out("  gcms.js domains --site <slug|id>");
  out("  gcms.js domains-plan --site <slug|id> <json|@file>");
  out("  gcms.js domains-apply --site <slug|id> <json|@file> --confirm true --request-id <stable-id>  # needs Pilot UI unlock");
  out("  gcms.js security-status             # status only; initial password is handled exclusively by Pilot UI");
  out("  gcms.js category-delete-plan --site <slug|id> <collection> <category-id> [--remove-navigation true]");
  out("  gcms.js category-delete --site <slug|id> <collection> <category-id> --expected-revision <revision-from-plan> [--remove-navigation true] --confirm true --request-id <stable-id>  # needs Pilot UI unlock");
  out("  gcms.js navigation-delete-plan --site <slug|id> <zero-based-index>");
  out("  gcms.js navigation-delete --site <slug|id> <zero-based-index> --expected-url <url-from-plan> --expected-revision <revision-from-plan> --confirm true --request-id <stable-id>  # needs Pilot UI unlock");
  out("  gcms.js doctor [--site <slug|id>]");
  out("  gcms.js page-context --site <slug|id> [--lang zh]  # Pilot 页面设计上下文：主题、站点资料、组件、真实数据源与预览门禁");
  out("  gcms.js page-capabilities --site <slug|id>  # 页面模式、操作、安全协议与限制");
  out("  gcms.js page-projects --site <slug|id> [--page-id id] [--lang zh] [--slug slug] [--mode composition|app] [--limit 50]  # 发现已有页面工程");
  out("  gcms.js page-get --site <slug|id> <project-id>  # 返回 _protocol.etag");
  out("  gcms.js page-create --site <slug|id> <json|@file> --etag <content-etag> --request-id <stable-id>");
  out("  gcms.js page-update --site <slug|id> <project-id> <json|@file> --etag <etag> --request-id <stable-id>");
  out("  gcms.js page-revisions --site <slug|id> <project-id> [--limit 100] | page-revision --site <slug|id> <project-id> <revision-id>");
  out("  gcms.js page-restore --site <slug|id> <project-id> --revision-id <id> [--summary text] --etag <etag> --request-id <stable-id> --confirm true");
  out("  gcms.js page-components --site <slug|id> | page-data-sources --site <slug|id> [--lang zh] | page-binding-preview --site <slug|id> <json|@file>");
  out("  gcms.js page-assets --site <slug|id> <project-id>");
  out("  gcms.js page-asset-upload --site <slug|id> <project-id> <image> [--logical-key key] --etag <etag> --request-id <stable-id>");
  out("  gcms.js page-app-upload --site <slug|id> <project-id> <app.zip> [--base-revision-id id] --etag <etag> --request-id <stable-id> --confirm true");
  out("  gcms.js page-app-source-read --site <slug|id> <project-id> <file-path> [--revision-id id]");
  out("  gcms.js page-app-source-edit --site <slug|id> <project-id> <file-path> <text|@file> --base-revision-id <id> --etag <etag> --request-id <stable-id> --confirm true");
  out("  gcms.js page-capability-list --site <slug|id> <project-id>");
  out("  gcms.js page-capability-request|page-capability-grant|page-capability-deny|page-capability-revoke --site <slug|id> <project-id> <name> [--config <json|@file>] --etag <etag> --request-id <stable-id> --confirm true");
  out("  gcms.js page-validate|page-preview --site <slug|id> <project-id> --revision-id <id> [--build-id <ready-build-id>] --etag <etag>  # 预览应绑定刚完成的构建；不需要 request-id");
  out("  gcms.js page-build --site <slug|id> <project-id> --revision-id <id> --etag <etag> --request-id <stable-id>");
  out("  gcms.js page-build-get --site <slug|id> <project-id> <build-id> | page-publications --site <slug|id> <project-id> [--limit 100]");
  out("  gcms.js page-publish-plan|page-rollback-plan --site <slug|id> <project-id> --revision-id <id> --etag <etag>  # 不需要 request-id");
  out("  gcms.js page-publish|page-rollback --site <slug|id> <project-id> --revision-id <id> --etag <etag> --request-id <same-id> --confirm true  # Pilot 原生确认");
  out("  gcms.js languages --site <slug|id> [--all]");
  out("  gcms.js language-create --site <slug|id> <json|@file>");
  out("  gcms.js language-enable --site <slug|id> <code> <on|off>");
  out("  gcms.js language-default --site <slug|id> <code>");
  out("  gcms.js language-catalog --site <slug|id> <code>");
  out("  gcms.js language-catalog-update --site <slug|id> <code> <json|@file>");
  out("  gcms.js site-profile --site <slug|id>                         # 含首页显示数量与全站 logo_scale");
  out("  gcms.js site-profile-update --site <slug|id> <json|@file>      # 顶层可写 logo_scale(0.3..2)、home_links_limit、home_posts_per_page");
  out("  gcms.js theme-options --site <slug|id> [--lang xx]   # 该站当前主题声明的配置槽与现值（site:read；写入走 site-profile-update 的 factory_*/dtc_* 字段）");
  out("  gcms.js navigation --site <slug|id>");
  out("  gcms.js navigation-update --site <slug|id> <json|@file>");
  out("  gcms.js upload --site <slug|id> <file> [file...]  # 多文件批量上传；瞬时网络失败在脚本内自动重试");
  out("  gcms.js types --site <slug|id> [--all]      # 该站内容类型与字段 schema（--all 含未启用）");
  out("  gcms.js type-enable --site <slug|id> <key> | type-disable --site <slug|id> <key>");
  out("  gcms.js type-create --site <slug|id> <json|@file>   # 新建自定义类型（先与用户确认内容模型）");
  out("  gcms.js type-update --site <slug|id> <key> <json|@file>");
  out("  gcms.js type-delete --site <slug|id> <key>  # 仅限没有内容的自定义类型");
  out("  gcms.js categories --site <slug|id> <collection> [--lang zh|all]   (posts/links 及支持分类的扩展集合)");
  out("  gcms.js category-entry --site <slug|id> <collection> [--lang zh|all]");
  out("  gcms.js update-category-entry --site <slug|id> <collection> <json|@file>");
  out("  gcms.js list --site <slug|id> <collection> [--lang zh|all] [--q text] [--slug slug] [--trans_group group] [--status draft] [--limit 20]");
  out("  gcms.js get --site <slug|id> <collection> <id>");
  out("  gcms.js similar --site <slug|id> [<collection>] --title \"标题\" [--lang zh] [--limit 5]  # 发文前查重（近似匹配，含草稿；collection 缺省 posts）");
  out("  gcms.js preview --site <slug|id> <posts|links> <id>");
  out("  gcms.js preview-url --site <slug|id> <posts|links|pages> <id>");
  out("  gcms.js pin --site <slug|id> <posts|links> <id> <on|off>");
  out("  gcms.js create --site <slug|id> <collection> <json|@file>  # 扩展集合自定义字段放 fields:{key:value}");
  out("  gcms.js update --site <slug|id> <collection> <id> <json|@file> [--etag <etag>] [--robots \"noindex, follow\"] [--canonical <url>]");
  out("  gcms.js relink --site <slug|id> <collection> <id> (--to-id <sibling-id> | --trans-group <group>)");
  out('  gcms.js discard --site <slug|id> <collection> <id> --reason "为何建议弃用"   # 报废申请：只给草稿打标记，删除由管理员执行');
  out("  gcms.js undiscard --site <slug|id> <collection> <id>   # 撤销报废标记");
  out("  gcms.js audit --site <slug|id> <collection> [--lang zh|all] [--limit 50] [--deep true]");
  out("  gcms.js search-stats --site <slug|id> [--days 28] [--limit 100] [--group query_page|query|page|date|total] [--compare] [--fresh]  # Search Console 搜索表现（stats:read）");
  out("  gcms.js traffic-stats --site <slug|id> [--days 7]         # GA 活跃用户/会话汇总（stats:read）");
  out("  gcms.js page-stats --site <slug|id> [--days 7] [--limit 50]  # GA 页面路径 × 活跃用户/会话（stats:read）");
  out("  gcms.js analytics-stats --site <slug|id> [--days 7] [--limit 50] [--group sources|geography|devices|trend] [--fresh] # GA 细分维度（stats:read）");
  out("  gcms.js tg-stats --site <slug|id>           # Telegram 频道订阅数（stats:read；未配置返回 telegram_not_configured）");
  out("  （collection = posts|pages|links 或该站 types 里的扩展集合，如 products/docs/自定义）");
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

async function assertCollection(P, name) {
  if (collections.has(name)) return;
  const types = await fetchTypes(P);
  if (types.some((t) => t.collection === name)) return;
  console.error("Unknown collection: " + (name || "(missing)"));
  console.error("Built-in: posts, pages, links" + (types.length ? "; extension: " + types.map((t) => t.collection).join(", ") : ""));
  console.error("Run `gcms.js types --site <slug|id>` to inspect extension types and their field schema.");
  process.exit(2);
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

const transientHTTPStatuses = new Set([408, 425, 429, 500, 502, 503, 504]);

function wait(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function uploadMedia(file, urlPath) {
  const maxAttempts = 4;
  let last;
  let attempts = 0;
  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    attempts = attempt;
    try {
      const result = await rawRequest("POST", urlPath, mediaBodyFromFile(file), {}, { timeoutMs: 20000 });
      if (result.ok) {
        return {
          ok: true,
          file,
          attempts: attempt,
          replayed: Boolean(result.headers?.replayed),
          ...result.data
        };
      }
      last = { status: result.status, error: result.data };
      if (!transientHTTPStatuses.has(result.status)) break;
    } catch (err) {
      last = { error: { error: "network_error", message: err.message || String(err) } };
    }
    if (attempt < maxAttempts) {
      await wait([400, 1200, 2800][attempt - 1] + Math.floor(Math.random() * 250));
    }
  }
  return { ok: false, file, attempts, ...(last || {}) };
}

async function uploadFiles(files, urlPath) {
  const results = new Array(files.length);
  let cursor = 0;
  const worker = async () => {
    while (cursor < files.length) {
      const index = cursor++;
      results[index] = await uploadMedia(files[index], urlPath);
    }
  };
  await Promise.all(Array.from({ length: Math.min(2, files.length) }, worker));
  const succeeded = results.filter((item) => item.ok).length;
  return {
    ok: succeeded === results.length,
    succeeded,
    failed: results.length - succeeded,
    total: results.length,
    items: results
  };
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

async function rawRequest(method, urlPath, body, extraHeaders = {}, options = {}) {
  requireConfig();
  const headers = { Authorization: "Bearer " + key, Accept: "application/json", ...extraHeaders };
  const init = { method, headers };
  if (body !== undefined) {
    if (typeof FormData !== "undefined" && body instanceof FormData) {
      init.body = body;
    } else {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
  }
  const controller = options.timeoutMs ? new AbortController() : null;
  const timer = controller ? setTimeout(() => controller.abort(), options.timeoutMs) : null;
  if (controller) init.signal = controller.signal;
  let res;
  try {
    res = await fetch(base + urlPath, init);
  } finally {
    if (timer) clearTimeout(timer);
  }
  const text = await res.text();
  let data;
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { raw: text };
  }
  return {
    ok: res.ok,
    status: res.status,
    data,
    headers: {
      etag: res.headers.get("etag") || "",
      replayed: res.headers.get("idempotent-replayed") === "true"
    }
  };
}

async function request(method, urlPath, body, extraHeaders) {
  const result = await rawRequest(method, urlPath, body, extraHeaders);
  const { ok, data } = result;
  if (!ok) {
    console.error(JSON.stringify(data, null, 2));
    process.exit(1);
  }
  return data;
}

function pageProtocolResult(result) {
  const data = result.data && typeof result.data === "object" && !Array.isArray(result.data)
    ? { ...result.data }
    : { data: result.data };
  data._protocol = {
    http_status: result.status,
    etag: result.headers?.etag || "",
    idempotent_replayed: Boolean(result.headers?.replayed)
  };
  const pageID = data.project?.PostID || data.project?.post_id || data.page_id;
  if (pageID) data._links = { ...(data._links || {}), admin_path: `/admin/pages/${pageID}/project` };
  return data;
}

function contentProtocolResult(result) {
  const data = result.data && typeof result.data === "object" && !Array.isArray(result.data)
    ? { ...result.data }
    : { data: result.data };
  data._protocol = {
    http_status: result.status,
    etag: result.headers?.etag || ""
  };
  return data;
}

function pageMutationOptions(args, { confirm = false, unlock = false } = {}) {
  const opt = parseOptions(args);
  const etag = String(opt.etag || "").trim();
  const requestID = String(opt["request-id"] || "").trim();
  if (!etag) {
    console.error("页面写操作必须携带从最新 page-get 响应取得的 --etag；禁止猜测或省略。");
    process.exit(2);
  }
  if (requestID.length < 8 || requestID.length > 200) {
    console.error("页面写操作必须携带稳定的 --request-id（8-200 字符）；仅在重试完全相同请求时复用。");
    process.exit(2);
  }
  if (confirm && !boolOption(opt.confirm)) {
    console.error("此敏感页面操作必须先向用户展示目标与影响并取得明确同意，再传 --confirm true。");
    process.exit(2);
  }
  const headers = { "If-Match": etag, "Idempotency-Key": requestID };
  if (unlock) {
    const unlockToken = String(process.env.GCMS_CONTROL_UNLOCK_TOKEN || "").trim();
    if (unlockToken) headers["X-GCMS-Control-Unlock"] = unlockToken;
  }
  return { opt, headers };
}

function pageETagOptions(args) {
  const opt = parseOptions(args);
  const etag = String(opt.etag || "").trim();
  if (!etag) {
    console.error("此修订绑定操作必须携带从最新 page-get 响应取得的 --etag；它不需要 --request-id。");
    process.exit(2);
  }
  return { opt, headers: { "If-Match": etag } };
}

function pagePathSegments(value) {
  const source = String(value || "").trim();
  if (!source || source.startsWith("/") || source.includes("\\") || source.split("/").some((part) => !part || part === "." || part === "..")) {
    console.error("互动应用源码路径必须是包内相对路径，且不能包含空段、.、.. 或反斜线。");
    process.exit(2);
  }
  return source.split("/").map(encodeURIComponent).join("/");
}

function pageTextFromArg(arg) {
  if (String(arg || "").startsWith("@")) return fs.readFileSync(String(arg).slice(1), "utf8");
  return String(arg ?? "");
}

function pageConfigFromOption(value) {
  if (value == null || String(value).trim() === "") return undefined;
  const parsed = bodyFromArg(String(value));
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
    console.error("--config 必须是 JSON 对象或 @JSON文件。");
    process.exit(2);
  }
  return parsed;
}

async function pageRequest(method, urlPath, body, headers = {}, { capability = false } = {}) {
  const result = await rawRequest(method, urlPath, body, headers);
  const missingContract = result.status === 404 && capability &&
    (!result.data?.error || ["not_found", "unknown_collection", "route_not_found"].includes(result.data.error));
  if (missingContract) {
    print({
      available: false,
      error: "page_platform_unavailable",
      message: "当前 GCMS 版本没有页面工程协议；继续使用标准 pages API，升级后再使用自由编排或互动应用。",
      _protocol: { http_status: 404, etag: "", idempotent_replayed: false }
    });
    return null;
  }
  const output = pageProtocolResult(result);
  if (!result.ok) {
    if (result.data?.unlock_required) {
      output.confirmation_required = true;
      output.next_action = "原样保留 unlock_required、operation、unlock_challenge、目标与 admin_path，等待 Pilot 原生确认；确认后只用完全相同的 ETag、request-id、目标和参数重试。";
      print(output);
      process.exitCode = 4;
      return null;
    }
    if (result.data?.error === "capability_confirmation_required") {
      output.confirmation_required = true;
      output.available = false;
      output.next_action = "当前服务端未返回 Pilot 可消费的 unlock_challenge；请在 GCMS 后台人工批准或升级服务端，绝不能让 AI 索取密码或 approval_token。";
      print(output);
      process.exitCode = 4;
      return null;
    }
    if (result.status === 409 || result.status === 412 || result.status === 428) {
      output.conflict = true;
      output.safe_to_overwrite = false;
      output.next_action = "重新执行 page-get，比较最新修订后创建新的合并修订；不要重放旧页面快照。";
      print(output);
      process.exitCode = 3;
      return null;
    }
    print(output);
    process.exitCode = 1;
    return null;
  }
  print(output);
  return output;
}

function pageFileForm(file, fields, fieldName = "file") {
  if (typeof FormData !== "function" || typeof Blob !== "function") {
    console.error("页面上传需要 Node.js 18+ 的 FormData 与 Blob。");
    process.exit(2);
  }
  const bytes = fs.readFileSync(file);
  const form = new FormData();
  form.append(fieldName, new Blob([bytes], { type: "application/octet-stream" }), path.basename(file));
  for (const [name, value] of Object.entries(fields)) {
    if (value !== undefined && value !== null && String(value) !== "") form.append(name, String(value));
  }
  return form;
}

async function handlePageCommand(cmd, first, rest, P) {
  const known = new Set([
    "page-context", "page-capabilities", "page-projects", "page-get", "page-create", "page-update",
    "page-revisions", "page-revision", "page-restore",
    "page-components", "page-data-sources", "page-binding-preview",
    "page-assets", "page-asset-upload", "page-app-upload",
    "page-app-source-read", "page-app-source-edit",
    "page-app-capabilities", "page-capability-list",
    "page-capability-request", "page-capability-grant",
    "page-capability-deny", "page-capability-revoke",
    "page-validate", "page-build", "page-build-get",
    "page-preview", "page-publish-plan", "page-publish",
    "page-publications", "page-rollback-plan", "page-rollback"
  ]);
  if (!known.has(cmd)) return false;
  if (cmd === "page-context") {
    const opt = parseOptions([first, ...rest].filter((value) => value != null));
    const qs = new URLSearchParams();
    if (opt.lang != null) qs.set("lang", opt.lang);
    await pageRequest("GET", P("/page-design-context" + (qs.toString() ? "?" + qs.toString() : "")), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-capabilities") {
    await pageRequest("GET", P("/page-platform/capabilities"), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-projects") {
    const opt = parseOptions([first, ...rest].filter((value) => value != null));
    const qs = new URLSearchParams();
    for (const key of ["page-id", "lang", "slug", "mode", "limit", "offset"]) {
      if (opt[key] != null) qs.set(key.replace("-", "_"), opt[key]);
    }
    await pageRequest("GET", P("/page-projects" + (qs.toString() ? "?" + qs.toString() : "")), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-components") {
    if (first != null || rest.length) usage();
    await pageRequest("GET", P("/page-components"), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-data-sources") {
    const opt = parseOptions([first, ...rest].filter((value) => value != null));
    const qs = new URLSearchParams();
    if (opt.lang != null) qs.set("lang", opt.lang);
    await pageRequest("GET", P("/page-data-sources" + (qs.toString() ? "?" + qs.toString() : "")), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-binding-preview") {
    if (!first || rest.length) usage();
    await pageRequest("POST", P("/page-bindings/preview"), bodyFromArg(first), {}, { capability: true });
    return true;
  }
  if (cmd === "page-create") {
    const bodyArg = first;
    if (!bodyArg) usage();
    const body = bodyFromArg(bodyArg);
    const parsed = parseOptions(rest);
    let headers;
    if (String(parsed.etag || "").trim()) {
      headers = pageMutationOptions(rest).headers;
    } else {
      const pageID = Number(body.page_id || body.post_id);
      const requestID = String(parsed["request-id"] || "").trim();
      if (!Number.isInteger(pageID) || pageID <= 0 || requestID.length < 8 || requestID.length > 200) {
        console.error("page-create 需要 body.page_id 与 8-200 字符的 --request-id；未传 --etag 时会先读取服务端转换预检 ETag。");
        process.exit(2);
      }
      const plan = await rawRequest(
        "POST", P("/pages/" + pageID + "/convert-plan"), {}
      );
      if (!plan.ok || !plan.headers?.etag) {
        const output = pageProtocolResult(plan);
        output.available = false;
        output.next_action = "继续保留标准页面，确认 page-capabilities 或升级 GCMS；不要猜测 ETag。";
        print(output);
        process.exitCode = plan.status === 404 ? 0 : 1;
        return true;
      }
      headers = { "If-Match": plan.headers.etag, "Idempotency-Key": requestID };
    }
    await pageRequest("POST", P("/page-projects"), body, headers, { capability: true });
    return true;
  }
  const projectID = String(first || "").trim();
  if (!/^[1-9]\d*$/.test(projectID)) usage();
  const projectPath = P("/page-projects/" + encodeURIComponent(projectID));
  if (cmd === "page-get") {
    if (rest.length) usage();
    await pageRequest("GET", projectPath, undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-revisions") {
    const opt = parseOptions(rest);
    const qs = new URLSearchParams();
    if (opt.limit != null) qs.set("limit", opt.limit);
    await pageRequest("GET", projectPath + "/revisions" + (qs.toString() ? "?" + qs.toString() : ""), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-revision") {
    const revisionID = String(rest[0] || "").trim();
    if (!/^[1-9]\d*$/.test(revisionID) || rest.length !== 1) usage();
    await pageRequest("GET", projectPath + "/revisions/" + encodeURIComponent(revisionID), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-assets") {
    if (rest.length) usage();
    await pageRequest("GET", projectPath + "/assets", undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-build-get") {
    const buildID = String(rest[0] || "").trim();
    if (!/^[1-9]\d*$/.test(buildID) || rest.length !== 1) usage();
    await pageRequest("GET", projectPath + "/builds/" + encodeURIComponent(buildID), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-publications") {
    const opt = parseOptions(rest);
    const qs = new URLSearchParams();
    if (opt.limit != null) qs.set("limit", opt.limit);
    await pageRequest("GET", projectPath + "/publications" + (qs.toString() ? "?" + qs.toString() : ""), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-app-source-read") {
    const filePath = rest[0];
    if (!filePath) usage();
    const opt = parseOptions(rest.slice(1));
    const qs = new URLSearchParams();
    if (opt["revision-id"] != null) qs.set("revision_id", opt["revision-id"]);
    const endpoint = projectPath + "/app-files/" + pagePathSegments(filePath);
    await pageRequest("GET", endpoint + (qs.toString() ? "?" + qs.toString() : ""), undefined, {}, { capability: true });
    return true;
  }
  if (cmd === "page-app-source-edit") {
    const filePath = rest[0];
    const contentArg = rest[1];
    if (!filePath || contentArg == null) usage();
    const { opt, headers } = pageMutationOptions(rest.slice(2), { confirm: true });
    const baseRevisionID = Number(opt["base-revision-id"]);
    if (!Number.isInteger(baseRevisionID) || baseRevisionID <= 0) {
      console.error("page-app-source-edit 必须提供当前 --base-revision-id。");
      process.exit(2);
    }
    headers["X-GCMS-Control-Confirm"] = "page_apps.source.edit";
    await pageRequest("PUT", projectPath + "/app-files/" + pagePathSegments(filePath), {
      base_revision_id: baseRevisionID,
      content: pageTextFromArg(contentArg),
      summary: opt.summary || "Pilot 编辑互动应用源码 " + filePath,
      conversation_id: opt["conversation-id"] || ""
    }, headers, { capability: true });
    return true;
  }
  if (cmd === "page-app-capabilities" || cmd === "page-capability-list") {
    if (rest.length) usage();
    await pageRequest("GET", projectPath + "/capabilities", undefined, {}, { capability: true });
    return true;
  }
  if (cmd.startsWith("page-capability-")) {
    const capability = String(rest[0] || "").trim();
    if (!capability) usage();
    const needsUnlock = cmd === "page-capability-grant";
    const { opt, headers } = pageMutationOptions(rest.slice(1), { confirm: true, unlock: needsUnlock });
    const body = { capability };
    const config = pageConfigFromOption(opt.config);
    if (config !== undefined) body.config = config;
    let endpoint = "/capabilities/request";
    let operation = "page_capabilities.request";
    if (cmd === "page-capability-grant" || cmd === "page-capability-deny") {
      endpoint = "/capabilities/apply";
      operation = "page_capabilities.grant";
      body.decision = cmd === "page-capability-grant" ? "approve" : "deny";
    } else if (cmd === "page-capability-revoke") {
      endpoint = "/capabilities/revoke";
      operation = "page_capabilities.revoke";
    }
    headers["X-GCMS-Control-Confirm"] = operation;
    await pageRequest("POST", projectPath + endpoint, body, headers, { capability: true });
    return true;
  }
  if (cmd === "page-update") {
    const bodyArg = rest[0];
    if (!bodyArg) usage();
    const { headers } = pageMutationOptions(rest.slice(1));
    const body = bodyFromArg(bodyArg);
    if (!Number.isInteger(Number(body.base_revision_id)) || Number(body.base_revision_id) <= 0) {
      console.error("page-update 请求体必须包含最新的 base_revision_id。");
      process.exit(2);
    }
    await pageRequest("POST", projectPath + "/revisions", body, headers, { capability: true });
    return true;
  }
  if (cmd === "page-asset-upload") {
    const file = rest[0];
    if (!file) usage();
    const { opt, headers } = pageMutationOptions(rest.slice(1));
    const form = pageFileForm(file, {
      logical_key: opt["logical-key"] || path.basename(file),
      origin: opt.origin || "pilot",
      provenance: opt.provenance || "{}"
    });
    await pageRequest("POST", projectPath + "/assets", form, headers, { capability: true });
    return true;
  }
  if (cmd === "page-app-upload") {
    const file = rest[0];
    if (!file) usage();
    const { opt, headers } = pageMutationOptions(rest.slice(1), { confirm: true });
    headers["X-GCMS-Control-Confirm"] = "page_apps.upload";
    const form = pageFileForm(file, {
      base_revision_id: opt["base-revision-id"],
      summary: opt.summary || "Pilot 上传互动应用包",
      conversation_id: opt["conversation-id"]
    }, "package");
    await pageRequest("POST", projectPath + "/app-package", form, headers, { capability: true });
    return true;
  }
  if (cmd === "page-restore") {
    const { opt, headers } = pageMutationOptions(rest, { confirm: true });
    const revisionID = Number(opt["revision-id"]);
    if (!Number.isInteger(revisionID) || revisionID <= 0) {
      console.error("page-restore 必须显式提供 --revision-id。");
      process.exit(2);
    }
    headers["X-GCMS-Control-Confirm"] = "page_revisions.restore";
    await pageRequest("POST", projectPath + "/restore", {
      revision_id: revisionID,
      summary: opt.summary || "Pilot 恢复历史页面修订"
    }, headers, { capability: true });
    return true;
  }
  const revisionBoundRead = cmd === "page-validate" || cmd === "page-preview" ||
    cmd === "page-publish-plan" || cmd === "page-rollback-plan";
  const risky = cmd === "page-publish" || cmd === "page-rollback";
  const parsed = revisionBoundRead
    ? pageETagOptions(rest)
    : pageMutationOptions(rest, { confirm: risky, unlock: risky });
  const { opt, headers } = parsed;
  const target = {};
  if (opt["revision-id"] != null) {
    target.revision_id = Number(opt["revision-id"]);
    if (!Number.isInteger(target.revision_id) || target.revision_id <= 0) usage();
  }
  if (opt["build-id"] != null) {
    target.build_id = Number(opt["build-id"]);
    if (!Number.isInteger(target.build_id) || target.build_id <= 0) usage();
  }
  const suffix = {
    "page-validate": "/validate", "page-build": "/builds",
    "page-preview": "/preview-url", "page-publish-plan": "/publish-plan",
    "page-publish": "/publish", "page-rollback-plan": "/rollback-plan",
    "page-rollback": "/rollback"
  }[cmd];
  if (risky) {
    headers["X-GCMS-Control-Confirm"] = cmd === "page-publish" ? "pages.publish" : "pages.rollback";
    if (!target.revision_id) {
      console.error(`${cmd} 必须显式传 --revision-id，不能让发布目标随工作指针漂移。`);
      process.exit(2);
    }
  }
  if (cmd === "page-rollback-plan" && !target.revision_id) {
    console.error("page-rollback-plan 必须显式传 --revision-id。");
    process.exit(2);
  }
  await pageRequest("POST", projectPath + suffix, target, headers, { capability: true });
  return true;
}

function controlMutationOptions(args) {
  const opt = parseOptions(args);
  if (!boolOption(opt.confirm)) {
    console.error("This write command needs --confirm true after the user explicitly approves the dry-run result.");
    process.exit(2);
  }
  const requestID = String(opt["request-id"] || "").trim();
  if (requestID.length < 8 || requestID.length > 128) {
    console.error("This write command needs a stable --request-id (8-128 characters). Reuse it only when retrying the same request.");
    process.exit(2);
  }
  return { requestID };
}

async function controlMutation(method, urlPath, operation, body, optionArgs) {
  const { requestID } = controlMutationOptions(optionArgs);
  const headers = {
    "X-GCMS-Control-Confirm": operation,
    "Idempotency-Key": requestID
  };
  // Pilot may inject a short-lived operation-bound token after its native password UI.
  // The skill never asks for, reads from stdin, or prints the GCMS backend password.
  const unlock = String(process.env.GCMS_CONTROL_UNLOCK_TOKEN || "").trim();
  if (unlock) headers["X-GCMS-Control-Unlock"] = unlock;
  return request(method, urlPath, body, headers);
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
let controlSitesCache = null;
async function fetchSites() {
  if (sitesCache) return sitesCache;
  const data = await request("GET", "/sites");
  sitesCache = Array.isArray(data.items) ? data.items : [];
  return sitesCache;
}

async function fetchControlSites() {
  if (controlSitesCache) return controlSitesCache;
  const data = await request("GET", "/control/sites");
  controlSitesCache = Array.isArray(data.items) ? data.items : [];
  return controlSitesCache;
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

async function resolveControlSite(sel) {
  if (sel == null || sel === "") {
    console.error("This command needs --site <slug|id>. Run 'node gcms.js control-sites' first.");
    process.exit(2);
  }
  const sites = await fetchControlSites();
  const hit = sites.find((s) => String(s.id) === String(sel)) || sites.find((s) => s.slug === sel) || null;
  if (!hit) {
    const avail = sites.length ? sites.map((s) => s.slug + " (#" + s.id + ")").join(", ") : "(none)";
    console.error("Unknown or unauthorized control site '" + sel + "'. Available: " + avail);
    process.exit(2);
  }
  return hit.id;
}

function auditItems(collection, data, options = {}) {
  const items = Array.isArray(data.items) ? data.items : [];
  const issues = [];
  const ext = !collections.has(collection);
  for (const item of items) {
    const missing = [];
    if (!item.title) missing.push("title");
    if (!item.slug) missing.push("slug");
    if (ext) {
      for (const f of options.requiredFields || []) {
        const v = item.fields ? item.fields[f] : undefined;
        if (v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0)) missing.push("fields." + f);
      }
    }
    if (!ext && !item.excerpt) missing.push("excerpt");
    if (!ext && !item.meta_desc) missing.push("meta_desc");
    if (!ext && !item.keywords) missing.push("keywords");
    if (!ext && collection !== "pages" && !item.category_id) missing.push("category_id");
    if (!ext && collection === "links" && !item.link_url) missing.push("link_url");
    if (!ext && !item.cover_image) missing.push("cover_image");
    if (options.deep && !ext && !item.content) missing.push("content");
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
  let requiredFields = [];
  if (!collections.has(collection)) {
    const t = (await fetchTypes((x) => prefix + x)).find((x) => x.collection === collection);
    requiredFields = ((t && t.fields) || []).filter((f) => f.required).map((f) => f.key);
  }
  const qs = new URLSearchParams(opt);
  const data = await request("GET", prefix + "/" + collection + (qs.toString() ? "?" + qs.toString() : ""));
  if (!deep) return auditItems(collection, data, { requiredFields });
  const detailed = [];
  for (const item of Array.isArray(data.items) ? data.items : []) {
    const got = await request("GET", prefix + "/" + collection + "/" + encodeURIComponent(item.id));
    detailed.push(got.item || item);
  }
  return auditItems(collection, { items: detailed }, { deep: true, requiredFields });
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
        add("openapi_link_featured_path", !!(paths["/links/featured/{id}"] && paths["/links/featured/{id}"].patch));
        add("openapi_featured_schema", !!schemas.FeaturedInput);
        const siteResponseProps = schemas.SiteProfileResponse && schemas.SiteProfileResponse.properties ? schemas.SiteProfileResponse.properties : {};
        const sitePatchProps = schemas.SiteProfilePatch && schemas.SiteProfilePatch.properties ? schemas.SiteProfilePatch.properties : {};
        add("openapi_home_display_schema", !!siteResponseProps.home_links_limit && !!siteResponseProps.home_posts_per_page && !!sitePatchProps.home_links_limit && !!sitePatchProps.home_posts_per_page);
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

  if (cmd === "capabilities") {
    print(await request("GET", "/control/capabilities"));
    return;
  }

  if (cmd === "sites" || cmd === "list-sites") {
    print(await request("GET", "/sites"));
    return;
  }

  if (cmd === "control-sites") {
    print(await request("GET", "/control/sites"));
    return;
  }

  if (cmd === "control-site") {
    const id = await resolveControlSite(siteSel);
    print(await request("GET", "/control/sites/" + id));
    return;
  }

  if (cmd === "site-create-plan" || cmd === "site-create") {
    const bodyArg = collection;
    if (!bodyArg) usage();
    const body = bodyFromArg(bodyArg);
    if (cmd === "site-create-plan") {
      print(await request("POST", "/control/sites?dry_run=1", body));
    } else {
      print(await controlMutation("POST", "/control/sites", "sites.create", body, rest));
    }
    return;
  }

  if (cmd === "site-update-plan" || cmd === "site-update") {
    const id = await resolveControlSite(siteSel);
    const bodyArg = collection;
    if (!bodyArg) usage();
    const body = bodyFromArg(bodyArg);
    if (cmd === "site-update-plan") {
      print(await request("PATCH", "/control/sites/" + id + "?dry_run=1", body));
    } else {
      print(await controlMutation("PATCH", "/control/sites/" + id, "sites.update", body, rest));
    }
    return;
  }

  if (cmd === "site-delete-plan" || cmd === "site-delete") {
    const id = await resolveControlSite(siteSel);
    if (cmd === "site-delete-plan") {
      print(await request("DELETE", "/control/sites/" + id + "?dry_run=1"));
    } else {
      print(await controlMutation("DELETE", "/control/sites/" + id, "sites.delete", undefined, [collection, ...rest].filter((v) => v != null)));
    }
    return;
  }

  if (cmd === "themes") {
    const suffix = collection ? "/" + encodeURIComponent(collection) : "";
    print(await request("GET", "/control/themes" + suffix));
    return;
  }

  if (cmd === "theme-current" || cmd === "theme-plan" || cmd === "theme-apply") {
    const id = await resolveControlSite(siteSel);
    const target = collection;
    if (cmd === "theme-current") {
      print(await request("GET", "/control/sites/" + id + "/theme"));
      return;
    }
    if (!target) usage();
    const body = target === "rollback" ? { rollback: true } : { theme_id: target };
    if (cmd === "theme-plan") {
      print(await request("PUT", "/control/sites/" + id + "/theme?dry_run=1", body));
    } else {
      print(await controlMutation("PUT", "/control/sites/" + id + "/theme", "themes.apply", body, rest));
    }
    return;
  }

  if (cmd === "domains" || cmd === "domains-plan" || cmd === "domains-apply") {
    const id = await resolveControlSite(siteSel);
    if (cmd === "domains") {
      print(await request("GET", "/control/sites/" + id + "/domains"));
      return;
    }
    const bodyArg = collection;
    if (!bodyArg) usage();
    const body = bodyFromArg(bodyArg);
    if (cmd === "domains-plan") {
      print(await request("PUT", "/control/sites/" + id + "/domains?dry_run=1", body));
    } else {
      print(await controlMutation("PUT", "/control/sites/" + id + "/domains", "domains.apply", body, rest));
    }
    return;
  }

  if (cmd === "security-status") {
    print(await request("GET", "/control/security"));
    return;
  }

  if (cmd === "category-delete-plan" || cmd === "category-delete") {
    const id = await resolveControlSite(siteSel);
    const targetCollection = collection;
    const categoryID = rest[0];
    if (!targetCollection || !categoryID || !/^[1-9][0-9]*$/.test(String(categoryID))) usage();
    const deleteOptions = parseOptions(rest.slice(1));
    const removeNavigation = boolOption(deleteOptions["remove-navigation"]);
    const expectedRevision = String(deleteOptions["expected-revision"] || "").trim();
    const targetPath = "/control/sites/" + id + "/categories/" +
      encodeURIComponent(targetCollection) + "/" + encodeURIComponent(categoryID);
    const query = removeNavigation ? "&remove_navigation=1" : "";
    if (cmd === "category-delete-plan") {
      print(await request("DELETE", targetPath + "?dry_run=1" + query));
    } else {
      if (!expectedRevision) {
        console.error("category-delete needs --expected-revision copied from the latest category-delete-plan result.");
        process.exit(2);
      }
      const qs = new URLSearchParams({ expected_revision: expectedRevision });
      if (removeNavigation) qs.set("remove_navigation", "1");
      print(await controlMutation(
        "DELETE",
        targetPath + "?" + qs.toString(),
        "categories.delete",
        undefined,
        rest.slice(1)
      ));
    }
    return;
  }

  if (cmd === "navigation-delete-plan" || cmd === "navigation-delete") {
    const id = await resolveControlSite(siteSel);
    const index = collection;
    if (index == null || !/^[0-9]+$/.test(String(index))) usage();
    const targetPath = "/control/sites/" + id + "/navigation/" + encodeURIComponent(index);
    if (cmd === "navigation-delete-plan") {
      if (rest.length !== 0) usage();
      print(await request("DELETE", targetPath + "?dry_run=1"));
    } else {
      const deleteOptions = parseOptions(rest);
      const expectedURL = String(deleteOptions["expected-url"] || "").trim();
      const expectedRevision = String(deleteOptions["expected-revision"] || "").trim();
      if (!expectedURL) {
        console.error("navigation-delete needs --expected-url copied from the latest navigation-delete-plan result.");
        process.exit(2);
      }
      if (!expectedRevision) {
        console.error("navigation-delete needs --expected-revision copied from the latest navigation-delete-plan result.");
        process.exit(2);
      }
      const qs = new URLSearchParams({ expected_url: expectedURL, expected_revision: expectedRevision });
      print(await controlMutation(
        "DELETE",
        targetPath + "?" + qs.toString(),
        "navigation.delete",
        undefined,
        rest
      ));
    }
    return;
  }

  if (cmd === "doctor") {
    await doctor(siteSel);
    return;
  }

  // Everything below operates on a single site: resolve --site, then prefix /sites/{id}.
  const siteID = await resolveSite(siteSel);
  const P = (p) => "/sites/" + siteID + p;
  if (await handlePageCommand(cmd, collection, rest, P)) return;

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

  // 主题配置槽（site:read）：该站当前主题（骨架）声明消费哪些数据槽 + 各槽现值。
  // 改工厂/独立站文案前先跑这条看契约，再用 site-profile-update 写对应 factory_*/dtc_* 字段；
  // 服务端较旧没有此端点时返回 404——按提示跳过本项，不要重试。
  if (cmd === "theme-options") {
    const opt = parseOptions([collection, ...rest].filter((a) => a != null));
    const qs = new URLSearchParams();
    if (opt.lang != null) qs.set("lang", opt.lang);
    const res = await rawRequest("GET", P("/theme-options" + (qs.toString() ? "?" + qs.toString() : "")));
    if (res.status === 404) {
      console.error(JSON.stringify(res.data, null, 2));
      console.error("服务端较旧（没有 theme-options 端点）：跳过本项，直接按 SKILL.md「主题配置」小节的字段约定操作，并在汇报里提醒管理员升级 gcms。");
      process.exit(1);
    }
    if (!res.ok) {
      console.error(JSON.stringify(res.data, null, 2));
      process.exit(1);
    }
    print(res.data);
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
    const files = [collection, ...rest].filter(Boolean);
    if (!files.length) usage();
    const result = await uploadFiles(files, P("/media"));
    print(files.length === 1 ? result.items[0] : result);
    if (!result.ok) process.exitCode = 1;
    return;
  }

  if (cmd === "types") {
    const all = [collection, ...rest].includes("--all");
    print({ types: await fetchTypes(P, all) });
    return;
  }

  if (cmd === "type-enable" || cmd === "type-disable") {
    const k = collection;
    if (!k) usage();
    print(await request("POST", P("/types/" + encodeURIComponent(k) + "/" + (cmd === "type-enable" ? "enable" : "disable"))));
    return;
  }

  if (cmd === "type-create") {
    const body = collection;
    if (!body) usage();
    print(await request("POST", P("/types"), bodyFromArg(body)));
    return;
  }

  if (cmd === "type-update") {
    const k = collection;
    const body = rest[0];
    if (!k || !body) usage();
    print(await request("PUT", P("/types/" + encodeURIComponent(k)), bodyFromArg(body)));
    return;
  }

  if (cmd === "type-delete") {
    const k = collection;
    if (!k) usage();
    print(await request("DELETE", P("/types/" + encodeURIComponent(k))));
    return;
  }

  if (cmd === "categories") {
    await assertCollection(P, collection);
    if (collection === "pages") usage();
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", P("/" + collection + "/categories" + (qs.toString() ? "?" + qs.toString() : ""))));
    return;
  }

  if (cmd === "category-entry") {
    await assertCollection(P, collection);
    if (collection === "pages") usage();
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", P("/" + collection + "/categories/all-entry" + (qs.toString() ? "?" + qs.toString() : ""))));
    return;
  }

  if (cmd === "update-category-entry") {
    await assertCollection(P, collection);
    if (collection === "pages") usage();
    const body = rest[0];
    if (!body) usage();
    print(await request("PATCH", P("/" + collection + "/categories/all-entry"), bodyFromArg(body)));
    return;
  }

  // 统计数据（stats:read）：Search Console 搜索词表现 / GA 流量与页面汇总，服务端缓存 1 小时。
  // search-stats --compare 让服务端附带「紧前等长区间」同 key 数据；--fresh 绕过一小时缓存。
  if (cmd === "search-stats" || cmd === "traffic-stats" || cmd === "page-stats" || cmd === "analytics-stats") {
    const args = [collection, ...rest].filter((a) => a != null);
    const compare = cmd === "search-stats" && args.includes("--compare");
    const fresh = (cmd === "search-stats" || cmd === "analytics-stats") && args.includes("--fresh");
    const opt = parseOptions(args.filter((a) => a !== "--compare" && a !== "--fresh"));
    const qs = new URLSearchParams();
    if (opt.days != null) qs.set("days", opt.days);
    if (cmd !== "traffic-stats" && opt.limit != null) qs.set("limit", opt.limit);
    if ((cmd === "search-stats" || cmd === "analytics-stats") && opt.group != null) qs.set("group", opt.group);
    if (compare) qs.set("compare", "1");
    if (fresh) qs.set("fresh", "1");
    const statsPath = cmd === "search-stats" ? "/stats/search" : cmd === "page-stats" ? "/stats/pages" : cmd === "analytics-stats" ? "/stats/analytics" : "/stats/traffic";
    print(await request("GET", P(statsPath + (qs.toString() ? "?" + qs.toString() : ""))));
    return;
  }

  // Telegram 频道订阅数（stats:read）：GET /stats/telegram → {ok,members}，服务端缓存 1 小时。
  // 服务端较旧没有此端点时会返回 404——说明该站 GCMS 版本还没有此能力，升级后再用。
  if (cmd === "tg-stats") {
    print(await request("GET", P("/stats/telegram")));
    return;
  }

  // 发文前查重：按标题做站内近似匹配（FTS5，含已发布 + 草稿），避免重复选题。collection 缺省 posts。
  if (cmd === "similar") {
    let col = collection;
    let flags = rest;
    if (!col || col.startsWith("--")) {
      flags = [collection, ...rest].filter((a) => a != null);
      col = "posts";
    }
    await assertCollection(P, col);
    const opt = parseOptions(flags);
    if (!opt.title) usage();
    const qs = new URLSearchParams();
    qs.set("title", opt.title);
    if (opt.lang != null) qs.set("lang", opt.lang);
    if (opt.limit != null) qs.set("limit", opt.limit);
    print(await request("GET", P("/" + col + "/similar?" + qs.toString())));
    return;
  }

  await assertCollection(P, collection);

  if (cmd === "list") {
    const opt = parseOptions(rest);
    const qs = new URLSearchParams(opt);
    print(await request("GET", P("/" + collection + (qs.toString() ? "?" + qs.toString() : ""))));
    return;
  }

  if (cmd === "get") {
    const id = rest[0];
    if (!id) usage();
    const result = await rawRequest("GET", P("/" + collection + "/" + encodeURIComponent(id)));
    if (!result.ok) {
      console.error(JSON.stringify(result.data, null, 2));
      process.exit(1);
    }
    print(contentProtocolResult(result));
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
    if (!id) usage();
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
    // 用法：update --site <slug|id> <collection> <id> <json|@file> [--robots "..."] [--canonical <url>]
    // --robots/--canonical 透传为 robots_override / canonical_override（单篇 SEO 覆盖）。
    const [id, ...updateArgs] = rest;
    if (!id) usage();
    let body = {};
    if (updateArgs.length && !String(updateArgs[0]).startsWith("--")) {
      body = bodyFromArg(updateArgs.shift());
    }
    const opt = parseOptions(updateArgs);
    if (opt.robots != null) body.robots_override = opt.robots;
    if (opt.canonical != null) body.canonical_override = opt.canonical;
    if (!Object.keys(body).length) usage();
    const target = P("/" + collection + "/" + encodeURIComponent(id));
    let etag = String(opt.etag || "").trim();
    if (collection === "pages" || !etag) {
      const current = await rawRequest("GET", target);
      if (!current.ok) {
        console.error(JSON.stringify(current.data, null, 2));
        process.exit(1);
      }
      if (collection === "pages") {
        const currentStatus = String(current.data?.item?.status || "").toLowerCase();
        const requestedStatus = String(body.status || "").toLowerCase();
        if (currentStatus !== "draft" || requestedStatus === "published" || requestedStatus === "scheduled") {
          print({
            error: "legacy_standard_page_protected",
            message: "Pilot 不通过旧 pages update 修改或发布线上标准页。",
            safe_to_overwrite: false,
            next_action: "请在 GCMS 后台操作，或先转换为受 ETag 与原生批准保护的页面工程。"
          });
          process.exitCode = 2;
          return;
        }
      }
      if (!etag) etag = String(current.headers?.etag || "").trim();
    }
    if (collection === "pages" && !etag) {
      print({
        error: "content_etag_unavailable",
        message: "当前服务器未提供标准页 ETag，Pilot 无法安全合并人工修改。",
        safe_to_overwrite: false,
        next_action: "请升级 GCMS 后重试。"
      });
      process.exitCode = 2;
      return;
    }
    const result = await rawRequest("PATCH", target, body, etag ? { "If-Match": etag } : {});
    if (!result.ok) {
      console.error(JSON.stringify(result.data, null, 2));
      process.exit(1);
    }
    print(contentProtocolResult(result));
    return;
  }

  // 重连互译组：把已存在的一篇并入某翻译组（唯一能改 trans_group 的入口）。
  if (cmd === "relink") {
    const [id, ...flags] = rest;
    if (!id) usage();
    const opt = parseOptions(flags);
    const body = {};
    if (opt["to-id"] != null) body.link_to_id = Number(opt["to-id"]);
    else if (opt["trans-group"] != null) body.trans_group = opt["trans-group"];
    else usage();
    print(await request("POST", P("/" + collection + "/" + encodeURIComponent(id) + "/relink"), body));
    return;
  }

  // 报废申请（标记删除）：AI 没有删除权——发现废稿（重复选题/质量不可救/用户否决）时，
  // 只能给「草稿」打建议弃用标记 + 理由（≤200 字），删除永远由管理员在后台执行。
  // 标记非草稿会返回 409 not_draft；重复标记＝更新理由（幂等）；undiscard 可随时撤销。
  if (cmd === "discard") {
    const [id, ...flags] = rest;
    if (!id) usage();
    const opt = parseOptions(flags);
    if (!opt.reason) usage();
    const res = await rawRequest("POST", P("/" + collection + "/" + encodeURIComponent(id) + "/discard"), { reason: opt.reason });
    if (res.status === 404) {
      console.error(JSON.stringify(res.data, null, 2));
      console.error("服务端版本较旧（没有 discard 端点）：请改为把草稿开头加上「【建议弃用：理由】」文字标注，并在汇报里提醒管理员升级 gcms。");
      process.exit(1);
    }
    if (!res.ok) {
      console.error(JSON.stringify(res.data, null, 2));
      process.exit(1);
    }
    print(res.data);
    return;
  }

  if (cmd === "undiscard") {
    const id = rest[0];
    if (!id) usage();
    print(await request("DELETE", P("/" + collection + "/" + encodeURIComponent(id) + "/discard")));
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
