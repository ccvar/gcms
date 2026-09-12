//! Per-invocation GCMS shell environment. Never persist credentials or change
//! the user's global Codex configuration. Other connection types keep theirs.

use serde_json::{json, Value};
use tokio::process::Command;

pub(crate) const POLICY_VERSION: &str = "gcms-shell-env-v1";
pub(crate) const VALIDATION_ERROR: &str = "Pilot GCMS 环境校验失败";

// Keep the runtime usable on Windows and Unix, but do not expose unrelated
// provider credentials (OPENAI_*, AWS_*, etc.) to GCMS command subprocesses.
const SHELL_VARIABLES: &[&str] = &[
    "PATH",
    "PATHEXT",
    "HOME",
    "USER",
    "LOGNAME",
    "SHELL",
    "LANG",
    "LC_*",
    "TZ",
    "TERM",
    "COLORTERM",
    "TMPDIR",
    "TMP",
    "TEMP",
    "SYSTEMROOT",
    "WINDIR",
    "COMSPEC",
    "USERPROFILE",
    "HOMEDRIVE",
    "HOMEPATH",
    "APPDATA",
    "LOCALAPPDATA",
    "PROGRAMDATA",
    "PROGRAMFILES",
    "PROGRAMFILES(X86)",
    "COMMONPROGRAMFILES",
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "ALL_PROXY",
    "NO_PROXY",
    "SSL_CERT_FILE",
    "SSL_CERT_DIR",
    "NODE_EXTRA_CA_CERTS",
    "GCMS_API_BASE",
    "GCMS_API_KEY",
    "GCMS_CONTROL_UNLOCK_TOKEN",
];

fn shell_policy() -> Value {
    json!({
        // `include_only` cannot restore variables already removed by `core`
        // or the default KEY/TOKEN exclusion. Start from the parent environment
        // and then restrict it to the explicit allowlist, including GCMS keys.
        "inherit": "all",
        "ignore_default_excludes": true,
        "exclude": [],
        "include_only": SHELL_VARIABLES
    })
}

pub(crate) fn apply_cli(cmd: &mut Command, kind: &str) {
    if kind != "gcms" {
        return;
    }
    // Override the complete table, not individual leaves: otherwise existing
    // `filters`, `exclude` or `include_only` could still strip credentials.
    // Nested `set` entries can survive Codex's layer merge; reject those below
    // instead of silently using a key from another connection.
    // argv contains names only, never credential values.
    let names = serde_json::to_string(SHELL_VARIABLES).expect("static variable names");
    cmd.args([
        "-c",
        &format!(
            "shell_environment_policy={{inherit=\"all\",ignore_default_excludes=true,exclude=[],include_only={names}}}"
        ),
    ]);
}

pub(crate) fn validate_effective_policy(config: &Value) -> Result<(), String> {
    let policy = &config["shell_environment_policy"];
    let overrides_connection = policy["set"].as_object().is_some_and(|set| {
        set.keys().any(|name| {
            matches!(
                name.to_ascii_uppercase().as_str(),
                "GCMS_API_BASE" | "GCMS_API_KEY" | "GCMS_CONTROL_UNLOCK_TOKEN"
            )
        })
    });
    if overrides_connection {
        return Err(format!("{VALIDATION_ERROR}：Codex 配置的 shell_environment_policy.set 中存在 GCMS 凭证覆盖。为避免使用其它连接的密钥，本轮未启动。请移除该处的 GCMS 覆盖项，使用 Pilot 连接设置管理凭证；不要把密钥发送到聊天中。"));
    }
    if policy["inherit"] != "all"
        || policy["ignore_default_excludes"] != true
        || policy["include_only"] != json!(SHELL_VARIABLES)
        || policy["exclude"] != json!([])
        || !policy["filters"].is_null()
    {
        return Err(format!("{VALIDATION_ERROR}：当前 Codex 环境限制未允许 Pilot 的连接凭证传入脚本。本轮未启动，请检查 Codex 的生效配置或联系管理员；重复填写 API 密钥无法解除此限制。"));
    }
    Ok(())
}

pub(crate) fn thread_config(kind: &str) -> Value {
    let mut config = json!({"sandbox_workspace_write": {"network_access": true}});
    if kind == "gcms" {
        // Explicit on both thread/start and thread/resume, including old threads.
        config["shell_environment_policy"] = shell_policy();
    }
    config
}

pub(crate) fn validate_credentials(kind: &str, base: &str, key: &str) -> Result<(), String> {
    if kind == "gcms" && (base.trim().is_empty() || key.trim().is_empty()) {
        return Err("当前 GCMS 连接缺少平台地址或 API 密钥，尚未启动任务。请在 Pilot 的连接设置中检查并重新保存；不要把密钥发送到聊天中。这与 Codex 账号登录是两套独立的凭证。".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    // Opt-in real CLI regression. Uses only dummy values, a disposable CODEX_HOME,
    // and local command/exec (no model request, account login, or GCMS network call).
    // PILOT_TEST_CODEX_BIN=/absolute/codex PILOT_TEST_NODE_BIN=/absolute/node
    // cargo test --lib isolated_codex_environment -- --ignored
    #[tokio::test]
    #[ignore = "requires an installed Codex/Node runtime; never uses real credentials"]
    async fn isolated_codex_environment() {
        use std::process::Stdio;
        use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};

        async fn rpc(
            input: &mut tokio::process::ChildStdin,
            output: &mut tokio::io::Lines<BufReader<tokio::process::ChildStdout>>,
            id: u64,
            method: &str,
            params: Value,
        ) -> Result<Value, String> {
            let request = json!({"id": id, "method": method, "params": params});
            input
                .write_all(format!("{request}\n").as_bytes())
                .await
                .map_err(|_| "write failed")?;
            tokio::time::timeout(std::time::Duration::from_secs(30), async {
                while let Some(line) = output.next_line().await.map_err(|_| "read failed")? {
                    let Ok(response) = serde_json::from_str::<Value>(&line) else {
                        continue;
                    };
                    if response["id"] == id {
                        // Never print subprocess output or environmental values on failure.
                        if response.get("error").is_some() {
                            return Err(format!("{method} rejected"));
                        }
                        return Ok(response["result"].clone());
                    }
                }
                Err(format!("{method} ended without response"))
            })
            .await
            .map_err(|_| format!("{method} timed out"))?
        }

        let bin = std::env::var_os("PILOT_TEST_CODEX_BIN").expect("set PILOT_TEST_CODEX_BIN");
        let node = std::env::var("PILOT_TEST_NODE_BIN").expect("set PILOT_TEST_NODE_BIN");
        for (fixture, repair, unlock) in [
            ("inherit=\"core\"", false, false),
            ("inherit=\"core\"", true, false),
            ("inherit=\"none\"\nignore_default_excludes=false\nexclude=[\"GCMS_*\"]\ninclude_only=[\"PATH\"]", true, true),
            ("inherit=\"core\"\nfilters={\"GCMS_*\"=\"exclude\"}", true, true),
        ] {
            let root = tempfile::tempdir().unwrap();
            let config = format!("[shell_environment_policy]\n{fixture}\n");
            std::fs::write(root.path().join("config.toml"), &config).unwrap();
            let mut cmd = Command::new(&bin);
            cmd.args(["app-server", "--stdio"]);
            if repair { apply_cli(&mut cmd, "gcms"); }
            cmd.env_clear();
            for name in SHELL_VARIABLES.iter().filter(|name| !name.starts_with("GCMS_") && !name.contains('*')) {
                if let Some(value) = std::env::var_os(name) { cmd.env(name, value); }
            }
            cmd.current_dir(root.path())
                .env("CODEX_HOME", root.path())
                .env("HOME", root.path())
                .env("USERPROFILE", root.path())
                .env("GCMS_API_BASE", "https://example.invalid")
                .env("GCMS_API_KEY", "test-only-key")
                .env("UNRELATED_SECRET", "must-not-pass")
                .stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::null())
                .kill_on_drop(true);
            if unlock { cmd.env("GCMS_CONTROL_UNLOCK_TOKEN", "test-only-unlock"); }
            let mut child = cmd.spawn().expect("start isolated Codex");
            let mut input = child.stdin.take().unwrap();
            let mut output = BufReader::new(child.stdout.take().unwrap()).lines();
            let result: Result<Value, String> = async {
                rpc(&mut input, &mut output, 1, "initialize", json!({
                    "clientInfo": {"name": "pilot-env-test", "version": "0.0.0"},
                    "capabilities": {"experimentalApi": true}
                })).await?;
                input.write_all(b"{\"method\":\"initialized\"}\n").await.map_err(|_| "initialize failed")?;
                let effective = rpc(&mut input, &mut output, 3, "config/read", json!({"includeLayers":false,"cwd":root.path()})).await?;
                if repair { validate_effective_policy(&effective["config"])?; }
                rpc(&mut input, &mut output, 2, "command/exec", json!({
                    "command": [node, "-e", "console.log(JSON.stringify({base:process.env.GCMS_API_BASE==='https://example.invalid',key:process.env.GCMS_API_KEY==='test-only-key',unlock:process.env.GCMS_CONTROL_UNLOCK_TOKEN==='test-only-unlock',unrelated:!!process.env.UNRELATED_SECRET}))"],
                    "cwd": root.path(), "timeoutMs": 10000
                })).await
            }.await;
            let _ = child.kill().await;
            let _ = child.wait().await;
            let result = result.expect("isolated RPC check");
            assert_eq!(result["exitCode"], 0, "probe must complete");
            let presence: Value = serde_json::from_str(result["stdout"].as_str().unwrap_or("")).expect("boolean-only probe response");
            assert_eq!(presence, json!({"base":repair,"key":repair,"unlock":repair && unlock,"unrelated":false}));
            assert_eq!(std::fs::read_to_string(root.path().join("config.toml")).unwrap(), config);
        }
    }

    #[test]
    fn policy_keeps_only_runtime_and_current_gcms_credentials() {
        let config = thread_config("gcms");
        let policy = &config["shell_environment_policy"];
        assert_eq!(policy["inherit"], "all");
        assert_eq!(policy["ignore_default_excludes"], true);
        assert_eq!(policy["exclude"], json!([]));
        assert!(policy.get("set").is_none());
        for name in [
            "GCMS_API_BASE",
            "GCMS_API_KEY",
            "GCMS_CONTROL_UNLOCK_TOKEN",
            "SYSTEMROOT",
            "PATH",
            "HOME",
        ] {
            assert!(SHELL_VARIABLES.contains(&name));
        }
        for name in [
            "*",
            "GCMS_*",
            "OPENAI_API_KEY",
            "AWS_SECRET_ACCESS_KEY",
            "CODEX_HOME",
        ] {
            assert!(!SHELL_VARIABLES.contains(&name));
        }
        assert_eq!(config["sandbox_workspace_write"]["network_access"], true);
    }

    #[test]
    fn other_connection_policies_are_untouched() {
        for kind in ["workspace", "ssh", "cloudflare"] {
            let mut cmd = Command::new("codex");
            apply_cli(&mut cmd, kind);
            assert_eq!(cmd.as_std().get_args().count(), 0);
            assert!(thread_config(kind)
                .get("shell_environment_policy")
                .is_none());
            assert!(validate_credentials(kind, "", "").is_ok());
        }
    }

    #[test]
    fn cli_and_rpc_share_the_allowlist() {
        let mut cmd = Command::new("codex");
        apply_cli(&mut cmd, "gcms");
        let args: Vec<_> = cmd.as_std().get_args().collect();
        assert_eq!(args[0], "-c");
        let arg = args[1].to_str().unwrap();
        let names = arg
            .split_once("include_only=")
            .unwrap()
            .1
            .strip_suffix('}')
            .unwrap();
        assert_eq!(
            serde_json::from_str::<Value>(names).unwrap(),
            shell_policy()["include_only"]
        );
        assert!(!arg.contains('\n'));
        assert!(!arg.contains("set="));
    }

    #[test]
    fn missing_credentials_fail_without_disclosing_values() {
        for (base, key) in [("", "dummy-private-key"), ("https://private.invalid", " ")] {
            let error = validate_credentials("gcms", base, key).unwrap_err();
            assert!(!error.contains("dummy-private-key"));
            assert!(!error.contains("private.invalid"));
        }
        assert!(validate_credentials("gcms", "https://example.invalid", "test-only").is_ok());
    }

    #[test]
    fn explicit_config_credential_overrides_fail_closed() {
        let config = thread_config("gcms");
        assert!(validate_effective_policy(&config).is_ok());
        for name in ["GCMS_API_BASE", "gcms_api_key", "GCMS_CONTROL_UNLOCK_TOKEN"] {
            let mut overridden = config.clone();
            overridden["shell_environment_policy"]["set"] = json!({name:"do-not-disclose"});
            let error = validate_effective_policy(&overridden).unwrap_err();
            assert!(error.starts_with(VALIDATION_ERROR));
            assert!(!error.contains("do-not-disclose"));
        }
        let mut restricted = config;
        restricted["shell_environment_policy"]["inherit"] = json!("core");
        assert!(validate_effective_policy(&restricted).is_err());
    }
}
