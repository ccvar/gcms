export type DialogResizeEdge =
  | 'n'
  | 'ne'
  | 'e'
  | 'se'
  | 's'
  | 'sw'
  | 'w'
  | 'nw';

export type DialogGeometryOptions = {
  /** 关闭整个行为层；适合根据弹窗状态动态切换。 */
  enabled?: boolean;
  /** 标题栏拖动。 */
  draggable?: boolean;
  /** 四边与四角缩放。 */
  resizable?: boolean;
  /** 标题栏元素或其选择器。默认寻找 data 标记，其次寻找 .sheet-head。 */
  handle?: string | HTMLElement;
  minWidth?: number;
  minHeight?: number;
  maxWidth?: number;
  maxHeight?: number;
  /** 弹窗与可视区域边缘之间至少保留的距离。 */
  viewportPadding?: number;
  /** 顶部单独预留的安全距离；macOS Overlay 标题栏默认会自动留出更大空间。 */
  viewportTopPadding?: number;
  /** 此宽度及以下恢复原有响应式布局，并禁用移动和缩放。 */
  smallScreenMax?: number;
  /** 粗指针设备使用原有响应式布局。 */
  disableOnCoarsePointer?: boolean;
  /** 允许创建的缩放边。 */
  resizeEdges?: DialogResizeEdge[];
  /** 双击标题栏恢复默认尺寸并居中。 */
  resetOnDoubleClick?: boolean;
  /**
   * 位置只在当前会话记忆。需要同时提供 id 或 storageKey。
   * 不写入长期存储，避免切换屏幕后弹窗长期停留在旧坐标。
   */
  rememberPosition?: boolean;
  /**
   * 尺寸写入 localStorage。需要同时提供 id 或 storageKey；
   * storageVersion 变化后旧尺寸会被自动忽略。
   */
  rememberSize?: boolean;
  id?: string;
  storageKey?: string;
  storageVersion?: number;
};

type Geometry = {
  left: number;
  top: number;
  width: number;
  height: number;
};

type DragOperation = {
  kind: 'drag';
  pointerId: number;
  startX: number;
  startY: number;
  start: Geometry;
  activated: boolean;
};

type ResizeOperation = {
  kind: 'resize';
  pointerId: number;
  startX: number;
  startY: number;
  start: Geometry;
  edge: DialogResizeEdge;
};

type Operation = DragOperation | ResizeOperation;

type GeometryCssProperty =
  | 'inset'
  | 'left'
  | 'top'
  | 'right'
  | 'bottom'
  | 'margin'
  | 'width'
  | 'height'
  | 'max-width'
  | 'max-height';

type InlineProperty = {
  value: string;
  priority: string;
};

type InlineGeometry = Record<GeometryCssProperty, InlineProperty>;

const DEFAULT_HANDLE = '[data-dialog-drag-handle], .sheet-head';
const DEFAULT_EDGES: DialogResizeEdge[] = ['n', 'ne', 'e', 'se', 's', 'sw', 'w', 'nw'];
const GEOMETRY_PROPERTIES: GeometryCssProperty[] = [
  'inset',
  'left',
  'top',
  'right',
  'bottom',
  'margin',
  'width',
  'height',
  'max-width',
  'max-height',
];
const INTERACTIVE_SELECTOR = [
  'button',
  'a',
  'input',
  'textarea',
  'select',
  'option',
  'label',
  'summary',
  '[contenteditable="true"]',
  '[role="button"]',
  '[data-dialog-no-drag]',
].join(',');

const EDGE_STYLE: Record<DialogResizeEdge, Partial<CSSStyleDeclaration>> = {
  n: { top: '0', left: '12px', right: '12px', height: '7px', cursor: 'ns-resize' },
  ne: { top: '0', right: '0', width: '14px', height: '14px', cursor: 'nesw-resize' },
  e: { top: '12px', right: '0', bottom: '12px', width: '7px', cursor: 'ew-resize' },
  se: { right: '0', bottom: '0', width: '14px', height: '14px', cursor: 'nwse-resize' },
  s: { right: '12px', bottom: '0', left: '12px', height: '7px', cursor: 'ns-resize' },
  sw: { bottom: '0', left: '0', width: '14px', height: '14px', cursor: 'nesw-resize' },
  w: { top: '12px', bottom: '12px', left: '0', width: '7px', cursor: 'ew-resize' },
  nw: { top: '0', left: '0', width: '14px', height: '14px', cursor: 'nwse-resize' },
};

function finite(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback;
}

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(Math.max(value, min), max);
}

function readJson<T>(storage: Storage, key: string): T | null {
  try {
    const raw = storage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function writeJson(storage: Storage, key: string, value: unknown) {
  try {
    storage.setItem(key, JSON.stringify(value));
  } catch {
    // 隐私模式、存储已满或被宿主禁用时，弹窗仍应正常工作。
  }
}

function removeStored(storage: Storage, key: string) {
  try {
    storage.removeItem(key);
  } catch {
    // 与 writeJson 一样，存储失败不影响基础交互。
  }
}

function copyInlineGeometry(style: CSSStyleDeclaration): InlineGeometry {
  return Object.fromEntries(
    GEOMETRY_PROPERTIES.map((property) => [
      property,
      {
        value: style.getPropertyValue(property),
        priority: style.getPropertyPriority(property),
      },
    ]),
  ) as InlineGeometry;
}

function restoreInlineGeometry(style: CSSStyleDeclaration, original: InlineGeometry) {
  for (const property of GEOMETRY_PROPERTIES) {
    const entry = original[property];
    if (entry.value) style.setProperty(property, entry.value, entry.priority);
    else style.removeProperty(property);
  }
}

/**
 * 让固定定位的 DOM 弹窗具有桌面窗口式的移动与缩放能力。
 *
 * ```svelte
 * <div
 *   class="modal"
 *   use:dialogGeometry={{
 *     id: 'file-editor',
 *     minWidth: 520,
 *     minHeight: 320,
 *     rememberPosition: true,
 *     rememberSize: true,
 *   }}
 * >
 *   <header class="sheet-head">...</header>
 * </div>
 * ```
 *
 * 注意：本 action 始终使用 left/top/width/height，绝不使用 transform，
 * 以免为弹窗内 position:fixed 的下拉菜单创建错误的 containing block。
 */
export function dialogGeometry(node: HTMLElement, initial: DialogGeometryOptions = {}) {
  const originalGeometry = copyInlineGeometry(node.style);
  let options = { ...initial };
  let dragHandle: HTMLElement | null = null;
  let dragHandleCursor = '';
  let dragHandleTouchAction = '';
  let operation: Operation | null = null;
  let pendingGeometry: Geometry | null = null;
  let frame = 0;
  let geometryActive = false;
  let explicitSize = false;
  let defaultSize: Pick<Geometry, 'width' | 'height'> | null = null;
  let destroyed = false;
  let resizeHandles: HTMLElement[] = [];
  let bodyUserSelect = '';
  let restoreFrame = 0;

  const coarsePointerMedia = window.matchMedia('(pointer: coarse)');
  const finePointerMedia = window.matchMedia('(any-pointer: fine)');
  let responsiveDisabled = false;

  const version = () => Math.max(1, Math.floor(finite(options.storageVersion, 1)));
  const memoryId = () => (options.storageKey || options.id || '').trim();
  const positionKey = () => `pilot:dialog-position:${memoryId()}`;
  const sizeKey = () => `pilot:dialog-size:${memoryId()}`;

  const isDisabled = () => {
    if (options.enabled === false) return true;
    const small = window.innerWidth <= finite(options.smallScreenMax, 760);
    const coarse = options.disableOnCoarsePointer !== false
      && coarsePointerMedia.matches
      && !finePointerMedia.matches;
    return small || coarse;
  };

  const rememberDefaultSize = (rect = node.getBoundingClientRect()) => {
    if (defaultSize || explicitSize || rect.width <= 0 || rect.height <= 0) return;
    defaultSize = { width: rect.width, height: rect.height };
  };

  const limits = () => {
    const padding = Math.max(0, finite(options.viewportPadding, 16));
    const macOverlayTop = /Macintosh|Mac OS X/i.test(navigator.userAgent) ? 38 : padding;
    const topPadding = Math.max(padding, finite(options.viewportTopPadding, macOverlayTop));
    const viewportWidth = window.visualViewport?.width ?? window.innerWidth;
    const viewportHeight = window.visualViewport?.height ?? window.innerHeight;
    const maxWidth = Math.max(
      1,
      Math.min(finite(options.maxWidth, Number.POSITIVE_INFINITY), viewportWidth - padding * 2),
    );
    const maxHeight = Math.max(
      1,
      Math.min(finite(options.maxHeight, Number.POSITIVE_INFINITY), viewportHeight - topPadding - padding),
    );
    const configuredMinWidth = Math.max(1, finite(options.minWidth, 320));
    const configuredMinHeight = Math.max(1, finite(options.minHeight, 180));
    return {
      padding,
      topPadding,
      viewportWidth,
      viewportHeight,
      maxWidth,
      maxHeight,
      // CSS 的默认弹窗有时比通用配置中的 minWidth/minHeight 更紧凑。
      // 默认打开尺寸必须始终能够缩回，否则第一次拖动就会突然跳大。
      minWidth: Math.min(
        Math.min(configuredMinWidth, defaultSize?.width ?? configuredMinWidth),
        maxWidth,
      ),
      minHeight: Math.min(
        Math.min(configuredMinHeight, defaultSize?.height ?? configuredMinHeight),
        maxHeight,
      ),
    };
  };

  const geometryFromRect = (rect: DOMRect): Geometry => ({
    left: rect.left,
    top: rect.top,
    width: rect.width,
    height: rect.height,
  });

  const clampGeometry = (geometry: Geometry): Geometry => {
    const limit = limits();
    const width = clamp(geometry.width, limit.minWidth, limit.maxWidth);
    const height = clamp(geometry.height, limit.minHeight, limit.maxHeight);
    return {
      width,
      height,
      left: clamp(
        geometry.left,
        limit.padding,
        Math.max(limit.padding, limit.viewportWidth - paddingRight(limit) - width),
      ),
      top: clamp(
        geometry.top,
        limit.topPadding,
        Math.max(limit.topPadding, limit.viewportHeight - paddingBottom(limit) - height),
      ),
    };
  };

  // 分开保留函数名，便于以后支持不对称安全区（例如带原生标题栏的窗口）。
  const paddingRight = (limit: ReturnType<typeof limits>) => limit.padding;
  const paddingBottom = (limit: ReturnType<typeof limits>) => limit.padding;

  const restoreInlineProperty = (property: GeometryCssProperty) => {
    const entry = originalGeometry[property];
    if (entry.value) node.style.setProperty(property, entry.value, entry.priority);
    else node.style.removeProperty(property);
  };

  const updateResizeHandleValue = (geometry: Geometry) => {
    const handle = resizeHandles.find((item) => item.dataset.dialogResize === 'se');
    handle?.setAttribute(
      'aria-valuetext',
      `宽 ${Math.round(geometry.width)} 像素，高 ${Math.round(geometry.height)} 像素`,
    );
  };

  const applyGeometry = (geometry: Geometry, includeSize = explicitSize) => {
    const next = clampGeometry(geometry);
    const limit = limits();
    // 部分大型弹窗的预设宽度带 !important；几何行为也使用 important，
    // reset/destroy 时再精确恢复 action 挂载前的值与优先级。
    node.style.setProperty('inset', 'auto', 'important');
    node.style.setProperty('left', `${Math.round(next.left)}px`, 'important');
    node.style.setProperty('top', `${Math.round(next.top)}px`, 'important');
    node.style.setProperty('right', 'auto', 'important');
    node.style.setProperty('bottom', 'auto', 'important');
    node.style.setProperty('margin', '0', 'important');
    if (includeSize) {
      node.style.setProperty('width', `${Math.round(next.width)}px`, 'important');
      node.style.setProperty('height', `${Math.round(next.height)}px`, 'important');
      node.style.setProperty('max-width', `${Math.round(limit.maxWidth)}px`, 'important');
      node.style.setProperty('max-height', `${Math.round(limit.maxHeight)}px`, 'important');
    } else {
      restoreInlineProperty('width');
      restoreInlineProperty('height');
      restoreInlineProperty('max-width');
      restoreInlineProperty('max-height');
    }
    geometryActive = true;
    updateResizeHandleValue(next);
  };

  const scheduleGeometry = (geometry: Geometry) => {
    pendingGeometry = geometry;
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      if (!pendingGeometry || destroyed) return;
      applyGeometry(pendingGeometry);
      pendingGeometry = null;
    });
  };

  const persistGeometry = () => {
    if (!geometryActive || !memoryId()) return;
    const rect = node.getBoundingClientRect();
    const payload = {
      version: version(),
      left: Math.round(rect.left),
      top: Math.round(rect.top),
      width: Math.round(rect.width),
      height: Math.round(rect.height),
    };
    if (options.rememberPosition) {
      writeJson(window.sessionStorage, positionKey(), payload);
    }
    if (options.rememberSize && explicitSize) {
      writeJson(window.localStorage, sizeKey(), payload);
    }
  };

  const clearPersistedGeometry = () => {
    if (!memoryId()) return;
    removeStored(window.sessionStorage, positionKey());
    removeStored(window.localStorage, sizeKey());
  };

  const reset = (clearStored = true) => {
    if (frame) cancelAnimationFrame(frame);
    frame = 0;
    pendingGeometry = null;
    geometryActive = false;
    explicitSize = false;
    restoreInlineGeometry(node.style, originalGeometry);
    if (clearStored) clearPersistedGeometry();
  };

  const storedGeometry = (): { geometry: Partial<Geometry>; hasSize: boolean } | null => {
    if (!memoryId()) return null;
    const expectedVersion = version();
    const position = options.rememberPosition
      ? readJson<Geometry & { version?: number }>(window.sessionStorage, positionKey())
      : null;
    const size = options.rememberSize
      ? readJson<Geometry & { version?: number }>(window.localStorage, sizeKey())
      : null;

    if (position && position.version !== expectedVersion) {
      removeStored(window.sessionStorage, positionKey());
    }
    if (size && size.version !== expectedVersion) {
      removeStored(window.localStorage, sizeKey());
    }

    const validPosition = position?.version === expectedVersion ? position : null;
    const validSize = size?.version === expectedVersion ? size : null;
    if (!validPosition && !validSize) return null;
    return {
      geometry: {
        ...(validPosition ? { left: validPosition.left, top: validPosition.top } : {}),
        ...(validSize ? { width: validSize.width, height: validSize.height } : {}),
      },
      hasSize: Boolean(validSize),
    };
  };

  const restoreStoredGeometry = (): boolean => {
    if (destroyed || operation || isDisabled()) return false;
    const stored = storedGeometry();
    if (!stored) return false;
    const current = geometryFromRect(node.getBoundingClientRect());
    const width = finite(stored.geometry.width, current.width);
    const height = finite(stored.geometry.height, current.height);
    const limit = limits();
    const centeredLeft = (limit.viewportWidth - width) / 2;
    const centeredTop = (limit.viewportHeight - height) / 2;
    explicitSize = stored.hasSize;
    applyGeometry({
      left: finite(stored.geometry.left, centeredLeft),
      top: finite(stored.geometry.top, centeredTop),
      width,
      height,
    }, explicitSize);
    return true;
  };

  const interactiveTarget = (target: EventTarget | null) =>
    target instanceof Element && Boolean(target.closest(INTERACTIVE_SELECTOR));

  const beginOperation = (
    event: PointerEvent,
    kind: Operation['kind'],
    edge?: DialogResizeEdge,
  ) => {
    if (isDisabled() || event.button !== 0 || !event.isPrimary) return;
    if (kind === 'drag' && (options.draggable === false || interactiveTarget(event.target))) return;
    if (kind === 'resize' && options.resizable === false) return;

    const rect = node.getBoundingClientRect();
    rememberDefaultSize(rect);
    const start = clampGeometry(geometryFromRect(rect));
    if (kind === 'resize') {
      explicitSize = true;
      applyGeometry(start, true);
    }
    operation = kind === 'drag'
      ? {
          kind,
          pointerId: event.pointerId,
          startX: event.clientX,
          startY: event.clientY,
          start,
          activated: false,
        }
      : {
          kind,
          pointerId: event.pointerId,
          startX: event.clientX,
          startY: event.clientY,
          start,
          edge: edge as DialogResizeEdge,
        };

    bodyUserSelect = document.body.style.userSelect;
    document.body.style.userSelect = 'none';
    if (kind === 'resize') node.dataset.dialogGeometryActive = kind;
    try {
      node.setPointerCapture(event.pointerId);
    } catch {
      // 某些 WebView 在指针离开标题栏的同一帧可能拒绝 capture；window 监听仍可兜底。
    }
    event.preventDefault();
    event.stopPropagation();
  };

  const resizedGeometry = (
    start: Geometry,
    edge: DialogResizeEdge,
    dx: number,
    dy: number,
  ): Geometry => {
    const limit = limits();
    const startRight = start.left + start.width;
    const startBottom = start.top + start.height;
    let left = start.left;
    let top = start.top;
    let width = start.width;
    let height = start.height;

    if (edge.includes('e')) {
      width = clamp(
        start.width + dx,
        limit.minWidth,
        Math.min(limit.maxWidth, limit.viewportWidth - limit.padding - start.left),
      );
    }
    if (edge.includes('s')) {
      height = clamp(
        start.height + dy,
        limit.minHeight,
        Math.min(limit.maxHeight, limit.viewportHeight - limit.padding - start.top),
      );
    }
    if (edge.includes('w')) {
      left = clamp(
        start.left + dx,
        Math.max(limit.padding, startRight - limit.maxWidth),
        startRight - limit.minWidth,
      );
      width = startRight - left;
    }
    if (edge.includes('n')) {
      top = clamp(
        start.top + dy,
        Math.max(limit.topPadding, startBottom - limit.maxHeight),
        startBottom - limit.minHeight,
      );
      height = startBottom - top;
    }
    return { left, top, width, height };
  };

  const onPointerMove = (event: PointerEvent) => {
    if (!operation || event.pointerId !== operation.pointerId) return;
    const dx = event.clientX - operation.startX;
    const dy = event.clientY - operation.startY;
    if (operation.kind === 'drag') {
      if (!operation.activated) {
        if (Math.hypot(dx, dy) < 3) return;
        operation.activated = true;
        node.dataset.dialogGeometryActive = 'drag';
        dragHandle?.setAttribute('data-dialog-dragging', 'true');
      }
      scheduleGeometry({
        ...operation.start,
        left: operation.start.left + dx,
        top: operation.start.top + dy,
      });
    } else {
      scheduleGeometry(resizedGeometry(operation.start, operation.edge, dx, dy));
    }
  };

  const endOperation = (event?: PointerEvent) => {
    if (!operation || (event && event.pointerId !== operation.pointerId)) return;
    const pointerId = operation.pointerId;
    if (frame && pendingGeometry) {
      cancelAnimationFrame(frame);
      frame = 0;
      applyGeometry(pendingGeometry);
      pendingGeometry = null;
    }
    operation = null;
    try {
      if (node.hasPointerCapture(pointerId)) node.releasePointerCapture(pointerId);
    } catch {
      // 弹窗关闭或 WebView 已自动释放 capture 时无需处理。
    }
    document.body.style.userSelect = bodyUserSelect;
    delete node.dataset.dialogGeometryActive;
    dragHandle?.removeAttribute('data-dialog-dragging');
    if (geometryActive) persistGeometry();
  };

  const onDragPointerDown = (event: PointerEvent) => beginOperation(event, 'drag');
  const onHandleDoubleClick = (event: MouseEvent) => {
    if (options.resetOnDoubleClick === false || interactiveTarget(event.target)) return;
    event.preventDefault();
    reset(true);
  };

  const resolveDragHandle = () => {
    if (options.handle instanceof HTMLElement) return options.handle;
    const selector = typeof options.handle === 'string' && options.handle.trim()
      ? options.handle
      : DEFAULT_HANDLE;
    return node.matches(selector) ? node : node.querySelector<HTMLElement>(selector);
  };

  const unbindDragHandle = () => {
    if (!dragHandle) return;
    dragHandle.removeEventListener('pointerdown', onDragPointerDown);
    dragHandle.removeEventListener('dblclick', onHandleDoubleClick);
    dragHandle.style.cursor = dragHandleCursor;
    dragHandle.style.touchAction = dragHandleTouchAction;
    dragHandle = null;
  };

  const bindDragHandle = () => {
    unbindDragHandle();
    dragHandle = resolveDragHandle();
    if (!dragHandle) return;
    dragHandleCursor = dragHandle.style.cursor;
    dragHandleTouchAction = dragHandle.style.touchAction;
    const canDrag = options.draggable !== false && !isDisabled();
    dragHandle.style.cursor = canDrag ? 'grab' : dragHandleCursor;
    dragHandle.style.touchAction = canDrag ? 'none' : dragHandleTouchAction;
    dragHandle.addEventListener('pointerdown', onDragPointerDown);
    dragHandle.addEventListener('dblclick', onHandleDoubleClick);
  };

  const removeResizeHandles = () => {
    for (const handle of resizeHandles) handle.remove();
    resizeHandles = [];
  };

  const updateCapabilityMarkers = () => {
    node.dataset.dialogGeometry = 'true';
    node.dataset.dialogResizable = String(options.resizable !== false && !isDisabled());
  };

  const onResizeHandleKeyDown = (event: KeyboardEvent) => {
    const edge = (event.currentTarget as HTMLElement).dataset.dialogResize;
    if (edge !== 'se' || options.resizable === false || isDisabled()) return;
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      event.stopPropagation();
      reset(true);
      return;
    }
    const step = event.shiftKey ? 24 : 8;
    let dx = 0;
    let dy = 0;
    if (event.key === 'ArrowRight') dx = step;
    else if (event.key === 'ArrowLeft') dx = -step;
    else if (event.key === 'ArrowDown') dy = step;
    else if (event.key === 'ArrowUp') dy = -step;
    else return;

    event.preventDefault();
    event.stopPropagation();
    const current = geometryFromRect(node.getBoundingClientRect());
    if (event.altKey) {
      applyGeometry({
        ...current,
        left: current.left + dx,
        top: current.top + dy,
      }, explicitSize);
    } else {
      explicitSize = true;
      applyGeometry(resizedGeometry(current, 'se', dx, dy), true);
    }
    persistGeometry();
  };

  const onResizeHandleDoubleClick = (event: MouseEvent) => {
    if (options.resetOnDoubleClick === false) return;
    event.preventDefault();
    event.stopPropagation();
    reset(true);
  };

  const createResizeHandles = () => {
    removeResizeHandles();
    updateCapabilityMarkers();
    if (options.resizable === false || isDisabled()) return;
    const enabledEdges = new Set(options.resizeEdges ?? DEFAULT_EDGES);
    for (const edge of DEFAULT_EDGES) {
      if (!enabledEdges.has(edge)) continue;
      const handle = document.createElement('div');
      handle.dataset.dialogResize = edge;
      Object.assign(handle.style, {
        position: 'absolute',
        zIndex: '4',
        touchAction: 'none',
        userSelect: 'none',
        ...EDGE_STYLE[edge],
      });
      handle.addEventListener('pointerdown', (event) => beginOperation(event, 'resize', edge));
      if (edge === 'se') {
        handle.tabIndex = 0;
        handle.setAttribute('role', 'button');
        handle.setAttribute('aria-label', '调整弹窗大小和位置');
        handle.setAttribute(
          'aria-keyshortcuts',
          'ArrowUp ArrowDown ArrowLeft ArrowRight Alt+ArrowUp Alt+ArrowDown Alt+ArrowLeft Alt+ArrowRight Enter Space',
        );
        handle.title = '拖动调整大小；方向键缩放；Alt 加方向键移动；双击或回车恢复默认';
        handle.addEventListener('keydown', onResizeHandleKeyDown);
        handle.addEventListener('dblclick', onResizeHandleDoubleClick);
        handle.addEventListener('focus', () => {
          handle.style.outline = '2px solid currentColor';
          handle.style.outlineOffset = '-3px';
          handle.style.borderRadius = '4px';
        });
        handle.addEventListener('blur', () => {
          handle.style.outline = '';
          handle.style.outlineOffset = '';
          handle.style.borderRadius = '';
        });
      } else {
        handle.setAttribute('aria-hidden', 'true');
      }
      node.append(handle);
      resizeHandles.push(handle);
    }
    updateResizeHandleValue(geometryFromRect(node.getBoundingClientRect()));
  };

  const clampToViewport = () => {
    if (!geometryActive || isDisabled()) return;
    applyGeometry(geometryFromRect(node.getBoundingClientRect()));
  };

  const onResponsiveChange = () => {
    endOperation();
    responsiveDisabled = isDisabled();
    if (isDisabled()) {
      reset(false);
    } else {
      if (restoreFrame) cancelAnimationFrame(restoreFrame);
      restoreFrame = requestAnimationFrame(() => {
        restoreFrame = 0;
        if (!destroyed && !operation) restoreStoredGeometry();
      });
    }
    bindDragHandle();
    createResizeHandles();
  };

  const onViewportResize = () => {
    const nextDisabled = isDisabled();
    if (nextDisabled !== responsiveDisabled) onResponsiveChange();
    else clampToViewport();
  };

  let contentResizeFrame = 0;
  const contentResizeObserver = new ResizeObserver(() => {
    if (!geometryActive || explicitSize || operation || isDisabled() || destroyed) return;
    if (contentResizeFrame) cancelAnimationFrame(contentResizeFrame);
    contentResizeFrame = requestAnimationFrame(() => {
      contentResizeFrame = 0;
      if (!geometryActive || explicitSize || operation || isDisabled() || destroyed) return;
      applyGeometry(geometryFromRect(node.getBoundingClientRect()), false);
    });
  });
  contentResizeObserver.observe(node);

  const initializeGeometry = () => {
    rememberDefaultSize();
    if (restoreStoredGeometry() || isDisabled() || destroyed) return;
    const rect = node.getBoundingClientRect();
    const next = clampGeometry(geometryFromRect(rect));
    if (
      Math.abs(next.left - rect.left) > 0.5
      || Math.abs(next.top - rect.top) > 0.5
      || rect.right > limits().viewportWidth - limits().padding
      || rect.bottom > limits().viewportHeight - limits().padding
    ) {
      applyGeometry(next, false);
    }
  };

  const onWindowBlur = () => endOperation();

  window.addEventListener('pointermove', onPointerMove);
  window.addEventListener('pointerup', endOperation);
  window.addEventListener('pointercancel', endOperation);
  window.addEventListener('resize', onViewportResize);
  window.addEventListener('blur', onWindowBlur);
  window.visualViewport?.addEventListener('resize', onViewportResize);
  coarsePointerMedia.addEventListener('change', onResponsiveChange);
  finePointerMedia.addEventListener('change', onResponsiveChange);

  responsiveDisabled = isDisabled();
  rememberDefaultSize();
  bindDragHandle();
  createResizeHandles();
  restoreFrame = requestAnimationFrame(() => {
    restoreFrame = 0;
    initializeGeometry();
  });

  return {
    update(next: DialogGeometryOptions = {}) {
      options = { ...next };
      const nextDisabled = isDisabled();
      if (nextDisabled !== responsiveDisabled) onResponsiveChange();
      else {
        bindDragHandle();
        createResizeHandles();
        clampToViewport();
      }
    },
    destroy() {
      destroyed = true;
      endOperation();
      if (frame) cancelAnimationFrame(frame);
      if (contentResizeFrame) cancelAnimationFrame(contentResizeFrame);
      if (restoreFrame) cancelAnimationFrame(restoreFrame);
      unbindDragHandle();
      removeResizeHandles();
      contentResizeObserver.disconnect();
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', endOperation);
      window.removeEventListener('pointercancel', endOperation);
      window.removeEventListener('resize', onViewportResize);
      window.removeEventListener('blur', onWindowBlur);
      window.visualViewport?.removeEventListener('resize', onViewportResize);
      coarsePointerMedia.removeEventListener('change', onResponsiveChange);
      finePointerMedia.removeEventListener('change', onResponsiveChange);
      delete node.dataset.dialogGeometry;
      delete node.dataset.dialogResizable;
      restoreInlineGeometry(node.style, originalGeometry);
    },
  };
}
