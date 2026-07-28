//! GCMS 网页端 Pilot 控制台客户端。
//! 仅建立出站 HTTPS 连接；不监听端口，不依赖 SSH、Cloudflare 或公网访问本机。

use crate::{agent, keychain, now_secs, permit, AppState};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;
use tauri::ipc::{Channel, InvokeResponseBody};
use tauri::{AppHandle, Manager};
use tokio::sync::Notify;

const PROTOCOL_VERSION: &str = "1";
const DEVICE_SECRET_ACCOUNT: &str = "pilot-console:device";

#[derive(Clone, Serialize, Deserialize, Debug)]
pub struct ConsoleBinding {
    pub id: String,
    pub conn_id: String,
    pub api_base: String,
    pub device_id: String,
    pub device_name: String,
    pub pilot_version: String,
    #[serde(default)]
    pub default_site_id: i64,
    #[serde(default)]
    pub default_brain: String,
    #[serde(default)]
    pub default_model: String,
    #[serde(default)]
    pub default_effort: String,
    #[serde(default)]
    pub last_seen_at: u64,
    #[serde(default)]
    pub last_error: String,
    #[serde(default)]
    pub connected: bool,
}

#[derive(Clone)]
pub struct BindingStore {
    file: PathBuf,
    device_file: PathBuf,
    notify: Arc<Notify>,
}

impl BindingStore {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            file: data_dir.join("pilot_console_bindings.json"),
            device_file: data_dir.join("pilot_device_id"),
            notify: Arc::new(Notify::new()),
        }
    }

    pub fn list(&self) -> Vec<ConsoleBinding> {
        fs::read(&self.file)
            .ok()
            .and_then(|raw| serde_json::from_slice(&raw).ok())
            .unwrap_or_default()
    }

    fn save(&self, list: &[ConsoleBinding]) -> Result<(), String> {
        let raw = serde_json::to_vec_pretty(list).map_err(|e| e.to_string())?;
        let tmp = self.file.with_extension("json.tmp");
        fs::write(&tmp, raw).map_err(|e| format!("write Pilot console bindings: {e}"))?;
        // Windows 不保证 rename 可覆盖现有文件：先使用同目录 replace 语义失败时保留原文件，
        // 不做删除再改名，避免崩溃窗口清空绑定。
        #[cfg(target_os = "windows")]
        {
            if self.file.exists() {
                let backup = self.file.with_extension("json.previous");
                let _ = fs::rename(&self.file, &backup);
                if let Err(error) = fs::rename(&tmp, &self.file) {
                    let _ = fs::rename(&backup, &self.file);
                    return Err(format!("replace Pilot console bindings: {error}"));
                }
                let _ = fs::remove_file(backup);
                return Ok(());
            }
        }
        fs::rename(&tmp, &self.file).map_err(|e| format!("replace Pilot console bindings: {e}"))
    }

    pub fn upsert(&self, binding: ConsoleBinding) -> Result<(), String> {
        let mut list = self.list();
        if let Some(slot) = list.iter_mut().find(|item| item.id == binding.id) {
            *slot = binding;
        } else {
            list.retain(|item| item.conn_id != binding.conn_id);
            list.push(binding);
        }
        self.save(&list)?;
        self.notify.notify_waiters();
        Ok(())
    }

    pub fn remove(&self, id: &str) -> Result<(), String> {
        let mut list = self.list();
        list.retain(|item| item.id != id);
        self.save(&list)?;
        self.notify.notify_waiters();
        Ok(())
    }

    fn update_status(&self, id: &str, connected: bool, error: String) {
        let mut list = self.list();
        if let Some(binding) = list.iter_mut().find(|item| item.id == id) {
            binding.connected = connected;
            binding.last_error = error;
            if connected {
                binding.last_seen_at = now_secs();
            }
            let _ = self.save(&list);
        }
    }

    fn device_id(&self) -> Result<String, String> {
        if let Ok(value) = fs::read_to_string(&self.device_file) {
            let value = value.trim();
            if !value.is_empty() {
                return Ok(value.to_string());
            }
        }
        let value = uuid::Uuid::new_v4().to_string();
        fs::write(&self.device_file, format!("{value}\n"))
            .map_err(|e| format!("write Pilot device id: {e}"))?;
        Ok(value)
    }
}

#[derive(Deserialize)]
struct BindResponse {
    binding: RemoteBinding,
}

#[derive(Deserialize)]
struct RemoteBinding {
    #[serde(rename = "ID")]
    id: String,
}

#[derive(Clone, Deserialize)]
struct RemoteTask {
    #[serde(rename = "ID")]
    id: String,
    #[serde(rename = "RequestID")]
    request_id: String,
    #[serde(rename = "ConversationID")]
    conversation_id: String,
    #[serde(rename = "Prompt")]
    prompt: String,
    #[serde(rename = "Operation")]
    operation: String,
    #[serde(rename = "SiteSlugsJSON")]
    site_slugs_json: String,
    #[serde(rename = "SiteNamesJSON")]
    site_names_json: String,
    #[serde(rename = "Brain")]
    brain: String,
    #[serde(rename = "Model")]
    model: String,
    #[serde(rename = "Effort")]
    effort: String,
}

#[derive(Deserialize)]
struct ClaimResponse {
    task: Option<RemoteTask>,
    lease_token: Option<String>,
}

fn console_api_base(api_base: &str) -> Result<String, String> {
    let value = api_base.trim().trim_end_matches('/');
    if !value.starts_with("https://") && !cfg!(debug_assertions) {
        return Err("Pilot 控制台绑定要求 HTTPS GCMS 地址".into());
    }
    if let Some((root, _)) = value.split_once("/api/platform/v1/") {
        return Ok(format!("{root}/api/platform/v1/pilot"));
    }
    if let Some(root) = value.strip_suffix("/api/platform/v1") {
        return Ok(format!("{root}/api/platform/v1/pilot"));
    }
    if let Some(root) = value.strip_suffix("/api/admin/v1") {
        return Ok(format!("{root}/api/platform/v1/pilot"));
    }
    Err("技能包 API 地址不是受支持的 GCMS v1 地址".into())
}

fn device_secret() -> Result<String, String> {
    if let Ok(secret) = keychain::get_named_secret(DEVICE_SECRET_ACCOUNT) {
        if !secret.trim().is_empty() {
            return Ok(secret);
        }
    }
    let secret = format!(
        "gcmspd_{}{}",
        uuid::Uuid::new_v4().simple(),
        uuid::Uuid::new_v4().simple()
    );
    keychain::set_named_secret(DEVICE_SECRET_ACCOUNT, &secret)?;
    Ok(secret)
}

fn platform_name() -> &'static str {
    if cfg!(target_os = "windows") {
        "windows"
    } else if cfg!(target_os = "macos") {
        "macos"
    } else {
        "unsupported"
    }
}

async fn response_json(response: reqwest::Response) -> Result<Value, String> {
    let status = response.status();
    let text = response.text().await.unwrap_or_default();
    let value: Value =
        serde_json::from_str(&text).unwrap_or_else(|_| json!({"message": text.trim()}));
    if !status.is_success() {
        return Err(value
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or("GCMS Pilot 控制台请求失败")
            .to_string());
    }
    Ok(value)
}

#[tauri::command]
pub async fn pilot_console_bind(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    device_name: String,
    pilot_version: String,
    default_site_id: i64,
    default_brain: String,
    default_model: String,
    default_effort: String,
) -> Result<ConsoleBinding, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "gcms" {
        return Err("只能绑定已导入的 GCMS 技能包连接".into());
    }
    let api_base = console_api_base(&conn.api_base)?;
    let device_id = state.pilot_bindings.device_id()?;
    let secret = device_secret()?;
    let skill_key = keychain::get_key(&conn.id)?;
    let name = if device_name.trim().is_empty() {
        std::env::var("COMPUTERNAME")
            .or_else(|_| std::env::var("HOSTNAME"))
            .unwrap_or_else(|_| "GCMS Pilot".into())
    } else {
        device_name.trim().to_string()
    };
    let response = reqwest::Client::new()
        .post(format!("{api_base}/bindings"))
        .bearer_auth(skill_key)
        .timeout(Duration::from_secs(30))
        .json(&json!({
            "device_id": device_id,
            "device_secret": secret,
            "device_name": name,
            "pilot_version": pilot_version,
            "protocol_version": PROTOCOL_VERSION,
            "platform": platform_name(),
            "connection_name": conn.name,
            "default_site_id": default_site_id,
        }))
        .send()
        .await
        .map_err(|e| format!("无法连接 GCMS：{e}"))?;
    let value = response_json(response).await?;
    let result: BindResponse = serde_json::from_value(value).map_err(|e| e.to_string())?;
    let binding = ConsoleBinding {
        id: result.binding.id,
        conn_id,
        api_base,
        device_id,
        device_name: name,
        pilot_version,
        default_site_id,
        default_brain,
        default_model,
        default_effort,
        last_seen_at: now_secs(),
        last_error: String::new(),
        connected: true,
    };
    state.pilot_bindings.upsert(binding.clone())?;
    Ok(binding)
}

#[tauri::command]
pub fn pilot_console_status(state: tauri::State<'_, AppState>) -> Vec<ConsoleBinding> {
    state.pilot_bindings.list()
}

#[tauri::command]
pub async fn pilot_console_unbind(
    state: tauri::State<'_, AppState>,
    binding_id: String,
) -> Result<(), String> {
    let binding = state
        .pilot_bindings
        .list()
        .into_iter()
        .find(|item| item.id == binding_id)
        .ok_or("绑定不存在")?;
    let secret = device_secret()?;
    let response = reqwest::Client::new()
        .delete(format!("{}/bindings/{}", binding.api_base, binding.id))
        .header("X-GCMS-Pilot-Device", &binding.device_id)
        .bearer_auth(secret)
        .timeout(Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| format!("无法在 GCMS 撤销绑定：{e}"))?;
    let status = response.status();
    if status.is_success() {
        response_json(response).await?;
    } else if status != reqwest::StatusCode::UNAUTHORIZED
        && status != reqwest::StatusCode::NOT_FOUND
    {
        response_json(response).await?;
    }
    // GCMS 端先解除绑定时会立即作废最后一枚设备凭据；401/404 表示服务端
    // 已完成撤销，本地仍须清理元数据和旧钥匙串密钥，才能使用新密钥重绑。
    state.pilot_bindings.remove(&binding.id)?;
    if state.pilot_bindings.list().is_empty() {
        keychain::delete_named_secret(DEVICE_SECRET_ACCOUNT)?;
    }
    Ok(())
}

#[tauri::command]
pub fn pilot_console_reconnect(state: tauri::State<'_, AppState>) {
    state.pilot_bindings.notify.notify_waiters();
}

#[tauri::command]
pub async fn pilot_console_set_default_site(
    state: tauri::State<'_, AppState>,
    binding_id: String,
    site_id: i64,
) -> Result<ConsoleBinding, String> {
    let mut binding = state
        .pilot_bindings
        .list()
        .into_iter()
        .find(|item| item.id == binding_id)
        .ok_or("绑定不存在")?;
    device_request(
        &binding,
        reqwest::Method::PATCH,
        &format!("/bindings/{}/default-site", binding.id),
        json!({"SiteID":site_id}),
    )
    .await?;
    // 只有服务端确认站点仍在技能包实时授权范围内后才更新本地持久值。
    binding.default_site_id = site_id;
    state.pilot_bindings.upsert(binding.clone())?;
    Ok(binding)
}

async fn device_request(
    binding: &ConsoleBinding,
    method: reqwest::Method,
    path: &str,
    body: Value,
) -> Result<Value, String> {
    let secret = device_secret()?;
    let response = reqwest::Client::new()
        .request(method, format!("{}{}", binding.api_base, path))
        .header("X-GCMS-Pilot-Device", &binding.device_id)
        .bearer_auth(secret)
        .timeout(Duration::from_secs(35))
        .json(&body)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    response_json(response).await
}

async fn post_event(binding: &ConsoleBinding, task_id: &str, lease_token: &str, event: Value) {
    let event_type = event
        .get("type")
        .and_then(Value::as_str)
        .unwrap_or("progress");
    let _ = device_request(
        binding,
        reqwest::Method::POST,
        &format!("/tasks/{task_id}/events"),
        json!({"LeaseToken":lease_token,"Type":event_type,"Payload":event}),
    )
    .await;
}

fn remote_task_event_is_persistable(event: &Value) -> bool {
    event.get("type").and_then(Value::as_str) != Some("gcms_unlock_required")
}

async fn execute_control_task(
    app: &AppHandle,
    binding: &ConsoleBinding,
    task: &RemoteTask,
    lease: &str,
) -> Option<Result<String, String>> {
    if !matches!(
        task.operation.as_str(),
        "schedule.create"
            | "schedule.toggle"
            | "schedule.delete"
            | "managed.pause"
            | "managed.resume"
            | "managed.disable"
    ) {
        return None;
    }
    let _ = device_request(
        binding,
        reqwest::Method::PATCH,
        &format!("/tasks/{}", task.id),
        json!({"LeaseToken":lease,"Status":"running","Progress":"正在更新 Pilot 本地设置"}),
    )
    .await;
    let payload: Value = match serde_json::from_str(&task.prompt) {
        Ok(value) => value,
        Err(error) => return Some(Err(format!("控制参数无效：{error}"))),
    };
    let local_id = payload.get("id").and_then(Value::as_str).unwrap_or("");
    if task.operation != "schedule.create" && local_id.is_empty() {
        return Some(Err("缺少 Pilot 本地记录 ID".into()));
    }
    let state = app.state::<AppState>();
    let result = match task.operation.as_str() {
        "schedule.create" => {
            let site_slugs: Vec<String> =
                serde_json::from_str(&task.site_slugs_json).unwrap_or_default();
            let site_names: Vec<String> =
                serde_json::from_str(&task.site_names_json).unwrap_or_default();
            let title = payload
                .get("title")
                .and_then(Value::as_str)
                .unwrap_or("")
                .to_string();
            let prompt = payload
                .get("prompt")
                .and_then(Value::as_str)
                .unwrap_or("")
                .to_string();
            let interval = payload
                .get("interval_minutes")
                .and_then(Value::as_u64)
                .unwrap_or(1440);
            let brain = payload
                .get("brain")
                .and_then(Value::as_str)
                .unwrap_or("codex")
                .to_string();
            let model = payload
                .get("model")
                .and_then(Value::as_str)
                .unwrap_or("")
                .to_string();
            let effort = payload
                .get("effort")
                .and_then(Value::as_str)
                .unwrap_or("")
                .to_string();
            super::upsert_task(
                &state.conns,
                &state.tasks,
                None,
                binding.conn_id.clone(),
                site_slugs,
                site_names,
                "free".into(),
                brain,
                model,
                effort,
                String::new(),
                String::new(),
                String::new(),
                title,
                prompt,
                interval,
                0,
                true,
            )
            .map(|_| "定时任务已保存到 Pilot".to_string())
        }
        "schedule.toggle" => {
            let enabled = payload
                .get("enabled")
                .and_then(Value::as_bool)
                .ok_or_else(|| "缺少 enabled".to_string());
            match (state.tasks.get(local_id), enabled) {
                (Some(current), Ok(enabled)) if current.conn_id == binding.conn_id => {
                    let now = now_secs();
                    state
                        .tasks
                        .mutate(local_id, |item| {
                            item.enabled = enabled;
                            if enabled {
                                item.advance_past(now);
                            }
                            item.updated_at = now;
                        })
                        .map(|_| {
                            if enabled {
                                "定时任务已启用"
                            } else {
                                "定时任务已暂停"
                            }
                            .to_string()
                        })
                }
                (Some(_), _) => Err("定时任务不属于当前 GCMS 技能包连接".into()),
                (None, _) => Err("定时任务不存在".into()),
            }
        }
        "schedule.delete" => match state.tasks.get(local_id) {
            Some(current) if current.conn_id == binding.conn_id => state
                .tasks
                .remove(local_id)
                .map(|_| "定时任务已删除".to_string()),
            Some(_) => Err("定时任务不属于当前 GCMS 技能包连接".into()),
            None => Err("定时任务不存在".into()),
        },
        "managed.pause" | "managed.resume" => match state.managed.get(local_id) {
            Some(current) if current.conn_id == binding.conn_id => {
                let paused = task.operation == "managed.pause";
                let now = now_secs();
                let updated = state.managed.mutate(local_id, now, |item| {
                    item.paused = paused;
                    if !paused {
                        item.fused_at = 0;
                    }
                });
                if let Ok(Some(item)) = &updated {
                    for task_id in &item.task_ids {
                        let _ = state.tasks.mutate(task_id, |scheduled| {
                            scheduled.enabled = !paused;
                            if !paused {
                                scheduled.advance_past(now);
                            }
                            scheduled.updated_at = now;
                        });
                    }
                }
                updated.map(|_| {
                    if paused {
                        "托管已暂停"
                    } else {
                        "托管已恢复"
                    }
                    .to_string()
                })
            }
            Some(_) => Err("托管记录不属于当前 GCMS 技能包连接".into()),
            None => Err("托管记录不存在".into()),
        },
        "managed.disable" => match state.managed.get(local_id) {
            Some(current) if current.conn_id == binding.conn_id => {
                for task_id in &current.task_ids {
                    let _ = state.tasks.remove(task_id);
                }
                state
                    .managed
                    .remove(local_id)
                    .map(|_| "托管已关闭；站点内容和草稿未删除".to_string())
            }
            Some(_) => Err("托管记录不属于当前 GCMS 技能包连接".into()),
            None => Err("托管记录不存在".into()),
        },
        _ => unreachable!(),
    };
    Some(result)
}

async fn execute_task(app: AppHandle, binding: ConsoleBinding, task: RemoteTask, lease: String) {
    if let Some(result) = execute_control_task(&app, &binding, &task, &lease).await {
        let body = match result {
            Ok(output) => {
                json!({"LeaseToken":lease,"Status":"completed","Progress":"设置已同步到 Pilot","Output":output})
            }
            Err(error) => {
                json!({"LeaseToken":lease,"Status":"failed","ErrorCode":"pilot_control_failed","ErrorMessage":error})
            }
        };
        let _ = device_request(
            &binding,
            reqwest::Method::PATCH,
            &format!("/tasks/{}", task.id),
            body,
        )
        .await;
        return;
    }
    let state = app.state::<AppState>();
    if state.convos.get(&task.conversation_id).is_some() {
        let _ = device_request(
            &binding,
            reqwest::Method::PATCH,
            &format!("/tasks/{}", task.id),
            json!({"LeaseToken":lease,"Status":"failed","ErrorCode":"duplicate_delivery","ErrorMessage":"重复投递已拦截；本地对话已存在"}),
        )
        .await;
        return;
    }
    let site_slugs: Vec<String> = serde_json::from_str(&task.site_slugs_json).unwrap_or_default();
    let site_names: Vec<String> = serde_json::from_str(&task.site_names_json).unwrap_or_default();
    let sitebuild = task.operation == "sites.create";
    let single = site_slugs.len() == 1;
    let brain = if task.brain.trim().is_empty() {
        binding.default_brain.clone()
    } else {
        task.brain.clone()
    };
    let brain = if brain.trim().is_empty() {
        "codex".into()
    } else {
        brain
    };
    let model = if task.model.trim().is_empty() {
        binding.default_model.clone()
    } else {
        task.model.clone()
    };
    let effort = if task.effort.trim().is_empty() {
        binding.default_effort.clone()
    } else {
        task.effort.clone()
    };
    let _ = device_request(
        &binding,
        reqwest::Method::PATCH,
        &format!("/tasks/{}", task.id),
        json!({"LeaseToken":lease,"Status":"running","Progress":format!("Pilot 已创建本地对话并开始执行 · {}", task.request_id)}),
    )
    .await;
    let renewal_stop = Arc::new(Notify::new());
    let was_canceled = Arc::new(AtomicBool::new(false));
    let renewal_binding = binding.clone();
    let renewal_task_id = task.id.clone();
    let renewal_lease = lease.clone();
    let renewal_conv_id = task.conversation_id.clone();
    let renewal_runs = state.runs.clone();
    let renewal_pending_dir = state.data_dir.join("permit").join("pending");
    let renewal_done = renewal_stop.clone();
    let renewal_canceled = was_canceled.clone();
    let renewal = tauri::async_runtime::spawn(async move {
        let mut answered_confirmation = String::new();
        loop {
            tokio::select! {
                _ = renewal_done.notified() => break,
                _ = tokio::time::sleep(Duration::from_secs(3)) => {
                    let pending = permit::list_pending(&renewal_pending_dir)
                        .into_iter()
                        .find(|item| item.conv == renewal_conv_id);
                    if let Some(pending) = pending {
                        if pending.id != answered_confirmation {
                            let response = device_request(
                                &renewal_binding,
                                reqwest::Method::POST,
                                &format!("/tasks/{renewal_task_id}/confirmation"),
                                json!({
                                    "LeaseToken":renewal_lease,
                                    "ConfirmationID":pending.id,
                                    "Confirmation":pending,
                                }),
                            ).await;
                            match response {
                                Ok(value) => {
                                    if value.pointer("/task/CancelRequested").and_then(Value::as_bool).unwrap_or(false) {
                                        renewal_canceled.store(true, Ordering::SeqCst);
                                        renewal_runs.cancel(&renewal_conv_id);
                                        break;
                                    }
                                    let decision = value.pointer("/task/ConfirmationDecision").and_then(Value::as_str).unwrap_or("");
                                    if decision == "allow" || decision == "deny" {
                                        let _ = permit::respond(&renewal_pending_dir, &pending.id, decision == "allow");
                                        answered_confirmation = pending.id;
                                    }
                                }
                                Err(_) => break,
                            }
                            continue;
                        }
                    }
                    let response = device_request(
                        &renewal_binding,
                        reqwest::Method::PATCH,
                        &format!("/tasks/{renewal_task_id}"),
                        json!({"LeaseToken":renewal_lease,"Status":"running","Progress":"Pilot 正在执行，租约已续期"}),
                    ).await;
                    match response {
                        Ok(value) if value.pointer("/task/CancelRequested").and_then(Value::as_bool).unwrap_or(false) => {
                            renewal_canceled.store(true, Ordering::SeqCst);
                            renewal_runs.cancel(&renewal_conv_id);
                            break;
                        }
                        Err(_) => break,
                        _ => {}
                    }
                }
            }
        }
    });

    let (event_tx, mut event_rx) = tokio::sync::mpsc::unbounded_channel::<Value>();
    let channel: Channel<agent::TurnEvent> = Channel::new(move |body| {
        if let InvokeResponseBody::Json(raw) = body {
            if let Ok(value) = serde_json::from_str(&raw) {
                // 页面解锁挑战只能活在本机原生 UI 的当前回合内，不能作为
                // 远程任务事件回传并持久化到 GCMS 任务历史。
                if remote_task_event_is_persistable(&value) {
                    let _ = event_tx.send(value);
                }
            }
        }
        Ok(())
    });
    let event_binding = binding.clone();
    let event_task = task.id.clone();
    let event_lease = lease.clone();
    let event_poster = tauri::async_runtime::spawn(async move {
        while let Some(event) = event_rx.recv().await {
            post_event(&event_binding, &event_task, &event_lease, event).await;
        }
    });

    let result = super::create_conversation(
        state.conns.clone(),
        state.convos.clone(),
        state.runs.clone(),
        state.pack_updating.clone(),
        task.conversation_id.clone(),
        binding.conn_id.clone(),
        if single {
            site_slugs[0].clone()
        } else {
            String::new()
        },
        if single {
            site_names.get(0).cloned().unwrap_or_default()
        } else {
            site_names.join("、")
        },
        site_slugs,
        site_names,
        if sitebuild {
            "sitebuild".into()
        } else {
            "free".into()
        },
        brain,
        model,
        // 远程写操作先经过网页明确确认；Claude 仍保留逐工具 ask 闸作为第二层。
        "ask".into(),
        effort,
        false, // 控制台任务无人值守：fast 单独计费，不自动开
        String::new(),
        task.prompt.clone(),
        channel,
        state.data_dir.clone(),
        state.ssh.clone(),
    )
    .await;
    renewal_stop.notify_waiters();
    let _ = renewal.await;
    drop(state);
    let _ = event_poster.await;
    match result {
        _ if was_canceled.load(Ordering::SeqCst) => {
            let _ = device_request(
                &binding,
                reqwest::Method::PATCH,
                &format!("/tasks/{}", task.id),
                json!({"LeaseToken":lease,"Status":"canceled","Progress":"用户已取消","ErrorCode":"user_canceled","ErrorMessage":"任务已由 GCMS 网页取消"}),
            )
            .await;
        }
        Ok(conversation) => {
            let final_output = conversation
                .messages
                .iter()
                .rev()
                .find(|message| message.role == "assistant")
                .map(|message| message.text.clone())
                .unwrap_or_default();
            let _ = device_request(
                &binding,
                reqwest::Method::PATCH,
                &format!("/tasks/{}", task.id),
                json!({"LeaseToken":lease,"Status":"completed","Progress":"执行完成","Output":final_output}),
            )
            .await;
        }
        Err(error) => {
            let _ = device_request(
                &binding,
                reqwest::Method::PATCH,
                &format!("/tasks/{}", task.id),
                json!({"LeaseToken":lease,"Status":"failed","ErrorCode":"pilot_execution_failed","ErrorMessage":error}),
            )
            .await;
        }
    }
}

async fn poll_binding(app: &AppHandle, binding: &ConsoleBinding) -> Result<String, String> {
    let state = app.state::<AppState>();
    let scheduled_tasks: Vec<_> = state
        .tasks
        .list()
        .into_iter()
        .filter(|task| task.conn_id == binding.conn_id)
        .collect();
    let managed_sites: Vec<_> = state
        .managed
        .list()
        .into_iter()
        .filter(|site| site.conn_id == binding.conn_id)
        .collect();
    let heartbeat = device_request(
        binding,
        reqwest::Method::POST,
        "/heartbeat",
        json!({
            "Name":binding.device_name,
            "Version":binding.pilot_version,
            "Protocol":PROTOCOL_VERSION,
            "bindings":[{
                "binding_id":binding.id,
                "scheduled_tasks":scheduled_tasks,
                "managed_sites":managed_sites
            }]
        }),
    )
    .await?;
    if heartbeat
        .get("bindings")
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .any(|item| item.get("valid") == Some(&Value::Bool(false)))
        })
        .unwrap_or(false)
    {
        return Err("技能包或站点授权已失效；请在 GCMS/Pilot 检查连接".into());
    }
    let warning = heartbeat
        .get("bindings")
        .and_then(Value::as_array)
        .and_then(|items| {
            items.iter().find(|item| {
                item.pointer("/binding/ID").and_then(Value::as_str) == Some(binding.id.as_str())
            })
        })
        .filter(|item| item.get("default_site_valid") == Some(&Value::Bool(false)))
        .map(|_| "默认站点已失效；新对话会临时回退到当前授权列表首项，持久设置未被覆盖".to_string())
        .unwrap_or_default();
    // 远程对话仍是本地真实 Conversation；用户在 Pilot 侧继续后，通过心跳
    // 把最新权威快照补回 GCMS。服务端会按内容去重。
    let conversations = state.convos.list();
    for conversation in conversations
        .iter()
        .filter(|item| item.conn_id == binding.conn_id && item.id.starts_with("pc_"))
    {
        let output = conversation
            .messages
            .iter()
            .rev()
            .find(|message| message.role == "assistant")
            .map(|message| message.text.clone())
            .unwrap_or_default();
        let _ = device_request(
            binding,
            reqwest::Method::PATCH,
            &format!("/conversations/{}", conversation.id),
            json!({
                "binding_id": binding.id,
                "snapshot": {
                    "status": conversation.status,
                    "title": conversation.title,
                    "updated_at": conversation.updated_at,
                    "output": output
                }
            }),
        )
        .await;
    }
    let claim = device_request(binding, reqwest::Method::POST, "/tasks/claim", json!({})).await?;
    let claim: ClaimResponse = serde_json::from_value(claim).map_err(|e| e.to_string())?;
    if let (Some(task), Some(lease)) = (claim.task, claim.lease_token) {
        let app = app.clone();
        let binding = binding.clone();
        tauri::async_runtime::spawn(async move { execute_task(app, binding, task, lease).await });
    }
    Ok(warning)
}

pub fn spawn_worker(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let mut failures = 0u32;
        loop {
            let store = app.state::<AppState>().pilot_bindings.clone();
            let bindings = store.list();
            let mut any_error = false;
            for binding in bindings {
                match poll_binding(&app, &binding).await {
                    Ok(warning) => store.update_status(&binding.id, true, warning),
                    Err(error) => {
                        any_error = true;
                        store.update_status(&binding.id, false, error);
                    }
                }
            }
            failures = if any_error {
                failures.saturating_add(1)
            } else {
                0
            };
            let seconds = if failures == 0 {
                3
            } else {
                (2u64.saturating_pow(failures.min(5))).min(30)
            };
            tokio::select! {
                _ = tokio::time::sleep(Duration::from_secs(seconds)) => {}
                _ = store.notify.notified() => {}
            }
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn derives_console_endpoint_without_ssh_or_local_ports() {
        assert_eq!(
            console_api_base("https://gcms.test/api/platform/v1").unwrap(),
            "https://gcms.test/api/platform/v1/pilot"
        );
        assert_eq!(
            console_api_base("https://gcms.test/api/platform/v1/sites/12").unwrap(),
            "https://gcms.test/api/platform/v1/pilot"
        );
        assert_eq!(
            console_api_base("https://gcms.test/api/admin/v1").unwrap(),
            "https://gcms.test/api/platform/v1/pilot"
        );
    }

    #[test]
    fn old_binding_json_loads_with_defaults() {
        let raw = r#"[{"id":"b","conn_id":"c","api_base":"https://x/api/platform/v1/pilot","device_id":"d","device_name":"Mac","pilot_version":"0.2.39"}]"#;
        let list: Vec<ConsoleBinding> = serde_json::from_str(raw).unwrap();
        assert_eq!(list[0].default_site_id, 0);
        assert!(!list[0].connected);
    }

    #[test]
    fn page_unlock_challenge_is_never_forwarded_to_remote_task_history() {
        assert!(!remote_task_event_is_persistable(&json!({
            "type": "gcms_unlock_required",
            "operation": "pages.publish",
            "unlock_challenge": "gcmspc_secret",
            "admin_path": "/admin/pages/1/project"
        })));
        assert!(remote_task_event_is_persistable(
            &json!({"type":"delta","text":"safe"})
        ));
    }
}
