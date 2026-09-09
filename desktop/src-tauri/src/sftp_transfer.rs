//! Downloads are staged locally and only published after completion. Drag-out
//! accepts opaque prepared IDs, never arbitrary frontend-provided local paths.
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

type Active = HashMap<String, (String, watch::Sender<bool>)>;
static ACTIVE: OnceLock<Mutex<Active>> = OnceLock::new();
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
            return Err("不能取消其他窗口的下载".into());
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
