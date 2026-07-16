mod acp;
mod agent;
mod brains;
mod bridge;
mod managed;
mod cf;
mod cf_templates;
mod convo;
mod discovery;
mod keychain;
mod limits;
mod node_boot;
mod pack;
mod path_env;
mod permit;
mod scheduled;
mod ssh;
mod tasks;
mod tools;
mod usage;

use std::collections::HashSet;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tauri::ipc::Channel;
use tauri::menu::{Menu, MenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Emitter, Manager, WindowEvent};
use tauri_plugin_notification::NotificationExt;

use convo::{Conversation, Message};
use tasks::ScheduledTask;

struct AppState {
    conns: pack::ConnStore,
    convos: convo::ConvStore,
    tasks: tasks::TaskStore,
    managed: managed::ManagedStore,
    runs: agent::RunRegistry,
    /// 正在跑的定时任务 id（调度器与 run_task_now 共享，防止同一任务被重复触发）。
    firing: Arc<Mutex<HashSet<String>>>,
    /// 应用数据目录（权限钩子资产 + 待批请求落在 <data_dir>/permit 下）。
    data_dir: PathBuf,
    /// 当前运行的本地预览（wrangler dev）进程；一次只跑一个。
    preview: Arc<Mutex<Option<PreviewHandle>>>,
    /// 活跃的 SSH PTY 会话（按 conn_id）。
    ssh: ssh::SshSessions,
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

/// 计算本轮 cwd 但不建目录：CF 连接＝工作区下的项目目录；gcms＝空串（调用方回退技能包目录）。
/// 读路径（如附件缩略图）用它，避免只读操作把用户删掉的项目目录悄悄复活成幽灵项目。
fn work_dir_path(conn: &pack::Connection, site_slug: &str) -> String {
    if conn.kind == "cloudflare" {
        std::path::Path::new(&conn.skill_dir)
            .join("projects")
            .join(cf_project_slug(site_slug))
            .to_string_lossy()
            .into_owned()
    } else {
        String::new()
    }
}

/// 本轮 cwd：CF 连接＝该连接工作区下的项目目录（按需创建）；gcms＝技能包目录（返回空由 run_turn 兜底）。
fn resolve_work_dir(conn: &pack::Connection, site_slug: &str) -> Result<String, String> {
    let dir = work_dir_path(conn, site_slug);
    if !dir.is_empty() {
        std::fs::create_dir_all(&dir).map_err(|e| format!("建项目目录失败: {e}"))?;
    }
    Ok(dir)
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

#[derive(serde::Serialize)]
struct PackUpdateInfo {
    current: String,
    latest: String,
    has_update: bool,
}

/// 查询技能包是否有新版：GET {api_base}/skill-pack/version（密钥鉴权）。
/// 旧连接（pack_version 为空）：技能目录无 PACK_VERSION 标记 ⇒ 必是老包，首次就提示有更新；
/// 有标记则读取落库后正常比较。服务端太老没有该端点（404）或网络失败 → 静默视为无更新（不打扰用户）。
#[tauri::command]
async fn check_pack_update(
    state: tauri::State<'_, AppState>,
    conn_id: String,
) -> Result<PackUpdateInfo, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "gcms" {
        return Ok(PackUpdateInfo { current: String::new(), latest: String::new(), has_update: false });
    }
    let key = keychain::get_key(&conn.id)?;
    let url = format!("{}/skill-pack/version", conn.api_base.trim_end_matches('/'));
    let none = PackUpdateInfo { current: conn.pack_version.clone(), latest: String::new(), has_update: false };
    let resp = match reqwest::Client::new()
        .get(&url)
        .bearer_auth(&key)
        .timeout(std::time::Duration::from_secs(10))
        .send()
        .await
    {
        Ok(r) if r.status().is_success() => r,
        _ => return Ok(none), // 404（旧服务端）/网络失败：静默无更新
    };
    let latest = resp
        .json::<serde_json::Value>()
        .await
        .ok()
        .and_then(|v| v.get("version").and_then(|x| x.as_str()).map(str::to_string))
        .unwrap_or_default();
    if latest.is_empty() {
        return Ok(none);
    }
    if conn.pack_version.is_empty() {
        // 版本未落库。看技能目录里有没有 PACK_VERSION 标记：
        // - 没有 ⇒ 一定是 v1.3.10 之前的老包 ⇒ 相对任何能答版本查询的服务端都算「有更新」，首次就提示；
        // - 有 ⇒ 读标记落库后正常比较（导入/升级时会读，这里只是兜底）。
        let marker = std::fs::read_to_string(std::path::Path::new(&conn.skill_dir).join("PACK_VERSION"))
            .map(|s| s.trim().to_string())
            .unwrap_or_default();
        if marker.is_empty() {
            return Ok(PackUpdateInfo { current: String::new(), latest: latest.clone(), has_update: true });
        }
        let _ = state.conns.set_pack_version(&conn.id, &marker);
        let has = latest != marker;
        return Ok(PackUpdateInfo { current: marker, latest, has_update: has });
    }
    let has = latest != conn.pack_version;
    Ok(PackUpdateInfo { current: conn.pack_version, latest, has_update: has })
}

/// 一键升级技能包：用钥匙串密钥从服务端下载最新原始包，就地覆盖技能目录。
/// 连接 / 密钥 / 对话全保留。
#[tauri::command]
async fn update_pack(state: tauri::State<'_, AppState>, conn_id: String) -> Result<String, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "gcms" {
        return Err("只有 gcms 技能包连接支持升级".into());
    }
    // 后端权威守卫：该连接下有正在跑的对话（含托盘定时任务在后台开的轮——前端快照看不到）
    // 时拒绝升级，防止覆盖正在被 CLI 使用的脚本。前端的检查只是提示层，这里才是闸。
    if state.convos.list().iter().any(|c| c.conn_id == conn_id && c.status == "running") {
        return Err("该连接下有对话正在运行（可能是定时任务），请等它跑完再升级技能包。".into());
    }
    let key = keychain::get_key(&conn.id)?;
    let url = format!("{}/skill-pack", conn.api_base.trim_end_matches('/'));
    let resp = reqwest::Client::new()
        .get(&url)
        .bearer_auth(&key)
        .timeout(std::time::Duration::from_secs(60))
        .send()
        .await
        .map_err(|e| format!("下载技能包失败：{e}"))?;
    if !resp.status().is_success() {
        return Err(if resp.status().as_u16() == 404 {
            "服务端版本太旧，还没有技能包下载接口（需 v1.3.10+）。请先升级 gcms 服务端。".into()
        } else {
            format!("下载技能包失败：HTTP {}", resp.status())
        });
    }
    let version = resp
        .headers()
        .get("X-GCMS-Version")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();
    let bytes = resp.bytes().await.map_err(|e| format!("读取技能包失败：{e}"))?;
    if bytes.len() < 200 {
        return Err("下载的技能包内容异常（太小）".into());
    }
    let tmp_zip = state.data_dir.join(format!("pack-update-{}.zip", uuid::Uuid::new_v4()));
    std::fs::write(&tmp_zip, &bytes).map_err(|e| format!("写临时文件失败：{e}"))?;
    let store = state.conns.clone();
    let cid = conn_id.clone();
    let zp = tmp_zip.to_string_lossy().into_owned();
    let result = tauri::async_runtime::spawn_blocking(move || store.upgrade_from_zip(&cid, &zp))
        .await
        .map_err(|e| e.to_string())?;
    let _ = std::fs::remove_file(&tmp_zip);
    result?;
    if !version.is_empty() {
        let _ = state.conns.set_pack_version(&conn_id, &version);
    }
    Ok(if version.is_empty() {
        "技能包已升级，对话全部保留".into()
    } else {
        format!("技能包已升级到 {version}，对话全部保留")
    })
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

// ---- SSH 远程连接 ----

/// 由「表单原始输入」组装认证材料（试连时用，此时还没有连接记录/钥匙串条目）。
fn ssh_auth_from(
    auth: &str,
    user: &str,
    password: &str,
    key_path: &str,
    key_pass: &str,
) -> ssh::SshAuth {
    if auth == "key" {
        ssh::SshAuth {
            user: user.into(),
            password: None,
            key_path: Some(key_path.into()),
            key_pass: (!key_pass.is_empty()).then(|| key_pass.into()),
        }
    } else {
        ssh::SshAuth {
            user: user.into(),
            password: Some(password.into()),
            key_path: None,
            key_pass: None,
        }
    }
}

/// 试连：拿主机指纹 + 验证认证。首次添加传空 expect（TOFU 探测，指纹交 UI 确认）。
#[allow(clippy::too_many_arguments)]
#[tauri::command]
async fn verify_ssh(
    state: tauri::State<'_, AppState>,
    host: String,
    port: u16,
    user: String,
    auth: String,
    password: String,
    key_path: String,
    key_pass: String,
    expect_fingerprint: String,
    conn_id: Option<String>,
) -> Result<ssh::SshProbe, String> {
    let mut a = ssh_auth_from(&auth, &user, &password, &key_path, &key_pass);
    // 编辑已有连接时密码/口令允许留空 = 沿用钥匙串里存着的那条（UI 不回显密码，也不该逼用户重打一遍）。
    // 只有在**认证方式没变**时才这么补：换了方式，旧条目存的是另一种东西（密码 vs 私钥口令）。
    if password.is_empty() && key_pass.is_empty() {
        if let Some(old) = conn_id
            .as_deref()
            .filter(|s| !s.is_empty())
            .and_then(|id| state.conns.get(id).ok())
            .filter(|c| c.kind == "ssh" && c.ssh_auth == auth)
        {
            let stored = ssh::auth_for(&old).ok();
            match (auth.as_str(), stored) {
                ("password", Some(s)) => a.password = s.password,
                ("key", Some(s)) => a.key_pass = s.key_pass,
                _ => {}
            }
        }
    }
    let expect = (!expect_fingerprint.trim().is_empty()).then(|| expect_fingerprint.trim().to_string());
    ssh::probe(&host, if port == 0 { 22 } else { port }, &a, expect).await
}

/// 新建 SSH 连接（指纹必须是 UI 里用户确认过的那个）。
#[allow(clippy::too_many_arguments)]
#[tauri::command]
async fn connect_ssh(
    state: tauri::State<'_, AppState>,
    name: String,
    host: String,
    port: u16,
    user: String,
    auth: String,
    key_path: String,
    secret: String,
    fingerprint: String,
) -> Result<pack::Connection, String> {
    let store = state.conns.clone();
    tauri::async_runtime::spawn_blocking(move || {
        store.add_ssh(&name, &host, port, &user, &auth, &key_path, &secret, &fingerprint)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// 修改已有远程连接（指纹必须是 UI 里刚试连确认过的那个；secret 留空＝不动钥匙串里的密码/口令）。
#[allow(clippy::too_many_arguments)]
#[tauri::command]
async fn update_ssh(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    name: String,
    host: String,
    port: u16,
    user: String,
    auth: String,
    key_path: String,
    secret: String,
    fingerprint: String,
) -> Result<pack::Connection, String> {
    // 改完地址/认证后，表里那条旧会话（连的还是旧机器、用的还是旧凭据）必须作废。
    state.ssh.close(&conn_id).await;
    let store = state.conns.clone();
    tauri::async_runtime::spawn_blocking(move || {
        store.update_ssh(&conn_id, &name, &host, port, &user, &auth, &key_path, &secret, &fingerprint)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// 从 /etc/os-release 解析 (PRETTY_NAME, ID)。拿不到 PRETTY_NAME 就退回 NAME+VERSION_ID。
fn parse_os_release(s: &str) -> (String, String) {
    let val = |key: &str| -> String {
        s.lines()
            .find_map(|l| l.strip_prefix(key)?.strip_prefix('='))
            .map(|v| v.trim().trim_matches('"').trim_matches('\'').to_string())
            .unwrap_or_default()
    };
    let id = val("ID");
    let pretty = val("PRETTY_NAME");
    if !pretty.is_empty() {
        return (pretty, id);
    }
    let name = val("NAME");
    if !name.is_empty() {
        let v = val("VERSION_ID");
        return (if v.is_empty() { name } else { format!("{name} {v}") }, id);
    }
    // 不是 systemd 系（或压根没有 os-release）：调用方会把 uname 的输出丢进来兜底。
    (s.trim().lines().next().unwrap_or_default().trim().to_string(), id)
}

/// 探远端系统版本并记进连接（UI 用它显示「Ubuntu 24.04」+ 发行版图标）。
/// 只在还没探过时调（探到就存下来，之后不再连）。
#[tauri::command]
async fn ssh_os_probe(
    state: tauri::State<'_, AppState>,
    conn_id: String,
) -> Result<pack::Connection, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程连接".into());
    }
    let auth = ssh::auth_for(&conn)?;
    let expect = (!conn.ssh_fingerprint.is_empty()).then(|| conn.ssh_fingerprint.clone());
    state
        .ssh
        .ensure(&conn_id, &conn.ssh_host, conn.ssh_port, &auth, expect)
        .await?;
    // 非 Linux（或精简镜像）没有 os-release → 退回 uname 也好过什么都不显示。
    let out = state
        .ssh
        .exec(&conn_id, "cat /etc/os-release 2>/dev/null || uname -sr", 20, false) // 后台探测，别拿噪音污染终端
        .await?;
    let (pretty, os_id) = parse_os_release(&out.stdout);
    let store = state.conns.clone();
    tauri::async_runtime::spawn_blocking(move || store.set_ssh_os(&conn_id, &pretty, &os_id))
        .await
        .map_err(|e| e.to_string())?
}

/// 取一次远端负载（CPU 累计计数 + 内存 + 根分区）。顶栏那三个数用它。
/// 没有现成会话就直接失败 —— 前端静默忽略，不为了三个数去偷偷登服务器。
#[tauri::command]
async fn ssh_stats(state: tauri::State<'_, AppState>, conn_id: String) -> Result<ssh::SshStats, String> {
    state.ssh.stats(&conn_id).await
}

/// 打开交互式 PTY shell，输出流给前端 xterm。连接时带上已确认指纹，不匹配即拒。
#[tauri::command]
async fn ssh_open_shell(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    cols: u32,
    rows: u32,
    on_event: Channel<ssh::SshEvent>,
) -> Result<(), String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程连接".into());
    }
    let auth = ssh::auth_for(&conn)?;
    let expect = (!conn.ssh_fingerprint.is_empty()).then(|| conn.ssh_fingerprint.clone());
    state
        .ssh
        .open_shell(
            &conn_id,
            &conn.ssh_host,
            conn.ssh_port,
            &auth,
            expect,
            cols.max(1),
            rows.max(1),
            on_event,
        )
        .await
}

/// 键盘输入（base64：可能敲出控制字节/非 UTF-8 序列）。
#[tauri::command]
async fn ssh_input(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    b64: String,
) -> Result<(), String> {
    use base64::Engine as _;
    let bytes = base64::engine::general_purpose::STANDARD
        .decode(b64.as_bytes())
        .map_err(|e| format!("输入解码失败: {e}"))?;
    state.ssh.input(&conn_id, &bytes).await
}

#[tauri::command]
async fn ssh_resize(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    cols: u32,
    rows: u32,
) -> Result<(), String> {
    state.ssh.resize(&conn_id, cols.max(1), rows.max(1)).await
}

#[tauri::command]
async fn ssh_close(state: tauri::State<'_, AppState>, conn_id: String) -> Result<(), String> {
    state.ssh.close(&conn_id).await;
    Ok(())
}

// ---- SSH 远程文件（SFTP，复用已打开的终端会话，不重新握手） ----

/// 在线编辑的大小闸门：整文件进内存 + base64 过 IPC，2MB 之上就该用下载了。
const SFTP_EDIT_MAX: u64 = 2 * 1024 * 1024;

/// SFTP 命令的会话保障：过去 SFTP 只 `g.get(conn_id)`，要**靠终端先把会话建起来** ——
/// 于是「文件面板抢在终端连上之前加载」就卡死在「会话未打开：请先连接远程终端」，
/// 而且前端那个 $effect 加载失败不重试，终端后来连上了文件面板也不会自己回来（真踩过）。
/// 这里让每条 SFTP 命令**自己 ensure**：会话已在就是一次 map 查询直接返回（ensure 内部对活着的
/// 会话早退，不重连、不读凭据）；没在就按连接的凭据+指纹连上（和 open_shell 同一套）。
/// 文件浏览是用户的直接动作，该连就连——不像 stats 那样为了三个数字克制着不登录。
async fn ensure_ssh(state: &AppState, conn_id: &str) -> Result<(), String> {
    if state.ssh.is_open(conn_id).await {
        return Ok(());
    }
    let conn = state.conns.get(conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程连接".into());
    }
    let auth = ssh::auth_for(&conn)?;
    let expect = (!conn.ssh_fingerprint.is_empty()).then(|| conn.ssh_fingerprint.clone());
    state
        .ssh
        .ensure(conn_id, &conn.ssh_host, conn.ssh_port, &auth, expect)
        .await
}

#[tauri::command]
async fn sftp_list(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    path: String,
) -> Result<Vec<ssh::SftpEntry>, String> {
    ensure_ssh(&state, &conn_id).await?;
    state.ssh.list_dir(&conn_id, &path).await
}

/// 解析起始目录（"." → 真实 home 的绝对路径）。
#[tauri::command]
async fn sftp_home(state: tauri::State<'_, AppState>, conn_id: String) -> Result<String, String> {
    ensure_ssh(&state, &conn_id).await?;
    state.ssh.real_path(&conn_id, ".").await
}

/// 读整个文件给在线编辑器。返回 base64：文本是否合法 UTF-8 由前端判定（TextDecoder fatal）。
#[tauri::command]
async fn sftp_read(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    path: String,
) -> Result<String, String> {
    use base64::Engine as _;
    ensure_ssh(&state, &conn_id).await?;
    let bytes = state.ssh.read_file(&conn_id, &path, SFTP_EDIT_MAX).await?;
    Ok(base64::engine::general_purpose::STANDARD.encode(bytes))
}

#[tauri::command]
async fn sftp_write(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    path: String,
    b64: String,
) -> Result<(), String> {
    use base64::Engine as _;
    let bytes = base64::engine::general_purpose::STANDARD
        .decode(b64.as_bytes())
        .map_err(|e| format!("内容解码失败: {e}"))?;
    ensure_ssh(&state, &conn_id).await?;
    state.ssh.write_file(&conn_id, &path, &bytes).await
}

#[tauri::command]
async fn sftp_rename(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    from: String,
    to: String,
) -> Result<(), String> {
    ensure_ssh(&state, &conn_id).await?;
    state.ssh.rename(&conn_id, &from, &to).await
}

#[tauri::command]
async fn sftp_remove(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    path: String,
    dir: bool,
) -> Result<(), String> {
    ensure_ssh(&state, &conn_id).await?;
    state.ssh.remove(&conn_id, &path, dir).await
}

#[tauri::command]
async fn sftp_mkdir(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    path: String,
) -> Result<(), String> {
    ensure_ssh(&state, &conn_id).await?;
    state.ssh.mkdir(&conn_id, &path).await
}

#[tauri::command]
async fn sftp_download(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    remote: String,
    local: String,
) -> Result<u64, String> {
    ensure_ssh(&state, &conn_id).await?;
    state.ssh.download(&conn_id, &remote, &local).await
}

#[tauri::command]
async fn sftp_upload(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    local: String,
    remote: String,
) -> Result<u64, String> {
    ensure_ssh(&state, &conn_id).await?;
    state.ssh.upload(&conn_id, &local, &remote).await
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

/// 校验并解析消息里提到的文件路径为绝对路径。相对路径按工作目录解析；绝对路径
/// （AI 常直接给全路径）也接，但 canonicalize 后必须仍在工作目录内——边界与相对
/// 路径完全一致，工作目录之外一律拒绝。只读解析不建目录（目录被删就自然报错，调用方降级）。
fn resolve_in_workdir(conn: &pack::Connection, project: &str, raw: &str) -> Result<std::path::PathBuf, String> {
    let dir = {
        let w = work_dir_path(conn, project);
        if w.is_empty() { conn.skill_dir.clone() } else { w }
    };
    if dir.is_empty() {
        return Err("没有可用的工作目录".into());
    }
    let root = std::fs::canonicalize(&dir).map_err(|e| format!("工作目录不可用: {e}"))?;
    let raw = raw.trim();
    let is_abs = raw.starts_with('/')
        || raw.starts_with('\\')
        || (raw.len() > 2 && raw.as_bytes()[1] == b':' && raw.as_bytes()[0].is_ascii_alphabetic());
    let p = if is_abs {
        if raw.len() > 1024 {
            return Err("路径不合法".into());
        }
        // 本机不存在的「绝对路径」再按工作目录相对解析一次：助手常写站点口径的
        // /uploads/xx.svg，它真身就在 <工作目录>/uploads/ 下。
        let exact = match std::fs::canonicalize(raw) {
            Ok(p) => Ok(p),
            Err(e) => std::fs::canonicalize(root.join(raw.trim_start_matches(['/', '\\'])))
                .map_err(|_| format!("读取失败: {e}")),
        };
        match exact {
            Ok(p) if p.starts_with(&root) => p,
            // 智能体自己的产物目录也放行（codex 生图固定写 ~/.codex/generated_images）
            Ok(p) if agent_output_roots().iter().any(|r| p.starts_with(r)) => p,
            Ok(_) => return Err("文件在工作目录之外".into()),
            // 精确解析不到（模型常把真实写在工作目录里的文件链接成想象中的前缀，
            // 如 /mnt/data/xx.webp、sandbox:…）：按文件名在工作目录内搜，唯一命中才用。
            Err(e) => find_by_basename(&root, raw).ok_or(e)?,
        }
    } else {
        let rel = raw.trim_start_matches("./");
        if rel.is_empty() || rel.len() > 512 || rel.contains(':') || rel.contains("..") {
            return Err("路径不合法".into());
        }
        match std::fs::canonicalize(root.join(rel)) {
            Ok(p) if p.starts_with(&root) => p,
            Ok(_) => return Err("路径不合法".into()),
            Err(e) => find_by_basename(&root, rel).ok_or_else(|| format!("读取失败: {e}"))?,
        }
    };
    Ok(p)
}

/// 智能体自己的产物目录（工作目录之外的只读预览白名单）：codex 生图工具固定写到
/// ~/.codex/generated_images（CODEX_HOME 可改基目录）。只放行这一处，不放开家目录其他内容。
fn agent_output_roots() -> Vec<std::path::PathBuf> {
    std::env::var_os("CODEX_HOME")
        .map(std::path::PathBuf::from)
        .or_else(|| {
            std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
                .map(|h| std::path::Path::new(&h).join(".codex"))
        })
        .map(|b| b.join("generated_images"))
        .and_then(|p| std::fs::canonicalize(p).ok())
        .into_iter()
        .collect()
}

/// 按文件名在工作目录内浅搜（深度≤4、扫描≤2000 项；跳过 .xx 隐藏目录和 node_modules）。
/// **恰好一个**同名文件才返回——两个以上宁可不猜；命中后仍 canonicalize + 界内校验
/// （防目录内的符号链接指到外面）。
fn find_by_basename(root: &std::path::Path, raw: &str) -> Option<std::path::PathBuf> {
    let name = raw.trim().rsplit(['/', '\\']).next()?.trim();
    if name.is_empty() || !name.contains('.') {
        return None;
    }
    let mut found: Option<std::path::PathBuf> = None;
    let mut stack = vec![(root.to_path_buf(), 0usize)];
    let mut seen = 0usize;
    while let Some((dir, depth)) = stack.pop() {
        let Ok(rd) = std::fs::read_dir(&dir) else { continue };
        for e in rd.flatten() {
            seen += 1;
            if seen > 2000 {
                return None; // 目录太大：放弃兜底，不做半截扫描的猜测
            }
            let p = e.path();
            let fname = e.file_name();
            let fname = fname.to_string_lossy();
            if fname.starts_with('.') || fname == "node_modules" {
                continue;
            }
            let Ok(ft) = e.file_type() else { continue };
            if ft.is_dir() {
                if depth < 4 {
                    stack.push((p, depth + 1));
                }
            } else if fname == name {
                if found.is_some() {
                    return None; // 同名多处：有歧义，不猜
                }
                found = Some(p);
            }
        }
    }
    let p = std::fs::canonicalize(found?).ok()?;
    p.starts_with(root).then_some(p)
}

#[cfg(test)]
mod os_release_tests {
    use super::parse_os_release;

    #[test]
    fn reads_pretty_name_and_id() {
        // Ubuntu（真机 24.04 的形状）
        let (p, id) = parse_os_release(
            "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\nID_LIKE=debian\n",
        );
        assert_eq!(p, "Ubuntu 24.04.1 LTS");
        assert_eq!(id, "ubuntu");
        // Debian
        let (p, id) = parse_os_release("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n");
        assert_eq!(p, "Debian GNU/Linux 12 (bookworm)");
        assert_eq!(id, "debian");
        // Alpine 没有 PRETTY_NAME → 退回 NAME + VERSION_ID
        let (p, id) = parse_os_release("NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.20.3\n");
        assert_eq!(p, "Alpine Linux 3.20.3");
        assert_eq!(id, "alpine");
        // 单引号 + 无 VERSION_ID
        let (p, _) = parse_os_release("NAME='Arch Linux'\nID=arch\n");
        assert_eq!(p, "Arch Linux");
    }

    #[test]
    fn falls_back_to_uname_output() {
        // 没有 os-release 的机器：命令退回 `uname -sr`，第一行就是全部信息
        let (p, id) = parse_os_release("Linux 6.8.0-45-generic\n");
        assert_eq!(p, "Linux 6.8.0-45-generic");
        assert_eq!(id, "");
        // 什么都没拿到也别炸
        assert_eq!(parse_os_release(""), (String::new(), String::new()));
        assert_eq!(parse_os_release("   \n\n"), (String::new(), String::new()));
    }

    /// ID 是子串匹配的重灾区：别让 VERSION_ID / ID_LIKE 冒充 ID。
    #[test]
    fn does_not_confuse_similar_keys() {
        let (_, id) = parse_os_release("VERSION_ID=\"22.04\"\nID_LIKE=debian\nID=ubuntu\n");
        assert_eq!(id, "ubuntu");
        let (_, id) = parse_os_release("ID_LIKE=\"rhel fedora\"\nVERSION_ID=9\n");
        assert_eq!(id, "", "只有 ID_LIKE 时不该把它当成 ID");
    }
}

#[cfg(test)]
mod workdir_tests {
    use super::*;

    fn test_conn(dir: &str) -> pack::Connection {
        serde_json::from_value(serde_json::json!({
            "id": "t", "name": "t", "api_base": "", "skill_dir": dir,
            "key_prefix": "", "key_kind": "gcms_", "created_at": "",
        }))
        .expect("test connection")
    }

    /// 相对 / 绝对 / 站点口径绝对(/uploads/..) 路径都必须解析进工作目录；越界与逃逸一律拒绝。
    #[test]
    fn resolve_in_workdir_boundaries() {
        let tmp = std::env::temp_dir().join(format!("gcms-pilot-workdir-test-{}", std::process::id()));
        let up = tmp.join("uploads");
        std::fs::create_dir_all(&up).unwrap();
        std::fs::write(up.join("a.svg"), "<svg/>").unwrap();
        let conn = test_conn(tmp.to_str().unwrap());

        assert!(resolve_in_workdir(&conn, "", "uploads/a.svg").is_ok(), "相对路径");
        let abs = tmp.join("uploads").join("a.svg");
        assert!(resolve_in_workdir(&conn, "", abs.to_str().unwrap()).is_ok(), "工作目录内绝对路径");
        assert!(resolve_in_workdir(&conn, "", "/uploads/a.svg").is_ok(), "站点口径绝对路径回退相对解析");
        assert!(resolve_in_workdir(&conn, "", "/etc/hosts").is_err(), "工作目录之外的绝对路径");
        assert!(resolve_in_workdir(&conn, "", "../x").is_err(), "相对逃逸");
        assert!(resolve_in_workdir(&conn, "", "uploads/missing.svg").is_err(), "不存在的文件");

        // 文件名兜底：模型把真实文件链接成想象中的前缀（/mnt/data/、sandbox: 剥壳后）
        std::fs::create_dir_all(tmp.join("brand/out")).unwrap();
        std::fs::write(tmp.join("brand/out/preview.webp"), "x").unwrap();
        assert!(resolve_in_workdir(&conn, "", "/mnt/data/preview.webp").is_ok(), "想象前缀按文件名找回");
        assert!(resolve_in_workdir(&conn, "", "outputs/preview.webp").is_ok(), "相对路径写错也找回");
        // 多处同名 → 有歧义不猜
        std::fs::write(tmp.join("uploads/preview.webp"), "y").unwrap();
        assert!(resolve_in_workdir(&conn, "", "/mnt/data/preview.webp").is_err(), "同名多处不猜");
        // 真不存在仍报错
        assert!(resolve_in_workdir(&conn, "", "/mnt/data/nothing.webp").is_err(), "真不存在");
        std::fs::remove_dir_all(&tmp).ok();
    }

    /// codex 生图产物目录（CODEX_HOME/generated_images）在工作目录之外也可预览；
    /// 家目录其他位置仍拒绝（resolve_in_workdir_boundaries 里的 /etc/hosts 用例）。
    #[test]
    fn codex_generated_images_root_allowed() {
        let ch = std::env::temp_dir().join(format!("gcms-pilot-codexhome-test-{}", std::process::id()));
        std::fs::create_dir_all(ch.join("generated_images").join("sub")).unwrap();
        let img = ch.join("generated_images").join("sub").join("gen.png");
        std::fs::write(&img, "p").unwrap();
        std::env::set_var("CODEX_HOME", &ch);

        let wd = std::env::temp_dir().join(format!("gcms-pilot-workdir2-test-{}", std::process::id()));
        std::fs::create_dir_all(&wd).unwrap();
        let conn = test_conn(wd.to_str().unwrap());
        assert!(resolve_in_workdir(&conn, "", img.to_str().unwrap()).is_ok(), "生图目录放行");

        std::env::remove_var("CODEX_HOME");
        std::fs::remove_dir_all(&ch).ok();
        std::fs::remove_dir_all(&wd).ok();
    }

    /// 安装失败摘要：优先脚本 stdout 末行；stderr 异常首行剥掉 PowerShell 命令回显前缀。
    #[test]
    fn install_err_brief_extracts_script_words() {
        // 复刻用户实拍：脚本把真实原因写在 stdout，PS 异常首行是整条命令回显 + 通用消息
        let stdout = "Downloading Claude Code...\nFailed to download from https://storage.googleapis.com/...: timeout\n".as_bytes();
        let stderr = "[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; irm https://claude.ai/install.ps1 | iex : Installation failed (exit code 1)\nAt line:1 char:1\n+ FullyQualifiedErrorId : xxx\n".as_bytes();
        let m = install_err_brief(stdout, stderr);
        assert!(m.contains("Failed to download"), "应含脚本自述原因: {m}");
        assert!(m.contains("Installation failed"), "应含异常消息: {m}");
        assert!(!m.contains("SecurityProtocol"), "不应带命令回显: {m}");

        // stdout 为空时退回 stderr；两者全空给未知错误
        assert!(install_err_brief(b"", b"curl: (7) Failed to connect\n").contains("curl: (7)"));
        assert_eq!(install_err_brief(b"", b""), "未知错误");
    }

    /// WinINET ProxyServer 解析：单一 host:port / 分协议优先 https / 仅 ftp、socks 不猜 / 空与畸形。
    #[test]
    fn win_proxy_server_parse() {
        assert_eq!(parse_win_proxy_server("127.0.0.1:7890").as_deref(), Some("127.0.0.1:7890"));
        assert_eq!(parse_win_proxy_server("  10.0.0.1:8080  ").as_deref(), Some("10.0.0.1:8080"), "裸值去空白");
        assert_eq!(
            parse_win_proxy_server("http=1.2.3.4:80;https=5.6.7.8:443;ftp=9.9.9.9:21").as_deref(),
            Some("5.6.7.8:443"),
            "分协议优先 https"
        );
        assert_eq!(parse_win_proxy_server("http=1.2.3.4:80").as_deref(), Some("1.2.3.4:80"), "只有 http 段用 http");
        assert!(parse_win_proxy_server("ftp=1.2.3.4:21;socks=5.6.7.8:1080").is_none(), "只配 ftp/socks 不硬猜");
        assert!(parse_win_proxy_server("").is_none());
        assert!(parse_win_proxy_server("   ").is_none());
        assert!(parse_win_proxy_server("https=").is_none(), "空段畸形不算命中");
    }

    /// 安装详情取尾不取头：回显/进度剥掉、PS 异常首行只留 iex 后消息、超长保留末尾 600 字。
    #[test]
    fn install_err_tail_strips_echo_and_keeps_tail() {
        // 多行实拍形态：进度行 + 真错误在 stdout 尾部；stderr 首行是整句命令回显
        let stdout = "Installing Claude Code native build latest...\nDownloading...\nFailed to download from https://storage.googleapis.com/x: timeout\n".as_bytes();
        let stderr = "[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; irm https://claude.ai/install.ps1 | iex : Installation failed (exit code 1)\nAt line:1 char:1\n+ FullyQualifiedErrorId : xxx\n".as_bytes();
        let t = install_err_tail(stdout, stderr);
        assert!(t.contains("Failed to download"), "真错误保留: {t}");
        assert!(t.contains("Installation failed (exit code 1)"), "回显行只留 iex 后消息: {t}");
        assert!(!t.contains("SecurityProtocol"), "命令回显剥掉");
        assert!(!t.contains("Installing Claude Code"), "进度行剥掉");
        assert!(t.contains("At line:1"), "PS 装饰行保留（取尾语义，不做白名单）");
        // 超长取尾：真错误在末尾 600 字里，且带截断记号
        let long = format!("{}\nREAL ERROR AT TAIL", "x".repeat(2000));
        let t2 = install_err_tail(long.as_bytes(), b"");
        assert!(t2.starts_with('…') && t2.ends_with("REAL ERROR AT TAIL"));
        assert!(t2.chars().count() <= 601, "…+600 字上限: {}", t2.chars().count());
        // 无回显的普通短输出原样、不截
        assert_eq!(install_err_tail(b"boom", b""), "boom");
        // 全空 / 剥完只剩回显 → 占位
        assert_eq!(install_err_tail(b"", b""), "（无输出）");
        assert_eq!(install_err_tail(b"Installing Claude Code native build latest...", b""), "（无输出）");
    }

    /// 开启托管：三个配套任务（每日/审计/周报）创建齐、厂商/模型/强度逐项透传、
    /// 等级与上限进 prompt、记录字段完整、同站重复开启被拒。
    #[test]
    fn enable_managed_creates_tasks_and_passes_model() {
        let dir = std::env::temp_dir().join(format!("gcms-pilot-managed-en-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let conn = test_conn(dir.to_str().unwrap()); // id = "t"
        std::fs::write(dir.join("connections.json"), serde_json::to_vec_pretty(&[conn]).unwrap()).unwrap();
        let conns = pack::ConnStore::new(&dir).unwrap();
        let tstore = tasks::TaskStore::new(&dir);
        let mstore = managed::ManagedStore::new(&dir);

        let m = enable_managed(
            &conns, &tstore, &mstore,
            "t".into(), "blog".into(), "博客".into(), "定位：测试站".into(),
            4, 3, "l1".into(), 500_000, "grok".into(), "grok-4.5".into(), "high".into(),
        )
        .expect("开启托管");
        assert_eq!(m.brain, "grok");
        assert_eq!(m.model, "grok-4.5");
        assert_eq!(m.effort, "high");
        assert_eq!(m.level, "l1");
        assert_eq!(m.token_weekly_budget, 500_000);
        assert_eq!(m.weekly_edit_limit, 3, "修改配额入记录");
        assert_eq!(m.task_ids.len(), 3, "每日/审计/周报三个配套任务");
        assert!(m.enabled_at > 0, "开启时间落库（配额爬坡起点）");
        assert_eq!(m.audit_notes, "", "开启时还没有审计要点");

        let daily = tstore.get(&m.task_ids[0]).expect("每日任务");
        assert!(daily.title.starts_with("托管 · 每日内容"));
        assert_eq!((daily.brain.as_str(), daily.model.as_str(), daily.effort.as_str()), ("grok", "grok-4.5", "high"), "模型透传");
        assert_eq!(daily.interval_minutes, 1440);
        assert!(daily.prompt.contains("不得超过 4 篇"));
        assert!(daily.prompt.contains("可以直接发布"), "L1 的每日 prompt 放开常规发布");

        let audit = tstore.get(&m.task_ids[1]).expect("审计任务");
        assert!(audit.title.starts_with("托管 · 每周审计"));
        assert_eq!(audit.interval_minutes, 10080);
        assert_eq!(audit.brain, "grok");

        let report = tstore.get(&m.task_ids[2]).expect("周报任务");
        assert!(report.title.starts_with("托管 · 每周周报"));
        assert!(report.prompt.contains("本周实测数据"));
        assert!(report.prompt.contains("计划关键词 vs 实际曝光词偏差"), "周报偏差对照节");
        assert!(report.prompt.contains("定位：测试站"), "90 天计划随周报 prompt 下发");
        assert_eq!(report.model, "grok-4.5");

        // 同站重复开启被拒；非法等级回落 l0
        assert!(enable_managed(&conns, &tstore, &mstore, "t".into(), "blog".into(), String::new(), String::new(), 3, 2, "l0".into(), 0, String::new(), String::new(), String::new()).is_err());
        let m2 = enable_managed(&conns, &tstore, &mstore, "t".into(), "shop".into(), String::new(), String::new(), 3, 2, "bogus".into(), 0, "claude".into(), "sonnet".into(), String::new()).unwrap();
        assert_eq!(m2.level, "l0", "非法等级回落 L0（安全侧）");
        // 配额爬坡：新开启（第 0 天）上界 7，传 30 被钳到 7
        let m3 = enable_managed(&conns, &tstore, &mstore, "t".into(), "news".into(), String::new(), String::new(), 30, 2, "l0".into(), 0, "claude".into(), "sonnet".into(), String::new()).unwrap();
        assert_eq!(m3.weekly_post_limit, 7, "新站爬坡期钳到 7 篇/周");
        assert!(tstore.get(&m3.task_ids[0]).unwrap().prompt.contains("不得超过 7 篇"), "钳后的上限进每日 prompt");
        std::fs::remove_dir_all(&dir).ok();
    }

    fn test_conv(brain: &str, model: &str, perm: &str) -> Conversation {
        serde_json::from_value(serde_json::json!({
            "id": "c1", "conn_id": "t", "conn_name": "", "site_slug": "s", "site_name": "",
            "task_type": "free", "brain": brain, "model": model, "perm_mode": perm,
            "session_ref": "sess-old", "title": "t", "messages": [], "status": "idle",
            "created_at": 0, "updated_at": 0,
        }))
        .expect("test conversation")
    }

    /// ★ 放开 codex 跑远程连接之后，**AI 桥那道确认卡是它仅剩的闸**（闸在 bridge.rs，与厂商无关）。
    /// 所以「codex 的 ask/auto 落 full」这条规则**绝不能**落到 ssh 对话上：
    /// 前端切完厂商会立刻 rebuild_session 自动重跑，静默升档 = 一次「换个模型」的点击
    /// 就在真机上无人确认地跑起来了。这是评审里的 critical，别改回去。
    #[test]
    fn ssh_conv_keeps_ask_auto_when_switching_to_codex() {
        for m in ["ask", "auto"] {
            let mut c = test_conv("claude", "sonnet", m);
            apply_brain_switch(&mut c, "codex", "gpt-5.6", true); // is_ssh
            assert_eq!(c.perm_mode, m, "ssh 对话切 codex 必须保住 {m}（桥还要靠它弹卡）");
            assert_eq!(c.session_ref, "", "跨厂商仍要抛弃旧 session");
        }
        // 非 ssh 照旧落 full：那里 codex 真的没有任何逐命令闸，ask/auto 名不副实（原行为不变）
        let mut c = test_conv("claude", "sonnet", "auto");
        apply_brain_switch(&mut c, "codex", "gpt-5.6", false);
        assert_eq!(c.perm_mode, "full");
    }

    /// 跨厂商切换：换 brain 清 session_ref（另一家 resume 不了旧会话）+ codex 下 ask/auto 落 full；
    /// 同厂商只换 model，session/perm 原样保留。effort 字段两条路径都不碰。
    #[test]
    fn apply_brain_switch_rules() {
        // claude → codex：清 session、auto 落 full
        let mut c = test_conv("claude", "sonnet", "auto");
        apply_brain_switch(&mut c, "codex", "gpt-5.6-terra", false);
        assert_eq!(c.brain, "codex");
        assert_eq!(c.model, "gpt-5.6-terra");
        assert_eq!(c.session_ref, "", "跨厂商必须抛弃旧 session");
        assert_eq!(c.perm_mode, "full", "codex 无逐命令确认，auto 落 full");

        // claude → codex：plan 档保留（只读语义两家通用）
        let mut c = test_conv("claude", "opus", "plan");
        apply_brain_switch(&mut c, "codex", "", false);
        assert_eq!(c.perm_mode, "plan");

        // codex → claude：ask/auto 不存在被误改的方向，perm 原样
        let mut c = test_conv("codex", "gpt-5.5", "full");
        apply_brain_switch(&mut c, "claude", "sonnet", false);
        assert_eq!(c.session_ref, "");
        assert_eq!(c.perm_mode, "full");

        // claude → grok：grok 有逐命令批准（ACP 权限桥），ask/auto 原样保留
        let mut c = test_conv("claude", "sonnet", "ask");
        apply_brain_switch(&mut c, "grok", "", false);
        assert_eq!(c.brain, "grok");
        assert_eq!(c.session_ref, "");
        assert_eq!(c.perm_mode, "ask");

        // 同厂商换档：session_ref 保留（还是同一条底层会话）
        let mut c = test_conv("claude", "sonnet", "ask");
        apply_brain_switch(&mut c, "claude", "opus", false);
        assert_eq!(c.session_ref, "sess-old");
        assert_eq!(c.perm_mode, "ask");
        assert_eq!(c.model, "opus");
    }
}

/// 读工作目录内的图片为 data URI（webview 读不了本地文件）。附件缩略图（uploads/xx）和
/// 助手消息里提到的项目内图片（如 brand/logo.svg）都走它。扩展名白名单；
/// 前端打开会话会批量调用：async + spawn_blocking，读盘/编码不占主线程；
/// 超过 8MB 不生成（data URI 会膨胀 1/3 常驻前端内存），退回文件卡展示。
#[tauri::command]
async fn read_workdir_image(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    project: String,
    path: String,
) -> Result<String, String> {
    let ext = std::path::Path::new(&path)
        .extension()
        .and_then(|s| s.to_str())
        .unwrap_or("")
        .to_ascii_lowercase();
    let mime = match ext.as_str() {
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "gif" => "image/gif",
        "webp" => "image/webp",
        "svg" => "image/svg+xml",
        "bmp" => "image/bmp",
        "avif" => "image/avif",
        _ => return Err("不是图片文件".into()),
    };
    let conn = state.conns.get(&conn_id)?;
    tauri::async_runtime::spawn_blocking(move || {
        let p = resolve_in_workdir(&conn, &project, &path)?;
        let meta = std::fs::metadata(&p).map_err(|e| format!("读取失败: {e}"))?;
        if !meta.is_file() {
            return Err("不是文件".into());
        }
        if meta.len() > 8_000_000 {
            return Err("图片较大（>8MB），不生成缩略图".into());
        }
        let data = std::fs::read(&p).map_err(|e| format!("读取失败: {e}"))?;
        use base64::Engine as _;
        Ok(format!("data:{mime};base64,{}", base64::engine::general_purpose::STANDARD.encode(data)))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// 把工作目录内的相对路径解析成绝对路径（给「在文件管理器中显示」用）。同样的合法性约束。
#[tauri::command]
async fn resolve_workdir_file(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    project: String,
    path: String,
) -> Result<String, String> {
    let conn = state.conns.get(&conn_id)?;
    tauri::async_runtime::spawn_blocking(move || {
        let p = resolve_in_workdir(&conn, &project, &path)?;
        if !p.is_file() {
            return Err("文件不存在".into());
        }
        Ok(p.to_string_lossy().into_owned())
    })
    .await
    .map_err(|e| e.to_string())?
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

/// 在新窗口里打开一个连接（几台机器 / 几个站点并排看）。同一连接已有窗口就把它拎到前面。
///
/// 窗口之间只是各自一份前端：连接表、对话、SSH 会话、权限待批都在 Rust 侧的同一份 AppState 里，
/// 所以两个窗口看到的是同一份数据，不会分叉。
///
/// **连接 id 走窗口 label（`conn-<id>`），不走 URL**：`WebviewUrl::App` 只有在路径**恰好**是
/// "index.html" 时才直接用根 URL，带上 `?conn=…` 就会 join 成 `/index.html?conn=…` ——
/// 而 SvelteKit 的 dev server 只认 `/`、不认 `/index.html`，那样开出来是一片白。
/// label 本来就是现成的（上面判重就用它），前端读 `getCurrentWindow().label` 即可。
#[tauri::command]
async fn open_conn_window(
    app: AppHandle,
    state: tauri::State<'_, AppState>,
    conn_id: String,
) -> Result<(), String> {
    let conn = state.conns.get(&conn_id)?;
    // 标签只允许字母数字和 -/:_ —— uuid 正好合规。
    let label = format!("conn-{conn_id}");
    if let Some(w) = app.get_webview_window(&label) {
        let _ = w.unminimize();
        let _ = w.show();
        let _ = w.set_focus();
        return Ok(());
    }
    // mut 只有 macOS 分支用得上（下面那两个设置是 mac 专有）
    #[cfg_attr(not(target_os = "macos"), allow(unused_mut))]
    let mut b = tauri::WebviewWindowBuilder::new(
        &app,
        &label,
        // 必须原样是 "index.html"：这条路径和主窗口走的是同一个分支（直接用根 URL）。
        tauri::WebviewUrl::App("index.html".into()),
    )
    .title(&conn.name)
    .inner_size(1024.0, 700.0)
    .min_inner_size(820.0, 560.0);
    // 主窗口在 tauri.conf 里是 Overlay + 隐藏标题（前端自己画标题栏），新窗口得对齐，
    // 否则同一套前端布局会和系统标题栏叠在一起。这两个设置只有 macOS 有。
    #[cfg(target_os = "macos")]
    {
        b = b.title_bar_style(tauri::TitleBarStyle::Overlay).hidden_title(true);
    }
    let w = b.build().map_err(|e| format!("打开新窗口失败: {e}"))?;
    #[cfg(target_os = "windows")]
    style_titlebar_windows(&w);
    let _ = w.set_focus();
    Ok(())
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
    tauri::async_runtime::spawn_blocking(|| {
        path_env::fix();
        apply_system_proxy_to_process();
    })
    .await
    .map_err(|e| e.to_string())?;
    Ok(brains::detect().await)
}

/// GUI 进程没有终端里的 HTTP(S)_PROXY 环境变量；从系统代理配置补一份给安装子进程，
/// 让脚本内部的 curl / node 下载步骤也走代理（VPN 用户「浏览器能开 claude.ai 但安装失败」
/// 的主因之一）。用户环境已有代理变量时不覆盖；读不到系统代理就什么都不注入。
fn system_proxy_env() -> Vec<(String, String)> {
    for k in ["HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"] {
        if std::env::var_os(k).is_some() {
            return vec![];
        }
    }
    let Some(mut u) = system_proxy_url() else { return vec![] };
    if !u.contains("://") {
        u = format!("http://{u}");
    }
    ["HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"]
        .iter()
        .map(|k| (k.to_string(), u.clone()))
        .collect()
}

/// 解析 WinINET ProxyServer 注册表值（纯函数，单测覆盖；mac 上也编译便于审读/测试）：
/// 单一 "host:port" 直接用；按协议分段 "http=…;https=…;ftp=…" 时优先 https 再 http；
/// 只配了 ftp=/socks= 等不硬猜；空/空白 → None。
#[cfg_attr(not(windows), allow(dead_code))]
fn parse_win_proxy_server(server: &str) -> Option<String> {
    let server = server.trim();
    if server.is_empty() {
        return None;
    }
    if server.contains('=') {
        for want in ["https=", "http="] {
            if let Some(seg) = server.split(';').find_map(|s| s.trim().strip_prefix(want)) {
                let seg = seg.trim();
                if !seg.is_empty() {
                    return Some(seg.to_string());
                }
            }
        }
        return None; // 只配了 ftp=/socks= 等，不硬猜
    }
    Some(server.to_string())
}

/// Windows：WinINET 用户级代理（ProxyEnable=1 时 ProxyServer 形如 "127.0.0.1:7890"，
/// 或按协议分段 "http=…;https=…"）。
#[cfg(target_os = "windows")]
fn system_proxy_url() -> Option<String> {
    let q = |name: &str| -> Option<String> {
        let mut c = std::process::Command::new("reg");
        c.args(["query", r"HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings", "/v", name]);
        {
            use std::os::windows::process::CommandExt;
            c.creation_flags(0x0800_0000);
        }
        let out = c.output().ok()?;
        let s = String::from_utf8_lossy(&out.stdout);
        s.lines()
            .find(|l| l.trim_start().starts_with(name))
            .and_then(|l| l.split_whitespace().last().map(str::to_string))
    };
    let enabled = q("ProxyEnable")
        .and_then(|v| u32::from_str_radix(v.trim_start_matches("0x"), 16).ok())
        .unwrap_or(0)
        != 0;
    if !enabled {
        return None;
    }
    parse_win_proxy_server(&q("ProxyServer")?)
}

/// macOS：scutil --proxy（HTTPSEnable/HTTPSProxy/HTTPSPort，HTTP 同构；curl 不认系统
/// 代理只认环境变量，这里读出来转成环境变量）。
#[cfg(target_os = "macos")]
fn system_proxy_url() -> Option<String> {
    let out = std::process::Command::new("scutil").arg("--proxy").output().ok()?;
    let s = String::from_utf8_lossy(&out.stdout);
    let get = |k: &str| -> Option<String> {
        s.lines()
            .find(|l| l.trim_start().starts_with(k))
            .and_then(|l| l.splitn(2, ':').nth(1).map(|v| v.trim().to_string()))
    };
    for (e, h, p) in [
        ("HTTPSEnable", "HTTPSProxy", "HTTPSPort"),
        ("HTTPEnable", "HTTPProxy", "HTTPPort"),
    ] {
        if get(e).as_deref() == Some("1") {
            if let (Some(host), Some(port)) = (get(h), get(p)) {
                if !host.is_empty() && !port.is_empty() {
                    return Some(format!("{host}:{port}"));
                }
            }
        }
    }
    None
}

#[cfg(not(any(target_os = "windows", target_os = "macos")))]
fn system_proxy_url() -> Option<String> {
    None
}

/// 安装失败时抽「脚本自己说的话」：优先 stdout 末尾的有效行（官方脚本把失败原因写在
/// 这里），stderr 只留异常首行并剥掉 PowerShell 的命令回显前缀（"… | iex : 消息"）。
fn install_err_brief(stdout: &[u8], stderr: &[u8]) -> String {
    let sout = String::from_utf8_lossy(stdout);
    let mut parts: Vec<String> = vec![];
    if let Some(l) = sout.lines().rev().map(str::trim).find(|l| !l.is_empty()) {
        parts.push(l.to_string());
    }
    let serr = String::from_utf8_lossy(stderr);
    if let Some(l) = serr.lines().map(str::trim).find(|l| {
        !l.is_empty()
            && !l.starts_with('+')
            && !l.starts_with('~')
            && !l.starts_with("At line")
            && !l.starts_with("CategoryInfo")
            && !l.starts_with("FullyQualifiedErrorId")
    }) {
        let l = l.rfind("| iex : ").map(|i| &l[i + 8..]).unwrap_or(l).trim();
        if !l.is_empty() && !parts.iter().any(|p| p == l) {
            parts.push(l.to_string());
        }
    }
    if parts.is_empty() {
        parts.push("未知错误".into());
    }
    parts.join("；").chars().take(220).collect()
}

/// 安装失败详情（取尾不取头，纯函数）：合并 stdout+stderr，剥掉我们自己的命令回显与进度行
///（PowerShell 异常首行整句复读命令时只留 "| iex : " 之后的真实消息），保留末尾 ~600 字——
/// PowerShell/npm 的真正异常都在输出末尾，取头只能看到回显、真错误被截掉。
fn install_err_tail(stdout: &[u8], stderr: &[u8]) -> String {
    const MAX: usize = 600;
    let combined = format!("{}\n{}", String::from_utf8_lossy(stdout), String::from_utf8_lossy(stderr));
    let kept: Vec<String> = combined
        .lines()
        .filter_map(|raw| {
            let l = raw.trim();
            if l.is_empty() || l.starts_with("Installing Claude Code") {
                return None; // 我们/脚本的进度行
            }
            if l.contains("SecurityProtocol") || l.contains("install.ps1 | iex") {
                // 命令回显（PS 异常首行会整句复读命令）：只留 "| iex : " 之后的真实消息
                return l.rfind("| iex : ").map(|i| l[i + 8..].trim().to_string()).filter(|s| !s.is_empty());
            }
            Some(l.to_string())
        })
        .collect();
    let joined = kept.join("\n");
    if joined.trim().is_empty() {
        return "（无输出）".into();
    }
    let chars: Vec<char> = joined.chars().collect();
    if chars.len() > MAX {
        format!("…{}", chars[chars.len() - MAX..].iter().collect::<String>())
    } else {
        joined
    }
}

/// 本机 node 主版本号；没有 node / 跑不起来返回 0。
fn node_major() -> u32 {
    let mut c = std::process::Command::new(brains::resolve_bin("node"));
    c.arg("--version");
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000);
    }
    let Ok(out) = c.output() else { return 0 };
    if !out.status.success() {
        return 0;
    }
    String::from_utf8_lossy(&out.stdout)
        .trim()
        .trim_start_matches('v')
        .split('.')
        .next()
        .and_then(|s| s.parse().ok())
        .unwrap_or(0)
}

/// npm 全局装包：managed=Some((托管 node bin, data_dir)) 时用托管 npm——
/// prefix 指到 <data_dir>/node/npm-global、子进程 PATH 前置托管 bin（npm 脚本自己也要找 node）。
fn npm_install_global(pkg: &str, proxy: &[(String, String)], managed: Option<(&std::path::Path, &std::path::Path)>) -> Result<(), String> {
    let mut c = match managed {
        Some((bin, data_dir)) => {
            let mut c = std::process::Command::new(node_boot::managed_npm_exe(bin));
            let sep = if cfg!(windows) { ';' } else { ':' };
            let cur = std::env::var("PATH").unwrap_or_default();
            c.env("PATH", format!("{}{sep}{cur}", bin.to_string_lossy()));
            c.env("npm_config_prefix", node_boot::npm_prefix_dir(data_dir));
            c
        }
        None => std::process::Command::new(brains::resolve_bin("npm")),
    };
    c.args(["install", "-g", pkg]);
    c.envs(proxy.iter().cloned());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000);
    }
    let out = c.output().map_err(|e| format!("启动 npm 失败: {e}"))?;
    if out.status.success() {
        Ok(())
    } else {
        Err(install_err_tail(&out.stdout, &out.stderr))
    }
}

/// 官方原生安装脚本渠道（独立二进制，不需要 Node/npm）。
/// macOS/Linux: curl … install.sh | sh；Windows: PowerShell irm … install.ps1 | iex。
fn install_claude_native(proxy: &[(String, String)]) -> Result<(), String> {
    #[cfg(target_os = "windows")]
    let mut c = {
        let mut c = std::process::Command::new("powershell");
        // 老 Win10 的 PowerShell 5.1 默认不启用 TLS1.2，irm 会直接握手失败——先显式开启。
        c.args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command",
            "[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; irm https://claude.ai/install.ps1 | iex"]);
        c
    };
    #[cfg(not(target_os = "windows"))]
    let mut c = {
        let mut c = std::process::Command::new("/bin/sh");
        c.args(["-c", "curl -fsSL https://claude.ai/install.sh | sh"]);
        c
    };
    c.envs(proxy.iter().cloned());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000);
    }
    let out = c.output().map_err(|e| format!("启动安装失败: {e}"))?;
    if out.status.success() {
        Ok(())
    } else {
        // 取尾不取头：真正的异常在输出末尾（回显/进度行已剥），供 UI 详情区展示。
        Err(install_err_tail(&out.stdout, &out.stderr))
    }
}

/// npm 系 CLI 的统一安装（自举式托管 Node）：
/// ① 系统 Node≥18 → 系统 npm，行为与从前零变化；② 无 → 自举托管 Node（ensure：复用/下载/解压/校验，
/// 进度经 "node-boot" 事件推前端）再用托管 npm 装；③ 自举成功后进程 PATH 前置 + 用户级持久 PATH
///（幂等；写入失败不致命，消息里提示手动）。native_fallback（仅 claude）：以上全失败回落官方脚本。
async fn install_cli_via_node(app: AppHandle, data_dir: PathBuf, pkg: &'static str, cli_name: &'static str, native_fallback: bool) -> Result<String, String> {
    let proxy = system_proxy_env();
    let sys_node = tauri::async_runtime::spawn_blocking(node_major).await.map_err(|e| e.to_string())?;
    let mut errs: Vec<String> = Vec::new();
    if sys_node >= 18 {
        // 系统 Node 可用：老路径，零变化。
        let p2 = proxy.clone();
        match tauri::async_runtime::spawn_blocking(move || npm_install_global(pkg, &p2, None)).await.map_err(|e| e.to_string())? {
            Ok(()) => return Ok(format!("{cli_name} 安装完成（npm 渠道），正在重新检测…")),
            Err(e) => errs.push(format!("npm 渠道：{e}")),
        }
    } else {
        // 自举托管 Node：进度推给前端（下载 x% → 解压 → 校验 → 装 CLI）。
        let app2 = app.clone();
        let boot = node_boot::ensure(&data_dir, move |phase, pct| {
            let _ = app2.emit("node-boot", serde_json::json!({ "phase": phase, "pct": pct }));
        })
        .await;
        match boot {
            Ok(bin) => {
                let _ = app.emit("node-boot", serde_json::json!({ "phase": "npm", "pct": 0 }));
                let (p2, bin2, dd2) = (proxy.clone(), bin.clone(), data_dir.clone());
                let res = tauri::async_runtime::spawn_blocking(move || npm_install_global(pkg, &p2, Some((&bin2, &dd2))))
                    .await
                    .map_err(|e| e.to_string())?;
                match res {
                    Ok(()) => {
                        // 立即生效：进程 PATH 前置（探测/CLI 子进程继承）；再写用户级持久 PATH。
                        node_boot::prepend_process_path(&data_dir);
                        let mut msg = format!("{cli_name} 安装完成（托管 Node 渠道），正在重新检测…");
                        if let Err(e) = node_boot::register_user_path(&[bin, node_boot::npm_global_bin(&data_dir)]) {
                            msg.push_str(&format!("（已装好但未能写入系统 PATH，终端里使用需手动加：{e}）"));
                        }
                        return Ok(msg);
                    }
                    Err(e) => errs.push(format!("托管 npm 渠道：{e}")),
                }
            }
            Err(e) => errs.push(format!("托管 Node 自举：{e}")),
        }
    }
    if native_fallback {
        let p2 = proxy.clone();
        match tauri::async_runtime::spawn_blocking(move || install_claude_native(&p2)).await.map_err(|e| e.to_string())? {
            Ok(()) => return Ok(format!("{cli_name} 安装完成，正在重新检测…")),
            Err(e) => errs.push(format!("官方脚本渠道：{e}")),
        }
    }
    let mut msg = String::from("安装失败：多为网络问题（VPN/代理没覆盖下载域名）");
    if sys_node < 18 {
        msg.push_str("。更稳的替代：先装 Node.js(≥18)，再点一键安装将自动走 npm 通道（npm 遵循系统代理）");
    }
    msg.push_str(&format!("\n——详情（输出末尾）——\n{}", errs.join("\n")));
    Err(msg)
}

/// 一键安装 Claude Code：系统 Node≥18 优先 npm 官方包渠道；无 Node 自举托管 Node 走 npm；
/// 仍失败回落官方原生脚本。全渠道注入系统代理。
#[tauri::command]
async fn install_claude(app: AppHandle, state: tauri::State<'_, AppState>) -> Result<String, String> {
    install_cli_via_node(app, state.data_dir.clone(), "@anthropic-ai/claude-code", "Claude Code", true).await
}

/// 把系统代理补进本进程环境（已有环境变量不覆盖）：Windows 代理软件通常只设注册表
/// 系统代理、不设环境变量，而 claude/codex CLI 只认环境变量——不补的话 GUI 派生的
/// 对话子进程全部直连（用户被迫开 TUN 模式）。之后 spawn 的所有子进程自动继承。
fn apply_system_proxy_to_process() {
    for (k, v) in system_proxy_env() {
        std::env::set_var(k, v);
    }
}

/// 一键安装 Codex：系统 Node≥18 走系统 npm；无 Node 自举托管 Node 再装（无原生脚本兜底）。
#[tauri::command]
async fn install_codex(app: AppHandle, state: tauri::State<'_, AppState>) -> Result<String, String> {
    install_cli_via_node(app, state.data_dir.clone(), "@openai/codex", "Codex", false).await
}

/// 一键安装 Grok CLI（官方脚本 curl | bash，装到 ~/.grok/bin 并自动補 PATH）。
/// Windows 直下 grok 官方原生单文件 exe（URL 口径抄官方 install.sh，见 node_boot 注释）。
/// 跨平台编译（mac 上可编译审读、纯函数有单测），仅 Windows 分支调用。
#[cfg_attr(not(windows), allow(dead_code))]
async fn install_grok_win_binary(app: &AppHandle, home: &std::path::Path) -> Result<String, String> {
    let emit = |phase: &str, pct: u32| {
        let _ = app.emit("node-boot", serde_json::json!({ "phase": phase, "pct": pct }));
    };
    // ① 最新 stable 版本号（主源 → GCS 回退）
    let mut version: Option<String> = None;
    let mut last_err = String::new();
    for url in node_boot::grok_version_urls() {
        match node_boot::fetch_text(&url).await {
            Ok(body) => match node_boot::parse_grok_version(&body) {
                Some(v) => {
                    version = Some(v);
                    break;
                }
                None => last_err = format!("{url}: 返回的不是版本号"),
            },
            Err(e) => last_err = format!("{url}: {e}"),
        }
    }
    let Some(version) = version else {
        return Err(format!("安装失败：获取 grok 版本号失败，多为网络问题（代理需覆盖 x.ai）\n——详情——\n{last_err}"));
    };
    // ② 下载单文件 exe（带真实进度）
    let mut data: Option<Vec<u8>> = None;
    for url in node_boot::grok_win_exe_urls(&version, std::env::consts::ARCH) {
        match node_boot::download(&url, &|_, pct| emit("grok-download", pct)).await {
            Ok(b) => {
                data = Some(b);
                break;
            }
            Err(e) => last_err = format!("{url}: {e}"),
        }
    }
    let Some(data) = data else {
        return Err(format!("安装失败：下载 grok 失败，多为网络问题（代理需覆盖 x.ai 与 storage.googleapis.com）\n——详情——\n{last_err}"));
    };
    // ③ 官方安装布局：~/.grok/bin/{grok.exe, agent.exe}
    let bin_dir = node_boot::grok_win_bin_dir(home);
    std::fs::create_dir_all(&bin_dir).map_err(|e| format!("创建 {}: {e}", bin_dir.display()))?;
    for name in ["grok.exe", "agent.exe"] {
        std::fs::write(bin_dir.join(name), &data).map_err(|e| format!("写入 {name} 失败（若在运行请先退出 grok）: {e}"))?;
    }
    // ④ 校验 + PATH：进程内前置立即可被探测/拉起；用户级持久 PATH 幂等追加（失败不致命）。
    emit("grok-verify", 0);
    node_boot::prepend_path_dirs(&[bin_dir.clone()]);
    let mut c = std::process::Command::new(bin_dir.join("grok.exe"));
    c.arg("--version");
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        c.creation_flags(0x0800_0000);
    }
    let ok = c.output().map(|o| o.status.success()).unwrap_or(false);
    if !ok {
        return Err("安装失败：下载的 grok 无法运行（--version 未通过），可能被安全软件拦截——请重试或手动安装".into());
    }
    let mut msg = format!("Grok CLI {version} 安装完成，正在重新检测…");
    if let Err(e) = node_boot::register_user_path(&[bin_dir]) {
        msg.push_str(&format!("（已装好但未能写入系统 PATH，终端里使用需手动加：{e}）"));
    }
    Ok(msg)
}

/// 一键安装 Grok CLI：mac/linux 走官方脚本（curl | bash）；Windows 直下官方原生 exe——
/// 不再要求 Git Bash（侦查：官方无 npm 包，但 x.ai/cli 有 windows 单文件产物直链）。
#[tauri::command]
async fn install_grok(app: AppHandle) -> Result<String, String> {
    #[cfg(windows)]
    {
        let home = std::env::var("USERPROFILE").map_err(|_| "拿不到用户目录（USERPROFILE）".to_string())?;
        return install_grok_win_binary(&app, std::path::Path::new(&home)).await;
    }
    #[cfg(not(windows))]
    {
        let _ = &app;
        tauri::async_runtime::spawn_blocking(|| {
            let mut c = std::process::Command::new("/bin/bash");
            c.args(["-c", "curl -fsSL https://x.ai/cli/install.sh | bash"]);
            c.envs(system_proxy_env());
            let out = c.output().map_err(|e| format!("启动安装脚本失败: {e}"))?;
            if out.status.success() {
                Ok("Grok CLI 安装完成，正在重新检测…".to_string())
            } else {
                Err(format!(
                    "安装失败：多为网络问题（代理需覆盖 x.ai）\n——详情（输出末尾）——\n{}",
                    install_err_tail(&out.stdout, &out.stderr)
                ))
            }
        })
        .await
        .map_err(|e| e.to_string())?
    }
}

/// 一键安装 wrangler（npm i -g wrangler）。npm 走已修复的 PATH（Windows 上 .cmd 必须
/// 完整路径才能 spawn）；同样注入系统代理。可能耗时半分钟到一两分钟。
#[tauri::command]
async fn install_wrangler() -> Result<String, String> {
    tauri::async_runtime::spawn_blocking(|| {
        let mut c = std::process::Command::new(brains::resolve_bin("npm"));
        c.args(["install", "-g", "wrangler"]);
        c.envs(system_proxy_env());
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

/// 排期条目的前台预览链接（短期有效）。
#[tauri::command]
async fn scheduled_preview_url(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    site_slug: String,
    id: i64,
) -> Result<String, String> {
    let conn = state.conns.get(&conn_id)?;
    scheduled::preview_url(&conn, &site_slug, id).await
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

/// 跨厂商切换的会话字段改写：brain+model 一起换。换厂商时清空 session_ref——
/// 旧厂商的 session 另一家恢复不了，留着只会让失败路径拿旧 ref 去 resume 新厂商；
/// 调用方随后必须走 rebuild_session 以历史摘要重建续跑（begin_rebuild 本就不要求已有 session）。
/// effort 保留；codex 不支持逐命令确认，ask/auto 档按启动器同规则落到 full
///（claude/grok 都有逐命令批准，互切时 perm 原样保留）。
///
/// ★ `is_ssh` 例外：远程连接下**绝不能**把 ask/auto 改写成 full。那里 codex 虽然仍没有本地逐命令闸，
/// 但走 AI 桥的远程命令**照样逐条弹卡**（闸在 bridge.rs，与厂商无关）——那是 codex 仅剩的一道闸。
/// 把它也拿掉，用户就会「明明选了自动，却在无人确认地动真机」。这是评审里的 critical，别改回去。
fn apply_brain_switch(c: &mut Conversation, brain: &str, model: &str, is_ssh: bool) {
    if c.brain != brain {
        c.session_ref = String::new();
        if brain == "codex" && !is_ssh && (c.perm_mode == "ask" || c.perm_mode == "auto") {
            c.perm_mode = "full".into();
        }
    }
    c.brain = brain.into();
    c.model = model.into();
}

/// 会话内跨厂商换模型（含同厂商，前端只在跨厂商时调它）。运行中拒绝：
/// 本轮已带旧厂商启动，中途换 brain 会让收尾逻辑把新厂商会话和旧 session 缝在一起。
#[tauri::command]
fn set_conversation_brain_model(
    state: tauri::State<'_, AppState>,
    conv_id: String,
    brain: String,
    model: String,
) -> Result<Option<Conversation>, String> {
    if brain != "claude" && brain != "codex" && brain != "grok" {
        return Err(format!("未知厂商：{brain}"));
    }
    if let Some(c) = state.convos.get(&conv_id) {
        if c.status == "running" {
            return Err("上一轮还在进行中，请稍候".into());
        }
    }
    // ssh 对话切厂商时不许把 ask/auto 改写成 full（见 apply_brain_switch）——
    // 前端切完会立刻 rebuild_session 自动重跑，静默升档等于一次点击就无确认地动了真机。
    let is_ssh = state
        .convos
        .get(&conv_id)
        .and_then(|c| state.conns.get(&c.conn_id).ok())
        .map(|x| x.kind == "ssh")
        .unwrap_or(false);
    state
        .convos
        .mutate(&conv_id, now_secs(), move |c| apply_brain_switch(c, &brain, &model, is_ssh))
}

/// 会话进行中切换权限档位（plan/ask/auto/full）：仅改存储，下一轮 send_message 读到即用。
/// 注意 claude 把权限档位钉在会话创建时，改基座模式要新对话才全生效；但钩子每轮重生成，
/// 故「自动/询问」的拦截逻辑仍随时可调（详见 permit.rs）。
#[tauri::command]
fn set_conversation_effort(
    state: tauri::State<'_, AppState>,
    conv_id: String,
    effort: String,
) -> Result<Option<convo::Conversation>, String> {
    state
        .convos
        .mutate(&conv_id, now_secs(), move |c| c.effort = effort)
}

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
    site_slugs: Vec<String>,
    site_names: Vec<String>,
    task_type: String,
    brain: String,
    model: String,
    effort: String,
    title: String,
    prompt: String,
    interval_minutes: u64,
    first_run: i64,
    enabled: bool,
) -> Result<ScheduledTask, String> {
    upsert_task(
        &state.conns, &state.tasks, id, conn_id, site_slugs, site_names, task_type, brain, model,
        effort, title, prompt, interval_minutes, first_run, enabled,
    )
}

/// 构建 + 落库一个定时任务：save_task 命令与「托管」配套任务创建共用的核心逻辑。
#[allow(clippy::too_many_arguments)]
fn upsert_task(
    conns: &pack::ConnStore,
    tasks: &tasks::TaskStore,
    id: Option<String>,
    conn_id: String,
    site_slugs: Vec<String>,
    site_names: Vec<String>,
    task_type: String,
    brain: String,
    model: String,
    effort: String,
    title: String,
    prompt: String,
    interval_minutes: u64,
    first_run: i64,
    enabled: bool,
) -> Result<ScheduledTask, String> {
    let site_slugs: Vec<String> = site_slugs.iter().map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).collect();
    if site_slugs.is_empty() {
        return Err("至少选择一个站点".into());
    }
    if prompt.trim().is_empty() {
        return Err("请填写要让它做的事".into());
    }
    if interval_minutes < 1 {
        return Err("周期至少 1 分钟".into());
    }
    // 连接必须存在，否则这个任务每次运行都会失败——直接在保存时拦下。
    let conn = conns.get(&conn_id).map_err(|_| "所选连接不存在".to_string())?;
    // ★ 远程连接不许挂定时任务：定时任务**无人值守、强制 full 档**（见 fire_task），
    // 那等于把一台真机的 root shell 交给没人看着的 AI 定期折腾。这个组合不提供。
    if conn.kind == "ssh" {
        return Err("远程连接不支持定时任务：无人值守的自动执行会直接改动你的服务器，风险太大。".into());
    }
    let conn_name = conn.name;
    let now = now_secs();
    let existing = id.as_ref().and_then(|i| tasks.get(i));
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
        // 首个站点写进旧字段：列表展示与旧版数据结构兼容
        site_slug: site_slugs[0].clone(),
        site_name: site_names.first().cloned().unwrap_or_else(|| site_slugs[0].clone()),
        task_type,
        brain,
        model,
        effort,
        site_slugs,
        site_names,
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
        history: existing.as_ref().map(|e| e.history.clone()).unwrap_or_default(),
        created_at: existing.as_ref().map(|e| e.created_at).unwrap_or(now),
        updated_at: now,
    };
    tasks.upsert(task.clone())?;
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
    let mstore = state.managed.clone();
    let firing = state.firing.clone();
    let data_dir = state.data_dir.clone();
    let ssh = state.ssh.clone();
    tauri::async_runtime::spawn(async move {
        fire_task(app, conns, convos, runs, tstore, mstore, task, data_dir, ssh).await;
        firing.lock().unwrap().remove(&id);
    });
    Ok(())
}

// ---- 托管站点（L0 试运行 / L1 自动发布 / L2 自动发布+抽检）----

#[tauri::command]
fn managed_list(state: tauri::State<'_, AppState>) -> Vec<managed::ManagedSite> {
    state.managed.list()
}

/// 系统通知（托管熔断/降级等关键事件用；失败静默）。
fn notify_user(app: &AppHandle, title: &str, body: String) {
    let _ = app.notification().builder().title(title).body(body).show();
}

/// 重新组装并回写「每日内容」与「每周周报」任务的 prompt——plan / 周上限 / 打回记录 / 等级 /
/// 审计要点任一变化后调用，否则任务还带着旧边界跑。审计任务 prompt 是静态的，不用同步。
/// 后台任务（fire_task）没有 AppState，签名收 TaskStore。
fn sync_daily_prompt(tasks: &tasks::TaskStore, m: &managed::ManagedSite) {
    if let Some(tid) = m.task_ids.first() {
        let p = managed::daily_prompt(&m.site_name, &m.plan, m.weekly_post_limit, &m.review_notes, &m.level, m.weekly_edit_limit, &m.audit_notes);
        let _ = tasks.mutate(tid, |t| {
            t.prompt = p.clone();
            t.updated_at = now_secs();
        });
    }
    // 周报 prompt 带计划摘要（「计划关键词 vs 实际曝光词偏差」的对照基准），计划变了要跟着换。
    if let Some(tid) = m.task_ids.get(2) {
        let p = managed::report_prompt(&m.site_name, &m.plan);
        let _ = tasks.mutate(tid, |t| {
            t.prompt = p.clone();
            t.updated_at = now_secs();
        });
    }
}

/// 开启托管：存记录 + 自动创建两个配套定时任务（每日内容 1440 分钟 / 每周审计 10080 分钟）。
/// 任务标题带「托管 · 」前缀，在定时任务视图里也可见可管。
#[allow(clippy::too_many_arguments)]
#[tauri::command]
fn managed_enable(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    site_slug: String,
    site_name: String,
    plan: String,
    weekly_post_limit: u32,
    weekly_edit_limit: u32,
    level: String,
    token_weekly_budget: u64,
    brain: String,
    model: String,
    effort: String,
) -> Result<managed::ManagedSite, String> {
    enable_managed(
        &state.conns, &state.tasks, &state.managed,
        conn_id, site_slug, site_name, plan, weekly_post_limit, weekly_edit_limit, level,
        token_weekly_budget, brain, model, effort,
    )
}

fn valid_level(level: &str) -> bool {
    matches!(level, "l0" | "l1" | "l2" | "l3")
}

/// managed_enable 的核心（纯 store 版，可单测）：厂商/模型/强度既透传给三个配套任务
///（每日内容/每周审计/每周周报），也存进托管记录（卡片展示 + 后续同步用）。
#[allow(clippy::too_many_arguments)]
fn enable_managed(
    conns: &pack::ConnStore,
    tasks: &tasks::TaskStore,
    managed_store: &managed::ManagedStore,
    conn_id: String,
    site_slug: String,
    site_name: String,
    plan: String,
    weekly_post_limit: u32,
    weekly_edit_limit: u32,
    level: String,
    token_weekly_budget: u64,
    brain: String,
    model: String,
    effort: String,
) -> Result<managed::ManagedSite, String> {
    let site_slug = site_slug.trim().to_string();
    if site_slug.is_empty() {
        return Err("请选择要托管的站点".into());
    }
    if managed_store.find_site(&conn_id, &site_slug).is_some() {
        return Err("该站点已在托管中".into());
    }
    let level = if valid_level(&level) { level } else { "l0".to_string() };
    let site_name = if site_name.trim().is_empty() { site_slug.clone() } else { site_name.trim().to_string() };
    // 配额爬坡：刚开启（第 0 天）上界 7 篇/周（满 30 天 14、60 天后 50，managed::ramp_cap），
    // 防新站短期批量灌内容被搜索引擎判责；UI 侧超过 7 会提示将被钳。
    let limit = weekly_post_limit.clamp(1, managed::ramp_cap(0));
    let edit_limit = weekly_edit_limit.clamp(1, 20);
    let daily = upsert_task(
        conns, tasks, None, conn_id.clone(),
        vec![site_slug.clone()], vec![site_name.clone()],
        "article".into(), brain.clone(), model.clone(), effort.clone(),
        format!("托管 · 每日内容 · {site_name}"),
        managed::daily_prompt(&site_name, &plan, limit, &[], &level, edit_limit, ""),
        1440, 0, true,
    )?;
    let audit = upsert_task(
        conns, tasks, None, conn_id.clone(),
        vec![site_slug.clone()], vec![site_name.clone()],
        "free".into(), brain.clone(), model.clone(), effort.clone(),
        format!("托管 · 每周审计 · {site_name}"),
        managed::audit_prompt(&site_name),
        10080, 0, true,
    )?;
    let report = upsert_task(
        conns, tasks, None, conn_id.clone(),
        vec![site_slug.clone()], vec![site_name.clone()],
        "free".into(), brain.clone(), model.clone(), effort.clone(),
        format!("托管 · 每周周报 · {site_name}"),
        managed::report_prompt(&site_name, &plan),
        10080, 0, true,
    )?;
    let now = now_secs();
    let m = managed::ManagedSite {
        id: uuid::Uuid::new_v4().to_string(),
        conn_id,
        site_slug,
        site_name,
        level,
        weekly_post_limit: limit,
        weekly_edit_limit: edit_limit,
        plan: plan.trim().to_string(),
        brain,
        model,
        effort,
        task_ids: vec![daily.id, audit.id, report.id],
        paused: false,
        review_notes: vec![],
        token_weekly_budget,
        fused_at: 0,
        review_events: vec![],
        demote_note: String::new(),
        audit_notes: String::new(),
        enabled_at: now,
        reports: vec![],
        created_at: now,
        updated_at: now,
    };
    managed_store.upsert(m.clone())?;
    Ok(m)
}

/// 调整托管等级（l0/l1/l2）：手动调级清空自动降级说明，并同步每日任务 prompt。
#[tauri::command]
fn managed_set_level(
    state: tauri::State<'_, AppState>,
    id: String,
    level: String,
) -> Result<Option<managed::ManagedSite>, String> {
    if !valid_level(&level) {
        return Err(format!("未知等级：{level}"));
    }
    let updated = state.managed.mutate(&id, now_secs(), |m| {
        m.level = level.clone();
        m.demote_note = String::new();
    })?;
    if let Some(m) = &updated {
        sync_daily_prompt(&state.tasks, m);
    }
    Ok(updated)
}

/// 调整 L3 的每周存量修改配额（1-20）。prompt 声明配额由 AI 自数、周报如实汇报；
/// Pilot 侧另有硬闸：managed_prefire 按 week_stats 的 edited 口径实测本周已改篇数，
/// 配额用完时往当天 prompt 注入「禁止修改存量」权威行（任务不整体跳过，常规创作照做）。
#[tauri::command]
fn managed_set_edit_limit(
    state: tauri::State<'_, AppState>,
    id: String,
    limit: u32,
) -> Result<Option<managed::ManagedSite>, String> {
    let limit = limit.clamp(1, 20);
    let updated = state.managed.mutate(&id, now_secs(), |m| m.weekly_edit_limit = limit)?;
    if let Some(m) = &updated {
        sync_daily_prompt(&state.tasks, m);
    }
    Ok(updated)
}

/// 暂停/恢复托管＝启停全部配套任务（恢复时把 next_run 推进到未来，避免立刻补跑）。
fn set_managed_paused(state: &AppState, id: &str, paused: bool) -> Result<Option<managed::ManagedSite>, String> {
    let now = now_secs();
    let updated = state.managed.mutate(id, now, |m| m.paused = paused)?;
    if let Some(m) = &updated {
        for tid in &m.task_ids {
            let _ = state.tasks.mutate(tid, |t| {
                t.enabled = !paused;
                if !paused {
                    t.advance_past(now);
                }
                t.updated_at = now;
            });
        }
    }
    Ok(updated)
}

#[tauri::command]
fn managed_pause(state: tauri::State<'_, AppState>, id: String) -> Result<Option<managed::ManagedSite>, String> {
    set_managed_paused(&state, &id, true)
}

/// 恢复托管：重启配套任务，并解除预算熔断标记（熔断恢复只能走这里=手动）。
#[tauri::command]
fn managed_resume(state: tauri::State<'_, AppState>, id: String) -> Result<Option<managed::ManagedSite>, String> {
    let _ = state.managed.mutate(&id, now_secs(), |m| m.fused_at = 0)?;
    set_managed_paused(&state, &id, false)
}

/// 关闭托管：删掉配套定时任务 + 托管记录。站点内容（含草稿）一概不动。
#[tauri::command]
fn managed_disable(state: tauri::State<'_, AppState>, id: String) -> Result<(), String> {
    let Some(m) = state.managed.get(&id) else { return Ok(()) };
    for tid in &m.task_ids {
        let _ = state.tasks.remove(tid);
    }
    state.managed.remove(&id)
}

/// 记录一次「打回」：只记理由（草稿本身不动），并立刻把打回意见同步进每日任务的 prompt。
/// 同时记审核事件并做**自动降级**判定：连续 3 次打回或最近 10 条打回占比≥50%（样本≥5）
/// → L1/L2 自动降回 L0 + 系统通知 + 卡上黄字说明。
#[tauri::command]
fn managed_record_reject(
    state: tauri::State<'_, AppState>,
    app: AppHandle,
    id: String,
    post_id: i64,
    title: String,
    reason: String,
) -> Result<Option<managed::ManagedSite>, String> {
    if reason.trim().is_empty() {
        return Err("请写一句打回理由（会注入后续任务让它规避）".into());
    }
    let now = now_secs();
    let mut updated = state.managed.mutate(&id, now, |m| {
        m.review_notes.insert(0, managed::ReviewNote {
            ts: now,
            post_id,
            title: title.trim().to_string(),
            reason: reason.trim().to_string(),
        });
        m.review_events.insert(0, managed::ReviewEvent { ts: now, approved: false });
    })?;
    // 自动降级判定（先取出要用的数据再改 updated，避免自引用）。
    let demote = updated.as_ref().and_then(|m| {
        let target = managed::demote_target(&m.level)?;
        let why = managed::should_demote(&m.review_events)?;
        Some((m.site_name.clone(), why, target))
    });
    if let Some((site_name, why, target)) = demote {
        let date = chrono::Local::now().format("%m-%d %H:%M");
        let label = managed::level_label(target);
        updated = state.managed.mutate(&id, now, |x| {
            x.level = target.into();
            x.demote_note = format!("已自动降到 {label}：{why}（{date}）。整改后可手动调回。");
        })?;
        notify_user(&app, "托管已自动降级", format!("「{site_name}」{why}，已降到 {label}。"));
    }
    if let Some(m) = &updated {
        sync_daily_prompt(&state.tasks, m);
    }
    Ok(updated)
}

/// 保存 90 天计划（向导里生成/手写，之后也可在托管卡里改），并同步每日任务 prompt。
#[tauri::command]
fn managed_plan_save(
    state: tauri::State<'_, AppState>,
    id: String,
    plan: String,
) -> Result<Option<managed::ManagedSite>, String> {
    let updated = state.managed.mutate(&id, now_secs(), |m| m.plan = plan.trim().to_string())?;
    if let Some(m) = &updated {
        sync_daily_prompt(&state.tasks, m);
    }
    Ok(updated)
}

/// 托管开启前的机械预检（向导选站后调用）：存量条数 + 统计可用性 → 软警示列表。
/// Err（网络等）由前端静默降级——不警示也不拦，预检绝不能挡住向导。
#[tauri::command]
async fn managed_precheck(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    site_slug: String,
) -> Result<managed::PrecheckResult, String> {
    let conn = state.conns.get(&conn_id)?;
    managed::precheck(&conn, &site_slug).await
}

/// 待审队列：该站全部草稿。
#[tauri::command]
async fn managed_drafts(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    site_slug: String,
) -> Result<Vec<managed::DraftItem>, String> {
    let conn = state.conns.get(&conn_id)?;
    managed::list_drafts(&conn, &site_slug).await
}

/// 批准发布：只把 status 改为 published（部分更新，其他字段不动）。
/// 成功后记一条「批准」审核事件（降级判定的分母）。
#[tauri::command]
async fn managed_publish(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    site_slug: String,
    id: i64,
) -> Result<(), String> {
    let conn = state.conns.get(&conn_id)?;
    managed::publish_post(&conn, &site_slug, id).await?;
    if let Some(m) = state.managed.find_site(&conn_id, &site_slug) {
        let now = now_secs();
        let _ = state.managed.mutate(&m.id, now, |x| {
            x.review_events.insert(0, managed::ReviewEvent { ts: now, approved: true });
        });
    }
    Ok(())
}

/// 本周托管 token 用量：配套任务运行记录（ts≥周一）关联的会话 total_tokens 求和。
/// 每次任务触发都开全新对话，所以单个会话的累计＝那次运行的用量，不会重复计。
fn managed_week_tokens(
    tstore: &tasks::TaskStore,
    convos: &convo::ConvStore,
    m: &managed::ManagedSite,
    week_start: u64,
) -> u64 {
    let mut entries: Vec<(u64, u64)> = Vec::new();
    for tid in &m.task_ids {
        let Some(t) = tstore.get(tid) else { continue };
        for r in &t.history {
            for s in &r.sites {
                if s.conv_id.is_empty() {
                    continue;
                }
                if let Some(c) = convos.get(&s.conv_id) {
                    entries.push((r.ts, c.total_tokens));
                }
            }
        }
    }
    managed::sum_week_tokens(&entries, week_start)
}

/// 周报数字卡：本周发布数 / 草稿数 / token 用量 / 配套任务近况。
#[tauri::command]
async fn managed_summary(
    state: tauri::State<'_, AppState>,
    id: String,
) -> Result<managed::ManagedSummary, String> {
    let m = state.managed.get(&id).ok_or("托管记录不存在")?;
    let conn = state.conns.get(&m.conn_id)?;
    let briefs: Vec<managed::TaskBrief> = m
        .task_ids
        .iter()
        .filter_map(|tid| state.tasks.get(tid))
        .map(|t| managed::TaskBrief {
            id: t.id,
            title: t.title,
            enabled: t.enabled,
            last_status: t.last_status,
            last_run: t.last_run,
            next_run: t.next_run,
            last_conv_id: t.last_conv_id,
        })
        .collect();
    let stats = managed::week_stats(&conn, &m.site_slug).await?;
    let week_tokens = managed_week_tokens(&state.tasks, &state.convos, &m, stats.week_start.max(0) as u64);
    Ok(managed::ManagedSummary {
        published_this_week: stats.published_this_week,
        drafts: stats.drafts_total,
        drafts_new: stats.drafts_new,
        week_start: stats.week_start,
        week_tokens,
        tasks: briefs,
    })
}

/// 托管任务的触发前裁决：跳过（周上限硬闸/预算熔断）或放行（可附带注入 prompt 的数据块）。
enum ManagedGate {
    Proceed(Option<String>),
    Skip(String),
}

/// 只对「属于某条托管记录」的任务生效；普通任务直接放行。
/// - 预算熔断：任何配套任务触发前先算本周 token；触顶→暂停全部配套任务+通知+记熔断，本次跳过。
/// - 每日内容：week_stats 实测本周产出（发布+新增草稿）≥ 上限→跳过；未达→注入权威计数行。
///   实测拉取失败（网络等）**放行**——prompt 里保留了 AI 自查的兜底条款，不能让瞬时故障停摆日更。
/// - 每周周报：注入 Pilot 组装的【本周实测数据】块（拉取失败则注明数据不可用）。
async fn managed_prefire(
    app: &AppHandle,
    conns: &pack::ConnStore,
    convos: &convo::ConvStore,
    tstore: &tasks::TaskStore,
    mstore: &managed::ManagedStore,
    task: &ScheduledTask,
) -> ManagedGate {
    let Some(m) = mstore.list().into_iter().find(|m| m.task_ids.contains(&task.id)) else {
        return ManagedGate::Proceed(None);
    };
    let ws = managed::week_start_local().max(0) as u64;
    // 预算熔断（对全部配套任务生效；恢复只能手动 resume）
    if m.token_weekly_budget > 0 {
        let used = managed_week_tokens(tstore, convos, &m, ws);
        if managed::budget_exceeded(used, m.token_weekly_budget) {
            if m.fused_at == 0 {
                let now = now_secs();
                let _ = mstore.mutate(&m.id, now, |x| {
                    x.fused_at = now;
                    x.paused = true;
                });
                for tid in &m.task_ids {
                    let _ = tstore.mutate(tid, |t| {
                        t.enabled = false;
                        t.updated_at = now;
                    });
                }
                notify_user(app, "托管预算已熔断", format!(
                    "「{}」本周 token 用量 {used} 已达预算 {}，配套任务已全部暂停；恢复需在托管卡手动操作。",
                    m.site_name, m.token_weekly_budget
                ));
            }
            return ManagedGate::Skip(format!("预算已熔断（{used}/{}），恢复需手动", m.token_weekly_budget));
        }
    }
    let conn = match conns.get(&m.conn_id) {
        Ok(c) => c,
        Err(_) => return ManagedGate::Proceed(None),
    };
    // 每日内容任务：周上限硬闸 + 权威计数注入（L3 再注入存量修改配额硬闸行）
    if m.task_ids.first() == Some(&task.id) {
        match managed::week_stats(&conn, &m.site_slug).await {
            Ok(stats) => {
                let output = stats.published_this_week + stats.drafts_new;
                if let Some(reason) = managed::weekly_cap_skip(output, m.weekly_post_limit) {
                    return ManagedGate::Skip(reason);
                }
                let mut extra = managed::weekly_count_line(output, m.weekly_post_limit);
                // L3 存量修改硬闸：edited 口径（updated_at≥周一 && published_at<周一）实测本周已改篇数。
                // 配额用完**不 Skip 整个任务**——只注入「禁止修改存量」权威行，常规创作照做。
                if m.level == "l3" {
                    extra.push('\n');
                    extra.push_str(&managed::edit_cap_line(stats.edited_count, m.weekly_edit_limit));
                }
                return ManagedGate::Proceed(Some(extra));
            }
            Err(_) => return ManagedGate::Proceed(None),
        }
    }
    // 每周周报任务：注入真实数据块
    if m.task_ids.get(2) == Some(&task.id) {
        let block = match managed::week_stats(&conn, &m.site_slug).await {
            Ok(stats) => {
                let task_lines: Vec<String> = m
                    .task_ids
                    .iter()
                    .filter_map(|tid| tstore.get(tid))
                    .map(|t| {
                        let status = if t.last_run == 0 {
                            "还没跑过".to_string()
                        } else {
                            format!("上次{}（共 {} 次）", if t.last_status == "ok" { "成功" } else { "失败" }, t.runs)
                        };
                        format!("{}：{status}", t.title)
                    })
                    .collect();
                let facts = managed::WeekFacts {
                    published: stats.published_this_week,
                    drafts_total: stats.drafts_total,
                    drafts_new: stats.drafts_new,
                    weekly_limit: m.weekly_post_limit,
                    week_tokens: managed_week_tokens(tstore, convos, &m, ws),
                    budget: m.token_weekly_budget,
                    task_lines,
                    reject_reasons: m
                        .review_notes
                        .iter()
                        .take(5)
                        .map(|n| format!("《{}》：{}", n.title, n.reason))
                        .collect(),
                    published_titles: if m.level == "l2" || m.level == "l3" { stats.published_titles } else { vec![] },
                    edit_limit: if m.level == "l3" { m.weekly_edit_limit } else { 0 },
                    edited_titles: if m.level == "l3" { stats.edited_titles } else { vec![] },
                };
                managed::weekly_report_data(&facts)
            }
            Err(e) => format!("【本周实测数据】拉取失败（{e}）——请在周报开头注明本周数据不可用，只做定性总结。"),
        };
        return ManagedGate::Proceed(Some(block));
    }
    ManagedGate::Proceed(None)
}

/// 本地时间的「月-日 时:分」（限额顺延提示用）。
fn fmt_ts_local(ts: u64) -> String {
    use chrono::TimeZone;
    chrono::Local
        .timestamp_opt(ts as i64, 0)
        .single()
        .map(|d| d.format("%m-%d %H:%M").to_string())
        .unwrap_or_default()
}

/// 单站执行结果（多站任务撞限止损用）。
enum SiteRun {
    Done(Box<Conversation>),
    Failed(String),
    /// 同一轮里前面的站撞了限额：这站直接顺延，不再白跑。
    LimitSkipped,
}

/// 触发一次任务：对每个目标站点各开一个全新对话跑 task.prompt（顺序执行，互不阻断），
/// 回写任务的 last_* 并通知。托管配套任务先过 managed_prefire（硬闸/熔断/数据注入）。
/// 订阅限额感知：触发前 brain 在限额期 → 整体顺延（非失败）；运行中撞限 → 剩余站止损、
/// next_run 顺延到恢复点 + 抖动；系统通知每 brain 每限额窗口只发一次。
#[allow(clippy::too_many_arguments)]
async fn fire_task(
    app: AppHandle,
    conns: pack::ConnStore,
    convos: convo::ConvStore,
    runs: agent::RunRegistry,
    tstore: tasks::TaskStore,
    mstore: managed::ManagedStore,
    task: ScheduledTask,
    data_dir: PathBuf,
    ssh: ssh::SshSessions,
) {
    // ① 触发前拦截：限额期内不跑、不记失败（runs 不加），next_run 顺延，历史记「顺延」条目。
    // 托管配套任务同样先走这里（在 prefire 之前），Skip 语义不变、通知有节流不会双发。
    let lstore = limits::LimitStore::new(&data_dir);
    {
        let now = now_secs();
        if let Some(entry) = lstore.active(&task.brain, now) {
            let next = limits::defer_next_run(entry.reset_ts, &task.id);
            let msg = format!("订阅限额中，本次未运行——已顺延到 {} 后重试", fmt_ts_local(entry.reset_ts));
            let _ = tstore.mutate(&task.id, |x| {
                if next > x.next_run {
                    x.next_run = next;
                }
                x.last_run = now;
                x.last_status = "ok".into();
                x.last_summary = msg.clone();
                x.history.insert(0, tasks::TaskRun { ts: now, ok: true, summary: msg.clone(), sites: vec![], deferred: true });
                x.history.truncate(20);
            });
            if lstore.claim_notify(&task.brain, now) {
                notify_user(&app, "订阅限额，定时任务已顺延", format!(
                    "{} 处于限额期（预计 {} 恢复），「{}」等定时任务将自动顺延继续。",
                    task.brain, fmt_ts_local(entry.reset_ts), task.title
                ));
            }
            return;
        }
    }
    let prompt_extra = match managed_prefire(&app, &conns, &convos, &tstore, &mstore, &task).await {
        ManagedGate::Proceed(extra) => extra,
        ManagedGate::Skip(reason) => {
            // 跳过也是一次「运行」：写记录让用户在任务卡上看得到原因；next_run 已由调用方推进。
            let now = now_secs();
            let _ = tstore.mutate(&task.id, |x| {
                x.last_run = now;
                x.runs += 1;
                x.last_status = "ok".into();
                x.last_summary = reason.clone();
                x.history.insert(0, tasks::TaskRun { ts: now, ok: true, summary: reason.clone(), sites: vec![], deferred: false });
                x.history.truncate(20);
            });
            return;
        }
    };
    let effective_prompt = match &prompt_extra {
        Some(extra) => format!("{}\n\n{extra}", task.prompt),
        None => task.prompt.clone(),
    };
    let targets = task.targets();
    let multi = targets.len() > 1;
    // 并发执行，信号量限流：CLI 进程以网络等待为主，但每个要吃几百 MB 内存，
    // 上限按核数自动定（核数/2，钳 2–6）。同连接共享工作目录，写盘冲突概率低但非零，
    // 不放开到全并发。
    let sem = std::sync::Arc::new(tokio::sync::Semaphore::new(task_concurrency()));
    // ③ 多站中途撞限的止损旗：某站检测到限额（登记全局）后，同轮尚未开跑的站直接标「顺延」。
    let limit_flag: Arc<Mutex<Option<u64>>> = Arc::new(Mutex::new(None));
    let mut handles = Vec::with_capacity(targets.len());
    for (slug, name) in targets.clone() {
        let sem = sem.clone();
        let limit_flag = limit_flag.clone();
        let lstore2 = lstore.clone();
        let (conns, convos, runs, data_dir, ssh) = (conns.clone(), convos.clone(), runs.clone(), data_dir.clone(), ssh.clone());
        let (conn_id, task_type, brain, model, effort, prompt) = (
            task.conn_id.clone(), task.task_type.clone(), task.brain.clone(),
            task.model.clone(), task.effort.clone(), effective_prompt.clone(),
        );
        handles.push(tauri::async_runtime::spawn(async move {
            let _permit = sem.acquire_owned().await;
            if limit_flag.lock().unwrap().is_some() {
                return (slug, SiteRun::LimitSkipped);
            }
            let conv_id = uuid::Uuid::new_v4().to_string();
            // 后台运行没有前端接收方，用一个丢弃事件的 Channel。
            let sink: Channel<agent::TurnEvent> = Channel::new(|_| Ok(()));
            let brain_for_limit = brain.clone();
            // 定时任务无人值守：只能全自动（询问/自动档会卡在等批准，永远回不来）。
            let res = create_conversation(
                conns, convos, runs, conv_id,
                conn_id, slug.clone(), name, vec![], vec![],
                task_type, brain, model,
                "full".into(), effort, prompt, sink, data_dir, ssh,
            )
            .await;
            match res {
                Ok(c) => {
                    // 撞限检测：登记全局（create_conversation 里已登记，这里补拿生效 reset）并竖旗止损。
                    if let Some(reset) = c.messages.iter().rev().find(|m| m.role == "assistant").and_then(|m| m.limit_reset) {
                        let eff = lstore2.register(&brain_for_limit, reset, now_secs());
                        *limit_flag.lock().unwrap() = Some(eff);
                    }
                    (slug, SiteRun::Done(Box::new(c)))
                }
                Err(e) => (slug, SiteRun::Failed(e)),
            }
        }));
    }
    let mut ok_count = 0usize;
    let mut first_err = String::new();
    let mut last_conv = String::new();
    let mut last_summary = String::new();
    // 最后一条 assistant 消息全文（审计/周报任务提取标记块用；托管配套任务都是单站）。
    let mut last_assistant_full = String::new();
    let mut run_sites: Vec<tasks::TaskRunSite> = Vec::with_capacity(targets.len());
    for h in handles {
        let Ok((slug, outcome)) = h.await else { continue };
        match outcome {
            SiteRun::Done(c) => {
                let limited = c.messages.iter().rev().find(|m| m.role == "assistant").and_then(|m| m.limit_reset).is_some();
                if limited {
                    run_sites.push(tasks::TaskRunSite { slug, ok: false, conv_id: c.id.clone(), error: "撞到订阅限额".into(), deferred: true });
                } else {
                    ok_count += 1;
                    last_conv = c.id.clone();
                    last_summary = last_snippet(&c);
                    last_assistant_full = c
                        .messages
                        .iter()
                        .rev()
                        .find(|m| m.role == "assistant")
                        .map(|m| m.text.clone())
                        .unwrap_or_default();
                    run_sites.push(tasks::TaskRunSite { slug, ok: true, conv_id: c.id.clone(), error: String::new(), deferred: false });
                }
            }
            SiteRun::Failed(e) => {
                let brief: String = e.chars().take(120).collect();
                if first_err.is_empty() {
                    first_err = format!("{slug}: {brief}");
                }
                run_sites.push(tasks::TaskRunSite { slug, ok: false, conv_id: String::new(), error: brief, deferred: false });
            }
            SiteRun::LimitSkipped => {
                run_sites.push(tasks::TaskRunSite { slug, ok: false, conv_id: String::new(), error: "限额顺延，本轮未运行".into(), deferred: true });
            }
        }
    }
    let limit_hit: Option<u64> = *limit_flag.lock().unwrap();

    let now = now_secs();
    let all_ok = ok_count == targets.len();
    let deferred = limit_hit.is_some();
    let summary = if let Some(reset) = limit_hit {
        format!("撞到订阅限额：完成 {ok_count}/{} 站，其余顺延——已顺延到 {} 后重试", targets.len(), fmt_ts_local(reset))
    } else if multi {
        let mut s = format!("{ok_count}/{} 个站点成功", targets.len());
        if !first_err.is_empty() {
            s.push_str("；");
            s.push_str(&first_err);
        }
        s.chars().take(160).collect()
    } else if all_ok {
        last_summary.clone()
    } else {
        first_err.chars().take(160).collect()
    };
    let _ = tstore.mutate(&task.id, |x| {
        x.last_run = now;
        x.runs += 1;
        // 顺延＝非失败语义：限额不算这个任务「坏了」。
        x.last_status = if all_ok || deferred { "ok".into() } else { "error".into() };
        x.last_summary = summary.clone();
        if !last_conv.is_empty() {
            x.last_conv_id = last_conv.clone();
        }
        if let Some(reset) = limit_hit {
            let next = limits::defer_next_run(reset, &x.id);
            if next > x.next_run {
                x.next_run = next;
            }
        }
        x.history.insert(0, tasks::TaskRun { ts: now, ok: all_ok || deferred, summary: summary.clone(), sites: run_sites.clone(), deferred });
        x.history.truncate(20);
    });
    // 审计要点回灌：本任务是某托管的「每周审计」（task_ids[1]）且成功跑完 → 从最后一条
    // assistant 消息提取 AUDIT-NOTES 块存进托管记录，并同步每日任务 prompt——
    // 下轮创作避开重复主题、落实内链建议。没有块（老 prompt 跑的旧会话等）就保留上次要点。
    if all_ok && !last_assistant_full.is_empty() {
        if let Some(m) = mstore.list().into_iter().find(|m| m.task_ids.get(1) == Some(&task.id)) {
            if let Some(notes) = managed::extract_audit_notes(&last_assistant_full) {
                if let Ok(Some(updated)) = mstore.mutate(&m.id, now, |x| x.audit_notes = notes.clone()) {
                    sync_daily_prompt(&tstore, &updated);
                }
            }
        }
        // 周报归档：本任务是某托管的「每周周报」（task_ids[2]）→ 提取 REPORT-METRICS 块（剥掉不展示），
        // 正文截断后连指标一起存进 reports（新到旧，store 封顶 26 条）。
        if let Some(m) = mstore.list().into_iter().find(|m| m.task_ids.get(2) == Some(&task.id)) {
            let (content, metrics) = managed::extract_report_metrics(&last_assistant_full);
            if !content.trim().is_empty() {
                let content: String = content.chars().take(managed::REPORT_MAX_CHARS).collect();
                let _ = mstore.mutate(&m.id, now, |x| {
                    x.reports.insert(0, managed::ReportEntry { ts: now, content: content.clone(), metrics: metrics.clone() });
                });
            }
        }
    }
    // ② 撞限收尾：不发常规完成/失败通知——发一次性的「已顺延」通知（每 brain 每窗口一次）。
    if let Some(reset) = limit_hit {
        if lstore.claim_notify(&task.brain, now) {
            notify_user(&app, "订阅限额，定时任务已顺延", format!(
                "{} 处于限额期（预计 {} 恢复），「{}」将在恢复后自动重试。",
                task.brain, fmt_ts_local(reset), task.title
            ));
        }
        return;
    }
    let body = if all_ok {
        if multi {
            format!("{} · {} 个站点全部完成", task.title, targets.len())
        } else {
            format!("{} · 已完成一次自动运行", task.title)
        }
    } else {
        format!("{} · {}", task.title, summary.chars().take(80).collect::<String>())
    };
    let _ = app
        .notification()
        .builder()
        .title(if all_ok { "定时任务完成" } else { "定时任务失败" })
        .body(body)
        .show();
}

/// 多站任务的并发上限：按机器核数自动协调（核数/2，钳在 2–6）。
/// CLI 进程网络等待为主，但每个常驻几百 MB 内存，全并发会把低配机器打爆。
fn task_concurrency() -> usize {
    let cores = std::thread::available_parallelism().map(|c| c.get()).unwrap_or(4);
    (cores / 2).clamp(2, 6)
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
                let mstore = state.managed.clone();
                let running2 = running.clone();
                let tid = t.id.clone();
                let data_dir = state.data_dir.clone();
                let ssh = state.ssh.clone();
                tauri::async_runtime::spawn(async move {
                    fire_task(app2, conns, convos, runs, tstore, mstore, t, data_dir, ssh).await;
                    running2.lock().unwrap().remove(&tid);
                });
            }
        }
    });
}

fn user_msg(text: String, now: u64) -> Message {
    Message { role: "user".into(), text, tools: vec![], ts: now, hidden: false, error: false, proposal: None, limit_reset: None }
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
        limit_reset: res.limit_reset,
    }
}

/// 用本轮 usage 更新会话的「上下文大小 + 累计 token」。拿不到 usage 就不动（保留上一轮的值）。
/// 口径差异：claude 用最后一条 assistant 消息的 usage＝最终那次调用读入的上下文，能当"会话大小"；
/// grok（ACP prompt 结果 _meta 顶层）同样是最后一次调用的读数，口径一致可标上下文；
/// codex（exec --json）只有 turn.completed 的整轮汇总——多步回合是各步之和，当上下文显示会离谱
/// 膨胀（一轮几百步能加到几十 M），故 codex 不标上下文（置 0，前端隐藏该条），只记累计吞吐。
/// 全局限额登记：任何一轮对话检测到限额（limit_reset）都写进 per-brain 状态——
/// 定时任务触发前据此顺延，不再到点白跑。reset=0（拿不到时间）由 store 按 now+30 分钟保守登记。
fn note_limit(data_dir: &std::path::Path, brain: &str, res: &agent::TurnResult) {
    if let Some(reset) = res.limit_reset {
        limits::LimitStore::new(data_dir).register(brain, reset, now_secs());
    }
}

fn apply_usage(c: &mut Conversation, res: &agent::TurnResult, data_dir: &std::path::Path) {
    if let Some(u) = &res.usage {
        let ctx = u.input + u.cache_read + u.cache_create;
        c.ctx_tokens = if c.brain == "codex" { 0 } else { ctx };
        c.total_tokens += ctx + u.output; // 累计（每轮全量计入，反映实际处理量）
        usage::append(data_dir, &usage::UsageEntry {
            ts: now_secs() as i64,
            brain: c.brain.clone(),
            model: c.model.clone(),
            input: u.input,
            output: u.output,
            cache_read: u.cache_read,
            cache_create: u.cache_create,
        });
    }
}

/// 本地用量参考统计：两个起点（前端按本地时区算好，如近 5 小时 / 今日零点）各汇总一份。
#[tauri::command]
fn usage_stats(state: tauri::State<'_, AppState>, since_a: i64, since_b: i64) -> usage::UsageStats {
    usage::stats(&state.data_dir, since_a, since_b)
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
    site_slugs: Vec<String>,
    site_names: Vec<String>,
    task_type: String,
    brain: String,
    model: String,
    perm_mode: String,
    effort: String,
    message: String,
    on_event: Channel<agent::TurnEvent>,
    data_dir: PathBuf,
    ssh: ssh::SshSessions,
) -> Result<Conversation, String> {
    let conn = conns.get(&conn_id)?;
    let now = now_secs();
    // 会话种子：claude 由我们指定 uuid（--session-id）；codex/grok 由 CLI 自己生成、首轮结果回填。
    let session_seed = if brain == "claude" {
        uuid::Uuid::new_v4().to_string()
    } else {
        String::new()
    };
    let work_dir = resolve_work_dir(&conn, site_slug.trim())?;
    let is_cf = conn.kind == "cloudflare";
    let is_ssh = conn.kind == "ssh";
    let multi = !is_cf && !is_ssh && site_slugs.len() > 1;
    let sys = if is_ssh {
        agent::ssh_system_prompt(
            &conn.ssh_user, &conn.ssh_host, conn.ssh_port,
            &tools::ssh_js_path(&data_dir).to_string_lossy(),
        )
    } else if is_cf {
        agent::cf_system_prompt(&cf_project_slug(site_slug.trim()), &conn.account_id)
    } else if multi {
        agent::multi_site_system_prompt(&site_slugs, &site_names)
    } else {
        agent::system_prompt(&task_type, site_slug.trim(), &site_name)
    };
    // 追加网页截图能力说明（shot.js 在启动时生成到 <data_dir>/tools/）。
    // ssh 会话不给：那是运维场景，截图帮不上忙，只是白占提示词。
    let sys = if is_ssh {
        sys
    } else {
        let shot = data_dir.join("tools").join("shot.js");
        format!("{sys}\n\n{}", agent::shot_prompt(&shot.to_string_lossy(), is_cf))
    };
    let conv = Conversation {
        id: conv_id.clone(),
        conn_id: conn.id.clone(),
        conn_name: conn.name.clone(),
        site_slug: site_slug.trim().to_string(),
        site_name,
        site_slugs: if multi { site_slugs.clone() } else { vec![] },
        site_names: if multi { site_names.clone() } else { vec![] },
        task_type,
        brain: brain.clone(),
        model: model.clone(),
        perm_mode: perm_mode.clone(),
        effort: effort.clone(),
        session_ref: String::new(),
        title: title_from(&message),
        messages: vec![user_msg(message.trim().to_string(), now)],
        status: "running".into(),
        created_at: now,
        updated_at: now,
        ctx_tokens: 0,
        total_tokens: 0,
    };
    convos.upsert(conv)?;

    let res = agent::run_turn(
        runs, conn, work_dir, brain, model, perm_mode, effort, data_dir.clone(), ssh, session_seed, true, Some(sys),
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
        apply_usage(c, &res, &data_dir);
        note_limit(&data_dir, &c.brain, &res);
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
    site_slugs: Vec<String>,
    site_names: Vec<String>,
    task_type: String,
    brain: String,
    model: String,
    perm_mode: String,
    effort: String,
    message: String,
    on_event: Channel<agent::TurnEvent>,
) -> Result<Conversation, String> {
    if conv_id.trim().is_empty() {
        return Err("会话 id 缺失".into());
    }
    // ssh 连接没有「站点」这回事（对象就是那台机器）；其余 kind 仍必须指定站点/项目。
    let is_ssh = state.conns.get(&conn_id).map(|c| c.kind == "ssh").unwrap_or(false);
    if !is_ssh && site_slug.trim().is_empty() && site_slugs.len() < 2 {
        return Err("站点不能为空".into());
    }
    if message.trim().is_empty() {
        return Err("请先说点什么".into());
    }
    create_conversation(
        state.conns.clone(), state.convos.clone(), state.runs.clone(),
        conv_id, conn_id, site_slug, site_name, site_slugs, site_names, task_type, brain, model, perm_mode, effort, message, on_event,
        state.data_dir.clone(), state.ssh.clone(),
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
        conv.effort.clone(),
        state.data_dir.clone(),
        state.ssh.clone(),
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
        apply_usage(c, &res, &state.data_dir);
        note_limit(&state.data_dir, &c.brain, &res);
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
        conv.effort.clone(),
        state.data_dir.clone(),
        state.ssh.clone(),
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
        apply_usage(c, &res, &state.data_dir);
        note_limit(&state.data_dir, &c.brain, &res);
    })?;
    updated.ok_or_else(|| "会话丢失".into())
}

/// 重建会话续跑：底层 CLI 会话状态损坏、重试无效时，抛弃旧 session，
/// 用**全新会话**（原系统提示 + 历史记录摘要）重跑最后一条用户请求。
/// 对用户而言＝这条对话原地继续；成功后把新 session id 接回本会话。
#[tauri::command]
async fn rebuild_session(
    state: tauri::State<'_, AppState>,
    conv_id: String,
    on_event: Channel<agent::TurnEvent>,
) -> Result<Conversation, String> {
    let now = now_secs();
    let last_user = state.convos.begin_rebuild(&conv_id, now)?;
    let conv = state.convos.get(&conv_id).ok_or("会话不存在")?;
    let conn = state.conns.get(&conv.conn_id)?;
    let work_dir = resolve_work_dir(&conn, &conv.site_slug)?;
    let is_cf = conn.kind == "cloudflare";
    let is_ssh = conn.kind == "ssh";
    // 与 create_conversation 的首轮系统提示保持同款（重建＝换个 session 从头讲一遍规矩）。
    let sys = if is_ssh {
        agent::ssh_system_prompt(
            &conn.ssh_user, &conn.ssh_host, conn.ssh_port,
            &tools::ssh_js_path(&state.data_dir).to_string_lossy(),
        )
    } else if is_cf {
        agent::cf_system_prompt(&cf_project_slug(&conv.site_slug), &conn.account_id)
    } else if conv.site_slugs.len() > 1 {
        agent::multi_site_system_prompt(&conv.site_slugs, &conv.site_names)
    } else {
        agent::system_prompt(&conv.task_type, &conv.site_slug, &conv.site_name)
    };
    let sys = if is_ssh {
        sys
    } else {
        let shot = state.data_dir.join("tools").join("shot.js");
        format!("{sys}\n\n{}", agent::shot_prompt(&shot.to_string_lossy(), is_cf))
    };
    // 历史摘要（不含将要重跑的最后一条用户消息）；发给模型的组合消息不落库，界面保持干净。
    let recap = convo::recap(&conv.messages, 8000);
    let message = if recap.is_empty() {
        last_user
    } else {
        format!(
            "【会话已重建】以下是此前对话的记录，供你衔接上下文；项目/站点里的实际文件与内容以当前现状为准，先查看再动手：\n\n{recap}\n\n——\n【继续执行用户的最新请求】\n{last_user}"
        )
    };
    let session_seed = if conv.brain == "claude" { uuid::Uuid::new_v4().to_string() } else { String::new() };

    let res = agent::run_turn(
        state.runs.clone(),
        conn,
        work_dir,
        conv.brain.clone(),
        conv.model.clone(),
        conv.perm_mode.clone(),
        conv.effort.clone(),
        state.data_dir.clone(),
        state.ssh.clone(),
        session_seed,
        true,
        Some(sys),
        message,
        conv_id.clone(),
        on_event,
    )
    .await;

    let now2 = now_secs();
    let updated = state.convos.mutate(&conv_id, now2, |c| {
        // 只有这轮成功才把新 session 接上；失败保留旧 ref（反正都坏，避免半截会话顶掉）。
        if res.ok && !res.session_ref.is_empty() {
            c.session_ref = res.session_ref.clone();
        }
        c.status = "idle".into();
        c.messages.push(assistant_msg(&res, now2));
        apply_usage(c, &res, &state.data_dir);
        note_limit(&state.data_dir, &c.brain, &res);
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
        "grok" => "grok login",
        other => return Err(format!("未知的执行引擎: {other}")),
    };
    // 登录成功标记：claude 读 --json 的 loggedIn=true，codex 读 "logged in"，
    // grok models 已登录时打印 "You are logged in with …"（未登录是 "You are not authenticated."）。
    let (status_cmd, marker) = match brain.as_str() {
        "claude" => ("claude auth status --json", r#""loggedIn": true"#),
        "grok" => ("grok models", "logged in with"),
        _ => ("codex login status", "logged in"),
    };
    // 系统代理注入：授权终端里的 CLI 只认环境变量（浏览器走系统代理没事，CLI 的登录/OIDC
    // 发现请求会直连超时）。生成脚本时把检测到的代理写进开头并回显一行，便于用户自诊。
    // system_proxy_env 在本进程已带代理环境变量时返回空——那正是启动时注入的系统代理，读回来用。
    let proxy_url = system_proxy_env()
        .iter()
        .find(|(k, _)| k == "HTTPS_PROXY")
        .map(|(_, v)| v.clone())
        .or_else(|| std::env::var("HTTPS_PROXY").ok().filter(|v| !v.is_empty()))
        .or_else(|| std::env::var("https_proxy").ok().filter(|v| !v.is_empty()));
    // 失败提示里的厂商登录域（超时/连接错误时的规则代理引导）。
    let login_domains = match brain.as_str() {
        "claude" => "claude.ai",
        "grok" => "auth.x.ai",
        _ => "auth.openai.com 及 chatgpt.com",
    };
    #[cfg(target_os = "macos")]
    let proxy_block = match &proxy_url {
        Some(u) => format!(
            "export HTTPS_PROXY=\"{u}\" HTTP_PROXY=\"{u}\" ALL_PROXY=\"{u}\"\nexport NO_PROXY=\"localhost,127.0.0.1,::1,.local\"\necho \"已注入代理: {u}\"\necho"
        ),
        None => "echo \"未检测到系统代理\"\necho".to_string(),
    };
    #[cfg(target_os = "windows")]
    let proxy_block = match &proxy_url {
        Some(u) => format!(
            "$env:HTTPS_PROXY='{u}'; $env:HTTP_PROXY='{u}'; $env:ALL_PROXY='{u}'\r\nWrite-Host '已注入代理: {u}'\r\nWrite-Host ''\r\n"
        ),
        None => "Write-Host '未检测到系统代理'\r\nWrite-Host ''\r\n".to_string(),
    };
    #[cfg(not(any(target_os = "macos", target_os = "windows")))]
    let _ = (&proxy_url, login_domains);
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
{proxy_block}
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
  echo "   若上面是 timed out / connection 错误：代理/VPN 需覆盖 {login_domains}，规则模式请把该域加进规则或临时切全局。"
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
        // PowerShell：UTF-8 输出 + 注入系统代理（CLI 只认环境变量）+ 跑登录命令 + 状态自查。
        let file = dir.join(format!("{brain}-login.ps1"));
        let script = format!(
            "$OutputEncoding = [Console]::OutputEncoding = [Text.Encoding]::UTF8\r\n\
             Write-Host 'GCMS Pilot · {brain} 授权登录'\r\n\
             Write-Host '浏览器会自动打开，完成登录后先回到这个窗口等它打出结果，再关闭。'\r\n\
             Write-Host ''\r\n\
             {proxy_block}\
             {login_cmd}\r\n\
             Write-Host ''\r\n\
             if ({status_cmd} 2>$null | Select-String -SimpleMatch '{marker}') {{\r\n\
             Write-Host '[OK] 登录成功！现在可以关闭这个窗口，GCMS Pilot 里的状态灯会自动变绿。'\r\n\
             }} else {{\r\n\
             Write-Host '[X] 登录还没完成。请截图上面的输出，或重新在 Pilot 里点「去授权」再试一次。'\r\n\
             Write-Host '    若上面是 timed out / connection 错误：代理/VPN 需覆盖 {login_domains}，规则模式请把该域加进规则或临时切全局。'\r\n\
             }}\r\n\
             Read-Host '按回车关闭这个窗口'\r\n"
        );
        // 必须带 UTF-8 BOM：无 BOM 的 .ps1 会被 Windows PowerShell 5.1 按 ANSI 解析，
        // 中文文本直接造成脚本解析失败——窗口闪一下就退，这就是「去授权闪退」的根因。
        let mut bytes = vec![0xEF, 0xBB, 0xBF];
        bytes.extend_from_slice(script.as_bytes());
        std::fs::write(&file, bytes).map_err(|e| e.to_string())?;
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
    apply_system_proxy_to_process();

    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_process::init())
        .setup(|app| {
            let data_dir = app.path().app_data_dir()?;
            std::fs::create_dir_all(&data_dir)?;
            // 托管 Node（若已自举过）：bin + npm 全局 bin 前置进程 PATH——
            // 探测（which 读 PATH）与之后 spawn 的全部 CLI 子进程自动继承。
            node_boot::prepend_process_path(&data_dir);
            let conns = pack::ConnStore::new(&data_dir).map_err(std::io::Error::other)?;
            let convos = convo::ConvStore::new(&data_dir);
            convos.mark_idle(now_secs());
            // 清掉上次进程遗留的待批权限请求（幽灵批准卡）。
            permit::sweep_all(&data_dir.join("permit").join("pending"));
            let _ = tools::ensure_shot(&data_dir); // 随附截图工具，覆写以随版本刷新
            let _ = tools::ensure_ssh(&data_dir); // 随附远程执行工具（AI 桥的 AI 侧）
            let task_store = tasks::TaskStore::new(&data_dir);
            app.manage(AppState {
                conns,
                convos,
                tasks: task_store,
                managed: managed::ManagedStore::new(&data_dir),
                runs: agent::RunRegistry::default(),
                firing: Arc::new(Mutex::new(HashSet::new())),
                data_dir: data_dir.clone(),
                preview: Arc::new(Mutex::new(None)),
                ssh: ssh::SshSessions::new(),
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
            check_pack_update,
            update_pack,
            remove_connection,
            open_conn_window,
            discover_sites,
            detect_brains,
            install_wrangler,
            install_claude,
            verify_cf_token,
            connect_cloudflare,
            verify_ssh,
            connect_ssh,
            update_ssh,
            ssh_os_probe,
            ssh_stats,
            ssh_open_shell,
            ssh_input,
            ssh_resize,
            ssh_close,
            sftp_list,
            sftp_home,
            sftp_read,
            sftp_write,
            sftp_rename,
            sftp_remove,
            sftp_mkdir,
            sftp_download,
            sftp_upload,
            save_attachment,
            read_workdir_image,
            resolve_workdir_file,
            usage_stats,
            install_codex,
            install_grok,
            scheduled_preview_url,
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
            rebuild_session,
            cancel_turn,
            set_conversation_model,
            set_conversation_brain_model,
            set_conversation_perm_mode,
            set_conversation_effort,
            list_pending_permits,
            respond_permit,
            list_tasks,
            save_task,
            delete_task,
            set_task_enabled,
            run_task_now,
            managed_list,
            managed_enable,
            managed_set_level,
            managed_set_edit_limit,
            managed_pause,
            managed_resume,
            managed_disable,
            managed_record_reject,
            managed_plan_save,
            managed_precheck,
            managed_drafts,
            managed_publish,
            managed_summary,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
