import type { Brain, TaskType } from './types';

export interface Prefs {
  brain: Brain;
  model: string;
  /** 全局自定义模型 ID 列表（按厂商分开）；作为该厂商模型下拉里的附加档位。在「连接与模型」里增删。 */
  customClaudeIds: string[];
  customCodexIds: string[];
  taskType: TaskType;
  /** 权限档位默认值：plan | ask | auto | full。默认 full（保持既有全自动，不改现有 gcms 行为）。 */
  perm: string;
}

const KEY = 'gcms.pilot.prefs';

export const DEFAULT_PREFS: Prefs = { brain: 'claude', model: 'sonnet', customClaudeIds: [], customCodexIds: [], taskType: 'article', perm: 'full' };

export function loadPrefs(): Prefs {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) {
      const p = { ...DEFAULT_PREFS, ...JSON.parse(raw) } as Prefs & { customClaude?: string; customCodex?: string };
      // 始终用全新数组（避免共享 DEFAULT_PREFS 的引用被后续 push/splice 污染）。
      p.customClaudeIds = Array.isArray(p.customClaudeIds) ? [...p.customClaudeIds] : [];
      p.customCodexIds = Array.isArray(p.customCodexIds) ? [...p.customCodexIds] : [];
      // 迁移旧的单串自定义模型 → 数组。
      const c = (p.customClaude ?? '').trim();
      const x = (p.customCodex ?? '').trim();
      if (c && !p.customClaudeIds.includes(c)) p.customClaudeIds.push(c);
      if (x && !p.customCodexIds.includes(x)) p.customCodexIds.push(x);
      delete p.customClaude;
      delete p.customCodex;
      return p;
    }
  } catch {
    /* ignore */
  }
  return { ...DEFAULT_PREFS, customClaudeIds: [], customCodexIds: [] };
}

export function savePrefs(p: Prefs): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(p));
  } catch {
    /* ignore */
  }
}
