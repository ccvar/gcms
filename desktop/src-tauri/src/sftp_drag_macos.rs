//! Native file promises let Finder choose the destination before SFTP starts.
//! AppKit calls the promise writer on our background operation queue. Only the
//! final, exclusive rename runs inside file coordination; network work never
//! holds a file coordinator or blocks the main thread.
use crate::sftp_transfer::PromiseRequest;
use block2::{DynBlock, RcBlock, StackBlock};
use objc2::{
    define_class, msg_send,
    rc::Retained,
    runtime::{AnyObject, Bool, NSObject, NSObjectProtocol, ProtocolObject},
    AnyThread, DefinedClass, MainThreadMarker, MainThreadOnly,
};
use objc2_app_kit::{
    NSApp, NSAttributedStringNSStringDrawing, NSBezierPath, NSColor, NSDragOperation,
    NSDraggingContext, NSDraggingImageComponent, NSDraggingImageComponentIconKey,
    NSDraggingImageComponentLabelKey, NSDraggingItem, NSDraggingSession, NSDraggingSource, NSEvent,
    NSEventModifierFlags, NSEventType, NSFilePromiseProvider, NSFilePromiseProviderDelegate,
    NSFont, NSFontAttributeName, NSForegroundColorAttributeName, NSImage, NSLineBreakMode,
    NSMutableParagraphStyle, NSParagraphStyleAttributeName, NSWindow, NSWorkspace,
};
use objc2_foundation::{
    NSArray, NSAttributedString, NSDictionary, NSError, NSFileCoordinator,
    NSFileCoordinatorWritingOptions, NSFileTypeForHFSTypeCode, NSLocalizedDescriptionKey,
    NSOperationQueue, NSPoint, NSRect, NSSize, NSString, NSURL,
};
use std::{
    cell::RefCell,
    collections::HashMap,
    ffi::CStr,
    os::unix::ffi::OsStrExt,
    path::PathBuf,
    ptr::NonNull,
    sync::{Arc, Mutex},
};

// Both NSDraggingSession's source and NSFilePromiseProvider's delegate are weak.
// Keep them alive until the drag AND its promised write have completed.
thread_local! {
    static DRAGS: RefCell<HashMap<String, Retained<PilotPromiseSource>>> = RefCell::new(HashMap::new());
}

#[derive(Default)]
struct Lifecycle {
    drag_ended: bool,
    writer_started: bool,
    writer_ended: bool,
    dropped_at: Option<tokio::time::Instant>,
}

struct NativeState {
    request: Arc<PromiseRequest>,
    lifecycle: Mutex<Lifecycle>,
    changed: tokio::sync::Notify,
}

impl NativeState {
    fn release_if_finished(&self) {
        let release = self
            .lifecycle
            .lock()
            .map(|s| s.drag_ended && s.writer_ended)
            .unwrap_or(false);
        if release {
            let id = self.request.id.clone();
            let _ = self.request.app.run_on_main_thread(move || {
                DRAGS.with(|drags| {
                    drags.borrow_mut().remove(&id);
                });
            });
        }
    }

    fn watch_pending(self: &Arc<Self>) {
        let Some(mut cancel) = self.request.cancellation() else {
            return;
        };
        let state = self.clone();
        tauri::async_runtime::spawn(async move {
            loop {
                // Register the notification before observing state to avoid a
                // lost wakeup between the writer callback and this wait.
                let changed = state.changed.notified();
                let deadline = {
                    let Ok(lifecycle) = state.lifecycle.lock() else {
                        return;
                    };
                    if lifecycle.writer_started || state.request.is_finished() {
                        return;
                    }
                    lifecycle
                        .dropped_at
                        .map(|when| when + std::time::Duration::from_secs(60))
                };
                let cancelled = *cancel.borrow();
                let reason = if cancelled {
                    Some("下载已取消，未保存文件")
                } else {
                    tokio::select! {
                        _ = cancel.changed() => Some("下载已取消，未保存文件"),
                        _ = changed => None,
                        _ = async {
                            match deadline {
                                Some(deadline) => tokio::time::sleep_until(deadline).await,
                                None => std::future::pending::<()>().await,
                            }
                        } => Some("目标应用未接收文件，请拖到 Finder 文件夹或使用下载按钮"),
                    }
                };
                if let Some(reason) = reason {
                    // Recheck under the same lock used by writer startup. Once
                    // writing begins its watch receiver owns cancellation.
                    let Ok(mut lifecycle) = state.lifecycle.lock() else {
                        return;
                    };
                    if lifecycle.writer_started || state.request.is_finished() {
                        return;
                    }
                    state.request.cancel();
                    state.request.finish(Err(reason.into()));
                    lifecycle.writer_ended = true;
                    drop(lifecycle);
                    state.release_if_finished();
                    return;
                }
            }
        });
    }
}

struct PromiseIvars {
    name: String,
    queue: Retained<NSOperationQueue>,
    write: Box<dyn Fn(&NSURL) -> Result<PathBuf, String> + Send + Sync>,
    started: Box<dyn Fn() + Send + Sync>,
    finished: Box<dyn Fn(Result<PathBuf, String>) + Send + Sync>,
}

define_class!(
    #[unsafe(super(NSObject))]
    #[name = "PilotSftpFilePromiseDelegate"]
    #[ivars = PromiseIvars]
    struct PilotFilePromiseDelegate;

    unsafe impl NSObjectProtocol for PilotFilePromiseDelegate {}

    unsafe impl NSFilePromiseProviderDelegate for PilotFilePromiseDelegate {
        #[unsafe(method_id(filePromiseProvider:fileNameForType:))]
        fn file_name(
            &self,
            _provider: &NSFilePromiseProvider,
            _file_type: &NSString,
        ) -> Retained<NSString> {
            NSString::from_str(&self.ivars().name)
        }

        #[unsafe(method_id(operationQueueForFilePromiseProvider:))]
        fn operation_queue(&self, _provider: &NSFilePromiseProvider) -> Retained<NSOperationQueue> {
            self.ivars().queue.clone()
        }

        #[unsafe(method(filePromiseProvider:writePromiseToURL:completionHandler:))]
        fn write_promise(
            &self,
            _provider: &NSFilePromiseProvider,
            url: &NSURL,
            completion: &DynBlock<dyn Fn(*mut NSError)>,
        ) {
            // Keep this delegate alive if the main thread releases the drag while
            // our background callback is returning to AppKit.
            let _retained = unsafe { Retained::retain(self as *const Self as *mut Self) };
            let ivars = self.ivars();
            (ivars.started)();
            let result =
                std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| (ivars.write)(url)))
                    .unwrap_or_else(|_| Err("拖出下载未完成，请重试或使用下载按钮".into()));
            // Each native invocation gets exactly one completion, including
            // cancellation, coordination failure and an unexpected Rust panic.
            let error = result.as_ref().err().map(|message| native_error(message));
            completion.call((error.as_ref().map_or(std::ptr::null_mut(), |e| {
                Retained::as_ptr(e) as *mut NSError
            }),));
            (ivars.finished)(result);
        }
    }
);

struct SourceIvars {
    state: Arc<NativeState>,
    _delegate: Retained<PilotFilePromiseDelegate>,
    _provider: Retained<NSFilePromiseProvider>,
}

define_class!(
    #[unsafe(super(NSObject))]
    #[thread_kind = MainThreadOnly]
    #[name = "PilotSftpPromiseDraggingSource"]
    #[ivars = SourceIvars]
    struct PilotPromiseSource;

    unsafe impl NSObjectProtocol for PilotPromiseSource {}

    unsafe impl NSDraggingSource for PilotPromiseSource {
        #[unsafe(method(draggingSession:sourceOperationMaskForDraggingContext:))]
        fn operation(
            &self,
            _session: &NSDraggingSession,
            _context: NSDraggingContext,
        ) -> NSDragOperation {
            NSDragOperation::Copy
        }

        #[unsafe(method(ignoreModifierKeysForDraggingSession:))]
        fn ignore_modifiers(&self, _session: &NSDraggingSession) -> bool {
            // The remote item stays on the server, regardless of Option/Command.
            true
        }

        #[unsafe(method(draggingSession:endedAtPoint:operation:))]
        fn ended(&self, _session: &NSDraggingSession, _point: NSPoint, operation: NSDragOperation) {
            let _retained = unsafe { Retained::retain(self as *const Self as *mut Self) };
            let state = &self.ivars().state;
            if operation == NSDragOperation::None {
                state.request.cancel();
                state.request.finish(Err("下载已取消，未保存文件".into()));
                if let Ok(mut lifecycle) = state.lifecycle.lock() {
                    lifecycle.drag_ended = true;
                    if !lifecycle.writer_started {
                        lifecycle.writer_ended = true;
                    }
                }
            } else {
                state.request.emit("dropped");
                if let Ok(mut lifecycle) = state.lifecycle.lock() {
                    lifecycle.drag_ended = true;
                    lifecycle.dropped_at = Some(tokio::time::Instant::now());
                }
            }
            state.changed.notify_one();
            state.release_if_finished();
        }
    }
);

fn file_path(url: &NSURL) -> Result<PathBuf, String> {
    if !url.isFileURL() {
        return Err("拖出目标必须是本地文件夹".into());
    }
    // SAFETY: NSURL guarantees a null-terminated filesystem representation
    // valid for its lifetime. Copy it before the borrowed URL is released.
    let bytes = unsafe { CStr::from_ptr(url.fileSystemRepresentation().as_ptr()) }.to_bytes();
    Ok(PathBuf::from(std::ffi::OsStr::from_bytes(bytes)))
}

fn fulfill(request: &PromiseRequest, url: &NSURL) -> Result<PathBuf, String> {
    let destination = file_path(url)?;
    let prepared = tauri::async_runtime::block_on(request.prepare(&destination))?;
    coordinate_publish(url, |destination| prepared.publish(destination))
}

fn coordinate_publish(
    url: &NSURL,
    publish: impl Fn(&std::path::Path) -> Result<PathBuf, String>,
) -> Result<PathBuf, String> {
    let coordinator = NSFileCoordinator::initWithFilePresenter(NSFileCoordinator::alloc(), None);
    let result = RefCell::new(None);
    let writer = StackBlock::new(|coordinated: NonNull<NSURL>| {
        // The coordinator owns this URL for the synchronous accessor call.
        let path = file_path(unsafe { coordinated.as_ref() });
        *result.borrow_mut() = Some(path.and_then(|path| publish(&path)));
    });
    let mut coordination_error = None;
    coordinator.coordinateWritingItemAtURL_options_error_byAccessor(
        url,
        NSFileCoordinatorWritingOptions::empty(),
        Some(&mut coordination_error),
        &writer,
    );
    if let Some(error) = coordination_error {
        return Err(error.localizedDescription().to_string());
    }
    result
        .into_inner()
        .unwrap_or_else(|| Err("系统未允许写入拖出目标".into()))
}

fn native_error(message: &str) -> Retained<NSError> {
    let description = NSString::from_str(message);
    // SAFETY: Foundation's description key expects an NSString value.
    unsafe {
        let info = NSDictionary::from_slices(&[NSLocalizedDescriptionKey], &[description.as_ref()]);
        NSError::errorWithDomain_code_userInfo(
            &NSString::from_str("com.pilot.sftp-download"),
            1,
            Some(&info),
        )
    }
}

/// Use the system's file association icon without creating or fetching a file.
/// This compatibility API accepts filename extensions and the standard folder
/// HFS type; it avoids needing a remote file or a temporary local placeholder.
#[allow(deprecated)]
fn file_icon(name: &str, directory: bool) -> Retained<NSImage> {
    let file_type = if directory {
        NSFileTypeForHFSTypeCode(u32::from_be_bytes(*b"fldr"))
            .unwrap_or_else(|| NSString::from_str("public.folder"))
    } else {
        NSString::from_str(
            std::path::Path::new(name)
                .extension()
                .and_then(|extension| extension.to_str())
                .unwrap_or(""),
        )
    };
    NSWorkspace::sharedWorkspace().iconForFileType(&file_type)
}

fn display_name(name: &str) -> String {
    name.chars()
        .map(|character| {
            if character.is_control() {
                ' '
            } else {
                character
            }
        })
        .collect()
}

fn label_image(name: &str) -> Retained<NSImage> {
    let font = NSFont::systemFontOfSize(12.0);
    let foreground = NSColor::labelColor();
    let paragraph = NSMutableParagraphStyle::new();
    paragraph.setLineBreakMode(NSLineBreakMode::ByTruncatingMiddle);
    let text = unsafe {
        let attributes = NSDictionary::<NSString, AnyObject>::from_slices(
            &[
                NSFontAttributeName,
                NSForegroundColorAttributeName,
                NSParagraphStyleAttributeName,
            ],
            &[font.as_ref(), foreground.as_ref(), paragraph.as_ref()],
        );
        NSAttributedString::initWithString_attributes(
            NSAttributedString::alloc(),
            &NSString::from_str(&display_name(name)),
            Some(&attributes),
        )
    };
    let measured = text.size();
    let size = NSSize::new(measured.width.ceil().clamp(20.0, 240.0) + 12.0, 22.0);
    let background = NSColor::controlBackgroundColor().colorWithAlphaComponent(0.94);
    let drawing = RcBlock::new(move |bounds: NSRect| {
        background.setFill();
        NSBezierPath::bezierPathWithRoundedRect_xRadius_yRadius(bounds, 5.0, 5.0).fill();
        text.drawInRect(NSRect::new(
            NSPoint::new(6.0, (bounds.size.height - measured.height.ceil()) / 2.0),
            NSSize::new(bounds.size.width - 12.0, measured.height.ceil()),
        ));
        Bool::YES
    });
    NSImage::imageWithSize_flipped_drawingHandler(size, false, &drawing)
}

fn set_file_drag_image(item: &NSDraggingItem, name: &str, directory: bool, pointer: NSPoint) {
    let icon = file_icon(name, directory);
    let label = label_image(name);
    let label_size = label.size();
    // Components use the dragging item's own coordinates. Keeping the pointer
    // on the icon prevents a long filename from shifting the drag away from it.
    let icon_component = NSDraggingImageComponent::draggingImageComponentWithKey(unsafe {
        NSDraggingImageComponentIconKey
    });
    let label_component = NSDraggingImageComponent::draggingImageComponentWithKey(unsafe {
        NSDraggingImageComponentLabelKey
    });
    icon_component.setFrame(NSRect::new(NSPoint::new(0.0, 0.0), NSSize::new(24.0, 24.0)));
    label_component.setFrame(NSRect::new(NSPoint::new(28.0, 1.0), label_size));
    unsafe {
        icon_component.setContents(Some(&icon));
        label_component.setContents(Some(&label));
    }
    let components = NSArray::from_retained_slice(&[icon_component, label_component]);
    let provider = RcBlock::new(move || NonNull::from(components.as_ref()));
    item.setDraggingFrame(NSRect::new(
        NSPoint::new(pointer.x - 10.0, pointer.y - 12.0),
        NSSize::new(28.0 + label_size.width, 24.0),
    ));
    // AppKit copies this block, which retains the array and both image contents
    // for the full native drag session, including cancellation animation.
    unsafe {
        item.setImageComponentsProvider(Some(&provider));
    }
}

pub(crate) fn start(window: &tauri::Window, request: Arc<PromiseRequest>) -> Result<(), String> {
    let mtm = MainThreadMarker::new().ok_or("拖出操作必须在主线程启动")?;
    if NSEvent::pressedMouseButtons() & 1 == 0 {
        return Err("鼠标已松开，请按住该项拖到本机文件夹".into());
    }
    let pointer = window.ns_window().map_err(|e| e.to_string())?;
    let native = unsafe { (pointer as *const NSWindow).as_ref() }.ok_or("本机窗口不可用")?;
    let view = native.contentView().ok_or("本机窗口视图不可用")?;
    let position = native.mouseLocationOutsideOfEventStream();
    let image_position = view.convertPoint_fromView(position, None);
    let queue = NSOperationQueue::new();
    queue.setMaxConcurrentOperationCount(1);
    let state = Arc::new(NativeState {
        request,
        lifecycle: Mutex::new(Lifecycle::default()),
        changed: tokio::sync::Notify::new(),
    });
    let write_state = state.clone();
    let started_state = state.clone();
    let finished_state = state.clone();
    let delegate = new_delegate(PromiseIvars {
        name: state.request.name.clone(),
        queue,
        write: Box::new(move |url| fulfill(&write_state.request, url)),
        started: Box::new(move || {
            if let Ok(mut lifecycle) = started_state.lifecycle.lock() {
                lifecycle.writer_started = true;
            }
            started_state.changed.notify_one();
        }),
        finished: Box::new(move |result| {
            finished_state.request.finish(result);
            if let Ok(mut lifecycle) = finished_state.lifecycle.lock() {
                lifecycle.writer_ended = true;
            }
            finished_state.release_if_finished();
        }),
    });
    let provider = NSFilePromiseProvider::initWithFileType_delegate(
        NSFilePromiseProvider::alloc(),
        &NSString::from_str(if state.request.directory {
            "public.folder"
        } else {
            "public.data"
        }),
        ProtocolObject::from_ref(&*delegate),
    );
    let item = NSDraggingItem::initWithPasteboardWriter(
        NSDraggingItem::alloc(),
        ProtocolObject::from_ref(&*provider),
    );
    set_file_drag_image(
        &item,
        &state.request.name,
        state.request.directory,
        image_position,
    );
    let source: Retained<PilotPromiseSource> = unsafe {
        msg_send![
            super(PilotPromiseSource::alloc(mtm).set_ivars(SourceIvars {
                state: state.clone(),
                _delegate: delegate,
                _provider: provider,
            })),
            init
        ]
    };
    // Looking up a system icon can take a moment on the first drag. Do not
    // create a new native drag if the user has already released the mouse.
    if NSEvent::pressedMouseButtons() & 1 == 0 {
        return Err("鼠标已松开，请按住该项拖到本机文件夹".into());
    }
    let position = native.mouseLocationOutsideOfEventStream();
    let pointer = view.convertPoint_fromView(position, None);
    let mut frame = item.draggingFrame();
    frame.origin = NSPoint::new(pointer.x - 10.0, pointer.y - 12.0);
    item.setDraggingFrame(frame);
    let current_event = NSApp(mtm).currentEvent();
    let event = {
        NSEvent::mouseEventWithType_location_modifierFlags_timestamp_windowNumber_context_eventNumber_clickCount_pressure(
            NSEventType::LeftMouseDragged, position, NSEventModifierFlags::empty(),
            current_event.map_or(0.0, |event| event.timestamp()), native.windowNumber(), None, 0, 1, 1.0,
        )
    }.ok_or("无法开始系统拖出")?;
    DRAGS.with(|drags| {
        drags
            .borrow_mut()
            .insert(state.request.id.clone(), source.clone());
    });
    view.beginDraggingSessionWithItems_event_source(
        &NSArray::from_retained_slice(&[item]),
        &event,
        ProtocolObject::from_ref(&*source),
    );
    state.request.emit("dragging");
    state.watch_pending();
    Ok(())
}

fn new_delegate(ivars: PromiseIvars) -> Retained<PilotFilePromiseDelegate> {
    unsafe {
        msg_send![
            super(PilotFilePromiseDelegate::alloc().set_ivars(ivars)),
            init
        ]
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use objc2::rc::autoreleasepool;
    use std::sync::atomic::{AtomicUsize, Ordering};

    #[test]
    fn native_drag_image_retains_icon_and_filename_after_setup() {
        autoreleasepool(|_| {
            for (index, name, directory) in [
                (0, "服务器配置", true),
                (1, "deployment.json", false),
                (
                    2,
                    "非常长的远程配置文件名称-with-more-details-and-a-long-name.json",
                    false,
                ),
            ] {
                let url = NSURL::fileURLWithPath_isDirectory(
                    &NSString::from_str("/unused-preview"),
                    directory,
                );
                let item = NSDraggingItem::initWithPasteboardWriter(
                    NSDraggingItem::alloc(),
                    ProtocolObject::from_ref(&*url),
                );
                set_file_drag_image(&item, name, directory, NSPoint::new(100.0, 80.0));
                let components = item
                    .imageComponents()
                    .expect("drag images must survive the setup scope");
                assert_eq!(components.len(), 2);
                assert_eq!(
                    components.objectAtIndex(0).key().to_string(),
                    unsafe { NSDraggingImageComponentIconKey }.to_string()
                );
                assert_eq!(
                    components.objectAtIndex(1).key().to_string(),
                    unsafe { NSDraggingImageComponentLabelKey }.to_string()
                );
                let frame = item.draggingFrame();
                assert_eq!(frame.origin, NSPoint::new(90.0, 68.0));
                assert!(frame.size.width <= 280.0);
                assert_eq!(frame.size.height, 24.0);
                for component_index in 0..2 {
                    let component = components.objectAtIndex(component_index);
                    let contents = component.contents().unwrap();
                    let image = contents.downcast_ref::<NSImage>().unwrap();
                    let rendered = image
                        .TIFFRepresentation()
                        .expect("native image must render");
                    assert!(rendered.length() > 0);
                    // Optional local-only visual QA; never used by the app.
                    if let Some(directory) = std::env::var_os("PILOT_DRAG_PREVIEW_DIR") {
                        let path = PathBuf::from(directory)
                            .join(format!("preview-{index}-{component_index}.tiff"));
                        std::fs::write(path, rendered.to_vec()).unwrap();
                    }
                }
            }
            assert_eq!(display_name("file\nname\t.json"), "file name .json");
        });
    }

    // Dispatch through the real provider's Objective-C delegate protocol, with
    // a local mock payload. A complete Finder drop still needs an interactive
    // drag session: a plain pasteboard cannot fulfill NSFilePromiseProvider.
    fn native_callback(directory: bool, fail: bool) {
        autoreleasepool(|_| {
            let root = tempfile::tempdir().unwrap();
            let staged = root.path().join("staged");
            if directory {
                std::fs::create_dir_all(staged.join("empty")).unwrap();
                std::fs::write(staged.join(".hidden"), "payload").unwrap();
            } else {
                std::fs::write(&staged, "payload").unwrap();
            }
            let name = if directory {
                "测试目录"
            } else {
                "测试文件.txt"
            };
            let destination = root.path().join(name);
            let write_count = Arc::new(AtomicUsize::new(0));
            let finish_count = Arc::new(AtomicUsize::new(0));
            let writes = write_count.clone();
            let finishes = finish_count.clone();
            let delegate = new_delegate(PromiseIvars {
                name: name.into(),
                queue: NSOperationQueue::new(),
                write: Box::new(move |url| {
                    writes.fetch_add(1, Ordering::SeqCst);
                    assert!(
                        MainThreadMarker::new().is_none(),
                        "native writer blocked the main thread"
                    );
                    if fail {
                        return Err("下载已取消，临时文件已清理".into());
                    }
                    coordinate_publish(url, |target| {
                        crate::sftp_transfer::publish_promise(&staged, target)
                    })
                }),
                started: Box::new(|| {}),
                finished: Box::new(move |_result| {
                    finishes.fetch_add(1, Ordering::SeqCst);
                }),
            });
            let provider = NSFilePromiseProvider::initWithFileType_delegate(
                NSFilePromiseProvider::alloc(),
                &NSString::from_str(if directory {
                    "public.folder"
                } else {
                    "public.data"
                }),
                ProtocolObject::from_ref(&*delegate),
            );
            let callbacks = Arc::new(AtomicUsize::new(0));
            let results = Arc::new(Mutex::new(Vec::new()));
            let callback_count = callbacks.clone();
            let callback_results = results.clone();
            let completion = StackBlock::new(move |error: *mut NSError| {
                callback_count.fetch_add(1, Ordering::SeqCst);
                let result = if error.is_null() {
                    Ok(())
                } else {
                    Err(unsafe { &*error }.localizedDescription().to_string())
                };
                callback_results.lock().unwrap().push(result);
            });
            let destination_url = NSURL::fileURLWithPath_isDirectory(
                &NSString::from_str(destination.to_str().unwrap()),
                directory,
            );
            provider
                .delegate()
                .unwrap()
                .filePromiseProvider_writePromiseToURL_completionHandler(
                    &provider,
                    &destination_url,
                    &completion,
                );
            assert_eq!(
                write_count.load(Ordering::SeqCst),
                1,
                "native delegate did not invoke writer exactly once"
            );
            assert_eq!(finish_count.load(Ordering::SeqCst), 1);
            assert_eq!(
                callbacks.load(Ordering::SeqCst),
                1,
                "native completion count"
            );
            let result = results.lock().unwrap().pop().unwrap();
            assert_eq!(result.is_err(), fail, "{result:?}");
            if !fail {
                let path = destination;
                assert_eq!(path.file_name().unwrap(), name);
                if directory {
                    assert!(path.join("empty").is_dir());
                    assert_eq!(
                        std::fs::read_to_string(path.join(".hidden")).unwrap(),
                        "payload"
                    );
                } else {
                    assert_eq!(std::fs::read_to_string(path).unwrap(), "payload");
                }
            } else {
                assert!(!destination.exists());
            }
        });
    }

    #[test]
    fn native_file_promise_callback_completes_once() {
        native_callback(false, false);
    }

    #[test]
    fn native_folder_promise_callback_preserves_hidden_and_empty_entries() {
        native_callback(true, false);
    }

    #[test]
    fn native_cancelled_promise_callback_reports_error_once() {
        native_callback(false, true);
    }
}
