export type ModelSetting = { brain: string; model: string; effort: string };
export type ManagedMode = 'plan' | 'growth';
export const managedModeOptions = [
  { value: 'plan', label: '计划托管', sub: '按已保存的运营计划执行内容、审计和周报' },
  { value: 'growth', label: '增长托管', sub: '结合站点定位与可用数据发现机会、执行和复盘' },
];
export function managedModeName(mode: string): string { return mode === 'growth' ? '增长托管' : '计划托管'; }
export const managedModeSwitchNotice = '将同步配套任务的名称与提示词，不会立即执行任务。切换模式本身不改变任务时间、启停状态、模型、预算及已保存的运营计划。草稿、周报和增长机会记录均保留，各模式的自定义提示词分别保存。有任务正在运行时需等待结束后再切换。';
export type ManagedSettingsPatch = {
  mode?: ManagedMode;
  primary?: ModelSetting;
  fallback?: ModelSetting;
  level?: string;
  weekly_post_limit?: number;
  weekly_edit_limit?: number;
  token_weekly_budget?: number;
};
export type ManagedSettingsTarget = {
  id: string; site_name: string; brain: string; model: string; effort: string;
  mode?: ManagedMode;
  fallback_brain?: string; fallback_model?: string; fallback_effort?: string;
  level: string; weekly_post_limit: number; weekly_edit_limit: number; token_weekly_budget: number;
};
export const settingLabels = {
  mode: '托管模式', primary: '主模型', fallback: '备用模型', level: '托管级别',
  weekly_post_limit: '每周产出上限', weekly_edit_limit: '每周存量修改上限', token_weekly_budget: '每周 token 预算',
};
export type SettingKey = keyof typeof settingLabels;
export function validateManagedPatch(patch: ManagedSettingsPatch, targets: ManagedSettingsTarget[]): string {
  if (!targets.length) return '请选择托管站点';
  if (!Object.keys(patch).length) return '请至少选择一个修改项';
  if (patch.mode !== undefined && !['plan', 'growth'].includes(patch.mode)) return '托管模式无效';
  for (const [key, max] of [['weekly_post_limit', 50], ['weekly_edit_limit', 20], ['token_weekly_budget', Number.MAX_SAFE_INTEGER]] as const) {
    const value = patch[key];
    if (value !== undefined && (!Number.isSafeInteger(value) || value < (key === 'token_weekly_budget' ? 0 : 1) || value > max)) return `${settingLabels[key]}数值无效`;
  }
  for (const t of targets) {
    const p = patch.primary ?? {brain:t.brain, model:t.model, effort:t.effort};
    const f = patch.fallback ?? {brain:t.fallback_brain, model:t.fallback_model, effort:t.fallback_effort};
    if (f.brain && p.brain === f.brain && p.model === f.model && (p.effort || '') === (f.effort || '')) return `${t.site_name}：备用模型不能与主模型完全相同`;
  }
  return '';
}
