//! Native API agent. Protocol transcripts are kept locally, never mixed with CLI sessions.
//! No automatic retry/fallback: a disconnected request may already have executed a tool.
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
    items: Vec<Value>,
    /// Set before side effects; cleared only after a complete assistant response.
    interrupted: bool,
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
        let value: History =
            serde_json::from_slice(&raw).map_err(|_| "API 会话历史损坏，未自动覆盖".to_string())?;
        if value.scope != scope || value.protocol != profile.protocol {
            return Err("API 会话归属不匹配".into());
        }
        if value.interrupted {
            return Err(
                "上一回合在工具执行期间中断。为避免重复发布或重复写入，请核对执行记录后新建会话"
                    .into(),
            );
        }
        value
    } else {
        ensure_new_history(&path)?;
        History {
            scope,
            protocol: profile.protocol.clone(),
            system: system.unwrap_or_default(),
            items: vec![],
            interrupted: false,
        }
    };
    history
        .items
        .push(json!({"role":"user", "content":message}));
    save_history(&path, &history)?;
    result.session_ref = format!("api:{id}");
    let instructions = format!("{}\n\n{}", history.system, tools.instructions());
    let client = api_profiles::http_client()?;
    for _ in 0..MAX_ROUNDS {
        let body = request_body(
            profile,
            model,
            &instructions,
            &history.items,
            &tools.definitions(),
        )?;
        if body.to_string().len() > MAX_HISTORY_BYTES {
            return Err("上下文已达到安全上限，请新建会话；未自动丢弃历史".into());
        }
        let reply = stream_request(&client, profile, &loaded.api_key, body, |delta| {
            let safe = tool_runtime::redact(delta, &[&loaded.api_key, site_key]);
            result.text.push_str(&safe);
            let _ = channel.send(TurnEvent::Delta { text: safe });
        })
        .await
        .map_err(|e| tool_runtime::redact(&e, &[&loaded.api_key, site_key]))?;
        add_usage(&mut result.usage, &reply.usage);
        let calls = reply.calls()?;
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
            let detail =
                tool_runtime::redact(&call.arguments.to_string(), &[&loaded.api_key, site_key]);
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
                &[&loaded.api_key, site_key],
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
    system: &str,
    items: &[Value],
    definitions: &[Value],
) -> Result<Value, String> {
    let tools: Vec<Value> = definitions.iter().map(|d| match profile.protocol.as_str() {
        "openai_responses" => json!({"type":"function","name":d["name"],"description":d["description"],"parameters":d["parameters"],"strict":false}),
        "anthropic_messages" => json!({"name":d["name"],"description":d["description"],"input_schema":d["parameters"]}),
        _ => json!({"type":"function","function":d}),
    }).collect();
    match profile.protocol.as_str() {
        "openai_responses" => Ok(
            json!({"model":model,"instructions":system,"input":items,"tools":tools,
            "stream":true,"store":false,"include":["reasoning.encrypted_content"],"max_output_tokens":profile.max_output_tokens}),
        ),
        "anthropic_messages" => Ok(
            json!({"model":model,"system":system,"messages":items,"tools":tools,
            "stream":true,"max_tokens":profile.max_output_tokens}),
        ),
        "openai_chat" => {
            let mut messages = vec![json!({"role":"system","content":system})];
            messages.extend_from_slice(items);
            Ok(
                json!({"model":model,"messages":messages,"tools":tools,"stream":true,
                "stream_options":{"include_usage":true},"max_tokens":profile.max_output_tokens}),
            )
        }
        _ => Err("此 API 协议不受支持，未自动改用其他协议".into()),
    }
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

struct Call {
    id: String,
    name: String,
    arguments: Value,
}
struct Reply {
    protocol: String,
    items: Vec<Value>,
    usage: TurnUsage,
}
impl Reply {
    fn calls(&self) -> Result<Vec<Call>, String> {
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
                    self.items.insert(index, v["content_block"].clone());
                }
                "content_block_delta" => {
                    let item = self.items.get_mut(&index).ok_or("API 工具流顺序异常")?;
                    let d = &v["delta"];
                    match d["type"].as_str().unwrap_or("") {
                        "text_delta" => {
                            delta = d["text"].as_str().unwrap_or("").into();
                            append(item, "text", &delta);
                        }
                        "input_json_delta" => self
                            .partial
                            .entry(index)
                            .or_default()
                            .push_str(d["partial_json"].as_str().unwrap_or("")),
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
                    if let Some(args) = self.partial.remove(&index) {
                        self.items.get_mut(&index).ok_or("API 工具流顺序异常")?["input"] =
                            serde_json::from_str(&args).map_err(|_| "API 工具参数 JSON 不完整")?;
                    }
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
                        "end_turn" | "tool_use" | "stop_sequence"
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
    fn finish(self, protocol: &str) -> Result<Reply, String> {
        if !self.complete {
            return Err("API 连接在回合完成前断开，未标记成功，也不会自动重试".into());
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
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    fn profile(protocol: &str, url: &str) -> ApiProfile {
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
    fn conn() -> Connection {
        serde_json::from_value(
            json!({"id":"workspace-a","name":"test","kind":"workspace","api_base":"","skill_dir":"",
            "key_prefix":"","key_kind":"","created_at":""}),
        )
        .unwrap()
    }
    fn event(v: Value) -> String {
        format!("data: {}\n\n", v)
    }
    /// Local-only HTTP fixture, inspecting actual JSON and auth headers without credential vault access.
    async fn server(
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
            items: vec![
                json!({"type":"function_call","call_id":"a","name":"write_file","arguments":"{}"}),
                json!({"type":"function_call","call_id":"a","name":"write_file","arguments":"{}"}),
            ],
        };
        assert!(reply.calls().is_err());
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
            request_body(&p, "test-model", "system", &items, &[]).unwrap(),
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
            request_body(&p, "test-model", "system", &items, &[]).unwrap(),
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
                "test-model",
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
        let next = request_body(&p, "test-model", "s", &items, &[]).unwrap();
        assert_eq!(next["messages"][1]["content"][0]["tool_use_id"], "tool1");
        let requests = server.await.unwrap();
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
