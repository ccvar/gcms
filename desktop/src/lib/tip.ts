// 深色迷你 hover 气泡（可复用 Svelte action）：`use:tip={'文案'}`。
// 行为与 UsageRing 原自绘气泡一致：~300ms 延时显示；fixed 定位在目标上方居中、
// 左右 8px 钳制不出界、上方空间不够翻下方；pointer-events:none 不挡交互；
// 离开/按下/页面滚动/文案清空立即隐藏。样式类 .ui-tip（+page.svelte 里 :global 定义）。
export function tip(node: HTMLElement, text: string) {
  let el: HTMLDivElement | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let cur = text;

  function hide() {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    el?.remove();
    el = null;
    window.removeEventListener('scroll', hide, true);
  }
  function place() {
    if (!el) return;
    const r = node.getBoundingClientRect();
    const w = el.offsetWidth;
    const h = el.offsetHeight;
    const left = Math.max(8, Math.min(r.left + r.width / 2 - w / 2, window.innerWidth - w - 8));
    const top = r.top - h - 6 >= 8 ? r.top - h - 6 : r.bottom + 6;
    el.style.top = `${Math.round(top)}px`;
    el.style.left = `${Math.round(left)}px`;
  }
  function show() {
    if (!cur || timer || el) return;
    timer = setTimeout(() => {
      timer = null;
      if (!cur) return;
      el = document.createElement('div');
      el.className = 'ui-tip';
      el.textContent = cur;
      document.body.appendChild(el);
      place();
      window.addEventListener('scroll', hide, true);
    }, 300);
  }
  node.addEventListener('mouseenter', show);
  node.addEventListener('mouseleave', hide);
  node.addEventListener('mousedown', hide);
  return {
    update(t: string) {
      cur = t;
      if (!t) {
        hide();
        return;
      }
      if (el) {
        el.textContent = t;
        place();
      }
    },
    destroy() {
      hide();
      node.removeEventListener('mouseenter', show);
      node.removeEventListener('mouseleave', hide);
      node.removeEventListener('mousedown', hide);
    },
  };
}
