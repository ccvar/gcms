//! Bridge a real AppKit file drop to HTML without intercepting WebKit's drop.
//!
//! The drag pasteboard survives its gesture, so reading it from IPC alone is not
//! safe. A passive observer first binds paths to an actual native drag sequence;
//! only a successful native drop can redeem a short-lived, one-use snapshot.
use serde::{Deserialize, Serialize};
use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::{Mutex, OnceLock},
    time::{Duration, Instant, UNIX_EPOCH},
};
#[cfg(target_os = "macos")]
use tauri::Manager;

const TTL: Duration = Duration::from_secs(30);
const LATE_CAPTURE: Duration = Duration::from_secs(2);
const MAX_ENTRIES: usize = 4096;

#[derive(Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DropEntry {
    name: String,
    size: u64,
    mtime: u64,
    directory: Option<bool>,
}

#[derive(Serialize)]
pub struct CapturedDrop {
    token: String,
    count: usize,
}

#[derive(Serialize)]
pub struct ConsumedDrop {
    paths: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct Identity {
    name: String,
    directory: bool,
    size: u64,
    modified_ns: u128,
    #[cfg(unix)]
    device: u64,
    #[cfg(unix)]
    inode: u64,
}

fn identity(path: &Path) -> Result<Identity, String> {
    if !path.is_absolute() {
        return Err("拖入文件不是绝对本地路径".into());
    }
    let metadata = std::fs::symlink_metadata(path).map_err(|e| format!("读取拖入文件失败: {e}"))?;
    if metadata.file_type().is_symlink() || !(metadata.is_file() || metadata.is_dir()) {
        return Err("不支持拖入符号链接或特殊文件，请选择原始文件".into());
    }
    let name = path
        .file_name()
        .and_then(|n| n.to_str())
        .ok_or("文件名无法安全读取")?;
    if name.is_empty() || name == "." || name == ".." {
        return Err("无效的拖入文件名".into());
    }
    path.to_str().ok_or("文件路径不是有效的 UTF-8")?;
    #[cfg(unix)]
    use std::os::unix::fs::MetadataExt;
    Ok(Identity {
        name: name.to_owned(),
        directory: metadata.is_dir(),
        size: metadata.len(),
        modified_ns: metadata
            .modified()
            .map_err(|e| e.to_string())?
            .duration_since(UNIX_EPOCH)
            .map_err(|_| "文件时间早于 Unix 纪元")?
            .as_nanos(),
        #[cfg(unix)]
        device: metadata.dev(),
        #[cfg(unix)]
        inode: metadata.ino(),
    })
}

impl Identity {
    fn matches(&self, entry: &DropEntry) -> bool {
        if self.name != entry.name || entry.directory.is_some_and(|d| d != self.directory) {
            return false;
        }
        if self.directory {
            // WebKit directory File size/lastModified are synthetic. Require its
            // explicit directory entry; never reinterpret an unknown/empty File.
            return entry.directory == Some(true);
        }
        self.size == entry.size
            && (self.modified_ns / 1_000_000).abs_diff(entry.mtime as u128) <= 1000
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct Anchor {
    view: usize,
    sequence: isize,
    count: isize,
    generation: u64,
    pasteboard: String,
}

#[derive(Clone, Copy)]
enum Phase {
    Hover,
    Dropping,
    Dropped(Instant),
    Consumed,
}

#[derive(Clone)]
struct Gesture {
    anchor: Anchor,
    paths: Result<Vec<PathBuf>, String>,
    phase: Phase,
}

struct Snapshot {
    token: String,
    anchor: Anchor,
    created: Instant,
    paths: Vec<PathBuf>,
    identities: Vec<Identity>,
}

#[derive(Default)]
struct Registry {
    views: HashMap<usize, String>,
    gestures: HashMap<String, Gesture>,
    snapshots: HashMap<String, Snapshot>,
    generation: u64,
    capture_serial: u64,
    capture_tickets: HashMap<String, u64>,
}

impl Registry {
    fn begin(&mut self, owner: &str, mut gesture: Gesture) {
        self.generation = self.generation.wrapping_add(1);
        gesture.anchor.generation = self.generation;
        self.snapshots.remove(owner);
        self.capture_tickets.remove(owner);
        self.gestures.insert(owner.into(), gesture);
    }

    fn clear(&mut self, owner: &str) {
        self.snapshots.remove(owner);
        self.gestures.remove(owner);
        self.capture_tickets.remove(owner);
    }

    fn capture_gesture(
        &mut self,
        owner: &str,
        pressed: bool,
        now: Instant,
    ) -> Result<(Gesture, u64), String> {
        let gesture = self
            .gestures
            .get(owner)
            .ok_or("没有当前原生文件拖动，请重新拖入或使用上传按钮")?;
        match gesture.phase {
            Phase::Hover if pressed => {}
            // IPC may arrive after mouse-up. Only the just-acknowledged real
            // native drop, not a bare pasteboard, is allowed in this case.
            Phase::Dropped(at) if now.saturating_duration_since(at) <= LATE_CAPTURE => {}
            _ => return Err("拖动已经取消或过期，请重新拖入".into()),
        }
        gesture.paths.as_ref().map_err(Clone::clone)?;
        self.capture_serial = self.capture_serial.wrapping_add(1);
        self.capture_tickets
            .insert(owner.into(), self.capture_serial);
        Ok((gesture.clone(), self.capture_serial))
    }

    fn capture(
        &mut self,
        owner: &str,
        gesture: Gesture,
        ticket: u64,
        identities: Vec<Identity>,
        now: Instant,
    ) -> Result<CapturedDrop, String> {
        let current = self.gestures.get(owner).ok_or("拖动已结束，请重新拖入")?;
        if current.anchor != gesture.anchor
            || matches!(current.phase, Phase::Dropping | Phase::Consumed)
            || self.capture_tickets.get(owner) != Some(&ticket)
        {
            return Err("拖动内容已改变，请重新拖入".into());
        }
        let paths = gesture.paths?;
        if paths.is_empty() || paths.len() != identities.len() || paths.len() > MAX_ENTRIES {
            return Err("拖入文件列表不完整".into());
        }
        let token = uuid::Uuid::new_v4().to_string();
        self.capture_tickets.remove(owner);
        let result = CapturedDrop {
            token: token.clone(),
            count: paths.len(),
        };
        self.snapshots.insert(
            owner.into(),
            Snapshot {
                token,
                anchor: gesture.anchor,
                created: now,
                paths,
                identities,
            },
        );
        Ok(result)
    }

    fn take(&mut self, owner: &str, token: &str, now: Instant) -> Result<Snapshot, String> {
        if self.snapshots.get(owner).map(|s| s.token.as_str()) != Some(token) {
            return Err("拖动令牌无效或已使用，请重新拖入".into());
        }
        // Invalid attempts burn only this owner's matching token. A late old
        // token must not delete a new gesture or another window's selection.
        let snapshot = self.snapshots.remove(owner).ok_or("拖动令牌已使用")?;
        if now.saturating_duration_since(snapshot.created) > TTL {
            return Err("拖动已过期，请重新拖入".into());
        }
        let gesture = self.gestures.get_mut(owner).ok_or("拖动已取消")?;
        if gesture.anchor != snapshot.anchor {
            return Err("拖动内容已改变，请重新拖入".into());
        }
        match gesture.phase {
            Phase::Dropped(at) if now.saturating_duration_since(at) <= TTL => {
                gesture.phase = Phase::Consumed;
                Ok(snapshot)
            }
            _ => Err("尚未确认原生文件落下，未上传任何文件".into()),
        }
    }
}

fn registry() -> &'static Mutex<Registry> {
    static REGISTRY: OnceLock<Mutex<Registry>> = OnceLock::new();
    REGISTRY.get_or_init(Default::default)
}

fn verify(
    snapshot: Snapshot,
    entries: Vec<DropEntry>,
    count: isize,
) -> Result<ConsumedDrop, String> {
    if snapshot.anchor.count != count || entries.len() != snapshot.paths.len() || entries.is_empty()
    {
        return Err("拖入文件列表已改变，请重新拖入；未上传任何文件".into());
    }
    let current = snapshot
        .paths
        .iter()
        .map(|p| identity(p))
        .collect::<Result<Vec<_>, _>>()?;
    if current != snapshot.identities {
        return Err("拖入文件已发生变化，请重新拖入；未上传任何文件".into());
    }
    let mut used = vec![false; entries.len()];
    let mut paths = Vec::with_capacity(entries.len());
    // Match a multiset, not a name map: two selected directories may have the
    // same basename. Every native item must be consumed exactly once.
    for entry in entries {
        let index = current
            .iter()
            .enumerate()
            .position(|(i, value)| !used[i] && value.matches(&entry))
            .ok_or("浏览器文件与原生拖动不一致，请重新拖入；未上传任何文件")?;
        used[index] = true;
        paths.push(
            snapshot.paths[index]
                .to_str()
                .ok_or("文件路径无法读取")?
                .to_owned(),
        );
    }
    Ok(ConsumedDrop { paths })
}

pub fn clear_owner(owner: &str) {
    if let Ok(mut state) = registry().lock() {
        state.clear(owner);
        state.views.retain(|_, value| value != owner);
    }
}

#[tauri::command]
pub async fn sftp_capture_local_drop(window: tauri::Window) -> Result<CapturedDrop, String> {
    #[cfg(target_os = "macos")]
    {
        let owner = window.label().to_owned();
        let for_main = owner.clone();
        let (gesture, ticket) = native::on_main(window.app_handle().clone(), move || {
            let pressed = objc2_app_kit::NSEvent::pressedMouseButtons() & 1 != 0;
            registry()
                .lock()
                .map_err(|_| "拖动状态不可用")?
                .capture_gesture(&for_main, pressed, Instant::now())
        })
        .await?;
        let paths = gesture.paths.clone()?;
        let identities = tokio::task::spawn_blocking(move || {
            paths
                .iter()
                .map(|p| identity(p))
                .collect::<Result<Vec<_>, _>>()
        })
        .await
        .map_err(|e| e.to_string())??;
        registry().lock().map_err(|_| "拖动状态不可用")?.capture(
            &owner,
            gesture,
            ticket,
            identities,
            Instant::now(),
        )
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = window;
        Err("当前平台暂不支持原生文件拖入，请使用上传按钮".into())
    }
}

#[tauri::command]
pub async fn sftp_consume_local_drop(
    window: tauri::Window,
    token: String,
    entries: Vec<DropEntry>,
) -> Result<ConsumedDrop, String> {
    #[cfg(target_os = "macos")]
    {
        let snapshot = registry().lock().map_err(|_| "拖动状态不可用")?.take(
            window.label(),
            &token,
            Instant::now(),
        )?;
        let pasteboard = snapshot.anchor.pasteboard.clone();
        let anchor = snapshot.anchor.clone();
        let count = native::on_main(window.app_handle().clone(), move || {
            Ok(native::pasteboard_count(&pasteboard))
        })
        .await?;
        let result = tokio::task::spawn_blocking(move || verify(snapshot, entries, count))
            .await
            .map_err(|e| e.to_string())??;
        let state = registry().lock().map_err(|_| "拖动状态不可用")?;
        if !state
            .gestures
            .get(window.label())
            .is_some_and(|g| g.anchor == anchor && matches!(g.phase, Phase::Consumed))
        {
            return Err("拖动窗口或内容已改变，未上传任何文件".into());
        }
        Ok(result)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (window, token, entries);
        Err("当前平台暂不支持原生文件拖入，请使用上传按钮".into())
    }
}

#[tauri::command]
pub fn sftp_discard_local_drop(window: tauri::Window, token: String) {
    if let Ok(mut state) = registry().lock() {
        if state
            .snapshots
            .get(window.label())
            .map(|s| s.token.as_str())
            == Some(token.as_str())
        {
            state.snapshots.remove(window.label());
        }
    }
}

pub fn plugin() -> tauri::plugin::TauriPlugin<tauri::Wry> {
    tauri::plugin::Builder::new("sftp-local-drop")
        .on_webview_ready(|webview| {
            #[cfg(target_os = "macos")]
            {
                let owner = webview.window().label().to_owned();
                // Never observe the external preview window.
                if owner == "main" || owner.starts_with("conn-") || owner.starts_with("theme-") {
                    let _ = webview.with_webview(move |platform| {
                        if let Err(error) =
                            unsafe { native::install(platform.inner().cast(), owner) }
                        {
                            eprintln!("Native local-file drop unavailable: {error}");
                        } else {
                            eprintln!("Native local-file drop observer ready");
                        }
                    });
                }
            }
            #[cfg(not(target_os = "macos"))]
            let _ = webview;
        })
        .build()
}

#[cfg(target_os = "macos")]
mod native {
    use super::*;
    use objc2::{
        msg_send,
        rc::Retained,
        runtime::{AnyClass, AnyObject, Bool, Imp, Sel},
        sel, ClassType,
    };
    use objc2_app_kit::{
        NSDragOperation, NSPasteboard, NSPasteboardTypeFileURL,
        NSPasteboardURLReadingFileURLsOnlyKey,
    };
    use objc2_foundation::{NSArray, NSDictionary, NSNumber, NSString, NSURL};
    use std::{cell::RefCell, ffi::CStr, os::unix::ffi::OsStrExt};

    // objc2 0.6 generates module_path + class name + package version when Wry
    // omits #[name]. This exact class is pinned to the Wry implementation we
    // reviewed in Cargo.lock; a dependency upgrade must be reviewed again.
    const WRY_CLASS: &[u8] = b"wry::wkwebview::class::wry_web_view::WryWebView0.55.1";

    type Operation =
        unsafe extern "C-unwind" fn(*mut AnyObject, Sel, &AnyObject) -> NSDragOperation;
    type Perform = unsafe extern "C-unwind" fn(*mut AnyObject, Sel, &AnyObject) -> Bool;
    type Exit = unsafe extern "C-unwind" fn(*mut AnyObject, Sel, &AnyObject);
    #[derive(Clone, Copy)]
    struct Hooks {
        class: usize,
        entered: Operation,
        updated: Operation,
        perform: Perform,
        exited: Exit,
    }
    thread_local! { static HOOKS: RefCell<Option<Hooks>> = const { RefCell::new(None) }; }

    // The Objective-C ABI only contains an object pointer for NSDraggingInfo.
    // Type erasure also lets local tests supply a thread-safe mock object with
    // these selectors instead of constructing AppKit's private drag-session type.
    fn info_pasteboard(info: &AnyObject) -> Retained<NSPasteboard> {
        unsafe { msg_send![info, draggingPasteboard] }
    }
    fn info_sequence(info: &AnyObject) -> isize {
        unsafe { msg_send![info, draggingSequenceNumber] }
    }

    pub async fn on_main<T: Send + 'static>(
        app: tauri::AppHandle,
        work: impl FnOnce() -> Result<T, String> + Send + 'static,
    ) -> Result<T, String> {
        let (send, receive) = tokio::sync::oneshot::channel();
        app.run_on_main_thread(move || {
            let _ = send.send(work());
        })
        .map_err(|e| e.to_string())?;
        receive
            .await
            .map_err(|_| "原生拖动窗口已关闭".to_string())?
    }

    pub fn pasteboard_count(name: &str) -> isize {
        NSPasteboard::pasteboardWithName(&NSString::from_str(name)).changeCount()
    }

    fn paths(pasteboard: &NSPasteboard) -> Result<Vec<PathBuf>, String> {
        let items = pasteboard
            .pasteboardItems()
            .ok_or("当前拖动不包含本地文件项目")?;
        if items.is_empty()
            || items.len() > MAX_ENTRIES
            || items.iter().any(|item| {
                !item
                    .types()
                    .iter()
                    .any(|kind| &*kind == unsafe { NSPasteboardTypeFileURL })
            })
        {
            return Err("仅支持原生文件 URL，不接受文本或网络链接".into());
        }
        let classes = NSArray::from_slice(&[NSURL::class()]);
        // Do not read plain strings / legacy text URLs as local paths. NSURL's
        // pasteboard reader supplies actual URL objects; every one must be local.
        let yes = NSNumber::numberWithBool(true);
        let options = NSDictionary::<NSString, AnyObject>::from_slices(
            &[unsafe { NSPasteboardURLReadingFileURLsOnlyKey }],
            &[yes.as_ref()],
        );
        let objects = unsafe { pasteboard.readObjectsForClasses_options(&classes, Some(&options)) }
            .ok_or("当前拖动不包含原生本地文件")?;
        if objects.len() != items.len() {
            return Err("拖入文件数量不受支持".into());
        }
        objects
            .iter()
            .map(|object| {
                let url = object
                    .downcast_ref::<NSURL>()
                    .ok_or("拖入内容不是文件 URL")?;
                if !url.isFileURL()
                    || url
                        .host()
                        .is_some_and(|h| !h.is_empty() && h.to_string() != "localhost")
                {
                    return Err("仅支持本地文件拖入，不支持网络 URL".into());
                }
                let url = url.filePathURL().ok_or("拖入文件 URL 无法解析")?;
                let bytes =
                    unsafe { CStr::from_ptr(url.fileSystemRepresentation().as_ptr()) }.to_bytes();
                let path = PathBuf::from(std::ffi::OsStr::from_bytes(bytes));
                if !path.is_absolute() {
                    return Err("拖入文件路径无效".into());
                }
                Ok(path)
            })
            .collect()
    }

    fn observe(view: *mut AnyObject, info: &AnyObject, entered: bool) {
        let view = view as usize;
        let Ok(state) = registry().lock() else {
            return;
        };
        let Some(owner) = state.views.get(&view).cloned() else {
            return;
        };
        drop(state);
        let pasteboard = info_pasteboard(info);
        let count = pasteboard.changeCount();
        let sequence = info_sequence(info);
        let Ok(state) = registry().lock() else {
            return;
        };
        let unchanged = state.gestures.get(&owner).is_some_and(|g| {
            g.anchor.view == view
                && g.anchor.sequence == sequence
                && g.anchor.count == count
                && matches!(g.phase, Phase::Hover)
        });
        let already_dropped = state.gestures.get(&owner).is_some_and(|g| {
            g.anchor.view == view
                && g.anchor.sequence == sequence
                && matches!(g.phase, Phase::Dropped(_) | Phase::Consumed)
        });
        drop(state);
        if already_dropped {
            return;
        }
        // An update cannot resurrect a cancelled/missing/finished gesture. Only
        // a real entered callback may establish a new native selection.
        if !entered {
            if !unchanged {
                if let Ok(mut state) = registry().lock() {
                    state.clear(&owner);
                }
            }
            return;
        }
        let paths = paths(&pasteboard);
        let paths = if pasteboard.changeCount() == count {
            paths
        } else {
            Err("拖动剪贴板内容已改变".into())
        };
        if let Ok(mut state) = registry().lock() {
            state.begin(
                &owner,
                Gesture {
                    anchor: Anchor {
                        view,
                        sequence,
                        count,
                        generation: 0,
                        pasteboard: pasteboard.name().to_string(),
                    },
                    paths,
                    phase: Phase::Hover,
                },
            );
        }
    }

    fn phase(view: *mut AnyObject, info: &AnyObject, phase: Option<Phase>) {
        let sequence = info_sequence(info);
        let count = info_pasteboard(info).changeCount();
        if let Ok(mut state) = registry().lock() {
            let Some(owner) = state.views.get(&(view as usize)).cloned() else {
                return;
            };
            let Some(gesture) = state.gestures.get_mut(&owner) else {
                return;
            };
            if gesture.anchor.view != view as usize || gesture.anchor.sequence != sequence {
                return;
            }
            if gesture.anchor.count != count || phase.is_none() {
                // Exit after an acknowledged drop is not cancellation.
                if phase.is_none() && matches!(gesture.phase, Phase::Dropped(_) | Phase::Consumed) {
                    return;
                }
                state.clear(&owner);
            } else if let Some(phase) = phase {
                gesture.phase = phase;
            }
        }
    }

    unsafe extern "C-unwind" fn entered(
        view: *mut AnyObject,
        selector: Sel,
        info: &AnyObject,
    ) -> NSDragOperation {
        let original = HOOKS.with(|h| h.borrow().map(|h| h.entered));
        observe(view, info, true);
        original
            .map(|f| unsafe { f(view, selector, info) })
            .unwrap_or(NSDragOperation::None)
    }
    unsafe extern "C-unwind" fn updated(
        view: *mut AnyObject,
        selector: Sel,
        info: &AnyObject,
    ) -> NSDragOperation {
        let original = HOOKS.with(|h| h.borrow().map(|h| h.updated));
        observe(view, info, false);
        original
            .map(|f| unsafe { f(view, selector, info) })
            .unwrap_or(NSDragOperation::None)
    }
    unsafe extern "C-unwind" fn perform(
        view: *mut AnyObject,
        selector: Sel,
        info: &AnyObject,
    ) -> Bool {
        let original = HOOKS.with(|h| h.borrow().map(|h| h.perform));
        phase(view, info, Some(Phase::Dropping));
        let accepted = original
            .map(|f| unsafe { f(view, selector, info) })
            .unwrap_or(Bool::NO);
        if accepted.as_bool() {
            phase(view, info, Some(Phase::Dropped(Instant::now())));
        } else {
            phase(view, info, None);
        }
        accepted
    }
    unsafe extern "C-unwind" fn exited(view: *mut AnyObject, selector: Sel, info: &AnyObject) {
        let original = HOOKS.with(|h| h.borrow().map(|h| h.exited));
        phase(view, info, None);
        if let Some(original) = original {
            unsafe { original(view, selector, info) };
        }
    }

    /// Installs once, before the first gesture. Only the known Wry class is
    /// eligible; a changed runtime/ABI disables the bridge, never guesses paths.
    pub unsafe fn install(view: *mut AnyObject, owner: String) -> Result<(), String> {
        let _main = objc2::MainThreadMarker::new().ok_or("原生拖动只能在主线程初始化")?;
        unsafe { install_checked(view, owner, WRY_CLASS) }
    }

    fn hook_class<'a>(actual: &'a AnyClass, expected: &[u8]) -> Result<&'a AnyClass, String> {
        if actual.name().to_bytes() == expected {
            return Ok(actual);
        }
        // Observed WebKit KVO proxy: permit exactly one known, transparent
        // layer. Do not walk arbitrary ancestors or accept generic WKWebViews.
        let mut kvo_name = b"..NSKVONotifying_".to_vec();
        kvo_name.extend_from_slice(expected);
        if actual.name().to_bytes() != kvo_name {
            return Err(format!(
                "不兼容的 WebView 类: {}",
                actual.name().to_string_lossy()
            ));
        }
        let base = actual.superclass().ok_or("KVO WebView 缺少直接父类")?;
        if base.name().to_bytes() != expected {
            return Err(format!(
                "KVO WebView 父类不兼容: {}",
                base.name().to_string_lossy()
            ));
        }
        let overrides = actual.instance_methods();
        for selector in [
            sel!(draggingEntered:),
            sel!(draggingUpdated:),
            sel!(performDragOperation:),
            sel!(draggingExited:),
        ] {
            if overrides.iter().any(|method| method.name() == selector) {
                return Err(format!(
                    "KVO WebView 改写了拖动方法: {}",
                    selector.name().to_string_lossy()
                ));
            }
        }
        Ok(base)
    }

    unsafe fn install_checked(
        view: *mut AnyObject,
        owner: String,
        expected_class: &[u8],
    ) -> Result<(), String> {
        let view_ref = unsafe { view.as_ref() }.ok_or("WebView 不可用")?;
        let actual = view_ref.class();
        let class = hook_class(actual, expected_class)?;
        if HOOKS.with(|h| {
            h.borrow()
                .is_some_and(|h| h.class != class as *const AnyClass as usize)
        }) {
            return Err("原生拖动观察器已经绑定其他类".into());
        }
        if !HOOKS.with(|h| h.borrow().is_some()) {
            let own_methods = class.instance_methods();
            for selector in [
                sel!(draggingEntered:),
                sel!(draggingUpdated:),
                sel!(performDragOperation:),
                sel!(draggingExited:),
            ] {
                if !own_methods.iter().any(|method| method.name() == selector) {
                    return Err("不兼容的 WebView 拖动方法继承关系".into());
                }
            }
            class
                .verify_sel::<(&AnyObject,), NSDragOperation>(sel!(draggingEntered:))
                .map_err(|e| e.to_string())?;
            class
                .verify_sel::<(&AnyObject,), NSDragOperation>(sel!(draggingUpdated:))
                .map_err(|e| e.to_string())?;
            class
                .verify_sel::<(&AnyObject,), Bool>(sel!(performDragOperation:))
                .map_err(|e| e.to_string())?;
            class
                .verify_sel::<(&AnyObject,), ()>(sel!(draggingExited:))
                .map_err(|e| e.to_string())?;
            let methods = [
                sel!(draggingEntered:),
                sel!(draggingUpdated:),
                sel!(performDragOperation:),
                sel!(draggingExited:),
            ]
            .map(|s| class.instance_method(s).ok_or("原生拖动方法缺失"));
            let [entered_method, updated_method, perform_method, exited_method] = methods;
            let (a, b, c, d) = (
                entered_method?,
                updated_method?,
                perform_method?,
                exited_method?,
            );
            // All calls run on AppKit's main thread, no drag callback can run
            // between these replacements. Store exact previous IMPs, not super.
            let originals = Hooks {
                class: class as *const AnyClass as usize,
                entered: unsafe { std::mem::transmute::<Imp, Operation>(a.implementation()) },
                updated: unsafe { std::mem::transmute::<Imp, Operation>(b.implementation()) },
                perform: unsafe { std::mem::transmute::<Imp, Perform>(c.implementation()) },
                exited: unsafe { std::mem::transmute::<Imp, Exit>(d.implementation()) },
            };
            HOOKS.with(|h| *h.borrow_mut() = Some(originals));
            unsafe {
                a.set_implementation(std::mem::transmute::<Operation, Imp>(entered));
                b.set_implementation(std::mem::transmute::<Operation, Imp>(updated));
                c.set_implementation(std::mem::transmute::<Perform, Imp>(perform));
                d.set_implementation(std::mem::transmute::<Exit, Imp>(exited));
            }
        }
        let mut state = registry().lock().map_err(|_| "拖动状态不可用")?;
        if state.views.get(&(view as usize)) == Some(&owner) {
            return Ok(());
        }
        state.clear(&owner);
        state.views.insert(view as usize, owner);
        eprintln!(
            "Native local-file drop verified class: {} (base: {})",
            actual.name().to_string_lossy(),
            class.name().to_string_lossy()
        );
        Ok(())
    }

    #[cfg(test)]
    mod native_tests {
        use super::*;
        use objc2::{
            define_class, msg_send,
            rc::Retained,
            runtime::{ClassBuilder, NSObject, NSObjectProtocol, ProtocolObject},
            AnyThread, DefinedClass,
        };
        use objc2_app_kit::{
            NSPasteboardItem, NSPasteboardTypeString, NSPasteboardTypeURL, NSPasteboardWriting,
        };
        use std::cell::Cell;

        #[derive(Default)]
        struct DestinationState {
            entered: Cell<u32>,
            updated: Cell<u32>,
            performed: Cell<u32>,
            exited: Cell<u32>,
            accepted: Cell<bool>,
        }
        define_class!(
            #[unsafe(super(NSObject))]
            #[name = "PilotLocalDropTestDestination"]
            #[ivars = DestinationState]
            struct Destination;
            unsafe impl NSObjectProtocol for Destination {}
            impl Destination {
                #[unsafe(method(draggingEntered:))]
                fn enter(&self, _info: &AnyObject) -> NSDragOperation {
                    self.ivars().entered.set(self.ivars().entered.get()+1); NSDragOperation::Link
                }
                #[unsafe(method(draggingUpdated:))]
                fn update(&self, _info: &AnyObject) -> NSDragOperation {
                    self.ivars().updated.set(self.ivars().updated.get()+1); NSDragOperation::Move
                }
                #[unsafe(method(performDragOperation:))]
                fn perform(&self, _info: &AnyObject) -> Bool {
                    self.ivars().performed.set(self.ivars().performed.get()+1); Bool::new(self.ivars().accepted.get())
                }
                #[unsafe(method(draggingExited:))]
                fn exit(&self, _info: &AnyObject) { self.ivars().exited.set(self.ivars().exited.get()+1); }
            }
        );
        struct InfoState {
            pasteboard: Retained<NSPasteboard>,
            sequence: Cell<isize>,
        }
        define_class!(
            #[unsafe(super(NSObject))]
            #[name = "PilotLocalDropTestInfo"]
            #[ivars = InfoState]
            struct Info;
            unsafe impl NSObjectProtocol for Info {}
            impl Info {
                #[unsafe(method_id(draggingPasteboard))]
                fn pasteboard(&self) -> Retained<NSPasteboard> {
                    self.ivars().pasteboard.clone()
                }
                #[unsafe(method(draggingSequenceNumber))]
                fn sequence(&self) -> isize {
                    self.ivars().sequence.get()
                }
            }
        );

        // This calls the Objective-C selectors dynamically through real replaced
        // IMPs, on a local mock destination. It does not launch Pilot or use SSH.
        // It verifies native forwarding, not Finder/WebKit end-to-end behavior.
        #[test]
        fn native_drop_callbacks_preserve_originals_and_require_drop_ack() {
            objc2::rc::autoreleasepool(|_| {
                let dir = tempfile::tempdir().unwrap();
                let file = dir.path().join("native-large.bin");
                let f = std::fs::File::create(&file).unwrap();
                f.set_len(80 * 1024 * 1024).unwrap(); // sparse; no byte-buffer fallback
                let folder = dir.path().join("目录");
                std::fs::create_dir(&folder).unwrap();
                let pasteboard = NSPasteboard::pasteboardWithUniqueName();
                let urls = [
                    NSURL::fileURLWithPath(&NSString::from_str(file.to_str().unwrap())),
                    NSURL::fileURLWithPath(&NSString::from_str(folder.to_str().unwrap())),
                ];
                let objects = NSArray::from_slice(&[
                    ProtocolObject::<dyn NSPasteboardWriting>::from_ref(&*urls[0]),
                    ProtocolObject::from_ref(&*urls[1]),
                ]);
                assert!(pasteboard.writeObjects(&objects));
                assert_eq!(
                    paths(&pasteboard).unwrap(),
                    vec![file.clone(), folder.clone()]
                );
                let info: Retained<Info> = unsafe {
                    msg_send![
                        super(Info::alloc().set_ivars(InfoState {
                            pasteboard: pasteboard.clone(),
                            sequence: Cell::new(41)
                        })),
                        init
                    ]
                };
                let destination: Retained<Destination> = unsafe {
                    msg_send![
                        super(Destination::alloc().set_ivars(DestinationState::default())),
                        init
                    ]
                };
                let kvo_name =
                    std::ffi::CString::new("..NSKVONotifying_PilotLocalDropTestDestination")
                        .unwrap();
                let kvo = ClassBuilder::new(&kvo_name, Destination::class())
                    .unwrap()
                    .register();
                assert_eq!(kvo.instance_size(), Destination::class().instance_size());
                // Reproduce KVO's transparent isa replacement on an already
                // initialized object; no additional ivars or methods are added.
                unsafe {
                    AnyObject::set_class(&destination, kvo);
                }
                let view = Retained::as_ptr(&destination) as *mut AnyObject;
                let owner = "native-local-drop-test";
                assert!(unsafe { install_checked(view, owner.into(), b"WrongClass") }.is_err());
                unsafe { install_checked(view, owner.into(), b"PilotLocalDropTestDestination") }
                    .unwrap();
                unsafe { install_checked(view, owner.into(), b"PilotLocalDropTestDestination") }
                    .unwrap();
                let info_ref: &AnyObject = &info;
                let other_destination: Retained<Destination> = unsafe {
                    msg_send![
                        super(Destination::alloc().set_ivars(DestinationState::default())),
                        init
                    ]
                };
                let other_view = Retained::as_ptr(&other_destination) as *mut AnyObject;
                let other_owner = "native-local-drop-second-window";
                unsafe {
                    install_checked(
                        other_view,
                        other_owner.into(),
                        b"PilotLocalDropTestDestination",
                    )
                }
                .unwrap();
                let _: NSDragOperation =
                    unsafe { msg_send![&*other_destination, draggingEntered: info_ref] };
                let result: NSDragOperation =
                    unsafe { msg_send![&*destination, draggingEntered: info_ref] };
                assert_eq!(result, NSDragOperation::Link);
                let result: NSDragOperation =
                    unsafe { msg_send![&*destination, draggingUpdated: info_ref] };
                assert_eq!(result, NSDragOperation::Move);
                let (gesture, ticket) = registry()
                    .lock()
                    .unwrap()
                    .capture_gesture(owner, true, Instant::now())
                    .unwrap();
                let ids = [identity(&file).unwrap(), identity(&folder).unwrap()].to_vec();
                let token = registry()
                    .lock()
                    .unwrap()
                    .capture(owner, gesture, ticket, ids, Instant::now())
                    .unwrap();
                assert_eq!(
                    registry()
                        .lock()
                        .unwrap()
                        .gestures
                        .get(other_owner)
                        .unwrap()
                        .anchor
                        .view,
                    other_view as usize
                );
                assert!(registry()
                    .lock()
                    .unwrap()
                    .take(other_owner, &token.token, Instant::now())
                    .is_err());
                let result: Bool =
                    unsafe { msg_send![&*destination, performDragOperation: info_ref] };
                assert!(!result.as_bool());
                assert!(registry()
                    .lock()
                    .unwrap()
                    .take(owner, &token.token, Instant::now())
                    .is_err());
                info.ivars().sequence.set(42);
                destination.ivars().accepted.set(true);
                let _: NSDragOperation =
                    unsafe { msg_send![&*destination, draggingEntered: info_ref] };
                let result: Bool =
                    unsafe { msg_send![&*destination, performDragOperation: info_ref] };
                assert!(result.as_bool());
                let (gesture, ticket) = registry()
                    .lock()
                    .unwrap()
                    .capture_gesture(owner, false, Instant::now())
                    .unwrap();
                let ids = [identity(&file).unwrap(), identity(&folder).unwrap()].to_vec();
                let entries = vec![
                    super::super::tests::entry(&ids[1]),
                    super::super::tests::entry(&ids[0]),
                ];
                let token = registry()
                    .lock()
                    .unwrap()
                    .capture(owner, gesture, ticket, ids, Instant::now())
                    .unwrap();
                let _: () = unsafe { msg_send![&*destination, draggingExited: info_ref] };
                let snapshot = registry()
                    .lock()
                    .unwrap()
                    .take(owner, &token.token, Instant::now())
                    .unwrap();
                let result = verify(snapshot, entries, pasteboard.changeCount()).unwrap();
                assert_eq!(
                    result.paths,
                    vec![folder.to_str().unwrap(), file.to_str().unwrap()]
                );
                assert_eq!(
                    (
                        destination.ivars().entered.get(),
                        destination.ivars().updated.get(),
                        destination.ivars().performed.get(),
                        destination.ivars().exited.get()
                    ),
                    (2, 1, 2, 1)
                );
                assert!(registry()
                    .lock()
                    .unwrap()
                    .capture_gesture(owner, false, Instant::now())
                    .is_err());
                clear_owner(owner);
                clear_owner(other_owner);
                unsafe {
                    AnyObject::set_class(&destination, Destination::class());
                }
                // NSURL readers alone can interpret URL-shaped strings. Only
                // explicit file-URL pasteboard items are accepted by our bridge.
                for kind in [unsafe { NSPasteboardTypeString }, unsafe {
                    NSPasteboardTypeURL
                }] {
                    pasteboard.clearContents();
                    let item = NSPasteboardItem::new();
                    assert!(item.setString_forType(&urls[0].absoluteString().unwrap(), kind));
                    assert!(
                        pasteboard.writeObjects(&NSArray::from_slice(&[ProtocolObject::<
                            dyn NSPasteboardWriting,
                        >::from_ref(
                            &*item
                        )]))
                    );
                    assert!(paths(&pasteboard).is_err());
                }
                pasteboard.clearContents();
                let forged = NSPasteboardItem::new();
                assert!(forged.setString_forType(
                    &NSString::from_str("https://example.invalid/file.txt"),
                    unsafe { NSPasteboardTypeFileURL }
                ));
                assert!(
                    pasteboard.writeObjects(&NSArray::from_slice(&[ProtocolObject::<
                        dyn NSPasteboardWriting,
                    >::from_ref(
                        &*forged
                    )]))
                );
                assert!(paths(&pasteboard).is_err());
                pasteboard.clearContents();
            });
        }

        #[test]
        fn native_class_resolution_rejects_unknown_proxy_and_overrides() {
            let arbitrary =
                ClassBuilder::new(c"PilotLocalDropUnknownSubclass", Destination::class())
                    .unwrap()
                    .register();
            assert!(hook_class(arbitrary, b"PilotLocalDropTestDestination").is_err());
            let wrong_parent = ClassBuilder::new(
                c"..NSKVONotifying_PilotLocalDropWrongParent",
                NSObject::class(),
            )
            .unwrap()
            .register();
            assert!(hook_class(wrong_parent, b"PilotLocalDropWrongParent").is_err());
            let base = ClassBuilder::new(c"PilotLocalDropOverrideBase", Destination::class())
                .unwrap()
                .register();
            let mut overridden =
                ClassBuilder::new(c"..NSKVONotifying_PilotLocalDropOverrideBase", base).unwrap();
            unsafe extern "C-unwind" fn reject(
                _this: &AnyObject,
                _sel: Sel,
                _info: &AnyObject,
            ) -> NSDragOperation {
                NSDragOperation::None
            }
            unsafe {
                overridden.add_method(
                    sel!(draggingEntered:),
                    reject as unsafe extern "C-unwind" fn(_, _, _) -> _,
                );
            }
            let overridden = overridden.register();
            assert!(hook_class(overridden, b"PilotLocalDropOverrideBase").is_err());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    pub(super) fn entry(id: &Identity) -> DropEntry {
        DropEntry {
            name: id.name.clone(),
            size: id.size,
            mtime: (id.modified_ns / 1_000_000) as u64,
            directory: Some(id.directory),
        }
    }
    fn begin(registry: &mut Registry, owner: &str, sequence: isize, paths: Vec<PathBuf>) {
        registry.begin(
            owner,
            Gesture {
                anchor: Anchor {
                    view: 1,
                    sequence,
                    count: 7,
                    generation: 0,
                    pasteboard: "test-only".into(),
                },
                paths: Ok(paths),
                phase: Phase::Hover,
            },
        );
    }
    fn capture(registry: &mut Registry, owner: &str, now: Instant) -> CapturedDrop {
        let (gesture, ticket) = registry.capture_gesture(owner, true, now).unwrap();
        let ids = gesture
            .paths
            .as_ref()
            .unwrap()
            .iter()
            .map(|p| identity(p).unwrap())
            .collect();
        registry.capture(owner, gesture, ticket, ids, now).unwrap()
    }
    fn fixture() -> (tempfile::TempDir, PathBuf, Registry, Instant) {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("file.txt");
        std::fs::write(&path, b"test").unwrap();
        let mut state = Registry::default();
        begin(&mut state, "main", 1, vec![path.clone()]);
        (dir, path, state, Instant::now())
    }
    #[test]
    fn tokens_are_owner_bound_single_use_and_require_successful_drop() {
        let (_dir, _path, mut state, now) = fixture();
        let capture = capture(&mut state, "main", now);
        assert!(state.take("other", &capture.token, now).is_err());
        assert!(state.take("main", &capture.token, now).is_err()); // no native ACK burns token
        let capture = self::capture(&mut state, "main", now);
        state.gestures.get_mut("main").unwrap().phase = Phase::Dropped(now);
        assert!(state.take("main", &capture.token, now).is_ok());
        assert!(state.take("main", &capture.token, now).is_err());
        assert!(state.capture_gesture("main", false, now).is_err());
    }
    #[test]
    fn cancelled_new_gesture_and_expired_tokens_cannot_be_redeemed() {
        let (_dir, path, mut state, now) = fixture();
        let token = capture(&mut state, "main", now);
        state.clear("main");
        assert!(state.take("main", &token.token, now).is_err());
        begin(&mut state, "main", 2, vec![path.clone()]);
        let old = capture(&mut state, "main", now);
        begin(&mut state, "main", 3, vec![path]);
        let new = capture(&mut state, "main", now);
        assert!(state.take("main", &old.token, now).is_err());
        state.gestures.get_mut("main").unwrap().phase = Phase::Dropped(now);
        assert!(state
            .take("main", &new.token, now + TTL + Duration::from_secs(1))
            .is_err());
    }
    #[test]
    fn delayed_capture_only_accepts_just_acknowledged_drop() {
        let (_dir, _path, mut state, now) = fixture();
        assert!(state.capture_gesture("missing", true, now).is_err());
        assert!(state.capture_gesture("main", false, now).is_err());
        state.gestures.get_mut("main").unwrap().phase = Phase::Dropping;
        assert!(state.capture_gesture("main", false, now).is_err());
        state.gestures.get_mut("main").unwrap().phase = Phase::Dropped(now);
        assert!(state
            .capture_gesture("main", false, now + Duration::from_millis(500))
            .is_ok());
        assert!(state
            .capture_gesture("main", false, now + Duration::from_secs(3))
            .is_err());
    }
    #[test]
    fn out_of_order_capture_cannot_replace_new_token() {
        let (_dir, path, mut state, now) = fixture();
        let (old, old_ticket) = state.capture_gesture("main", true, now).unwrap();
        let newest = capture(&mut state, "main", now);
        assert!(state
            .capture("main", old, old_ticket, vec![identity(&path).unwrap()], now)
            .is_err());
        assert_eq!(state.snapshots.get("main").unwrap().token, newest.token);
    }
    #[test]
    fn manifest_matches_multiset_and_returns_browser_order() {
        let (dir, path, mut state, now) = fixture();
        let sub = dir.path().join("sub");
        std::fs::create_dir(&sub).unwrap();
        let same_name = sub.join("file.txt");
        std::fs::write(&same_name, b"longer").unwrap();
        begin(&mut state, "main", 2, vec![path.clone(), same_name.clone()]);
        let token = capture(&mut state, "main", now);
        state.gestures.get_mut("main").unwrap().phase = Phase::Dropped(now);
        let snapshot = state.take("main", &token.token, now).unwrap();
        let result = verify(
            snapshot,
            vec![
                entry(&identity(&same_name).unwrap()),
                entry(&identity(&path).unwrap()),
            ],
            7,
        )
        .unwrap();
        assert_eq!(
            result.paths,
            vec![same_name.to_str().unwrap(), path.to_str().unwrap()]
        );
    }
    #[test]
    fn changed_pasteboard_manifest_and_files_are_rejected() {
        for mutation in 0..5 {
            let (_dir, path, mut state, now) = fixture();
            let token = capture(&mut state, "main", now);
            state.gestures.get_mut("main").unwrap().phase = Phase::Dropped(now);
            let snapshot = state.take("main", &token.token, now).unwrap();
            let mut manifest = entry(&snapshot.identities[0]);
            match mutation {
                0 => manifest.name = "wrong.txt".into(),
                1 => manifest.size += 1,
                2 => manifest.mtime += 5000,
                3 => std::fs::write(path, b"changed").unwrap(),
                _ => {}
            }
            assert!(verify(snapshot, vec![manifest], if mutation == 4 { 8 } else { 7 }).is_err());
        }
    }
    #[test]
    fn directories_require_known_kind_and_symlinks_are_rejected() {
        let dir = tempfile::tempdir().unwrap();
        let id = identity(dir.path()).unwrap();
        let mut manifest = entry(&id);
        manifest.size = 0;
        manifest.mtime = 0;
        assert!(id.matches(&manifest));
        manifest.directory = None;
        assert!(!id.matches(&manifest));
        manifest.directory = Some(false);
        assert!(!id.matches(&manifest));
        #[cfg(unix)]
        {
            let link = dir.path().join("link");
            std::os::unix::fs::symlink(dir.path(), &link).unwrap();
            assert!(identity(&link).is_err());
        }
    }
}
