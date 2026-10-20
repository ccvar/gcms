//! Native API agent. Protocol transcripts are kept locally, never mixed with CLI sessions.
//! Only a confirmed Claude output-limit response can be regenerated once. Never
//! replay tools or automatically retry network failures / uncertain side effects.
use crate::agent::{RunRegistry, TurnEvent, TurnResult, TurnUsage};
use crate::api_profiles::{self, ApiProfile};
use crate::convo::ToolCall;
use crate::pack::Connection;
use crate::permit::{self, PermMode};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::{Mutex, OnceLock};
use std::time::Duration;
use tauri::ipc::Channel;

#[path = "api_agent_recovery.rs"]
mod recovery;
#[path = "api_agent_tools.rs"]
mod tool_runtime;

const MAX_HISTORY_BYTES: usize = 2 * 1024 * 1024;
const MAX_STREAM_BYTES: usize = 8 * 1024 * 1024;
const MAX_ROUNDS: usize = 32;
static ACTIVE: OnceLock<Mutex<HashSet<String>>> = OnceLock::new();

#[derive(Serialize, Deserialize)]
struct History {
    scope: String,
    protocol: String,
    system: String,
    /// Fingerprint of the effective system + tool schema used for Claude's
    /// retained reasoning. Legacy histories did not record this prefix.
    #[serde(default)]
    thinking_prefix: String,
    items: Vec<Value>,
    /// Set before side effects; cleared atomically with all recorded tool results.
    interrupted: bool,
}

impl History {
    fn prepare_thinking_prefix(&mut self, system: &str, definitions: &[Value]) -> usize {
        if self.protocol != "anthropic_messages" {
            return 0;
        }
        let prefix = format!(
            "{:x}",
            Sha256::digest(json!([system, definitions]).to_string().as_bytes())
        );
        if self.thinking_prefix == prefix {
            return 0;
        }
        // A changed prefix invalidates every later reasoning block. Old
        // versions never saved the effective prompt, so migrate them once too.
        // Preserve all user text, tool calls and results; never replay a tool.
        let mut removed = 0;
        self.items.retain_mut(|item| {
            if item["role"] != "assistant" {
                return true;
            }
            let Some(blocks) = item["content"].as_array_mut() else {
                return true;
            };
            let before = blocks.len();
            blocks.retain(|block| {
                !matches!(
                    block["type"].as_str(),
                    Some("thinking" | "redacted_thinking")
                )
            });
            removed += before - blocks.len();
            // Do not send an empty assistant turn after removing thinking-only
            // output. Later reasoning was removed as well, avoiding a gap.
            before == blocks.len() || !blocks.is_empty()
        });
        self.thinking_prefix = prefix;
        removed
    }
}

struct SessionGuard(String);
impl SessionGuard {
    fn lock(key: String) -> Result<Self, String> {
        if !ACTIVE
            .get_or_init(Default::default)
            .lock()
            .unwrap()
            .insert(key.clone())
        {
            return Err("这个 API 会话正在执行，请等待当前回合结束".into());
        }
        Ok(Self(key))
    }
}
impl Drop for SessionGuard {
    fn drop(&mut self) {
        ACTIVE.get().unwrap().lock().unwrap().remove(&self.0);
    }
}

fn scope(
    conn: &Connection,
    work_dir: &Path,
    profile: &ApiProfile,
    model: &str,
    conversation_id: &str,
) -> String {
    let material = json!([
        conn.id,
        conn.kind,
        work_dir,
        profile.id,
        profile.protocol,
        profile.base_url,
        model,
        conversation_id
    ]);
    format!("{:x}", Sha256::digest(material.to_string().as_bytes()))
}

fn session_id(session_ref: &str, _is_first: bool) -> Result<String, String> {
    // The conversation store seeds this before starting a first API turn. Keep
    // that ID even on the first turn so process crashes cannot lose the link to
    // a transcript which has already started executing side effects.
    if session_ref.is_empty() {
        return Ok(uuid::Uuid::new_v4().to_string());
    }
    let id = session_ref
        .strip_prefix("api:")
        .ok_or("API 会话标识不匹配，请新建会话")?;
    uuid::Uuid::parse_str(id).map_err(|_| "API 会话标识无效")?;
    Ok(id.to_string())
}

fn ensure_new_history(path: &Path) -> Result<(), String> {
    if path
        .try_exists()
        .map_err(|e| format!("检查 API 会话历史失败：{e}"))?
    {
        return Err("此 API 会话已开始过，不能以首轮方式覆盖历史。请核对执行记录后新建会话".into());
    }
    Ok(())
}

fn save_history(path: &Path, history: &History) -> Result<(), String> {
    let body = serde_json::to_vec(history).map_err(|e| e.to_string())?;
    if body.len() > MAX_HISTORY_BYTES {
        return Err("API 会话已达到本地上下文上限，请新建会话；未自动截断历史".into());
    }
    std::fs::create_dir_all(path.parent().ok_or("会话目录无效")?).map_err(|e| e.to_string())?;
    let temp = path.with_extension(format!("{}.tmp", uuid::Uuid::new_v4()));
    let mut opts = std::fs::OpenOptions::new();
    opts.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    use std::io::Write;
    let mut file = opts
        .open(&temp)
        .map_err(|e| format!("保存 API 会话失败：{e}"))?;
    file.write_all(&body)
        .and_then(|_| file.sync_all())
        .map_err(|e| e.to_string())?;
    std::fs::rename(&temp, path).map_err(|e| format!("替换 API 会话失败：{e}"))
}

#[allow(clippy::too_many_arguments)]
pub(crate) async fn run_turn(
    registry: RunRegistry,
    conn: Connection,
    work_dir: String,
    profile_id: String,
    model: String,
    effort: String,
    mode: PermMode,
    selected_skills: Vec<String>,
    data_dir: PathBuf,
    lease: Option<&crate::bridge::Lease>,
    session_ref: String,
    is_first: bool,
    system: Option<String>,
    message: String,
    turn_id: String,
    channel: Channel<TurnEvent>,
    site_key: String,
) -> TurnResult {
    let mut result = TurnResult {
        ok: false,
        text: String::new(),
        tools: vec![],
        error: String::new(),
        session_ref: session_ref.clone(),
        proposal: None,
        usage: None,
        limit_reset: None,
    };
    let (_canceled, mut stop) = registry.register(&turn_id);
    let operation = execute(
        &conn,
        &work_dir,
        &profile_id,
        &model,
        &effort,
        mode,
        &selected_skills,
        &data_dir,
        lease,
        &session_ref,
        is_first,
        system,
        &message,
        &turn_id,
        &channel,
        &site_key,
        &mut result,
    );
    let outcome = tokio::select! { out = operation => out, _ = &mut stop => Err("已停止；已执行的操作不会自动重试".into()) };
    registry.unregister(&turn_id);
    permit::sweep_conv(&data_dir.join("permit/pending"), &turn_id);
    result.ok = outcome.is_ok();
    result.error = outcome.err().unwrap_or_default();
    if !result.ok {
        result.limit_reset = crate::agent::detect_usage_limit(&result.error);
    }
    let (text, proposal) =
        crate::agent::extract_proposal(&crate::agent::redact_gcms_unlock_challenges(&result.text));
    result.text = text;
    result.proposal = proposal;
    let _ = channel.send(TurnEvent::Done {
        ok: result.ok,
        error: result.error.clone(),
    });
    result
}

#[allow(clippy::too_many_arguments)]
async fn execute(
    conn: &Connection,
    work_dir: &str,
    profile_id: &str,
    model: &str,
    effort: &str,
    mode: PermMode,
    selected_skills: &[String],
    data_dir: &Path,
    lease: Option<&crate::bridge::Lease>,
    session_ref: &str,
    is_first: bool,
    system: Option<String>,
    message: &str,
    turn_id: &str,
    channel: &Channel<TurnEvent>,
    site_key: &str,
    result: &mut TurnResult,
) -> Result<(), String> {
    let loaded = api_profiles::load_enabled(data_dir, profile_id)?;
    let profile = &loaded.profile;
    let model = if model.trim().is_empty() {
        &profile.default_model
    } else {
        model
    };
    if model.trim().is_empty() {
        return Err("请先为 API 服务选择模型".into());
    }
    if !profile.models.iter().any(|m| m == model) {
        return Err("该模型已不在此 API 服务中，请重新选择模型".into());
    }
    // Reject stale/unsupported explicit selections before creating or appending
    // a transcript. Never display an effort while silently using a default.
    crate::api_reasoning::validate(&profile.protocol, model, effort)?;
    let work = std::fs::canonicalize(work_dir).map_err(|e| format!("工作目录不可用：{e}"))?;
    let id = session_id(session_ref, is_first)?;
    let scope = scope(conn, &work, profile, model, turn_id);
    let path = data_dir
        .join("api-sessions")
        .join(&scope)
        .join(format!("{id}.json"));
    let _guard = SessionGuard::lock(path.to_string_lossy().into_owned())?;
    let tools = tool_runtime::Tools::new(
        conn,
        &work,
        data_dir,
        selected_skills,
        mode,
        turn_id,
        lease,
        site_key,
    )?;
    let mut history = if !is_first && !session_ref.is_empty() {
        let raw = std::fs::read(&path).map_err(|_| "无法恢复此 API 会话（工作区、接口或模型可能已变化），请新建会话；不会借用其他工作区历史".to_string())?;
        if raw.len() > MAX_HISTORY_BYTES {
            return Err("API 会话历史超过安全上限".into());
        }
        let mut value: History =
            serde_json::from_slice(&raw).map_err(|_| "API 会话历史损坏，未自动覆盖".to_string())?;
        if value.scope != scope || value.protocol != profile.protocol {
            return Err("API 会话归属不匹配".into());
        }
        if value.interrupted {
            // Older versions kept this flag until final assistant text, even
            // when every tool result was already durable. Recover only after
            // validating the ENTIRE transcript, never from a flag alone.
            if !recovery::can_resume_legacy(&value.protocol, &value.items) {
                return Err(
                    "上一回合有工具执行结果未确认。为避免重复发布或重复写入，请核对执行记录后新建会话"
                        .into(),
                );
            }
            value.interrupted = false;
            save_history(&path, &value)?;
        }
        value
    } else {
        ensure_new_history(&path)?;
        History {
            scope,
            protocol: profile.protocol.clone(),
            system: system.unwrap_or_default(),
            thinking_prefix: String::new(),
            items: vec![],
            interrupted: false,
        }
    };
    history
        .items
        .push(json!({"role":"user", "content":message}));
    save_history(&path, &history)?;
    result.session_ref = format!("api:{id}");
    execute_model_loop(
        profile,
        model,
        effort,
        &loaded.api_key,
        &tools,
        &mut history,
        &path,
        channel,
        site_key,
        result,
    )
    .await
}

#[allow(clippy::too_many_arguments)]
async fn execute_model_loop(
    profile: &ApiProfile,
    model: &str,
    effort: &str,
    api_key: &str,
    tools: &tool_runtime::Tools<'_>,
    history: &mut History,
    path: &Path,
    channel: &Channel<TurnEvent>,
    site_key: &str,
    result: &mut TurnResult,
) -> Result<(), String> {
    let instructions = format!("{}\n\n{}", history.system, tools.instructions());
    let definitions = tools.definitions();
    let removed_thinking = history.prepare_thinking_prefix(&instructions, &definitions);
    // Commit the migration before any request. A failed request or restart must
    // never resurrect the old, invalid blocks or change the already-sent prefix.
    save_history(path, history)?;
    if removed_thinking > 0 {
        let _ = channel.send(TurnEvent::Activity {
            activity_id: "api-thinking-migration".into(),
            label: "已兼容旧会话的思考历史".into(),
            detail: "运行说明已更新，已移除旧思考签名；正文、工具调用和执行结果完整保留".into(),
            error: String::new(),
            status: "completed".into(),
            kind: "reasoning".into(),
            phase: "继续原会话".into(),
            current: None,
            total: None,
        });
    }
    let client = api_profiles::http_client()?;
    let mut recovered = false;
    let mut retry_pending = false;
    for _ in 0..MAX_ROUNDS {
        let body = request_body(
            profile,
            model,
            effort,
            &instructions,
            &history.items,
            &definitions,
        )?;
        if body.to_string().len() > MAX_HISTORY_BYTES {
            return Err("上下文已达到安全上限，请新建会话；未自动丢弃历史".into());
        }
        let text_checkpoint = result.text.len();
        let reply = stream_request(&client, profile, api_key, body, |delta| {
            let safe = tool_runtime::redact(delta, &[api_key, site_key]);
            result.text.push_str(&safe);
            let _ = channel.send(TurnEvent::Delta { text: safe });
        })
        .await
        .map_err(|e| tool_runtime::redact(&e, &[api_key, site_key]))?;
        add_usage(&mut result.usage, &reply.usage);
        if reply.output_limited {
            // A confirmed terminal max_tokens response cannot run ANY tools,
            // even complete-looking calls preceding a clipped JSON block.
            // Keep its billed usage, but neither its text nor arguments enter
            // model history. The next request includes all prior tool results.
            result.text.truncate(text_checkpoint);
            let _ = channel.send(TurnEvent::TextReset {
                text: result.text.clone(),
            });
            if recovered {
                let error = format!("Claude 单次输出达到 {} token 上限（max_tokens），自动恢复一次后仍未完成。已保存此前完整工具结果；可在设置 → API 服务 → 高级设置提高单次最大输出 token，或要求分文件、分步骤生成后继续，无需重建会话。", profile.max_output_tokens);
                let _ = channel.send(TurnEvent::Activity {
                    activity_id: "api-output-recovery".into(),
                    label: "输出恢复未完成".into(),
                    detail: "已保留此前完整工具结果".into(),
                    error: error.clone(),
                    status: "failed".into(),
                    kind: "reasoning".into(),
                    phase: String::new(),
                    current: None,
                    total: None,
                });
                return Err(error);
            }
            recovered = true;
            retry_pending = true;
            // Append the recovery hint instead of editing system: Claude binds
            // signatures to the entire preceding prefix, including system.
            history.items.push(json!({"role":"user", "content":"Pilot 输出恢复提示：本轮有回复因输出上限被截断，该回复中的工具调用均未执行、未写入历史。请根据现有完整工具结果继续下一步，不要重做已经完成的操作。保持回复简短，一次只生成一个较小文件或一个步骤，避免在单次工具参数中放入整个项目。"}));
            save_history(path, history)?;
            let _ = channel.send(TurnEvent::Activity {
                activity_id: "api-output-recovery".into(),
                label: "输出达到上限，正在恢复".into(),
                detail: format!(
                    "按原上限 {} token 重新生成一次；不会重放此前工具操作",
                    profile.max_output_tokens
                ),
                error: String::new(),
                status: "running".into(),
                kind: "reasoning".into(),
                phase: "缩短输出，分步骤继续".into(),
                current: None,
                total: None,
            });
            continue;
        }
        if retry_pending {
            retry_pending = false;
            let _ = channel.send(TurnEvent::Activity {
                activity_id: "api-output-recovery".into(),
                label: "输出恢复完成".into(),
                detail: "继续处理当前回复；此前工具结果已保留".into(),
                error: String::new(),
                status: "completed".into(),
                kind: "reasoning".into(),
                phase: "输出恢复完成".into(),
                current: None,
                total: None,
            });
        }
        let calls = reply.calls()?;
        if calls
            .iter()
            .any(|call| recorded_call_id(&profile.protocol, &history.items, &call.id))
        {
            return Err(
                "模型返回了已记录的工具调用 ID，已阻止重复执行；请核对执行记录后继续".into(),
            );
        }
        history.items.extend(reply.items);
        if calls.is_empty() {
            history.interrupted = false;
            save_history(&path, &history)?;
            return Ok(());
        }
        history.interrupted = true;
        save_history(&path, &history)?;
        let mut outputs = Vec::new();
        for call in calls {
            let detail = tool_runtime::redact(&call.arguments.to_string(), &[api_key, site_key]);
            result.tools.push(ToolCall {
                label: call.name.clone(),
                detail: detail.clone(),
            });
            let _ = channel.send(TurnEvent::Tool {
                label: call.name.clone(),
                detail: detail.clone(),
            });
            activity(channel, &call.id, &call.name, &detail, "running", "");
            let output = tools.execute(&call.name, &call.arguments).await;
            let failed = output
                .as_ref()
                .map(|v| v["success"] == false)
                .unwrap_or(true);
            let output = match output {
                Ok(value) => value,
                Err(error) => json!({"error":error}),
            };
            if let Some(unlock) = crate::agent::gcms_unlock_from_tool_payload(&output) {
                let _ = channel.send(TurnEvent::GcmsUnlockRequired {
                    operation: unlock.operation,
                    unlock_challenge: unlock.unlock_challenge,
                    admin_path: unlock.admin_path,
                });
            }
            let safe = crate::agent::redact_gcms_unlock_challenges(&tool_runtime::redact(
                &output.to_string(),
                &[api_key, site_key],
            ));
            activity(
                channel,
                &call.id,
                &call.name,
                &detail,
                if failed { "failed" } else { "completed" },
                if failed { &safe } else { "" },
            );
            outputs.push((call.id, safe, failed));
        }
        append_tool_outputs(&profile.protocol, &mut history.items, outputs)?;
        // Clear only alongside all results in the same atomic write. A crash
        // mid-batch or failed save leaves the on-disk flag true, so unfinished
        // side effects remain blocked rather than being replayed.
        history.interrupted = false;
        save_history(&path, &history)?;
    }
    Err("本轮已达到 32 次模型调用上限，已停止；请核对执行记录，避免重复操作".into())
}

fn activity(
    channel: &Channel<TurnEvent>,
    id: &str,
    name: &str,
    detail: &str,
    status: &str,
    error: &str,
) {
    let _ = channel.send(TurnEvent::Activity {
        activity_id: format!("api-{id}"),
        label: name.into(),
        detail: detail.into(),
        error: error.into(),
        status: status.into(),
        kind: "tool".into(),
        phase: String::new(),
        current: None,
        total: None,
    });
}

fn request_body(
    profile: &ApiProfile,
    model: &str,
    effort: &str,
    system: &str,
    items: &[Value],
    definitions: &[Value],
) -> Result<Value, String> {
    let tools: Vec<Value> = definitions.iter().map(|d| match profile.protocol.as_str() {
        "openai_responses" => json!({"type":"function","name":d["name"],"description":d["description"],"parameters":d["parameters"],"strict":false}),
        "anthropic_messages" => json!({"name":d["name"],"description":d["description"],"input_schema":d["parameters"]}),
        _ => json!({"type":"function","function":d}),
    }).collect();
    let mut body = match profile.protocol.as_str() {
        "openai_responses" => {
            json!({"model":model,"instructions":system,"input":items,"tools":tools,
            "stream":true,"store":false,"include":["reasoning.encrypted_content"],"max_output_tokens":profile.max_output_tokens})
        }
        "anthropic_messages" => {
            json!({"model":model,"system":system,"messages":items,"tools":tools,
            "stream":true,"max_tokens":profile.max_output_tokens})
        }
        "openai_chat" => {
            let mut messages = vec![json!({"role":"system","content":system})];
            messages.extend_from_slice(items);
            let mut body = json!({"model":model,"messages":messages,"tools":tools,"stream":true,
                "stream_options":{"include_usage":true}});
            // Modern OpenAI reasoning models reject the legacy max_tokens key.
            // Keep the configured cap; unrelated compatible endpoints retain
            // the existing parameter rather than inheriting an unknown schema.
            let limit_key = if crate::api_reasoning::uses_chat_completion_tokens(model) {
                "max_completion_tokens"
            } else {
                "max_tokens"
            };
            body[limit_key] = json!(profile.max_output_tokens);
            body
        }
        _ => return Err("此 API 协议不受支持，未自动改用其他协议".into()),
    };
    crate::api_reasoning::apply(&profile.protocol, model, effort, &mut body)?;
    Ok(body)
}

fn append_tool_outputs(
    protocol: &str,
    items: &mut Vec<Value>,
    outputs: Vec<(String, String, bool)>,
) -> Result<(), String> {
    match protocol {
        "openai_responses" => items.extend(outputs.into_iter().map(|(id,output,_)| json!({"type":"function_call_output","call_id":id,"output":output}))),
        "anthropic_messages" => items.push(json!({"role":"user","content":outputs.into_iter().map(|(id,output,error)| json!({"type":"tool_result","tool_use_id":id,"content":output,"is_error":error})).collect::<Vec<_>>()})),
        "openai_chat" => items.extend(outputs.into_iter().map(|(id,output,_)| json!({"role":"tool","tool_call_id":id,"content":output}))),
        _ => return Err("不支持的 API 协议".into()),
    }
    Ok(())
}

fn recorded_call_id(protocol: &str, items: &[Value], id: &str) -> bool {
    items.iter().any(|item| match protocol {
        "anthropic_messages" if item["role"] == "assistant" => {
            item["content"].as_array().is_some_and(|blocks| {
                blocks
                    .iter()
                    .any(|block| block["type"] == "tool_use" && block["id"] == id)
            })
        }
        "openai_responses" => item["type"] == "function_call" && item["call_id"] == id,
        "openai_chat" if item["role"] == "assistant" => item["tool_calls"]
            .as_array()
            .is_some_and(|calls| calls.iter().any(|call| call["id"] == id)),
        _ => false,
    })
}

struct Call {
    id: String,
    name: String,
    arguments: Value,
}
struct Reply {
    protocol: String,
    items: Vec<Value>,
    usage: TurnUsage,
    // A completed stream can still have hit its output budget. Such a response
    // carries usage only: none of its text, thinking or tools may be replayed.
    output_limited: bool,
}
impl Reply {
    fn calls(&self) -> Result<Vec<Call>, String> {
        if self.output_limited {
            return Err("模型输出达到上限，本次响应中的工具均未执行".into());
        }
        let mut calls = Vec::new();
        for item in &self.items {
            let raw: Vec<&Value> = match self.protocol.as_str() {
                "openai_responses" if item["type"] == "function_call" => vec![item],
                "anthropic_messages" => item["content"]
                    .as_array()
                    .map(|x| x.iter().filter(|x| x["type"] == "tool_use").collect())
                    .unwrap_or_default(),
                "openai_chat" => item["tool_calls"]
                    .as_array()
                    .map(|x| x.iter().collect())
                    .unwrap_or_default(),
                _ => vec![],
            };
            for tool in raw {
                let (id, name, args) = match self.protocol.as_str() {
                    "openai_responses" => (&tool["call_id"], &tool["name"], &tool["arguments"]),
                    "anthropic_messages" => (&tool["id"], &tool["name"], &tool["input"]),
                    _ => (
                        &tool["id"],
                        &tool["function"]["name"],
                        &tool["function"]["arguments"],
                    ),
                };
                let arguments = if let Some(text) = args.as_str() {
                    serde_json::from_str(text).map_err(|_| "模型返回的工具参数不是有效 JSON")?
                } else {
                    args.clone()
                };
                if !arguments.is_object() {
                    return Err("工具参数必须是对象".into());
                }
                calls.push(Call {
                    id: id
                        .as_str()
                        .filter(|s| !s.is_empty())
                        .ok_or("工具缺少调用 ID")?
                        .into(),
                    name: name.as_str().ok_or("工具缺少名称")?.into(),
                    arguments,
                });
            }
        }
        if calls.len() > 32 {
            return Err("模型一次请求过多工具，未执行".into());
        }
        let mut ids = HashSet::new();
        if calls.iter().any(|x| !ids.insert(&x.id)) {
            return Err("模型返回重复的工具调用 ID，未执行".into());
        }
        Ok(calls)
    }
}

fn usage(v: &Value) -> TurnUsage {
    let cached = v["input_tokens_details"]["cached_tokens"]
        .as_u64()
        .or_else(|| v["prompt_tokens_details"]["cached_tokens"].as_u64())
        .unwrap_or(0);
    TurnUsage {
        input: v["input_tokens"]
            .as_u64()
            .or_else(|| v["prompt_tokens"].as_u64())
            .unwrap_or(0)
            .saturating_sub(cached),
        output: v["output_tokens"]
            .as_u64()
            .or_else(|| v["completion_tokens"].as_u64())
            .unwrap_or(0),
        cache_read: v["cache_read_input_tokens"].as_u64().unwrap_or(cached),
        cache_create: v["cache_creation_input_tokens"].as_u64().unwrap_or(0),
    }
}
fn add_usage(total: &mut Option<TurnUsage>, value: &TurnUsage) {
    let total = total.get_or_insert_with(TurnUsage::default);
    total.input += value.input;
    total.output += value.output;
    total.cache_read += value.cache_read;
    total.cache_create += value.cache_create;
}

/// Byte-oriented framing preserves split UTF-8 codepoints and CRLF across HTTP chunks.
#[derive(Default)]
struct Sse {
    bytes: Vec<u8>,
}
impl Sse {
    fn push(&mut self, bytes: &[u8]) -> Result<Vec<String>, String> {
        self.bytes.extend_from_slice(bytes);
        if self.bytes.len() > MAX_STREAM_BYTES {
            return Err("API 流事件超过安全上限".into());
        }
        let mut out = Vec::new();
        loop {
            let boundary = self
                .bytes
                .windows(2)
                .position(|x| x == b"\n\n")
                .map(|p| (p, 2))
                .into_iter()
                .chain(
                    self.bytes
                        .windows(4)
                        .position(|x| x == b"\r\n\r\n")
                        .map(|p| (p, 4)),
                )
                .min_by_key(|x| x.0);
            let Some((end, skip)) = boundary else { break };
            let frame = String::from_utf8(self.bytes.drain(..end + skip).collect())
                .map_err(|_| "API 返回了无效 UTF-8")?;
            let data = frame
                .lines()
                .filter_map(|l| {
                    l.strip_prefix("data:")
                        .map(|s| s.strip_prefix(' ').unwrap_or(s))
                })
                .collect::<Vec<_>>()
                .join("\n");
            if !data.is_empty() {
                out.push(data);
            }
        }
        Ok(out)
    }
}

#[derive(Default)]
struct Accumulator {
    items: BTreeMap<usize, Value>,
    partial: BTreeMap<usize, String>,
    closed_blocks: HashSet<usize>,
    text: String,
    usage: TurnUsage,
    complete: bool,
    stop: String,
}
impl Accumulator {
    fn event(&mut self, protocol: &str, v: &Value) -> Result<String, String> {
        if v["type"] == "error" || v.get("error").is_some_and(|e| !e.is_null()) {
            return Err(api_error(v));
        }
        let mut delta = String::new();
        let index = v["index"]
            .as_u64()
            .or_else(|| v["output_index"].as_u64())
            .unwrap_or(0) as usize;
        match protocol {
            "openai_responses" => match v["type"].as_str().unwrap_or("") {
                "response.output_text.delta" => delta = v["delta"].as_str().unwrap_or("").into(),
                "response.output_item.done" => {
                    self.items.insert(index, v["item"].clone());
                }
                "response.completed" => {
                    self.complete = true;
                    self.usage = usage(&v["response"]["usage"]);
                    if let Some(items) = v["response"]["output"].as_array() {
                        self.items = items.iter().cloned().enumerate().collect();
                    }
                }
                "response.failed" | "response.incomplete" | "response.cancelled" => {
                    return Err(api_error(&v["response"]))
                }
                _ => {}
            },
            "anthropic_messages" => match v["type"].as_str().unwrap_or("") {
                "message_start" => self.usage = usage(&v["message"]["usage"]),
                "content_block_start" => {
                    if self.items.contains_key(&index) {
                        return Err("API 内容块序号重复，未执行工具".into());
                    }
                    self.items.insert(index, v["content_block"].clone());
                }
                "content_block_delta" => {
                    if self.closed_blocks.contains(&index) {
                        return Err("API 内容块已结束，未执行工具".into());
                    }
                    let item = self.items.get_mut(&index).ok_or("API 工具流顺序异常")?;
                    let d = &v["delta"];
                    match d["type"].as_str().unwrap_or("") {
                        "text_delta" => {
                            delta = d["text"].as_str().unwrap_or("").into();
                            append(item, "text", &delta);
                        }
                        "input_json_delta" => {
                            if item["type"] != "tool_use" {
                                return Err("API 工具参数出现在非工具内容块中".into());
                            }
                            self.partial.entry(index).or_default().push_str(
                                d["partial_json"].as_str().ok_or("API 工具参数片段无效")?,
                            );
                        }
                        "thinking_delta" => {
                            append(item, "thinking", d["thinking"].as_str().unwrap_or(""))
                        }
                        "signature_delta" => {
                            append(item, "signature", d["signature"].as_str().unwrap_or(""))
                        }
                        _ => {}
                    }
                }
                "content_block_stop" => {
                    if !self.items.contains_key(&index) || !self.closed_blocks.insert(index) {
                        return Err("API 内容块结束顺序异常".into());
                    }
                    // A capped tool can end with incomplete JSON. Wait until
                    // message_stop tells us whether to validate or discard it.
                }
                "message_delta" => {
                    self.stop = v["delta"]["stop_reason"].as_str().unwrap_or("").into();
                    if let Some(output) = v["usage"]["output_tokens"].as_u64() {
                        self.usage.output = output;
                    }
                }
                "message_stop" => {
                    if !matches!(
                        self.stop.as_str(),
                        "end_turn" | "tool_use" | "stop_sequence" | "max_tokens"
                    ) {
                        return Err(format!("Claude 回合未正常完成：{}", self.stop));
                    }
                    self.complete = true;
                }
                _ => {}
            },
            "openai_chat" => {
                if v["usage"].is_object() {
                    self.usage = usage(&v["usage"]);
                }
                let choice = &v["choices"][0];
                let d = &choice["delta"];
                delta = d["content"].as_str().unwrap_or("").into();
                if let Some(calls) = d["tool_calls"].as_array() {
                    for call in calls {
                        let idx = call["index"].as_u64().ok_or("API 工具调用缺少序号")? as usize;
                        let target=self.items.entry(idx).or_insert_with(||json!({"id":"","type":"function","function":{"name":"","arguments":""}}));
                        if let Some(id) = call["id"].as_str() {
                            append(target, "id", id);
                        }
                        if let Some(name) = call["function"]["name"].as_str() {
                            append(&mut target["function"], "name", name);
                        }
                        if let Some(args) = call["function"]["arguments"].as_str() {
                            append(&mut target["function"], "arguments", args);
                        }
                    }
                }
                if let Some(reason) = choice["finish_reason"].as_str() {
                    if !matches!(reason, "stop" | "tool_calls") {
                        return Err(format!("API 回合未正常完成：{reason}"));
                    }
                    self.complete = true;
                    self.stop = reason.into();
                }
            }
            _ => return Err("不支持的流协议".into()),
        }
        self.text.push_str(&delta);
        Ok(delta)
    }
    fn finish(mut self, protocol: &str) -> Result<Reply, String> {
        if !self.complete {
            return Err("API 连接在回合完成前断开，未标记成功，也不会自动重试".into());
        }
        if protocol == "anthropic_messages"
            && self.items.values().any(|block| {
                !matches!(
                    block["type"].as_str(),
                    Some("text" | "thinking" | "redacted_thinking" | "tool_use")
                )
            })
        {
            // No server tools are registered by this executor. Never regenerate
            // a response containing unknown/provider-side execution blocks.
            return Err(
                "Claude 返回了不支持的内容或服务端工具，无法安全恢复，未执行本地工具".into(),
            );
        }
        if protocol == "anthropic_messages" && self.stop == "max_tokens" {
            // Discard the entire capped response, including fully formed tool
            // blocks before the truncated one. The caller may retry only from
            // its already persisted, fully acknowledged tool checkpoint.
            return Ok(Reply {
                protocol: protocol.into(),
                items: vec![],
                usage: self.usage,
                output_limited: true,
            });
        }
        if protocol == "anthropic_messages" {
            let has_tools = self.items.values().any(|block| block["type"] == "tool_use");
            if (self.stop == "tool_use") != has_tools {
                return Err("Claude 结束状态与工具调用内容不一致，未执行工具".into());
            }
            if self
                .items
                .keys()
                .any(|index| !self.closed_blocks.contains(index))
            {
                return Err("API 内容块未完整结束，未执行工具".into());
            }
            for (index, args) in self.partial {
                self.items.get_mut(&index).ok_or("API 工具流顺序异常")?["input"] =
                    serde_json::from_str(&args).map_err(|_| "API 工具参数 JSON 不完整")?;
            }
        }
        let items = match protocol {
            "openai_responses" => self.items.into_values().collect(),
            "anthropic_messages" => vec![
                json!({"role":"assistant","content":self.items.into_values().collect::<Vec<_>>()}),
            ],
            _ => {
                let mut msg = json!({"role":"assistant","content":self.text});
                if !self.items.is_empty() {
                    msg["tool_calls"] = json!(self.items.into_values().collect::<Vec<_>>());
                }
                vec![msg]
            }
        };
        Ok(Reply {
            protocol: protocol.into(),
            items,
            usage: self.usage,
            output_limited: false,
        })
    }
}
fn append(value: &mut Value, key: &str, delta: &str) {
    let mut s = value[key].as_str().unwrap_or("").to_string();
    s.push_str(delta);
    value[key] = json!(s);
}
fn api_error(v: &Value) -> String {
    let error = if v["error"].is_object() {
        &v["error"]
    } else {
        v
    };
    let message = error["message"]
        .as_str()
        .or_else(|| error["code"].as_str())
        .unwrap_or("模型 API 请求失败或输出不完整");
    message.chars().take(600).collect()
}

async fn stream_request(
    client: &reqwest::Client,
    profile: &ApiProfile,
    key: &str,
    body: Value,
    mut on_delta: impl FnMut(&str),
) -> Result<Reply, String> {
    let route = match profile.protocol.as_str() {
        "openai_responses" => "responses",
        "anthropic_messages" => "messages",
        "openai_chat" => "chat/completions",
        _ => return Err("不支持的 API 协议".into()),
    };
    let mut req = client
        .post(api_profiles::endpoint(profile, route)?)
        .timeout(Duration::from_secs(600))
        .json(&body);
    req = if profile.protocol == "anthropic_messages" {
        req.header("x-api-key", key)
            .header("anthropic-version", "2023-06-01")
    } else {
        req.bearer_auth(key)
    };
    let mut response = req
        .send()
        .await
        .map_err(|e| format!("API 请求失败：{}", e.without_url()))?;
    let status = response.status();
    if !status.is_success() {
        let mut data = Vec::new();
        while let Some(chunk) = response.chunk().await.map_err(|_| "读取 API 错误失败")? {
            if data.len() + chunk.len() > 64 * 1024 {
                break;
            }
            data.extend_from_slice(&chunk);
        }
        let message = serde_json::from_slice::<Value>(&data)
            .map(|v| api_error(&v))
            .unwrap_or_else(|_| "服务返回了非 JSON 错误，请检查接口地址与协议".into());
        return Err(format!("API HTTP {}：{}", status.as_u16(), message));
    }
    if !response
        .headers()
        .get(reqwest::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|s| s.contains("text/event-stream"))
    {
        return Err("该接口未返回 SSE 流，请检查协议和地址；未自动改用其他接口".into());
    }
    let mut sse = Sse::default();
    let mut acc = Accumulator::default();
    let mut received = 0;
    while let Some(chunk) = response
        .chunk()
        .await
        .map_err(|e| format!("API 流中断：{}", e.without_url()))?
    {
        received += chunk.len();
        if received > MAX_STREAM_BYTES {
            return Err("API 响应超过安全大小上限".into());
        }
        for data in sse.push(&chunk)? {
            if data == "[DONE]" {
                return acc.finish(&profile.protocol);
            }
            let event: Value =
                serde_json::from_str(&data).map_err(|_| "API 返回了无效的 SSE JSON")?;
            let delta = acc.event(&profile.protocol, &event)?;
            if !delta.is_empty() {
                on_delta(&delta);
            }
            if acc.complete && profile.protocol != "openai_chat" {
                return acc.finish(&profile.protocol);
            }
        }
    }
    acc.finish(&profile.protocol)
}

#[cfg(test)]
#[path = "api_agent_execution_tests.rs"]
mod execution_tests;

#[cfg(test)]
#[path = "api_agent_binding_tests.rs"]
mod binding_tests;

#[cfg(test)]
#[path = "api_agent_binding_live_tests.rs"]
mod binding_live_tests;

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    pub(super) fn profile(protocol: &str, url: &str) -> ApiProfile {
        ApiProfile {
            id: "11111111-1111-4111-8111-111111111111".into(),
            name: "mock".into(),
            protocol: protocol.into(),
            base_url: url.into(),
            enabled: true,
            models: vec!["test-model".into()],
            default_model: "test-model".into(),
            max_output_tokens: 512,
        }
    }
    pub(super) fn conn() -> Connection {
        serde_json::from_value(
            json!({"id":"workspace-a","name":"test","kind":"workspace","api_base":"","skill_dir":"",
            "key_prefix":"","key_kind":"","created_at":""}),
        )
        .unwrap()
    }
    pub(super) fn event(v: Value) -> String {
        format!("data: {}\n\n", v)
    }
    /// Local-only HTTP fixture, inspecting actual JSON and auth headers without credential vault access.
    pub(super) async fn server(
        responses: Vec<(u16, String)>,
    ) -> (String, tokio::task::JoinHandle<Vec<(String, Value)>>) {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let task = tokio::spawn(async move {
            let mut requests = Vec::new();
            for (status, body) in responses {
                let (mut socket, _) = listener.accept().await.unwrap();
                let mut bytes = Vec::new();
                let header_end = loop {
                    let mut chunk = [0u8; 4096];
                    let n = socket.read(&mut chunk).await.unwrap();
                    assert!(n > 0);
                    bytes.extend_from_slice(&chunk[..n]);
                    if let Some(p) = bytes.windows(4).position(|x| x == b"\r\n\r\n") {
                        break p + 4;
                    }
                };
                let headers = String::from_utf8(bytes[..header_end].to_vec()).unwrap();
                let length = headers
                    .lines()
                    .find_map(|l| {
                        let (k, v) = l.split_once(':')?;
                        k.eq_ignore_ascii_case("content-length")
                            .then(|| v.trim().parse::<usize>().unwrap())
                    })
                    .unwrap();
                while bytes.len() < header_end + length {
                    let mut chunk = [0u8; 4096];
                    let n = socket.read(&mut chunk).await.unwrap();
                    assert!(n > 0);
                    bytes.extend_from_slice(&chunk[..n]);
                }
                requests.push((
                    headers,
                    serde_json::from_slice(&bytes[header_end..header_end + length]).unwrap(),
                ));
                let content_type = if status == 200 {
                    "text/event-stream"
                } else {
                    "application/json"
                };
                let header=format!("HTTP/1.1 {status} fixture\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",body.len());
                socket.write_all(header.as_bytes()).await.unwrap();
                // Deliberately fragment multi-byte characters and JSON fields.
                for chunk in body.as_bytes().chunks(7) {
                    socket.write_all(chunk).await.unwrap();
                }
            }
            requests
        });
        (format!("http://{addr}/v1"), task)
    }
    #[test]
    fn sse_preserves_utf8_and_crlf() {
        let bytes = "data: {\"text\":\"你好\"}\r\n\r\n".as_bytes();
        let mut parser = Sse::default();
        let mut data = vec![];
        for b in bytes {
            data.extend(parser.push(&[*b]).unwrap());
        }
        assert_eq!(data, vec!["{\"text\":\"你好\"}"]);
    }
    #[test]
    fn api_default_requests_omit_all_reasoning_controls() {
        for protocol in ["openai_responses", "anthropic_messages", "openai_chat"] {
            for model in ["test-model", "gpt-6-sol", "claude-sonnet-5-5"] {
                let p = profile(protocol, "https://example.test/v1");
                let body = request_body(&p, model, "", "system", &[], &[]).unwrap();
                for key in ["reasoning", "reasoning_effort", "thinking", "output_config"] {
                    assert!(
                        body.get(key).is_none(),
                        "default {protocol}/{model} added {key}"
                    );
                }
                assert_eq!(body["model"], model);
            }
        }
    }
    #[test]
    fn api_request_rejects_invalid_effort_without_falling_back() {
        let p = profile("anthropic_messages", "https://example.test/v1");
        assert!(request_body(&p, "claude-sonnet-5-5", "api:ultra", "", &[], &[]).is_err());
        assert!(request_body(&p, "claude-opus-4-6", "api:xhigh", "", &[], &[]).is_err());
        assert!(request_body(&p, "test-model", "api:high", "", &[], &[]).is_err());
        let p = profile("openai_chat", "https://example.test/v1");
        assert!(request_body(&p, "gpt-6-sol", "api:high", "", &[], &[]).is_err());
    }
    #[test]
    fn chat_reasoning_uses_completion_limit_without_changing_compatible_models() {
        let p = profile("openai_chat", "https://example.test/v1");
        let body = request_body(&p, "gpt-5.6-sol", "api:high", "system", &[], &[]).unwrap();
        assert_eq!(body["reasoning_effort"], "high");
        assert_eq!(body["max_completion_tokens"], p.max_output_tokens);
        assert!(body.get("max_tokens").is_none());
        let body = request_body(&p, "gpt-6-sol", "api:none", "system", &[], &[]).unwrap();
        assert_eq!(body["reasoning_effort"], "none");
        assert_eq!(body["max_completion_tokens"], p.max_output_tokens);
        let body = request_body(&p, "custom-compatible-model", "", "system", &[], &[]).unwrap();
        assert_eq!(body["max_tokens"], p.max_output_tokens);
        assert!(body.get("max_completion_tokens").is_none());
    }
    #[test]
    fn histories_are_scoped_to_connection_conversation_profile_model_endpoint() {
        let p = profile("openai_responses", "https://api.openai.com/v1");
        let c = conn();
        let w = Path::new("/test/work");
        let original = scope(&c, w, &p, "m", "conv-a");
        assert_ne!(original, scope(&c, w, &p, "m", "conv-b"));
        assert_ne!(original, scope(&c, w, &p, "other", "conv-a"));
        let mut other = c.clone();
        other.id = "workspace-b".into();
        assert_ne!(original, scope(&other, w, &p, "m", "conv-a"));
        let mut p2 = p.clone();
        p2.base_url = "https://gateway.example/v1".into();
        assert_ne!(original, scope(&c, w, &p2, "m", "conv-a"));
        p2 = p.clone();
        p2.id = "different".into();
        assert_ne!(original, scope(&c, w, &p2, "m", "conv-a"));
        assert!(session_id("../../outside", false).is_err());
    }
    #[test]
    fn first_turn_preserves_seed_and_cannot_overwrite_crashed_history() {
        let id = uuid::Uuid::new_v4().to_string();
        let reference = format!("api:{id}");
        assert_eq!(session_id(&reference, true).unwrap(), id);
        assert_eq!(session_id(&reference, false).unwrap(), id);
        assert!(session_id("legacy-cli-session", true).is_err());
        assert!(uuid::Uuid::parse_str(&session_id("", true).unwrap()).is_ok());
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("session.json");
        ensure_new_history(&path).unwrap();
        let interrupted = History {
            scope: "scope".into(),
            protocol: "openai_responses".into(),
            system: String::new(),
            thinking_prefix: String::new(),
            items: vec![],
            interrupted: true,
        };
        save_history(&path, &interrupted).unwrap();
        assert!(ensure_new_history(&path).is_err());
        let persisted: History = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
        assert!(persisted.interrupted);
    }

    #[test]
    fn explicit_history_limits_do_not_drop_old_messages() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("session.json");
        let mut h = History {
            scope: "x".into(),
            protocol: "openai_responses".into(),
            system: "".into(),
            thinking_prefix: String::new(),
            items: vec![json!({"role":"user","content":"keep"})],
            interrupted: false,
        };
        save_history(&path, &h).unwrap();
        h.items
            .push(json!({"role":"user","content":"x".repeat(MAX_HISTORY_BYTES)}));
        assert!(save_history(&path, &h).is_err());
        assert!(std::fs::read_to_string(path).unwrap().contains("keep"));
    }
    #[test]
    fn duplicate_tools_and_truncated_streams_are_errors() {
        let mut acc = Accumulator::default();
        acc.event(
            "openai_responses",
            &json!({"type":"response.output_text.delta","delta":"partial"}),
        )
        .unwrap();
        assert!(acc.finish("openai_responses").is_err());
        let reply = Reply {
            protocol: "openai_responses".into(),
            usage: TurnUsage::default(),
            output_limited: false,
            items: vec![
                json!({"type":"function_call","call_id":"a","name":"write_file","arguments":"{}"}),
                json!({"type":"function_call","call_id":"a","name":"write_file","arguments":"{}"}),
            ],
        };
        assert!(reply.calls().is_err());
    }
    fn anthropic_accumulator(events: Vec<Value>) -> Accumulator {
        let mut accumulator = Accumulator::default();
        accumulator.event("anthropic_messages", &json!({
            "type":"message_start", "message":{"usage":{
                "input_tokens":120, "cache_read_input_tokens":30, "cache_creation_input_tokens":10
            }}
        })).unwrap();
        for event in events {
            accumulator.event("anthropic_messages", &event).unwrap();
        }
        accumulator
    }
    fn anthropic_tool_events(arguments: &str, closed: bool) -> Vec<Value> {
        let mut events = vec![
            json!({"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"write-1","name":"write_file","input":{}}}),
            json!({"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":arguments}}),
        ];
        if closed {
            events.push(json!({"type":"content_block_stop","index":0}));
        }
        events
    }
    fn stop_anthropic(accumulator: &mut Accumulator, reason: &str) -> Result<String, String> {
        accumulator.event("anthropic_messages", &json!({
            "type":"message_delta", "delta":{"stop_reason":reason}, "usage":{"output_tokens":8192}
        }))?;
        accumulator.event("anthropic_messages", &json!({"type":"message_stop"}))
    }
    #[test]
    fn anthropic_output_limit_discards_text_thinking_and_every_tool_block() {
        let cases = vec![
            vec![
                json!({"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}),
                json!({"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"unfinished answer"}}),
                json!({"type":"content_block_stop","index":0}),
            ],
            vec![
                json!({"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}),
                json!({"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"unfinished private reasoning"}}),
                json!({"type":"content_block_stop","index":0}),
            ],
            anthropic_tool_events("{\"path\":\"index.html\",\"content\":\"incomplete", true),
            anthropic_tool_events("{\"path\":\"index.html\",\"content\":\"incomplete", false),
            anthropic_tool_events(
                r#"{"path":"index.html","content":"complete","overwrite":true}"#,
                true,
            ),
        ];
        for events in cases {
            let mut accumulator = anthropic_accumulator(events);
            stop_anthropic(&mut accumulator, "max_tokens").unwrap();
            let reply = accumulator.finish("anthropic_messages").unwrap();
            assert!(reply.output_limited);
            assert!(
                reply.items.is_empty(),
                "capped response content must not enter history"
            );
            assert!(
                reply.calls().is_err(),
                "capped response must never enter tool execution"
            );
            assert_eq!(reply.usage.input, 120);
            assert_eq!(reply.usage.output, 8192);
            assert_eq!(reply.usage.cache_read, 30);
            assert_eq!(reply.usage.cache_create, 10);
        }
    }
    #[test]
    fn anthropic_output_limit_discards_completed_tools_before_truncated_tool() {
        let mut events = anthropic_tool_events(
            r#"{"path":"done.html","content":"done","overwrite":true}"#,
            true,
        );
        events.extend([
            json!({"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"write-2","name":"write_file","input":{}}}),
            json!({"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"next.html\",\"content\":\"cut off"}}),
            json!({"type":"content_block_stop","index":1}),
        ]);
        let mut accumulator = anthropic_accumulator(events);
        stop_anthropic(&mut accumulator, "max_tokens").unwrap();
        let reply = accumulator.finish("anthropic_messages").unwrap();
        assert!(reply.output_limited);
        assert!(reply.items.is_empty());
        assert!(reply.calls().is_err());
    }
    #[test]
    fn anthropic_output_limit_requires_message_stop_not_just_stop_reason() {
        let mut accumulator = anthropic_accumulator(anthropic_tool_events("{\"path\":", true));
        accumulator.event("anthropic_messages", &json!({
            "type":"message_delta", "delta":{"stop_reason":"max_tokens"}, "usage":{"output_tokens":8192}
        })).unwrap();
        let error = accumulator.finish("anthropic_messages").err().unwrap();
        assert!(error.contains("连接在回合完成前断开"));
    }
    #[test]
    fn anthropic_output_limit_does_not_relax_normal_tool_validation() {
        for (arguments, closed) in [("{\"path\":", true), (r#"{"path":"index.html"}"#, false)] {
            let mut accumulator = anthropic_accumulator(anthropic_tool_events(arguments, closed));
            stop_anthropic(&mut accumulator, "tool_use").unwrap();
            assert!(accumulator.finish("anthropic_messages").is_err());
        }
        let mut accumulator = anthropic_accumulator(anthropic_tool_events("[]", true));
        stop_anthropic(&mut accumulator, "tool_use").unwrap();
        let reply = accumulator.finish("anthropic_messages").unwrap();
        assert!(!reply.output_limited);
        assert!(reply.calls().is_err());
        let mut accumulator = anthropic_accumulator(anthropic_tool_events(
            r#"{"path":"index.html","content":"ok","overwrite":true}"#,
            true,
        ));
        stop_anthropic(&mut accumulator, "tool_use").unwrap();
        let reply = accumulator.finish("anthropic_messages").unwrap();
        assert!(!reply.output_limited);
        assert_eq!(reply.calls().unwrap()[0].arguments["path"], "index.html");
        assert_eq!(reply.usage.output, 8192);
    }
    #[test]
    fn anthropic_output_limit_does_not_recover_other_abnormal_stops() {
        for reason in [
            "refusal",
            "model_context_window_exceeded",
            "pause_turn",
            "unknown",
            "",
        ] {
            let mut accumulator = anthropic_accumulator(vec![]);
            assert!(
                stop_anthropic(&mut accumulator, reason).is_err(),
                "{reason}"
            );
            assert!(accumulator.finish("anthropic_messages").is_err());
        }
    }
    #[tokio::test]
    async fn responses_stream_tool_roundtrip_keeps_encrypted_reasoning() {
        let reasoning =
            json!({"type":"reasoning","id":"r1","summary":[],"encrypted_content":"opaque"});
        let tool = json!({"type":"function_call","id":"f1","call_id":"call1","name":"list_files","arguments":"{\"path\":\".\"}"});
        let first = event(
            json!({"type":"response.completed","response":{"output":[reasoning,tool],"usage":{"input_tokens":10,"output_tokens":3}}}),
        );
        let last = event(json!({"type":"response.output_text.delta","delta":"完成"}))
            + &event(
                json!({"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}],"usage":{"input_tokens":20,"output_tokens":2}}}),
            );
        let (url, server) = server(vec![(200, first), (200, last)]).await;
        let p = profile("openai_responses", &url);
        let client = api_profiles::http_client().unwrap();
        let mut items = vec![json!({"role":"user","content":"list"})];
        let reply = stream_request(
            &client,
            &p,
            "mock-secret",
            request_body(&p, "gpt-6-sol", "api:high", "system", &items, &[]).unwrap(),
            |_| {},
        )
        .await
        .unwrap();
        assert_eq!(reply.calls().unwrap()[0].name, "list_files");
        items.extend(reply.items);
        append_tool_outputs(
            &p.protocol,
            &mut items,
            vec![("call1".into(), "[]".into(), false)],
        )
        .unwrap();
        let mut text = String::new();
        let final_reply = stream_request(
            &client,
            &p,
            "mock-secret",
            request_body(&p, "gpt-6-sol", "api:high", "system", &items, &[]).unwrap(),
            |s| text.push_str(s),
        )
        .await
        .unwrap();
        assert_eq!(text, "完成");
        assert!(final_reply.calls().unwrap().is_empty());
        let requests = server.await.unwrap();
        assert!(requests[0]
            .0
            .to_ascii_lowercase()
            .contains("authorization: bearer mock-secret"));
        assert_eq!(requests[1].1["input"][1]["encrypted_content"], "opaque");
        assert_eq!(requests[1].1["input"][3]["type"], "function_call_output");
        assert_eq!(requests[0].1["store"], false);
        for (_, body) in &requests {
            assert_eq!(body["reasoning"]["effort"], "high");
            assert_eq!(body["max_output_tokens"], p.max_output_tokens);
        }
    }
    #[tokio::test]
    async fn anthropic_stream_preserves_thinking_signature_and_tools() {
        let parts = vec![
            json!({"type":"message_start","message":{"usage":{"input_tokens":8,"cache_read_input_tokens":2}}}),
            json!({"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}),
            json!({"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"opaque thought"}}),
            json!({"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}),
            json!({"type":"content_block_stop","index":0}),
            json!({"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool1","name":"read_file","input":{}}}),
            json!({"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"SKILL.md\"}"}}),
            json!({"type":"content_block_stop","index":1}),
            json!({"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}),
            json!({"type":"message_stop"}),
        ];
        let (url, server) = server(vec![(200, parts.into_iter().map(event).collect())]).await;
        let p = profile("anthropic_messages", &url);
        let client = api_profiles::http_client().unwrap();
        let reply = stream_request(
            &client,
            &p,
            "secret",
            request_body(
                &p,
                "claude-sonnet-5-5",
                "api:xhigh",
                "s",
                &[json!({"role":"user","content":"read"})],
                &[],
            )
            .unwrap(),
            |_| panic!("thinking must not stream as user text"),
        )
        .await
        .unwrap();
        assert_eq!(reply.calls().unwrap()[0].arguments["path"], "SKILL.md");
        assert_eq!(reply.items[0]["content"][0]["signature"], "signed");
        assert_eq!(reply.usage.input, 8);
        let mut items = reply.items;
        append_tool_outputs(
            &p.protocol,
            &mut items,
            vec![("tool1".into(), "contents".into(), false)],
        )
        .unwrap();
        let next = request_body(&p, "claude-sonnet-5-5", "api:xhigh", "s", &items, &[]).unwrap();
        assert_eq!(next["messages"][1]["content"][0]["tool_use_id"], "tool1");
        assert_eq!(next["output_config"]["effort"], "xhigh");
        assert_eq!(next["thinking"]["type"], "adaptive");
        let requests = server.await.unwrap();
        assert_eq!(requests[0].1["output_config"]["effort"], "xhigh");
        assert_eq!(requests[0].1["thinking"]["type"], "adaptive");
        assert_eq!(requests[0].1["max_tokens"], p.max_output_tokens);
        assert!(requests[0]
            .0
            .to_ascii_lowercase()
            .contains("x-api-key: secret"));
        assert!(!requests[0]
            .0
            .to_ascii_lowercase()
            .contains("authorization:"));
    }
    #[tokio::test]
    async fn http_and_stream_errors_never_become_success() {
        let responses = vec![
            (
                400,
                json!({"error":{"message":"unsupported model"}}).to_string(),
            ),
            (
                200,
                event(json!({"type":"error","error":{"message":"overloaded"}})),
            ),
            (
                200,
                event(json!({"type":"response.output_text.delta","delta":"partial"})),
            ),
        ];
        let (url, server) = server(responses).await;
        let p = profile("openai_responses", &url);
        let client = api_profiles::http_client().unwrap();
        for _ in 0..3 {
            assert!(stream_request(&client, &p, "test", json!({}), |_| {})
                .await
                .is_err());
        }
        server.await.unwrap();
    }
    #[test]
    fn chat_tool_fragments_are_assembled_and_nonstop_rejected() {
        let mut a = Accumulator::default();
        a.event("openai_chat",&json!({"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"read_file","arguments":"{\"pa"}}]}}]})).unwrap();
        a.event("openai_chat",&json!({"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a\"}"}}]},"finish_reason":"tool_calls"}]})).unwrap();
        assert_eq!(
            a.finish("openai_chat").unwrap().calls().unwrap()[0].arguments["path"],
            "a"
        );
        assert!(Accumulator::default()
            .event(
                "openai_chat",
                &json!({"choices":[{"finish_reason":"length"}]})
            )
            .is_err());
        assert!(Accumulator::default()
            .event("anthropic_messages", &json!({"type":"message_stop"}))
            .is_err());
    }
    #[tokio::test]
    async fn registry_cancellation_interrupts_pending_api_wait() {
        let registry = RunRegistry::default();
        let (_, mut stop) = registry.register("turn");
        let copy = registry.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(10)).await;
            copy.cancel("turn");
        });
        let stopped =
            tokio::select! {_=&mut stop=>true,_=tokio::time::sleep(Duration::from_secs(1))=>false};
        assert!(stopped);
        assert!(registry.is_canceled("turn"));
        registry.unregister("turn");
    }
}
