mod agent;
mod brains;
mod cf;
mod cf_templates;
mod convo;
mod discovery;
mod keychain;
mod pack;
mod path_env;
mod permit;
mod scheduled;
mod tasks;

use std::collections::HashSet;
use std::path::PathBuf;
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
    /// 应用数据目录（权限钩子资产 + 待批请求落在 <data_dir>/permit 下）。
    data_dir: PathBuf,
    /// 当前运行的本地预览（wrangler dev）进程；一次只跑一个。
    preview: Arc<Mutex<Option<PreviewHandle>>>,
}

fn now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// 项目 slug：ASCII 小写字母/数字/连字符（CF 建站的 site_slug 复用为项目名）。
fn cf_project_slug(s: &str) -> String {
    let slug: String = s
        .trim()
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c.to_ascii_lowercase() } else { '-' })
        .collect();
    let slug = slug.trim_matches('-').to_string();
    if slug.is_empty() { "site".into() } else { slug }
}

/// 附件文件名消毒：只留安全字符，去路径，限长。
fn sanitize_filename(name: &str) -> String {
    let base = std::path::Path::new(name)
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("file");
    let cleaned: String = base
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() || c == '.' || c == '-' || c == '_' { c } else { '_' })
        .collect();
    let cleaned: String = cleaned.trim_matches('.').chars().take(80).collect();
    if cleaned.is_empty() { "file".into() } else { cleaned }
}

/// 本轮 cwd：CF 连接＝该连接工作区下的项目目录（按需创建）；gcms＝技能包目录（返回空由 run_turn 兜底）。
fn resolve_work_dir(conn: &pack::Connection, site_slug: &str) -> Result<String, String> {
    if conn.kind == "cloudflare" {
        let dir = std::path::Path::new(&conn.skill_dir)
            .join("projects")
            .join(cf_project_slug(site_slug));
        std::fs::create_dir_all(&dir).map_err(|e| format!("建项目目录失败: {e}"))?;
        Ok(dir.to_string_lossy().into_owned())
    } else {
        Ok(String::new())
    }
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
    let convos = state.convos.clone();
    tauri::async_runtime::spawn_blocking(move || {
        store.remove(&id)?; // 先删连接本体（钥匙串 + 技能目录）
        convos.remove_by_conn_id(&id) // 再级联删该连接下的所有对话
    })
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

/// 验证 Cloudflare API Token 并返回可用账号 + 域名（UI 据此让用户选默认账号）。
#[tauri::command]
async fn verify_cf_token(token: String) -> Result<cf::CfVerify, String> {
    cf::verify_token(&token).await
}

/// 连接 Cloudflare：再验证一遍 token（防被吊销）→ 存钥匙串 + 建工作区 + 写 connections.json。
#[tauri::command]
async fn connect_cloudflare(
    state: tauri::State<'_, AppState>,
    name: String,
    token: String,
    account_id: String,
) -> Result<pack::Connection, String> {
    cf::verify_token(&token).await?;
    let store = state.conns.clone();
    tauri::async_runtime::spawn_blocking(move || store.add_cloudflare(&name, &token, &account_id))
        .await
        .map_err(|e| e.to_string())?
}

/// 把粘贴/拖拽的文件存进当前会话的工作目录 uploads/ 下，返回相对路径（AI 可直接读取）。
/// CF 会话＝项目目录；gcms 会话＝技能包目录。
#[tauri::command]
fn save_attachment(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    project: String,
    filename: String,
    data: Vec<u8>,
) -> Result<String, String> {
    if data.len() > 25_000_000 {
        return Err("文件太大（上限 25MB）".into());
    }
    let conn = state.conns.get(&conn_id)?;
    let dir = {
        let w = resolve_work_dir(&conn, &project)?;
        if w.is_empty() { conn.skill_dir.clone() } else { w }
    };
    if dir.is_empty() {
        return Err("没有可用的工作目录".into());
    }
    let up = std::path::Path::new(&dir).join("uploads");
    std::fs::create_dir_all(&up).map_err(|e| format!("建 uploads 目录失败: {e}"))?;
    let safe = sanitize_filename(&filename);
    let mut target = up.join(&safe);
    if target.exists() {
        let p = std::path::Path::new(&safe);
        let stem = p.file_stem().and_then(|s| s.to_str()).unwrap_or("file");
        let ext = p
            .extension()
            .and_then(|s| s.to_str())
            .map(|e| format!(".{e}"))
            .unwrap_or_default();
        for i in 1..1000 {
            let cand = up.join(format!("{stem}-{i}{ext}"));
            if !cand.exists() {
                target = cand;
                break;
            }
        }
    }
    std::fs::write(&target, &data).map_err(|e| format!("保存附件失败: {e}"))?;
    let name = target.file_name().and_then(|n| n.to_str()).unwrap_or(&safe).to_string();
    Ok(format!("uploads/{name}"))
}

/// 列出某 CF 连接工作区下已有的站点项目（<workspace>/projects/* 目录名）。
#[tauri::command]
fn list_cf_projects(state: tauri::State<'_, AppState>, conn_id: String) -> Vec<String> {
    let Ok(conn) = state.conns.get(&conn_id) else { return vec![] };
    if conn.kind != "cloudflare" {
        return vec![];
    }
    let dir = std::path::Path::new(&conn.skill_dir).join("projects");
    let Ok(rd) = std::fs::read_dir(&dir) else { return vec![] };
    let mut v: Vec<String> = rd
        .flatten()
        .filter(|e| e.path().is_dir())
        .filter_map(|e| e.file_name().to_str().map(|s| s.to_string()))
        .collect();
    v.sort();
    v
}

/// 项目是否已经建出可预览/部署的内容（有 index.html / *.html / package.json）。
/// 前端据此在"还只给了方案、没写文件"时把预览/部署置灰，避免点太早。
#[tauri::command]
fn cf_project_ready(state: tauri::State<'_, AppState>, conn_id: String, project: String) -> bool {
    let Ok(conn) = state.conns.get(&conn_id) else { return false };
    if conn.kind != "cloudflare" {
        return false;
    }
    let dir = std::path::Path::new(&conn.skill_dir)
        .join("projects")
        .join(cf_project_slug(&project));
    fn has_site(dir: &std::path::Path, depth: u8) -> bool {
        let Ok(rd) = std::fs::read_dir(dir) else { return false };
        for e in rd.flatten() {
            let name = e.file_name().to_string_lossy().into_owned();
            let p = e.path();
            if p.is_file() {
                if name == "index.html" || name == "package.json" || name.ends_with(".html") {
                    return true;
                }
            } else if depth < 3 && p.is_dir() && !name.starts_with('.') && name != "node_modules" {
                if has_site(&p, depth + 1) {
                    return true;
                }
            }
        }
        false
    }
    has_site(&dir, 0)
}

// ---- 本地预览（wrangler pages dev + 预览窗口）----

struct PreviewHandle {
    child: std::process::Child,
    #[allow(dead_code)]
    url: String,
    #[allow(dead_code)]
    project: String,
}

const PREVIEW_DEFAULT_PORT: u16 = 8788;

/// pilot.json → (dev 命令, 产物目录, 预览端口)。缺省 dev=None、out=""、port=8788。
/// 自定义 dev 命令监听的端口必须在 pilot.json 的 "port" 里写对，否则预览窗打不开。
fn read_pilot_json(dir: &std::path::Path) -> (Option<String>, String, u16) {
    let Ok(raw) = std::fs::read(dir.join("pilot.json")) else {
        return (None, String::new(), PREVIEW_DEFAULT_PORT);
    };
    let Ok(v) = serde_json::from_slice::<serde_json::Value>(&raw) else {
        return (None, String::new(), PREVIEW_DEFAULT_PORT);
    };
    let dev = v
        .get("dev")
        .and_then(|x| x.as_str())
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(|s| s.to_string());
    let out = v.get("out").and_then(|x| x.as_str()).unwrap_or("").trim().to_string();
    let port = v
        .get("port")
        .and_then(|x| x.as_u64())
        .filter(|p| *p >= 1 && *p <= 65535)
        .map(|p| p as u16)
        .unwrap_or(PREVIEW_DEFAULT_PORT);
    (dev, out, port)
}

/// 杀掉当前预览进程树并回收（避免僵尸）。
fn stop_preview(state: &AppState) {
    if let Some(mut h) = state.preview.lock().unwrap().take() {
        let pid = h.child.id();
        #[cfg(unix)]
        {
            let _ = std::process::Command::new("kill")
                .args(["-9", &format!("-{pid}")])
                .status();
        }
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            let _ = std::process::Command::new("taskkill")
                .args(["/T", "/F", "/PID", &pid.to_string()])
                .creation_flags(0x0800_0000)
                .status();
        }
        let _ = h.child.kill();
        let _ = h.child.wait();
    }
}

fn open_preview_window(app: &AppHandle, url: &str) -> Result<(), String> {
    if let Some(w) = app.get_webview_window("preview") {
        let _ = w.eval(&format!("window.location.replace('{url}')"));
        let _ = w.show();
        let _ = w.set_focus();
        return Ok(());
    }
    let parsed = url.parse().map_err(|e| format!("预览地址解析失败: {e}"))?;
    tauri::WebviewWindowBuilder::new(app, "preview", tauri::WebviewUrl::External(parsed))
        .title("本地预览")
        .inner_size(1024.0, 728.0)
        .build()
        .map_err(|e| format!("打开预览窗口失败: {e}"))?;
    Ok(())
}

/// 启动本地预览：在项目目录里跑 dev（pilot.json 的 dev，或 `wrangler pages dev`），开预览窗。
#[tauri::command]
async fn cf_preview_start(
    app: AppHandle,
    state: tauri::State<'_, AppState>,
    conn_id: String,
    project: String,
) -> Result<String, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "cloudflare" {
        return Err("这个连接不是 Cloudflare".into());
    }
    let token = keychain::get_key(&conn.id)?;
    let dir = resolve_work_dir(&conn, &project)?;
    let (dev_cmd, out, port) = read_pilot_json(std::path::Path::new(&dir));
    stop_preview(&state); // 一次只跑一个预览
    let shell_cmd = match dev_cmd {
        Some(d) => d,
        None => format!(
            "wrangler pages dev {} --ip 127.0.0.1 --port {}",
            if out.is_empty() { ".".to_string() } else { out },
            port
        ),
    };
    #[cfg(windows)]
    let mut c = {
        let mut c = std::process::Command::new("cmd");
        c.arg("/C").arg(&shell_cmd);
        c
    };
    #[cfg(not(windows))]
    let mut c = {
        let mut c = std::process::Command::new("sh");
        c.arg("-c").arg(&shell_cmd);
        c
    };
    c.current_dir(&dir).env("CLOUDFLARE_API_TOKEN", &token);
    if !conn.account_id.is_empty() {
        c.env("CLOUDFLARE_ACCOUNT_ID", &conn.account_id);
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        c.process_group(0); // 整组，停预览时连 node 子进程一起杀
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000);
    }
    let child = c.spawn().map_err(|e| format!("启动预览失败（确认已装 wrangler）: {e}"))?;
    let url = format!("http://127.0.0.1:{port}");
    *state.preview.lock().unwrap() = Some(PreviewHandle {
        child,
        url: url.clone(),
        project,
    });
    // dev server 起来要一两秒，稍等再开窗（起不来窗里刷新即可）。
    tokio::time::sleep(Duration::from_millis(2500)).await;
    open_preview_window(&app, &url)?;
    Ok(url)
}

#[tauri::command]
fn cf_preview_stop(app: AppHandle, state: tauri::State<'_, AppState>) -> Result<(), String> {
    stop_preview(&state);
    if let Some(w) = app.get_webview_window("preview") {
        let _ = w.close();
    }
    Ok(())
}

/// 预览一个模板：在模板目录里跑 wrangler pages dev + 开预览窗，看它真实的样子。
#[tauri::command]
async fn cf_preview_template(
    app: AppHandle,
    state: tauri::State<'_, AppState>,
    slug: String,
) -> Result<String, String> {
    if slug.is_empty() || !slug.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_') {
        return Err("非法模板名".into());
    }
    let dir = state.data_dir.join("templates").join(&slug);
    if !dir.is_dir() {
        return Err("模板不存在".into());
    }
    let (dev_cmd, out, port) = read_pilot_json(&dir);
    // 静态预览不一定需要 token；有 CF 连接就顺手带上（模板含绑定时用得到）。
    let token = state
        .conns
        .list()
        .iter()
        .find(|c| c.kind == "cloudflare")
        .and_then(|c| keychain::get_key(&c.id).ok())
        .unwrap_or_default();
    stop_preview(&state);
    let shell_cmd = match dev_cmd {
        Some(d) => d,
        None => format!(
            "wrangler pages dev {} --ip 127.0.0.1 --port {}",
            if out.is_empty() { ".".to_string() } else { out },
            port
        ),
    };
    #[cfg(windows)]
    let mut c = {
        let mut c = std::process::Command::new("cmd");
        c.arg("/C").arg(&shell_cmd);
        c
    };
    #[cfg(not(windows))]
    let mut c = {
        let mut c = std::process::Command::new("sh");
        c.arg("-c").arg(&shell_cmd);
        c
    };
    c.current_dir(&dir);
    if !token.is_empty() {
        c.env("CLOUDFLARE_API_TOKEN", &token);
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        c.process_group(0);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000);
    }
    let child = c.spawn().map_err(|e| format!("启动预览失败（确认已装 wrangler）: {e}"))?;
    let url = format!("http://127.0.0.1:{port}");
    *state.preview.lock().unwrap() = Some(PreviewHandle {
        child,
        url: url.clone(),
        project: format!("template:{slug}"),
    });
    tokio::time::sleep(Duration::from_millis(2500)).await;
    open_preview_window(&app, &url)?;
    Ok(url)
}

// ---- 模板库 ----

#[tauri::command]
fn list_templates(state: tauri::State<'_, AppState>) -> Vec<cf_templates::Template> {
    cf_templates::list(&state.data_dir.join("templates"))
}

/// 读模板入口 HTML（模板卡用 iframe srcdoc 展示真实样子）。找不到返回空串；过大截断。
#[tauri::command]
fn template_index_html(state: tauri::State<'_, AppState>, slug: String) -> String {
    if slug.is_empty() || !slug.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_') {
        return String::new();
    }
    let dir = state.data_dir.join("templates").join(&slug);
    let mut path = dir.join("index.html");
    if !path.is_file() {
        if let Ok(rd) = std::fs::read_dir(&dir) {
            if let Some(h) = rd
                .flatten()
                .map(|e| e.path())
                .find(|p| p.extension().and_then(|x| x.to_str()) == Some("html"))
            {
                path = h;
            }
        }
    }
    match std::fs::read_to_string(&path) {
        Ok(s) if s.len() <= 900_000 => s,
        Ok(s) => s.chars().take(400_000).collect(),
        Err(_) => String::new(),
    }
}

/// 把某 CF 项目存成模板（沉淀）。
#[tauri::command]
async fn save_as_template(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    project: String,
    name: String,
    desc: String,
) -> Result<cf_templates::Template, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "cloudflare" {
        return Err("只有 Cloudflare 站点项目能存为模板".into());
    }
    let src = resolve_work_dir(&conn, &project)?;
    let tdir = state.data_dir.join("templates");
    tauri::async_runtime::spawn_blocking(move || {
        cf_templates::save(&tdir, &name, &desc, std::path::Path::new(&src))
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
fn delete_template(state: tauri::State<'_, AppState>, slug: String) -> Result<(), String> {
    cf_templates::delete(&state.data_dir.join("templates"), &slug)
}

/// 引用模板到某 CF 连接下的新项目（拷贝模板文件，之后在对话里定制）。
#[tauri::command]
async fn use_template(
    state: tauri::State<'_, AppState>,
    slug: String,
    conn_id: String,
    project: String,
) -> Result<(), String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "cloudflare" {
        return Err("请先切到一个 Cloudflare 连接再用模板建站".into());
    }
    let dest = resolve_work_dir(&conn, &project)?;
    let tdir = state.data_dir.join("templates");
    tauri::async_runtime::spawn_blocking(move || {
        cf_templates::instantiate(&tdir, &slug, std::path::Path::new(&dest))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// 手动「重新检测」：先重新导入登录 shell 的 PATH（装完 CLI 后新增的目录也能认到），再探测。
/// 自动轮询仍用轻量的 detect_brains（不每 6s 起登录 shell）。
#[tauri::command]
async fn redetect_brains() -> Result<brains::BrainsInfo, String> {
    tauri::async_runtime::spawn_blocking(path_env::fix)
        .await
        .map_err(|e| e.to_string())?;
    Ok(brains::detect().await)
}

/// 一键安装 wrangler（npm i -g wrangler）。npm 走已修复的 PATH。可能耗时半分钟到一两分钟。
#[tauri::command]
async fn install_wrangler() -> Result<String, String> {
    tauri::async_runtime::spawn_blocking(|| {
        let mut c = std::process::Command::new("npm");
        c.args(["install", "-g", "wrangler"]);
        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            c.creation_flags(0x0800_0000);
        }
        let out = c
            .output()
            .map_err(|e| format!("运行 npm 失败（确认已装 Node/npm）: {e}"))?;
        if out.status.success() {
            Ok("wrangler 安装完成".to_string())
        } else {
            let err = String::from_utf8_lossy(&out.stderr);
            let last: String = err
                .lines()
                .rev()
                .find(|l| !l.trim().is_empty())
                .unwrap_or("未知错误")
                .chars()
                .take(200)
                .collect();
            Err(format!("安装失败：{last}"))
        }
    })
    .await
    .map_err(|e| e.to_string())?
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

/// 会话进行中切换权限档位（plan/ask/auto/full）：仅改存储，下一轮 send_message 读到即用。
/// 注意 claude 把权限档位钉在会话创建时，改基座模式要新对话才全生效；但钩子每轮重生成，
/// 故「自动/询问」的拦截逻辑仍随时可调（详见 permit.rs）。
#[tauri::command]
fn set_conversation_perm_mode(
    state: tauri::State<'_, AppState>,
    conv_id: String,
    perm_mode: String,
) -> Result<Option<Conversation>, String> {
    state
        .convos
        .mutate(&conv_id, now_secs(), move |c| c.perm_mode = perm_mode)
}

/// 列出所有等待用户批准的工具调用（钩子写在 <data_dir>/permit/pending）。UI 轮询它渲染批准卡。
#[tauri::command]
fn list_pending_permits(state: tauri::State<'_, AppState>) -> Vec<permit::PendingPermit> {
    permit::list_pending(&state.data_dir.join("permit").join("pending"))
}

/// 应答一条待批请求：allow=放行 / 否则拒绝。钩子轮询到响应文件后据此放行或拦截。
#[tauri::command]
fn respond_permit(
    state: tauri::State<'_, AppState>,
    id: String,
    allow: bool,
) -> Result<(), String> {
    permit::respond(&state.data_dir.join("permit").join("pending"), &id, allow)
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
    let data_dir = state.data_dir.clone();
    tauri::async_runtime::spawn(async move {
        fire_task(app, conns, convos, runs, tstore, task, data_dir).await;
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
    data_dir: PathBuf,
) {
    let conv_id = uuid::Uuid::new_v4().to_string();
    // 后台运行没有前端接收方，用一个丢弃事件的 Channel。
    let sink: Channel<agent::TurnEvent> = Channel::new(|_| Ok(()));
    // 定时任务无人值守：只能全自动（询问/自动档会卡在等批准，永远回不来）。
    let res = create_conversation(
        conns, convos, runs, conv_id,
        task.conn_id.clone(), task.site_slug.clone(), task.site_name.clone(),
        task.task_type.clone(), task.brain.clone(), task.model.clone(),
        "full".into(), task.prompt.clone(), sink, data_dir,
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
                let data_dir = state.data_dir.clone();
                tauri::async_runtime::spawn(async move {
                    fire_task(app2, conns, convos, runs, tstore, t, data_dir).await;
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
        // 只有「失败且没产出内容」才标红（展示错误信息）；有正常回答就不标红——
        // CLI 有时退出码非 0 但回答是好的（如跑了 open 打开浏览器），不该把整段染红。
        error: !res.ok && res.text.trim().is_empty(),
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
    perm_mode: String,
    message: String,
    on_event: Channel<agent::TurnEvent>,
    data_dir: PathBuf,
) -> Result<Conversation, String> {
    let conn = conns.get(&conn_id)?;
    let now = now_secs();
    let session_seed = if brain == "codex" {
        String::new()
    } else {
        uuid::Uuid::new_v4().to_string()
    };
    let work_dir = resolve_work_dir(&conn, site_slug.trim())?;
    let sys = if conn.kind == "cloudflare" {
        agent::cf_system_prompt(&cf_project_slug(site_slug.trim()), &conn.account_id)
    } else {
        agent::system_prompt(&task_type, site_slug.trim(), &site_name)
    };
    let conv = Conversation {
        id: conv_id.clone(),
        conn_id: conn.id.clone(),
        conn_name: conn.name.clone(),
        site_slug: site_slug.trim().to_string(),
        site_name,
        task_type,
        brain: brain.clone(),
        model: model.clone(),
        perm_mode: perm_mode.clone(),
        session_ref: String::new(),
        title: title_from(&message),
        messages: vec![user_msg(message.trim().to_string(), now)],
        status: "running".into(),
        created_at: now,
        updated_at: now,
    };
    convos.upsert(conv)?;

    let res = agent::run_turn(
        runs, conn, work_dir, brain, model, perm_mode, data_dir.join("permit"), session_seed, true, Some(sys),
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
    perm_mode: String,
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
        conv_id, conn_id, site_slug, site_name, task_type, brain, model, perm_mode, message, on_event,
        state.data_dir.clone(),
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
    let work_dir = resolve_work_dir(&conn, &conv.site_slug)?;

    let res = agent::run_turn(
        state.runs.clone(),
        conn,
        work_dir,
        conv.brain.clone(),
        conv.model.clone(),
        conv.perm_mode.clone(),
        state.data_dir.join("permit"),
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

/// 重试上一轮：去掉失败/部分的助手消息，用最后一条用户消息 resume 再跑一轮（不新增用户消息）。
#[tauri::command]
async fn retry_turn(
    state: tauri::State<'_, AppState>,
    conv_id: String,
    on_event: Channel<agent::TurnEvent>,
) -> Result<Conversation, String> {
    let now = now_secs();
    let message = state.convos.begin_retry(&conv_id, now)?;
    let conv = state.convos.get(&conv_id).ok_or("会话不存在")?;
    let conn = state.conns.get(&conv.conn_id)?;
    let work_dir = resolve_work_dir(&conn, &conv.site_slug)?;

    let res = agent::run_turn(
        state.runs.clone(),
        conn,
        work_dir,
        conv.brain.clone(),
        conv.model.clone(),
        conv.perm_mode.clone(),
        state.data_dir.join("permit"),
        conv.session_ref.clone(),
        false,
        None,
        message,
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
    let login_cmd = match brain.as_str() {
        "claude" => "claude auth login --claudeai",
        "codex" => "codex login",
        other => return Err(format!("未知的执行引擎: {other}")),
    };
    // 登录成功标记：claude 读 --json 的 loggedIn=true，codex 读输出里的 "logged in"。
    let (status_cmd, marker) = match brain.as_str() {
        "claude" => ("claude auth status --json", r#""loggedIn": true"#),
        _ => ("codex login status", "logged in"),
    };
    let dir = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("login");
    std::fs::create_dir_all(&dir).map_err(|e| e.to_string())?;

    #[cfg(target_os = "macos")]
    {
        let file = dir.join(format!("{brain}-login.command"));
        let script = format!(
            r#"#!/bin/zsh -il
clear
echo "GCMS Pilot · {brain} 授权登录"
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
if {status_cmd} 2>/dev/null | grep -qi '{marker}'; then
  echo "✅ 登录成功！现在可以关闭这个窗口，GCMS Pilot 里的状态灯会自动变绿。"
else
  echo "❌ 登录还没完成。请截图上面的输出，或重新在 Pilot 里点「去授权」再试一次。"
fi
echo
read -s -k 1 "?按任意键关闭这个窗口…"
"#
        );
        std::fs::write(&file, script).map_err(|e| e.to_string())?;
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o755))
                .map_err(|e| e.to_string())?;
        }
        std::process::Command::new("open")
            .arg(&file)
            .spawn()
            .map_err(|e| format!("打开终端失败: {e}"))?;
    }

    #[cfg(target_os = "windows")]
    {
        // PowerShell：UTF-8 输出 + 跑登录命令 + 状态自查。系统代理由 CLI / 环境变量自行处理。
        let file = dir.join(format!("{brain}-login.ps1"));
        let script = format!(
            "$OutputEncoding = [Console]::OutputEncoding = [Text.Encoding]::UTF8\r\n\
             Write-Host 'GCMS Pilot · {brain} 授权登录'\r\n\
             Write-Host '浏览器会自动打开，完成登录后先回到这个窗口等它打出结果，再关闭。'\r\n\
             Write-Host ''\r\n\
             {login_cmd}\r\n\
             Write-Host ''\r\n\
             if ({status_cmd} 2>$null | Select-String -SimpleMatch '{marker}') {{\r\n\
             Write-Host '[OK] 登录成功！现在可以关闭这个窗口，GCMS Pilot 里的状态灯会自动变绿。'\r\n\
             }} else {{\r\n\
             Write-Host '[X] 登录还没完成。请截图上面的输出，或重新在 Pilot 里点「去授权」再试一次。'\r\n\
             }}\r\n\
             Read-Host '按回车关闭这个窗口'\r\n"
        );
        std::fs::write(&file, script).map_err(|e| e.to_string())?;
        // 用 cmd start 拉起一个可见的 PowerShell 窗口跑这个临时脚本。
        std::process::Command::new("cmd")
            .args([
                "/c", "start", "", "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
            ])
            .arg(&file)
            .spawn()
            .map_err(|e| format!("打开终端失败: {e}"))?;
    }

    #[cfg(not(any(target_os = "macos", target_os = "windows")))]
    {
        let _ = (login_cmd, status_cmd, marker);
        return Err("当前平台暂不支持一键授权".to_string());
    }

    Ok(())
}

/// Windows 11：把原生标题栏背景/文字色设成与左栏（rail）一致，消除白色标题栏与米色侧栏的割裂。
/// Win10 不支持该 DWM 属性，调用失败被忽略（标题栏维持系统默认）。
#[cfg(target_os = "windows")]
fn style_titlebar_windows(w: &tauri::WebviewWindow) {
    use windows::Win32::Foundation::COLORREF;
    use windows::Win32::Graphics::Dwm::{
        DwmSetWindowAttribute, DWMWA_CAPTION_COLOR, DWMWA_TEXT_COLOR,
    };
    let Ok(hwnd) = w.hwnd() else { return };
    // COLORREF = 0x00BBGGRR。rail #faf9f7 → 0x00F7F9FA；标题文字 --text #26241f → 0x001F2426。
    let caption = COLORREF(0x00F7F9FA);
    let text = COLORREF(0x001F2426);
    let sz = std::mem::size_of::<COLORREF>() as u32;
    unsafe {
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_CAPTION_COLOR,
            &caption as *const _ as *const core::ffi::c_void,
            sz,
        );
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_TEXT_COLOR,
            &text as *const _ as *const core::ffi::c_void,
            sz,
        );
    }
}

fn setup_tray(app: &tauri::AppHandle) -> tauri::Result<()> {
    let show = MenuItem::with_id(app, "show", "显示主窗口", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "退出 GCMS Pilot", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &quit])?;
    let mut builder = TrayIconBuilder::new()
        .menu(&menu)
        .tooltip("GCMS Pilot")
        .on_menu_event(|app, event| match event.id.as_ref() {
            "show" => show_main(app),
            "quit" => {
                stop_preview(&app.state::<AppState>()); // 退出前杀掉本地预览进程
                app.exit(0);
            }
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
            // 清掉上次进程遗留的待批权限请求（幽灵批准卡）。
            permit::sweep_all(&data_dir.join("permit").join("pending"));
            let task_store = tasks::TaskStore::new(&data_dir);
            app.manage(AppState {
                conns,
                convos,
                tasks: task_store,
                runs: agent::RunRegistry::default(),
                firing: Arc::new(Mutex::new(HashSet::new())),
                data_dir: data_dir.clone(),
                preview: Arc::new(Mutex::new(None)),
            });
            setup_tray(app.handle())?;
            spawn_scheduler(app.handle().clone());
            // 每次启动都把主窗口显示并置前——修复自动更新 relaunch 后 macOS 不激活、窗口看不到（要点 Dock 才出来）。
            show_main(app.handle());
            #[cfg(target_os = "windows")]
            if let Some(w) = app.get_webview_window("main") {
                style_titlebar_windows(&w);
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                if window.label() == "main" {
                    api.prevent_close();
                    let _ = window.hide();
                } else if window.label() == "preview" {
                    // 关预览窗＝停掉 wrangler dev（否则进程 + 端口泄漏），但让窗口正常关。
                    stop_preview(&window.app_handle().state::<AppState>());
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            list_connections,
            import_pack,
            remove_connection,
            discover_sites,
            detect_brains,
            install_wrangler,
            verify_cf_token,
            connect_cloudflare,
            save_attachment,
            list_cf_projects,
            cf_project_ready,
            cf_preview_start,
            cf_preview_stop,
            cf_preview_template,
            list_templates,
            template_index_html,
            save_as_template,
            delete_template,
            use_template,
            redetect_brains,
            list_scheduled,
            open_brain_login,
            list_conversations,
            get_conversation,
            delete_conversation,
            start_conversation,
            send_message,
            retry_turn,
            cancel_turn,
            set_conversation_model,
            set_conversation_perm_mode,
            list_pending_permits,
            respond_permit,
            list_tasks,
            save_task,
            delete_task,
            set_task_enabled,
            run_task_now,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
