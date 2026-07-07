//! 随附工具脚本：生成到 <data_dir>/tools/ 供 AI 在对话里调用。
//! 目前只有 shot.js（无头网页截图）。每次启动覆写，升级 Pilot 即拿到新版脚本。

use std::fs;
use std::path::{Path, PathBuf};

/// 无头截图脚本：系统 Chrome/Edge/Chromium/Brave 优先，playwright 兜底（--no-install，不偷偷下载）。
/// 成功打印 JSON {ok:true,out,engine,bytes}；失败打印 {ok:false,error} 并退出 1，错误信息可直接转告用户。
pub const SHOT_JS: &str = r##"#!/usr/bin/env node
// shot.js — GCMS Pilot 随附的无头网页截图工具。
// 用法: node shot.js --url <url> --out <file.png> [--width 1280] [--height 800] [--full-page] [--wait <ms>]
const { spawnSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

function fail(msg) { console.log(JSON.stringify({ ok: false, error: msg })); process.exit(1); }

const args = process.argv.slice(2);
const opt = {};
for (let i = 0; i < args.length; i++) {
  const a = args[i];
  if (!a.startsWith("--")) fail("参数格式错误: " + a);
  const k = a.slice(2);
  if (k === "full-page") { opt[k] = true; continue; }
  const v = args[++i];
  if (v == null) fail("缺少 --" + k + " 的值");
  opt[k] = v;
}
if (!opt.url || !opt.out) fail("用法: node shot.js --url <url> --out <file.png> [--width 1280] [--height 800] [--full-page] [--wait <ms>]");

const width = parseInt(opt.width || "1280", 10) || 1280;
const height = parseInt(opt.height || "800", 10) || 800;
const wait = parseInt(opt.wait || "2500", 10) || 2500;
const fullPage = !!opt["full-page"];
const out = path.resolve(opt.out);
fs.mkdirSync(path.dirname(out), { recursive: true });

function findBrowser() {
  const cands = [];
  if (process.platform === "darwin") {
    cands.push(
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
      "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
      "/Applications/Chromium.app/Contents/MacOS/Chromium",
      "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"
    );
  } else if (process.platform === "win32") {
    for (const base of [process.env["ProgramFiles"], process.env["ProgramFiles(x86)"], process.env.LOCALAPPDATA].filter(Boolean)) {
      cands.push(
        path.join(base, "Google", "Chrome", "Application", "chrome.exe"),
        path.join(base, "Microsoft", "Edge", "Application", "msedge.exe")
      );
    }
  } else {
    cands.push("/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/microsoft-edge");
  }
  for (const p of cands) { try { if (fs.existsSync(p)) return p; } catch { /* ignore */ } }
  return null;
}

function fileOk() { try { return fs.statSync(out).size >= 1000; } catch { return false; } }

function shotWithChrome(browser) {
  // Chrome headless --screenshot 只截视口；--full-page 用加高窗口近似（真全页建议 playwright）。
  const h = fullPage ? Math.max(height, 6000) : height;
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), "pilot-shot-"));
  const flags = [
    "--headless=new", "--disable-gpu", "--hide-scrollbars", "--mute-audio",
    "--no-first-run", "--no-default-browser-check", "--disable-extensions",
    "--user-data-dir=" + profile,
    "--window-size=" + width + "," + h,
    "--virtual-time-budget=" + wait,
    "--screenshot=" + out,
    opt.url,
  ];
  const r = spawnSync(browser, flags, { timeout: 90000, stdio: "ignore" });
  try { fs.rmSync(profile, { recursive: true, force: true }); } catch { /* ignore */ }
  return r.status === 0;
}

function shotWithPlaywright() {
  const a = ["--no-install", "playwright", "screenshot", "--browser=chromium", "--viewport-size=" + width + "," + height, "--wait-for-timeout=" + wait];
  if (fullPage) a.push("--full-page");
  a.push(opt.url, out);
  const r = spawnSync(process.platform === "win32" ? "npx.cmd" : "npx", a, { timeout: 120000, stdio: "ignore" });
  return r.status === 0;
}

let engine = "";
const browser = findBrowser();
if (browser && shotWithChrome(browser) && fileOk()) engine = path.basename(browser);
if (!engine) {
  try { fs.rmSync(out, { force: true }); } catch { /* ignore */ }
  if (shotWithPlaywright() && fileOk()) engine = "playwright";
}
if (!engine) {
  try { fs.rmSync(out, { force: true }); } catch { /* ignore */ }
  fail(browser
    ? "截图失败：页面可能无法访问 / 渲染超时。试试加大 --wait（如 6000）或换 URL；需要登录或有反爬的页面截不了。"
    : "没找到可用浏览器。请安装 Google Chrome / Microsoft Edge，或 `npm i -g playwright && npx playwright install chromium` 后重试。");
}
console.log(JSON.stringify({ ok: true, out: out, engine: engine, bytes: fs.statSync(out).size, width: width, fullPage: fullPage }));
"##;

/// 把 shot.js 写到 <data_dir>/tools/shot.js（覆写），返回脚本路径。
pub fn ensure_shot(data_dir: &Path) -> std::io::Result<PathBuf> {
    let dir = data_dir.join("tools");
    fs::create_dir_all(&dir)?;
    let p = dir.join("shot.js");
    fs::write(&p, SHOT_JS)?;
    Ok(p)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ensure_shot_writes_script() {
        let base = std::env::temp_dir().join(format!("tools-{}", uuid::Uuid::new_v4()));
        let p = ensure_shot(&base).unwrap();
        let s = fs::read_to_string(&p).unwrap();
        assert!(s.contains("--url"));
        assert!(s.contains("findBrowser"));
        assert!(s.contains("playwright"));
        // 覆写不报错（升级刷新场景）
        ensure_shot(&base).unwrap();
        fs::remove_dir_all(&base).ok();
    }

    /// 真实 node 语法检查（需要本机有 node，CI 无 node 时跳过）。
    #[test]
    #[ignore]
    fn shot_js_passes_node_check() {
        let base = std::env::temp_dir().join(format!("tools-{}", uuid::Uuid::new_v4()));
        let p = ensure_shot(&base).unwrap();
        let ok = std::process::Command::new("node")
            .arg("--check")
            .arg(&p)
            .status()
            .map(|s| s.success())
            .unwrap_or(false);
        assert!(ok, "node --check failed for generated shot.js");
        fs::remove_dir_all(&base).ok();
    }
}
