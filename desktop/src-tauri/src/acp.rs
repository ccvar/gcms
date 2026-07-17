//! grok（xAI Grok CLI）第三厂商接入：ACP（Agent Client Protocol）客户端，JSON-RPC over stdio。
//!
//! 为什么走 ACP 而不是 `grok -p` 无头：无头流只有 thought/text/end——没有工具事件、
//! 也没有权限回调；ACP 两者齐备（`session/update` 的 tool_call*、`session/request_permission`），
//! 正是 Pilot 工具卡与批准卡需要的。协议均已真机实测（grok 0.2.99/0.2.101），
//! 单测夹具就是实测抓的帧。
//!
//! 进程生命周期：**一轮一进程**（spawn → initialize → session/new 或 session/load →
//! session/prompt → 收结果 → 杀进程）。选它的理由：与 run_turn 的取消模型（RunRegistry
//! 杀进程树）完全对齐、无常驻进程状态可坏；grok 会话本身持久化在 ~/.grok/sessions，
//! `session/load` 续轮是本地重放，代价可忽略。
//!
//! 多轮：session/new 结果里的 sessionId 存 conv.session_ref；续轮 `session/load` 同
//! id（cwd 必须与建会话时一致——Pilot 的 work_dir 按会话固定，天然满足）。load 的
//! 历史重放帧（user_message_chunk / 已完成的 tool_call）发生在 prompt 之前，靠
//! 「prompt 已发出」标志隔离，不会混进本轮渲染。
//!
//! 权限桥：`session/request_permission` → 按档位裁决：full 全放（进程同时挂
//! --always-approve）、plan 只读放行其余拒绝、auto 安全放行/危险弹卡、ask 全弹卡。
//! 弹卡＝写 permit pending 目录（与 claude 钩子中枢同一文件协议，前端批准卡零改动），
//! 轮询等用户决定；**超时/异常/找不到选项一律拒绝（fail-closed）**。注意 grok 语义：
//! 拒绝会取消整轮（stopReason=cancelled + cancellationCategory=PermissionRejected），
//! 不像 claude 把拒绝原因回给模型续跑——按用户主动决定处理，不算错误。

use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::{Arc, Mutex};
use tauri::ipc::Channel;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::process::{ChildStdin, Command};

use crate::agent::{self, RunRegistry, TurnEvent, TurnResult, TurnUsage};
use crate::convo::ToolCall;
use crate::pack::Connection;
use crate::permit::{self, PermMode};

/// 批准卡超时：与 claude 钩子中枢一致，15 分钟没人点＝拒绝。
const CARD_TIMEOUT_SECS: u64 = 15 * 60;
/// initialize / session-new / session-load 的握手超时（不含 prompt——长任务合法，靠停止按钮）。
const HANDSHAKE_TIMEOUT_SECS: u64 = 120;

/// 本轮收集的状态（与 agent.rs Collect 同型，但 ACP 专用字段更少）。
struct State {
    text: String,
    tools: Vec<ToolCall>,
    session_ref: String,
    /// stdout 里最后一条非 JSON 行（CLI 崩溃诊断线索）。
    raw_tail: String,
}

/// prompt 结果要点。
struct Done {
    stop_reason: String,
    cancel_category: String,
    usage: Option<TurnUsage>,
}

#[allow(clippy::too_many_arguments)]
pub async fn run_turn(
    registry: RunRegistry,
    conn: Connection,
    work_dir: String,
    model: String,
    mode: PermMode,
    effort: String,
    pending_dir: PathBuf,
    ssh_js: PathBuf,
    lease: Option<&crate::bridge::Lease>,
    session_ref: String,
    is_first: bool,
    system: Option<String>,
    message: String,
    turn_id: String,
    channel: Channel<TurnEvent>,
    api_key: String,
    plugin_dir: Option<PathBuf>,
) -> TurnResult {
    let fail = |e: String, session_ref: String| {
        let _ = channel.send(TurnEvent::Done { ok: false, error: e.clone() });
        TurnResult { ok: false, text: String::new(), tools: vec![], error: e, session_ref, proposal: None, usage: None, limit_reset: None }
    };

    let model = model.trim().to_string();
    if !model.is_empty() && (model.starts_with('-') || model.contains(char::is_whitespace)) {
        return fail(format!("无效的模型标识: {model}"), session_ref);
    }

    let mut cmd = Command::new(crate::brains::resolve_bin("grok"));
    cmd.arg("agent");
    // 随附的建站设计技能：grok 认 claude 那套 plugin 目录格式（实测 `grok plugin validate` 直接过），
    // 所以一个目录喂两家。每轮都带 —— 技能的价值就在任意一轮能重新拉取。
    if let Some(p) = plugin_dir.as_deref() {
        cmd.args(["--plugin-dir", &p.to_string_lossy()]);
    }
    if !model.is_empty() {
        cmd.args(["--model", &model]);
    }
    // 推理强度直通（grok 另有 xhigh/max 档，Pilot 三档语义与两家对齐即可）；空＝跟随默认。
    if matches!(effort.as_str(), "low" | "medium" | "high") {
        cmd.args(["--reasoning-effort", &effort]);
    }
    // full 档让 agent 自己全放行（省一来一回）；其余档位靠权限桥逐个裁决。
    if mode == PermMode::Full {
        cmd.arg("--always-approve");
    }
    cmd.arg("stdio");
    agent::apply_env_cwd(&mut cmd, &conn, &work_dir, &api_key, lease);
    cmd.stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    #[cfg(unix)]
    cmd.process_group(0);
    #[cfg(windows)]
    cmd.creation_flags(0x0800_0000); // CREATE_NO_WINDOW：跑 CLI 不弹控制台

    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => return fail(format!("启动 grok 失败（确认已安装并登录）: {e}"), session_ref),
    };
    let mut stdin = match child.stdin.take() {
        Some(s) => s,
        None => return fail("grok stdin 不可用".into(), session_ref),
    };
    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let pid = child.id();
    let (_canceled, mut kill_rx) = registry.register(&turn_id);

    let st = Arc::new(Mutex::new(State {
        text: String::new(),
        tools: Vec::new(),
        session_ref: session_ref.clone(),
        raw_tail: String::new(),
    }));
    let err_buf = Arc::new(Mutex::new(String::new()));
    let err_task = stderr.map(|s| collect_stderr(s, err_buf.clone()));

    let drive_res: Result<Done, String> = match stdout {
        Some(out) => {
            let fut = drive(
                &mut stdin, out, &st, &channel, is_first, &session_ref, system.as_deref(),
                &message, &work_dir, mode, &pending_dir, &ssh_js, &turn_id,
            );
            tokio::select! {
                r = fut => r,
                _ = &mut kill_rx => Err("已停止".into()),
            }
        }
        None => Err("grok stdout 不可用".into()),
    };

    // 一轮一进程：无论结局如何都把整棵进程树带走（grok 还会派生命令子进程）。
    agent::kill_tree(pid);
    let _ = child.kill().await;
    let _ = child.wait().await;
    if let Some(t) = err_task {
        let _ = t.await;
    }
    let canceled = registry.is_canceled(&turn_id);
    registry.unregister(&turn_id);
    // 清掉本会话遗留的批准卡（取消/超时路径 req 文件不会自删）。
    permit::sweep_conv(&pending_dir, &turn_id);

    let s = st.lock().unwrap();
    let err_text = err_buf.lock().unwrap().clone();

    let (ok, mut text, error) = match (&drive_res, canceled) {
        (_, true) => (false, s.text.clone(), "已停止".to_string()),
        (Ok(done), _) => match done.stop_reason.as_str() {
            "end_turn" => (true, s.text.clone(), String::new()),
            // 用户在批准卡上点了拒绝：grok 会取消整轮——这是用户主动决定，不算错误。
            "cancelled" if done.cancel_category == "PermissionRejected" => {
                let t = if s.text.trim().is_empty() {
                    "已按你的选择拒绝了该操作，本轮到此为止。需要继续，直接说下一步怎么做。".to_string()
                } else {
                    s.text.clone()
                };
                (true, t, String::new())
            }
            other => (false, s.text.clone(), format!("模型终止（{other}）")),
        },
        (Err(e), _) => {
            // 补上 stderr / stdout 尾行的诊断线索（限流/欠费信息常在 stderr）。
            let hint = agent::last_nonempty(&err_text)
                .or_else(|| if s.raw_tail.is_empty() { None } else { Some(s.raw_tail.clone()) })
                .unwrap_or_default();
            let msg = if hint.is_empty() || e.contains(&hint) { e.clone() } else { format!("{e}（{hint}）") };
            (false, s.text.clone(), msg)
        }
    };

    let usage = drive_res.as_ref().ok().and_then(|d| d.usage.clone());
    let limit_reset = if ok {
        None
    } else {
        agent::detect_usage_limit(&format!("{error}\n{err_text}\n{}", s.raw_tail))
    };
    let (clean_text, proposal) = agent::extract_proposal(&text);
    text = clean_text;

    let _ = channel.send(TurnEvent::Done { ok, error: error.clone() });
    TurnResult {
        ok,
        text,
        tools: s.tools.clone(),
        error,
        session_ref: s.session_ref.clone(),
        proposal,
        usage,
        limit_reset,
    }
}

/// 协议主驱动：握手 → 建/载会话 → 发 prompt → 消费流直到 prompt 响应。
/// 顺序执行（权限等待期间不读流——agent 此刻本就阻塞在等我们应答）。
#[allow(clippy::too_many_arguments)]
async fn drive(
    stdin: &mut ChildStdin,
    stdout: tokio::process::ChildStdout,
    st: &Arc<Mutex<State>>,
    ch: &Channel<TurnEvent>,
    is_first: bool,
    session_ref: &str,
    system: Option<&str>,
    message: &str,
    work_dir: &str,
    mode: PermMode,
    pending_dir: &Path,
    ssh_js: &Path,
    conv_id: &str,
) -> Result<Done, String> {
    let mut reader = BufReader::new(stdout);
    let hs = std::time::Duration::from_secs(HANDSHAKE_TIMEOUT_SECS);

    send(stdin, &serde_json::json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": 1,
            // 不给 fs/terminal 能力：文件与命令都由 grok 自己的内置工具执行（同无头行为），
            // Pilot 只做流渲染与权限裁决。
            "clientCapabilities": { "fs": { "readTextFile": false, "writeTextFile": false }, "terminal": false }
        }
    })).await?;
    tokio::time::timeout(hs, pump(stdin, &mut reader, st, ch, 1, false, mode, pending_dir, ssh_js, conv_id))
        .await
        .map_err(|_| "grok initialize 超时".to_string())??;

    let sid = if is_first {
        let mut params = serde_json::json!({ "cwd": work_dir, "mcpServers": [] });
        if let Some(sys) = system {
            // _meta.rules＝追加到系统提示（append 语义，实测生效）；绝不用 systemPromptOverride
            //（那是整体替换，会把 grok 自身的代理框架掀掉）。
            params["_meta"] = serde_json::json!({ "rules": sys });
        }
        let res = tokio::time::timeout(hs, async {
            send(stdin, &serde_json::json!({ "jsonrpc": "2.0", "id": 2, "method": "session/new", "params": params })).await?;
            pump(stdin, &mut reader, st, ch, 2, false, mode, pending_dir, ssh_js, conv_id).await
        })
        .await
        .map_err(|_| "grok 建会话超时".to_string())??;
        let sid = res.get("sessionId").and_then(|s| s.as_str()).unwrap_or_default().to_string();
        if sid.is_empty() {
            return Err("grok 未返回会话 id".into());
        }
        sid
    } else {
        tokio::time::timeout(hs, async {
            send(stdin, &serde_json::json!({
                "jsonrpc": "2.0", "id": 2, "method": "session/load",
                "params": { "sessionId": session_ref, "cwd": work_dir, "mcpServers": [] }
            })).await?;
            pump(stdin, &mut reader, st, ch, 2, false, mode, pending_dir, ssh_js, conv_id).await
        })
        .await
        .map_err(|_| "grok 载入会话超时".to_string())??;
        session_ref.to_string()
    };
    st.lock().unwrap().session_ref = sid.clone();

    send(stdin, &serde_json::json!({
        "jsonrpc": "2.0", "id": 3, "method": "session/prompt",
        "params": { "sessionId": sid, "prompt": [{ "type": "text", "text": message }] }
    })).await?;
    // prompt 阶段不设总超时：长任务合法，取消交给停止按钮（外层 select 杀进程）。
    let res = pump(stdin, &mut reader, st, ch, 3, true, mode, pending_dir, ssh_js, conv_id).await?;

    let stop_reason = res.get("stopReason").and_then(|s| s.as_str()).unwrap_or("").to_string();
    let meta = &res["_meta"];
    Ok(Done {
        stop_reason,
        cancel_category: meta.get("cancellationCategory").and_then(|s| s.as_str()).unwrap_or("").to_string(),
        usage: usage_from_meta(meta),
    })
}

/// 读流直到等到 `want_id` 的响应：期间处理 agent 的请求（权限）与通知（流式渲染）。
/// streaming=false 时忽略 session/update（session/load 的历史重放不进本轮渲染）。
#[allow(clippy::too_many_arguments)]
async fn pump(
    stdin: &mut ChildStdin,
    reader: &mut BufReader<tokio::process::ChildStdout>,
    st: &Arc<Mutex<State>>,
    ch: &Channel<TurnEvent>,
    want_id: u64,
    streaming: bool,
    mode: PermMode,
    pending_dir: &Path,
    ssh_js: &Path,
    conv_id: &str,
) -> Result<serde_json::Value, String> {
    let mut buf = Vec::new();
    loop {
        buf.clear();
        let n = reader.read_until(b'\n', &mut buf).await.map_err(|e| format!("读取 grok 输出失败: {e}"))?;
        if n == 0 {
            return Err("grok 进程意外退出".into());
        }
        let cow = String::from_utf8_lossy(&buf);
        let line = cow.trim_end_matches(['\n', '\r']);
        if line.trim().is_empty() {
            continue;
        }
        let Ok(msg) = serde_json::from_str::<serde_json::Value>(line) else {
            let t: String = line.trim().chars().take(300).collect();
            st.lock().unwrap().raw_tail = t;
            continue;
        };
        let method = msg.get("method").and_then(|m| m.as_str()).unwrap_or("");
        if !method.is_empty() {
            if msg.get("id").is_some() {
                // agent → client 请求：目前只有权限一种要真正处理；其余一律「不支持」，
                // 让 agent 走兜底（我们没有声明 fs/terminal 能力，正常不会有别的请求）。
                if method == "session/request_permission" {
                    let allow = decide_and_wait(&msg["params"], mode, pending_dir, ssh_js, conv_id).await;
                    let out = permission_response(&msg["id"], &msg["params"]["options"], allow);
                    send(stdin, &out).await?;
                } else {
                    send(stdin, &serde_json::json!({
                        "jsonrpc": "2.0", "id": msg["id"], "error": { "code": -32601, "message": "not supported" }
                    })).await?;
                }
            } else if streaming && method == "session/update" {
                if let Some((label, detail, delta)) = parse_update(&msg["params"]["update"]) {
                    if let Some(text) = delta {
                        st.lock().unwrap().text.push_str(&text);
                        let _ = ch.send(TurnEvent::Delta { text });
                    } else {
                        st.lock().unwrap().tools.push(ToolCall { label: label.clone(), detail: detail.clone() });
                        let _ = ch.send(TurnEvent::Tool { label, detail });
                    }
                }
            }
            continue;
        }
        // 响应帧：只认我们等的 id（agent 偶发自导自演的 "skills-reload" 响应帧，忽略）。
        if msg.get("id").and_then(|i| i.as_u64()) == Some(want_id) {
            if let Some(err) = msg.get("error") {
                let m = err.get("message").and_then(|s| s.as_str()).unwrap_or("未知错误");
                return Err(format!("grok: {m}"));
            }
            return Ok(msg.get("result").cloned().unwrap_or(serde_json::Value::Null));
        }
    }
}

/// stderr 收集（诊断用：限流/欠费/升级提示都打在这）。用 tokio::spawn 而非 tauri 运行时，
/// 让 #[ignore] 的真机集成测试能在普通 tokio 里跑通整条 run_turn 路径。
fn collect_stderr(
    reader: tokio::process::ChildStderr,
    sink: Arc<Mutex<String>>,
) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        let mut r = BufReader::new(reader);
        let mut buf = Vec::new();
        loop {
            buf.clear();
            match r.read_until(b'\n', &mut buf).await {
                Ok(0) | Err(_) => break,
                Ok(_) => {
                    let cow = String::from_utf8_lossy(&buf);
                    let mut s = sink.lock().unwrap();
                    s.push_str(cow.trim_end_matches(['\n', '\r']));
                    s.push('\n');
                }
            }
        }
    })
}

async fn send(stdin: &mut ChildStdin, msg: &serde_json::Value) -> Result<(), String> {
    let mut line = serde_json::to_string(msg).map_err(|e| e.to_string())?;
    line.push('\n');
    stdin
        .write_all(line.as_bytes())
        .await
        .map_err(|e| format!("写入 grok stdin 失败: {e}"))?;
    stdin.flush().await.map_err(|e| format!("写入 grok stdin 失败: {e}"))
}

// ---- 流事件解析（纯函数，夹具可测）----

/// session/update → 渲染事件：Some((_,_,Some(text)))＝文本增量；Some((label,detail,None))＝工具卡；
/// None＝忽略（思考流、tool_call_update、历史重放的 user_message_chunk、计划等）。
pub(crate) fn parse_update(u: &serde_json::Value) -> Option<(String, String, Option<String>)> {
    match u.get("sessionUpdate").and_then(|s| s.as_str()) {
        Some("agent_message_chunk") => {
            let t = u["content"].get("text").and_then(|t| t.as_str())?;
            Some((String::new(), String::new(), Some(t.to_string())))
        }
        Some("tool_call") => {
            let (label, detail) = tool_card(u);
            Some((label, detail, None))
        }
        _ => None,
    }
}

/// 工具卡：label 与另两家词汇对齐（命令类＝exec，同 codex；detail＝命令本体）。
fn tool_card(u: &serde_json::Value) -> (String, String) {
    let meta = &u["_meta"]["x.ai/tool"];
    let name = meta
        .get("name")
        .and_then(|s| s.as_str())
        .or_else(|| u.get("title").and_then(|s| s.as_str()))
        .unwrap_or("tool");
    let raw = &u["rawInput"];
    let label = match name {
        "run_terminal_command" => "exec",
        "read_file" => "read",
        "search_replace" | "write_file" | "create_file" => "edit",
        "web_search" => "search",
        "web_fetch" => "fetch",
        "list_dir" => "ls",
        "grep" => "grep",
        other => other,
    };
    let detail = raw
        .get("command")
        .and_then(|s| s.as_str())
        .or_else(|| raw.get("file_path").and_then(|s| s.as_str()))
        .or_else(|| raw.get("path").and_then(|s| s.as_str()))
        .or_else(|| raw.get("url").and_then(|s| s.as_str()))
        .or_else(|| raw.get("query").and_then(|s| s.as_str()))
        .or_else(|| raw.get("description").and_then(|s| s.as_str()))
        .map(|s| s.to_string())
        .unwrap_or_else(|| {
            u.get("title").and_then(|s| s.as_str()).map(|s| s.to_string()).unwrap_or_else(|| raw.to_string())
        });
    (label.to_string(), detail.chars().take(200).collect())
}

/// prompt 响应 _meta → 本轮用量。顶层字段＝最后一次模型调用（inputTokens 为**全量**上下文，
/// 含缓存），与 claude「最后一条 assistant 的 usage＝会话大小」同口径：
/// input 取未缓存部分，ctx = input + cache_read 复原全量。
pub(crate) fn usage_from_meta(meta: &serde_json::Value) -> Option<TurnUsage> {
    let g = |k: &str| meta.get(k).and_then(|v| v.as_u64()).unwrap_or(0);
    let input_full = g("inputTokens");
    let cached = g("cachedReadTokens");
    let output = g("outputTokens");
    if input_full + cached + output == 0 {
        return None;
    }
    Some(TurnUsage {
        input: input_full.saturating_sub(cached),
        output,
        cache_read: cached,
        cache_create: 0,
    })
}

// ---- 权限桥 ----

#[derive(PartialEq, Debug)]
pub(crate) enum Decision {
    Allow,
    Deny,
    /// 弹批准卡（dangerous 控制卡片的红色强调）。
    Card { dangerous: bool },
}

/// 档位 × 工具类别 → 裁决。对齐 claude 档位语义：
/// 只读（read/search/think 或标 read_only）任何档位直接放行（claude 的钩子同样放行只读）；
/// full 全放；plan 非只读一律拒；ask 非只读全弹卡；
/// auto＝acceptEdits 语义：编辑放行、命令安全放行、危险命令/网络抓取/删除弹卡、未知弹卡（安全侧）。
pub(crate) fn decide(mode: PermMode, kind: &str, read_only: bool, cmd: &str, ssh_js: &str) -> Decision {
    if read_only || matches!(kind, "read" | "search" | "think") {
        return Decision::Allow;
    }
    // AI 桥调用自带 Pilot 侧确认（bridge.rs，在 Rust 里）→ 这里放行，否则同一条远程命令弹两张卡。
    // 与 claude 钩子里的 BRIDGE 正则同一判定（permit::is_bridge_cmd）。plan 档仍旧拒：不动手就是不动手。
    if mode != PermMode::Plan && kind == "execute" && permit::is_bridge_cmd(cmd, ssh_js) {
        return Decision::Allow;
    }
    match mode {
        PermMode::Full => Decision::Allow,
        PermMode::Plan => Decision::Deny,
        PermMode::Ask => Decision::Card {
            dangerous: kind == "fetch" || kind == "delete" || permit::is_dangerous(cmd),
        },
        PermMode::Auto => match kind {
            "edit" | "move" => Decision::Allow,
            "execute" => {
                if permit::is_dangerous(cmd) {
                    Decision::Card { dangerous: true }
                } else {
                    Decision::Allow
                }
            }
            "fetch" | "delete" => Decision::Card { dangerous: true },
            _ => Decision::Card { dangerous: false },
        },
    }
}

/// 从 ACP 权限请求里抽（kind, read_only, cmd, desc, arg, tool 展示名, toolCallId）。
pub(crate) fn permission_facts(params: &serde_json::Value) -> (String, bool, String, String, String, String, String) {
    let tc = &params["toolCall"];
    let meta = &tc["_meta"]["x.ai/tool"];
    let kind = tc
        .get("kind")
        .and_then(|s| s.as_str())
        .or_else(|| meta.get("kind").and_then(|s| s.as_str()))
        .unwrap_or("")
        .to_string();
    let read_only = meta.get("read_only").and_then(|b| b.as_bool()).unwrap_or(false);
    let raw = &tc["rawInput"];
    let cmd = raw.get("command").and_then(|s| s.as_str()).unwrap_or("").to_string();
    let desc = raw.get("description").and_then(|s| s.as_str()).unwrap_or("").to_string();
    let arg = raw
        .get("url")
        .and_then(|s| s.as_str())
        .or_else(|| raw.get("file_path").and_then(|s| s.as_str()))
        .or_else(|| raw.get("path").and_then(|s| s.as_str()))
        .or_else(|| tc.get("title").and_then(|s| s.as_str()))
        .unwrap_or("")
        .to_string();
    // 展示名对齐 claude 工具词汇：前端 permitDesc 按 Bash/WebFetch/Write 合成人话标题。
    let tool = match kind.as_str() {
        "execute" => "Bash".to_string(),
        "fetch" => "WebFetch".to_string(),
        "edit" | "move" | "delete" => "Write".to_string(),
        _ => meta
            .get("name")
            .and_then(|s| s.as_str())
            .or_else(|| tc.get("title").and_then(|s| s.as_str()))
            .unwrap_or("tool")
            .to_string(),
    };
    let call_id = tc.get("toolCallId").and_then(|s| s.as_str()).unwrap_or("").to_string();
    (kind, read_only, cmd, desc, arg, tool, call_id)
}

/// 裁决 + 需要时弹卡等待。任何异常（写卡失败/超时/文件损坏）→ false（fail-closed）。
async fn decide_and_wait(params: &serde_json::Value, mode: PermMode, pending_dir: &Path, ssh_js: &Path, conv_id: &str) -> bool {
    let (kind, read_only, cmd, desc, arg, tool, call_id) = permission_facts(params);
    match decide(mode, &kind, read_only, &cmd, &ssh_js.to_string_lossy()) {
        Decision::Allow => true,
        Decision::Deny => false,
        Decision::Card { dangerous } => {
            let id = card_id(&call_id);
            let payload = serde_json::json!({
                "id": id, "conv": conv_id, "tool": tool, "cmd": cmd,
                "input": params["toolCall"]["rawInput"],
                "dangerous": dangerous, "mode": mode.as_str(),
                "ts": std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis() as u64).unwrap_or(0),
                "desc": desc, "arg": arg,
            });
            wait_card(pending_dir, &id, &payload).await
        }
    }
}

/// 批准卡 id：优先复用 toolCallId（已是 uuid 形态），字符不合白名单就换本地 uuid——
/// 必须过 permit::respond 的 safe_id 校验，否则前端点了也应答不进来。
fn card_id(call_id: &str) -> String {
    let ok = !call_id.is_empty()
        && call_id.len() <= 128
        && call_id.chars().all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-');
    if ok {
        call_id.to_string()
    } else {
        format!("g{}", uuid::Uuid::new_v4().simple())
    }
}

/// 写批准卡请求文件并轮询应答；超时/任何 IO 失败 → 拒绝（fail-closed）。
async fn wait_card(pending_dir: &Path, id: &str, payload: &serde_json::Value) -> bool {
    if std::fs::create_dir_all(pending_dir).is_err() {
        return false;
    }
    let req = pending_dir.join(format!("{id}.req.json"));
    let resp = pending_dir.join(format!("{id}.resp.json"));
    let Ok(body) = serde_json::to_vec(payload) else { return false };
    if std::fs::write(&req, body).is_err() {
        return false;
    }
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(CARD_TIMEOUT_SECS);
    loop {
        if let Some(allow) = poll_card_once(&req, &resp) {
            return allow;
        }
        if std::time::Instant::now() > deadline {
            let _ = std::fs::remove_file(&req);
            return false;
        }
        tokio::time::sleep(std::time::Duration::from_millis(300)).await;
    }
}

/// 查看一次应答文件：Some(决定)＝已应答（连带清理两份文件）；None＝还没人点。
/// 应答文件损坏按拒绝处理（fail-closed）。
pub(crate) fn poll_card_once(req: &Path, resp: &Path) -> Option<bool> {
    if !resp.exists() {
        return None;
    }
    let allow = std::fs::read(resp)
        .ok()
        .and_then(|raw| serde_json::from_slice::<serde_json::Value>(&raw).ok())
        .and_then(|v| v.get("decision").and_then(|d| d.as_str()).map(|d| d == "allow"))
        .unwrap_or(false);
    let _ = std::fs::remove_file(resp);
    let _ = std::fs::remove_file(req);
    Some(allow)
}

/// 组装权限应答：按 allow 选 allow_once / reject_once 选项；找不到对应选项时
/// 落到任何 reject 类选项，再不行就 cancelled（等效拒绝——fail-closed）。
pub(crate) fn permission_response(id: &serde_json::Value, options: &serde_json::Value, allow: bool) -> serde_json::Value {
    let pick = |want: &str| {
        options.as_array().and_then(|a| {
            a.iter()
                .find(|o| o.get("kind").and_then(|k| k.as_str()).map(|k| k.contains(want)).unwrap_or(false))
                .and_then(|o| o.get("optionId").and_then(|s| s.as_str()))
                .map(|s| s.to_string())
        })
    };
    let opt = if allow { pick("allow") } else { None }.or_else(|| pick("reject"));
    match opt {
        Some(option_id) => serde_json::json!({
            "jsonrpc": "2.0", "id": id,
            "result": { "outcome": { "outcome": "selected", "optionId": option_id } }
        }),
        None => serde_json::json!({
            "jsonrpc": "2.0", "id": id,
            "result": { "outcome": { "outcome": "cancelled" } }
        }),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// 实测夹具（grok 0.2.101 ACP，2026-07-14 抓包）：tool_call 更新帧。
    fn fx_tool_call() -> serde_json::Value {
        serde_json::from_str(r#"{"sessionUpdate": "tool_call", "toolCallId": "call-10e3413a-bc60-4abc-acf3-ab38f2ba1aa0-0", "title": "run_terminal_command", "rawInput": {"command": "echo acp-probe", "description": "Echo acp-probe to stdout"}, "_meta": {"x.ai/tool": {"version": 1, "name": "run_terminal_command", "kind": "execute", "namespace": "grok_build", "label": "Run Command", "read_only": false}}}"#).unwrap()
    }
    /// 实测夹具：权限请求 params。
    fn fx_permission() -> serde_json::Value {
        serde_json::from_str(r#"{"sessionId": "019f5e32-d00f-7012-8e59-b4ca70d5ab3c", "toolCall": {"toolCallId": "call-10e3413a-bc60-4abc-acf3-ab38f2ba1aa0-0", "kind": "execute", "title": "Execute `echo acp-probe`", "rawInput": {"variant": "Bash", "command": "echo acp-probe", "description": "Echo acp-probe to stdout", "is_background": false}, "_meta": {"x.ai/tool": {"version": 1, "name": "run_terminal_command", "kind": "execute", "namespace": "grok_build", "label": "Run Command", "read_only": false, "input": {"command": "echo acp-probe", "description": "Echo acp-probe to stdout"}}}}, "options": [{"optionId": "allow-once", "name": "Yes, proceed", "kind": "allow_once"}, {"optionId": "reject-once", "name": "No, and tell Grok what to do differently", "kind": "reject_once"}]}"#).unwrap()
    }
    /// 实测夹具：prompt 响应的 _meta（end_turn 那轮）。
    fn fx_meta() -> serde_json::Value {
        serde_json::from_str(r#"{"sessionId": "019f5e32-d00f-7012-8e59-b4ca70d5ab3c", "requestId": "431146b2-3b85-4a6a-a2da-d8fe8c813d96", "promptId": "431146b2-3b85-4a6a-a2da-d8fe8c813d96", "totalTokens": 13156, "modelId": "grok-4.5", "inputTokens": 13079, "outputTokens": 76, "cachedReadTokens": 12928, "reasoningTokens": 44, "usage": {"inputTokens": 26014, "outputTokens": 136, "totalTokens": 26150, "cachedReadTokens": 14208, "reasoningTokens": 69, "modelCalls": 2, "numTurns": 2}}"#).unwrap()
    }

    #[test]
    fn parse_update_text_tool_and_ignored() {
        // 文本增量
        let d = parse_update(&json!({"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"你好"}}));
        assert_eq!(d, Some((String::new(), String::new(), Some("你好".into()))));
        // 工具卡：命令类 label=exec、detail=命令本体（与 codex 卡片同款词汇）
        let t = parse_update(&fx_tool_call()).expect("tool_call 应产卡");
        assert_eq!(t.0, "exec");
        assert_eq!(t.1, "echo acp-probe");
        assert!(t.2.is_none());
        // 思考流 / 更新帧 / 历史重放不渲染
        assert!(parse_update(&json!({"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"x"}})).is_none());
        assert!(parse_update(&json!({"sessionUpdate":"tool_call_update","toolCallId":"c","status":"completed"})).is_none());
        assert!(parse_update(&json!({"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"旧消息"}})).is_none());
    }

    #[test]
    fn usage_meta_maps_to_claude_semantics() {
        // 顶层＝最后一次调用：input 拆成未缓存 + cache_read，ctx 复原为全量 13079。
        let u = usage_from_meta(&fx_meta()).expect("有用量");
        assert_eq!(u.input, 13079 - 12928);
        assert_eq!(u.cache_read, 12928);
        assert_eq!(u.output, 76);
        assert_eq!(u.input + u.cache_read + u.cache_create, 13079); // = apply_usage 的 ctx 口径
        assert!(usage_from_meta(&json!({})).is_none());
    }

    #[test]
    fn decide_matrix_matches_claude_semantics() {
        use Decision::*;
        const JS: &str = "/d/tools/ssh.js";
        // 只读任何档位放行
        for m in [PermMode::Plan, PermMode::Ask, PermMode::Auto, PermMode::Full] {
            assert_eq!(decide(m, "read", true, "", JS), Allow);
            assert_eq!(decide(m, "search", false, "", JS), Allow);
        }
        // full 全放
        assert_eq!(decide(PermMode::Full, "execute", false, "rm -rf /", JS), Allow);
        // plan 非只读一律拒（fail-closed 的只读模式）
        assert_eq!(decide(PermMode::Plan, "execute", false, "ls", JS), Deny);
        assert_eq!(decide(PermMode::Plan, "edit", false, "", JS), Deny);
        // ask 全弹卡；危险命令红色强调
        assert_eq!(decide(PermMode::Ask, "execute", false, "npm install", JS), Card { dangerous: false });
        assert_eq!(decide(PermMode::Ask, "execute", false, "wrangler pages deploy ./dist", JS), Card { dangerous: true });
        // auto＝acceptEdits：编辑放行、安全命令放行、危险命令/抓取/删除弹卡、未知弹卡
        assert_eq!(decide(PermMode::Auto, "edit", false, "", JS), Allow);
        assert_eq!(decide(PermMode::Auto, "execute", false, "npm run build", JS), Allow);
        assert_eq!(decide(PermMode::Auto, "execute", false, "git push origin main", JS), Card { dangerous: true });
        assert_eq!(decide(PermMode::Auto, "fetch", false, "", JS), Card { dangerous: true });
        assert_eq!(decide(PermMode::Auto, "delete", false, "", JS), Card { dangerous: true });
        assert_eq!(decide(PermMode::Auto, "other", false, "", JS), Card { dangerous: false });
    }

    #[test]
    fn permission_facts_from_fixture() {
        let (kind, read_only, cmd, desc, _arg, tool, call_id) = permission_facts(&fx_permission());
        assert_eq!(kind, "execute");
        assert!(!read_only);
        assert_eq!(cmd, "echo acp-probe");
        assert_eq!(desc, "Echo acp-probe to stdout");
        assert_eq!(tool, "Bash"); // 前端 permitDesc 词汇
        assert_eq!(call_id, "call-10e3413a-bc60-4abc-acf3-ab38f2ba1aa0-0");
        assert_eq!(card_id(&call_id), call_id); // uuid 形态直接复用
        assert!(card_id("../evil").starts_with('g')); // 不合法字符换本地 id
    }

    #[test]
    fn permission_response_picks_options_fail_closed() {
        let opts = fx_permission()["options"].clone();
        let ok = permission_response(&json!(0), &opts, true);
        assert_eq!(ok["result"]["outcome"]["optionId"], "allow-once");
        let no = permission_response(&json!(0), &opts, false);
        assert_eq!(no["result"]["outcome"]["optionId"], "reject-once");
        // allow 选项缺失 → 落到 reject（宁拒不放）
        let only_reject = json!([{ "optionId": "r", "kind": "reject_always" }]);
        let fallback = permission_response(&json!(1), &only_reject, true);
        assert_eq!(fallback["result"]["outcome"]["optionId"], "r");
        // 没有任何选项 → cancelled（等效拒绝）
        let none = permission_response(&json!(2), &json!([]), true);
        assert_eq!(none["result"]["outcome"]["outcome"], "cancelled");
    }

    /// 真机端到端（消耗真实请求，手动跑）：
    /// `cargo test --lib -- --ignored grok_acp_live_end_to_end --nocapture`
    /// 覆盖：spawn→initialize→session/new(+rules)→prompt→工具事件→usage→session/load 续跑。
    #[tokio::test]
    #[ignore]
    async fn grok_acp_live_end_to_end() {
        let dir = std::env::temp_dir().join(format!("acp-live-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let work_dir = dir.to_string_lossy().into_owned();
        let conn: Connection = serde_json::from_value(json!({
            "id": "t", "name": "t", "api_base": "", "skill_dir": work_dir,
            "key_prefix": "", "key_kind": "gcms_", "created_at": "",
        }))
        .unwrap();
        let ch = || Channel::<TurnEvent>::new(|_| Ok(()));
        let pend = dir.join("pending");

        // 轮 1：全新会话 + 触发一次命令执行（full 档 → --always-approve，不弹卡）。
        let r1 = run_turn(
            RunRegistry::default(), conn.clone(), work_dir.clone(), String::new(), PermMode::Full,
            String::new(), pend.clone(), std::path::PathBuf::from("/d/tools/ssh.js"), None, String::new(), true,
            Some("测试规则：所有回答的最后一行必须是单独的一个字「哞」。".into()),
            "运行 shell 命令 echo pilot-live 并告诉我输出".into(), "live-1".into(), ch(), String::new(), None,
        )
        .await;
        eprintln!("turn1 ok={} err={} session={} tools={:?} text={}", r1.ok, r1.error, r1.session_ref, r1.tools.iter().map(|t| format!("{}:{}", t.label, t.detail)).collect::<Vec<_>>(), r1.text);
        assert!(r1.ok, "轮1失败: {}", r1.error);
        assert!(!r1.session_ref.is_empty(), "应拿到 sessionId");
        assert!(r1.tools.iter().any(|t| t.label == "exec" && t.detail.contains("echo pilot-live")), "应有工具卡: {:?}", r1.tools.iter().map(|t| &t.detail).collect::<Vec<_>>());
        assert!(r1.text.contains("哞"), "rules 追加语义应生效: {}", r1.text);
        let u = r1.usage.expect("应有用量");
        assert!(u.input + u.cache_read + u.output > 0);

        // 轮 2：session/load 续跑（新进程），验证多轮记忆。
        let r2 = run_turn(
            RunRegistry::default(), conn, work_dir, String::new(), PermMode::Full,
            String::new(), pend, std::path::PathBuf::from("/d/tools/ssh.js"), None, r1.session_ref.clone(), false, None,
            "我刚才让你运行的命令原文是什么？只回答命令本身".into(), "live-2".into(), ch(), String::new(), None,
        )
        .await;
        eprintln!("turn2 ok={} err={} text={}", r2.ok, r2.error, r2.text);
        assert!(r2.ok, "轮2失败: {}", r2.error);
        assert!(r2.text.contains("echo pilot-live"), "resume 应记得首轮命令: {}", r2.text);
        assert_eq!(r2.session_ref, r1.session_ref, "续跑不换 session");
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn poll_card_roundtrip_and_fail_closed() {
        let dir = std::env::temp_dir().join(format!("acp-card-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let req = dir.join("t1.req.json");
        let resp = dir.join("t1.resp.json");
        std::fs::write(&req, b"{}").unwrap();
        // 没应答 → None（继续等）
        assert_eq!(poll_card_once(&req, &resp), None);
        // 批准 → true，两份文件都被清掉
        std::fs::write(&resp, br#"{"decision":"allow"}"#).unwrap();
        assert_eq!(poll_card_once(&req, &resp), Some(true));
        assert!(!req.exists() && !resp.exists());
        // 应答文件损坏 → 拒绝（fail-closed）
        std::fs::write(&req, b"{}").unwrap();
        std::fs::write(&resp, b"not-json").unwrap();
        assert_eq!(poll_card_once(&req, &resp), Some(false));
        std::fs::remove_dir_all(&dir).ok();
    }
}
