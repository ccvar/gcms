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
  out("  gcms.js languages");
  out("  gcms.js upload <file>");
  out("  gcms.js categories <posts|links> [--lang zh|all]");
  out("  gcms.js list <posts|pages|links> [--lang zh|all] [--q text] [--slug slug] [--trans_group group] [--status draft] [--limit 20]");
  out("  gcms.js get <posts|pages|links> <id>");
  out("  gcms.js create <posts|pages|links> <json|@file>");
  out("  gcms.js update <posts|pages|links> <id> <json|@file>");
  out("  gcms.js audit <posts|pages|links> [--lang zh|all] [--limit 50]");
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

async function request(method, urlPath, body) {
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
  if (!res.ok) {
    console.error(JSON.stringify(data, null, 2));
    process.exit(1);
  }
  return data;
}

function print(data) {
  console.log(JSON.stringify(data, null, 2));
}

function auditItems(collection, data) {
  const items = Array.isArray(data.items) ? data.items : [];
  const issues = [];
  for (const item of items) {
    const missing = [];
    if (!item.title) missing.push("title");
    if (!item.excerpt) missing.push("excerpt");
    if (!item.meta_desc) missing.push("meta_desc");
    if (!item.keywords) missing.push("keywords");
    if (collection !== "pages" && !item.category_id) missing.push("category_id");
    if (collection === "links" && !item.link_url) missing.push("link_url");
    if ((collection === "posts" || collection === "links") && !item.cover_image) missing.push("cover_image");
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

async function main() {
  const [cmd, collection, ...rest] = process.argv.slice(2);
  if (!cmd || cmd === "help" || cmd === "--help" || cmd === "-h") usage(0);

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
    if (!opt.limit) opt.limit = "50";
    const qs = new URLSearchParams(opt);
    const data = await request("GET", "/" + collection + (qs.toString() ? "?" + qs.toString() : ""));
    print(auditItems(collection, data));
    return;
  }

  usage();
}

main().catch((err) => {
  console.error(err && err.message ? err.message : err);
  process.exit(1);
});
