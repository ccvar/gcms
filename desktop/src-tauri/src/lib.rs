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
mod tools;
mod usage;

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
    let server = q("ProxyServer")?;
    if server.is_empty() {
        return None;
    }
    if server.contains('=') {
        for want in ["https=", "http="] {
            if let Some(seg) = server.split(';').find_map(|s| s.trim().strip_prefix(want)) {
                if !seg.is_empty() {
                    return Some(seg.to_string());
                }
            }
        }
        return None; // 只配了 ftp=/socks= 等，不硬猜
    }
    Some(server)
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

/// npm 渠道装 Claude Code（@anthropic-ai/claude-code，要求 Node ≥18）。
fn install_claude_npm(proxy: &[(String, String)]) -> Result<(), String> {
    let mut c = std::process::Command::new(brains::resolve_bin("npm"));
    c.args(["install", "-g", "@anthropic-ai/claude-code"]);
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
        Err(install_err_brief(&out.stdout, &out.stderr))
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
        Err(install_err_brief(&out.stdout, &out.stderr))
    }
}

/// 一键安装 Claude Code：本机有可用的 Node(≥18)/npm 就**优先走 npm 官方包渠道**——
/// npm 源直连可达，规则代理用户（只代理 claude.ai、没代理脚本内部下载域名）成功率最高；
/// npm 渠道失败或没有可用 Node 时再走官方原生脚本。两条渠道都注入系统代理。
#[tauri::command]
async fn install_claude() -> Result<String, String> {
    tauri::async_runtime::spawn_blocking(|| {
        let proxy = system_proxy_env();
        let npm_usable = node_major() >= 18;
        let mut npm_err = String::new();
        if npm_usable {
            match install_claude_npm(&proxy) {
                Ok(()) => return Ok("Claude Code 安装完成（npm 渠道），正在重新检测…".to_string()),
                Err(e) => npm_err = e,
            }
        }
        match install_claude_native(&proxy) {
            Ok(()) => Ok("Claude Code 安装完成，正在重新检测…".to_string()),
            Err(native_err) => {
                let mut msg = if npm_err.is_empty() {
                    format!("安装失败：{native_err}")
                } else {
                    format!("安装失败：npm 渠道：{npm_err}；官方脚本渠道：{native_err}")
                };
                if !npm_usable {
                    msg.push_str("；本机没有可用的 Node(≥18)/npm——装上 Node 后重试还能多一条 npm 渠道");
                }
                msg.push_str("。多为网络问题——VPN/代理需覆盖 claude.ai 及其下载域名（规则模式请把 storage.googleapis.com 加进代理规则，或临时切全局），也可复制右侧命令到终端手动执行看完整输出");
                Err(msg)
            }
        }
    })
    .await
    .map_err(|e| e.to_string())?
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

/// 触发一次任务：对每个目标站点各开一个全新对话跑 task.prompt（顺序执行，互不阻断），
/// 回写任务的 last_* 并通知。
async fn fire_task(
    app: AppHandle,
    conns: pack::ConnStore,
    convos: convo::ConvStore,
    runs: agent::RunRegistry,
    tstore: tasks::TaskStore,
    task: ScheduledTask,
    data_dir: PathBuf,
) {
    let targets = task.targets();
    let multi = targets.len() > 1;
    // 并发执行，信号量限流：CLI 进程以网络等待为主，但每个要吃几百 MB 内存，
    // 上限按核数自动定（核数/2，钳 2–6）。同连接共享工作目录，写盘冲突概率低但非零，
    // 不放开到全并发。
    let sem = std::sync::Arc::new(tokio::sync::Semaphore::new(task_concurrency()));
    let mut handles = Vec::with_capacity(targets.len());
    for (slug, name) in targets.clone() {
        let sem = sem.clone();
        let (conns, convos, runs, data_dir) = (conns.clone(), convos.clone(), runs.clone(), data_dir.clone());
        let (conn_id, task_type, brain, model, effort, prompt) = (
            task.conn_id.clone(), task.task_type.clone(), task.brain.clone(),
            task.model.clone(), task.effort.clone(), task.prompt.clone(),
        );
        handles.push(tauri::async_runtime::spawn(async move {
            let _permit = sem.acquire_owned().await;
            let conv_id = uuid::Uuid::new_v4().to_string();
            // 后台运行没有前端接收方，用一个丢弃事件的 Channel。
            let sink: Channel<agent::TurnEvent> = Channel::new(|_| Ok(()));
            // 定时任务无人值守：只能全自动（询问/自动档会卡在等批准，永远回不来）。
            let res = create_conversation(
                conns, convos, runs, conv_id,
                conn_id, slug.clone(), name,
                task_type, brain, model,
                "full".into(), effort, prompt, sink, data_dir,
            )
            .await;
            (slug, res)
        }));
    }
    let mut ok_count = 0usize;
    let mut first_err = String::new();
    let mut last_conv = String::new();
    let mut last_summary = String::new();
    let mut run_sites: Vec<tasks::TaskRunSite> = Vec::with_capacity(targets.len());
    for h in handles {
        let Ok((slug, res)) = h.await else { continue };
        match &res {
            Ok(c) => {
                ok_count += 1;
                last_conv = c.id.clone();
                last_summary = last_snippet(c);
                run_sites.push(tasks::TaskRunSite { slug, ok: true, conv_id: c.id.clone(), error: String::new() });
            }
            Err(e) => {
                let brief: String = e.chars().take(120).collect();
                if first_err.is_empty() {
                    first_err = format!("{slug}: {brief}");
                }
                run_sites.push(tasks::TaskRunSite { slug, ok: false, conv_id: String::new(), error: brief });
            }
        }
    }

    let now = now_secs();
    let all_ok = ok_count == targets.len();
    let summary = if multi {
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
        x.last_status = if all_ok { "ok".into() } else { "error".into() };
        x.last_summary = summary.clone();
        if !last_conv.is_empty() {
            x.last_conv_id = last_conv.clone();
        }
        x.history.insert(0, tasks::TaskRun { ts: now, ok: all_ok, summary: summary.clone(), sites: run_sites.clone() });
        x.history.truncate(20);
    });
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
/// codex（exec --json）只有 turn.completed 的整轮汇总——多步回合是各步之和，当上下文显示会离谱
/// 膨胀（一轮几百步能加到几十 M），故 codex 不标上下文（置 0，前端隐藏该条），只记累计吞吐。
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
    task_type: String,
    brain: String,
    model: String,
    perm_mode: String,
    effort: String,
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
    let is_cf = conn.kind == "cloudflare";
    let sys = if is_cf {
        agent::cf_system_prompt(&cf_project_slug(site_slug.trim()), &conn.account_id)
    } else {
        agent::system_prompt(&task_type, site_slug.trim(), &site_name)
    };
    // 追加网页截图能力说明（shot.js 在启动时生成到 <data_dir>/tools/）。
    let shot = data_dir.join("tools").join("shot.js");
    let sys = format!("{sys}\n\n{}", agent::shot_prompt(&shot.to_string_lossy(), is_cf));
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
        runs, conn, work_dir, brain, model, perm_mode, effort, data_dir.join("permit"), session_seed, true, Some(sys),
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
    effort: String,
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
        conv_id, conn_id, site_slug, site_name, task_type, brain, model, perm_mode, effort, message, on_event,
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
        conv.effort.clone(),
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
        apply_usage(c, &res, &state.data_dir);
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
        apply_usage(c, &res, &state.data_dir);
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
    let sys = if is_cf {
        agent::cf_system_prompt(&cf_project_slug(&conv.site_slug), &conn.account_id)
    } else {
        agent::system_prompt(&conv.task_type, &conv.site_slug, &conv.site_name)
    };
    let shot = state.data_dir.join("tools").join("shot.js");
    let sys = format!("{sys}\n\n{}", agent::shot_prompt(&shot.to_string_lossy(), is_cf));
    // 历史摘要（不含将要重跑的最后一条用户消息）；发给模型的组合消息不落库，界面保持干净。
    let recap = convo::recap(&conv.messages, 8000);
    let message = if recap.is_empty() {
        last_user
    } else {
        format!(
            "【会话已重建】以下是此前对话的记录，供你衔接上下文；项目/站点里的实际文件与内容以当前现状为准，先查看再动手：\n\n{recap}\n\n——\n【继续执行用户的最新请求】\n{last_user}"
        )
    };
    let session_seed = if conv.brain == "codex" { String::new() } else { uuid::Uuid::new_v4().to_string() };

    let res = agent::run_turn(
        state.runs.clone(),
        conn,
        work_dir,
        conv.brain.clone(),
        conv.model.clone(),
        conv.perm_mode.clone(),
        conv.effort.clone(),
        state.data_dir.join("permit"),
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
            let _ = tools::ensure_shot(&data_dir); // 随附截图工具，覆写以随版本刷新
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
            check_pack_update,
            update_pack,
            remove_connection,
            discover_sites,
            detect_brains,
            install_wrangler,
            install_claude,
            verify_cf_token,
            connect_cloudflare,
            save_attachment,
            read_workdir_image,
            resolve_workdir_file,
            usage_stats,
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
            set_conversation_perm_mode,
            set_conversation_effort,
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
