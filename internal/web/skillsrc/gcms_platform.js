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
  out("  gcms.js theme-options --site <slug|id> [--lang xx]   # 该站当前主题声明的配置槽与现值（site:read；写入走 site-profile-update 的 factory_*/dtc_* 字段）");
  out("  gcms.js navigation --site <slug|id>");
  out("  gcms.js navigation-update --site <slug|id> <json|@file>");
  out("  gcms.js upload --site <slug|id> <file>");
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
  out("  gcms.js preview-url --site <slug|id> <posts|links> <id>");
  out("  gcms.js pin --site <slug|id> <posts|links> <id> <on|off>");
  out("  gcms.js create --site <slug|id> <collection> <json|@file>  # 扩展集合自定义字段放 fields:{key:value}");
  out("  gcms.js update --site <slug|id> <collection> <id> <json|@file> [--robots \"noindex, follow\"] [--canonical <url>]");
  out("  gcms.js relink --site <slug|id> <collection> <id> (--to-id <sibling-id> | --trans-group <group>)");
  out('  gcms.js discard --site <slug|id> <collection> <id> --reason "为何建议弃用"   # 报废申请：只给草稿打标记，删除由管理员执行');
  out("  gcms.js undiscard --site <slug|id> <collection> <id>   # 撤销报废标记");
  out("  gcms.js audit --site <slug|id> <collection> [--lang zh|all] [--limit 50] [--deep true]");
  out("  gcms.js search-stats --site <slug|id> [--days 28] [--limit 100] [--compare]  # Search Console 搜索词表现（stats:read；--compare 附带紧前等长区间对比）");
  out("  gcms.js traffic-stats --site <slug|id> [--days 7]         # GA 活跃用户/会话汇总（stats:read）");
  out("  gcms.js page-stats --site <slug|id> [--days 7] [--limit 50]  # GA 页面路径 × 活跃用户/会话（stats:read）");
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
    const file = collection;
    if (!file) usage();
    print(await request("POST", P("/media"), mediaBodyFromFile(file)));
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
  // search-stats --compare 让服务端附带「紧前等长区间」同 key 数据（prev_clicks/prev_impressions/prev_position）。
  if (cmd === "search-stats" || cmd === "traffic-stats" || cmd === "page-stats") {
    const args = [collection, ...rest].filter((a) => a != null);
    const compare = cmd === "search-stats" && args.includes("--compare");
    const opt = parseOptions(args.filter((a) => a !== "--compare"));
    const qs = new URLSearchParams();
    if (opt.days != null) qs.set("days", opt.days);
    if (cmd !== "traffic-stats" && opt.limit != null) qs.set("limit", opt.limit);
    if (compare) qs.set("compare", "1");
    const statsPath = cmd === "search-stats" ? "/stats/search" : cmd === "page-stats" ? "/stats/pages" : "/stats/traffic";
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
    print(await request("PATCH", P("/" + collection + "/" + encodeURIComponent(id)), body));
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
