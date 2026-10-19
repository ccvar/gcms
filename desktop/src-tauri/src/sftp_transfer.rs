//! Downloads are staged locally and only published after completion. Native
//! file promises receive their destination from macOS after the drop. The legacy
//! cached drag command accepts opaque prepared IDs, never frontend local paths.
use serde::Serialize;
use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::{Arc, Mutex, OnceLock},
};
use tauri::{ipc::Channel, Manager};
use tokio::sync::watch;

#[derive(Clone, Serialize, Default)]
pub struct Progress {
    pub phase: String,
    pub bytes: u64,
    pub total_bytes: u64,
    pub files: u64,
    pub total_files: u64,
    pub skipped: u64,
}

#[derive(Serialize)]
pub struct DownloadResult {
    pub path: String,
    pub drag_id: Option<String>,
    pub skipped: u64,
}

#[derive(Serialize)]
pub struct UploadResult {
    pub path: String,
    pub bytes: u64,
    pub files: u64,
}

pub(crate) struct UploadFile {
    pub local: PathBuf,
    pub remote: String,
    pub size: u64,
}

pub(crate) struct UploadPlan {
    pub directories: Vec<String>,
    pub files: Vec<UploadFile>,
    pub total_bytes: u64,
}

/// Scan before any remote writes: hidden files and empty folders are included;
/// links/special files fail explicitly instead of silently uploading other data.
async fn plan_upload(
    local: &Path,
    remote: &str,
    mut report: impl FnMut(Progress),
) -> Result<UploadPlan, String> {
    if !local.is_absolute() {
        return Err("上传来源必须是本地绝对路径".into());
    }
    let remote = remote.trim_end_matches('/');
    if !remote.starts_with('/')
        || remote.is_empty()
        || remote.contains('\0')
        || remote.split('/').any(|part| matches!(part, "." | ".."))
    {
        return Err("请选择有效的远端上传路径，不能直接覆盖根目录".into());
    }
    let mut plan = UploadPlan {
        directories: Vec::new(),
        files: Vec::new(),
        total_bytes: 0,
    };
    let mut pending = vec![(local.to_path_buf(), remote.to_string(), 0_usize)];
    let mut entries = 0_usize;
    let mut last_progress = std::time::Instant::now();
    report(Progress {
        phase: "scanning".into(),
        ..Default::default()
    });
    while let Some((source, target, depth)) = pending.pop() {
        if depth > 128 || entries >= 100_000 {
            return Err("目录层级或文件数量过多，请拆分上传".into());
        }
        entries += 1;
        let meta = tokio::fs::symlink_metadata(&source)
            .await
            .map_err(|e| format!("读取上传来源 {} 失败: {e}", source.display()))?;
        if meta.is_dir() {
            plan.directories.push(target.clone());
            let mut children = tokio::fs::read_dir(&source)
                .await
                .map_err(|e| format!("读取上传目录 {} 失败: {e}", source.display()))?;
            while let Some(child) = children.next_entry().await.map_err(|e| e.to_string())? {
                // Bound the pending list as well as visited entries.
                if entries + pending.len() >= 100_000 {
                    return Err("文件数量超过 100000，请拆分上传".into());
                }
                let name = child.file_name();
                let name = name.to_str().ok_or("上传文件名不是有效的 UTF-8 编码")?;
                local_name(name)?;
                pending.push((child.path(), format!("{target}/{name}"), depth + 1));
            }
        } else if meta.is_file() {
            plan.total_bytes = plan
                .total_bytes
                .checked_add(meta.len())
                .ok_or("上传总大小超出限制")?;
            plan.files.push(UploadFile {
                local: source,
                remote: target,
                size: meta.len(),
            });
        } else {
            return Err(format!(
                "上传来源包含软链接或特殊文件：{}；请先打包或选择普通文件",
                source.display()
            ));
        }
        if last_progress.elapsed() >= std::time::Duration::from_millis(200) {
            report(Progress {
                phase: "scanning".into(),
                total_bytes: plan.total_bytes,
                total_files: plan.files.len() as u64,
                ..Default::default()
            });
            last_progress = std::time::Instant::now();
        }
    }
    report(Progress {
        phase: "scanning".into(),
        total_bytes: plan.total_bytes,
        total_files: plan.files.len() as u64,
        ..Default::default()
    });
    Ok(plan)
}

pub(crate) async fn cancellable_upload<T>(
    cancel: &mut watch::Receiver<bool>,
    work: impl std::future::Future<Output = Result<T, String>>,
) -> Result<T, String> {
    const CANCELLED: &str = "上传已取消；远端已写入的内容会保留，请检查后再决定是否重新上传";
    if *cancel.borrow() {
        return Err(CANCELLED.into());
    }
    tokio::select! {
        biased;
        _ = cancel.changed() => Err(CANCELLED.into()),
        result = work => result,
    }
}

/// remote is the final full path (including the selected file/directory name).
/// Preserve the legacy create/truncate upload policy; cancellation never removes
/// remote paths and no writes are retried automatically.
#[tauri::command]
pub async fn sftp_upload_entry(
    state: tauri::State<'_, crate::AppState>,
    window: tauri::Window,
    conn_id: String,
    local: String,
    remote: String,
    transfer_id: String,
    on_progress: Channel<Progress>,
) -> Result<UploadResult, String> {
    let (cancel_tx, mut cancel) = watch::channel(false);
    {
        let mut entries = ACTIVE
            .get_or_init(Default::default)
            .lock()
            .map_err(|e| e.to_string())?;
        if entries.contains_key(&transfer_id) {
            return Err("传输任务已存在".into());
        }
        entries.insert(transfer_id.clone(), (window.label().into(), cancel_tx));
    }
    let _guard = ActiveGuard(transfer_id);
    let plan = cancellable_upload(
        &mut cancel,
        plan_upload(Path::new(&local), &remote, |progress| {
            let _ = on_progress.send(progress);
        }),
    )
    .await?;
    let _ = on_progress.send(Progress {
        phase: "connecting".into(),
        total_bytes: plan.total_bytes,
        total_files: plan.files.len() as u64,
        ..Default::default()
    });
    cancellable_upload(&mut cancel, crate::ensure_ssh(&state, &conn_id)).await?;
    let (bytes, files) = cancellable_upload(
        &mut cancel,
        state.ssh.upload_tree(&conn_id, &plan, &on_progress),
    )
    .await?;
    Ok(UploadResult {
        path: remote.trim_end_matches('/').into(),
        bytes,
        files,
    })
}

#[derive(Clone, Serialize)]
pub struct DragDownloadEvent {
    pub phase: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// Start a file promise immediately; no network access or local destination is
/// required until the native receiver accepts the drop.
#[tauri::command]
pub async fn sftp_drag_download(
    window: tauri::Window,
    conn_id: String,
    remote: String,
    directory: bool,
    transfer_id: String,
    on_progress: Channel<Progress>,
    on_event: Channel<DragDownloadEvent>,
) -> Result<(), String> {
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (
            window,
            conn_id,
            remote,
            directory,
            transfer_id,
            on_progress,
            on_event,
        );
        Err("当前系统暂不支持直接拖出，请使用下载按钮".into())
    }
    #[cfg(target_os = "macos")]
    {
        let name = remote
            .trim_end_matches('/')
            .rsplit('/')
            .next()
            .unwrap_or("");
        local_name(name)?;
        let (cancel_tx, cancel) = watch::channel(false);
        {
            let mut entries = ACTIVE
                .get_or_init(Default::default)
                .lock()
                .map_err(|e| e.to_string())?;
            if entries.contains_key(&transfer_id) {
                return Err("下载任务已存在".into());
            }
            entries.insert(transfer_id.clone(), (window.label().into(), cancel_tx));
        }
        let request = Arc::new(PromiseRequest {
            app: window.app_handle().clone(),
            conn_id,
            name: name.into(),
            remote,
            directory,
            id: transfer_id.clone(),
            progress: on_progress,
            events: on_event,
            registration: Mutex::new(Some((ActiveGuard(transfer_id), cancel))),
            finished: std::sync::atomic::AtomicBool::new(false),
        });
        let (tx, rx) = tokio::sync::oneshot::channel();
        let app = window.app_handle().clone();
        app.run_on_main_thread(move || {
            let result = crate::sftp_drag_macos::start(&window, request);
            let _ = tx.send(result);
        })
        .map_err(|e| e.to_string())?;
        rx.await.map_err(|e| e.to_string())?
    }
}

#[cfg(target_os = "macos")]
pub(crate) struct PromiseRequest {
    pub app: tauri::AppHandle,
    conn_id: String,
    remote: String,
    pub name: String,
    pub directory: bool,
    pub id: String,
    progress: Channel<Progress>,
    events: Channel<DragDownloadEvent>,
    registration: Mutex<Option<(ActiveGuard, watch::Receiver<bool>)>>,
    finished: std::sync::atomic::AtomicBool,
}

#[cfg(target_os = "macos")]
impl PromiseRequest {
    pub fn cancel(&self) {
        if let Ok(active) = ACTIVE.get_or_init(Default::default).lock() {
            if let Some((_, sender)) = active.get(&self.id) {
                let _ = sender.send(true);
            }
        }
    }

    pub fn cancellation(&self) -> Option<watch::Receiver<bool>> {
        self.registration
            .lock()
            .ok()?
            .as_ref()
            .map(|(_, cancel)| cancel.clone())
    }

    pub fn is_finished(&self) -> bool {
        self.finished.load(std::sync::atomic::Ordering::Acquire)
    }

    pub fn emit(&self, phase: &str) {
        if !self.finished.load(std::sync::atomic::Ordering::Acquire) {
            let _ = self.events.send(DragDownloadEvent {
                phase: phase.into(),
                path: None,
                error: None,
            });
        }
    }

    pub fn finish(&self, result: Result<PathBuf, String>) {
        if self
            .finished
            .swap(true, std::sync::atomic::Ordering::AcqRel)
        {
            return;
        }
        // A cancelled drag has not handed its registration to the writer.
        if let Ok(mut pending) = self.registration.lock() {
            pending.take();
        }
        let event = match result {
            Ok(path) => DragDownloadEvent {
                phase: "completed".into(),
                path: Some(path.to_string_lossy().into_owned()),
                error: None,
            },
            Err(error) => DragDownloadEvent {
                phase: if error.starts_with("下载已取消") {
                    "cancelled"
                } else {
                    "error"
                }
                .into(),
                path: None,
                error: Some(error),
            },
        };
        let _ = self.events.send(event);
    }

    pub async fn prepare(&self, destination: &Path) -> Result<PromisedDownload, String> {
        let (guard, mut cancel) = self
            .registration
            .lock()
            .map_err(|e| e.to_string())?
            .take()
            .ok_or("拖出任务已经处理")?;
        validate_promise_destination(destination)?;
        let _ = self.progress.send(Progress {
            phase: "connecting".into(),
            ..Default::default()
        });
        let state = self.app.state::<crate::AppState>();
        cancellable(&mut cancel, crate::ensure_ssh(&state, &self.conn_id)).await?;
        let stage = tempfile::Builder::new()
            .prefix(".pilot-download-")
            .tempdir_in(destination.parent().ok_or("保存位置无效")?)
            .map_err(|e| format!("创建下载缓存失败: {e}"))?;
        let staged = stage.path().join(&self.name);
        let skipped = cancellable(
            &mut cancel,
            state.ssh.download_tree(
                &self.conn_id,
                &self.remote,
                &staged,
                self.directory,
                &self.progress,
            ),
        )
        .await?;
        if skipped > 0 {
            return Err(
                "所选目录包含软链接或特殊文件；为避免保存不完整目录，请使用打包下载".into(),
            );
        }
        Ok(PromisedDownload {
            staged,
            _stage: stage,
            _guard: guard,
            cancel,
        })
    }
}

#[cfg(target_os = "macos")]
pub(crate) struct PromisedDownload {
    staged: PathBuf,
    _stage: tempfile::TempDir,
    _guard: ActiveGuard,
    cancel: watch::Receiver<bool>,
}

#[cfg(target_os = "macos")]
impl PromisedDownload {
    pub fn publish(&self, destination: &Path) -> Result<PathBuf, String> {
        if *self.cancel.borrow() {
            return Err("下载已取消，临时文件已清理".into());
        }
        publish_promise(&self.staged, destination)
    }
}

#[cfg(target_os = "macos")]
fn validate_promise_destination(destination: &Path) -> Result<(), String> {
    if !destination.is_absolute() {
        return Err("保存位置必须是绝对路径".into());
    }
    local_name(
        destination
            .file_name()
            .and_then(|s| s.to_str())
            .ok_or("保存文件名无效")?,
    )?;
    if destination.symlink_metadata().is_ok() {
        return Err("本地已有同名项目，未覆盖；请换一个目标文件夹".into());
    }
    Ok(())
}

/// Darwin's exclusive rename publishes an entire file or directory atomically,
/// including when another process creates the destination during the download.
#[cfg(target_os = "macos")]
pub(crate) fn publish_promise(staged: &Path, destination: &Path) -> Result<PathBuf, String> {
    use std::{ffi::CString, os::unix::ffi::OsStrExt};
    validate_promise_destination(destination)?;
    let source = CString::new(staged.as_os_str().as_bytes()).map_err(|e| e.to_string())?;
    let target = CString::new(destination.as_os_str().as_bytes()).map_err(|e| e.to_string())?;
    // SAFETY: both C strings live for the call; RENAME_EXCL prevents replacement.
    if unsafe { libc::renamex_np(source.as_ptr(), target.as_ptr(), libc::RENAME_EXCL) } != 0 {
        return Err(format!(
            "保存失败（不会覆盖同名项目）：{}",
            std::io::Error::last_os_error()
        ));
    }
    Ok(destination.to_path_buf())
}

type Active = HashMap<String, (String, watch::Sender<bool>)>;
static ACTIVE: OnceLock<Mutex<Active>> = OnceLock::new();

/// Closing a detached window must not leave unobservable writes running after
/// its restored history reports interruption. Hiding the main window does not
/// call this. Cancellation only signals the transfer; it never deletes files.
pub(crate) fn cancel_owner(owner: &str) {
    let Some(active) = ACTIVE.get() else {
        return;
    };
    if let Ok(entries) = active.lock() {
        for (window, sender) in entries.values() {
            if window == owner {
                let _ = sender.send(true);
            }
        }
    }
}

async fn cancellable<T>(
    cancel: &mut watch::Receiver<bool>,
    work: impl std::future::Future<Output = Result<T, String>>,
) -> Result<T, String> {
    if *cancel.borrow() {
        return Err("下载已取消，临时文件已清理".into());
    }
    tokio::select! {
        biased;
        _ = cancel.changed() => Err("下载已取消，临时文件已清理".into()),
        result = work => result,
    }
}
struct ActiveGuard(String);
impl Drop for ActiveGuard {
    fn drop(&mut self) {
        if let Ok(mut entries) = ACTIVE.get_or_init(Default::default).lock() {
            entries.remove(&self.0);
        }
    }
}
struct Prepared {
    owner: String,
    path: PathBuf,
    // Retain through the OS copy operation, including after its drop callback.
    _stage: tempfile::TempDir,
}
static PREPARED: OnceLock<Mutex<HashMap<String, Arc<Prepared>>>> = OnceLock::new();

/// Only expired Pilot-owned staging folders, never destinations chosen by users.
fn clean_old_drag_cache(base: &Path) {
    let Ok(entries) = std::fs::read_dir(base) else {
        return;
    };
    let active = PREPARED.get_or_init(Default::default).lock().ok();
    for entry in entries.flatten() {
        if !entry
            .file_name()
            .to_string_lossy()
            .starts_with(".pilot-download-")
        {
            continue;
        }
        if active.as_ref().is_some_and(|items| {
            items
                .values()
                .any(|item| item._stage.path() == entry.path())
        }) {
            continue;
        }
        let Ok(meta) = entry.path().symlink_metadata() else {
            continue;
        };
        if meta.is_dir()
            && meta
                .modified()
                .ok()
                .and_then(|t| t.elapsed().ok())
                .is_some_and(|age| age.as_secs() > 7 * 86400)
        {
            let _ = std::fs::remove_dir_all(entry.path());
        }
    }
}

pub fn local_name(name: &str) -> Result<(), String> {
    if name.is_empty() || matches!(name, "." | "..") || name.contains(['/', '\\', '\0']) {
        return Err(format!("无法安全保存文件名：{name:?}"));
    }
    #[cfg(windows)]
    {
        let stem = name.split('.').next().unwrap_or("").to_ascii_uppercase();
        if name.contains(['<', '>', ':', '"', '|', '?', '*'])
            || name.ends_with(['.', ' '])
            || name.chars().any(|c| c.is_control())
            || matches!(stem.as_str(), "CON" | "PRN" | "AUX" | "NUL")
            || (stem.len() == 4
                && (stem.starts_with("COM") || stem.starts_with("LPT"))
                && matches!(stem.as_bytes()[3], b'1'..=b'9'))
        {
            return Err(format!("Windows 不支持文件名 {name:?}，请使用打包下载"));
        }
    }
    Ok(())
}

/// Refuse replacement even when another process creates the name after the UI check.
fn publish(stage: &Path, requested: &Path, directory: bool) -> Result<PathBuf, String> {
    if !directory {
        tempfile::TempPath::try_from_path(stage.to_path_buf())
            .map_err(|e| e.to_string())?
            .persist_noclobber(requested)
            .map_err(|e| format!("保存失败（不会覆盖同名文件，请另选名称）：{e}"))?;
        return Ok(requested.to_path_buf());
    }
    let parent = requested.parent().ok_or("保存目录无效")?;
    let name = requested
        .file_name()
        .ok_or("保存目录无效")?
        .to_string_lossy();
    for i in 0..10000 {
        let target = if i == 0 {
            requested.to_path_buf()
        } else {
            parent.join(format!("{name} 副本 {i}"))
        };
        match std::fs::create_dir(&target) {
            Ok(()) => {
                for entry in std::fs::read_dir(stage).map_err(|e| e.to_string())? {
                    let entry = entry.map_err(|e| e.to_string())?;
                    std::fs::rename(entry.path(), target.join(entry.file_name())).map_err(|e| {
                        format!("保存未完成，已保存部分保留于 {}：{e}", target.display())
                    })?;
                }
                return Ok(target);
            }
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(e) => return Err(format!("创建本地目录失败: {e}")),
        }
    }
    Err("同名副本过多，请选择其他目录".into())
}

#[tauri::command]
pub fn sftp_cancel_download(window: tauri::Window, transfer_id: String) -> Result<(), String> {
    let entries = ACTIVE
        .get_or_init(Default::default)
        .lock()
        .map_err(|e| e.to_string())?;
    if let Some((owner, sender)) = entries.get(&transfer_id) {
        if owner != window.label() {
            return Err("不能取消其他窗口的传输".into());
        }
        let _ = sender.send(true);
    }
    Ok(())
}

#[tauri::command]
pub async fn sftp_download_entry(
    state: tauri::State<'_, crate::AppState>,
    window: tauri::Window,
    conn_id: String,
    remote: String,
    local: Option<String>,
    directory: bool,
    archive: bool,
    prepare_drag: bool,
    transfer_id: String,
    on_progress: Channel<Progress>,
) -> Result<DownloadResult, String> {
    if archive && (!directory || prepare_drag) {
        return Err("下载选项无效".into());
    }
    let name = remote
        .trim_end_matches('/')
        .rsplit('/')
        .next()
        .ok_or("文件名无效")?;
    local_name(name)?;
    let (cancel_tx, mut cancel) = watch::channel(false);
    {
        let mut entries = ACTIVE
            .get_or_init(Default::default)
            .lock()
            .map_err(|e| e.to_string())?;
        if entries.contains_key(&transfer_id) {
            return Err("下载任务已存在".into());
        }
        entries.insert(transfer_id.clone(), (window.label().into(), cancel_tx));
    }
    let _guard = ActiveGuard(transfer_id);
    let _ = on_progress.send(Progress {
        phase: "connecting".into(),
        ..Default::default()
    });
    cancellable(&mut cancel, crate::ensure_ssh(&state, &conn_id)).await?;
    let requested = if prepare_drag {
        let entries = PREPARED
            .get_or_init(Default::default)
            .lock()
            .map_err(|e| e.to_string())?;
        if entries.len() >= 32 {
            return Err("本次会话拖出缓存已达上限，请使用普通下载或重启 Pilot 清理缓存".into());
        }
        drop(entries);
        let base = state.data_dir.join("sftp-drag");
        std::fs::create_dir_all(&base).map_err(|e| e.to_string())?;
        clean_old_drag_cache(&base);
        base.join(name)
    } else {
        let path = PathBuf::from(local.ok_or("请选择保存位置")?);
        if !path.is_absolute() {
            return Err("保存位置必须是绝对路径".into());
        }
        if directory && !archive {
            path.join(name)
        } else {
            path
        }
    };
    if !prepare_drag && (!directory || archive) && requested.symlink_metadata().is_ok() {
        return Err("本地已有同名文件，未开始下载；请另选名称或保存目录".into());
    }
    let stage = tempfile::Builder::new()
        .prefix(".pilot-download-")
        .tempdir_in(requested.parent().ok_or("保存位置无效")?)
        .map_err(|e| format!("创建下载缓存失败: {e}"))?;
    let staged = stage
        .path()
        .join(if archive { "archive.tar.gz" } else { name });
    let skipped = if archive {
        state
            .ssh
            .download_directory(
                &conn_id,
                &remote,
                staged.to_str().ok_or("路径编码无效")?,
                &on_progress,
                &mut cancel,
            )
            .await?;
        0
    } else {
        cancellable(
            &mut cancel,
            state
                .ssh
                .download_tree(&conn_id, &remote, &staged, directory, &on_progress),
        )
        .await?
    };
    if *cancel.borrow() {
        return Err("下载已取消，临时文件已清理".into());
    }
    if prepare_drag {
        if skipped > 0 {
            return Err(
                "所选目录包含软链接或特殊文件；为避免拖出不完整目录，请使用打包下载".into(),
            );
        }
        let id = uuid::Uuid::new_v4().to_string();
        let path = staged.to_string_lossy().into_owned();
        PREPARED
            .get_or_init(Default::default)
            .lock()
            .map_err(|e| e.to_string())?
            .insert(
                id.clone(),
                Arc::new(Prepared {
                    owner: window.label().into(),
                    path: staged,
                    _stage: stage,
                }),
            );
        Ok(DownloadResult {
            path,
            drag_id: Some(id),
            skipped,
        })
    } else {
        let path = publish(&staged, &requested, directory && !archive)?;
        Ok(DownloadResult {
            path: path.to_string_lossy().into_owned(),
            drag_id: None,
            skipped,
        })
    }
}

#[tauri::command]
pub async fn sftp_start_drag(
    window: tauri::Window,
    drag_id: String,
    on_event: Channel<String>,
) -> Result<(), String> {
    let prepared = PREPARED
        .get_or_init(Default::default)
        .lock()
        .map_err(|e| e.to_string())?
        .get(&drag_id)
        .cloned()
        .ok_or("拖出缓存已失效，请重新准备")?;
    if prepared.owner != window.label() {
        return Err("拖出缓存不属于当前窗口".into());
    }
    let (tx, rx) = tokio::sync::oneshot::channel();
    let app = window.app_handle().clone();
    app.run_on_main_thread(move || {
        #[cfg(target_os = "linux")]
        let native = window.gtk_window();
        #[cfg(not(target_os = "linux"))]
        let native: tauri::Result<_> = Ok(window);
        let result = native.map_err(|e| e.to_string()).and_then(|w| {
            drag::start_drag(
                &w,
                drag::DragItem::Files(vec![prepared.path.clone()]),
                drag::Image::Raw(include_bytes!("../icons/32x32.png").to_vec()),
                move |result, _| {
                    let _ = on_event.send(
                        match result {
                            drag::DragResult::Dropped => "dropped",
                            drag::DragResult::Cancel => "cancelled",
                        }
                        .into(),
                    );
                },
                drag::Options {
                    mode: drag::DragMode::Copy,
                    ..Default::default()
                },
            )
            .map_err(|e| format!("无法开始拖出: {e}"))
        });
        let _ = tx.send(result);
    })
    .map_err(|e| e.to_string())?;
    rx.await.map_err(|e| e.to_string())?
}

#[cfg(test)]
mod tests {
    use super::*;

    fn active_for_test(owner: &str) -> (String, ActiveGuard, watch::Receiver<bool>) {
        let id = uuid::Uuid::new_v4().to_string();
        let (sender, receiver) = watch::channel(false);
        ACTIVE
            .get_or_init(Default::default)
            .lock()
            .unwrap()
            .insert(id.clone(), (owner.into(), sender));
        (id.clone(), ActiveGuard(id), receiver)
    }

    #[test]
    fn destroyed_window_cancels_all_its_transfers_but_no_other_window() {
        let owner = format!("conn-{}", uuid::Uuid::new_v4());
        let other = format!("conn-{}", uuid::Uuid::new_v4());
        let (_upload_id, _upload_guard, upload) = active_for_test(&owner);
        let (_download_id, _download_guard, download) = active_for_test(&owner);
        let (_other_id, _other_guard, other_transfer) = active_for_test(&other);
        cancel_owner("unknown-window");
        assert!(!*upload.borrow());
        cancel_owner(&owner);
        assert!(*upload.borrow());
        assert!(*download.borrow());
        assert!(!*other_transfer.borrow());
        // A duplicate destroy notification remains harmless.
        cancel_owner(&owner);
        assert!(!*other_transfer.borrow());
    }

    #[test]
    fn active_guard_removes_only_its_completed_transfer() {
        let owner = format!("conn-{}", uuid::Uuid::new_v4());
        let (finished_id, finished_guard, finished) = active_for_test(&owner);
        let (running_id, _running_guard, running) = active_for_test(&owner);
        drop(finished_guard);
        {
            let entries = ACTIVE.get().unwrap().lock().unwrap();
            assert!(!entries.contains_key(&finished_id));
            assert!(entries.contains_key(&running_id));
        }
        cancel_owner(&owner);
        assert!(
            !*finished.borrow(),
            "completed work must not be resurrected or cancelled"
        );
        assert!(*running.borrow());
    }

    #[tokio::test]
    async fn upload_plan_counts_real_bytes_and_keeps_hidden_files_and_empty_directories() {
        let root = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(root.path().join("nested/empty")).unwrap();
        std::fs::write(root.path().join(".hidden"), b"123").unwrap();
        std::fs::write(root.path().join("nested/文件.txt"), b"45678").unwrap();
        std::fs::write(root.path().join("zero"), b"").unwrap();
        let mut progress = Vec::new();
        let plan = plan_upload(root.path(), "/upload/tree/", |p| progress.push(p))
            .await
            .unwrap();
        assert_eq!(plan.total_bytes, 8);
        assert_eq!(plan.files.len(), 3);
        assert_eq!(plan.directories.first().unwrap(), "/upload/tree");
        assert!(plan
            .directories
            .contains(&"/upload/tree/nested/empty".into()));
        assert!(plan
            .files
            .iter()
            .any(|f| f.remote == "/upload/tree/.hidden" && f.size == 3));
        let final_progress = progress.last().unwrap();
        assert_eq!(final_progress.phase, "scanning");
        assert_eq!(final_progress.total_bytes, 8);
        assert_eq!(final_progress.total_files, 3);
        assert_eq!(final_progress.bytes, 0);
        assert_eq!(final_progress.files, 0);
    }

    #[tokio::test]
    async fn upload_plan_supports_single_empty_file_and_rejects_unsafe_destinations() {
        let root = tempfile::tempdir().unwrap();
        let local = root.path().join("empty");
        std::fs::write(&local, b"").unwrap();
        let plan = plan_upload(&local, "/srv/renamed", |_| {}).await.unwrap();
        assert!(plan.directories.is_empty());
        assert_eq!(plan.files.len(), 1);
        assert_eq!(plan.files[0].remote, "/srv/renamed");
        assert_eq!(plan.total_bytes, 0);
        for remote in [
            "",
            "/",
            "relative",
            "/srv/../etc",
            "/srv/./file",
            "/srv/bad\0name",
        ] {
            assert!(
                plan_upload(&local, remote, |_| {}).await.is_err(),
                "{remote:?}"
            );
        }
        assert!(plan_upload(Path::new("relative"), "/srv/file", |_| {})
            .await
            .is_err());
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn upload_plan_rejects_symlinks_instead_of_reading_outside_the_selection() {
        let root = tempfile::tempdir().unwrap();
        let selected = root.path().join("selected");
        std::fs::create_dir(&selected).unwrap();
        std::fs::write(root.path().join("outside"), "private").unwrap();
        std::os::unix::fs::symlink(root.path().join("outside"), selected.join("link")).unwrap();
        let error = plan_upload(&selected, "/upload", |_| {})
            .await
            .err()
            .unwrap();
        assert!(error.contains("软链接"));
    }

    #[tokio::test]
    async fn upload_cancel_before_start_does_not_execute_work() {
        let (_sender, mut cancel) = watch::channel(true);
        let error = cancellable_upload(&mut cancel, async {
            panic!("cancelled upload started writing");
            #[allow(unreachable_code)]
            Ok::<(), String>(())
        })
        .await
        .unwrap_err();
        assert!(error.starts_with("上传已取消"));
        assert!(error.contains("保留"));
    }
    #[cfg(target_os = "macos")]
    #[test]
    fn promised_directory_is_published_atomically_and_never_replaces_a_target() {
        let root = tempfile::tempdir().unwrap();
        let staged = root.path().join("staged");
        let target = root.path().join("目标文件夹");
        std::fs::create_dir_all(staged.join("empty")).unwrap();
        std::fs::write(staged.join(".hidden"), "complete").unwrap();
        assert_eq!(publish_promise(&staged, &target).unwrap(), target);
        assert!(!staged.exists());
        assert!(target.join("empty").is_dir());
        assert_eq!(
            std::fs::read_to_string(target.join(".hidden")).unwrap(),
            "complete"
        );
        std::fs::create_dir(&staged).unwrap();
        assert!(publish_promise(&staged, &target).is_err());
        assert!(staged.is_dir());
        let link = root.path().join("link");
        std::os::unix::fs::symlink(&target, &link).unwrap();
        assert!(publish_promise(&staged, &link).is_err());
        assert_eq!(
            std::fs::read_to_string(target.join(".hidden")).unwrap(),
            "complete"
        );
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn promised_publish_race_has_one_winner_and_keeps_losing_staging() {
        let root = tempfile::tempdir().unwrap();
        let target = root.path().join("file");
        let barrier = Arc::new(std::sync::Barrier::new(2));
        let tasks: Vec<_> = ["a", "b"]
            .into_iter()
            .map(|name| {
                let source = root.path().join(name);
                std::fs::write(&source, name).unwrap();
                let target = target.clone();
                let barrier = barrier.clone();
                std::thread::spawn(move || {
                    barrier.wait();
                    (publish_promise(&source, &target), source)
                })
            })
            .collect();
        let results: Vec<_> = tasks.into_iter().map(|t| t.join().unwrap()).collect();
        assert_eq!(
            results.iter().filter(|(result, _)| result.is_ok()).count(),
            1
        );
        assert_eq!(
            results.iter().filter(|(_, source)| source.exists()).count(),
            1
        );
        assert!(["a", "b"].contains(&std::fs::read_to_string(target).unwrap().as_str()));
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn promised_cancellation_removes_staging_without_publishing() {
        let root = tempfile::tempdir().unwrap();
        let stage = tempfile::tempdir_in(root.path()).unwrap();
        let staged = stage.path().join("file");
        std::fs::write(&staged, "partial").unwrap();
        let stage_path = stage.path().to_path_buf();
        let (sender, cancel) = watch::channel(false);
        let prepared = PromisedDownload {
            staged,
            _stage: stage,
            _guard: ActiveGuard("local-test".into()),
            cancel,
        };
        sender.send(true).unwrap();
        let target = root.path().join("target");
        assert!(prepared
            .publish(&target)
            .unwrap_err()
            .starts_with("下载已取消"));
        drop(prepared);
        assert!(!target.exists());
        assert!(!stage_path.exists());
    }
    #[test]
    fn unsafe_remote_names_cannot_escape_destination() {
        for name in ["", ".", "..", "../x", "a/b", "a\\b", "a\0b"] {
            assert!(local_name(name).is_err());
        }
        for name in [".env", "文件.txt", "with space"] {
            assert!(local_name(name).is_ok());
        }
    }
    #[test]
    fn publish_never_overwrites_files_or_directories() {
        let root = tempfile::tempdir().unwrap();
        let source = root.path().join("source");
        let target = root.path().join("file");
        std::fs::write(&source, "new").unwrap();
        std::fs::write(&target, "old").unwrap();
        assert!(publish(&source, &target, false).is_err());
        assert_eq!(std::fs::read_to_string(&target).unwrap(), "old");
        let tree = root.path().join("tree");
        std::fs::create_dir(&tree).unwrap();
        std::fs::create_dir(tree.join("empty")).unwrap();
        let result = publish(&tree, &target, true).unwrap();
        assert_ne!(result, target);
        assert!(result.join("empty").is_dir());
    }

    #[tokio::test]
    async fn cancelling_drops_staging_and_does_not_run_pre_cancelled_work() {
        let root = tempfile::tempdir().unwrap();
        let (tx, mut rx) = watch::channel(false);
        let (ready_tx, ready_rx) = tokio::sync::oneshot::channel();
        let parent = root.path().to_path_buf();
        let task = tokio::spawn(async move {
            cancellable(&mut rx, async move {
                let stage = tempfile::tempdir_in(parent).unwrap();
                std::fs::write(stage.path().join("partial"), b"incomplete").unwrap();
                ready_tx.send(stage.path().to_path_buf()).unwrap();
                std::future::pending::<()>().await;
                drop(stage);
                Ok(())
            })
            .await
        });
        let stage = ready_rx.await.unwrap();
        assert!(stage.exists());
        tx.send(true).unwrap();
        assert!(task.await.unwrap().unwrap_err().contains("取消"));
        assert!(!stage.exists());
        let (_, mut already_cancelled) = watch::channel(true);
        assert!(cancellable(&mut already_cancelled, async {
            panic!("cancelled task ran");
            #[allow(unreachable_code)]
            Ok::<(), String>(())
        })
        .await
        .is_err());
    }

    #[test]
    fn publish_preserves_nested_content_empty_directories_and_hidden_files() {
        let root = tempfile::tempdir().unwrap();
        let source = root.path().join("stage");
        std::fs::create_dir_all(source.join("层级/empty")).unwrap();
        std::fs::write(source.join("层级/.hidden"), [0, 128, 255]).unwrap();
        let target = root.path().join("download");
        assert_eq!(publish(&source, &target, true).unwrap(), target);
        assert!(target.join("层级/empty").is_dir());
        assert_eq!(
            std::fs::read(target.join("层级/.hidden")).unwrap(),
            [0, 128, 255]
        );
    }
}
