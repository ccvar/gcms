import type { Brain, TaskType } from './types';

export interface Prefs {
  brain: Brain;
  model: string;
  /** 全局自定义模型 ID（按厂商分开；留空 → 用档位别名 / codex 默认）。在「连接与模型」里设置。 */
  customClaude: string;
  customCodex: string;
  taskType: TaskType;
}

const KEY = 'gcms.pilot.prefs';

export const DEFAULT_PREFS: Prefs = { brain: 'claude', model: 'sonnet', customClaude: '', customCodex: '', taskType: 'article' };

export function loadPrefs(): Prefs {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) return { ...DEFAULT_PREFS, ...JSON.parse(raw) };
  } catch {
    /* ignore */
  }
  return { ...DEFAULT_PREFS };
}

export function savePrefs(p: Prefs): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(p));
  } catch {
    /* ignore */
  }
}
