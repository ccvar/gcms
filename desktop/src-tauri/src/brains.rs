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
}

pub async fn detect() -> BrainsInfo {
    augment_path_env(); // 每次检测都补一遍：刚装完的目录此刻才存在
    let (claude, codex, grok, wrangler, node) = tokio::join!(
        detect_claude(),
        detect_codex(),
        detect_grok(),
        detect_wrangler(),
        detect_node()
    );
    BrainsInfo {
        claude,
        codex,
        grok,
        wrangler,
        node,
        browser: detect_browser(),
        path_env: std::env::var("PATH").unwrap_or_default(),
    }
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
    if let Some((_, out)) = run_capture(
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
    st
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
