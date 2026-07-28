//! 本地 AI CLI（“大脑”）检测：claude / codex。
//! 只读检测：--version + 登录状态；claude auth status 登出时退出码为 1，
//! 必须解析 stdout 而不是看退出码。

use serde::Serialize;
use std::process::Stdio;
use std::time::Duration;
use tokio::process::Command;

#[derive(Clone, Serialize, Default)]
pub struct BrainStatus {
    pub found: bool,
    pub path: String,
    pub version: String,
    pub logged_in: Option<bool>,
    pub account: String,
    pub detail: String,
}

#[derive(Clone, Serialize)]
pub struct BrainsInfo {
    pub claude: BrainStatus,
    pub codex: BrainStatus,
    /// xAI Grok CLI（ACP 接入）；登录态看 ~/.grok/auth.json（GROK_HOME 可改基目录）。
    pub grok: BrainStatus,
    /// Cloudflare 部署工具（建站/预览/部署/D1 都靠它）；用 env token，无登录态。
    pub wrangler: BrainStatus,
    /// 无头截图用的浏览器（Chrome/Edge/Chromium/Brave，可选能力）。只查路径存在，不执行。
    pub browser: BrainStatus,
    /// Node.js（npm 安装 Codex/wrangler 的前置；Claude Code 用原生安装器不需要它）。
    pub node: BrainStatus,
    pub path_env: String,
    /// 装着的 Claude CLI 够不够新到能用 **headless** fast mode。
    /// ★ 判定放在这里（而不是前端）：版本解析只有一份，别让 JS 再写一套 semver 比较。
    /// 旧版不会报错、只是那个 `fastMode` 键**静默失效** —— UI 必须据此置灰开关，
    /// 否则用户点开它、以为开了、其实什么都没变。
    pub claude_fast_ok: bool,
}

/// headless fast mode 的最低 CLI 版本。依据：Claude Code 文档 headless 章节
/// 「`/model`, `/effort`, `/fast` … accept the value as an argument … require v2.1.205 or later」。
pub const FAST_MIN_CLI: (u32, u32, u32) = (2, 1, 205);

pub async fn detect() -> BrainsInfo {
    augment_path_env(); // 每次检测都补一遍：刚装完的目录此刻才存在
    let (claude, codex, grok, wrangler, node) = tokio::join!(
        detect_claude(),
        detect_codex(),
        detect_grok(),
        detect_wrangler(),
        detect_node()
    );
    let claude_fast_ok = crate::agent::parse_version_triplet(&claude.version)
        .is_some_and(|v| v >= FAST_MIN_CLI);
    BrainsInfo {
        claude,
        codex,
        grok,
        wrangler,
        node,
        browser: detect_browser(),
        path_env: std::env::var("PATH").unwrap_or_default(),
        claude_fast_ok,
    }
}

/// PATH 上**所有**叫这个名字的可执行文件（去重、保序）。
///
/// 为什么需要「所有」而不是第一个：多安装是真实场景（实测同机有 native `~/.local/bin/claude`
/// 与 npm `/opt/homebrew/bin/claude` 两份，版本还不同）。**只有 PATH 里第一个会被真正调用**，
/// 所以升级必须针对它 —— 升了被遮蔽的那份，点完版本号纹丝不动，看着像功能坏了。
pub fn which_all(bin: &str) -> Vec<String> {
    let exts: Vec<String> = if cfg!(windows) {
        std::env::var("PATHEXT")
            .unwrap_or_else(|_| ".EXE;.CMD;.BAT".into())
            .split(';')
            .filter(|e| !e.is_empty())
            .map(|e| e.to_ascii_lowercase())
            .collect()
    } else {
        vec![String::new()]
    };
    let mut out: Vec<String> = Vec::new();
    for dir in std::env::split_paths(&std::env::var_os("PATH").unwrap_or_default()) {
        for ext in &exts {
            let p = dir.join(format!("{bin}{ext}"));
            if p.is_file() {
                let s = p.to_string_lossy().into_owned();
                if !out.contains(&s) {
                    out.push(s);
                }
            }
        }
    }
    out
}

async fn detect_node() -> BrainStatus {
    let mut st = BrainStatus::default();
    let Some(path) = which("node") else {
        st.detail = "PATH 中没有找到 Node.js".into();
        return st;
    };
    st.found = true;
    st.path = path;
    if let Some((_, ver)) = run_capture(&st.path, &["--version"], Duration::from_secs(8)).await {
        st.version = ver.trim().to_string();
    }
    st.logged_in = None;
    st
}

/// 探测可做无头截图的浏览器。路径清单与 tools.rs 生成的 shot.js 保持一致。
fn detect_browser() -> BrainStatus {
    let mut st = BrainStatus::default();
    let cands: Vec<std::path::PathBuf> = if cfg!(target_os = "macos") {
        [
            "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
            "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
            "/Applications/Chromium.app/Contents/MacOS/Chromium",
            "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
        ]
        .iter()
        .map(std::path::PathBuf::from)
        .collect()
    } else if cfg!(windows) {
        let mut v = Vec::new();
        for base in ["ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"] {
            if let Ok(b) = std::env::var(base) {
                v.push(
                    std::path::Path::new(&b)
                        .join("Google")
                        .join("Chrome")
                        .join("Application")
                        .join("chrome.exe"),
                );
                v.push(
                    std::path::Path::new(&b)
                        .join("Microsoft")
                        .join("Edge")
                        .join("Application")
                        .join("msedge.exe"),
                );
            }
        }
        v
    } else {
        [
            "/usr/bin/google-chrome",
            "/usr/bin/google-chrome-stable",
            "/usr/bin/chromium",
            "/usr/bin/chromium-browser",
            "/usr/bin/microsoft-edge",
        ]
        .iter()
        .map(std::path::PathBuf::from)
        .collect()
    };
    for c in cands {
        if c.exists() {
            st.found = true;
            st.path = c.to_string_lossy().into_owned();
            break;
        }
    }
    if !st.found {
        st.detail = "未检测到 Chrome / Edge / Chromium（AI 网页截图配图需要，可选）".into();
    }
    st
}

async fn detect_wrangler() -> BrainStatus {
    let mut st = BrainStatus::default();
    let Some(path) = which("wrangler") else {
        st.detail = "PATH 中没有找到 wrangler（Cloudflare 部署需要，可 npm i -g wrangler）".into();
        return st;
    };
    st.found = true;
    st.path = path;
    if let Some((_, ver)) = run_capture(&st.path, &["--version"], Duration::from_secs(10)).await {
        // wrangler --version 可能多行，取首个非空行。
        st.version = ver
            .lines()
            .find(|l| !l.trim().is_empty())
            .unwrap_or("")
            .trim()
            .to_string();
    }
    st.logged_in = None; // token 由 env 注入，不看登录态
    st
}

async fn run_capture(program: &str, args: &[&str], timeout: Duration) -> Option<(bool, String)> {
    let mut c = Command::new(program);
    c.args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    #[cfg(target_os = "windows")]
    c.creation_flags(0x0800_0000); // CREATE_NO_WINDOW：检测 CLI 不弹控制台
    let child = c.output();
    match tokio::time::timeout(timeout, child).await {
        Ok(Ok(out)) => {
            let mut text = String::from_utf8_lossy(&out.stdout).into_owned();
            if text.trim().is_empty() {
                text = String::from_utf8_lossy(&out.stderr).into_owned();
            }
            Some((out.status.success(), text.trim().to_string()))
        }
        _ => None,
    }
}

/// Pilot 中粘贴的 Claude 订阅 Token 只从系统安全存储读入，并只注入即将启动的
/// Claude 子进程。没有配置或安全存储暂时不可读时保持 CLI 原生登录路径。
fn apply_claude_oauth_token(command: &mut Command) -> bool {
    match crate::keychain::claude_oauth_token() {
        Ok(Some(token)) => {
            command
                .env_remove("ANTHROPIC_AUTH_TOKEN")
                .env_remove("ANTHROPIC_API_KEY")
                .env("CLAUDE_CODE_OAUTH_TOKEN", token);
            true
        }
        _ => false,
    }
}

async fn run_capture_claude(
    program: &str,
    args: &[&str],
    timeout: Duration,
) -> Option<(bool, String, bool)> {
    let mut command = Command::new(program);
    let token_configured = apply_claude_oauth_token(&mut command);
    command
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    #[cfg(target_os = "windows")]
    command.creation_flags(0x0800_0000);
    match tokio::time::timeout(timeout, command.output()).await {
        Ok(Ok(output)) => {
            let mut text = String::from_utf8_lossy(&output.stdout).into_owned();
            if text.trim().is_empty() {
                text = String::from_utf8_lossy(&output.stderr).into_owned();
            }
            Some((
                output.status.success(),
                text.trim().to_string(),
                token_configured,
            ))
        }
        _ => None,
    }
}

/// 解析可执行的完整路径给 spawn 用：Windows 上 npm 装的是 codex.cmd/wrangler.cmd，
/// 裸名 CreateProcess 只补 .exe 永远找不到；显式 .cmd 路径 std 会经 cmd.exe 启动。
/// 注意 .cmd 参数不能含换行，多行输入必须由调用方改走 stdin（见 agent::build_codex）。
/// 找不到就原样返回（保持旧行为，让系统再试一次并给出自然报错）。
pub fn resolve_bin(bin: &str) -> String {
    which(bin).unwrap_or_else(|| bin.to_string())
}

fn which(bin: &str) -> Option<String> {
    let path = std::env::var("PATH").ok()?;
    // Windows 的 PATH 分隔符是 ';'，且可执行文件带扩展名（node.exe / codex.cmd）——
    // 之前按 ':' 裸名查找，Windows 上永远找不到任何 CLI。
    let sep = if cfg!(windows) { ';' } else { ':' };
    let exts: Vec<String> = if cfg!(windows) {
        std::env::var("PATHEXT")
            .unwrap_or_else(|_| ".EXE;.CMD;.BAT;.COM".into())
            .split(';')
            .filter(|e| !e.is_empty())
            .map(|e| e.to_ascii_lowercase())
            .collect()
    } else {
        vec![String::new()]
    };
    for dir in path.split(sep).filter(|d| !d.is_empty()) {
        for ext in &exts {
            let cand = std::path::Path::new(dir).join(format!("{bin}{ext}"));
            if cand.is_file() {
                return Some(cand.to_string_lossy().into_owned());
            }
        }
    }
    None
}

/// Windows：GUI 进程的 PATH 在安装 Node/Claude 之后不会自动更新（系统 PATH 改了，
/// 已运行的进程看不见，要重启应用才生效）。把常见安装目录补进本进程 PATH——
/// 「重新检测」和后续 spawn（跑轮次、npm 安装）就都能立刻找到新装的 CLI。
fn augment_path_env() {
    if !cfg!(windows) {
        return;
    }
    let mut extra: Vec<std::path::PathBuf> = Vec::new();
    if let Ok(p) = std::env::var("ProgramFiles") {
        extra.push(std::path::Path::new(&p).join("nodejs"));
    }
    if let Ok(p) = std::env::var("APPDATA") {
        extra.push(std::path::Path::new(&p).join("npm")); // npm 全局（codex / wrangler）
    }
    if let Ok(p) = std::env::var("USERPROFILE") {
        extra.push(std::path::Path::new(&p).join(".local").join("bin")); // Claude 原生安装器
    }
    if let Ok(p) = std::env::var("LOCALAPPDATA") {
        extra.push(std::path::Path::new(&p).join("Programs").join("nodejs"));
    }
    let mut path = std::env::var("PATH").unwrap_or_default();
    for d in extra {
        if !d.is_dir() {
            continue;
        }
        let ds = d.to_string_lossy().into_owned();
        if !path.split(';').any(|p| p.eq_ignore_ascii_case(&ds)) {
            path.push(';');
            path.push_str(&ds);
        }
    }
    std::env::set_var("PATH", path);
}

async fn detect_claude() -> BrainStatus {
    let mut st = BrainStatus::default();
    let Some(path) = which("claude") else {
        st.detail = "PATH 中没有找到 claude，可先安装 Claude Code CLI".into();
        return st;
    };
    st.found = true;
    st.path = path;
    if let Some((_, ver)) = run_capture(&st.path, &["--version"], Duration::from_secs(10)).await {
        st.version = ver;
    }
    // 登出时退出码是 1，但 stdout 仍是 JSON —— 只解析 stdout。
    if let Some((_, out, token_configured)) = run_capture_claude(
        &st.path,
        &["auth", "status", "--json"],
        Duration::from_secs(15),
    )
    .await
    {
        if let Some(json_part) = extract_json(&out) {
            if let Ok(v) = serde_json::from_str::<serde_json::Value>(&json_part) {
                let logged = v
                    .get("loggedIn")
                    .or_else(|| v.get("logged_in"))
                    .and_then(serde_json::Value::as_bool);
                st.logged_in = logged;
                st.account = v
                    .get("email")
                    .or_else(|| v.get("account"))
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or_default()
                    .to_string();
            }
        }
        if st.logged_in.is_none() {
            st.detail = out.chars().take(200).collect();
        }
        if token_configured && st.logged_in == Some(true) && st.account.is_empty() {
            st.account = "Claude 订阅 Token".into();
        }
    }
    st
}

async fn detect_codex() -> BrainStatus {
    let mut st = BrainStatus::default();
    let Some(path) = which("codex") else {
        st.detail = "PATH 中没有找到 codex（可选）".into();
        return st;
    };
    st.found = true;
    st.path = path;
    if let Some((_, ver)) = run_capture(&st.path, &["--version"], Duration::from_secs(10)).await {
        st.version = ver;
    }
    if let Some((ok, out)) =
        run_capture(&st.path, &["login", "status"], Duration::from_secs(15)).await
    {
        // codex login status: 登录时 exit 0 且输出 "Logged in ..."。
        st.logged_in = Some(ok && out.to_lowercase().contains("logged in"));
        st.detail = out.chars().take(200).collect();
    }
    if st.logged_in == Some(true) {
        st.account = codex_account().unwrap_or_default();
    }
    st
}

/// Codex 当前账号：~/.codex/auth.json（CODEX_HOME 可改基目录）。
/// ChatGPT 登录 → tokens.id_token(JWT) 的 email 声明；API Key 模式没有身份，标 "API Key"。
/// 只读本地文件不跑网络；任何一步取不到都返回 None（界面上不显示而已，不算错）。
fn codex_account() -> Option<String> {
    let base = std::env::var("CODEX_HOME")
        .map(std::path::PathBuf::from)
        .or_else(|_| {
            std::env::var(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
                .map(|h| std::path::Path::new(&h).join(".codex"))
        })
        .ok()?;
    let v: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(base.join("auth.json")).ok()?).ok()?;
    if let Some(email) = v
        .pointer("/tokens/id_token")
        .and_then(serde_json::Value::as_str)
        .and_then(jwt_email)
    {
        return Some(email);
    }
    v.get("OPENAI_API_KEY")
        .and_then(serde_json::Value::as_str)
        .filter(|s| !s.trim().is_empty())
        .map(|_| "API Key".to_string())
}

/// 从 JWT 载荷取 email 声明（不验签——只做展示，凭据真伪由 CLI 自己负责）。
fn jwt_email(token: &str) -> Option<String> {
    use base64::Engine;
    let payload = token.split('.').nth(1)?;
    let bytes = base64::engine::general_purpose::URL_SAFE_NO_PAD
        .decode(payload.trim_end_matches('='))
        .ok()?;
    let v: serde_json::Value = serde_json::from_slice(&bytes).ok()?;
    v.get("email")
        .and_then(serde_json::Value::as_str)
        .filter(|s| !s.is_empty())
        .map(str::to_string)
}

/// xAI Grok CLI。安装器默认放 ~/.grok/bin（并 symlink ~/.local/bin），PATH 缺失时兜底直查。
/// 登录态**不跑网络命令**：官方安装脚本同款判据——~/.grok/auth.json 存在即已登录
///（`grok login` 写入、`grok logout` 删除；文件内容是 token，不解析只看存在与体量）。
async fn detect_grok() -> BrainStatus {
    let mut st = BrainStatus::default();
    let path = which("grok").or_else(|| {
        let home = std::env::var(if cfg!(windows) { "USERPROFILE" } else { "HOME" }).ok()?;
        let cand = std::path::Path::new(&home)
            .join(".grok")
            .join("bin")
            .join(if cfg!(windows) { "grok.exe" } else { "grok" });
        cand.is_file().then(|| cand.to_string_lossy().into_owned())
    });
    let Some(path) = path else {
        st.detail = "PATH 中没有找到 grok（可选）".into();
        return st;
    };
    st.found = true;
    st.path = path;
    if let Some((_, ver)) = run_capture(&st.path, &["--version"], Duration::from_secs(10)).await {
        st.version = ver
            .lines()
            .find(|l| !l.trim().is_empty())
            .unwrap_or("")
            .trim()
            .to_string();
    }
    let auth = std::env::var("GROK_HOME")
        .map(std::path::PathBuf::from)
        .or_else(|_| {
            std::env::var(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
                .map(|h| std::path::Path::new(&h).join(".grok"))
        })
        .map(|d| d.join("auth.json"));
    st.logged_in = Some(
        auth.as_ref()
            .map(|p| std::fs::metadata(p).map(|m| m.len() > 10).unwrap_or(false))
            .unwrap_or(false),
    );
    if st.logged_in == Some(false) {
        st.detail = "未登录：终端运行 grok login 完成授权".into();
    } else if let Ok(p) = auth.as_ref() {
        // 账号：auth.json 是 { "issuer::uuid": { email, ... } } 形状的凭据表，
        // 取第一条的 email 展示（只读本地文件，不跑网络；解析失败就不显示）。
        st.account = std::fs::read_to_string(p)
            .ok()
            .and_then(|txt| serde_json::from_str::<serde_json::Value>(&txt).ok())
            .and_then(|v| {
                v.as_object().and_then(|o| {
                    o.values()
                        .filter_map(|e| e.get("email").and_then(serde_json::Value::as_str))
                        .find(|s| !s.is_empty())
                        .map(str::to_string)
                })
            })
            .unwrap_or_default();
    }
    st
}

/// stdout 里可能混有非 JSON 行（升级提示等），取第一个 { 到最后一个 }。
fn extract_json(s: &str) -> Option<String> {
    let start = s.find('{')?;
    let end = s.rfind('}')?;
    if end > start {
        Some(s[start..=end].to_string())
    } else {
        None
    }
}


#[cfg(test)]
mod version_cmp_tests {
    use super::*;
    use crate::agent::parse_version_triplet as pv;

    /// ★ 版本号必须按数值比。字符串序下 "2.1.96" > "2.1.220" 成立，
    /// 那会把「有新版」判成「已最新」，而且不报错、只是升级入口永远不出现。
    #[test]
    fn version_compare_is_numeric_not_lexical() {
        let cur = pv("2.1.96 (Claude Code)").unwrap();
        let latest = pv("2.1.220").unwrap();
        assert_eq!(cur, (2, 1, 96), "要能吃 --version 的后缀");
        assert!(latest > cur, "2.1.220 必须判为比 2.1.96 新");
        assert!("2.1.96" > "2.1.220", "（对照）字符串比就是反的——所以不能用字符串");
    }

    /// fast 的门槛：2.1.96 不够，2.1.205 刚够，2.1.220 够。
    #[test]
    fn fast_gate_matches_documented_minimum() {
        assert!(pv("2.1.96").unwrap() < FAST_MIN_CLI, "2.1.96 不该判为支持 fast");
        assert!(pv("2.1.112").unwrap() < FAST_MIN_CLI, "npm 那份 2.1.112 也不够");
        assert!(pv("2.1.205").unwrap() >= FAST_MIN_CLI, "2.1.205 是文档给的最低版本");
        assert!(pv("2.1.220").unwrap() >= FAST_MIN_CLI);
    }
}
