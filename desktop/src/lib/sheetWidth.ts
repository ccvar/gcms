import type { Action } from 'svelte/action';

const STORAGE_PREFIX = 'gcms.pilot.sheetWidth.v1.';
const STORAGE_VERSION = 1;

export type SheetWidthOptions = {
  id: string;
  defaultWidth: number;
  minWidth: number;
  maxWidth: number;
  legacyStorageKey?: string;
  label?: string;
  keyboardStep?: number;
  smallScreenWidth?: number;
  viewportGutter?: number;
  disabled?: boolean;
};

type StoredWidth = {
  version: number;
  width: number;
};

let stopActiveResize: (() => void) | null = null;

function versionedStorageKey(id: string): string {
  return `${STORAGE_PREFIX}${encodeURIComponent(id.trim())}`;
}

function finiteWidth(value: unknown): number | null {
  const width = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(width) ? width : null;
}

function readStoredWidth(key: string, versioned: boolean): number | null {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    if (!versioned) return finiteWidth(raw);
    const value = JSON.parse(raw) as Partial<StoredWidth> | number;
    if (typeof value === 'number') return finiteWidth(value);
    return value?.version === STORAGE_VERSION ? finiteWidth(value.width) : null;
  } catch {
    return null;
  }
}

function writeStoredWidth(options: SheetWidthOptions, width: number): void {
  try {
    localStorage.setItem(
      versionedStorageKey(options.id),
      JSON.stringify({ version: STORAGE_VERSION, width } satisfies StoredWidth),
    );
    // GA / GSC 仍有旧代码读取这个纯数字键；迁移期间同步写回，避免两套宽度互相覆盖。
    if (options.legacyStorageKey) localStorage.setItem(options.legacyStorageKey, String(width));
  } catch {
    // localStorage 在隐私模式或容量不足时不可用，调宽本身仍应正常工作。
  }
}

function configuredBounds(options: SheetWidthOptions): { min: number; max: number } {
  const first = Math.max(1, Math.round(options.minWidth));
  const second = Math.max(1, Math.round(options.maxWidth));
  return { min: Math.min(first, second), max: Math.max(first, second) };
}

function clampConfigured(value: number, options: SheetWidthOptions): number {
  const { min, max } = configuredBounds(options);
  return Math.round(Math.min(max, Math.max(min, value)));
}

function loadWidth(options: SheetWidthOptions): number {
  // legacyStorageKey 必须优先：现有 GA/GSC 在接入 action 的过渡期仍可能更新旧键。
  const legacy = options.legacyStorageKey
    ? readStoredWidth(options.legacyStorageKey, false)
    : null;
  const stored = legacy ?? readStoredWidth(versionedStorageKey(options.id), true);
  return clampConfigured(stored ?? options.defaultWidth, options);
}

function clampToViewport(value: number, options: SheetWidthOptions): number {
  const { min, max } = configuredBounds(options);
  const gutter = Math.max(0, Math.round(options.viewportGutter ?? 8));
  const viewportMax = Math.max(1, window.innerWidth - gutter);
  const effectiveMax = Math.min(max, viewportMax);
  const effectiveMin = Math.min(min, effectiveMax);
  return Math.round(Math.min(effectiveMax, Math.max(effectiveMin, value)));
}

/**
 * Adds a keyboard-accessible resize handle to the left edge of a fixed right sheet.
 *
 * The sheet keeps its natural CSS width on small/coarse-pointer screens. On desktop,
 * dragging left grows it, dragging right shrinks it, arrow keys adjust it, and a
 * double click restores the configured default width.
 */
export const sheetWidth: Action<HTMLElement, SheetWidthOptions> = (node, initialOptions) => {
  let options = initialOptions;
  let desiredWidth = loadWidth(options);
  let enabled = false;
  let dragging = false;
  let startX = 0;
  let startWidth = 0;
  let activePointerId: number | null = null;
  let previousBodyCursor = '';
  let previousBodyUserSelect = '';

  const originalInlineWidth = node.style.width;
  const originalResizableAttribute = node.getAttribute('data-sheet-resizable');

  const handle = document.createElement('button');
  handle.type = 'button';
  handle.className = 'sheet-width-handle';
  handle.setAttribute('role', 'separator');
  handle.setAttribute('aria-orientation', 'vertical');
  handle.style.position = 'absolute';
  handle.style.zIndex = '12';
  handle.style.top = '0';
  handle.style.bottom = '0';
  handle.style.left = '-8px';
  handle.style.width = '24px';
  handle.style.padding = '0';
  handle.style.border = '0';
  handle.style.outlineOffset = '-4px';
  handle.style.background = 'transparent';
  handle.style.cursor = 'ew-resize';
  handle.style.touchAction = 'none';
  handle.style.userSelect = 'none';
  handle.style.setProperty('-webkit-app-region', 'no-drag');

  const grip = document.createElement('span');
  grip.className = 'sheet-width-handle-grip';
  grip.setAttribute('aria-hidden', 'true');
  grip.style.position = 'absolute';
  grip.style.top = '50%';
  grip.style.left = '9px';
  grip.style.width = '3px';
  grip.style.height = '58px';
  grip.style.borderRadius = '999px';
  grip.style.background = '#aaa69d';
  grip.style.opacity = '.34';
  grip.style.transform = 'translateY(-50%)';
  grip.style.transition = 'opacity .15s, background .15s';
  handle.append(grip);

  const shield = document.createElement('div');
  shield.className = 'sheet-width-resize-shield';
  shield.setAttribute('aria-hidden', 'true');
  shield.style.position = 'fixed';
  shield.style.zIndex = '11';
  shield.style.inset = '0';
  shield.style.cursor = 'ew-resize';
  shield.style.userSelect = 'none';
  shield.style.setProperty('-webkit-app-region', 'no-drag');

  node.append(handle);

  const coarsePointer = window.matchMedia('(pointer: coarse)');
  const finePointer = window.matchMedia('(any-pointer: fine)');

  function smallScreen(): boolean {
    return window.innerWidth <= Math.max(0, options.smallScreenWidth ?? 760);
  }

  function actionEnabled(): boolean {
    return !options.disabled && !smallScreen() && (!coarsePointer.matches || finePointer.matches);
  }

  function setGripActive(active: boolean): void {
    grip.style.background = active ? 'var(--accent, #a8402a)' : '#aaa69d';
    grip.style.opacity = active ? '.82' : '.34';
  }

  function updateAccessibility(width = clampToViewport(desiredWidth, options)): void {
    const { min, max } = configuredBounds(options);
    const label = options.label?.trim() || '调整面板宽度';
    handle.setAttribute('aria-label', label);
    handle.title = `${label}；左右拖动或按方向键调整，双击恢复默认`;
    handle.setAttribute('aria-valuemin', String(min));
    handle.setAttribute('aria-valuemax', String(Math.min(max, Math.max(1, window.innerWidth - (options.viewportGutter ?? 8)))));
    handle.setAttribute('aria-valuenow', String(width));
    handle.setAttribute('aria-valuetext', `当前 ${width} 像素`);
  }

  function applyDesiredWidth(): void {
    if (!enabled) return;
    const width = clampToViewport(desiredWidth, options);
    node.style.width = `${width}px`;
    updateAccessibility(width);
  }

  function stopResize(save = true): void {
    if (!dragging) return;
    dragging = false;
    if (activePointerId != null && handle.hasPointerCapture(activePointerId)) {
      handle.releasePointerCapture(activePointerId);
    }
    activePointerId = null;
    shield.remove();
    document.body.style.cursor = previousBodyCursor;
    document.body.style.userSelect = previousBodyUserSelect;
    setGripActive(false);
    stopActiveResize = null;
    if (save) writeStoredWidth(options, desiredWidth);
  }

  function syncEnabledState(): void {
    const nextEnabled = actionEnabled();
    if (enabled === nextEnabled) {
      if (enabled) applyDesiredWidth();
      return;
    }
    if (!nextEnabled) stopResize();
    enabled = nextEnabled;
    node.dataset.sheetResizable = String(enabled);
    handle.hidden = !enabled;
    handle.tabIndex = enabled ? 0 : -1;
    if (enabled) {
      applyDesiredWidth();
    } else {
      node.style.width = originalInlineWidth;
    }
  }

  function onPointerDown(event: PointerEvent): void {
    if (!enabled || event.button !== 0) return;
    event.preventDefault();
    event.stopPropagation();
    stopActiveResize?.();
    const bounds = node.getBoundingClientRect();
    startX = event.clientX;
    startWidth = bounds.width || clampToViewport(desiredWidth, options);
    activePointerId = event.pointerId;
    dragging = true;
    previousBodyCursor = document.body.style.cursor;
    previousBodyUserSelect = document.body.style.userSelect;
    document.body.style.cursor = 'ew-resize';
    document.body.style.userSelect = 'none';
    node.append(shield);
    try {
      handle.setPointerCapture(event.pointerId);
    } catch {
      // WebView 拒绝 capture 时由 window 级监听继续完成拖动。
    }
    setGripActive(true);
    stopActiveResize = () => stopResize();
  }

  function onPointerMove(event: PointerEvent): void {
    if (!dragging || event.pointerId !== activePointerId) return;
    event.preventDefault();
    desiredWidth = clampConfigured(startWidth - (event.clientX - startX), options);
    applyDesiredWidth();
  }

  function onPointerUp(event: PointerEvent): void {
    if (!dragging || event.pointerId !== activePointerId) return;
    stopResize();
  }

  function onKeyDown(event: KeyboardEvent): void {
    if (!enabled) return;
    const step = Math.max(1, Math.round(options.keyboardStep ?? 24));
    const delta = event.key === 'ArrowLeft' ? step : event.key === 'ArrowRight' ? -step : 0;
    if (!delta) return;
    event.preventDefault();
    desiredWidth = clampConfigured(desiredWidth + delta, options);
    applyDesiredWidth();
    writeStoredWidth(options, desiredWidth);
  }

  function resetWidth(event?: Event): void {
    event?.preventDefault();
    desiredWidth = clampConfigured(options.defaultWidth, options);
    applyDesiredWidth();
    writeStoredWidth(options, desiredWidth);
  }

  function onFocus(): void {
    setGripActive(true);
  }

  function onBlur(): void {
    if (!dragging) setGripActive(false);
  }

  function onWindowBlur(): void {
    stopResize();
  }

  handle.addEventListener('pointerdown', onPointerDown);
  handle.addEventListener('pointermove', onPointerMove);
  handle.addEventListener('pointerup', onPointerUp);
  handle.addEventListener('pointercancel', onPointerUp);
  window.addEventListener('pointermove', onPointerMove);
  window.addEventListener('pointerup', onPointerUp);
  window.addEventListener('pointercancel', onPointerUp);
  handle.addEventListener('keydown', onKeyDown);
  handle.addEventListener('dblclick', resetWidth);
  handle.addEventListener('focus', onFocus);
  handle.addEventListener('blur', onBlur);
  window.addEventListener('resize', syncEnabledState);
  window.addEventListener('blur', onWindowBlur);
  coarsePointer.addEventListener('change', syncEnabledState);
  finePointer.addEventListener('change', syncEnabledState);

  syncEnabledState();

  return {
    update(nextOptions) {
      const previousId = options.id;
      const previousLegacyKey = options.legacyStorageKey;
      stopResize();
      options = nextOptions;
      if (previousId !== options.id || previousLegacyKey !== options.legacyStorageKey) {
        desiredWidth = loadWidth(options);
      } else {
        desiredWidth = clampConfigured(desiredWidth, options);
      }
      updateAccessibility();
      syncEnabledState();
    },
    destroy() {
      stopResize();
      handle.removeEventListener('pointerdown', onPointerDown);
      handle.removeEventListener('pointermove', onPointerMove);
      handle.removeEventListener('pointerup', onPointerUp);
      handle.removeEventListener('pointercancel', onPointerUp);
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', onPointerUp);
      handle.removeEventListener('keydown', onKeyDown);
      handle.removeEventListener('dblclick', resetWidth);
      handle.removeEventListener('focus', onFocus);
      handle.removeEventListener('blur', onBlur);
      window.removeEventListener('resize', syncEnabledState);
      window.removeEventListener('blur', onWindowBlur);
      coarsePointer.removeEventListener('change', syncEnabledState);
      finePointer.removeEventListener('change', syncEnabledState);
      shield.remove();
      handle.remove();
      node.style.width = originalInlineWidth;
      if (originalResizableAttribute == null) {
        node.removeAttribute('data-sheet-resizable');
      } else {
        node.setAttribute('data-sheet-resizable', originalResizableAttribute);
      }
    },
  };
};
