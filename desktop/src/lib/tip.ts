// 全局 tooltip 定位器：所有气泡统一挂到 body，按渲染后的真实尺寸避让视口边缘。
// 这样不会被卡片、侧栏或抽屉的 overflow 裁剪，也不需要每个入口手工判断向左/向右。
export type TipAlign = 'start' | 'center' | 'end';

export interface TipOptions {
  align?: TipAlign;
  delay?: number;
}

const EDGE = 8;
const GAP = 7;
const MAX_WIDTH = 320;
const DEFAULT_DELAY = 300;

let activeNode: HTMLElement | null = null;
let activeText = '';
let activeAlign: TipAlign = 'center';
let tipEl: HTMLDivElement | null = null;
let showTimer: ReturnType<typeof setTimeout> | null = null;

function clearTimer() {
  if (!showTimer) return;
  clearTimeout(showTimer);
  showTimer = null;
}

function viewportRect() {
  const root = document.documentElement;
  return {
    top: 0,
    left: 0,
    right: Math.max(0, root.clientWidth || window.innerWidth),
    bottom: Math.max(0, root.clientHeight || window.innerHeight),
  };
}

function clamp(value: number, min: number, max: number) {
  if (max < min) return min;
  return Math.min(Math.max(value, min), max);
}

function placeTip() {
  if (!tipEl || !activeNode?.isConnected) {
    hideTip();
    return;
  }

  const viewport = viewportRect();
  const target = activeNode.getBoundingClientRect();
  const availableWidth = Math.max(1, viewport.right - viewport.left - EDGE * 2);

  // 先给出最终限宽，再读取真实尺寸；不再靠「大概半个 tooltip 宽」来猜边界。
  tipEl.style.maxWidth = `${Math.min(MAX_WIDTH, availableWidth)}px`;
  tipEl.style.visibility = 'hidden';
  tipEl.style.left = '0px';
  tipEl.style.top = '0px';

  const width = tipEl.offsetWidth;
  const height = tipEl.offsetHeight;
  const minLeft = viewport.left + EDGE;
  const maxLeft = viewport.right - EDGE - width;
  let left = target.left + (target.width - width) / 2;
  if (activeAlign === 'start') left = target.left;
  if (activeAlign === 'end') left = target.right - width;
  left = clamp(left, minLeft, maxLeft);

  const minTop = viewport.top + EDGE;
  const maxTop = viewport.bottom - EDGE - height;
  const above = target.top - viewport.top;
  const below = viewport.bottom - target.bottom;
  const fitsAbove = above >= height + GAP + EDGE;
  const fitsBelow = below >= height + GAP + EDGE;
  let placement: 'top' | 'bottom' = 'top';
  let top = target.top - height - GAP;

  if (!fitsAbove && fitsBelow) {
    placement = 'bottom';
    top = target.bottom + GAP;
  } else if (!fitsAbove && !fitsBelow && below > above) {
    placement = 'bottom';
    top = target.bottom + GAP;
  }
  top = clamp(top, minTop, maxTop);

  tipEl.dataset.placement = placement;
  tipEl.style.left = `${Math.round(left)}px`;
  tipEl.style.top = `${Math.round(top)}px`;
  tipEl.style.visibility = 'visible';
}

function onViewportChange() {
  if (tipEl) placeTip();
}

function onViewportScroll() {
  hideTip();
}

function mountTip() {
  showTimer = null;
  if (!activeNode?.isConnected || !activeText) return;

  tipEl = document.createElement('div');
  tipEl.className = 'ui-tip';
  tipEl.setAttribute('role', 'tooltip');
  tipEl.textContent = activeText;
  document.body.appendChild(tipEl);
  placeTip();

  window.addEventListener('scroll', onViewportScroll, true);
  window.addEventListener('resize', onViewportChange);
}

/** 显示统一 tooltip。重复命中同一元素时不会重启延时。 */
export function showTipFor(node: HTMLElement, text: string, options: TipOptions = {}) {
  const nextText = text.trim();
  if (!nextText) {
    hideTip(node);
    return;
  }

  const nextAlign = options.align ?? 'center';
  if (activeNode === node) {
    activeText = nextText;
    activeAlign = nextAlign;
    if (tipEl) {
      tipEl.textContent = nextText;
      placeTip();
    }
    return;
  }

  hideTip();
  activeNode = node;
  activeText = nextText;
  activeAlign = nextAlign;
  showTimer = setTimeout(mountTip, Math.max(0, options.delay ?? DEFAULT_DELAY));
}

/** 隐藏当前 tooltip；传 node 时只关闭该元素自己的气泡。 */
export function hideTip(node?: HTMLElement) {
  if (node && activeNode !== node) return;
  clearTimer();
  tipEl?.remove();
  tipEl = null;
  activeNode = null;
  activeText = '';
  activeAlign = 'center';
  window.removeEventListener('scroll', onViewportScroll, true);
  window.removeEventListener('resize', onViewportChange);
}

// 可复用 Svelte action：`use:tip={'文案'}`。
export function tip(node: HTMLElement, text: string) {
  let current = text;

  const show = () => showTipFor(node, current);
  const hide = () => hideTip(node);

  node.addEventListener('mouseenter', show);
  node.addEventListener('mouseleave', hide);
  node.addEventListener('focusin', show);
  node.addEventListener('focusout', hide);
  node.addEventListener('mousedown', hide);

  return {
    update(next: string) {
      current = next;
      if (!next) {
        hide();
        return;
      }
      if (activeNode === node) showTipFor(node, next, { delay: 0 });
    },
    destroy() {
      hide();
      node.removeEventListener('mouseenter', show);
      node.removeEventListener('mouseleave', hide);
      node.removeEventListener('focusin', show);
      node.removeEventListener('focusout', hide);
      node.removeEventListener('mousedown', hide);
    },
  };
}
