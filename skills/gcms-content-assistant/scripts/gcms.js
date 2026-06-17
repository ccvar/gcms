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
