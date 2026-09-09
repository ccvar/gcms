export const MANAGED_PLAN_TIMEOUT_SECONDS = 8 * 60;
export type PlanRun = {
  id: string; connId: string; siteSlug: string; initialPlan: string;
  startedAt: number; apply: boolean; stopReason: string; stopError: string;
  model: string;
};
export function canApplyPlan(run: PlanRun, current: { open: boolean; connId: string; siteSlug: string; plan: string; mode: string }) {
  return run.apply && !run.stopReason && current.open && current.mode === 'plan'
    && run.connId === current.connId && run.siteSlug === current.siteSlug && run.initialPlan === current.plan;
}
export function planElapsed(start: number, now: number) {
  const seconds = Math.max(0, Math.floor((now - start) / 1000));
  return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
}
