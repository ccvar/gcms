//! macOS GUI 进程拿到的是裸环境（PATH=/usr/bin:/bin…，也没有代理变量），
//! 找不到 Homebrew / nvm / cargo 安装的 claude、codex、node，
//! 且直连 Anthropic/OpenAI 会被区域屏蔽（用户代理通常配在 .zshrc）。
//! 这里在启动最早期起一次用户交互登录 shell（-l -i，读 .zprofile + .zshrc），
//! 把 PATH 与代理变量注入本进程——之后 spawn 的所有 CLI 子进程都继承。

const MARK: &str = "__GCMS_PILOT_ENV__";

/// 白名单导入：PATH + 代理相关。不整包导入，避免把 shell 私货带进 GUI 进程。
const IMPORT_KEYS: &[&str] = &[
    "PATH",
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "ALL_PROXY",
    "NO_PROXY",
    "http_proxy",
    "https_proxy",
    "all_proxy",
    "no_proxy",
];

pub fn fix() {
    if !cfg!(target_os = "macos") {
        return;
    }
    if let Some(pairs) = login_shell_env() {
        for (k, v) in pairs {
            if IMPORT_KEYS.contains(&k.as_str()) && !v.trim().is_empty() {
                std::env::set_var(&k, v.trim());
            }
        }
    }
}

fn login_shell_env() -> Option<Vec<(String, String)>> {
    let shell = std::env::var("SHELL").unwrap_or_else(|_| "/bin/zsh".to_string());
    let cmd = format!("printf '%s\\n' \"{MARK}\"; env; printf '%s\\n' \"{MARK}\"");
    let out = std::process::Command::new(&shell)
        .args(["-l", "-i", "-c", &cmd])
        .output()
        .ok()?;
    let stdout = String::from_utf8_lossy(&out.stdout);
    // shell rc 文件可能自带输出，只取两个标记行之间的内容。
    let start = stdout.find(MARK)? + MARK.len();
    let end = start + stdout[start..].rfind(MARK)?;
    let mut pairs = Vec::new();
    for line in stdout[start..end].lines() {
        if let Some((k, v)) = line.split_once('=') {
            if !k.is_empty() {
                pairs.push((k.to_string(), v.to_string()));
            }
        }
    }
    Some(pairs)
}
