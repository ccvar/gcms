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
    pub path_env: String,
}

pub async fn detect() -> BrainsInfo {
    let (claude, codex) = tokio::join!(detect_claude(), detect_codex());
    BrainsInfo {
        claude,
        codex,
        path_env: std::env::var("PATH").unwrap_or_default(),
    }
}

async fn run_capture(program: &str, args: &[&str], timeout: Duration) -> Option<(bool, String)> {
    let child = Command::new(program)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .output();
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

fn which(bin: &str) -> Option<String> {
    let path = std::env::var("PATH").ok()?;
    for dir in path.split(':') {
        let cand = std::path::Path::new(dir).join(bin);
        if cand.is_file() {
            return Some(cand.to_string_lossy().into_owned());
        }
    }
    None
}

async fn detect_claude() -> BrainStatus {
    let mut st = BrainStatus::default();
    let Some(path) = which("claude") else {
        st.detail = "PATH 中没有找到 claude，可先安装 Claude Code CLI".into();
        return st;
    };
    st.found = true;
    st.path = path;
    if let Some((_, ver)) = run_capture("claude", &["--version"], Duration::from_secs(10)).await {
        st.version = ver;
    }
    // 登出时退出码是 1，但 stdout 仍是 JSON —— 只解析 stdout。
    if let Some((_, out)) =
        run_capture("claude", &["auth", "status", "--json"], Duration::from_secs(15)).await
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
    if let Some((_, ver)) = run_capture("codex", &["--version"], Duration::from_secs(10)).await {
        st.version = ver;
    }
    if let Some((ok, out)) = run_capture("codex", &["login", "status"], Duration::from_secs(15)).await
    {
        // codex login status: 登录时 exit 0 且输出 "Logged in ..."。
        st.logged_in = Some(ok && out.to_lowercase().contains("logged in"));
        st.detail = out.chars().take(200).collect();
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
