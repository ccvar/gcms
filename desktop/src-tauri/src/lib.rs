mod agent;
mod brains;
mod convo;
mod discovery;
mod keychain;
mod pack;
mod path_env;
mod scheduled;
mod tasks;

use std::collections::HashSet;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tauri::ipc::Channel;
use tauri::menu::{Menu, MenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Manager, WindowEvent};
use tauri_plugin_notification::NotificationExt;

use convo::{Conversation, Message};
use tasks::ScheduledTask;

struct AppState {
    conns: pack::ConnStore,
    convos: convo::ConvStore,
    tasks: tasks::TaskStore,
    runs: agent::RunRegistry,
    /// 正在跑的定时任务 id（调度器与 run_task_now 共享，防止同一任务被重复触发）。
    firing: Arc<Mutex<HashSet<String>>>,
}

fn now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

#[tauri::command]
fn list_connections(state: tauri::State<'_, AppState>) -> Vec<pack::Connection> {
    state.conns.list()
}

#[tauri::command]
async fn import_pack(
    state: tauri::State<'_, AppState>,
    zip_path: String,
    name: Option<String>,
    key: Option<String>,
) -> Result<pack::ImportOutcome, String> {
    let store = state.conns.clone();
    tauri::async_runtime::spawn_blocking(move || store.import_zip(&zip_path, name, key))
        .await
        .map_err(|e| e.to_string())?
}

#[tauri::command]
async fn remove_connection(state: tauri::State<'_, AppState>, id: String) -> Result<(), String> {
    let store = state.conns.clone();
    tauri::async_runtime::spawn_blocking(move || store.remove(&id))
        .await
        .map_err(|e| e.to_string())?
}

#[tauri::command]
async fn discover_sites(
    state: tauri::State<'_, AppState>,
    conn_id: String,
) -> Result<serde_json::Value, String> {
    let conn = state.conns.get(&conn_id)?;
    discovery::discover(&conn).await
}

#[tauri::command]
async fn detect_brains() -> Result<brains::BrainsInfo, String> {
    Ok(brains::detect().await)
}

/// 排期：查该连接下各站点待定时发布的内容（只读反映 gcms 服务端状态）。
#[tauri::command]
async fn list_scheduled(
    state: tauri::State<'_, AppState>,
    conn_id: String,
) -> Result<Vec<scheduled::ScheduledItem>, String> {
    let conn = state.conns.get(&conn_id)?;
    scheduled::list_scheduled(&conn).await
}

#[tauri::command]
fn list_conversations(state: tauri::State<'_, AppState>) -> Vec<Conversation> {
    state.convos.list()
}

#[tauri::command]
fn get_conversation(state: tauri::State<'_, AppState>, id: String) -> Option<Conversation> {
    state.convos.get(&id)
}

#[tauri::command]
fn delete_conversation(state: tauri::State<'_, AppState>, id: String) -> Result<(), String> {
    state.convos.remove(&id)
}

#[tauri::command]
fn cancel_turn(state: tauri::State<'_, AppState>, conv_id: String) -> bool {
    state.runs.cancel(&conv_id)
}

/// 会话进行中切换模型（同厂商档位）：仅改存储里的 model，下一轮 send_message 读到即用；
/// 厂商/站点不可改（session/thread 与厂商绑定，站点决定 api/key/cwd）。
#[tauri::command]
fn set_conversation_model(
    state: tauri::State<'_, AppState>,
    conv_id: String,
    model: String,
) -> Result<Option<Conversation>, String> {
    state
        .convos
        .mutate(&conv_id, now_secs(), move |c| c.model = model)
}

// ---- 定时任务 ----

#[tauri::command]
fn list_tasks(state: tauri::State<'_, AppState>) -> Vec<ScheduledTask> {
    state.tasks.list()
}

#[allow(clippy::too_many_arguments)]
#[tauri::command]
fn save_task(
    state: tauri::State<'_, AppState>,
    id: Option<String>,
    conn_id: String,
    site_slug: String,
    site_name: String,
    task_type: String,
    brain: String,
    model: String,
    title: String,
    prompt: String,
    interval_minutes: u64,
    first_run: i64,
    enabled: bool,
) -> Result<ScheduledTask, String> {
    if site_slug.trim().is_empty() {
        return Err("站点不能为空".into());
    }
    if prompt.trim().is_empty() {
        return Err("请填写要让它做的事".into());
    }
    if interval_minutes < 1 {
        return Err("周期至少 1 分钟".into());
    }
    // 连接必须存在，否则这个任务每次运行都会失败——直接在保存时拦下。
    let conn_name = state.conns.get(&conn_id).map_err(|_| "所选连接不存在".to_string())?.name;
    let now = now_secs();
    let existing = id.as_ref().and_then(|i| state.tasks.get(i));
    let tid = id.unwrap_or_else(|| uuid::Uuid::new_v4().to_string());
    let step = interval_minutes.max(1) * 60;
    let mut next = if first_run > 0 { first_run as u64 } else { now };
    while next <= now {
        next += step;
    }
    let task = ScheduledTask {
        id: tid,
        conn_id,
        conn_name,
        site_slug: site_slug.trim().into(),
        site_name,
        task_type,
        brain,
        model,
        title: if title.trim().is_empty() { title_from(&prompt) } else { title.trim().into() },
        prompt: prompt.trim().into(),
        interval_minutes,
        next_run: next,
        enabled,
        last_run: existing.as_ref().map(|e| e.last_run).unwrap_or(0),
        last_status: existing.as_ref().map(|e| e.last_status.clone()).unwrap_or_default(),
        last_summary: existing.as_ref().map(|e| e.last_summary.clone()).unwrap_or_default(),
        last_conv_id: existing.as_ref().map(|e| e.last_conv_id.clone()).unwrap_or_default(),
        runs: existing.as_ref().map(|e| e.runs).unwrap_or(0),
        created_at: existing.as_ref().map(|e| e.created_at).unwrap_or(now),
        updated_at: now,
    };
    state.tasks.upsert(task.clone())?;
    Ok(task)
}

#[tauri::command]
fn delete_task(state: tauri::State<'_, AppState>, id: String) -> Result<(), String> {
    state.tasks.remove(&id)
}

#[tauri::command]
fn set_task_enabled(
    state: tauri::State<'_, AppState>,
    id: String,
    enabled: bool,
) -> Result<Option<ScheduledTask>, String> {
    let now = now_secs();
    state.tasks.mutate(&id, |t| {
        t.enabled = enabled;
        if enabled {
            t.advance_past(now);
        }
        t.updated_at = now;
    })
}

#[tauri::command]
fn run_task_now(state: tauri::State<'_, AppState>, app: AppHandle, id: String) -> Result<(), String> {
    let task = state.tasks.get(&id).ok_or("任务不存在")?;
    // 与调度器共享同一 running 集：正在跑就拒绝，避免重复触发。
    {
        let mut g = state.firing.lock().unwrap();
        if g.contains(&id) {
            return Err("这个任务正在运行中，请稍候".into());
        }
        g.insert(id.clone());
    }
    // 若已到点，手动这次就消费掉到点槽，调度器不会再触发一次。
    let now = now_secs();
    if task.next_run > 0 && task.next_run <= now {
        let mut adv = task.clone();
        adv.advance_past(now);
        let next = adv.next_run;
        let _ = state.tasks.mutate(&id, |x| x.next_run = next);
    }
    let conns = state.conns.clone();
    let convos = state.convos.clone();
    let runs = state.runs.clone();
    let tstore = state.tasks.clone();
    let firing = state.firing.clone();
    tauri::async_runtime::spawn(async move {
        fire_task(app, conns, convos, runs, tstore, task).await;
        firing.lock().unwrap().remove(&id);
    });
    Ok(())
}

/// 触发一次任务：开一个全新对话跑 task.prompt，回写任务的 last_* 并通知。
async fn fire_task(
    app: AppHandle,
    conns: pack::ConnStore,
    convos: convo::ConvStore,
    runs: agent::RunRegistry,
    tstore: tasks::TaskStore,
    task: ScheduledTask,
) {
    let conv_id = uuid::Uuid::new_v4().to_string();
    // 后台运行没有前端接收方，用一个丢弃事件的 Channel。
    let sink: Channel<agent::TurnEvent> = Channel::new(|_| Ok(()));
    let res = create_conversation(
        conns, convos, runs, conv_id,
        task.conn_id.clone(), task.site_slug.clone(), task.site_name.clone(),
        task.task_type.clone(), task.brain.clone(), task.model.clone(), task.prompt.clone(), sink,
    )
    .await;

    let now = now_secs();
    let ok = res.is_ok();
    let _ = tstore.mutate(&task.id, |x| {
        x.last_run = now;
        x.runs += 1;
        match &res {
            Ok(c) => {
                x.last_status = "ok".into();
                x.last_conv_id = c.id.clone();
                x.last_summary = last_snippet(c);
            }
            Err(e) => {
                x.last_status = "error".into();
                x.last_summary = e.chars().take(160).collect();
            }
        }
    });
    let body = match &res {
        Ok(_) => format!("{} · 已完成一次自动运行", task.title),
        Err(e) => format!("{} · 失败：{}", task.title, e.chars().take(80).collect::<String>()),
    };
    let _ = app
        .notification()
        .builder()
        .title(if ok { "定时任务完成" } else { "定时任务失败" })
        .body(body)
        .show();
}

fn last_snippet(c: &Conversation) -> String {
    c.messages
        .iter()
        .rev()
        .find(|m| m.role == "assistant")
        .map(|m| m.text.chars().take(120).collect())
        .unwrap_or_default()
}

/// 托盘常驻调度器：每 30s 检查到点的启用任务，开新对话跑一轮（不重复触发同一任务）。
fn spawn_scheduler(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        loop {
            tokio::time::sleep(Duration::from_secs(30)).await;
            let now = now_secs();
            let state = app.state::<AppState>();
            let running = state.firing.clone();
            let due: Vec<ScheduledTask> = state
                .tasks
                .list()
                .into_iter()
                .filter(|t| t.enabled && t.next_run <= now && t.next_run > 0)
                .filter(|t| !running.lock().unwrap().contains(&t.id))
                .collect();
            for t in due {
                running.lock().unwrap().insert(t.id.clone());
                // 立刻推进 next_run，避免下一 tick 重复触发（并跳过错过的窗口）。
                let mut adv = t.clone();
                adv.advance_past(now);
                let next = adv.next_run;
                let _ = state.tasks.mutate(&t.id, |x| x.next_run = next);

                let app2 = app.clone();
                let conns = state.conns.clone();
                let convos = state.convos.clone();
                let runs = state.runs.clone();
                let tstore = state.tasks.clone();
                let running2 = running.clone();
                let tid = t.id.clone();
                tauri::async_runtime::spawn(async move {
                    fire_task(app2, conns, convos, runs, tstore, t).await;
                    running2.lock().unwrap().remove(&tid);
                });
            }
        }
    });
}

fn user_msg(text: String, now: u64) -> Message {
    Message { role: "user".into(), text, tools: vec![], ts: now, hidden: false, error: false, proposal: None }
}
fn assistant_msg(res: &agent::TurnResult, now: u64) -> Message {
    Message {
        role: "assistant".into(),
        text: if res.text.trim().is_empty() && !res.ok { res.error.clone() } else { res.text.clone() },
        tools: res.tools.clone(),
        ts: now,
        hidden: false,
        error: !res.ok,
        proposal: res.proposal.clone(),
    }
}

/// 开新会话的共享实现（命令与定时任务调度器都用它）。stores 传入克隆，便于后台任务持有。
/// conv_id 由调用方生成；session_ref 首轮先留空，跑完才回填（崩溃留空→续对话给「已失效」）。
#[allow(clippy::too_many_arguments)]
async fn create_conversation(
    conns: pack::ConnStore,
    convos: convo::ConvStore,
    runs: agent::RunRegistry,
    conv_id: String,
    conn_id: String,
    site_slug: String,
    site_name: String,
    task_type: String,
    brain: String,
    model: String,
    message: String,
    on_event: Channel<agent::TurnEvent>,
) -> Result<Conversation, String> {
    let conn = conns.get(&conn_id)?;
    let now = now_secs();
    let session_seed = if brain == "codex" {
        String::new()
    } else {
        uuid::Uuid::new_v4().to_string()
    };
    let sys = agent::system_prompt(&task_type, site_slug.trim(), &site_name);
    let conv = Conversation {
        id: conv_id.clone(),
        conn_id: conn.id.clone(),
        conn_name: conn.name.clone(),
        site_slug: site_slug.trim().to_string(),
        site_name,
        task_type,
        brain: brain.clone(),
        model: model.clone(),
        session_ref: String::new(),
        title: title_from(&message),
        messages: vec![user_msg(message.trim().to_string(), now)],
        status: "running".into(),
        created_at: now,
        updated_at: now,
    };
    convos.upsert(conv)?;

    let res = agent::run_turn(
        runs, conn, brain, model, session_seed, true, Some(sys),
        message.trim().to_string(), conv_id.clone(), on_event,
    )
    .await;

    let now2 = now_secs();
    let updated = convos.mutate(&conv_id, now2, |c| {
        if !res.session_ref.is_empty() {
            c.session_ref = res.session_ref.clone();
        }
        c.status = "idle".into();
        c.messages.push(assistant_msg(&res, now2));
    })?;
    updated.ok_or_else(|| "会话丢失".into())
}

/// 开新会话（前端命令）：conv_id 前端生成，从首轮就能流式渲染 + 停止。
#[allow(clippy::too_many_arguments)]
#[tauri::command]
async fn start_conversation(
    state: tauri::State<'_, AppState>,
    conv_id: String,
    conn_id: String,
    site_slug: String,
    site_name: String,
    task_type: String,
    brain: String,
    model: String,
    message: String,
    on_event: Channel<agent::TurnEvent>,
) -> Result<Conversation, String> {
    if conv_id.trim().is_empty() {
        return Err("会话 id 缺失".into());
    }
    if site_slug.trim().is_empty() {
        return Err("站点不能为空".into());
    }
    if message.trim().is_empty() {
        return Err("请先说点什么".into());
    }
    create_conversation(
        state.conns.clone(), state.convos.clone(), state.runs.clone(),
        conv_id, conn_id, site_slug, site_name, task_type, brain, model, message, on_event,
    )
    .await
}

/// 续对话：resume 同一会话跑一轮。
#[tauri::command]
async fn send_message(
    state: tauri::State<'_, AppState>,
    conv_id: String,
    message: String,
    on_event: Channel<agent::TurnEvent>,
) -> Result<Conversation, String> {
    if message.trim().is_empty() {
        return Err("消息不能为空".into());
    }
    let conv = state.convos.get(&conv_id).ok_or("会话不存在")?;
    let now = now_secs();
    // 原子开始：锁内检查 running/session 并追加用户消息，杜绝并发起两轮。
    match state
        .convos
        .begin_turn(&conv_id, now, user_msg(message.trim().to_string(), now))
    {
        convo::TurnStart::NotFound => return Err("会话不存在".into()),
        convo::TurnStart::Busy => return Err("上一轮还在进行中，请稍候".into()),
        convo::TurnStart::NoSession => {
            return Err("这个会话已失效（首轮未建立），请新建对话".into())
        }
        convo::TurnStart::Started => {}
    }
    let conn = state.conns.get(&conv.conn_id)?;

    let res = agent::run_turn(
        state.runs.clone(),
        conn,
        conv.brain.clone(),
        conv.model.clone(),
        conv.session_ref.clone(),
        false,
        None,
        message.trim().to_string(),
        conv_id.clone(),
        on_event,
    )
    .await;

    let now2 = now_secs();
    let updated = state.convos.mutate(&conv_id, now2, |c| {
        c.status = "idle".into();
        c.messages.push(assistant_msg(&res, now2));
    })?;
    updated.ok_or_else(|| "会话丢失".into())
}

fn title_from(message: &str) -> String {
    let t: String = message.trim().chars().take(30).collect();
    if t.is_empty() {
        "新对话".into()
    } else {
        t
    }
}

/// 打开 Terminal 跑对应 CLI 的登录命令（.command 以 zsh 登录 shell 执行，PATH 完整；
/// claude auth login / codex login 会自动拉起浏览器完成授权）。
#[tauri::command]
fn open_brain_login(app: tauri::AppHandle, brain: String) -> Result<(), String> {
    let (login_cmd, status_check) = match brain.as_str() {
        "claude" => (
            "claude auth login --claudeai",
            r#"claude auth status --json 2>/dev/null | grep -q '"loggedIn": true'"#,
        ),
        "codex" => (
            "codex login",
            "codex login status 2>/dev/null | grep -qi 'logged in'",
        ),
        other => return Err(format!("未知的执行引擎: {other}")),
    };
    let dir = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("login");
    std::fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
    let file = dir.join(format!("{brain}-login.command"));
    let script = format!(
        r#"#!/bin/zsh -il
clear
echo "gcms Pilot · {brain} 授权登录"
echo "浏览器会自动打开，完成登录后【先回到这个窗口】等它打出结果，再关闭。"
echo
if [ -z "$HTTPS_PROXY$https_proxy" ]; then
  _sys_proxy=$(scutil --proxy | awk '/HTTPSEnable : 1/{{e=1}} /HTTPSProxy/{{h=$3}} /HTTPSPort/{{p=$3}} END{{if(e&&h&&p) print h":"p}}')
  if [ -n "$_sys_proxy" ]; then
    export HTTPS_PROXY="http://$_sys_proxy" HTTP_PROXY="http://$_sys_proxy" ALL_PROXY="http://$_sys_proxy"
    export NO_PROXY="localhost,127.0.0.1,::1,.local"
  fi
fi
if [ -n "$HTTPS_PROXY$https_proxy" ]; then
  echo "使用代理: ${{HTTPS_PROXY:-$https_proxy}}"
  echo
fi
{login_cmd}
echo
if {status_check}; then
  echo "✅ 登录成功！现在可以关闭这个窗口，gcms Pilot 里的状态灯会自动变绿。"
else
  echo "❌ 登录还没完成。请截图上面的输出，或重新在 Pilot 里点「去授权」再试一次。"
fi
echo
read -s -k 1 "?按任意键关闭这个窗口…"
"#
    );
    std::fs::write(&file, script).map_err(|e| e.to_string())?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o755))
            .map_err(|e| e.to_string())?;
    }
    std::process::Command::new("open")
        .arg(&file)
        .spawn()
        .map_err(|e| format!("打开终端失败: {e}"))?;
    Ok(())
}

fn setup_tray(app: &tauri::AppHandle) -> tauri::Result<()> {
    let show = MenuItem::with_id(app, "show", "显示主窗口", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "退出 gcms Pilot", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &quit])?;
    let mut builder = TrayIconBuilder::new()
        .menu(&menu)
        .tooltip("gcms Pilot")
        .on_menu_event(|app, event| match event.id.as_ref() {
            "show" => show_main(app),
            "quit" => app.exit(0),
            _ => {}
        });
    if let Some(icon) = app.default_window_icon().cloned() {
        builder = builder.icon(icon);
    }
    builder.build(app)?;
    Ok(())
}

fn show_main(app: &tauri::AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.set_focus();
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    // GUI 进程的 PATH / 代理是裸的，必须最先修复，否则找不到 claude/node 且直连被墙。
    path_env::fix();

    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_process::init())
        .setup(|app| {
            let data_dir = app.path().app_data_dir()?;
            std::fs::create_dir_all(&data_dir)?;
            let conns = pack::ConnStore::new(&data_dir).map_err(std::io::Error::other)?;
            let convos = convo::ConvStore::new(&data_dir);
            convos.mark_idle(now_secs());
            let task_store = tasks::TaskStore::new(&data_dir);
            app.manage(AppState {
                conns,
                convos,
                tasks: task_store,
                runs: agent::RunRegistry::default(),
                firing: Arc::new(Mutex::new(HashSet::new())),
            });
            setup_tray(app.handle())?;
            spawn_scheduler(app.handle().clone());
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                if window.label() == "main" {
                    api.prevent_close();
                    let _ = window.hide();
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            list_connections,
            import_pack,
            remove_connection,
            discover_sites,
            detect_brains,
            list_scheduled,
            open_brain_login,
            list_conversations,
            get_conversation,
            delete_conversation,
            start_conversation,
            send_message,
            cancel_turn,
            set_conversation_model,
            list_tasks,
            save_task,
            delete_task,
            set_task_enabled,
            run_task_now,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
