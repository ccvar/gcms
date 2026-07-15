//! 随附工具脚本：生成到 <data_dir>/tools/ 供 AI 在对话里调用。
//! shot.js（无头网页截图）+ ssh.js（远程执行，走 AI 桥）。每次启动覆写，升级 Pilot 即拿到新版脚本。

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

/// 远程执行脚本（AI 桥的 AI 侧）：**它自己什么都不会做** —— 只把命令写进本轮租约目录，
/// 等 Pilot（Rust）确认 + 用钥匙串凭据执行完再把结果捡回来。SSH 凭据永远不进这个进程。
/// 协议见 bridge.rs：先写心跳再写请求；心跳停了 Pilot 就不执行（防「工具已超时但命令照跑」）。
pub const SSH_JS: &str = r##"#!/usr/bin/env node
// ssh.js — GCMS Pilot 随附的远程命令工具（凭据在 Pilot 手里，本脚本只是个传声筒）。
// 用法: node ssh.js [--timeout <秒>] '<命令>'
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");

let timer = null;
// ★ 别用 console.log + process.exit：stdout 是管道时 node 的写是异步的，process.exit 会把
// 还在队列里的部分直接丢掉 —— 远端输出上了管道缓冲（64KB）就会被截成半截 JSON，而且退出码还是 0。
// 这里同步写满再退：writeSync 对非阻塞管道可能抛 EAGAIN，得自己转圈把剩下的写完。
function out(o, code) {
  if (timer) clearInterval(timer);
  const buf = Buffer.from(JSON.stringify(o) + "\n");
  let off = 0;
  while (off < buf.length) {
    try { off += fs.writeSync(1, buf, off, buf.length - off); }
    catch (e) {
      if (e.code === "EAGAIN") continue;   // 管道满了，对面还没读走 —— 重试
      if (e.code === "EPIPE") break;       // 对面不听了（AI 放弃了这次调用）
      throw e;
    }
  }
  process.exit(code);
}
function fail(msg) { out({ ok: false, error: msg }, 1); }

const dir = process.env.GCMS_SSH_DIR || "";
const token = process.env.GCMS_SSH_TOKEN || "";
if (!dir || !token) fail("这个对话没有连接远程机器：本工具只在「远程连接」的对话里可用。");

const args = process.argv.slice(2);
let timeout = 0;
let cmd = null;
for (let i = 0; i < args.length; i++) {
  if (args[i] === "--timeout") {
    timeout = parseInt(args[++i] || "0", 10) || 0;
    continue;
  }
  if (cmd !== null) fail("一次只能给一条命令：多步请用 && 串起来，并把整条命令放进一对引号里。");
  cmd = args[i];
}
if (!cmd || !cmd.trim()) fail("用法: node ssh.js [--timeout <秒>] '<命令>'");

const id = "s" + crypto.randomBytes(8).toString("hex");
const alive = path.join(dir, id + ".alive");
const reqFile = path.join(dir, id + ".req.json");
const respFile = path.join(dir, id + ".resp.json");

function writeAtomic(p, s) { const t = p + ".writing"; fs.writeFileSync(t, s); fs.renameSync(t, p); }
// 心跳也必须原子写：writeFileSync 是 open(O_TRUNC)+write，中间有个「文件是空的」的窗口，
// Pilot 每 200ms 读一次，读到空就判定脚本已死 → 撤掉批准卡、拒掉一条合法命令。rename 没这个窗口。
function beat() { try { writeAtomic(alive, String(Date.now())); } catch { /* ignore */ } }
function cleanup() { try { fs.rmSync(alive, { force: true }); fs.rmSync(respFile, { force: true }); } catch { /* ignore */ } }

// 心跳必须先于请求存在：Pilot 见到请求却没有新鲜心跳，就判定本脚本已死、拒绝执行。
beat();
try { writeAtomic(reqFile, JSON.stringify({ token: token, cmd: cmd, timeout: timeout, ts: Date.now() })); }
catch (e) { cleanup(); fail("提交命令失败: " + e.message); }

// 被杀（AI 工具超时 / 用户停这一轮）时尽量把心跳文件带走，让 Pilot 立刻撤掉批准卡。
for (const sig of ["SIGTERM", "SIGINT", "SIGHUP"]) {
  try { process.on(sig, () => { cleanup(); process.exit(1); }); } catch { /* ignore */ }
}

timer = setInterval(() => {
  beat(); // 只要本进程还活着就一直跳；停跳 = Pilot 撤卡不执行
  let raw;
  try { raw = fs.readFileSync(respFile, "utf8"); } catch { return; }
  cleanup();
  let r;
  try { r = JSON.parse(raw); } catch { fail("结果解析失败"); }
  if (!r.ok) fail(r.error || "执行失败");
  // 远端退出码原样传导：非 0 就让这次工具调用也算失败，别让 AI 以为跑成功了。
  out({ ok: true, code: r.code, stdout: r.stdout, stderr: r.stderr, truncated: !!r.truncated }, r.code === 0 ? 0 : 1);
}, 200);
"##;

/// 把 shot.js 写到 <data_dir>/tools/shot.js（覆写），返回脚本路径。
pub fn ensure_shot(data_dir: &Path) -> std::io::Result<PathBuf> {
    let dir = data_dir.join("tools");
    fs::create_dir_all(&dir)?;
    let p = dir.join("shot.js");
    fs::write(&p, SHOT_JS)?;
    Ok(p)
}

/// <data_dir>/tools/ssh.js 的路径（不建文件）。权限钩子要用它认出桥命令，见 permit::is_bridge_cmd。
pub fn ssh_js_path(data_dir: &Path) -> PathBuf {
    data_dir.join("tools").join("ssh.js")
}

/// 把 ssh.js 写到 <data_dir>/tools/ssh.js（覆写），返回脚本路径。
pub fn ensure_ssh(data_dir: &Path) -> std::io::Result<PathBuf> {
    let dir = data_dir.join("tools");
    fs::create_dir_all(&dir)?;
    let p = ssh_js_path(data_dir);
    fs::write(&p, SSH_JS)?;
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

    #[test]
    fn ensure_ssh_writes_script() {
        let base = std::env::temp_dir().join(format!("tools-{}", uuid::Uuid::new_v4()));
        let p = ensure_ssh(&base).unwrap();
        assert_eq!(p, ssh_js_path(&base));
        let s = fs::read_to_string(&p).unwrap();
        assert!(s.contains("GCMS_SSH_DIR"));
        assert!(s.contains("GCMS_SSH_TOKEN"));
        // 脚本里绝不能有任何取凭据的路子（这正是 AI 桥存在的理由）
        assert!(!s.contains("password"));
        assert!(!s.contains("keychain"));
        ensure_ssh(&base).unwrap(); // 覆写不报错
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

    #[test]
    #[ignore]
    fn ssh_js_passes_node_check() {
        let base = std::env::temp_dir().join(format!("tools-{}", uuid::Uuid::new_v4()));
        let p = ensure_ssh(&base).unwrap();
        let ok = std::process::Command::new("node")
            .arg("--check")
            .arg(&p)
            .status()
            .map(|s| s.success())
            .unwrap_or(false);
        assert!(ok, "node --check failed for generated ssh.js");
        fs::remove_dir_all(&base).ok();
    }

    /// ★ 回归：大输出必须原样送达 AI。
    /// 曾经的 bug：`console.log(...) + process.exit()` —— stdout 是管道时 node 的写是异步的，
    /// process.exit 会把没写完的部分丢掉 → 远端输出一过管道缓冲（64KB）就被截成半截 JSON，
    /// 而且退出码还是 0（AI 以为成功、拿到一坨烂数据）。这里用真 node 跑，本测试扮演 Pilot 侧。
    /// 手动跑：cargo test -- --ignored ssh_js_large_output
    #[test]
    #[ignore]
    fn ssh_js_large_output_is_not_truncated() {
        use std::time::{Duration, Instant};
        let base = std::env::temp_dir().join(format!("tools-{}", uuid::Uuid::new_v4()));
        let script = ensure_ssh(&base).unwrap();
        let dir = base.join("lease");
        fs::create_dir_all(&dir).unwrap();

        let child = std::process::Command::new("node")
            .arg(&script)
            .arg("cat /var/log/big.log")
            .env("GCMS_SSH_DIR", &dir)
            .env("GCMS_SSH_TOKEN", "tok")
            .stdout(std::process::Stdio::piped())
            .spawn()
            .expect("spawn node");

        // 扮演 Pilot：等脚本递上请求，回一个 256KB 的 stdout（= ssh.rs 的 EXEC_OUT_MAX 上限）。
        let big = "x".repeat(256 * 1024);
        let deadline = Instant::now() + Duration::from_secs(10);
        let id = loop {
            assert!(Instant::now() < deadline, "脚本没在 10 秒内提交请求");
            let found = fs::read_dir(&dir).unwrap().flatten().find_map(|e| {
                let n = e.file_name().to_string_lossy().into_owned();
                n.strip_suffix(".req.json").map(|s| s.to_string())
            });
            if let Some(id) = found {
                break id;
            }
            std::thread::sleep(Duration::from_millis(20));
        };
        let body = serde_json::json!({ "ok": true, "code": 0, "stdout": big, "stderr": "", "truncated": false });
        let tmp = dir.join(format!("{id}.resp.tmp"));
        fs::write(&tmp, serde_json::to_vec(&body).unwrap()).unwrap();
        fs::rename(&tmp, dir.join(format!("{id}.resp.json"))).unwrap();

        let out = child.wait_with_output().expect("wait node");
        let s = String::from_utf8_lossy(&out.stdout);
        let v: serde_json::Value = serde_json::from_str(s.trim())
            .unwrap_or_else(|e| panic!("输出不是完整 JSON（被截断了？{} 字节）: {e}", s.len()));
        assert_eq!(v["stdout"].as_str().unwrap().len(), 256 * 1024, "大输出被截断");
        assert!(out.status.success());
        fs::remove_dir_all(&base).ok();
    }

    /// 没有租约 env 就该干脆报错（别的连接类型的对话误调用时）。
    #[test]
    #[ignore]
    fn ssh_js_without_lease_fails_fast() {
        let base = std::env::temp_dir().join(format!("tools-{}", uuid::Uuid::new_v4()));
        let p = ensure_ssh(&base).unwrap();
        let out = std::process::Command::new("node")
            .arg(&p)
            .arg("ls")
            .env_remove("GCMS_SSH_DIR")
            .env_remove("GCMS_SSH_TOKEN")
            .output()
            .expect("run node");
        assert!(!out.status.success());
        let s = String::from_utf8_lossy(&out.stdout);
        assert!(s.contains("\"ok\":false"), "{s}");
        fs::remove_dir_all(&base).ok();
    }
}
