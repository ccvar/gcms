<script lang="ts">
  import { invoke, Channel } from '@tauri-apps/api/core';
  // xterm 自带样式：必须从 JS 侧引入。写进组件样式块的 @import 会被 Svelte 作用域化，
  // 而 xterm 的 DOM 是运行时建的、静态分析看不到 → 26 条 unused selector 警告。
  import '@xterm/xterm/css/xterm.css';
  import { getCurrentWindow } from '@tauri-apps/api/window';
  import { getVersion } from '@tauri-apps/api/app';
  import { open, save as saveDialog, confirm as confirmDialog } from '@tauri-apps/plugin-dialog';
  import { openUrl, revealItemInDir } from '@tauri-apps/plugin-opener';
  import { check as checkUpdate } from '@tauri-apps/plugin-updater';
  import { relaunch } from '@tauri-apps/plugin-process';
  import BrainIcon from '$lib/BrainIcon.svelte';
  import SiteMark from '$lib/SiteMark.svelte';
  import SiteFav from '$lib/SiteFav.svelte';
  import ModelFx from '$lib/ModelFx.svelte';
  import UsageRing from '$lib/UsageRing.svelte';
  import { tip as tipAction } from '$lib/tip';
  import { listen } from '@tauri-apps/api/event';
  import AppIcon from '$lib/AppIcon.svelte';
  import type {
    Connection, Discovery, Site, BrainsInfo, Brain, ImportOutcome,
    Conversation, Message, TaskType, TurnEvent, ToolCall, ScheduledItem, ScheduledTask, TaskProposal,
  } from '$lib/types';
  import { loadPrefs, savePrefs } from '$lib/defaults';
  import { PRESET_PROMPTS, loadUserPrompts, saveUserPrompts, newPromptId, type Prompt } from '$lib/prompts';
  import Dropdown from '$lib/Dropdown.svelte';
  import { marked } from 'marked';
  import DOMPurify from 'dompurify';

  // ---------- setup ----------
  let conns = $state<Connection[]>([]);
  let activeConnId = $state('');
  let discovery = $state<Discovery | null>(null);
  let discoveryLoading = $state(false);
  let brains = $state<BrainsInfo | null>(null);
  let importBusy = $state(false);
  let setupOpen = $state(false);
  let switcherOpen = $state(false);
  // 连接切换器里每条连接的右键菜单（新窗口打开 / 编辑 / 删除）。
  let connCtx = $state<null | { x: number; y: number; conn: Connection }>(null);
  function openConnCtx(e: MouseEvent, c: Connection) {
    e.preventDefault();
    e.stopPropagation(); // 别让全局那个输入框右键菜单也跳出来
    connCtx = { x: Math.min(e.clientX, window.innerWidth - 180), y: Math.min(e.clientY, window.innerHeight - 150), conn: c };
  }
  $effect(() => {
    if (!connCtx) return;
    const close = () => (connCtx = null);
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') connCtx = null; };
    window.addEventListener('click', close);
    window.addEventListener('contextmenu', close);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('click', close);
      window.removeEventListener('contextmenu', close);
      window.removeEventListener('keydown', onKey);
    };
  });
  async function openConnWindow(id: string) {
    try { await invoke('open_conn_window', { connId: id }); }
    catch (e) { say(String(e), 'err'); }
  }
  let footerEl = $state<HTMLElement | null>(null);
  $effect(() => {
    if (!switcherOpen) return;
    const onDoc = (e: MouseEvent) => { if (footerEl && !footerEl.contains(e.target as Node)) switcherOpen = false; };
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') switcherOpen = false; };
    // 捕获阶段：点空白（含拖拽区）也能关闭切换器。
    document.addEventListener('mousedown', onDoc, true);
    document.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('mousedown', onDoc, true); document.removeEventListener('keydown', onKey); };
  });
  let flash = $state('');
  let flashKind = $state<'ok' | 'err'>('ok');

  const activeConn = $derived(conns.find((c) => c.id === activeConnId) ?? null);
  const sites = $derived(discovery?.items ?? []);

  /** 全部厂商（顺序即下拉展示顺序）。新增厂商在此登记，brainUsable/brainLabel/模型档随之生效。 */
  const ALL_BRAINS: Brain[] = ['claude', 'codex', 'grok'];
  /** 第一个可用（已装已登录）的厂商；全不可用时回退 claude（保持旧行为）。 */
  function firstUsableBrain(): Brain { return ALL_BRAINS.find((b) => brainUsable(b)) ?? 'claude'; }

  // ---------- conversations ----------
  let convos = $state<Conversation[]>([]);
  let activeConvId = $state('');
  let activeConv = $state<Conversation | null>(null);
  // 会话可切换模型；threadModel 跟随活动会话、改动即持久化。
  let threadModel = $state('');
  async function persistThreadModel(m: string) {
    if (!activeConv || m === activeConv.model) return;
    try {
      const u = await invoke<Conversation | null>('set_conversation_model', { convId: activeConv.id, model: m });
      if (u && activeConvId === u.id) activeConv = u;
    } catch (e) { say(String(e), 'err'); }
  }
  // 会话内模型面板：三家厂商的模型都列出（值编码 "<brain>::<model>"，同启动器 comboOpts；行首图标区分）。
  // 当前厂商永远可选（保持原行为）；其他厂商未就绪（未装/未登录）整组置灰。当前厂商排前面。
  const threadComboOpts = $derived.by(() => {
    const cur = (activeConv?.brain ?? 'claude') as Brain;
    const order: Brain[] = [cur, ...ALL_BRAINS.filter((b) => b !== cur)];
    const out: { value: string; label: string; sub?: string; icon?: string; disabled?: boolean }[] = [];
    for (const b of order) {
      // 当前厂商永远可选（会话已经在用它了，灰掉只会让触发器显示成一个选不中的项）。
      // 远程连接下的 codex 不置灰、只挂注记：它能用，只是那道闸是打折的（完整说法见 permTipFor）。
      const usable = b === cur || brainUsable(b);
      const note = isCodexSsh(b, isSshConn) ? CODEX_SSH_NOTE : usable ? '' : brains?.[b].found ? '未登录' : '未安装';
      for (const m of launcherModelOpts(b)) {
        out.push({ value: `${b}::${m.value}`, label: m.label, sub: note || m.sub, icon: b, disabled: !usable });
      }
    }
    // 会话当前模型不在清单里（比如自定义 ID 事后被删）：补一条，触发器别显示成裸编码值。
    const curVal = `${cur}::${threadModel}`;
    if (!out.some((o) => o.value === curVal)) out.unshift({ value: curVal, label: threadModel || '模型', sub: '当前会话', icon: cur });
    return out;
  });
  // 会话内选模型：同厂商＝只改存储、下一轮生效（原行为不变）；跨厂商＝底层 session 换不了家，
  // 确认后更新会话 brain/model（后端顺带清 session_ref；非 ssh 的 codex 下 ask/auto 落 full，
  // ssh 下**保持原档**——桥那道卡是 codex 仅剩的闸，见 lib.rs::apply_brain_switch），
  // 再走「重建继续」：全新底层会话 + 历史摘要，自动重跑最近一条用户请求。
  async function pickThreadCombo(v: string) {
    const conv = activeConv;
    if (!conv || running[conv.id]) return;
    const i = v.indexOf('::');
    if (i < 0) return;
    const brain = v.slice(0, i) as Brain;
    const model = v.slice(i + 2);
    if (brain === conv.brain) { threadModel = model; void persistThreadModel(model); return; }
    const label = brainLabel(brain);
    // ★ 这个框就是「在真机上自动重跑」的同意点：切完立刻 rebuildSession 重跑最近一条请求。
    // 切到 codex 的风险必须**当场**说，不能只靠权限档位 tip 那种要悬停才出的——用户点完就跑了。
    const risk = isCodexSsh(brain, isSshConn)
      ? `\n\n注意：Codex 无头模式没有逐命令闸。经 Pilot 跑的远程命令仍会弹卡${threadPerm === 'full' ? '（但「全自动」档下这些卡也全关了）' : ''}，但它可以绕开 Pilot、直接用你本机的 ssh 和 ~/.ssh 密钥连这台机器——那条路一张卡都不弹。`
      : '';
    const yes = await confirmDialog(
      `切到 ${label} 后，这条对话将以历史摘要重建一个全新底层会话继续（自动重跑最近一条请求）。项目文件不受影响；很长的对话会有一定上下文损耗。${risk}`,
      { title: `切换到 ${label}`, kind: 'warning' },
    );
    if (!yes) return;
    try {
      const u = await invoke<Conversation | null>('set_conversation_brain_model', { convId: conv.id, brain, model });
      if (u && activeConvId === u.id) { activeConv = u; threadModel = u.model; threadPerm = u.perm_mode || 'full'; threadEffort = u.effort || ''; }
      void rebuildSession(conv.id);
    } catch (e) { say(String(e), 'err'); }
  }
  // 进行中会话可切换权限档位：claude 的钩子/参数和 codex 的 sandbox 都是每轮下发的，
  // 改完从下一轮（含排队消息）生效；正在跑的那轮已带旧档位启动，改动拦不住它（想拦先停止）。
  let threadPerm = $state('');
  let threadEffort = $state('');
  async function persistThreadEffort(v: string) {
    if (!activeConv || v === (activeConv.effort || '')) return;
    try {
      const u = await invoke<Conversation | null>('set_conversation_effort', { convId: activeConv.id, effort: v });
      if (u && activeConvId === u.id) { activeConv = u; threadEffort = u.effort || ''; }
      if (viewBusy) say('推理强度已调整，从下一轮开始生效');
    } catch (e) { say(String(e), 'err'); }
  }
  async function persistThreadPerm(p: string) {
    if (!activeConv || p === (activeConv.perm_mode || 'full')) return;
    try {
      const u = await invoke<Conversation | null>('set_conversation_perm_mode', { convId: activeConv.id, permMode: p });
      // 回写 threadPerm：若这段 RPC 期间轮次刚好收尾，endTurn 可能已把下拉打回旧值，这里以落库值自愈。
      if (u && activeConvId === u.id) { activeConv = u; threadPerm = u.perm_mode || 'full'; }
      if (viewBusy) say('权限档位已切换，从下一轮开始生效（不影响正在跑的这轮）');
    } catch (e) { say(String(e), 'err'); }
  }
  let view = $state<'launcher' | 'thread' | 'schedule' | 'tasks' | 'managed' | 'templates' | 'prompts' | 'remote'>('launcher');

  // 排期视图
  let sched = $state<ScheduledItem[]>([]);
  let schedLoading = $state(false);
  let schedError = $state('');
  async function openSchedule() {
    view = 'schedule'; activeConvId = ''; activeConv = null;
    await loadScheduled();
  }
  async function loadScheduled() {
    if (!activeConn) return;
    schedLoading = true; schedError = '';
    try { sched = await invoke<ScheduledItem[]>('list_scheduled', { connId: activeConnId }); }
    catch (e) { schedError = String(e); }
    finally { schedLoading = false; }
  }

  // 定时任务
  let tasks = $state<ScheduledTask[]>([]);
  async function openTasks() { view = 'tasks'; activeConvId = ''; activeConv = null; await loadTasks(); }
  async function loadTasks() { try { tasks = await invoke<ScheduledTask[]>('list_tasks'); } catch (e) { say(String(e), 'err'); } }

  // ---------- 托管（AI 全权运营站点 · P1/L0 试运行）----------
  // 机制化边界：AI 只产草稿（配套任务 prompt 写死硬约束），发布/打回都在待审队列里由人完成。
  type ManagedNote = { ts: number; post_id: number; title: string; reason: string };
  type ManagedSite = {
    id: string; conn_id: string; site_slug: string; site_name: string; level: string;
    weekly_post_limit: number; weekly_edit_limit: number; plan: string; brain: string; model: string; effort: string;
    task_ids: string[]; paused: boolean; review_notes: ManagedNote[];
    token_weekly_budget: number; fused_at: number;
    review_events: { ts: number; approved: boolean }[]; demote_note: string;
    audit_notes: string; enabled_at: number;
    reports: { ts: number; content: string; metrics: ManagedReportMetrics }[];
    created_at: number; updated_at: number;
  };
  type ManagedReportMetrics = { published: number | null; drafts_new: number | null; rejected: number | null; discarded: number | null; tokens: number | null; impressions: number | null; clicks: number | null };
  type ManagedDraft = { id: number; title: string; lang: string; updated_at: string };
  type ManagedSummary = {
    published_this_week: number; drafts: number; drafts_new: number; drafts_discarded: number; week_start: number; week_tokens: number;
    tasks: { id: string; title: string; enabled: boolean; last_status: string; last_run: number; next_run: number; last_conv_id: string }[];
  };
  let managedList = $state<ManagedSite[]>([]);
  let managedLoading = $state(false);
  const managedOfConn = $derived(managedList.filter((m) => m.conn_id === activeConnId));
  let mSummaries = $state<Record<string, ManagedSummary>>({});
  let mDrafts = $state<Record<string, ManagedDraft[]>>({});
  let mQueueOpen = $state<Record<string, boolean>>({});
  let mPlanOpen = $state<Record<string, boolean>>({});
  let mPlanDraft = $state<Record<string, string>>({});
  async function openManaged() { view = 'managed'; activeConvId = ''; activeConv = null; await loadManaged(); }
  async function loadManaged() {
    managedLoading = true;
    try { managedList = await invoke<ManagedSite[]>('managed_list'); } catch (e) { say(String(e), 'err'); }
    finally { managedLoading = false; }
    for (const m of managedList.filter((x) => x.conn_id === activeConnId)) void loadManagedDetail(m);
  }
  // 周报数字 + 待审草稿按卡拉取；单卡失败不打断其他卡（例如某站密钥缺 posts 权限）。
  async function loadManagedDetail(m: ManagedSite) {
    try { mSummaries[m.id] = await invoke<ManagedSummary>('managed_summary', { id: m.id }); } catch { /* 保留旧值 */ }
    try { mDrafts[m.id] = await invoke<ManagedDraft[]>('managed_drafts', { connId: m.conn_id, siteSlug: m.site_slug }); } catch { /* 同上 */ }
  }
  async function toggleManaged(m: ManagedSite) {
    try { await invoke(m.paused ? 'managed_resume' : 'managed_pause', { id: m.id }); await loadManaged(); }
    catch (e) { say(String(e), 'err'); }
  }
  async function disableManaged(m: ManagedSite) {
    const yes = await confirmDialog(`关闭「${m.site_name}」的托管？两个配套定时任务会被删除；站点内容与草稿一概不动。`, { title: '关闭托管', kind: 'warning' });
    if (!yes) return;
    try { await invoke('managed_disable', { id: m.id }); await loadManaged(); await loadTasks(); } catch (e) { say(String(e), 'err'); }
  }
  async function saveManagedPlan(m: ManagedSite) {
    try {
      await invoke('managed_plan_save', { id: m.id, plan: mPlanDraft[m.id] ?? '' });
      say('计划已保存，并已同步进每日任务'); mPlanOpen[m.id] = false; await loadManaged();
    } catch (e) { say(String(e), 'err'); }
  }
  async function managedPreview(m: ManagedSite, d: ManagedDraft) {
    try { const u = await invoke<string>('scheduled_preview_url', { connId: m.conn_id, siteSlug: m.site_slug, id: d.id }); await openUrl(u); }
    catch (e) { say(String(e), 'err'); }
  }
  async function approveDraft(m: ManagedSite, d: ManagedDraft) {
    const yes = await confirmDialog(`发布《${d.title}》？发布后立即公开可见（只改状态，其余字段不动）。`, { title: '批准发布', kind: 'warning' });
    if (!yes) return;
    try { await invoke('managed_publish', { connId: m.conn_id, siteSlug: m.site_slug, id: d.id }); say('已发布'); void loadManagedDetail(m); }
    catch (e) { say(String(e), 'err'); }
  }
  // 打回：只记理由（草稿不动），后端记审核事件 + 注入每日任务 prompt，并可能触发自动降级。
  let rejectFor = $state<{ m: ManagedSite; d: ManagedDraft } | null>(null);
  let rejectReason = $state('');
  // rework=true：记录理由后立刻触发每日任务返工（run_task_now）。
  async function submitReject(rework = false) {
    if (!rejectFor || !rejectReason.trim()) return;
    const { m, d } = rejectFor;
    try {
      await invoke('managed_record_reject', { id: m.id, postId: d.id, title: d.title, reason: rejectReason });
      if (rework && m.task_ids[0]) {
        try { await invoke('run_task_now', { id: m.task_ids[0] }); say('已记录打回，并已触发每日任务立即返工'); }
        catch (e) { say(`已记录打回，但触发返工失败：${String(e)}`, 'err'); }
      } else {
        say('已记录打回意见，后续任务会规避同类问题');
      }
      rejectFor = null; rejectReason = ''; await loadManaged();
    } catch (e) { say(String(e), 'err'); }
  }
  // 托管等级：展示名与选项（与后端 managed::level_label 同口径）。
  function levelLabel(l: string): string { return l === 'l1' ? 'L1 自动发布' : l === 'l2' ? 'L2 自动发布+抽检' : l === 'l3' ? 'L3 存量维护' : 'L0 试运行'; }
  const LEVEL_OPTS = [
    { value: 'l0', label: 'L0 试运行', sub: '只产草稿 · 人审人发' },
    { value: 'l1', label: 'L1 自动发布', sub: '常规文章可直接发布' },
    { value: 'l2', label: 'L2 自动发布+抽检', sub: 'L1 + 周报附抽查清单' },
    { value: 'l3', label: 'L3 存量维护', sub: '可改旧文/下线转草稿 · 慎用' },
  ];
  /** L3 强警示文案（调级弹窗与向导共用）。 */
  const L3_WARN = `⚠️ L3 允许 AI 修改已发布的存量内容（标题/正文/meta/内链）并把低质旧文转草稿下线：
① 修改存量可能损害既有搜索流量——排名波动风险自负；
② 需要服务端 ≥ v1.3.23（含内容修订历史，可在后台一键回滚）且连接密钥含 stats:read——否则 AI 拿不到 GSC/GA 数据、只能凭感觉改，强烈不建议开启；
③ 建议先在 L1/L2 稳定运行满 2 周、打回率低后再升 L3。
改动均会留修订历史；每周查看周报的「观察名单」跟踪数据回落。`;
  function modelDisp(brain: string, model: string): string { return launcherModelOpts(brain).find((o) => o.value === model)?.label ?? (model || '默认'); }
  function effortDisp(e: string): string { return e === 'low' ? '低' : e === 'medium' ? '中' : e === 'high' ? '高' : ''; }
  function mdTok(n: number): string { return n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1e3 ? Math.round(n / 1e3) + 'k' : String(n); }
  // 调级（卡上 Dropdown 已 bind 改了 m.level）：确认弹窗写明含义；取消则 reload 还原。
  async function changeLevel(m: ManagedSite, level: string) {
    const desc = level === 'l0'
      ? '回到只产草稿、人审人发。'
      : level === 'l1'
        ? 'AI 自检通过后可直接发布常规文章；删除/导航/站点结构仍全部禁止，审计纪要等仍走草稿待审。'
        : level === 'l2'
          ? '在 L1 基础上，每周周报会列出本周自动发布清单供你抽查。'
          : L3_WARN;
    // L3 用红色警示弹窗（error），其余等级黄色确认（warning）。
    const yes = await confirmDialog(`把「${m.site_name}」调整为 ${levelLabel(level)}？\n${desc}`, { title: level === 'l3' ? '⚠️ 升到 L3 存量维护（高风险）' : '调整托管等级', kind: level === 'l3' ? 'error' : 'warning' });
    if (!yes) { await loadManaged(); return; }
    try { await invoke('managed_set_level', { id: m.id, level }); say('等级已调整，已同步每日任务'); await loadManaged(); }
    catch (e) { say(String(e), 'err'); await loadManaged(); }
  }
  // L3 存量修改配额（prompt 声明 + Pilot 触发前实测硬闸：配额用完当天禁改存量）。
  async function saveEditLimit(m: ManagedSite) {
    const v = Math.min(20, Math.max(1, Math.round(Number(m.weekly_edit_limit) || 2)));
    try { await invoke('managed_set_edit_limit', { id: m.id, limit: v }); say('存量修改配额已更新，已同步每日任务'); await loadManaged(); }
    catch (e) { say(String(e), 'err'); await loadManaged(); }
  }
  // 周报归档：查看=打开历史列表（新到旧），点条目看渲染后的正文；无归档回落旧行为（打开上次对话）。
  let reportListFor = $state<ManagedSite | null>(null);
  let reportView = $state<{ ts: number; content: string; metrics: ManagedReportMetrics } | null>(null);
  /** 归档条目的核心数字摘要（只列拿得到的项）。 */
  function reportMetLine(mt: ManagedReportMetrics | undefined): string {
    if (!mt) return '';
    const parts: string[] = [];
    if (mt.published != null) parts.push(`发布 ${mt.published}`);
    if (mt.drafts_new != null) parts.push(`新草稿 ${mt.drafts_new}`);
    if (mt.rejected != null) parts.push(`打回 ${mt.rejected}`);
    if (mt.impressions != null) parts.push(`曝光 ${mt.impressions}`);
    if (mt.clicks != null) parts.push(`点击 ${mt.clicks}`);
    return parts.join(' · ');
  }
  /** 仪表盘趋势：归档周报某指标的时间序列（旧→新，最多 8 期；不足 2 期不画）。 */
  function reportTrend(m: ManagedSite, key: 'published' | 'clicks'): number[] {
    const vals = (m.reports ?? []).map((r) => r.metrics?.[key]).filter((v): v is number => typeof v === 'number');
    return vals.slice(0, 8).reverse();
  }
  /** 12px 内联小柱高度（最矮 2px 保证可见）。 */
  function trendBarPx(v: number, arr: number[]): number {
    const max = Math.max(...arr, 1);
    return Math.max(2, Math.round((v / max) * 12));
  }
  /** 仪表盘汇总（纯派生自 managed_list + mSummaries，不新造统计）。 */
  const mdAgg = $derived.by(() => {
    let run = 0, paused = 0, fused = 0, out = 0, cap = 0, drafts = 0, tokens = 0;
    for (const m of managedOfConn) {
      if (m.fused_at) fused++;
      else if (m.paused) paused++;
      else run++;
      cap += m.weekly_post_limit;
      const s = mSummaries[m.id];
      if (s) { out += s.published_this_week + s.drafts_new; drafts += s.drafts; tokens += s.week_tokens; }
    }
    return { run, paused, fused, out, cap, drafts, tokens };
  });
  /** 例外站（只列需要关注的）：熔断 / 暂停 / 待审>0 / 预算已用 ≥80%（设了预算才算）。 */
  const mdExceptions = $derived.by(() => {
    const out: { m: ManagedSite; tags: { t: string; k: 'err' | 'warn' | 'off' }[] }[] = [];
    for (const m of managedOfConn) {
      const s = mSummaries[m.id];
      const tags: { t: string; k: 'err' | 'warn' | 'off' }[] = [];
      if (m.fused_at) tags.push({ t: '熔断', k: 'err' });
      else if (m.paused) tags.push({ t: '暂停', k: 'off' });
      if (s && s.drafts > 0) tags.push({ t: `待审 ${s.drafts}`, k: 'warn' });
      if (m.token_weekly_budget > 0 && s && s.week_tokens >= m.token_weekly_budget * 0.8) {
        tags.push({ t: `预算 ${Math.round((s.week_tokens / m.token_weekly_budget) * 100)}%`, k: 'warn' });
      }
      if (tags.length) out.push({ m, tags });
    }
    return out;
  });
  /** 定时任务概览（纯本地派生，按 history 汇总今日口径）。 */
  const taskAgg = $derived.by(() => {
    const midnight = new Date();
    midnight.setHours(0, 0, 0, 0);
    const t0 = Math.floor(midnight.getTime() / 1000);
    let ran = 0, failed = 0, deferred = 0, enabled = 0;
    let next: { ts: number; title: string } | null = null;
    for (const t of tasks) {
      if (t.enabled) {
        enabled++;
        if (t.next_run > 0 && (!next || t.next_run < next.ts)) next = { ts: t.next_run, title: t.title };
      }
      for (const r of t.history ?? []) {
        if (r.ts < t0) continue;
        if (r.deferred) deferred++;
        else {
          ran++;
          if (!r.ok) failed++;
        }
      }
    }
    return { total: tasks.length, enabled, ran, failed, deferred, next };
  });
  const taskNextLabel = $derived.by(() => {
    const n = taskAgg.next;
    if (!n) return '无启用任务';
    if (n.ts * 1000 - Date.now() < 60_000) return `即将（${n.title}）`;
    return `${fmtSched(new Date(n.ts * 1000).toISOString())}（${n.title}）`;
  });
  // 周报：查看=打开周报任务上次运行的对话；立即生成=run_task_now。
  async function genReportNow(taskId: string) {
    try { await invoke('run_task_now', { id: taskId }); say('周报生成中——完成后点「查看周报」打开对话'); }
    catch (e) { say(String(e), 'err'); }
  }
  // 「调整任务」：跳到定时任务视图并高亮该托管的配套任务（几秒后自动取消高亮）。
  let hlTaskIds = $state<string[]>([]);
  async function adjustTasks(m: ManagedSite) {
    hlTaskIds = [...m.task_ids];
    await openTasks();
    setTimeout(() => (hlTaskIds = []), 6000);
  }
  // 待审队列按语种分组（保持组内原有排序：最近更新在前）。
  function groupDrafts(ds: ManagedDraft[]): { lang: string; items: ManagedDraft[] }[] {
    const map = new Map<string, ManagedDraft[]>();
    for (const d of ds) {
      const k = d.lang || '';
      if (!map.has(k)) map.set(k, []);
      map.get(k)!.push(d);
    }
    return [...map.entries()].map(([lang, items]) => ({ lang, items }));
  }
  // 批量批准：逐条确认合并为一次确认，逐篇发布，失败不阻断其余。
  async function approveGroup(m: ManagedSite, items: ManagedDraft[]) {
    const yes = await confirmDialog(`批量发布这 ${items.length} 篇草稿？发布后立即公开可见（只改状态，其余字段不动）。`, { title: '批量批准发布', kind: 'warning' });
    if (!yes) return;
    let ok = 0; let firstFail = '';
    for (const d of items) {
      try { await invoke('managed_publish', { connId: m.conn_id, siteSlug: m.site_slug, id: d.id }); ok++; }
      catch (e) { if (!firstFail) firstFail = `《${d.title}》：${String(e)}`; }
    }
    if (firstFail) say(`已发布 ${ok}/${items.length} 篇；首个失败：${firstFail}`, 'err');
    else say(`已发布 ${ok} 篇`);
    void loadManagedDetail(m);
  }
  // 开启向导（3 步：选站 → 90 天计划 → 模型/等级/上限/预算与边界确认）
  let mwOpen = $state(false);
  let mwStep = $state(1);
  let mwSite = $state('');
  let mwPlan = $state('');
  let mwLimit = $state(3);
  let mwGenBusy = $state(false);
  let mwBusy = $state(false);
  // 配套任务的厂商/模型/强度（默认取启动器当前偏好）+ 等级 + 每周 token 预算（0=不限）。
  let mwBrain = $state<string>('claude');
  let mwModel = $state('sonnet');
  let mwEffort = $state('');
  let mwLevel = $state('l0');
  let mwBudget = $state(0);
  let mwEditLimit = $state(2);
  // 选站后的机械预检（软警示，不硬拦）：存量条数 + 统计可用性 → 警示列表。
  // 预检失败（网络/旧服务端等）静默降级：不警示也不拦——预检绝不能挡住向导。
  let mwWarns = $state<string[]>([]);
  let mwPrecheckSeq = 0; // 竞态防护：切换站点/重开向导只认最后一次结果
  async function runMwPrecheck(slug: string) {
    const seq = ++mwPrecheckSeq;
    mwWarns = [];
    try {
      const r = await invoke<{ published_count: number; stats: string; warnings: string[] }>('managed_precheck', { connId: activeConnId, siteSlug: slug });
      if (seq === mwPrecheckSeq) mwWarns = r.warnings;
    } catch { /* 预检失败：静默降级 */ }
  }
  $effect(() => { if (mwOpen && mwSite) void runMwPrecheck(mwSite); else { mwPrecheckSeq++; mwWarns = []; } });
  function openManagedWizard() {
    mwOpen = true; mwStep = 1; mwPlan = ''; mwLimit = 3; mwGenBusy = false; mwBusy = false;
    mwSite = sites.find((s) => !managedOfConn.some((m) => m.site_slug === s.slug))?.slug ?? '';
    mwBrain = brainUsable(prefs.brain) ? prefs.brain : firstUsableBrain();
    mwModel = mwBrain === prefs.brain && isLauncherModel(mwBrain, prefs.model) ? prefs.model : defaultModelFor(mwBrain);
    mwEffort = mwBrain === prefs.brain ? (prefs.effort ?? '') : '';
    mwLevel = 'l0'; mwBudget = 0; mwEditLimit = 2;
  }
  // 向导里换厂商后，档位不属于该厂商时回落默认（与任务表单同规则）。
  $effect(() => { if (mwOpen && !isLauncherModel(mwBrain, mwModel)) mwModel = defaultModelFor(mwBrain); });
  const mwSiteOpts = $derived(sites.map((s) => {
    const taken = managedOfConn.some((m) => m.site_slug === s.slug);
    return {
      value: s.slug, label: s.name || s.slug,
      sub: taken ? '已在托管中' : (s.url ? hostOf(s.url) : '未绑定域名'),
      img: s.favicon || s.logo || faviconGuess(s.url),
      disabled: taken,
    };
  }));
  const MW_PLAN_PROMPT = `请为本站生成一份 90 天内容运营计划（纯文本，将作为后续每日内容任务的方向依据）。
先读站点资料、导航与近期内容摸清定位，然后输出：
1) 站点定位与目标读者（两三句）；2) 3-5 个内容支柱（每个配 2-3 个具体选题方向）；
3) 每周更新节奏建议（频率/语种）；4) 8-12 个 SEO 关键词方向（每个方向注明目标搜索意图与判断依据——面向哪类读者、解决什么问题、为何判断本站有机会）；5) 前 4 周的选题清单（标题级）。
只输出计划本身，不要创建或修改任何内容。`;
  // 生成计划＝后台开一个一次性对话跑摸底 prompt（auto 档：读站点数据自动放行；prompt 明令只读）。
  // 拿最后一条助手消息填进可编辑 textarea；对话会留在侧栏可追溯。
  async function mwGenPlan() {
    if (!mwSite || mwGenBusy) return;
    if (!brainUsable(mwBrain as Brain)) { say('所选厂商未就绪，去设置里授权或换一个', 'err'); return; }
    mwGenBusy = true;
    const site = sites.find((s) => s.slug === mwSite);
    const id = crypto.randomUUID();
    try {
      const conv = await invoke<Conversation>('start_conversation', {
        convId: id, connId: activeConnId, siteSlug: mwSite, siteName: site?.name || mwSite,
        siteSlugs: [], siteNames: [], taskType: 'free', brain: mwBrain, model: mwModel,
        permMode: 'auto', effort: mwEffort, message: MW_PLAN_PROMPT, onEvent: makeChannel(id),
      });
      const last = [...conv.messages].reverse().find((x) => x.role === 'assistant' && !x.error && x.text.trim());
      if (last) mwPlan = last.text.trim();
      else say('没拿到计划文本——可在侧栏打开这条对话查看原因，或直接手写', 'err');
      await refreshConvos();
    } catch (e) { say(String(e), 'err'); }
    finally { mwGenBusy = false; }
  }
  async function mwEnable() {
    if (!mwSite || mwBusy) return;
    if (!brainUsable(mwBrain as Brain)) { say('所选厂商未就绪，去设置里授权或换一个', 'err'); return; }
    mwBusy = true;
    const site = sites.find((s) => s.slug === mwSite);
    try {
      await invoke('managed_enable', {
        connId: activeConnId, siteSlug: mwSite, siteName: site?.name || mwSite,
        plan: mwPlan, weeklyPostLimit: mwLimit, weeklyEditLimit: Math.min(20, Math.max(1, Math.round(Number(mwEditLimit) || 2))), level: mwLevel,
        tokenWeeklyBudget: Math.max(0, Math.round(Number(mwBudget) || 0)),
        brain: mwBrain, model: mwModel, effort: mwEffort,
      });
      say('托管已开启：每日内容 + 每周审计 + 每周周报三个任务已创建');
      mwOpen = false; await loadManaged(); await loadTasks();
    } catch (e) { say(String(e), 'err'); }
    finally { mwBusy = false; }
  }

  interface TaskForm {
    id: string | null; connId: string; connName: string; siteSlugs: string[]; taskType: string; brain: string; model: string; effort: string;
    title: string; prompt: string; period: string; firstRun: string; enabled: boolean;
  }
  let taskModalOpen = $state(false);
  /** 正在查看运行记录的任务（null=关闭） */
  let taskHistoryFor = $state<ScheduledTask | null>(null);
  let tf = $state<TaskForm>(freshTaskForm());
  // 已从对话提议卡成功创建过的定时任务（按内容 key，持久化）→ 卡片显示「已创建」防重复点。
  let createdProposals = $state(loadCreatedProposals());
  let pendingProposalKey = $state('');
  function proposalKey(p: TaskProposal): string { return `${p.title}|${p.prompt}|${p.every_minutes}|${p.first_run}`; }
  function loadCreatedProposals(): Set<string> { try { return new Set<string>(JSON.parse(localStorage.getItem('gcms.pilot.createdProposals') || '[]')); } catch { return new Set(); } }
  function markProposalCreated(key: string) { if (!key) return; const n = new Set(createdProposals); n.add(key); createdProposals = n; try { localStorage.setItem('gcms.pilot.createdProposals', JSON.stringify([...n])); } catch { /* */ } }
  function freshTaskForm(): TaskForm {
    const brain = firstUsableBrain();
    return {
      id: null, connId: activeConnId, connName: activeConn?.name ?? '', siteSlugs: sites[0] ? [sites[0].slug] : [], taskType: 'article',
      brain, model: defaultModelFor(brain), effort: '',
      title: '', prompt: '', period: '1440', firstRun: '', enabled: true,
    };
  }
  function openNewTask() { pendingProposalKey = ''; tf = freshTaskForm(); taskModalOpen = true; }
  // AI 在对话里提议的定时任务 → 用当前会话的站点/模型预填，弹确认卡让用户确认/微调。
  function openTaskFromProposal(p: TaskProposal) {
    const c = activeConv;
    if (!c) return;
    pendingProposalKey = proposalKey(p);
    const firstRunSecs = p.first_run && !isNaN(new Date(p.first_run).getTime()) ? Math.floor(new Date(p.first_run).getTime() / 1000) : 0;
    tf = {
      id: null, connId: c.conn_id, connName: c.conn_name, siteSlugs: c.site_slugs?.length ? [...c.site_slugs] : (c.site_slug ? [c.site_slug] : []),
      taskType: c.task_type === 'free' ? 'free' : 'article', brain: c.brain, model: c.model, effort: c.effort || '',
      title: p.title, prompt: p.prompt, period: String(p.every_minutes || 1440),
      firstRun: firstRunSecs ? toLocalInput(firstRunSecs) : '', enabled: true,
    };
    taskModalOpen = true;
  }
  function openEditTask(t: ScheduledTask) {
    pendingProposalKey = '';
    // 编辑保留任务原本所属的连接，绝不改绑到当前活动连接。
    tf = {
      id: t.id, connId: t.conn_id, connName: t.conn_name, siteSlugs: t.site_slugs?.length ? [...t.site_slugs] : [t.site_slug],
      taskType: t.task_type, brain: t.brain, model: t.model, effort: t.effort || '',
      title: t.title, prompt: t.prompt, period: String(t.interval_minutes),
      firstRun: t.next_run ? toLocalInput(t.next_run) : '', enabled: t.enabled,
    };
    taskModalOpen = true;
  }
  // 可添加的站点选项：编辑跨连接任务时，活动连接的 discovery 里可能没有它的站点，
  // 已选中的站点从候选里去掉（用下方胶囊管理）。
  const taskSiteOpts = $derived.by(() => {
    const base = tf.connId === activeConnId ? siteOpts : [];
    const extras = tf.siteSlugs
      .filter((s) => !base.some((o) => o.value === s))
      .map((s) => ({ value: s, label: s, sub: '当前' }));
    return [...extras, ...base].filter((o) => !tf.siteSlugs.includes(o.value));
  });
  // 多选：下拉选一个就加进胶囊列表（value 始终回置空串）
  let sitePick = $state('');
  function addTaskSite(v: string) {
    if (v && !tf.siteSlugs.includes(v)) tf.siteSlugs = [...tf.siteSlugs, v];
    sitePick = '';
  }
  function removeTaskSite(v: string) {
    tf.siteSlugs = tf.siteSlugs.filter((s) => s !== v);
  }
  function taskSiteName(slug: string): string {
    return sites.find((s) => s.slug === slug)?.name || slug;
  }
  function toLocalInput(secs: number): string {
    const d = new Date(secs * 1000); const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  }
  async function saveTask() {
    if (!tf.siteSlugs.length || !tf.prompt.trim()) { say('请选择站点并填写指令', 'err'); return; }
    if (!brainUsable(tf.brain as Brain)) { say('所选模型未就绪，去设置里授权', 'err'); return; }
    // 新建用当前活动连接；编辑保留任务原连接。
    const connId = tf.connId || activeConnId;
    const siteNames = tf.siteSlugs.map((s) => (tf.connId === activeConnId ? taskSiteName(s) : s));
    const firstRun = tf.firstRun ? Math.floor(new Date(tf.firstRun).getTime() / 1000) : 0;
    try {
      await invoke('save_task', {
        id: tf.id, connId, siteSlugs: tf.siteSlugs, siteNames,
        taskType: tf.taskType, brain: tf.brain, model: tf.model, effort: tf.effort,
        title: tf.title, prompt: tf.prompt, intervalMinutes: parseInt(tf.period) || 1440, firstRun, enabled: tf.enabled,
      });
      if (pendingProposalKey) { markProposalCreated(pendingProposalKey); pendingProposalKey = ''; }
      taskModalOpen = false; await loadTasks();
    } catch (e) { say(String(e), 'err'); }
  }
  async function toggleTask(t: ScheduledTask) { try { await invoke('set_task_enabled', { id: t.id, enabled: !t.enabled }); await loadTasks(); } catch (e) { say(String(e), 'err'); } }
  async function deleteTask(t: ScheduledTask) {
    const yes = await confirmDialog(`删除定时任务「${t.title}」？`, { title: '删除任务', kind: 'warning' });
    if (!yes) return;
    try { await invoke('delete_task', { id: t.id }); await loadTasks(); } catch (e) { say(String(e), 'err'); }
  }
  async function runTaskNow(t: ScheduledTask) {
    try { await invoke('run_task_now', { id: t.id }); say('已手动触发，稍后在对话历史里查看结果'); setTimeout(loadTasks, 1500); }
    catch (e) { say(String(e), 'err'); }
  }
  function periodLabel(min: number): string {
    if (min % 10080 === 0) return `每 ${min / 10080} 周`;
    if (min % 1440 === 0) return min === 1440 ? '每天' : `每 ${min / 1440} 天`;
    if (min % 60 === 0) return min === 60 ? '每小时' : `每 ${min / 60} 小时`;
    return `每 ${min} 分钟`;
  }
  const periodOpts = [
    { value: '60', label: '每小时' }, { value: '360', label: '每 6 小时' }, { value: '720', label: '每 12 小时' },
    { value: '1440', label: '每天' }, { value: '2880', label: '每 2 天' }, { value: '10080', label: '每周' },
  ];
  const taskTypeOpts = [
    { value: 'article', label: '内容创作', sub: '写文章' }, { value: 'free', label: '自由', sub: '任意指令' },
  ];

  // ---------- launcher form ----------
  let prefs = $state(loadPrefs());
  let lSite = $state('');
  // Cloudflare 建站：连接是 CF 时，lSite 复用为「项目名」，站点选择器换成项目输入。
  const isCfConn = $derived(activeConn?.kind === 'cloudflare');
  let cfProjects = $state<string[]>([]);
  const activeConvIsCf = $derived(!!activeConv && conns.find((c) => c.id === activeConv!.conn_id)?.kind === 'cloudflare');
  let previewBusy = $state(false);
  let cfReady = $state(false); // 项目是否已建出可预览/部署的内容（否则预览/部署置灰）
  async function checkCfReady() {
    if (!activeConvIsCf || !activeConv) { cfReady = false; return; }
    try { cfReady = await invoke<boolean>('cf_project_ready', { connId: activeConv.conn_id, project: activeConv.site_slug }); }
    catch { cfReady = false; }
  }
  /** 从预览 URL 里取 ":端口" 给提示用（端口是后端挑的，不再恒等于 8788）。 */
  function portOf(url: string): string {
    try { return ':' + new URL(url).port; } catch { return url; }
  }
  async function startPreview() {
    if (!activeConv || previewBusy) return;
    previewBusy = true;
    say('正在启动本地预览…（起来就开窗）');
    try {
      // 端口是后端挑的空端口（8788 被别的程序占着时会自动往上让），所以别写死——用它返回的真 URL。
      const u = await invoke<string>('cf_preview_start', { connId: activeConv.conn_id, project: activeConv.site_slug });
      say(`本地预览已打开（${portOf(u)}）·关掉预览窗即停止`);
    } catch (e) { say(String(e), 'err'); }
    finally { previewBusy = false; }
  }
  let wrInstalling = $state(false);
  let wrElapsed = $state(0); // 安装 wrangler 已用时（毫秒），给个"在动"的实时反馈
  let wrTimer: ReturnType<typeof setInterval> | undefined;
  async function installWrangler() {
    if (wrInstalling) return;
    wrInstalling = true; wrElapsed = 0;
    const t0 = Date.now();
    wrTimer = setInterval(() => { wrElapsed = Date.now() - t0; }, 500);
    try { const m = await invoke<string>('install_wrangler'); say(m); await refreshBrainsManual(); }
    catch (e) { say(String(e), 'err'); }
    finally { wrInstalling = false; if (wrTimer) { clearInterval(wrTimer); wrTimer = undefined; } }
  }
  // Claude Code 用官方原生安装器（独立二进制，不需要 Node/npm）——npm 命令对新手是三重门槛。
  // 注意：不能引用 isWindows（声明在后面，TDZ），这里自足判断。
  const CLAUDE_INSTALL_CMD = typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent)
    ? 'irm https://claude.ai/install.ps1 | iex'
    : 'curl -fsSL https://claude.ai/install.sh | sh';
  // Grok 官方脚本（独立二进制，不需要 Node）；Windows 官方要求 Git-Bash/MSYS2 跑同一条命令。
  const GROK_INSTALL_CMD = 'curl -fsSL https://x.ai/cli/install.sh | bash';
  let claudeInstalling = $state(false);
  let claudeElapsed = $state(0);
  let claudeTimer: ReturnType<typeof setInterval> | undefined;
  let codexInstalling = $state(false);
  let codexElapsed = $state(0);
  let codexTimer: ReturnType<typeof setInterval> | undefined;
  async function installCodex() {
    if (codexInstalling) return;
    codexInstalling = true; codexElapsed = 0;
    const t0 = Date.now();
    codexTimer = setInterval(() => { codexElapsed = Date.now() - t0; }, 500);
    try { const m = await invoke<string>('install_codex'); say(m); await refreshBrainsManual(); }
    catch (e) { say(String(e), 'err', 20000); }
    finally { codexInstalling = false; nodeBoot = ''; if (codexTimer) { clearInterval(codexTimer); codexTimer = undefined; } }
  }
  let grokInstalling = $state(false);
  let grokElapsed = $state(0);
  let grokTimer: ReturnType<typeof setInterval> | undefined;
  async function installGrok() {
    if (grokInstalling) return;
    grokInstalling = true; grokElapsed = 0;
    const t0 = Date.now();
    grokTimer = setInterval(() => { grokElapsed = Date.now() - t0; }, 500);
    try { const m = await invoke<string>('install_grok'); say(m); await refreshBrainsManual(); }
    catch (e) { say(String(e), 'err', 20000); }
    finally { grokInstalling = false; nodeBoot = ''; if (grokTimer) { clearInterval(grokTimer); grokTimer = undefined; } }
  }
  // 托管 Node 自举进度（后端 "node-boot" 事件）：一键安装按钮上分步显示。
  let nodeBoot = $state('');
  $effect(() => {
    const un = listen<{ phase: string; pct: number }>('node-boot', (e) => {
      const p = e.payload;
      nodeBoot = p.phase === 'download' ? `下载 Node ${p.pct}%`
        : p.phase === 'extract' ? '解压 Node…'
        : p.phase === 'verify' ? '校验 Node…'
        : p.phase === 'grok-download' ? `下载 Grok ${p.pct}%`
        : p.phase === 'grok-verify' ? '校验 Grok…'
        : '安装 CLI…';
    });
    return () => { void un.then((f) => f()); };
  });
  async function installClaude() {
    if (claudeInstalling) return;
    claudeInstalling = true; claudeElapsed = 0;
    const t0 = Date.now();
    claudeTimer = setInterval(() => { claudeElapsed = Date.now() - t0; }, 500);
    try { const m = await invoke<string>('install_claude'); say(m); await refreshBrainsManual(); }
    catch (e) { say(String(e), 'err', 20000); } // 详情含输出末尾，多留时间读/滚动
    finally { claudeInstalling = false; nodeBoot = ''; if (claudeTimer) { clearInterval(claudeTimer); claudeTimer = undefined; } }
  }
  function fillDeploy() {
    if (viewBusy) return;
    // 预填一条部署指令，用户可补上域名后发送；真正的 wrangler deploy 会经权限确认。
    draft = '把当前项目构建并部署到 Cloudflare Pages。若要绑定自定义域名，用这个域名：';
  }
  let lTask = $state<TaskType>(prefs.taskType);
  let lBrain = $state<Brain>(prefs.brain);
  let lModel = $state(prefs.model);
  let lPerm = $state<string>(prefs.perm ?? 'full');
  let lEffort = $state<string>(prefs.effort ?? '');
  const permOpts = [
    { value: 'plan', label: '计划', sub: '只读 · 只给方案' },
    { value: 'ask', label: '询问', sub: '每步都要你批准' },
    { value: 'auto', label: '自动', sub: '仅危险动作确认' },
    { value: 'full', label: '全自动', sub: '全程不拦' },
  ];
  // CF 建站「视觉风格」预设：每档是一组具体 design tokens 指令，拼进首条消息，
  // 把"语言欠定视觉"变成"点一下锁定方向"。空 = 让 AI 按内容自行判断。
  const STYLE_OPTS = [
    { value: '', label: '风格：让 AI 定', sub: '按站点内容自行判断' },
    { value: 'minimal', label: '极简留白', sub: '黑白灰 · 大留白' },
    { value: 'editorial', label: '杂志编辑', sub: '衬线标题 · 窄栏长文' },
    { value: 'dark-tech', label: '深色科技', sub: '近黑底 · 霓虹点缀' },
    { value: 'warm-craft', label: '暖色手作', sub: '奶油底 · 陶土强调' },
    { value: 'saas', label: '企业 SaaS', sub: '浅底 · 卡片层次' },
  ];
  const STYLE_DIRECTIVES: Record<string, string> = {
    minimal: '极简留白：纯白底、近黑文字，仅一个低饱和强调色；无衬线字体；区块间大留白、细分割线；圆角 8px；无花哨动效。',
    editorial: '杂志编辑风：米白底；衬线大标题 + 无衬线正文；正文窄栏（约 720px）居中；标题字号大胆、段落节奏松；用细横线分节。',
    'dark-tech': '深色科技：近黑背景（如 #0b0e14）、高对比浅色文字；一个霓虹强调色（青或紫）；数据/代码用等宽字体点缀；小圆角或直角；hover 微发光。',
    'warm-craft': '暖色手作：奶油白/米色底、深棕文字、陶土或暖橙强调色；12px 圆润圆角；人文气质字体；阴影温和；整体温暖安静。',
    saas: '企业 SaaS：浅灰白底、品牌蓝或紫强调；卡片 + 轻阴影分层；间距体系化、信息密度适中；CTA 按钮醒目；配 FAQ/价格等标准分区样式。',
  };
  let lStyle = $state('');

  // 启动页「厂商+模型」合并为一个下拉：行带厂商图标，值编码为 "<brain>::<model>"。
  // 厂商未就绪时整组置灰（原厂商下拉的禁用语义保留）。
  const comboOpts = $derived.by(() => {
    const out: { value: string; label: string; sub?: string; icon?: string; disabled?: boolean }[] = [];
    for (const b of ALL_BRAINS) {
      const usable = brainUsable(b);
      const note = isCodexSsh(b, isSshConn) ? CODEX_SSH_NOTE : usable ? '' : brains?.[b].found ? '未登录' : '未安装';
      for (const m of launcherModelOpts(b)) {
        out.push({ value: `${b}::${m.value}`, label: m.label, sub: note || m.sub, icon: b, disabled: !usable });
      }
    }
    return out;
  });
  // Codex + 远程连接（用户拍板放开）：能用，但闸是**打折**的 —— 走 AI 桥的远程命令照样逐条弹卡
  // （闸在 bridge.rs，与厂商无关），可它无头下没有逐工具闸（claude 有 PreToolUse 钩子、grok 有 ACP
  // 权限回调），拦不住它绕开桥、直接用系统 ssh + 用户自己的 ~/.ssh 密钥打同一台机器。
  // 所以这里不置灰、只挂这句注记 —— 完整说法在权限档位的 tip 里（permTipFor）。
  // ★ 这句话必须**自足**：它长在模型下拉里，别再写「见下方警告」——下面那个红框已经撤了，
  //   指过去就是指向空气（这坑踩过一次）。
  const CODEX_SSH_NOTE = '拦不住它绕开 Pilot 的确认卡';
  function isCodexSsh(b: string, ssh: boolean): boolean { return ssh && b === 'codex'; }
  function pickCombo(v: string) {
    const i = v.indexOf('::');
    if (i < 0) return;
    lBrain = v.slice(0, i) as Brain;
    lModel = v.slice(i + 2);
  }

  // 权限档位按风险给下拉文字上色：自动=警告色、全自动(全程不拦)=危险色，计划/询问保持中性。
  function permTone(v: string): string { return v === 'full' ? 'danger' : v === 'auto' ? 'warn' : ''; }
  // Codex 没有逐命令确认（询问/自动的弹卡是 Claude 的 PreToolUse 钩子）——这两档对它无意义，
  // 下拉里直接置灰，省掉那行说明。Codex 实际就两档能用：计划(只读) / 全自动(建站要联网，跑得动)。
  // **远程连接例外**：那里的命令闸在 Pilot 里（AI 桥，见 bridge.rs），不依赖厂商的钩子能力，
  // 所以 codex 一样能逐条确认远程命令 —— 这两档对它有意义，不置灰。
  // 远程连接下每个档位的长说明（挂 data-tip，Dropdown 会把它交给全局 .tipbox；\n 会保留）。
  // ★ 这些话原来是 composer 下面的常驻框，现在长在档位本身上 —— 那才是**决策点**：
  // 原来的写法全都 reactive 于 perm==='full'，等于用户已经把闸拨到底了才弹出来说「这样很危险」，
  // 亡羊补牢。挂在选项上，用户在**拨之前**就读得到；挂在触发器上，选完悬停还能复查。
  function permTipFor(brain: string, perm: string, ssh = false): string {
    if (!ssh) return '';
    const codex = brain === 'codex';
    if (perm === 'full') {
      return codex
        ? 'Codex ＋「全自动」＝ 没有任何确认。\n「全自动」档下 Pilot 的确认卡本来就全关了，而 Codex 无头模式自己也没有逐命令闸——它在这台机器上删数据、改配置、重启都不会问你。\n它还能绕开 Pilot，直接用你本机的 ssh 和 ~/.ssh 密钥连上来。\n除非你非常清楚要这样，否则退回「自动」。'
        : '「全自动」档下，AI 在这台机器上跑命令不会问你——包括删数据、改配置、重启。\n除非你清楚要这样，否则用「自动」。';
    }
    if (perm === 'ask' || perm === 'auto') {
      return codex
        ? 'Codex 的确认卡是打折的。\n它经 Pilot 跑的每条远程命令仍会弹卡等你确认；但 Codex 无头模式没有逐命令闸，它可以绕开 Pilot、直接用你本机的 ssh 和 ~/.ssh 密钥连这台机器——那条路一张卡都不弹。\n要每条命令都真正过你的眼，用 Claude 或 Grok。'
        : '远程命令的闸在 Pilot 里（与厂商无关）：每条要在这台机器上跑的命令都会弹卡等你确认。';
    }
    // plan 档在 ssh 下：bridge.rs 的 gate() 对 Plan 一律 Err —— 连只读命令都不跑，别写「只读远端」。
    if (perm === 'plan') return '只出方案、不碰这台机器：计划档下 AI 一条远程命令都不会跑（连只读的也不跑）。';
    return '';
  }
  function permOptsFor(brain: string, ssh = false) {
    if (ssh) {
      // 远程连接下「询问/自动」对三家**都有意义**：远程命令的闸在 Pilot 的 AI 桥里（bridge.rs），
      // 与厂商无关 —— 所以这里不像下面那样把 codex 的这两档灰掉。
      // 但对 codex 要说实话：它能绕开桥（见 permTipFor），所以副标题不能许「每条都确认」。
      const codex = brain === 'codex';
      return permOpts.map((o) => {
        const tip = permTipFor(brain, o.value, true);
        return o.value === 'auto' ? { ...o, tip, sub: codex ? '经 Pilot 的远程命令要确认' : '本地放行 · 每条远程命令都确认' }
        : o.value === 'ask' ? { ...o, tip, sub: codex ? '经 Pilot 的远程命令要确认' : o.sub }
        // ★ 全自动的副标题就是那句警告的**短版**（红字），长版在 tip 里。codex 这档一道闸都没有。
        : o.value === 'full' ? { ...o, tip, tone: 'danger', sub: codex ? 'Codex：一张确认卡都没有' : '连远程命令也不拦' }
        : { ...o, tip };
      });
    }
    if (brain !== 'codex') return permOpts;
    return permOpts.map((o) => (o.value === 'ask' || o.value === 'auto') ? { ...o, disabled: true, sub: 'Codex 不支持逐命令确认' } : o);
  }
  // 选中的档位若在 Codex 下不可用，落到「全自动」（保持已选值始终合法，不显示一个灰着的当前项）。
  // 远程连接不做这个降级：见上，那里 codex 的询问/自动是真能用的（降级会把真机推进无人确认的全自动）。
  $effect(() => { if (lBrain === 'codex' && !isSshConn && (lPerm === 'ask' || lPerm === 'auto')) lPerm = 'full'; });
  // 待批工具调用：询问/自动档下钩子把请求写在后端 pending 目录，UI 轮询渲染批准卡。
  type Permit = { id: string; conv: string; tool: string; cmd: string; desc: string; arg: string; dangerous: boolean; mode: string; ts: number };
  // 批准卡标题：优先用模型自己写的操作说明（Bash 的 description），没有就按工具合成一句人话。
  function permitDesc(p: Permit): string {
    if (p.desc) return p.desc;
    if (p.tool === 'WebFetch') return p.arg ? `抓取网页：${p.arg}` : '抓取一个网页';
    if (p.tool === 'WebSearch') return p.arg ? `联网搜索：${p.arg}` : '联网搜索';
    if (p.tool === 'Write' || p.tool === 'Edit' || p.tool === 'NotebookEdit') return p.arg ? `写入文件：${p.arg}` : '写入文件';
    if (p.tool === 'SSH') return p.arg ? `在 ${p.arg} 上执行命令` : '在远程服务器上执行命令';
    if (p.tool === 'Bash') return '运行一条命令';
    return `使用工具 ${p.tool}`;
  }
  // 命令一律默认收起（说明才是给人看的重点），点「查看具体命令」再展开。
  let permitCmdOpen = $state(new Set<string>());
  function togglePermitCmd(id: string) { const s = new Set(permitCmdOpen); if (!s.delete(id)) s.add(id); permitCmdOpen = s; }
  // 已应答过的请求 id：应答后钩子最多要 300ms 才删掉 .req.json，而轮询每 600ms 重扫目录，
  // 扫描落进窗口会让刚点掉的卡"复活"一次（看起来像连续弹两个）。id 不复用，永久过滤即可。
  const respondedPermits = new Set<string>();
  let pendingPermits = $state<Permit[]>([]);
  const activePermits = $derived(pendingPermits.filter((p) => p.conv === activeConvId));
  const permConvs = $derived(new Set(pendingPermits.map((p) => p.conv))); // 哪些对话有待批（含后台的，侧栏标出来）
  $effect(() => { if (activePermits.length) scrollSoon(true); }); // 待批卡一出现就滚到它，不用手动翻
  async function respondPermit(id: string, allow: boolean) {
    respondedPermits.add(id);
    pendingPermits = pendingPermits.filter((p) => p.id !== id); // 乐观移除
    try { await invoke('respond_permit', { id, allow }); } catch (e) { say(String(e), 'err'); }
  }
  $effect(() => {
    const t = setInterval(async () => {
      if (!Object.keys(running).length) { if (pendingPermits.length) pendingPermits = []; return; }
      try { pendingPermits = (await invoke<Permit[]>('list_pending_permits')).filter((p) => !respondedPermits.has(p.id)); } catch { /* */ }
    }, 600);
    return () => clearInterval(t);
  });
  let lDraft = $state('');
  // 全局自定义模型 ID（按厂商，可多个，在「连接与模型」里增删）；作为该厂商模型下拉的附加档位。
  let customDraft = $state<Record<string, string>>({ claude: '', codex: '', grok: '' });
  let customOpen = $state<Record<string, boolean>>({ claude: false, codex: false, grok: false });
  function customsOf(b: string): string[] { return (b === 'codex' ? prefs.customCodexIds : b === 'grok' ? prefs.customGrokIds : prefs.customClaudeIds) ?? []; }
  function addCustom(b: string) {
    const v = (customDraft[b] ?? '').trim();
    if (!v) return;
    const arr = customsOf(b);
    if (!arr.includes(v)) { arr.push(v); savePrefs(prefs); }
    customDraft[b] = '';
  }
  function removeCustom(b: string, id: string) {
    const arr = customsOf(b);
    const i = arr.indexOf(id);
    if (i < 0) return;
    arr.splice(i, 1);
    savePrefs(prefs);
    if (lBrain === b && lModel === id) lModel = defaultModelFor(b);
    if (tf.brain === b && tf.model === id) tf.model = defaultModelFor(b);
  }
  // 切换引擎后，若当前档位不属于该引擎（launcher 含自定义），重置为该引擎默认档位（避免下拉空选/发错模型）。
  $effect(() => { if (!isLauncherModel(lBrain, lModel)) lModel = defaultModelFor(lBrain); });
  // launcher 全集（含已保存的自定义 ID）都算合法——任务模型下拉与对话一致后不再有单独的自定义输入框
  $effect(() => { if (!isLauncherModel(tf.brain, tf.model)) tf.model = defaultModelFor(tf.brain); });
  // 首次识别出本地 CLI 后：若默认厂商不可用（只装/登录了另一个），把 composer 默认厂商切到可用的那个；之后尊重手动选择。
  let brainAutoSet = false;
  $effect(() => {
    if (!brains || brainAutoSet) return;
    brainAutoSet = true;
    if (!brainUsable(lBrain)) lBrain = brains && ALL_BRAINS.some((b) => brainUsable(b)) ? firstUsableBrain() : lBrain;
  });
  // 自定义模型输入框的占位示例，按当前引擎给不同提示。
  // ---------- composer 自动撑高 ----------
  // 有内容随行数长高，到上限后固定内部滚动；清空/发送后回到初始高度（对标 Claude Code）。
  function autogrow(node: HTMLTextAreaElement, max = 220) {
    const min = node.offsetHeight; // 以挂载时的初始高度为下限（启动器 3 行、会话 1 行各自保形）
    const resize = () => {
      node.style.height = 'auto';
      const h = Math.max(min, Math.min(node.scrollHeight, max));
      node.style.height = h + 'px';
      node.style.overflowY = node.scrollHeight > max ? 'auto' : 'hidden';
    };
    node.addEventListener('input', resize);
    requestAnimationFrame(resize);
    (node as unknown as { __autogrow?: () => void }).__autogrow = resize;
    return { destroy: () => node.removeEventListener('input', resize) };
  }
  let draftEl = $state<HTMLTextAreaElement | undefined>();
  let lDraftEl = $state<HTMLTextAreaElement | undefined>();
  // 程序化清空（发送/排队/切会话）不触发 input 事件，这里补一脚
  $effect(() => { void draft; const el = draftEl as unknown as { __autogrow?: () => void } | undefined; el?.__autogrow?.(); });
  $effect(() => { void lDraft; const el = lDraftEl as unknown as { __autogrow?: () => void } | undefined; el?.__autogrow?.(); });

  // ---------- composer / live turn ----------
  let draft = $state('');
  // 并发对话：running = convId → connId（在跑的对话；带 connId 便于删连接时可靠判定）；lives = 每个对话的流式缓冲。
  let running = $state<Record<string, string>>({});
  let lives = $state<Record<string, { text: string; tools: ToolCall[]; error: string; failed: boolean; startedAt: number }>>({});
  let autoRetried = $state<Record<string, boolean>>({}); // convId → 本轮已自动重试过（每个用户轮只自动重试一次）
  let retryTimers: Record<string, ReturnType<typeof setTimeout>> = {}; // 待触发的自动重连定时器（新一轮开始时取消，防陈旧触发）
  // 智能升级：手动重试后若以同样的错误再次失败，说明 resume 救不了 → 之后只显示「重建继续」。
  let retryExhausted = $state<Record<string, boolean>>({});
  let threadEl = $state<HTMLDivElement | null>(null);

  const viewBusy = $derived(!!running[activeConvId]); // 当前查看的对话是否在跑
  const liveView = $derived(lives[activeConvId]); // 当前对话的流式缓冲（可能 undefined）

  // 加载中的"耗时"计时：仅在当前对话跑着时开一个轻量心跳刷新 nowMs，停了自动清掉。
  let nowMs = $state(0);
  $effect(() => {
    if (!viewBusy) return;
    nowMs = Date.now();
    const t = setInterval(() => { nowMs = Date.now(); }, 500);
    return () => clearInterval(t);
  });
  function elapsedLabel(ms: number): string {
    const s = Math.max(0, Math.floor(ms / 1000));
    return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`;
  }
  // 会话大小/用量：上下文按「厂商 + 模型」估算——codex 272k；grok-4.5 500k（ACP initialize
  // 的 totalContextTokens 实测值）；claude 按模型细分：fable ＝ Max 专属档、窗口 1M，
  // sonnet/opus/haiku/自定义保持 200k 保守默认（Pro 档就是 200k，绝不虚报）。
  function ctxLimit(brain: string, model = ''): number {
    if (brain === 'codex') return 272000;
    if (brain === 'grok') return 500000;
    return model.toLowerCase().includes('fable') ? 1_000_000 : 200_000;
  }
  // 实测自适应升档（证据驱动，只升不降）：实测 ctx 已超过假定上限的 95%，说明真实窗口更大
  //（如 Max 订阅跑 Sonnet 实际 1M），按下一档 1M 计；展示百分比由调用方钳 100%。
  function ctxLimitAdaptive(brain: string, model: string, ctx: number): number {
    const base = ctxLimit(brain, model);
    return base < 1_000_000 && ctx > base * 0.95 ? 1_000_000 : base;
  }
  // 拖动窗口：忽略交互元素（按钮/输入等），否则点它们会误触发拖动。
  /** 页头「抬升」：内容真滑到它底下时才淡入阴影、把那条线淡掉（见 .thread-head 的注释）。
   *
   *  ★ 一处监听管全部视图：scroll 事件**不冒泡、但会捕获**，所以在 document 上抓一次就能收到
   *  任意视图里 .thread 的滚动 —— 不必逐视图挂钩子（7 个页头 / 6 个 .thread 分散在同一条
   *  {#if} 链里，同一时刻只挂一个）。判定靠**结构约定**（.thread 的前一个兄弟是 .thread-head），
   *  所以新增视图照现有写法写就自动继承，不用记得注册。
   *  写歪了（比如把 .thread 包了一层）→ 取不到页头 → 直接 no-op，退回今天的样子：
   *  是「没变好」，不是「坏了」—— 这个退化方向是选它而不选纯 CSS 方案的关键。
   *
   *  迟滞（4 上 / 1 下）不是洁癖：惯性滚动和触控板会在 0~1px 抖，用 >0 判定会让阴影反复
   *  淡入淡出，看着就是闪。 */
  $effect(() => {
    const onScroll = (e: Event) => {
      const el = e.target as HTMLElement | null;
      if (!el?.classList?.contains('thread')) return;
      const head = el.previousElementSibling; // 注释节点不算元素，convPane 那种 {@render} 锚点跳得过去
      if (!head?.classList.contains('thread-head')) return;
      const up = head.classList.contains('elevated');
      head.classList.toggle('elevated', el.scrollTop > (up ? 1 : 4));
    };
    document.addEventListener('scroll', onScroll, true); // capture：scroll 不冒泡
    return () => document.removeEventListener('scroll', onScroll, true);
  });

  function startDrag(e: MouseEvent) {
    if (e.button !== 0) return;
    const t = e.target as HTMLElement;
    if (t.closest('button, a, input, textarea, select, [role="button"], [data-no-drag]')) return;
    getCurrentWindow().startDragging().catch(() => {});
  }

  // Windows 无 macOS 红绿灯（窗口控件在右侧），顶部工具按钮改靠左对齐，别飘在中间。
  const isWindows = typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent);
  /** 密钥存储的平台名称（文案用）：mac=钥匙串，win=凭据管理器。 */
  const keystoreName = isWindows ? 'Windows 凭据管理器' : 'macOS 钥匙串';
  // 全屏时无红绿灯，顶部工具按钮改与左栏菜单左对齐。
  let isFullscreen = $state(false);
  $effect(() => {
    const win = getCurrentWindow();
    let unlisten: (() => void) | undefined;
    const sync = async () => { try { isFullscreen = await win.isFullscreen(); } catch { /* */ } };
    sync();
    win.onResized(sync).then((u) => (unlisten = u)).catch(() => {});
    return () => unlisten?.();
  });

  // 侧栏折叠 + 可拖拽宽度（持久化）。
  let railCollapsed = $state(loadRailFlag());
  /** 会话列表已滚动（不在顶部）——导航与列表之间显示分界线。 */
  let convosScrolled = $state(false);
  let railWidth = $state(loadRailWidth());
  function loadRailFlag(): boolean { try { return localStorage.getItem('gcms.pilot.railCollapsed') === '1'; } catch { return false; } }
  function loadRailWidth(): number { try { const n = parseInt(localStorage.getItem('gcms.pilot.railWidth') || ''); return n >= 190 && n <= 460 ? n : 240; } catch { return 240; } }
  function toggleRail() { railCollapsed = !railCollapsed; try { localStorage.setItem('gcms.pilot.railCollapsed', railCollapsed ? '1' : '0'); } catch { /* */ } }
  function startResize(e: MouseEvent) {
    e.preventDefault();
    const onMove = (ev: MouseEvent) => { railWidth = Math.min(460, Math.max(190, Math.round(ev.clientX))); };
    const onUp = () => {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      document.body.style.cursor = '';
      try { localStorage.setItem('gcms.pilot.railWidth', String(railWidth)); } catch { /* */ }
    };
    document.body.style.cursor = 'col-resize';
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  }

  // 全局搜索（分区）：视图导航 / 定时任务 / 托管站点 / 会话 / 排期查找动作项。
  let searchOpen = $state(false);
  let searchQ = $state('');
  let searchInput = $state<HTMLInputElement | null>(null);
  function openSearch() { searchOpen = true; searchQ = ''; searchIdx = 0; void ensureTemplateMeta(); requestAnimationFrame(() => searchInput?.focus()); }
  // 会话匹配（原有逻辑保留）：按标题 / 站点名 / slug / 域名。
  const searchResults = $derived.by(() => {
    const q = searchQ.trim().toLowerCase();
    const list = convos.filter((c) => {
      if (c.conn_id !== activeConnId) return false; // 只搜当前连接下的会话，别把别的连接（gcms）串进来
      if (!q) return true;
      const site = sites.find((s) => s.slug === c.site_slug);
      const host = site?.url ? hostOf(site.url).toLowerCase() : '';
      return (c.title || '').toLowerCase().includes(q)
        || (c.site_name || '').toLowerCase().includes(q)
        || (c.site_slug || '').toLowerCase().includes(q)
        || host.includes(q);
    });
    return list.slice(0, 60);
  });
  type SearchEntry =
    | { kind: 'view'; view: 'schedule' | 'tasks' | 'managed' | 'templates'; label: string }
    | { kind: 'task'; t: ScheduledTask }
    | { kind: 'managed'; m: ManagedSite }
    | { kind: 'conv'; c: Conversation }
    | { kind: 'template'; t: Template }
    | { kind: 'conn'; c: Connection }
    | { kind: 'sched-find'; q: string };
  const SEARCH_VIEWS: { view: 'schedule' | 'tasks' | 'managed' | 'templates'; label: string }[] = [
    { view: 'schedule', label: '排期' },
    { view: 'tasks', label: '定时任务' },
    { view: 'managed', label: '托管' },
    { view: 'templates', label: '模板库' },
  ];
  /** 分区结果（按匹配数动态显隐）：空查询只给三个视图导航项。 */
  const searchSections = $derived.by(() => {
    const q = searchQ.trim().toLowerCase();
    const out: { title: string; entries: SearchEntry[] }[] = [];
    const views = SEARCH_VIEWS
      // 模板库只有 Cloudflare 连接有；别的连接下选中它只会被弹回启动页（见上面的视图守卫）
      .filter((v) => v.view !== 'templates' || activeConn?.kind === 'cloudflare')
      .filter((v) => !q || v.label.includes(q))
      .map((v) => ({ kind: 'view' as const, ...v }));
    if (views.length) out.push({ title: '视图', entries: views });
    if (q) {
      // 远程连接：名称 / IP / user@host / 端口 / 系统。
      // ★ 这一段**不按 activeConnId 过滤** —— 别的分区搜的是「当前连接内部的东西」，
      //   而连接本身是**跨连接**的：机器一多，全局搜索就是最快的切换方式（记不住哪台是哪个 IP）。
      const connHits = conns
        .filter((c) => c.kind === 'ssh')
        .filter((c) => connHay(c).includes(q))
        .slice(0, 8);
      if (connHits.length) out.push({ title: '远程连接', entries: connHits.map((c) => ({ kind: 'conn' as const, c })) });
      // 定时任务：标题 / prompt / 站点名（slug 与展示名都算）
      const taskHits = tasks
        .filter((t) => t.conn_id === activeConnId)
        .filter((t) => {
          const names = [t.title, t.prompt, t.site_name, t.site_slug, ...(t.site_names ?? []), ...(t.site_slugs ?? [])].join('\n').toLowerCase();
          return names.includes(q);
        })
        .slice(0, 10);
      if (taskHits.length) out.push({ title: '定时任务', entries: taskHits.map((t) => ({ kind: 'task' as const, t })) });
      // 托管站点：站点名 / slug
      const mHits = managedOfConn.filter((m) => m.site_name.toLowerCase().includes(q) || m.site_slug.toLowerCase().includes(q)).slice(0, 8);
      if (mHits.length) out.push({ title: '托管站点', entries: mHits.map((m) => ({ kind: 'managed' as const, m })) });
      // 模板：名称 / 描述 / slug / 分类（模板库视图里因此不再摆搜索框）
      if (activeConn?.kind === 'cloudflare') {
        const tplHits = templates.filter((t) => tmplHay(t).includes(q)).slice(0, 8);
        if (tplHits.length) out.push({ title: '模板', entries: tplHits.map((t) => ({ kind: 'template' as const, t })) });
      }
      // 会话（原有匹配逻辑）
      if (searchResults.length) out.push({ title: '会话', entries: searchResults.map((c) => ({ kind: 'conv' as const, c })) });
      // 排期：远端数据不实时搜——固定一条动作项，选中去排期视图做标题过滤
      out.push({ title: '排期', entries: [{ kind: 'sched-find', q: searchQ.trim() }] });
    }
    return out;
  });
  const searchFlat = $derived(searchSections.flatMap((s) => s.entries));
  let searchIdx = $state(0);
  $effect(() => { void searchQ; searchIdx = 0; }); // 换关键词回到第一项
  function searchNudge(delta: number) {
    if (!searchFlat.length) return;
    searchIdx = Math.max(0, Math.min(searchFlat.length - 1, searchIdx + delta));
    requestAnimationFrame(() => document.querySelector('.search-item.on')?.scrollIntoView({ block: 'nearest' }));
  }
  function pickEntry(it: SearchEntry) {
    searchOpen = false;
    if (it.kind === 'view') {
      if (it.view === 'schedule') void openSchedule();
      else if (it.view === 'tasks') void openTasks();
      else if (it.view === 'templates') openTemplates();
      else void openManaged();
    } else if (it.kind === 'template') {
      // 分类筛选可能正好把它挡在外面——搜到了、跳过去却看不见，比搜不到还让人懵
      tmplCat = '全部';
      openTemplates();
      void focusTemplate(it.t.slug);
    } else if (it.kind === 'conn') {
      // 走和切换器同一条路：连接切换牵着视图守卫、SSH 探系统、终端重建，别在这儿另起一套
      void selectConn(it.c.id);
    } else if (it.kind === 'conv') {
      openConv(it.c.id);
    } else if (it.kind === 'task') {
      // 切到定时任务视图并直接打开该任务的编辑弹窗（openEditTask 自带回填）
      void openTasks();
      openEditTask(it.t);
    } else if (it.kind === 'managed') {
      void openManaged();
      const id = `mc-${it.m.id}`;
      setTimeout(() => document.getElementById(id)?.scrollIntoView({ block: 'start', behavior: 'smooth' }), 120);
    } else {
      schedTitleQ = it.q;
      void openSchedule();
    }
  }
  $effect(() => {
    if (!searchOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') searchOpen = false;
      else if (e.key === 'ArrowDown') { e.preventDefault(); searchNudge(1); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); searchNudge(-1); }
      else if (e.key === 'Enter') { const it = searchFlat[searchIdx]; if (it) pickEntry(it); }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });
  function say(m: string, k: 'ok' | 'err' = 'ok', ms = 0) { flash = m; flashKind = k; setTimeout(() => (flash = ''), ms || (k === 'err' ? 8000 : 4000)); }
  function brainUsable(b: Brain): boolean { const s = b === 'claude' ? brains?.claude : b === 'grok' ? brains?.grok : brains?.codex; return !!s && s.found && s.logged_in !== false; }
  function hostOf(u: string): string { try { return new URL(u).host; } catch { return u; } }

  // 从对话里认出这个项目已部署的线上地址（AI 部署完都会说「访问 https://xxx」）。
  // 优先自定义域名，其次 *.pages.dev；忽略 localhost / CDN / 文档等无关链接。
  function detectSiteUrl(convs: (Conversation | null)[]): string {
    const re = /https?:\/\/[a-z0-9.-]+\.[a-z]{2,}[^\s`"'）)，。；]*/gi;
    let custom = '', pages = '';
    for (const c of convs) {
      if (!c) continue;
      for (const m of c.messages) {
        if (m.role !== 'assistant' || m.hidden) continue;
        const found = m.text.match(re);
        if (!found) continue;
        for (let u of found) {
          u = u.replace(/[.,;)]+$/, '');
          const host = u.replace(/^https?:\/\//i, '').split('/')[0].toLowerCase();
          if (host.includes('localhost') || host.startsWith('127.') || host.includes('cloudflare.com')
            || host.includes('developers.') || host.includes('tailwindcss.com') || host.includes('github')
            || host.includes('npmjs') || host.includes('unpkg') || host.includes('jsdelivr')) continue;
          if (host.endsWith('.pages.dev')) pages = u; else custom = u;
        }
      }
    }
    return custom || pages;
  }
  // 当前会话站点的公开地址：CF 从项目文件探测；gcms 用 discovery 里站点自带的 url。
  const activeSiteUrl = $derived.by(() => {
    const c = activeConv;
    if (!c) return '';
    if (activeConvIsCf) return detectSiteUrl([c]);
    return sites.find((s) => s.slug === c.site_slug)?.url ?? '';
  });
  // 从当前发现结果里按 slug 找站点图标（favicon 优先，其次 logo）；找不到返回空由 SiteFav 用首字母兜底。
  // 站点公开地址猜标准 favicon 位置，作为 discovery favicon/logo 之外的兜底（SiteFav 加载失败再退首字母）。
  function faviconGuess(url?: string): string { const u = (url ?? '').trim(); return u ? u.replace(/\/+$/, '') + '/favicon.ico' : ''; }
  function siteFav(slug: string): string { const s = sites.find((x) => x.slug === slug); return s?.favicon || s?.logo || faviconGuess(s?.url); }

  // 「新窗口打开」开出来的窗口，label 是 `conn-<id>`：这一份前端就固定开在那个连接上。
  // （连接/对话/SSH 会话都在 Rust 侧同一份 AppState 里，多个窗口看到的是同一份数据。）
  // 用 label 而不是 URL 参数：带 query 的 WebviewUrl::App 在 dev 下会指到 /index.html，
  // 而 SvelteKit 只认 /，开出来是白屏。详见 lib.rs::open_conn_window。
  const CONN_WIN_PREFIX = 'conn-';
  function bootConnId(): string {
    try {
      const l = getCurrentWindow().label;
      return l.startsWith(CONN_WIN_PREFIX) ? l.slice(CONN_WIN_PREFIX.length) : '';
    } catch { return ''; }
  }
  async function refreshConns() {
    try {
      conns = await invoke('list_connections');
      if (!activeConnId && conns.length) {
        const want = bootConnId();
        selectConn(conns.some((c) => c.id === want) ? want : conns[0].id);
      }
    } catch (e) { say(String(e), 'err'); }
  }
  async function refreshBrains() { try { brains = await invoke('detect_brains'); } catch (e) { say(String(e), 'err'); } }
  let copiedCmd = $state('');
  async function copyCmd(cmd: string) {
    try { await navigator.clipboard.writeText(cmd); copiedCmd = cmd; setTimeout(() => { if (copiedCmd === cmd) copiedCmd = ''; }, 1500); }
    catch { say('复制失败，请手动选中命令复制', 'err'); }
  }
  // 技能包新增/移除站点后，重新拉取当前连接的可管站点列表。
  async function refreshSites() { if (activeConnId && !discoveryLoading) await selectConn(activeConnId); }
  let brainsBusy = $state(false);
  // 手动重新检测：走 redetect_brains（后端先重读登录 shell 的 PATH，再探测）——治「装/登录完仍显示未安装/未登录」。
  async function refreshBrainsManual() { brainsBusy = true; try { brains = await invoke('redetect_brains'); } catch (e) { say(String(e), 'err'); } finally { brainsBusy = false; } }

  // 应用版本 + 在线检查更新（Tauri updater：拉 release 仓 latest.json，ed25519 验签后下载安装再重启）。
  let appVersion = $state('');
  let updBusy = $state(false);
  let updMsg = $state('');
  let updPct = $state(-1); // 下载进度 0-100；-1 = 不确定（无 content-length）
  let updAvail = $state(''); // 非空 = 静默检查到的新版本号，驱动工具栏「待更新」图标
  getVersion().then((v) => (appVersion = v)).catch(() => { /* */ });
  // 静默检查：只置「有更新」标记，不弹窗、不自动下载；失败（离线 / 非 Tauri 环境）静默忽略。
  async function checkUpdateSilent() {
    if (updBusy) return;
    try {
      const upd = await checkUpdate();
      updAvail = upd ? upd.version : '';
      if (upd) { try { await upd.close(); } catch { /* */ } } // 释放句柄，点击时再重新拉取下载
    } catch { /* 离线 / dev 无后端：忽略 */ }
  }
  async function runUpdate() {
    if (updBusy) return;
    updBusy = true; updMsg = '检查中…'; updPct = -1;
    try {
      const upd = await checkUpdate();
      if (!upd) { updAvail = ''; updMsg = '已是最新版本'; return; }
      updAvail = upd.version;
      const ok = await confirmDialog(`发现新版本 ${upd.version}，现在下载更新并重启？`, { title: '有可用更新', kind: 'info' });
      if (!ok) { updMsg = ''; return; }
      let total = 0, got = 0;
      updMsg = '准备下载…'; updPct = 0;
      await upd.downloadAndInstall((ev) => {
        if (ev.event === 'Started') { total = ev.data.contentLength ?? 0; got = 0; updPct = 0; updMsg = '下载更新…'; }
        else if (ev.event === 'Progress') { got += ev.data.chunkLength; updPct = total > 0 ? Math.min(100, Math.round((got / total) * 100)) : -1; updMsg = total > 0 ? `下载更新 ${updPct}%` : '下载更新…'; }
        else if (ev.event === 'Finished') { updPct = 100; updMsg = '安装中…'; }
      });
      updMsg = '即将重启…'; updPct = 100;
      await relaunch();
    } catch (e) { updMsg = '更新失败：' + String(e); updPct = -1; }
    finally { updBusy = false; }
  }
  // 启动后稍等再静默查一次（让窗口先就绪），之后每 6 小时查一次（应用常驻，SPA 不卸载无需清理）。
  setTimeout(checkUpdateSilent, 4000);
  setInterval(checkUpdateSilent, 6 * 60 * 60 * 1000);
  async function refreshConvos() { try { convos = await invoke('list_conversations'); } catch (e) { say(String(e), 'err'); } }

  let selSeq = 0;
  async function selectConn(id: string) {
    const switching = id !== activeConnId;
    const conn = conns.find((c) => c.id === id);
    const seq = ++selSeq; activeConnId = id; discovery = null;
    void maybeCheckPackUpdate(id); // 静默查技能包更新（24h 节流，不阻塞选择）
    if (switching) {
      // 换连接＝换工作区：关掉上个连接的对话/排期/定时任务视图，别串场。
      // 模板库 / 提示词库只在 CF 连接下有；切到 gcms 时也退回启动页。
      activeConvId = ''; activeConv = null;
      if (view === 'thread' || view === 'schedule' || view === 'tasks' || view === 'managed' || view === 'remote' || ((view === 'templates' || view === 'prompts') && conn?.kind !== 'cloudflare')) view = 'launcher';
    }
    if (conn?.kind === 'ssh') {
      // SSH 连接没有站点/项目可发现，也没有独立的启动页/对话页——**一切都在远程工作台里**
      //（终端为主，底部对话、右侧文件按需开）。
      void probeOs(id); // 系统版本：没探过才连，探到就存着，之后一直是本地读
      view = 'remote';
      if (switching) {
        disposeTerm();
        // 远程连接绝不默认「全自动」：那是别人的真机，默认得让每条命令都过一次你的眼。
        if (lPerm === 'full') lPerm = 'auto';
      }
      return;
    }
    if (conn?.kind === 'cloudflare') {
      // 只在真正换连接时清输入 + 拨默认档；同连接重选（如刷新）不动用户已填的项目名。
      if (switching) {
        cfProjects = []; lSite = ''; lStyle = '';
        if (lPerm === 'full') lPerm = 'auto'; // CF 默认「自动」——部署/改 DNS 弹确认，防手滑
      }
      try { const ps = await invoke<string[]>('list_cf_projects', { connId: id }); if (seq === selSeq) cfProjects = ps; }
      catch (e) { if (seq === selSeq) say(String(e), 'err'); }
      return;
    }
    discoveryLoading = true;
    try { const r = await invoke<Discovery>('discover_sites', { connId: id }); if (seq === selSeq) discovery = r; }
    catch (e) { if (seq === selSeq) say(String(e), 'err'); }
    finally { if (seq === selSeq) { discoveryLoading = false; if (!lSite && discovery?.items.length) lSite = discovery.items[0].slug; } }
  }

  $effect(() => { refreshConns(); refreshBrains(); refreshConvos(); });
  $effect(() => {
    const need = !!brains && ((brains.claude.found && brains.claude.logged_in === false) || (brains.codex.found && brains.codex.logged_in === false) || (brains.grok.found && brains.grok.logged_in === false));
    if (!need) return;
    const t = setInterval(() => { if (!document.hidden) refreshBrains(); }, 6000);
    return () => clearInterval(t);
  });

  // ---------- import ----------
  let keyOpen = $state(false); let keyZip = $state(''); let keyBase = $state(''); let keyVal = $state(''); let keyErr = $state('');
  async function importPack() {
    const f = await open({ multiple: false, filters: [{ name: '技能包', extensions: ['zip'] }] });
    if (!f) return; await doImport(f, null);
  }
  async function doImport(zip: string, key: string | null) {
    importBusy = true;
    try {
      const o = await invoke<ImportOutcome>('import_pack', { zipPath: zip, name: null, key });
      if (o.status === 'needs_key') { keyZip = zip; keyBase = o.api_base; keyVal = ''; keyErr = ''; keyOpen = true; return; }
      keyOpen = false;
      if (o.status === 'upgraded') {
        delete packUpdates[o.connection.id];
        say(`「${o.connection.name}」技能包已就地升级，对话全部保留`);
      } else {
        say(`已导入「${o.connection.name}」`);
      }
      await refreshConns(); await selectConn(o.connection.id);
    } catch (e) { if (keyOpen) keyErr = String(e); else say(String(e), 'err'); }
    finally { importBusy = false; }
  }
  async function confirmKey() {
    const k = keyVal.trim(); if (!k) return;
    if (!k.startsWith('gcmsp_') && !k.startsWith('gcms_')) { keyErr = '密钥前缀应为 gcmsp_ 或 gcms_'; return; }
    keyErr = ''; await doImport(keyZip, k);
  }

  // ---------- 技能包更新（徽标 + 一键升级） ----------
  let packUpdates = $state<Record<string, string>>({}); // connId → 可升级到的版本
  let packUpdating = $state<Record<string, boolean>>({});
  // 选中 gcms 连接时静默查一次版本（每连接每 24h 最多一次；旧服务端/断网都静默跳过）。
  async function maybeCheckPackUpdate(connId: string) {
    const conn = conns.find((c) => c.id === connId);
    if (!conn || conn.kind !== 'gcms') return;
    const k = 'gcms.pilot.packCheck.' + connId;
    const last = Number(localStorage.getItem(k) || 0);
    if (Date.now() - last < 24 * 3600 * 1000) return;
    try {
      const r = await invoke<{ current: string; latest: string; has_update: boolean }>('check_pack_update', { connId });
      localStorage.setItem(k, String(Date.now())); // 查成功才消耗 24h 窗口；失败（断网等）下次选中直接重试
      if (r.has_update) packUpdates[connId] = r.latest;
    } catch { /* 静默 */ }
  }
  async function upgradePack(connId: string) {
    if (packUpdating[connId]) return;
    // 有对话正在跑时不升级：换脚本会影响在途回合。
    if (Object.values(running).includes(connId) || convos.some((c) => c.conn_id === connId && c.status === 'running')) {
      say('该连接下有对话正在运行，请先点停止结束这一轮，再升级技能包。', 'err');
      return;
    }
    packUpdating[connId] = true;
    try {
      const msg = await invoke<string>('update_pack', { connId });
      delete packUpdates[connId];
      say(msg);
      await refreshConns();
    } catch (e) { say(String(e), 'err'); }
    finally { delete packUpdating[connId]; }
  }

  // ---------- 新建 / 编辑远程连接（SSH） ----------
  // 安全：密码/私钥口令只在这个表单的内存里过一下，随 connect_ssh/update_ssh 进钥匙串，绝不落盘。
  // 主机指纹走 TOFU：必须先「测试连接」拿到指纹、由你确认后才能保存（后端无指纹一律拒）。
  let sshOpen = $state(false);
  let sshTesting = $state(false);
  let sshConnecting = $state(false);
  let sshErr = $state('');
  let sshFp = $state(''); // 试连拿到的主机指纹；非空 = 认证已通过、待你确认指纹
  let sshEditId = $state(''); // 非空 = 编辑已有连接
  let sshOldFp = $state(''); // 编辑时：连接里存着的旧指纹，用来发现「机器的指纹变了」
  const SSH_BLANK = { name: '', host: '', port: 22, user: '', auth: 'password', password: '', keyPath: '', keyPass: '' };
  let sshF = $state({ ...SSH_BLANK });
  let sshF0 = $state({ ...SSH_BLANK }); // 编辑时的初始值：用来判断「有没有动过连接相关的字段」
  const sshAuthOpts = [
    { value: 'password', label: '密码', sub: '用账号密码登录' },
    { value: 'key', label: '密钥', sub: '用私钥文件登录（更安全）' },
  ];
  function openSshConnect() {
    sshOpen = true; sshErr = ''; sshFp = ''; sshEditId = ''; sshOldFp = '';
    sshF = { ...SSH_BLANK };
    sshF0 = { ...SSH_BLANK };
  }
  function openSshEdit(c: Connection) {
    sshOpen = true; sshErr = ''; sshFp = ''; sshEditId = c.id; sshOldFp = c.ssh_fingerprint || '';
    // 密码/口令不回显（我们自己也读不到明文以外的用途）——留空即保持钥匙串里那条不变。
    sshF = {
      name: c.name, host: c.ssh_host ?? '', port: c.ssh_port || 22, user: c.ssh_user ?? '',
      auth: c.ssh_auth || 'password', password: '', keyPath: c.ssh_key_path ?? '', keyPass: '',
    };
    sshF0 = { ...sshF };
  }
  // 改了连接相关的字段（地址/端口/用户/认证方式/密钥路径/新密码）→ 必须重新试连拿指纹再存。
  // 只改名字则不必：那和「能不能连上」无关，别为了改个名逼用户去连一次机器。
  const sshDirty = $derived(
    sshF.host.trim() !== sshF0.host || Number(sshF.port) !== Number(sshF0.port) ||
    sshF.user.trim() !== sshF0.user || sshF.auth !== sshF0.auth ||
    sshF.keyPath.trim() !== sshF0.keyPath || !!sshF.password || !!sshF.keyPass
  );
  const sshRenameOnly = $derived(!!sshEditId && !sshDirty);
  // 试连看到的指纹和存着的不一样 = 这台机器换了主机密钥（重装？也可能是中间人）——必须让人看见。
  const sshFpChanged = $derived(!!sshEditId && !!sshFp && !!sshOldFp && sshFp !== sshOldFp);
  async function saveSshEdit() {
    if (sshConnecting) return;
    sshConnecting = true; sshErr = '';
    try {
      const conn = await invoke<Connection>('update_ssh', {
        connId: sshEditId,
        name: sshF.name.trim(), host: sshF.host.trim(), port: Number(sshF.port) || 22,
        user: sshF.user.trim(), auth: sshF.auth, keyPath: sshF.keyPath,
        // 留空＝不动钥匙串（只改了名字/地址时不必重填密码）
        secret: sshF.auth === 'key' ? sshF.keyPass : sshF.password,
        // 只改名字时没试连，沿用原指纹
        fingerprint: sshFp || sshOldFp,
      });
      sshOpen = false; say(`已更新「${conn.name}」`);
      await refreshConns();
      if (activeConnId === conn.id) { disposeTerm(); void probeOs(conn.id); }
    } catch (e) { sshErr = String(e); }
    finally { sshConnecting = false; }
  }
  async function pickSshKey() {
    try {
      const p = await open({ multiple: false, title: '选择私钥文件（如 ~/.ssh/id_ed25519）' });
      if (typeof p === 'string') sshF.keyPath = p;
    } catch (e) { sshErr = String(e); }
  }
  // 编辑时密码可以留空（＝沿用钥匙串里那条），新建时必须填。
  const sshCanTest = $derived(
    !!sshF.host.trim() && !!sshF.user.trim() &&
    (sshF.auth === 'key' ? !!sshF.keyPath.trim() : (!!sshF.password || (!!sshEditId && sshF0.auth === 'password')))
  );
  async function testSsh() {
    if (sshTesting || !sshCanTest) return;
    sshTesting = true; sshErr = ''; sshFp = '';
    try {
      const p = await invoke<{ fingerprint: string; auth_ok: boolean; error: string }>('verify_ssh', {
        host: sshF.host.trim(), port: Number(sshF.port) || 22, user: sshF.user.trim(), auth: sshF.auth,
        password: sshF.password, keyPath: sshF.keyPath, keyPass: sshF.keyPass, expectFingerprint: '',
        // 编辑时密码/口令留空 = 用钥匙串里存着的那条来试（后端取，前端永远看不到明文）
        connId: sshEditId,
      });
      sshFp = p.fingerprint;
    } catch (e) { sshErr = String(e); }
    finally { sshTesting = false; }
  }
  async function confirmSsh() {
    if (sshConnecting || !sshFp) return;
    sshConnecting = true; sshErr = '';
    try {
      const conn = await invoke<Connection>('connect_ssh', {
        name: sshF.name.trim(), host: sshF.host.trim(), port: Number(sshF.port) || 22,
        user: sshF.user.trim(), auth: sshF.auth, keyPath: sshF.keyPath,
        secret: sshF.auth === 'key' ? sshF.keyPass : sshF.password,
        fingerprint: sshFp,
      });
      sshOpen = false; say(`已连接「${conn.name}」`);
      await refreshConns(); await selectConn(conn.id);
      view = 'remote';
    } catch (e) { sshErr = String(e); }
    finally { sshConnecting = false; }
  }

  // ---------- 远程连接：系统信息 ----------
  // os-release 的 ID 归一化到我们画得出图标的那几家；认不出的返回 '' 走通用形。
  function distroOf(c: Connection | undefined | null): string {
    const id = (c?.ssh_os_id || '').toLowerCase();
    if (['ubuntu', 'debian', 'alpine', 'fedora', 'arch', 'centos', 'rocky', 'almalinux', 'rhel'].includes(id)) return id;
    if (id === 'raspbian' || id === 'linuxmint' || id === 'pop' || id === 'kali') return 'debian'; // 都是 debian 系
    if (id === 'arch' || id === 'manjaro' || id === 'endeavouros') return 'arch';
    if (id.startsWith('opensuse') || id === 'sles') return ''; // 没画 suse，走通用形
    return '';
  }
  // 页脚/下拉的副标题：探到了就显示系统，没探到就显示 user@host（总得有东西）。
  /** 远程连接的搜索索引：名称 / IP / user@host / 端口 / 系统都能命中。
   *  机器一多就记不住「哪台是哪个 IP」——所以 IP 必须能搜，这正是用户要它的原因。 */
  const connHay = (c: Connection) =>
    `${c.name} ${c.ssh_user ?? ''}@${c.ssh_host ?? ''} ${c.ssh_host ?? ''}:${c.ssh_port ?? ''} ${c.ssh_os ?? ''} ${c.ssh_os_id ?? ''}`.toLowerCase();
  function sshSub(c: Connection | undefined | null): string {
    if (!c) return '';
    return c.ssh_os || `${c.ssh_user}@${c.ssh_host}${c.ssh_port && c.ssh_port !== 22 ? ':' + c.ssh_port : ''}`;
  }
  let osProbing = $state<Record<string, boolean>>({});
  /** 探一次远端系统版本并存进连接。已探过就不再连（除非 force）。 */
  async function probeOs(id: string, force = false) {
    const c = conns.find((x) => x.id === id);
    if (!c || c.kind !== 'ssh' || osProbing[id]) return;
    if (c.ssh_os && !force) return;
    osProbing = { ...osProbing, [id]: true };
    try {
      const u = await invoke<Connection>('ssh_os_probe', { connId: id });
      conns = conns.map((x) => (x.id === u.id ? u : x));
    } catch (e) {
      // 机器关着/网络不通都很正常：静默（真要用连接时自然会报错），只在手动点刷新时说话。
      if (force) say(String(e), 'err');
    } finally {
      osProbing = { ...osProbing, [id]: false };
    }
  }

  // ---------- 远程工作台：终端为主区，底部对话 / 右侧文件按需开（VS Code 那套） ----------
  // 两个面板可以同时开，各自可拖改大小；开关与尺寸都记住。
  // 面板布局**按对话独立**：同一台机器上不同对话的干活姿势不一样 ——
  // 一条在装东西要盯着终端，另一条只是问问题、想让对话占满。全局一份的话，
  // 在这条里关掉命令行，切过去那条也没了。
  // 存储只留一份「默认」：新对话（和还没碰过的旧对话）用它开局，你一拨就成为新的默认。
  // 各对话的当前布局只放内存 —— 否则 localStorage 会随对话数无限长，删了对话还留一堆孤儿键。
  // 面板布局**按对话独立**：会话1 只留对话框、会话2 只留命令行 —— 各是各的，互不影响。
  //
  // ★ 上一版错在「拨一下顺便记成全局默认」：于是在会话1关掉命令行就改写了默认，
  //   会话2（还没碰过）一开局就继承成会话1的样子 —— 看起来就是「共享」。**开关不许写全局**。
  //
  // 开关（term/chat/files）：每条对话一份，按对话 id 存盘；新对话用固定默认（命令行+对话框）。
  // 尺寸（chatH/filesW）：全局偏好 —— 「面板多高」是手感，不是某条对话的属性，
  //   每开一条新对话就要重新拖一遍才叫烦人。
  type WbFlags = { term: boolean; chat: boolean; files: boolean };
  type WbLayout = WbFlags & { chatH: number; filesW: number };
  const WB_FLAGS_DEF: WbFlags = { term: true, chat: true, files: false };
  const WB_BY_CONV = 'gcms.pilot.wb.byConv';
  let wbStartEl = $state<HTMLTextAreaElement | null>(null); // 起点输入框（新对话后把光标送进去）
  function loadWbSize(k: string, def: number, lo: number, hi: number): number {
    try { const n = parseInt(localStorage.getItem('gcms.pilot.wb.' + k) || ''); return n >= lo && n <= hi ? n : def; } catch { return def; }
  }
  function loadWbByConv(): Record<string, WbFlags> {
    try { const o = JSON.parse(localStorage.getItem(WB_BY_CONV) || '{}'); return o && typeof o === 'object' ? o : {}; } catch { return {}; }
  }
  let wbByConv: Record<string, WbFlags> = loadWbByConv(); // 对话 id → 开关；'new:<连接>' = 该连接还没有对话时的起点
  let wb = $state<WbLayout>({
    ...WB_FLAGS_DEF,
    chatH: loadWbSize('chatH', 260, 140, 900),
    filesW: loadWbSize('filesW', 380, 240, 900),
  });
  let wbKeyCur = '';
  function wbKeyOf(): string { return activeConvId || (isSshConn ? 'new:' + activeConnId : ''); }
  /** 存盘：顺手按「现存对话」清一遍，免得删了对话还留一堆孤儿键把 localStorage 撑大。 */
  function saveWbByConv() {
    try {
      // ★ convos 还没加载完（开机那会儿是空的）时**绝不能清**：那会把所有对话的布局
      //   一把全删光 —— 看起来就是「配置没保住」。空表说明「还不知道有哪些对话」，
      //   不是「一条都没有」。
      if (convos.length) {
        const live = new Set(convos.map((c) => c.id));
        const keep: Record<string, WbFlags> = {};
        for (const [k, v] of Object.entries(wbByConv)) {
          if (k.startsWith('new:') || live.has(k)) keep[k] = v;
        }
        wbByConv = keep;
      }
      localStorage.setItem(WB_BY_CONV, JSON.stringify(wbByConv));
    } catch { /* 配额满/隐私模式：布局丢了就丢了，不值得打断用户 */ }
  }
  // 切对话/切连接 → 取回目标那份（没碰过的用固定默认，**不继承别的对话**）。
  // 不必在这里存：setWbFlags 每次拨动就已经存过了。
  $effect(() => {
    const key = wbKeyOf();
    if (key === wbKeyCur) return;
    const prev = wbKeyCur;
    wbKeyCur = key;
    let f = wbByConv[key];
    // ★ 起点 → 新对话：在起点输入框里拨好的布局要跟过去。这条对话**就是**那个起点变的，
    //   不是「继承别的对话」。漏了它就会：你在起点拨好面板，一发消息全弹回默认。
    if (!f && key && !key.startsWith('new:') && prev.startsWith('new:')) {
      f = wbByConv[prev];
      if (f) { wbByConv[key] = { ...f }; saveWbByConv(); }
    }
    f = f ?? WB_FLAGS_DEF;
    wb = { ...wb, term: f.term, chat: f.chat, files: f.files };
  });
  function saveWbSize(k: 'chatH' | 'filesW', v: number) { try { localStorage.setItem('gcms.pilot.wb.' + k, String(v)); } catch { /* */ } }
  /** 拨一下：只改**当前这条对话**的开关，绝不碰别的对话、也不写全局默认。 */
  function setWbFlags(patch: Partial<WbFlags>) {
    wb = { ...wb, ...patch };
    const key = wbKeyCur || wbKeyOf();
    if (key) {
      wbByConv[key] = { term: wb.term, chat: wb.chat, files: wb.files };
      saveWbByConv();
    }
  }
  /** 三个面板至少留一个开着：全关＝主区一整块空白。
   *  以前更严——终端/对话必须留一个（那会儿文件栏只是右侧窄条、不占主区，关掉这俩主区就空了）。
   *  现在放开：文件栏单开时让它吃满整宽（见 wbFilesSolo + .wb-main.collapsed），
   *  所以「只留文件」是合法布局。每个开关只在「关掉后一块都不剩」时才顶个默认（终端）上来。 */
  function toggleWbTerm() { setWbFlags(!wb.term ? { term: true } : wb.chat || wb.files ? { term: false } : { term: false, chat: true }); }
  function toggleWbChat() { setWbFlags(!wb.chat ? { chat: true } : wb.term || wb.files ? { chat: false } : { chat: false, term: true }); }
  function toggleWbFiles() { setWbFlags(!wb.files ? { files: true } : wb.term || wb.chat ? { files: false } : { files: false, term: true }); }
  // 文件栏单开：终端和对话都关了，只剩文件 —— 这时让它撑满主区（.wb-main 空了就收起来）。
  const wbFilesSolo = $derived(wb.files && !wb.term && !wb.chat);
  /** 面板拖拽。dir=h：拖对话面板的上沿（往上拖＝更高）；dir=v：拖文件面板的左沿（往左拖＝更宽）。 */
  function startWbResize(e: MouseEvent, dir: 'h' | 'v') {
    e.preventDefault();
    const x0 = e.clientX, y0 = e.clientY;
    const h0 = wb.chatH, w0 = wb.filesW;
    const onMove = (ev: MouseEvent) => {
      if (dir === 'h') wb = { ...wb, chatH: Math.min(900, Math.max(140, Math.round(h0 - (ev.clientY - y0)))) };
      else wb = { ...wb, filesW: Math.min(900, Math.max(240, Math.round(w0 - (ev.clientX - x0)))) };
    };
    const onUp = () => {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      document.body.style.cursor = '';
      saveWbSize(dir === 'h' ? 'chatH' : 'filesW', dir === 'h' ? wb.chatH : wb.filesW); // 落定才存
    };
    document.body.style.cursor = dir === 'h' ? 'row-resize' : 'col-resize';
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  }

  // ---------- 远端负载（顶栏的 CPU / 内存 / 磁盘） ----------
  // CPU 那两个字段是 /proc/stat 的**累计计数**，不是百分比：单次读没意义，得跟上一次求差。
  // 所以这里按连接留一份上次的样本；第一次只能显示「—」，第二次（5 秒后）才有 CPU%。
  type SshStats = { cpu_total: number; cpu_idle: number; mem_total_kb: number; mem_avail_kb: number; disk_total_kb: number; disk_used_kb: number };
  const STAT_EVERY_MS = 5000;
  let statPrev: Record<string, SshStats> = {};
  let stat = $state<null | { cpu: number | null; memPct: number; memText: string; diskPct: number; diskText: string }>(null);
  function fmtKb(kb: number): string {
    const u = ['KB', 'MB', 'GB', 'TB'];
    let v = kb, i = 0;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${u[i]}`;
  }
  async function pollStats(id: string) {
    try {
      const s = await invoke<SshStats>('ssh_stats', { connId: id });
      const p = statPrev[id];
      statPrev[id] = s;
      // 两次采样之间的差：分母是这段时间里所有时间片的增量，分子是其中非空闲的部分。
      let cpu: number | null = null;
      if (p) {
        const dt = s.cpu_total - p.cpu_total;
        const di = s.cpu_idle - p.cpu_idle;
        // 机器重启过（计数器归零）→ dt<=0，这一轮跳过，下一轮就正常了
        if (dt > 0) cpu = Math.min(100, Math.max(0, Math.round((1 - di / dt) * 100)));
      }
      const memUsed = s.mem_total_kb - s.mem_avail_kb;
      stat = {
        cpu,
        memPct: s.mem_total_kb ? Math.round((memUsed / s.mem_total_kb) * 100) : 0,
        memText: `${fmtKb(memUsed)} / ${fmtKb(s.mem_total_kb)}`,
        diskPct: s.disk_total_kb ? Math.round((s.disk_used_kb / s.disk_total_kb) * 100) : 0,
        diskText: `${fmtKb(s.disk_used_kb)} / ${fmtKb(s.disk_total_kb)}`,
      };
    } catch {
      // 没连上 / 不是 Linux / 命令被拒 —— 都不吭声，顶栏空着就是了（这不是用户要办的事）
      stat = null;
    }
  }
  // 只在看得见工作台时才轮询；换连接清掉上一台的数（否则会拿别的机器的样本算差）。
  $effect(() => {
    const id = activeConnId;
    if (view !== 'remote' || !isSshConn || !id) { stat = null; return; }
    stat = null;
    delete statPrev[id];
    void pollStats(id);
    const t = setInterval(() => { if (!document.hidden) void pollStats(id); }, STAT_EVERY_MS);
    return () => clearInterval(t);
  });

  // ---------- 远程终端（xterm + PTY 流） ----------
  const isSshConn = $derived(activeConn?.kind === 'ssh');
  let termEl = $state<HTMLDivElement | null>(null);
  let termOn = $state(false);
  let termBusy = $state(false); // 建连中：刷新键转起来 + 禁重复点
  let term: import('@xterm/xterm').Terminal | null = null;
  let termFit: import('@xterm/addon-fit').FitAddon | null = null;
  let termConnId = '';
  // 注意：**不清 activeConv** —— 工作台里终端一直在，没有「切到终端」这回事；
  // 清掉对话只会把用户正在聊的东西关了（那正是侧栏「远程终端」入口被删掉的原因）。
  function openRemote() { view = 'remote'; }
  function b64ToBytes(b64: string): Uint8Array {
    const s = atob(b64); const a = new Uint8Array(s.length);
    for (let i = 0; i < s.length; i++) a[i] = s.charCodeAt(i);
    return a;
  }
  function strToB64(s: string): string {
    let bin = '';
    for (const b of new TextEncoder().encode(s)) bin += String.fromCharCode(b);
    return btoa(bin);
  }
  function disposeTerm() {
    if (term) { try { term.dispose(); } catch { /* */ } }
    term = null; termFit = null; termOn = false; termBusy = false; termConnId = '';
  }
  // 终端容器一变大小就重排：开关面板、拖面板边、拉窗口——一个 ResizeObserver 全包了，
  // 不用在每个改布局的地方各记一次 fit()（漏一个就是花屏）。
  $effect(() => {
    const el = termEl;
    if (!el) return;
    let raf = 0;
    const ro = new ResizeObserver(() => {
      // 合并同一帧里的连续变化（拖拽时每像素都会触发）
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => { try { termFit?.fit(); } catch { /* 终端已拆 */ } });
    });
    ro.observe(el);
    return () => { cancelAnimationFrame(raf); ro.disconnect(); };
  });
  // 进 remote 视图且容器就位 → 起终端；切走/换连接由 $effect 的清理拆掉。
  $effect(() => {
    const id = activeConnId;
    const el = termEl;
    if (view !== 'remote' || !isSshConn || !el || !id) return;
    if (termConnId === id && term) return; // 同一连接已在跑
    void startTerm(id, el);
  });
  async function startTerm(id: string, el: HTMLDivElement) {
    disposeTerm();
    termConnId = id;
    termBusy = true;
    // xterm 要 DOM，动态 import（SSR 关着，但仍别在模块顶层拉进来）。
    const [{ Terminal }, { FitAddon }] = await Promise.all([
      import('@xterm/xterm'),
      import('@xterm/addon-fit'),
    ]);
    const t = new Terminal({
      fontSize: 12,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      cursorBlink: true,
      // ★ 滚动条宽度只能从这儿改：xterm 6 不再用 viewport 的原生滚动条，改成了自绘的
      // （.xterm-scrollable-element > .scrollbar，从 VS Code 移植），宽度在 JS 里取
      // `overviewRuler?.width || 14` —— CSS 的 ::-webkit-scrollbar 对它完全无效。
      // 副作用：这个选项**身兼两职**，一设就同时把「装饰栏」渲染出来（文档原话：this must be
      // set in order to see the overview ruler），它会在自己左沿画一条贯穿全高的 1px 竖线
      // （_renderRulerOutline，用 theme.overviewRulerBorder，默认深色）——就是那条碍眼的线。
      // 我们压根不用 decoration，那个 canvas 纯属负担 → CSS 里直接 display:none（见 .term-wrap 那段）。
      overviewRuler: { width: 7 },
      theme: {
        background: '#1c1917',
        foreground: '#e8e4dc',
        cursor: '#e8e4dc',
        // 自绘滚动条的三档颜色（xterm 6 的 theme 项）：平时很淡，摸上去才明显。
        scrollbarSliderBackground: '#4b464033',
        scrollbarSliderHoverBackground: '#4b4640aa',
        scrollbarSliderActiveBackground: '#635d55cc',
      },
    });
    const f = new FitAddon();
    t.loadAddon(f); t.open(el); f.fit();
    term = t; termFit = f;
    // GPU 渲染：默认的 DOM 渲染器在 Retina 上敲字会发涩（每个字符都是 DOM 活儿）。
    // 必须 open() 之后再挂，且要能回退 —— WebView 里 WebGL 不一定可用，
    // 上下文丢失（切显卡/系统回收）也要退回 DOM，否则整个终端会变黑。
    try {
      const { WebglAddon } = await import('@xterm/addon-webgl');
      const gl = new WebglAddon();
      gl.onContextLoss(() => { try { gl.dispose(); } catch { /* 退回 DOM 渲染 */ } });
      t.loadAddon(gl);
    } catch { /* 没 WebGL 就用默认的 DOM 渲染器，功能不受影响 */ }
    const ch = new Channel<{ type: string; b64?: string; error?: string }>();
    ch.onmessage = (ev) => {
      if (term !== t) return; // 已被拆掉/换连接，丢弃尾包
      if (ev.type === 'data' && ev.b64) t.write(b64ToBytes(ev.b64));
      else if (ev.type === 'closed') {
        termOn = false;
        t.write(`\r\n\x1b[90m—— 连接已关闭${ev.error ? '：' + ev.error : ''}，按回车重新连接 ——\x1b[0m\r\n`);
      }
    };
    t.onData((d) => {
      // 关闭态：回车＝就地重连（iTerm/VS Code 的肌肉记忆），其余按键不再灌进死管道；
      // 连接中（termBusy）也别触发，免得连点回车叠一队重连。
      if (!termOn) {
        if (d.includes('\r') && !termBusy) void reconnectTerm();
        return;
      }
      void invoke('ssh_input', { connId: id, b64: strToB64(d) });
    });
    t.onResize(({ cols, rows }) => { void invoke('ssh_resize', { connId: id, cols, rows }); });
    // 「正在连接」用浮层（见 .term-connecting），**不往终端里写**：
    // 写进去就是终端缓冲的一部分，连上之后擦不干净（服务器的 banner 可能已经跟着来了，
    // 光标位置不由我们说了算）—— 那行字就会一直赖在最上面。浮层随 termBusy 自动收。
    try {
      await invoke('ssh_open_shell', { connId: id, cols: t.cols, rows: t.rows, onEvent: ch });
      termOn = true;
    } catch (e) {
      t.write(`\r\n\x1b[31m${String(e)}\x1b[0m\r\n`);
    } finally {
      if (term === t) termBusy = false; // 已被换掉的旧终端别去动当前状态
    }
  }
  async function reconnectTerm() {
    const id = activeConnId; const el = termEl;
    if (!id || !el || termBusy) return;
    termBusy = true; // 立刻转起来：ssh_close 也要等一下，别让这段时间看着像没反应
    try { await invoke('ssh_close', { connId: id }); } catch { /* 本来就没连上，照样重来 */ }
    await startTerm(id, el);
  }

  // ---------- 远程文件（SFTP，走终端同一条 SSH 会话） ----------
  // 视图＝可展开的树：sftpPath 是树根，每个目录的孩子按需加载后缓存在 sftpKids 里。
  type SftpEntry = { name: string; dir: boolean; link: boolean; perms: string; size: number; mtime: number };
  type SftpRow = SftpEntry & { path: string; depth: number };
  let sftpConnId = ''; // 已加载文件列表所属的连接；换连接才重置，切页签不重置
  let sftpPath = $state('');
  let sftpPathDraft = $state('');
  let sftpKids = $state<Record<string, SftpEntry[]>>({}); // 目录路径 → 它的孩子
  let sftpOpenDirs = $state(new Set<string>());
  let sftpLoading = $state<Record<string, boolean>>({});
  let sftpSel = $state('');
  let sftpSort = $state<{ key: 'name' | 'size' | 'mtime'; asc: boolean }>({ key: 'name', asc: true });
  let sftpBusy = $state(false);
  let sftpErr = $state('');
  let sftpXfer = $state(''); // 上传/下载进行中的提示文案
  // 首次开文件面板或换了连接 → 从 home 起步；关了再开则保留原目录。
  $effect(() => {
    const id = activeConnId;
    if (view !== 'remote' || !isSshConn || !wb.files || !id) return;
    if (sftpConnId === id) return;
    sftpConnId = id; sftpPath = ''; sftpPathDraft = ''; sftpErr = '';
    sftpKids = {}; sftpOpenDirs = new Set(); sftpSel = '';
    void sftpGo('');
  });
  function sftpJoin(dir: string, name: string): string { return dir === '/' ? '/' + name : dir + '/' + name; }
  function parentOf(p: string): string { return p.replace(/\/[^/]+\/?$/, '') || '/'; }
  function baseOf(p: string): string { return p.split('/').filter(Boolean).pop() || p; }
  function sortRows(list: SftpEntry[]): SftpEntry[] {
    const { key, asc } = sftpSort;
    const sign = asc ? 1 : -1;
    return [...list].sort((a, b) => {
      if (key === 'size') return sign * (a.size - b.size);
      if (key === 'mtime') return sign * (a.mtime - b.mtime);
      return sign * a.name.localeCompare(b.name, 'zh-Hans-CN', { numeric: true });
    });
  }
  function setSort(key: 'name' | 'size' | 'mtime') {
    sftpSort = sftpSort.key === key ? { key, asc: !sftpSort.asc } : { key, asc: true };
  }
  // ---- 列宽可拖（名称是弹性列，吃掉剩下的宽度）----
  // 手柄在每个定宽列的**左边缘**上：拖它就是拖这一列的左边，往右拖＝这列变窄，名称随之变宽。
  // 这样手柄始终跟着鼠标走，三根手柄的行为也一致。
  type ColKey = 'perm' | 'size' | 'date';
  const COL_MIN: Record<ColKey, number> = { perm: 62, size: 48, date: 92 };
  const COL_MAX: Record<ColKey, number> = { perm: 200, size: 160, date: 240 };
  const COL_DEF: Record<ColKey, number> = { perm: 96, size: 76, date: 128 };
  let colW = $state(loadColW());
  function loadColW(): Record<ColKey, number> {
    try {
      const raw = JSON.parse(localStorage.getItem('gcms.pilot.sftpCols') || '{}');
      const pick = (k: ColKey) => {
        const n = Number(raw[k]);
        return Number.isFinite(n) && n >= COL_MIN[k] && n <= COL_MAX[k] ? n : COL_DEF[k];
      };
      return { perm: pick('perm'), size: pick('size'), date: pick('date') };
    } catch { return { ...COL_DEF }; }
  }
  function saveColW() { try { localStorage.setItem('gcms.pilot.sftpCols', JSON.stringify(colW)); } catch { /* */ } }
  function setColW(k: ColKey, w: number) {
    colW = { ...colW, [k]: Math.min(COL_MAX[k], Math.max(COL_MIN[k], Math.round(w))) };
  }
  function startColDrag(e: MouseEvent, k: ColKey) {
    e.preventDefault();
    e.stopPropagation(); // 别让这一下变成「点了列头去排序」
    const x0 = e.clientX;
    const w0 = colW[k];
    const onMove = (ev: MouseEvent) => setColW(k, w0 - (ev.clientX - x0));
    const onUp = () => {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      document.body.style.cursor = '';
      saveColW();
    };
    document.body.style.cursor = 'col-resize';
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  }
  // 键盘也能调（手柄是个 button，Tab 得到）
  function onColKey(e: KeyboardEvent, k: ColKey) {
    const d = e.key === 'ArrowLeft' ? 8 : e.key === 'ArrowRight' ? -8 : 0;
    if (!d) return;
    e.preventDefault();
    setColW(k, colW[k] + d);
    saveColW();
  }
  // 树 → 扁平行（只把展开着的目录的孩子摊进来）。
  const sftpRows = $derived.by(() => {
    const out: SftpRow[] = [];
    const walk = (dir: string, depth: number) => {
      for (const e of sortRows(sftpKids[dir] ?? [])) {
        const p = sftpJoin(dir, e.name);
        out.push({ ...e, path: p, depth });
        if (e.dir && !e.link && sftpOpenDirs.has(p)) walk(p, depth + 1);
      }
    };
    if (sftpPath) walk(sftpPath, 0);
    return out;
  });
  /** 读一个目录的孩子进缓存。force=true 时无视缓存重读（改动后刷新用）。 */
  async function loadDir(path: string, force = false): Promise<boolean> {
    const id = sftpConnId;
    if (!force && sftpKids[path]) return true;
    sftpLoading = { ...sftpLoading, [path]: true };
    try {
      const list = await invoke<SftpEntry[]>('sftp_list', { connId: id, path });
      if (sftpConnId !== id) return false; // 加载途中换了连接，丢弃
      sftpKids = { ...sftpKids, [path]: list };
      return true;
    } catch (e) {
      if (path === sftpPath) sftpErr = String(e); else say(String(e), 'err');
      return false;
    } finally {
      sftpLoading = { ...sftpLoading, [path]: false };
    }
  }
  /** 换树根（path 为空＝去 home）。 */
  async function sftpGo(path: string) {
    const id = sftpConnId;
    sftpBusy = true; sftpErr = '';
    try {
      const p = path || await invoke<string>('sftp_home', { connId: id });
      if (sftpConnId !== id) return;
      sftpPath = p; sftpPathDraft = p; sftpSel = '';
      sftpOpenDirs = new Set();
      await loadDir(p, true);
    } catch (e) { sftpErr = String(e); }
    finally { sftpBusy = false; }
  }
  function sftpUp() {
    if (!sftpPath || sftpPath === '/') return;
    void sftpGo(parentOf(sftpPath));
  }
  // ---- 路径栏：面包屑（可点逐级跳）／点空白处切成输入框直接敲路径 ----
  const sftpCrumbs = $derived.by(() => {
    const out = [{ name: '/', path: '/' }];
    let acc = '';
    for (const p of sftpPath.split('/').filter(Boolean)) { acc += '/' + p; out.push({ name: p, path: acc }); }
    return out;
  });
  let sftpPathEdit = $state(false);
  let sftpPathEl = $state<HTMLInputElement | null>(null);
  let crumbEl = $state<HTMLElement | null>(null);
  function startPathEdit() { sftpPathDraft = sftpPath; sftpPathEdit = true; }
  $effect(() => { if (sftpPathEdit && sftpPathEl) { sftpPathEl.focus(); sftpPathEl.select(); } });
  // 路径深了就把尾巴（当前目录）滚进视野——面包屑是从左往右长的。
  $effect(() => {
    void sftpCrumbs;
    if (crumbEl) crumbEl.scrollLeft = crumbEl.scrollWidth;
  });
  function commitPath() {
    const p = sftpPathDraft.trim();
    sftpPathEdit = false;
    if (p && p !== sftpPath) void sftpGo(p);
  }
  /** 刷新：重读当前展开着的每一层，**保留展开状态**（sftpGo 是「换根」，会把整棵树收起来）。 */
  async function sftpRefresh() {
    if (!sftpPath || sftpBusy) return;
    sftpBusy = true; sftpErr = '';
    try {
      const dirs = [sftpPath, ...sftpOpenDirs];
      const ok = await Promise.all(dirs.map((d) => loadDir(d, true)));
      // 远端已经不在了的目录：连同展开态一起丢掉，别在树里留幽灵行
      const gone = dirs.filter((_, i) => !ok[i] && dirs[i] !== sftpPath);
      if (gone.length) {
        const n = new Set(sftpOpenDirs);
        const kids = { ...sftpKids };
        for (const d of gone) { n.delete(d); delete kids[d]; }
        sftpOpenDirs = n; sftpKids = kids;
      }
    } finally { sftpBusy = false; }
  }
  async function toggleDir(r: SftpRow) {
    const n = new Set(sftpOpenDirs);
    if (n.has(r.path)) { n.delete(r.path); sftpOpenDirs = n; return; }
    if (!(await loadDir(r.path))) return;
    n.add(r.path); sftpOpenDirs = n;
  }
  /** 试着把它当目录读一下。不是目录就 false —— 调用方退回按文件处理，不报错。 */
  async function isRemoteDir(path: string): Promise<boolean> {
    try { await invoke<SftpEntry[]>('sftp_list', { connId: sftpConnId, path }); return true; }
    catch { return false; }
  }
  /** 打开（双击 / 回车 / 右键「打开」）：目录＝**进到里面去**（换根，不是就地展开——展开是前面那个小箭头干的事）；
   *  软链＝先当目录试（sftp 的 readdir 给的是 lstat，指向目录的软链这里 dir=false）；文件＝在线编辑。 */
  async function sftpOpenRow(r: SftpRow) {
    if (r.dir && !r.link) { await sftpGo(r.path); return; }
    if (r.link && await isRemoteDir(r.path)) { await sftpGo(r.path); return; }
    sftpEdit(r.path);
  }
  function fmtSize(n: number): string {
    if (n < 1024) return `${n} B`;
    const u = ['KB', 'MB', 'GB', 'TB']; let v = n / 1024; let i = 0;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${u[i]}`;
  }
  function fmtStamp(secs: number): string {
    if (!secs) return '';
    const d = new Date(secs * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }
  // ---- 右键菜单 ----
  let fctxMenu = $state<null | { x: number; y: number; row: SftpRow | null }>(null);
  function openFctx(e: MouseEvent, row: SftpRow | null) {
    e.preventDefault();
    if (row) sftpSel = row.path;
    // 贴边时往回收，别让菜单跑出窗口
    fctxMenu = { x: Math.min(e.clientX, window.innerWidth - 190), y: Math.min(e.clientY, window.innerHeight - 240), row };
  }
  $effect(() => {
    if (!fctxMenu) return;
    const close = () => (fctxMenu = null);
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') fctxMenu = null; };
    // capture：菜单项自己的 onclick 先跑，再由这里收摊
    window.addEventListener('click', close);
    window.addEventListener('contextmenu', close);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('click', close);
      window.removeEventListener('contextmenu', close);
      window.removeEventListener('keydown', onKey);
    };
  });
  async function copyPath(p: string) {
    try { await navigator.clipboard.writeText(p); say('已复制路径'); }
    catch { say('复制失败，请手动选择复制', 'err'); }
  }
  // 新建文件夹 / 重命名共用一个「起名」弹窗。dir＝在哪个目录里操作。
  let fsAsk = $state<null | { mode: 'mkdir' | 'rename'; dir: string; from: string; value: string; busy: boolean; err: string }>(null);
  function openMkdir(dir = sftpPath) { fsAsk = { mode: 'mkdir', dir, from: '', value: '', busy: false, err: '' }; }
  function openRename(r: SftpRow) { fsAsk = { mode: 'rename', dir: parentOf(r.path), from: r.name, value: r.name, busy: false, err: '' }; }
  async function confirmFsAsk() {
    if (!fsAsk || fsAsk.busy) return;
    const v = fsAsk.value.trim();
    if (!v) return;
    if (v.includes('/')) { fsAsk.err = '名字里不能带 /'; return; }
    fsAsk.busy = true; fsAsk.err = '';
    const dir = fsAsk.dir;
    try {
      if (fsAsk.mode === 'mkdir') await invoke('sftp_mkdir', { connId: sftpConnId, path: sftpJoin(dir, v) });
      else await invoke('sftp_rename', { connId: sftpConnId, from: sftpJoin(dir, fsAsk.from), to: sftpJoin(dir, v) });
      fsAsk = null;
      await loadDir(dir, true);
    } catch (e) { if (fsAsk) { fsAsk.err = String(e); fsAsk.busy = false; } }
  }
  async function sftpDelete(r: SftpRow) {
    const isDir = r.dir && !r.link;
    const yes = await confirmDialog(
      isDir ? `删除文件夹「${r.name}」？只能删空文件夹。` : `删除「${r.name}」？此操作不可撤销。`,
      { title: '删除', kind: 'warning' },
    );
    if (!yes) return;
    try {
      await invoke('sftp_remove', { connId: sftpConnId, path: r.path, dir: isDir });
      await loadDir(parentOf(r.path), true);
    } catch (e) { say(String(e), 'err'); }
  }
  async function sftpDownload(r: SftpRow) {
    const local = await saveDialog({ defaultPath: r.name, title: `下载 ${r.name}` });
    if (!local) return;
    sftpXfer = `下载 ${r.name}…`;
    try { await invoke('sftp_download', { connId: sftpConnId, remote: r.path, local }); say(`已下载到 ${local}`); }
    catch (e) { say(String(e), 'err'); }
    finally { sftpXfer = ''; }
  }
  async function sftpUpload(dir = sftpPath) {
    const picked = await open({ multiple: true, title: '选择要上传的文件' });
    if (!picked) return;
    const paths = Array.isArray(picked) ? picked : [picked];
    for (const p of paths) {
      const name = p.split(/[\\/]/).pop() || p;
      sftpXfer = `上传 ${name}…`;
      try { await invoke('sftp_upload', { connId: sftpConnId, local: p, remote: sftpJoin(dir, name) }); }
      catch (e) { say(String(e), 'err'); sftpXfer = ''; return; }
    }
    sftpXfer = '';
    say(paths.length > 1 ? `已上传 ${paths.length} 个文件` : '已上传');
    await loadDir(dir, true);
  }
  // 在线编辑器
  let edOpen = $state(false);
  let edPath = $state('');
  let edText = $state('');
  let edLoading = $state(false);
  let edSaving = $state(false);
  let edErr = $state('');
  let edReadOnly = $state(false);
  async function sftpEdit(path: string) {
    edOpen = true; edPath = path; edText = ''; edErr = ''; edReadOnly = false; edLoading = true;
    try {
      const b64 = await invoke<string>('sftp_read', { connId: sftpConnId, path });
      // fatal 模式：不是合法 UTF-8（二进制）直接抛 TypeError → 只给下载不给编辑
      edText = new TextDecoder('utf-8', { fatal: true }).decode(b64ToBytes(b64));
    } catch (e) {
      edReadOnly = true;
      edErr = e instanceof TypeError ? '这是二进制文件，不能在线编辑；右键「下载」可以取回本地。' : String(e);
    } finally { edLoading = false; }
  }
  async function saveEdit() {
    if (edSaving || edReadOnly) return;
    edSaving = true; edErr = '';
    try {
      await invoke('sftp_write', { connId: sftpConnId, path: edPath, b64: strToB64(edText) });
      edOpen = false;
      say(`已保存 ${edPath}`);
      await loadDir(parentOf(edPath), true); // 大小/时间变了，刷新它所在的那层
    } catch (e) { edErr = String(e); }
    finally { edSaving = false; }
  }

  // ---------- 连接 Cloudflare ----------
  const CF_TOKEN_URL = 'https://dash.cloudflare.com/profile/api-tokens';
  let cfOpen = $state(false);
  let cfToken = $state('');
  let cfVerifying = $state(false);
  let cfConnecting = $state(false);
  let cfErr = $state('');
  let cfAccounts = $state<{ id: string; name: string }[]>([]);
  let cfZones = $state<{ id: string; name: string }[]>([]);
  let cfAccountId = $state('');
  let cfName = $state('');
  let cfPermsOpen = $state(false);
  function openCfConnect() { cfOpen = true; cfToken = ''; cfErr = ''; cfAccounts = []; cfZones = []; cfAccountId = ''; cfName = ''; cfPermsOpen = false; }
  async function verifyCf() {
    if (!cfToken.trim()) return;
    cfVerifying = true; cfErr = '';
    try {
      const v = await invoke<{ token_status: string; accounts: { id: string; name: string }[]; zones: { id: string; name: string }[] }>('verify_cf_token', { token: cfToken.trim() });
      cfAccounts = v.accounts; cfZones = v.zones;
      if (v.accounts.length === 1) cfAccountId = v.accounts[0].id;
      if (!cfName.trim() && v.accounts.length) cfName = (v.accounts.find((a) => a.id === cfAccountId) ?? v.accounts[0]).name;
      if (!v.accounts.length) cfErr = 'Token 有效，但没有可用账号（需要 Account 级权限）。';
    } catch (e) { cfErr = String(e); }
    finally { cfVerifying = false; }
  }
  async function confirmCf() {
    if (!cfAccountId) { cfErr = '请先选择一个账号'; return; }
    cfConnecting = true; cfErr = '';
    try {
      const conn = await invoke<Connection>('connect_cloudflare', { name: cfName.trim(), token: cfToken.trim(), accountId: cfAccountId });
      cfOpen = false; say(`已连接 Cloudflare「${conn.name}」`); await refreshConns(); await selectConn(conn.id);
    } catch (e) { cfErr = String(e); }
    finally { cfConnecting = false; }
  }

  // ---------- 模板库 ----------
  // builtin＝Pilot 随附的起始模板：删不掉（后端也拒），created_at 恒 0（列表里自然沉底）。
  type Template = { slug: string; name: string; desc: string; category: string; pages: number; created_at: number; builtin: boolean };
  let templates = $state<Template[]>([]);
  let templatesLoading = $state(false);
  let tmplHtml = $state<Record<string, string>>({}); // 每个模板的入口 HTML，做真实缩略图
  // 分类检索。随附模板的 category 由后端给；用户自己沉淀的没有分类（存模板时不问）——
  // 归到「自建」而不是硬塞进某一档，这既是实话，也正好是他们最想单独看的一组。
  const CAT_MINE = '自建';
  // 固定顺序：按出现顺序排的话，删一个模板就可能让整排筹码跳位。
  const CAT_ORDER = ['落地页', '内容', '作品集', '电商', '企业', '活动'];
  let tmplCat = $state('全部');
  const catOf = (t: Template) => (t.builtin ? t.category || '其他' : CAT_MINE);
  const tmplCats = $derived.by(() => {
    const has = new Set(templates.map(catOf));
    // 「自建」紧跟「全部」：它俩是「看什么范围」，后面那些才是「按页型筛」——
    // 同一类的摆一起，别让自建漂在页型清单的尾巴上。
    const out = has.has(CAT_MINE) ? [CAT_MINE] : [];
    out.push(...CAT_ORDER.filter((c) => has.has(c)));
    // 后端将来加了新分类，前端不用跟着改也不会把它漏掉
    for (const c of has) if (c !== CAT_MINE && !out.includes(c)) out.push(c);
    return out;
  });
  // 视图里只按分类筛。**按关键词找模板走全局搜索**（那里能一处搜到会话/任务/托管/模板，
  // 而不是每个视图各摆一个搜索框）。
  const tmplShown = $derived(templates.filter((t) => tmplCat === '全部' || catOf(t) === tmplCat));
  /** 模板文本索引：全局搜索按它匹配（名称 / 描述 / slug / 分类）。 */
  const tmplHay = (t: Template) => `${t.name} ${t.desc} ${t.slug} ${catOf(t)}`.toLowerCase();
  // 选中的分类可能整个消失（删掉最后一个自建模板）→ 那会停在一个永远空的筛选上，像坏了。
  $effect(() => {
    if (tmplCat !== '全部' && !tmplCats.includes(tmplCat)) tmplCat = '全部';
  });
  async function loadTemplates() {
    templatesLoading = true;
    try {
      templates = await invoke<Template[]>('list_templates');
      const map: Record<string, string> = {};
      await Promise.all(templates.map(async (t) => {
        try { map[t.slug] = await invoke<string>('template_index_html', { slug: t.slug }); } catch { map[t.slug] = ''; }
      }));
      tmplHtml = map;
    } catch (e) { say(String(e), 'err'); }
    finally { templatesLoading = false; }
  }
  function openTemplates() { view = 'templates'; activeConvId = ''; activeConv = null; loadTemplates(); }
  /** 全局搜索要搜模板，可 templates 本来只在模板库视图里才加载。这里**只补元数据**：
      loadTemplates 还会把 12 个模板的入口 HTML 全拉一遍（12 次 IPC × 几十 KB，用来做缩略图），
      为了搜索付这个代价不值当——缩略图等真进视图再说。 */
  async function ensureTemplateMeta() {
    if (activeConn?.kind !== 'cloudflare' || templates.length) return;
    try { templates = await invoke<Template[]>('list_templates'); } catch { /* 搜不到模板而已，别让它把搜索也带崩 */ }
  }
  /** 从全局搜索跳到某个模板卡：等它渲染出来再滚过去。
      openTemplates 里的 loadTemplates 是异步的，写死延时就是在赌它已经回来了——列表一慢就滚了个空。 */
  async function focusTemplate(slug: string) {
    for (let i = 0; i < 40; i++) { // 最多等 ~2s
      const el = document.getElementById(`tmpl-${slug}`);
      if (el) {
        el.scrollIntoView({ block: 'center', behavior: 'smooth' });
        el.classList.add('tmpl-flash');
        setTimeout(() => el.classList.remove('tmpl-flash'), 1400);
        return;
      }
      await new Promise((r) => setTimeout(r, 50));
    }
  }
  async function delTemplate(t: Template) {
    const yes = await confirmDialog(`删除模板「${t.name}」？`, { title: '删除模板', kind: 'warning' });
    if (!yes) return;
    try { await invoke('delete_template', { slug: t.slug }); await loadTemplates(); } catch (e) { say(String(e), 'err'); }
  }
  async function previewTemplate(t: Template) {
    say('正在启动模板预览…（约 2 秒后打开预览窗）');
    try { const u = await invoke<string>('cf_preview_template', { slug: t.slug }); say(`模板预览已打开（${portOf(u)}）·关掉预览窗即停`); } catch (e) { say(String(e), 'err'); }
  }
  // 存为模板（从 CF 会话）
  let saveTmplOpen = $state(false); let saveTmplName = $state(''); let saveTmplDesc = $state(''); let saveTmplBusy = $state(false); let saveTmplErr = $state('');
  function openSaveTmpl() { if (!activeConv) return; saveTmplName = activeConv.site_slug; saveTmplDesc = ''; saveTmplErr = ''; saveTmplOpen = true; }
  async function confirmSaveTmpl() {
    if (!activeConv || !saveTmplName.trim()) return;
    saveTmplBusy = true; saveTmplErr = '';
    try {
      await invoke('save_as_template', { connId: activeConv.conn_id, project: activeConv.site_slug, name: saveTmplName.trim(), desc: saveTmplDesc.trim() });
      saveTmplOpen = false; say('已存为模板');
    } catch (e) { saveTmplErr = String(e); }
    finally { saveTmplBusy = false; }
  }
  // 用模板建站
  let useTmplOpen = $state(false); let useTmplSlug = $state(''); let useTmplName = $state(''); let useTmplProject = $state(''); let useTmplBusy = $state(false); let useTmplErr = $state('');
  function openUseTmpl(t: Template) { useTmplSlug = t.slug; useTmplName = t.name; useTmplProject = ''; useTmplErr = ''; useTmplOpen = true; }
  async function confirmUseTmpl() {
    if (!useTmplProject.trim()) { useTmplErr = '给新项目起个名'; return; }
    if (!isCfConn) { useTmplErr = '请先在左下角切到一个 Cloudflare 连接'; return; }
    useTmplBusy = true; useTmplErr = '';
    try {
      const cid = activeConnId;
      const proj = useTmplProject.trim();
      await invoke('use_template', { slug: useTmplSlug, connId: cid, project: proj });
      useTmplOpen = false;
      say(`已用模板「${useTmplName}」建好项目，接着描述你要的定制吧`);
      activeConvId = ''; activeConv = null; view = 'launcher';
      await selectConn(cid); // 刷新项目列表（会清空 lSite）
      lSite = proj;
    } catch (e) { useTmplErr = String(e); }
    finally { useTmplBusy = false; }
  }
  // ---------- 提示词库 ----------
  // 预设（内置只读）+ 用户自建（存 localStorage）。点一下把内容填进启动页对话框开始建站。
  let userPrompts = $state<Prompt[]>(loadUserPrompts());
  let allPrompts = $derived<Prompt[]>([...PRESET_PROMPTS, ...userPrompts]);
  function openPrompts() { view = 'prompts'; activeConvId = ''; activeConv = null; }
  function usePrompt(p: Prompt) {
    lDraft = p.body;
    view = 'launcher';
    activeConvId = ''; activeConv = null;
    say(isCfConn ? '已填入建站需求，起个项目名就能开始' : '已填入需求，选好站点就能开始');
  }
  async function copyPrompt(p: Prompt) {
    try { await navigator.clipboard.writeText(p.body); say('已复制提示词'); }
    catch { say('复制失败，请手动选择复制', 'err'); }
  }
  // 新增 / 编辑（只对用户自建的）
  let promptEditOpen = $state(false);
  let promptEditId = $state('');   // '' = 新增
  let promptEditTitle = $state('');
  let promptEditBody = $state('');
  function openNewPrompt() { promptEditId = ''; promptEditTitle = ''; promptEditBody = ''; promptEditOpen = true; }
  function openEditPrompt(p: Prompt) { promptEditId = p.id; promptEditTitle = p.title; promptEditBody = p.body; promptEditOpen = true; }
  function savePrompt() {
    const title = promptEditTitle.trim(); const body = promptEditBody.trim();
    if (!title || !body) return;
    if (promptEditId) {
      userPrompts = userPrompts.map((p) => (p.id === promptEditId ? { ...p, title, body } : p));
    } else {
      userPrompts = [...userPrompts, { id: newPromptId(), title, body, created_at: Date.now() }];
    }
    if (!saveUserPrompts(userPrompts)) { say('保存失败：本地存储写入异常', 'err'); return; } // 存不下就别假装成功、保持弹窗开着
    promptEditOpen = false;
  }
  async function deletePrompt(p: Prompt) {
    const yes = await confirmDialog(`删除提示词「${p.title}」？`, { title: '删除提示词', kind: 'warning' });
    if (!yes) return;
    userPrompts = userPrompts.filter((x) => x.id !== p.id);
    if (!saveUserPrompts(userPrompts)) say('删除已生效，但本地存储写入异常，重启后可能恢复', 'err');
  }

  async function removeConn(id: string) {
    // 该连接下有对话正在跑一轮时不删：否则删掉会话行会让在途回合回写失败（「会话丢失」），子进程还在用已删的密钥/目录。
    const runningUnderConn = Object.values(running).includes(id);
    if (runningUnderConn || convos.some((c) => c.conn_id === id && c.status === 'running')) {
      say('该连接下有对话正在运行，请先点停止结束这一轮，再删除连接。', 'err');
      return;
    }
    const yes = await confirmDialog('删除这个连接？技能包目录、钥匙串密钥，以及该连接下的所有对话都会一并删除。', { title: '删除连接', kind: 'warning' });
    if (!yes) return;
    try {
      await invoke('remove_connection', { id });
      if (activeConnId === id) { activeConnId = ''; discovery = null; }
      if (activeConv?.conn_id === id) { activeConv = null; activeConvId = ''; view = 'launcher'; }
      await refreshConns();
      await refreshConvos();
    } catch (e) { say(String(e), 'err'); }
  }
  // 一键授权：开系统终端跑官方 CLI 登录命令（浏览器 OAuth 在终端流程里完成，Pilot 绝不代输凭据），
  // 随后进入「等待授权…」：每 4s 静默重检 brains，logged_in 翻 true 即复位绿灯；120s 超时自动复位。
  let authWaiting = $state<Brain | ''>('');
  let authTimer: ReturnType<typeof setInterval> | undefined;
  function stopAuthWait() { authWaiting = ''; if (authTimer) { clearInterval(authTimer); authTimer = undefined; } }
  async function authorize(b: Brain) {
    try {
      await invoke('open_brain_login', { brain: b });
      say('已打开终端，请在终端里完成登录——完成后这里自动变绿');
      authWaiting = b;
      const deadline = Date.now() + 120_000;
      if (authTimer) clearInterval(authTimer);
      authTimer = setInterval(() => {
        if (Date.now() > deadline) { stopAuthWait(); return; }
        void refreshBrains();
      }, 4000);
    } catch (e) { say(String(e), 'err'); }
  }
  $effect(() => {
    if (!authWaiting || !brains) return;
    const st = authWaiting === 'claude' ? brains.claude : authWaiting === 'codex' ? brains.codex : brains.grok;
    if (st.found && st.logged_in) { stopAuthWait(); say('授权完成，已就绪'); }
  });

  // ---------- 对话导航 ----------
  function newChat() {
    activeConvId = ''; activeConv = null; lDraft = '';
    // 远程连接没有启动页：新对话＝把底部对话面板腾空（工作台留在原地，终端不断）。
    // 它不换「页面」，所以必须给点看得见的反馈：把面板打开并把光标放进输入框。
    viewAfterConvGone();
    if (isSshConn) {
      if (!wb.chat) setWbFlags({ chat: true });
      requestAnimationFrame(() => wbStartEl?.focus());
    }
    if (!lSite && sites.length) lSite = sites[0].slug;
    if (!brainUsable(lBrain)) lBrain = firstUsableBrain();
  }
  async function openConv(id: string) {
    const c = await invoke<Conversation | null>('get_conversation', { id });
    if (!c) { await refreshConvos(); return; }
    // 打开的对话可能属于别的连接（从搜索/任务链接进来）——切到它自己的连接，否则侧栏会把它过滤掉。
    if (c.conn_id !== activeConnId) activeConnId = c.conn_id;
    activeConv = c; activeConvId = id; threadModel = c.model; threadPerm = c.perm_mode || 'full'; threadEffort = c.effort || '';
    // 远程连接的对话在工作台里开，不跳去独立对话页。
    // 命令行开着就让它开着 —— 面板开关是用户自己拨的，翻个旧对话不该替他改布局。
    if (conns.find((x) => x.id === c.conn_id)?.kind === 'ssh') {
      view = 'remote';
      if (!wb.chat) setWbFlags({ chat: true });
    } else view = 'thread';
    attachments = []; queued = null; // 换会话清掉未发送的附件 / 等待消息
    expandSite(c.site_slug);
    checkCfReady();
    scrollSoon(true);
  }
  async function deleteConv(id: string) {
    // 对话进行中不删：否则删掉会话行会孤儿掉后台在跑的 CLI 子进程 + 触发「会话丢失」。先停止再删。
    if (running[id]) { say('对话进行中，请先点停止再删除。', 'err'); return; }
    const c = convos.find((x) => x.id === id);
    const label = c?.title?.trim() ? `「${c.title.trim()}」` : '这条对话';
    const yes = await confirmDialog(`删除对话${label}？聊天记录不可恢复。`, { title: '删除对话', kind: 'warning' });
    if (!yes) return;
    try { await invoke('delete_conversation', { id }); if (activeConvId === id) { activeConvId = ''; activeConv = null; viewAfterConvGone(); } await refreshConvos(); } catch (e) { say(String(e), 'err'); }
  }

  // ---------- 运行一轮 ----------
  function makeChannel(convId: string): Channel<TurnEvent> {
    const ch = new Channel<TurnEvent>();
    ch.onmessage = (ev) => {
      const buf = lives[convId];
      if (!buf) return; // 该对话已结束/被清（切走后仍可能收到尾包）
      if (ev.type === 'delta') { buf.text += ev.text; if (activeConvId === convId) scrollSoon(); }
      else if (ev.type === 'tool') { buf.tools = [...buf.tools, { label: ev.label, detail: ev.detail }]; if (activeConvId === convId) scrollSoon(); }
      else if (ev.type === 'done') { if (!ev.ok) { buf.error = ev.error; buf.failed = true; } }
    };
    return ch;
  }
  function optimisticUser(text: string): Message {
    return { role: 'user', text, tools: [], ts: Math.floor(Date.now() / 1000), hidden: false, error: false };
  }
  // 乐观：立刻把用户消息塞进当前/合成会话，activeConvId 立即等于 convId，
  /** 当前对话没了（删掉/丢失/新建）之后该落到哪儿。
   *  ★ 远程连接永远留在工作台（底部面板自己退回起点输入框）——**别在各处硬写 view='launcher'**，
   *  那会把人踢出工作台、终端从眼前消失。ssh 连接压根没有启动页。 */
  function viewAfterConvGone() { view = isSshConn ? 'remote' : 'launcher'; }
  // 首轮也能流式渲染 + 停止（cancel 键对准真正的注册表 key）。
  function beginTurn(convId: string, optimistic: Conversation) {
    if (retryTimers[convId]) { clearTimeout(retryTimers[convId]); delete retryTimers[convId]; } // 任何新一轮开始都取消该会话待触发的自动重连
    lives[convId] = { text: '', tools: [], error: '', failed: false, startedAt: Date.now() };
    running[convId] = optimistic.conn_id;
    activeConv = optimistic; activeConvId = convId; threadModel = optimistic.model; threadPerm = optimistic.perm_mode || 'full'; threadEffort = optimistic.effort || '';
    cfReady = false; // 本轮跑完再重新判定是否已建出内容
    // 远程连接：对话就在工作台底部面板里跑，别跳去独立对话页——那样终端就从眼前消失了。
    // （openConv/newChat/selectConn 同理，见各自注释。这条是「发消息新建对话」这一路。）
    if (conns.find((c) => c.id === optimistic.conn_id)?.kind === 'ssh') {
      view = 'remote';
      if (!wb.chat) setWbFlags({ chat: true });
    } else {
      view = 'thread';
    }
    scrollSoon(true);
  }
  function endTurn(conv: Conversation | null, convId: string) {
    const failed = lives[convId]?.failed ?? false; // 删缓冲前先取，供等待消息门控用
    // 仅当用户仍停留在这条会话时用权威结果覆盖，避免打断已切到别的会话的用户。
    // 覆盖源按新鲜度择优：运行中改的档位/模型晚于轮末快照落库，刷新过的列表条目更全；
    // 但 refreshConvos 失败时列表是旧值（甚至 startChat 的乐观条目），无脑取列表会把
    // 刚跑完的整轮消息打回去——updated_at 更旧就回退用快照。
    if (conv && activeConvId === convId) {
      const inList = convos.find((x) => x.id === convId);
      const fresh = inList && inList.updated_at >= conv.updated_at ? inList : conv;
      activeConv = fresh; threadModel = fresh.model; threadPerm = fresh.perm_mode || 'full'; threadEffort = fresh.effort || '';
    }
    delete running[convId];
    delete lives[convId];
    if (activeConvId === convId) checkCfReady(); // 这轮可能写了文件，重新判定预览/部署是否可用
    // 等待消息：这轮**成功**结束、用户还在本会话，才把排队的那条发出去（失败/要重连时不发，避免连环失败）。
    let sentQueued = false;
    if (!failed && queued && queued.convId === convId && activeConvId === convId) {
      const q = queued; queued = null;
      draft = q.text; attachments = q.atts;
      queueMicrotask(() => { void send(); });
      sentQueued = true;
    }
    // 自动重建：claude 上下文 ≥90% 且本轮成功 → 直接换新会话续聊（带历史摘要，界面消息不丢）。
    // codex 没有真实上下文读数不触发；发了排队消息这轮先跑它，下轮收尾再检查（阈值有余量）。
    // 重建成功后 ctx 掉回摘要大小不会连环触发；重建失败走 failTurn 也不会循环。
    if (!sentQueued && !failed && conv && conv.brain === 'claude' && (conv.ctx_tokens ?? 0) >= ctxLimit('claude', conv.model ?? '') * 0.9) {
      if (activeConvId === convId) say('上下文接近上限，已自动重建续聊');
      queueMicrotask(() => { void rebuildSession(convId); });
    }
  }
  async function failTurn(e: unknown, convId: string) {
    delete running[convId];
    delete lives[convId];
    // 这轮失败：不自动发排队消息，把它放回输入框让用户决定。
    if (queued && queued.convId === convId && activeConvId === convId) { draft = queued.text; attachments = queued.atts; queued = null; }
    // 仅当用户仍在看这条会话时才弹错误提示——后台会话的失败不打断前台（成功也不弹，见 endTurn），
    // 失败态已落库，切回该会话即可看到。
    if (activeConvId === convId) say(String(e), 'err');
    await refreshConvos();
    const reloaded = await invoke<Conversation | null>('get_conversation', { id: convId });
    if (reloaded) { if (activeConvId === convId) activeConv = reloaded; }
    else if (activeConvId === convId) { activeConv = null; viewAfterConvGone(); }
  }

  async function startChat() {
    const multi = !isSshConn && lSites.length > 1;
    if ((!isSshConn && !multi && !lSite.trim()) || !lDraft.trim() || !brainUsable(lBrain)) return;
    const site = sites.find((s) => s.slug === lSite);
    const taskType = isSshConn ? 'remote' : isCfConn ? 'sitebuild' : lTask;
    prefs.brain = lBrain; prefs.model = lModel; prefs.taskType = lTask; prefs.perm = lPerm; prefs.effort = lEffort; savePrefs(prefs);
    const model = lModel;
    // CF 建站：把所选视觉风格的 tokens 指令拼进首条消息（可见、可追溯）。
    const styleDir = isCfConn && lStyle ? STYLE_DIRECTIVES[lStyle] : '';
    const text = lDraft.trim() + (styleDir ? `\n\n【视觉风格】${styleDir}` : '');
    const id = crypto.randomUUID();
    const now = Math.floor(Date.now() / 1000);
    // 远程连接没有站点：slug 留空，侧栏分组名用 user@host（那就是这台机器的身份）。
    const mSlug = multi || isSshConn ? '' : lSite;
    const mName = isSshConn ? `${activeConn?.ssh_user}@${activeConn?.ssh_host}` : multi ? `多站 · ${lSites.length} 站` : (site?.name || lSite);
    const mNames = multi ? lSites.map((sl) => sites.find((x) => x.slug === sl)?.name || sl) : [];
    const optimistic: Conversation = {
      id, conn_id: activeConnId, conn_name: activeConn?.name ?? '', site_slug: mSlug, site_name: mName, site_slugs: multi ? [...lSites] : [],
      task_type: taskType, brain: lBrain, model, perm_mode: lPerm, effort: lEffort, session_ref: '',
      title: text.slice(0, 30), messages: [optimisticUser(text)], status: 'running', created_at: now, updated_at: now,
    };
    // 立刻塞进侧栏，这样即便用户随后切走/新开对话，这条新会话也带着 running 圈可见，不必等这一轮跑完才出现。
    convos = [optimistic, ...convos];
    delete autoRetried[id];
    beginTurn(id, optimistic);
    lDraft = '';
    try {
      const conv = await invoke<Conversation>('start_conversation', {
        convId: id, connId: activeConnId, siteSlug: mSlug, siteName: mName,
        siteSlugs: multi ? lSites : [], siteNames: mNames,
        taskType, brain: lBrain, model, permMode: lPerm, effort: lEffort,
        message: text, onEvent: makeChannel(id),
      });
      await refreshConvos();
      const failed = lives[id]?.failed ?? false; const errText = lives[id]?.error ?? '';
      endTurn(conv, id);
      maybeAutoRetry(id, failed, errText);
    } catch (e) { await failTurn(e, id); }
  }

  // ---------- 附件：粘贴/拖拽文件到输入框，存进项目目录供 AI 读取 ----------
  type Attach = { name: string; path: string; preview: string };
  // 发消息时把这段追加到正文末尾让 AI 拿到路径；渲染时再从正文里拆出来单独做成附件卡（见 splitAttachments）。
  const ATT_MARKER = '附件（已存在项目里，可直接读取使用）：';
  // 助手消息里由后端补的 codex 生图产物清单（agent.rs append_generated_images），渲染成缩略图。
  const GEN_MARKER = '生成的图片：';
  // 从消息正文里拆出「真正的文字」和「路径列表」——存储的是完整正文，气泡里显示干净文字 + 图卡。
  function splitMarked(text: string, marker: string): { body: string; atts: string[] } {
    let cut = text.indexOf('\n\n' + marker);
    let sep = 2;
    if (cut < 0) {
      if (text.startsWith(marker)) { cut = 0; sep = 0; }
      else return { body: text, atts: [] };
    }
    const body = text.slice(0, cut);
    const atts = text.slice(cut + sep).split('\n').slice(1).map((l) => l.replace(/^-\s*/, '').trim()).filter(Boolean);
    return { body, atts };
  }
  function splitAttachments(text: string) { return splitMarked(text, ATT_MARKER); }
  function splitGenImages(text: string) { return splitMarked(text, GEN_MARKER); }
  function isImgPath(p: string): boolean { return /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i.test(p); }
  // 图片附件缩略图：消息里存的是相对工作目录的 uploads/ 路径，webview 读不了本地文件，
  // 走后端按「连接+项目」解析后读成 data URI；缓存键带上二者，避免不同会话同名文件串图。
  // data URI 可观（≤8MB 原图 → ~10MB 字符串），切会话时把其他会话的条目逐出，防止只增不减；
  // 顺带让上次读失败（''）的条目在重进会话时有重试机会。
  let thumbs = $state<Record<string, string>>({});
  const thumbJobs = new Map<string, Promise<string>>();
  function thumbKey(p: string): string { return (activeConv?.conn_id ?? '') + '|' + (activeConv?.site_slug ?? '') + '|' + p; }
  // 读工作目录内图片 → data URI（并发去重；失败缓存空串＝退回文件卡样式）。
  function loadWorkdirImg(connId: string, project: string, p: string): Promise<string> {
    const k = connId + '|' + project + '|' + p;
    if (thumbs[k] !== undefined) return Promise.resolve(thumbs[k]);
    let j = thumbJobs.get(k);
    if (!j) {
      j = invoke<string>('read_workdir_image', { connId, project, path: p })
        .then((d) => { thumbs = pruneThumbs({ ...thumbs, [k]: d }); return d; })
        .catch(() => { thumbs = { ...thumbs, [k]: '' }; return ''; })
        .finally(() => { thumbJobs.delete(k); });
      thumbJobs.set(k, j);
    }
    return j;
  }
  // 悬停/放大会把任意工作目录图片读进缓存：软上限 80 条，超了丢最早的（对象键序＝插入序），
  // 防止单会话里长期翻聊天记录把 data URI 越积越多。
  function pruneThumbs(t: Record<string, string>): Record<string, string> {
    const keys = Object.keys(t);
    if (keys.length <= 80) return t;
    for (const kk of keys.slice(0, keys.length - 80)) delete t[kk];
    return t;
  }
  function ensureThumb(connId: string, project: string, p: string) { void loadWorkdirImg(connId, project, p); }
  $effect(() => {
    const c = activeConv;
    if (!c) return;
    const prefix = c.conn_id + '|' + c.site_slug + '|';
    const stale = Object.keys(thumbs).filter((k) => !k.startsWith(prefix));
    if (stale.length) { const keep = { ...thumbs }; for (const k of stale) delete keep[k]; thumbs = keep; }
    for (const m of c.messages) {
      const atts = m.role === 'user' ? splitAttachments(m.text).atts : splitGenImages(m.text).atts;
      for (const p of atts) if (isImgPath(p)) ensureThumb(c.conn_id, c.site_slug, p);
    }
  });
  // 点缩略图看大图（点任意处/Esc 关闭）
  let lightbox = $state('');
  let attachments = $state<Attach[]>([]);
  let attaching = $state(false);
  async function attachFile(f: File) {
    if (!activeConv) { say('先进入一个对话再添加文件', 'err'); return; }
    if (f.size > 25 * 1024 * 1024) { say('文件太大（上限 25MB）', 'err'); return; }
    attaching = true;
    try {
      const buf = new Uint8Array(await f.arrayBuffer());
      const path = await invoke<string>('save_attachment', { connId: activeConv.conn_id, project: activeConv.site_slug, filename: f.name, data: Array.from(buf) });
      let preview = '';
      if (f.type.startsWith('image/')) {
        preview = await new Promise<string>((res) => { const r = new FileReader(); r.onload = () => res(String(r.result)); r.onerror = () => res(''); r.readAsDataURL(f); });
      }
      attachments = [...attachments, { name: path.split('/').pop() || f.name, path, preview }];
    } catch (e) { say(String(e), 'err'); }
    finally { attaching = false; }
  }
  function removeAttachment(i: number) { attachments = attachments.filter((_, idx) => idx !== i); }

  // 等待消息：这轮还在跑时先把下一条排进队列，等回合结束自动发；发出前可编辑/清除。绑定 convId，切走即清。
  let queued = $state<{ convId: string; text: string; atts: Attach[] } | null>(null);
  function queueMessage() {
    if (!activeConv) return;
    if (!draft.trim() && !attachments.length) return;
    if (!running[activeConv.id]) { void send(); return; } // 其实没在跑就直接发
    const t = draft.trim();
    // 已有排队则合并进同一条（不覆盖丢字）；否则新建。
    queued = queued && queued.convId === activeConv.id
      ? { convId: activeConv.id, text: [queued.text, t].filter(Boolean).join('\n'), atts: [...queued.atts, ...attachments] }
      : { convId: activeConv.id, text: t, atts: attachments };
    draft = ''; attachments = [];
  }
  function editQueued() {
    if (!queued) return;
    draft = queued.text; attachments = queued.atts; queued = null;
  }
  function clearQueued() { queued = null; }
  function onComposerPaste(e: ClipboardEvent) {
    const items = e.clipboardData?.items; if (!items) return;
    let handled = false;
    for (const it of items) { if (it.kind === 'file') { const f = it.getAsFile(); if (f) { attachFile(f); handled = true; } } }
    if (handled) e.preventDefault();
  }
  function onComposerDrop(e: DragEvent) {
    const files = e.dataTransfer?.files;
    if (files && files.length) { e.preventDefault(); for (const f of Array.from(files)) attachFile(f); }
  }
  function onComposerDragOver(e: DragEvent) { if (e.dataTransfer?.types?.includes('Files')) e.preventDefault(); }

  // ---------- 重试 / 自动重连 ----------
  // 错误文本像不像"瞬时问题"（网络/限流/过载/5xx）——只对这类做自动重试，避免对密钥错误/取消等硬错误反复重跑。
  function isTransient(err: string): boolean {
    if (!err || /已停止/.test(err)) return false;
    return /(网络|连接|超时|无法访问|timed?\s*out|timeout|overload|rate.?limit|too many requests|\b429\b|\b5\d\d\b|ECONN| ENET|EAI_AGAIN|socket hang|connection (reset|refused|closed|error)|temporar|try again|network error|fetch failed)/i.test(err);
  }
  // 重试上一轮：去掉失败的助手消息、用最后一条用户消息 resume 再跑（不新增用户气泡）。manual=用户手点（重置自动预算）。
  // ---------- 额度耗尽卡：倒计时 + 到点自动续跑 ----------
  let limitAuto = $state<Record<string, number>>({}); // convId → 触发时刻 ms
  let nowTick = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => {
      nowTick = Date.now();
      for (const [id, at] of Object.entries(limitAuto)) {
        if (nowTick >= at && !running[id]) {
          const rest = { ...limitAuto };
          delete rest[id];
          limitAuto = rest;
          void retry(id, false);
        }
      }
    }, 1000);
    return () => clearInterval(t);
  });
  function armLimitAuto(convId: string, resetSec: number) {
    limitAuto = { ...limitAuto, [convId]: resetSec * 1000 + 90_000 }; // 官方重置后再缓 90 秒
  }
  function disarmLimitAuto(convId: string) {
    const rest = { ...limitAuto };
    delete rest[convId];
    limitAuto = rest;
  }
  function fmtClock(sec: number): string {
    const d = new Date(sec * 1000);
    const hm = String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
    return new Date().toDateString() === d.toDateString() ? hm : `${d.getMonth() + 1}/${d.getDate()} ${hm}`;
  }
  function fmtRemain(ms: number): string {
    const m = Math.ceil(ms / 60000);
    if (m >= 60) return `${Math.floor(m / 60)} 小时 ${m % 60} 分`;
    if (m > 1) return `${m} 分钟`;
    return '不到 1 分钟';
  }
  function brainTitle(b?: string): string {
    return b === 'codex' ? 'Codex' : b === 'grok' ? 'Grok' : b === 'claude' ? 'Claude' : '模型';
  }

  async function retry(convId: string, manual = false) {
    if (running[convId]) return;
    const base = activeConvId === convId ? activeConv : convos.find((c) => c.id === convId);
    if (!base) return;
    if (!base.session_ref?.trim()) { if (manual) say('这个会话首轮就没建起来，无法重试；请回启动页重新发起。', 'err'); return; }
    if (manual) delete autoRetried[convId];
    const msgs = base.messages.slice();
    const prevErr = msgs.length && msgs[msgs.length - 1].role === 'assistant' && msgs[msgs.length - 1].error ? msgs[msgs.length - 1].text.trim() : '';
    if (msgs.length && msgs[msgs.length - 1].role === 'assistant') msgs.pop(); // 去掉失败/部分的那条
    beginTurn(convId, { ...base, messages: msgs, status: 'running' });
    try {
      const conv = await invoke<Conversation>('retry_turn', { convId, onEvent: makeChannel(convId) });
      await refreshConvos();
      const failed = lives[convId]?.failed ?? false; const errText = lives[convId]?.error ?? '';
      endTurn(conv, convId);
      // 手动重试后同错再败 → resume 无效，收起「重试」只留「重建继续」；成功则解除。
      if (manual && failed && prevErr && errText.trim() === prevErr) retryExhausted[convId] = true;
      if (!failed) delete retryExhausted[convId];
      maybeAutoRetry(convId, failed, errText);
    } catch (e) { await failTurn(e, convId); }
  }
  // 重建会话续跑：底层会话状态损坏、重试无效时——换全新会话（带历史摘要）原地继续这条对话。
  async function rebuildSession(convId: string) {
    if (running[convId]) return;
    const base = activeConvId === convId ? activeConv : convos.find((c) => c.id === convId);
    if (!base) return;
    delete autoRetried[convId];
    delete retryExhausted[convId];
    const msgs = base.messages.slice();
    if (msgs.length && msgs[msgs.length - 1].role === 'assistant') msgs.pop();
    beginTurn(convId, { ...base, messages: msgs, status: 'running' });
    try {
      const conv = await invoke<Conversation>('rebuild_session', { convId, onEvent: makeChannel(convId) });
      await refreshConvos();
      endTurn(conv, convId); // 重建后不做自动重试，避免坏环境下打转
    } catch (e) { await failTurn(e, convId); }
  }
  // 这轮若因瞬时问题失败且本轮还没自动重试过，隔几秒自动重连一次；再不行就停手（留手动「重试」）。
  function maybeAutoRetry(convId: string, failed: boolean, errText: string) {
    if (!failed || autoRetried[convId] || !isTransient(errText)) return;
    autoRetried[convId] = true;
    if (activeConvId === convId) say('网络/接口异常，正在自动重连重试…', 'err');
    // 存句柄，2.5s 后仅当没在跑且用户还停在本会话才自动重连（切走/又发新消息都不触发，避免抢焦点/陈旧重试）。
    retryTimers[convId] = setTimeout(() => {
      delete retryTimers[convId];
      if (!running[convId] && activeConvId === convId) void retry(convId, false);
    }, 2500);
  }

  async function send() {
    if (!activeConv || running[activeConv.id]) return;
    const atts = attachments;
    if (!draft.trim() && !atts.length) return;
    let text = draft.trim();
    if (atts.length) {
      text += (text ? '\n\n' : '') + ATT_MARKER + '\n' + atts.map((a) => `- ${a.path}`).join('\n');
    }
    draft = ''; attachments = [];
    const id = activeConv.id;
    delete autoRetried[id]; // 新的用户轮：重置自动重试预算
    delete retryExhausted[id];
    beginTurn(id, { ...activeConv, messages: [...activeConv.messages, optimisticUser(text)], status: 'running' });
    try {
      const conv = await invoke<Conversation>('send_message', { convId: id, message: text, onEvent: makeChannel(id) });
      await refreshConvos();
      const failed = lives[id]?.failed ?? false; const errText = lives[id]?.error ?? '';
      endTurn(conv, id);
      maybeAutoRetry(id, failed, errText);
    } catch (e) { await failTurn(e, id); }
  }

  async function stop() { if (running[activeConvId]) { try { await invoke('cancel_turn', { convId: activeConvId }); } catch { /* */ } } }

  function scrollSoon(force = false) {
    requestAnimationFrame(() => {
      if (!threadEl) return;
      const near = threadEl.scrollHeight - threadEl.scrollTop - threadEl.clientHeight < 160;
      if (force || near) threadEl.scrollTop = threadEl.scrollHeight;
    });
  }
  // 输入法组合中（候选框开着）按回车是确认候选词，不能当发送。isComposing 在部分 webview 不可靠，
  // 叠加手动组合标记 + keyCode===229（IME 处理中）双保险。
  let composing = false;
  function onComposerKey(e: KeyboardEvent, fn: () => void) {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !composing && e.keyCode !== 229) { e.preventDefault(); fn(); }
  }

  // ---------- 全局自定义 tips（替换原生 title 系统提示） ----------
  // 原生 title 出现慢、样式与应用割裂。首次悬停时把 title 搬进 data-tip（保留 aria-label
  // 供无障碍），统一由一个 fixed 浮层显示——不受侧栏等滚动容器裁剪。
  // disabled 按钮不触发鼠标事件 → 需要 tip 的禁用按钮外面包 .tipwrap 承载 data-tip。
  let tip = $state<{ x: number; y: number; text: string; below: boolean } | null>(null);
  let imgTip = $state<{ x: number; y: number; url: string; below: boolean } | null>(null);
  let imgTipReady = $state(false); // 图片 onload 之前浮层不可见，避免空白小块→大图的闪变
  let hoverWant = ''; // 正在等加载的悬停目标（工作目录图片异步读完时校验还悬停着才弹）
  function onTipHover(e: MouseEvent) {
    const tgt = e.target as HTMLElement;
    // 助手消息里的图片（行内代码路径或链接）→ 悬停缩略图浮层（点击见 mdClick）
    const holder = tgt.closest?.('a, code') as HTMLElement | null;
    if (holder && holder.closest('.text.md') && !holder.closest('pre')) {
      const isLink = holder.tagName === 'A';
      const s = isLink ? (holder.getAttribute('href') ?? '') : (holder.textContent ?? '').trim();
      // 远程图片：完整 URL（链接/代码都认），或行内代码里的 /uploads/ 站点路径
      let u = '';
      if (/^https?:/i.test(s)) {
        if (IMG_EXT_RE.test(s.replace(/[?#].*$/, '')) && !badImgUrls.has(s)) u = s;
      } else if (!isLink) {
        u = codeImgUrl(s);
      }
      // 工作目录内图片（相对/绝对/file: 都认）→ data URI（负缓存空串＝确认不是可预览图片，当普通元素处理）
      let rel = '';
      if (!u) { const r = relWorkPath(s) || workAbsPath(s); if (r && isImgPath(r)) rel = r; }
      const k = rel ? thumbKey(rel) : '';
      const known = rel ? thumbs[k] : undefined;
      if (u || (rel && known !== '')) {
        const r0 = holder.getBoundingClientRect();
        const below = r0.top < 230; // 上方放不下缩略图就翻到下方
        const x = Math.min(Math.max(r0.left + r0.width / 2, 150), window.innerWidth - 150);
        const place = { x, y: below ? r0.bottom + 8 : r0.top - 8, below };
        holder.style.cursor = 'zoom-in';
        tip = null;
        if (u || known) {
          hoverWant = ''; // 立即可显示：作废先前挂着的异步加载，防止旧图旧坐标顶掉当前浮层
          const src = u || (known as string);
          if (!imgTip || imgTip.url !== src) imgTipReady = false;
          imgTip = { ...place, url: src };
        } else {
          const c = activeConv;
          imgTip = null;
          if (c) {
            hoverWant = k;
            loadWorkdirImg(c.conn_id, c.site_slug, rel).then((d) => {
              if (d && hoverWant === k) { imgTipReady = false; imgTip = { ...place, url: d }; }
            });
          }
        }
        return;
      }
      holder.style.cursor = ''; // 负缓存命中/失效时把之前设过的手势复位
    }
    hoverWant = '';
    imgTip = null;
    const el = tgt.closest?.('[title], [data-tip]') as HTMLElement | null;
    if (!el) { tip = null; return; }
    const t = el.getAttribute('title');
    if (t) {
      el.removeAttribute('title');
      el.setAttribute('data-tip', t);
      if (!el.getAttribute('aria-label') && !el.textContent?.trim()) el.setAttribute('aria-label', t);
    }
    const text = el.getAttribute('data-tip') ?? '';
    if (!text) { tip = null; return; }
    const r = el.getBoundingClientRect();
    const below = r.top < 64; // 贴近顶部时改为在元素下方显示
    const x = Math.min(Math.max(r.left + r.width / 2, 120), window.innerWidth - 120);
    tip = { x, y: below ? r.bottom + 7 : r.top - 7, text, below };
  }

  // ---------- 右键菜单（原生） ----------
  // 替换 WKWebView 默认英文菜单（含 Inspect Element / AutoFill 等）。
  //
  // ★ 这里**必须交给原生菜单**，不能前端自己画：自己画就得用 navigator.clipboard.readText()
  // 去粘贴，而 macOS 14 起「程序读剪贴板」会弹一个系统「Paste」确认按钮 —— 用户点完我们的
  // 「粘贴」还得再点一次系统的，看着就像坏了，而且 JS 绕不过去（系统只看到有人要读剪贴板，
  // 看不到用户刚点过菜单，那正是它要防的）。见 lib.rs::show_edit_menu。
  // 启用态（没选中就灰掉复制等）也由系统自己管，不用我们猜。
  function onCtxMenu(e: MouseEvent) {
    e.preventDefault();
    const t = e.target as HTMLElement;
    const editable = t.closest('textarea, input[type="text"], input:not([type])') as HTMLElement | null;
    const sel = window.getSelection()?.toString() ?? '';
    if (!editable && !sel) return; // 空白处不出菜单
    // 先聚焦：原生「剪切/复制/粘贴」作用在**当前第一响应者**上，不聚焦就打空
    if (editable) editable.focus();
    void invoke('show_edit_menu', { editable: !!editable }).catch(() => {});
  }

  // ---------- 助手消息 Markdown 渲染 ----------
  // 模型输出的 **粗体**/列表/表格/代码块 之前是裸文本，读感远不如 Claude 客户端。
  // marked(GFM+breaks) 渲染 → DOMPurify 消毒（模型会读网页/站点内容，注入面必须过滤）→ {@html}。
  marked.setOptions({ gfm: true, breaks: true });
  // 在 DOMPurify 默认协议白名单上放行 file:/sandbox:（AI 常用它们链接本地产物；默认会把
  // href 整个剥掉，链接看着能点实际是死的）。javascript: 等仍被拦。
  const MD_URI_RE = /^(?:(?:(?:f|ht)tps?|mailto|tel|callto|sms|cid|xmpp|file|sandbox):|[^a-z]|[a-z+.\-]+(?:[^a-z+.\-:]|$))/i;
  // file: 只为 <a> 放行（点击全被 mdClick 代理接管）；<img> src 收紧回 http(s)/data:image，
  // 防 file: 子资源直连本地文件系统。
  DOMPurify.addHook('afterSanitizeAttributes', (n) => {
    if (n.tagName === 'IMG') {
      const src = n.getAttribute('src') || '';
      if (!/^(https?:|data:image\/)/i.test(src)) n.removeAttribute('src');
    }
  });
  function mdRender(text: string): string {
    return DOMPurify.sanitize(marked.parse(text, { async: false }) as string, { ALLOWED_URI_REGEXP: MD_URI_RE });
  }
  // Markdown 里 <a> 的点击代理：拦下 webview 内导航，改用系统浏览器打开。
  // 行内代码若是图片路径（如 `/uploads/xx.svg`）：点击也用浏览器打开完整地址。
  function mdClick(e: MouseEvent) {
    const t = e.target as HTMLElement;
    // 链接分支不看选区且永远 preventDefault——点链接是明确意图，而且 WebKit 里点 <a> 不清除
    // 页面残留选区，若因选区早退会放任 webview 自导航（本函数存在的意义就是拦它）。
    const a = t.closest('a');
    if (a) {
      e.preventDefault();
      const raw = a.getAttribute('href') ?? '';
      if (/^https?:/i.test(raw)) { openUrl(raw); return; }
      if (/^(mailto|tel):/i.test(raw)) { openUrl(raw); return; } // gfm 会把裸邮箱自动变 mailto 链接
      // 工作目录内的路径（相对 / 绝对 / file:）：图片→应用内放大；其他文件→文件管理器里定位
      const p = relWorkPath(raw) || workAbsPath(raw);
      if (p && isImgPath(p)) { openWorkdirLightbox(p); return; }
      if (p) { void revealWorkdir(p); return; }
      if (raw && raw !== '#') say('这个链接打不开（不是网址，也不是工作目录里的文件）', 'err');
      return;
    }
    const code = t.closest('code');
    if (code && !code.closest('pre')) {
      if (window.getSelection()?.toString()) return; // 用户在拖选复制路径文本：不劫持成打开动作
      const s = (code.textContent ?? '').trim();
      const u = codeImgUrl(s);
      if (u) { e.preventDefault(); openUrl(u); return; }
      const rel = relWorkPath(s) || workAbsPath(s);
      if (rel && isImgPath(rel)) { e.preventDefault(); openWorkdirLightbox(rel); return; }
      // 非图片的行内代码：必须带路径分隔符才当文件处理（`package.json`、`v1.2.3` 这类
      // 纯提及不劫持——否则点一下就弹「没找到文件」的噪音）。
      if (rel && rel.includes('/') && /\.[a-z0-9]{1,8}$/i.test(rel)) { e.preventDefault(); void revealWorkdir(rel); }
    }
  }

  // 工作目录相对路径（消息里提到的项目内文件）：无协议、不以 / 开头、无空白、不出目录。
  function relWorkPath(s: string): string {
    const v = s.trim().replace(/^\.\//, '');
    if (!v || /\s/.test(v) || /^[a-z][a-z0-9+.-]*:/i.test(v) || v.startsWith('/') || v.startsWith('#') || v.includes('..')) return '';
    return v;
  }
  // 本地绝对路径 / file: / sandbox: 链接 → 绝对路径字符串。只做形态识别；
  // 「必须在工作目录内」的边界由后端 canonicalize 后强制，越界一律拒绝。
  function workAbsPath(s: string): string {
    let v = s.trim();
    if (/^(file|sandbox):/i.test(v)) {
      try { v = decodeURIComponent(v.replace(/^file:\/\/(localhost)?/i, '').replace(/^sandbox:(\/\/)?/i, '')); }
      catch { return ''; }
    }
    if (!v || v.length > 1024) return '';
    if (!(v.startsWith('/') || /^[a-zA-Z]:[\\/]/.test(v))) return '';
    return v;
  }
  function openWorkdirLightbox(p: string) {
    const c = activeConv;
    if (!c) return;
    // 点击是明确意图：之前失败过（负缓存空串）的条目给重试机会——文件可能刚被生成出来
    const k = thumbKey(p);
    if (thumbs[k] === '') { const t2 = { ...thumbs }; delete t2[k]; thumbs = t2; }
    loadWorkdirImg(c.conn_id, c.site_slug, p).then((d) => {
      if (d) lightbox = d;
      else say('预览失败：文件不存在、超过 8MB，或在工作目录之外', 'err');
    });
  }
  async function revealWorkdir(p: string) {
    const c = activeConv;
    if (!c) return;
    try {
      const abs = await invoke<string>('resolve_workdir_file', { connId: c.conn_id, project: c.site_slug, path: p });
      await revealItemInDir(abs);
    } catch { say('打不开：文件不存在（可能还没生成或已移动），或在工作目录之外', 'err'); }
  }

  // ---------- 行内代码图片预览 ----------
  // 助手常回报「已上传 → /uploads/xx.svg」这类路径；把它变成可看的：悬停缩略图、点击打开。
  // 只认站点约定的 /uploads/ 路径且要求已知站点公开地址（服务端对拼不出公开地址的站点刻意
  // 返回空 url，前端不猜接口域名）；完整 http(s) 图片 URL 也认。其余（本地绝对路径、构建
  // 产物路径）一律不当图片，避免拼出必 404 的链接。加载失败的 URL 进负缓存不再反复尝试。
  const IMG_EXT_RE = /\.(png|jpe?g|gif|webp|svg|bmp|avif|ico)$/i;
  const badImgUrls = new Set<string>();
  function codeImgUrl(t: string): string {
    const s = t.trim();
    if (!s || /\s/.test(s) || !IMG_EXT_RE.test(s)) return '';
    let u = '';
    if (/^https?:\/\//i.test(s)) u = s;
    else if (s.startsWith('/uploads/')) {
      const base = (activeSiteUrl || '').replace(/\/+$/, '');
      if (base) u = base + s;
    }
    return u && !badImgUrls.has(u) ? u : '';
  }

  // linkify + 段落（用户消息/错误消息仍走纯文本路径）
  const urlRe = /(https?:\/\/[^\s)"'`」』】>，。；：！？、（）《》]+)/g;
  function segs(text: string): { t: string; link: boolean }[] {
    const out: { t: string; link: boolean }[] = [];
    let last = 0; let m: RegExpExecArray | null; urlRe.lastIndex = 0;
    while ((m = urlRe.exec(text))) {
      if (m.index > last) out.push({ t: text.slice(last, m.index), link: false });
      out.push({ t: m[0], link: true }); last = m.index + m[0].length;
    }
    if (last < text.length) out.push({ t: text.slice(last), link: false });
    return out;
  }

  function taskLabel(t: string): string { return t === 'sitebuild' ? '新站建设' : t === 'article' ? '内容创作' : t === 'remote' ? '远程运维' : '自由对话'; }
  function brainLabel(b: string): string { return b === 'codex' ? 'Codex' : b === 'grok' ? 'Grok' : 'Claude'; }
  function fmt(secs: number): string { return new Date(secs * 1000).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }); }
  function fmtSched(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString('zh-CN', { weekday: 'short', month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }
  // 排期三层：站点筛选 chips + 月度密度条 + 按天分组
  let schedSiteFilter = $state('');
  /** 排期标题包含过滤（纯前端；只由全局搜索「在排期中查找」预填，视图内以小标签形式展示/清除）。 */
  let schedTitleQ = $state('');
  // 切出排期视图即清空标题过滤，避免下次进来还挂着旧过滤。
  $effect(() => { if (view !== 'schedule') schedTitleQ = ''; });
  const schedFiltered = $derived.by(() => {
    let list = schedSiteFilter ? sched.filter((x) => x.site_slug === schedSiteFilter) : sched;
    const tq = schedTitleQ.trim().toLowerCase();
    if (tq) list = list.filter((x) => (x.title || '').toLowerCase().includes(tq));
    return list;
  });
  const schedSites = $derived([...new Set(sched.map((x) => x.site_slug))]);
  function dayKey(ms: number): string { const d = new Date(ms); return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`; }
  function dayLabel(ms: number): string {
    const t0 = new Date(); t0.setHours(0, 0, 0, 0); const day = t0.getTime();
    if (ms < day - 864e5) return new Date(ms).toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric', weekday: 'short' });
    if (ms < day) return '昨天';
    if (ms < day + 864e5) return '今天';
    if (ms < day + 2 * 864e5) return '明天';
    return new Date(ms).toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric', weekday: 'short' });
  }
  /** 密度条往回看几天。后端只带回最近一周的已发布（PUBLISHED_LOOKBACK_DAYS），
   *  这里跟它对齐——多画的格子只会永远是空的。 */
  const SCHED_PAST_DAYS = 7;
  // 密度条：过去一周（已发布）+ 未来 6 周（待发布），每天一格
  const schedDensity = $derived.by(() => {
    const t0 = new Date(); t0.setHours(0, 0, 0, 0);
    const counts = new Map<string, number>();
    for (const it of schedFiltered) {
      const ms = new Date(it.published_at).getTime();
      if (!isNaN(ms)) counts.set(dayKey(ms), (counts.get(dayKey(ms)) ?? 0) + 1);
    }
    return Array.from({ length: SCHED_PAST_DAYS + 42 }, (_, i) => {
      const ms = t0.getTime() + (i - SCHED_PAST_DAYS) * 864e5;
      const d = new Date(ms);
      const n = counts.get(dayKey(ms)) ?? 0;
      const rel = i - SCHED_PAST_DAYS;
      const when = rel === 0 ? '今天 ' : rel === -1 ? '昨天 ' : '';
      return {
        key: dayKey(ms), count: n,
        tip: `${when}${d.getMonth() + 1}/${d.getDate()} · ${n} 条${rel < 0 ? '（已发布）' : ''}`,
        today: rel === 0, past: rel < 0,
      };
    });
  });
  function jumpToDay(key: string) {
    document.getElementById('sched-day-' + key)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
  /** 排期条目的真实访问链接 —— **只有已发布的才有**。
   *  `it.url` 是相对路径（`/zh/posts/xxx`，见 api.go::apiContentURL），得配 discovery 里那个站的
   *  域名才成完整地址；站点没绑域名（site.url 空）就拼不出来，回 null 让调用方退回草稿预览。 */
  function schedLiveUrl(it: ScheduledItem): string | null {
    if (it.status !== 'published' || !it.url) return null;
    const base = sites.find((s) => s.slug === it.site_slug)?.url;
    if (!base) return null;
    try { return new URL(it.url, base).toString(); } catch { return null; }
  }
  /** 「预览」点下去开什么：已发布 → 真实访问链接；待发布 → 短期草稿链接（公开 URL 还打不开）。 */
  async function openSchedItem(it: ScheduledItem) {
    const live = schedLiveUrl(it);
    if (live) { void openUrl(live); return; }
    try {
      const u = await invoke<string>('scheduled_preview_url', { connId: activeConnId, siteSlug: it.site_slug, id: it.id });
      void openUrl(u);
    } catch (e) { say(String(e), 'err'); }
  }
  /** 待发布的时间要带日期 —— 只写「09:30」时，滚到第三屏就完全不知道是哪天的了。
   *  当天的省掉日期（分组标题已经写着「今天」，再重复一遍是噪音）。 */
  function schedWhen(it: ScheduledItem): string {
    const d = new Date(it.published_at);
    if (isNaN(d.getTime())) return '';
    const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
    const t0 = new Date(); t0.setHours(0, 0, 0, 0);
    const sameDay = d >= t0 && d.getTime() < t0.getTime() + 864e5;
    return sameDay ? hm : `${d.getMonth() + 1}/${d.getDate()} ${hm}`;
  }
  const schedGroups = $derived.by(() => {
    const now = Date.now();
    const overdue: ScheduledItem[] = [];
    const byDay = new Map<string, { key: string; label: string; items: ScheduledItem[] }>();
    for (const it of schedFiltered) {
      const ms = new Date(it.published_at).getTime();
      // ★ 已发布的时间必然在过去 —— 不先认状态就会被下面那条「过期」规则整批扫走，
      //   于是「昨天已发布」全挤进「已过期」，等于白拉。
      if (it.status === 'published') {
        const k = dayKey(ms);
        let g = byDay.get(k);
        if (!g) { g = { key: k, label: dayLabel(ms), items: [] }; byDay.set(k, g); }
        g.items.push(it);
        continue;
      }
      // 待发布却已经过了点 = 服务端没发出去，单独拎出来
      if (isNaN(ms) || ms < now) { overdue.push(it); continue; }
      const k = dayKey(ms);
      let g = byDay.get(k);
      if (!g) { g = { key: k, label: dayLabel(ms), items: [] }; byDay.set(k, g); }
      g.items.push(it);
    }
    // 按天升序：昨天的在今天前面（Map 的插入序不可靠——数据是按 published_at 排的，
    // 但已发布/待发布两条分支会把顺序打乱）
    const out = [...byDay.values()].sort((a, b) => Date.parse(a.items[0].published_at) - Date.parse(b.items[0].published_at));
    if (overdue.length) out.unshift({ key: 'overdue', label: '待发布 · 已过点未发出', items: overdue });
    return out;
  });

  const shownMessages = $derived((activeConv?.messages ?? []).filter((m) => !m.hidden));

  // 下拉选项
  const siteOpts = $derived(sites.map((s) => ({ value: s.slug, label: s.name || s.slug, sub: s.url ? hostOf(s.url) : '未绑定域名', img: s.favicon || s.logo || faviconGuess(s.url) })));
  // ---------- 多站会话（启动器）----------
  // lSites 非空 = 多站模式；「全部站点」是当下站点集的快照（新增站点不自动混进旧会话）。
  let lSites = $state<string[]>([]);
  const launcherSiteOpts = $derived.by(() => {
    const base = siteOpts.filter((o) => !lSites.includes(o.value));
    if (isCfConn || sites.length < 2) return base;
    const head = [{ value: '__all__', label: `全部站点 · ${sites.length} 个`, sub: '跨站会话：统计 / 巡检 / 批量' }];
    if (lSites.length) head.unshift({ value: '__multi__', label: `多站 · ${lSites.length} 个站点`, sub: '继续选择可再添加' });
    return [...head, ...base];
  });
  function onLauncherSitePick(v: string) {
    if (v === '__all__') { lSites = sites.map((s) => s.slug); lSite = '__multi__'; return; }
    if (v === '__multi__') return;
    if (lSites.length) { if (!lSites.includes(v)) lSites = [...lSites, v]; lSite = '__multi__'; }
  }
  function removeLauncherSite(slug: string) {
    lSites = lSites.filter((s) => s !== slug);
    if (lSites.length === 1) { lSite = lSites[0]; lSites = []; }
    else if (!lSites.length) { lSite = sites[0]?.slug ?? ''; }
  }
  // 单站模式下点「+多站」：把当前站作为第一个胶囊进入多站模式
  function enterMultiMode() {
    if (!lSites.length && lSite && lSite !== '__multi__') { lSites = [lSite]; lSite = '__multi__'; }
  }
  // 多站会话的站点清单 tooltip（名称 · 域名，每行一个）
  function convSitesTip(c: Conversation | null): string {
    if (!c || (c.site_slugs?.length ?? 0) < 2) return '';
    return (c.site_slugs ?? []).map((sl, i) => {
      const st = sites.find((x) => x.slug === sl);
      return `${c.site_names?.[i] || sl} · ${st?.url ? hostOf(st.url) : sl}`;
    }).join('\n');
  }
  // 侧栏「跨站会话」分组：当前连接所有多站会话的站点并集
  const multiGroupInfo = $derived.by(() => {
    const seen = new Map<string, string>();
    for (const c of convos) {
      if (c.conn_id !== activeConnId || (c.site_slugs?.length ?? 0) < 2) continue;
      c.site_slugs!.forEach((sl, i) => { if (!seen.has(sl)) seen.set(sl, c.site_names?.[i] || sl); });
    }
    const tip = [...seen.entries()].map(([sl, n]) => {
      const st = sites.find((x) => x.slug === sl);
      return `${n} · ${st?.url ? hostOf(st.url) : sl}`;
    }).join('\n');
    return { count: seen.size, tip };
  });
  // Claude 档位 = 别名（--model sonnet/opus/haiku），永远指向该档「当前最新」，
  // 厂商发新版自动跟随、无需更新客户端。sub 版本号仅「当前实际版本」提示，可能滞后。
  // Fable 例外：claude 2.1.96 尚无 fable 别名（实测报错），只能用全 ID，出新版需更新这里。
  const CLAUDE_MODELS = [
    { value: 'sonnet', label: 'Sonnet', sub: '性价比 · 当前 Sonnet 5' },
    { value: 'opus', label: 'Opus', sub: '最强 · 当前 Opus 4.8' },
    { value: 'claude-fable-5', label: 'Fable', sub: 'Claude 5 家族 · Fable 5' },
    { value: 'haiku', label: 'Haiku', sub: '最快 · 当前 Haiku 4.5' },
  ];
  // Codex 档位 = 具体模型 ID（-c model=）。首项「跟随 Codex 默认」= 不覆盖本地 codex 配置。
  // 型号取自本机 codex 模型清单，会随厂商更新；要用别的新模型走下方「自定义模型 ID」。
  const CODEX_MODELS = [
    { value: '', label: '跟随 Codex 默认', sub: '用本地 codex 配置' },
    // GPT-5.6 三分档（2026-07 GA）：裸 "gpt-5.6" 在 ChatGPT 订阅通道会被拒，必须用分档 ID；
    // 且要求 codex CLI ≥0.144（旧版报「requires a newer version of Codex」，升级 CLI 即可）。
    { value: 'gpt-5.6-sol', label: 'GPT-5.6 Sol', sub: '最强 · 细节打磨' },
    { value: 'gpt-5.6-terra', label: 'GPT-5.6 Terra', sub: '日常主力' },
    { value: 'gpt-5.6-luna', label: 'GPT-5.6 Luna', sub: '最快最省' },
    { value: 'gpt-5.5', label: 'GPT-5.5', sub: '上一代' },
    { value: 'gpt-5.4', label: 'GPT-5.4', sub: '上一代' },
    { value: 'gpt-5.4-mini', label: 'GPT-5.4-Mini', sub: '上一代 · 最省' },
  ];
  // Grok（订阅通道）目前只开放 grok-4.5 一档（登录后 `grok models` 实测清单）；
  // 首项「跟随默认」= 不传 --model，厂商换默认自动跟随。其他新模型走「自定义模型 ID」。
  const GROK_MODELS = [
    { value: '', label: '跟随 Grok 默认', sub: '当前 Grok 4.5' },
    { value: 'grok-4.5', label: 'Grok 4.5', sub: '500k 上下文' },
  ];
  function modelOptsFor(b: string) { return b === 'codex' ? CODEX_MODELS : b === 'grok' ? GROK_MODELS : CLAUDE_MODELS; }
  // launcher / 会话里可选：预设档位 + 该厂商的全局自定义模型 ID（定时任务表单仍只用预设 + 自己的 modelCustom）。
  function launcherModelOpts(b: string) { return [...modelOptsFor(b), ...customsOf(b).map((id) => ({ value: id, label: id, sub: '自定义' }))]; }
  function defaultModelFor(b: string): string { return b === 'claude' ? 'sonnet' : ''; }
  function isLauncherModel(b: string, m: string): boolean { return launcherModelOpts(b).some((o) => o.value === m); }

  // 会话按「站点 → 任务类型」两级分组：站点按最近活动倒序；任务类型固定顺序，只留有会话的。
  const TASK_ORDER = ['article', 'sitebuild', 'free', 'remote'];
  const grouped = $derived.by(() => {
    const bySite = new Map<string, { slug: string; name: string; recent: number; convs: Conversation[] }>();
    for (const c of convos) {
      if (c.conn_id !== activeConnId) continue; // 侧栏只显示当前连接的对话，别串场
      const multi = (c.site_slugs?.length ?? 0) > 1;
      const key = multi ? '__multi__' : (c.site_slug || '(未指定站点)');
      let g = bySite.get(key);
      if (!g) { g = { slug: key, name: multi ? '跨站会话' : (c.site_name || c.site_slug || key), recent: 0, convs: [] }; bySite.set(key, g); }
      g.convs.push(c);
      if (c.updated_at > g.recent) g.recent = c.updated_at;
    }
    const groups = [...bySite.values()].sort((a, b) => b.recent - a.recent);
    return groups.map((s) => {
      const subs: { type: string; label: string; items: Conversation[] }[] = [];
      for (const t of TASK_ORDER) {
        const items = s.convs.filter((c) => c.task_type === t).sort((a, b) => b.updated_at - a.updated_at);
        if (items.length) subs.push({ type: t, label: taskLabel(t), items });
      }
      const others = s.convs.filter((c) => !TASK_ORDER.includes(c.task_type)).sort((a, b) => b.updated_at - a.updated_at);
      if (others.length) subs.push({ type: 'other', label: '其它', items: others });
      // 分组头的域名：CF 从项目文件探测；gcms 用 discovery 里站点自带的公开地址。
      return { slug: s.slug, name: s.name, count: s.convs.length, subs, url: isCfConn ? detectSiteUrl(s.convs) : (sites.find((x) => x.slug === s.slug)?.url ?? '') };
    });
  });
  // CF 页脚：有域名（已部署）的项目算「站点」。
  const cfSiteCount = $derived(grouped.filter((g) => g.url).length);
  function faviconOf(u: string): string { try { return new URL(u).origin + '/favicon.ico'; } catch { return ''; } }
  // 站点分组折叠态（持久化）；默认全展开，打开某会话时自动展开其站点。
  let collapsedSites = $state(loadCollapsedSites());
  function loadCollapsedSites(): Set<string> { try { return new Set<string>(JSON.parse(localStorage.getItem('gcms.pilot.collapsedSites') || '[]')); } catch { return new Set(); } }
  function persistCollapsedSites() { try { localStorage.setItem('gcms.pilot.collapsedSites', JSON.stringify([...collapsedSites])); } catch { /* */ } }
  function toggleSite(slug: string) { const n = new Set(collapsedSites); if (n.has(slug)) n.delete(slug); else n.add(slug); collapsedSites = n; persistCollapsedSites(); }
  function expandSite(slug: string) { if (collapsedSites.has(slug)) { const n = new Set(collapsedSites); n.delete(slug); collapsedSites = n; persistCollapsedSites(); } }
  // 侧栏会话时间标签：今天 / 昨天 / N 天前 / 更早日期。
  function relTime(secs: number): string {
    const t0 = new Date(); t0.setHours(0, 0, 0, 0);
    const day = t0.getTime(); const ms = secs * 1000;
    if (ms >= day) return '今天';
    if (ms >= day - 864e5) return '昨天';
    const n = Math.floor((day - ms) / 864e5) + 1;
    if (n <= 7) return `${n} 天前`;
    const d = new Date(ms); return `${d.getMonth() + 1}/${d.getDate()}`;
  }
</script>

<svelte:window oncontextmenu={onCtxMenu} onmouseover={onTipHover} onscrollcapture={() => { tip = null; imgTip = null; hoverWant = ''; }} onresize={() => { tip = null; imgTip = null; hoverWant = ''; }} onkeydown={(e) => { if (e.key === 'Escape' && lightbox) lightbox = ''; }} />
<main class="app" class:win={isWindows} class:fs={isFullscreen} class:rail-collapsed={railCollapsed}>
  <!-- 融合式标题栏：透明拖拽条铺满顶部，红绿灯与工具按钮浮在其上（macOS Overlay） -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="titlebar" data-tauri-drag-region aria-hidden="true" onmousedown={startDrag} style="width:{railCollapsed ? 140 : railWidth}px"></div>

  <!-- 顶部工具：折叠侧栏 + 搜索会话（窗口模式紧挨红绿灯右侧；全屏时无红绿灯，与左栏菜单左对齐） -->
  <div class="win-tools" class:fs={isFullscreen} class:win={isWindows}>
    <button class="wt" onclick={toggleRail} title={railCollapsed ? '展开侧栏' : '折叠侧栏'} aria-label="折叠侧栏">
      <svg width="16" height="16" viewBox="0 0 18 18" fill="none">
        <rect x="2.5" y="3.5" width="13" height="11" rx="2" stroke="currentColor" stroke-width="1.3" />
        <path d="M7 3.7v10.6" stroke="currentColor" stroke-width="1.3" />
        <rect x="3.4" y="4.4" width="2.7" height="9.2" rx="1" fill="currentColor" opacity=".28" />
      </svg>
    </button>
    <button class="wt" onclick={openSearch} disabled={!activeConn} title="搜索会话" aria-label="搜索会话">
      <svg width="16" height="16" viewBox="0 0 18 18" fill="none">
        <circle cx="8" cy="8" r="5" stroke="currentColor" stroke-width="1.4" />
        <path d="M11.7 11.7L15 15" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
      </svg>
    </button>
    {#if updAvail}
      <button class="wt upd" onclick={runUpdate} disabled={updBusy}
        title={updBusy ? (updMsg || '更新中…') : `有新版本 ${updAvail}，点击下载并更新`} aria-label="有可用更新">
        {#if updBusy}
          <svg class="upd-spin" width="15" height="15" viewBox="0 0 18 18" fill="none"><path d="M15.5 9a6.5 6.5 0 1 1-2-4.72" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
        {:else}
          <svg width="15" height="15" viewBox="0 0 18 18" fill="none">
            <path d="M9 3v7.4M5.8 7.2 9 10.4l3.2-3.2" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
            <path d="M4.2 14.3h9.6" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
          </svg>
          <span class="upd-dot"></span>
        {/if}
      </button>
    {/if}
  </div>

  {#if updBusy}
    <div class="upd-toast" role="status">
      <svg class="upd-spin" width="13" height="13" viewBox="0 0 18 18" fill="none"><path d="M15.5 9a6.5 6.5 0 1 1-2-4.72" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg>
      <span class="upd-toast-msg">{updMsg}</span>
      {#if updPct >= 0}<span class="upd-toast-bar"><span style="width:{updPct}%"></span></span>{/if}
    </div>
  {/if}

  <!-- 左栏 -->
  <aside class="rail" class:collapsed={railCollapsed} style="width:{railWidth}px">
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div class="rail-resize" title="拖动调整宽度" onmousedown={startResize} role="separator" aria-orientation="vertical"></div>
    <div class="rail-head">
      <button class="newchat" onclick={newChat} disabled={!activeConn} title="新对话">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
          <path d="M11.5 2.5l2 2L6 12l-2.5.5L4 10l7.5-7.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
          <path d="M2.5 13.5h11" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
        </svg>
        新对话
      </button>
      {#if !isCfConn && !isSshConn}
      <button class="railnav {view === 'schedule' ? 'on' : ''}" onclick={openSchedule} disabled={!activeConn}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
          <rect x="2.5" y="3" width="11" height="10.5" rx="1.5" stroke="currentColor" stroke-width="1.3" />
          <path d="M2.5 6h11M5.5 2v2M10.5 2v2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
        </svg>
        排期
      </button>
      <button class="railnav {view === 'tasks' ? 'on' : ''}" onclick={openTasks} disabled={!activeConn}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="5.5" stroke="currentColor" stroke-width="1.3" />
          <path d="M8 5v3l2 1.5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        定时任务
      </button>
      <button class="railnav {view === 'managed' ? 'on' : ''}" onclick={openManaged} disabled={!activeConn}>
        {@render botIcon(14)}
        托管
      </button>
      {/if}
      {#if isCfConn}
      <button class="railnav {view === 'templates' ? 'on' : ''}" onclick={openTemplates}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
          <rect x="2.5" y="2.5" width="4.5" height="4.5" rx="1" stroke="currentColor" stroke-width="1.3" />
          <rect x="9" y="2.5" width="4.5" height="4.5" rx="1" stroke="currentColor" stroke-width="1.3" />
          <rect x="2.5" y="9" width="4.5" height="4.5" rx="1" stroke="currentColor" stroke-width="1.3" />
          <rect x="9" y="9" width="4.5" height="4.5" rx="1" stroke="currentColor" stroke-width="1.3" />
        </svg>
        模板库
      </button>
      <button class="railnav {view === 'prompts' ? 'on' : ''}" onclick={openPrompts}>
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
          <path d="M3 3h10a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H6.5L4 13.2V11H3a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1Z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
          <path d="M5.2 6.2h5.6M5.2 8.4h3.4" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
        </svg>
        提示词
      </button>
      {/if}
    </div>

    <div class="convos" class:scrolled={convosScrolled} onscroll={(e) => (convosScrolled = (e.currentTarget as HTMLElement).scrollTop > 2)}>
      {#if convos.length === 0}
        <p class="rail-empty">还没有对话。<br />选好站点和模型，直接说你想做什么。</p>
      {/if}
      {#each grouped as g (g.slug)}
        <button class="site-grp" onclick={() => toggleSite(g.slug)} title={g.name}>
          <svg class="site-grp-chev" class:collapsed={collapsedSites.has(g.slug)} width="10" height="10" viewBox="0 0 12 12" fill="none"><path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
          {#if isSshConn}{@render sshMark(13)}{:else if isCfConn}{#if g.url}<img class="site-grp-fav" src={faviconOf(g.url)} alt="" onerror={(e) => ((e.currentTarget as HTMLImageElement).style.display = 'none')} />{/if}{:else}<SiteFav src={siteFav(g.slug)} label={g.slug} size={14} />{/if}
          <span class="site-grp-name">{g.name}</span>
          {#if g.slug === '__multi__' && multiGroupInfo.count}<span class="site-grp-host" data-tip={multiGroupInfo.tip}>{multiGroupInfo.count} 个站点</span>{/if}
          {#if g.url}<span class="site-grp-host" title={hostOf(g.url)}>{hostOf(g.url)}</span>{/if}
        </button>
        {#if !collapsedSites.has(g.slug)}
          {#each g.subs as sub (sub.type)}
            {#each sub.items as c (c.id)}
              <div class="convo {activeConvId === c.id ? 'on' : ''}" role="button" tabindex="0"
                onclick={() => openConv(c.id)} onkeydown={(e) => e.key === 'Enter' && openConv(c.id)}>
                <div class="convo-body">
                  <span class="convo-bi" title={brainLabel(c.brain)}><BrainIcon brain={c.brain} size={11} /></span>
                  <span class="convo-title">{c.title}</span>
                  {#if c.status === 'running' || running[c.id]}<span class="mini-run"></span>{/if}
                  {#if permConvs.has(c.id)}<span class="mini-permit" title="有操作等你批准">待批</span>{/if}
                  <span class="convo-when">{relTime(c.updated_at)}</span>
                </div>
                <button class="convo-x" title="删除对话" onclick={(e) => { e.stopPropagation(); deleteConv(c.id); }}>×</button>
              </div>
            {/each}
          {/each}
        {/if}
      {/each}
    </div>

    <div class="foot-wrap" bind:this={footerEl}>
      {#if switcherOpen}
        <div class="conn-switch">
          {#each conns as c (c.id)}
            <button class="cs-item {activeConnId === c.id ? 'on' : ''}" onclick={() => { selectConn(c.id); switcherOpen = false; }}
              oncontextmenu={(e) => openConnCtx(e, c)}>
              {#if c.kind === 'cloudflare'}{@render cfMark(18)}{:else if c.kind === 'ssh'}{@render sshMark(18)}{:else}<SiteMark size={18} />{/if}
              <span class="cs-main"><b>{c.name}{#if packUpdates[c.id]}<span class="pack-dot" title="技能包有新版，去「连接与模型设置」一键升级"></span>{/if}</b>
                {#if c.kind === 'ssh'}
                  <small class="cs-os">{@render distroMark(distroOf(c), 12)}{sshSub(c)}</small>
                {:else}
                  <small>{c.key_prefix} · {c.kind === 'cloudflare' ? 'Cloudflare' : c.key_kind === 'gcmsp_' ? '平台' : '单站'}</small>
                {/if}</span>
              {#if activeConnId === c.id}<span class="cs-check">✓</span>{/if}
            </button>
          {/each}
          <!-- 没连接时上面是空的，别拿一条分隔线开头 -->
          {#if conns.length}<div class="cs-div"></div>{/if}
          <button class="cs-act" onclick={() => { switcherOpen = false; importPack(); }}>{@render plusIcon()}导入 gcms 技能包</button>
          <button class="cs-act" onclick={() => { switcherOpen = false; openCfConnect(); }}>{@render cfMark(15)}连接 Cloudflare</button>
          <button class="cs-act" onclick={() => { switcherOpen = false; openSshConnect(); }}>{@render sshMark(15)}新建远程连接</button>
          <button class="cs-act" onclick={() => { switcherOpen = false; setupOpen = true; }}>{@render gearIcon()}连接与模型设置…</button>
        </div>
      {/if}
    <!-- 空态也走切换器（不再直跳设置）：菜单里三条连接路径 + 设置一次性摆出，和有连接时同一套交互。 -->
    <button class="rail-foot" class:open={switcherOpen} onclick={() => { switcherOpen = !switcherOpen; }}>
      {#if isCfConn}{@render cfMark(18)}{:else if isSshConn}{@render sshMark(18)}{:else if activeConn && sites.length}<SiteFav src={siteFav(sites[0].slug)} label={sites[0].slug} size={18} />{:else}<AppIcon size={18} />{/if}
      <span class="foot-main">
        <b>{activeConn?.name ?? '未连接'}</b>
        <!-- 未连接时不出副文案：中间大区域已有导入/连接引导，这里越安静越好（原来那句「点此导入技能包」
             既和点击行为对不上，又只提了三种连接方式里的一种，已去掉）。 -->
        {#if activeConn}<small>{#if isCfConn}{cfProjects.length} 个项目{cfSiteCount ? ` · ${cfSiteCount} 个站点` : ''}{:else if isSshConn}<!--
          远程连接没有「站点」这回事：这里放远端系统版本（点一下重探，比如刚 do-release-upgrade 过）。
        --><span class="foot-os" role="button" tabindex="-1" title="远端系统 · 点击重新检测"
          onclick={(e) => { e.stopPropagation(); probeOs(activeConnId, true); }}
          onkeydown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); probeOs(activeConnId, true); } }}
          >{@render distroMark(distroOf(activeConn), 12)}{osProbing[activeConnId] ? '检测系统…' : sshSub(activeConn)}</span>{:else}{sites.length} 个站点<span class="foot-rfz" role="button" tabindex="-1" title="刷新站点（技能包新增站点后点这里）"
          onclick={(e) => { e.stopPropagation(); refreshSites(); }}
          onkeydown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); refreshSites(); } }}>{@render refreshIcon(discoveryLoading)}</span>{/if}</small>{/if}
      </span>
      <!-- 永远是展开箭头（空态也一样）：这是个切换器，不是设置快捷键。 -->
      <svg class="foot-chev" class:up={switcherOpen} width="13" height="13" viewBox="0 0 12 12" fill="none">
        <path d="M2.75 7.5L6 4.25L9.25 7.5" stroke="currentColor" stroke-width="1.15" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </button>
    </div>
  </aside>

  <!-- 主区 -->
  <section class="main">
    {#if flash}<div class="flash {flashKind}">{flash}</div>{/if}

    {#if tip}
      <div class="tipbox" class:below={tip.below} style="left:{tip.x}px; top:{tip.y}px">{tip.text}</div>
    {/if}
    {#if imgTip}
      {@const iu = imgTip.url}
      <div class="imgtip" class:ready={imgTipReady} class:below={imgTip.below} style="left:{imgTip.x}px; top:{imgTip.y}px">
        <img src={iu} alt="" onload={() => { if (imgTip?.url === iu) imgTipReady = true; }} onerror={() => { badImgUrls.add(iu); if (imgTip?.url === iu) imgTip = null; }} />
      </div>
    {/if}
    {#if lightbox}
      <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
      <div class="lightbox" onclick={() => (lightbox = '')}><img src={lightbox} alt="附件大图" /></div>
    {/if}

    {#if !activeConn}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="center" data-tauri-drag-region onmousedown={startDrag}>
        <div class="hero-card">
          <div class="hero-mark"><AppIcon size={54} /></div>
          <h1>开始之前，先连接一个来源</h1>
          <p>导入 gcms 技能包为你的站点做内容，或连接 Cloudflare 让本地 Claude / Codex / Grok 帮你建站并部署。</p>
          <div class="hero-btns">
            <button class="btn soft hero-import" onclick={importPack} disabled={importBusy}>{@render plusIcon()}{importBusy ? '导入中…' : '导入技能包'}</button>
            <button class="btn soft hero-import" onclick={openCfConnect}>{@render cfMark(15)}连接 Cloudflare</button>
          </div>
          {#if brains}
            <div class="cli-guide" data-no-drag>
              <div class="cli-guide-h"><span>本地 CLI 准备（至少装好并登录一个）</span><button class="cli-redetect" onclick={refreshBrainsManual} disabled={brainsBusy} title="装好 / 登录完点这里重新检测（会重新读取 PATH）">{@render refreshIcon(brainsBusy)}<span>重新检测</span></button></div>
              {#each [{ b: 'claude' as Brain, st: brains.claude, name: 'Claude Code', cmd: CLAUDE_INSTALL_CMD }, { b: 'codex' as Brain, st: brains.codex, name: 'Codex', cmd: 'npm i -g @openai/codex' }, { b: 'grok' as Brain, st: brains.grok, name: 'Grok', cmd: GROK_INSTALL_CMD }] as r (r.b)}
                <div class="cli-row">
                  <BrainIcon brain={r.b} size={16} />
                  <span class="cli-name">{r.name}</span>
                  {#if !r.st.found}
                    <span class="cli-tag bad">未安装</span>
                    {#if r.b === 'claude'}
                      <button class="wr-btn" use:tipAction={'安装失败排障：VPN/代理需覆盖 claude.ai 与下载域名 storage.googleapis.com（规则模式加进规则或临时切全局）；也可复制右侧命令到终端手动执行看完整输出。'} onclick={installClaude} disabled={claudeInstalling}>{#if claudeInstalling}<span class="wr-spin"></span>{nodeBoot || `安装中 ${elapsedLabel(claudeElapsed)}`}{:else}一键安装{/if}</button>
                    {:else if r.b === 'grok'}
                      <button class="wr-btn" onclick={installGrok} disabled={grokInstalling}>{#if grokInstalling}<span class="wr-spin"></span>{nodeBoot || `安装中 ${elapsedLabel(grokElapsed)}`}{:else}一键安装{/if}</button>
                    {:else if r.b === 'codex'}
                      <button class="wr-btn" onclick={installCodex} disabled={codexInstalling}>{#if codexInstalling}<span class="wr-spin"></span>{nodeBoot || `安装中 ${elapsedLabel(codexElapsed)}`}{:else}一键安装{/if}</button>
                    {/if}
                    <code class="cli-cmd" title={r.cmd}>{r.cmd}</code>
                    <button class="cli-copy" title="复制命令" aria-label="复制安装命令" onclick={() => copyCmd(r.cmd)}>
                      {#if copiedCmd === r.cmd}
                        <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M3.5 8.5l3 3 6-7" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /></svg>
                      {:else}
                        <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><rect x="5.4" y="5.4" width="8.2" height="8.2" rx="1.6" stroke="currentColor" stroke-width="1.3" /><path d="M10.6 5.4V4.1A1.6 1.6 0 0 0 9 2.5H4.1A1.6 1.6 0 0 0 2.5 4.1V9a1.6 1.6 0 0 0 1.6 1.6h1.3" stroke="currentColor" stroke-width="1.3" /></svg>
                      {/if}
                    </button>
                  {:else if r.st.logged_in === false}
                    <span class="cli-tag warn">未登录</span><button class="authbtn" onclick={() => authorize(r.b)} disabled={authWaiting === r.b}>{#if authWaiting === r.b}<span class="wr-spin"></span>等待授权…{:else}去授权 ↗{/if}</button>
                  {:else}
                    <span class="cli-tag ok">✓ {r.st.version || '已就绪'}</span>
                  {/if}
                </div>
              {/each}
              <p class="cli-note"><svg class="cli-note-ic" width="13" height="13" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.4" stroke="currentColor" stroke-width="1.3" /><path d="M8 7.3v3.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><circle cx="8" cy="4.8" r="0.95" fill="currentColor" /></svg><span>安装/登录后状态灯自动变绿；密钥只进 {keystoreName}，绝不落盘。</span></p>
              <p class="cli-note"><svg class="cli-note-ic" width="13" height="13" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.4" stroke="currentColor" stroke-width="1.3" /><path d="M8 7.3v3.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><circle cx="8" cy="4.8" r="0.95" fill="currentColor" /></svg><span>没有 Node 的机器，一键安装会自动下载托管版 Node(v22 LTS) 到应用数据目录，并写入用户级 PATH（只追加、绝不覆盖或截断；写入失败会提示手动配置）。</span></p>
              {#if !brains.browser.found}
                <p class="cli-note"><svg class="cli-note-ic" width="13" height="13" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.4" stroke="currentColor" stroke-width="1.3" /><path d="M1.9 8h12.2M8 1.6c-4.4 4.2-4.4 8.6 0 12.8 4.4-4.2 4.4-8.6 0-12.8Z" stroke="currentColor" stroke-width="1.1" /></svg><span>未检测到 Chrome / Edge——「AI 网页截图配图」不可用（可选功能，装个 Chrome 即启用）。</span></p>
              {/if}
            </div>
          {/if}
        </div>
      </div>

    {:else if view === 'launcher'}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="center launch-center" data-tauri-drag-region onmousedown={startDrag}>
        <div class="launcher">
          {#if isCfConn}
            <h1>让 AI 帮你建个站</h1>
            <p class="sub">给项目起个名，描述你想要的网站——边聊边建，随时本地预览，满意了再部署到 Cloudflare。</p>
          {:else}
            <h1>想让它帮你做点什么？</h1>
            <p class="sub">选好站点和模型，像聊天一样把需求说清楚，它会边聊边把事情做掉。</p>
            <div class="task-seg">
              {#each [['article', '内容创作', '策划并撰写文章'], ['sitebuild', '新站建设', '从零搭建整个站点'], ['free', '自由对话', '任意内容运营']] as t (t[0])}
                <button class:on={lTask === t[0]} onclick={() => (lTask = t[0] as TaskType)}>
                  {@render taskIcon(t[0])}
                  <span class="ts-txt"><b>{t[1]}</b><small>{t[2]}</small></span>
                </button>
              {/each}
            </div>
          {/if}

          <div class="composer big">
            <textarea bind:value={lDraft} bind:this={lDraftEl} use:autogrow rows="3"
              placeholder={isSshConn ? '例如：看看这台机器的配置和已装的东西，然后帮我装上 Docker' : isCfConn ? '例如：做个卖手冲咖啡的落地页，深色调，留个邮箱订阅表单存到 D1，先给我方案' : lTask === 'sitebuild' ? '例如：帮我搭一个介绍露营装备的中文站，风格轻松，先给我一个方案' : '例如：帮我写一篇 2026 年 macOS 效率工具盘点，先列个提纲'}
              oncompositionstart={() => (composing = true)} oncompositionend={() => (composing = false)}
              onkeydown={(e) => onComposerKey(e, startChat)}></textarea>
            <div class="composer-bar">
              <div class="cb-left">
                {#if isSshConn}
                  <!-- 远程连接没有站点/项目可选：对象就是那台机器 -->
                  <span class="ssh-target">{@render sshMark(13)}{activeConn?.ssh_user}@{activeConn?.ssh_host}:{activeConn?.ssh_port}</span>
                {:else if isCfConn}
                  <input class="cf-proj-in" bind:value={lSite} placeholder="项目名，如 coffee-landing" spellcheck="false" autocapitalize="off" autocorrect="off" />
                  <Dropdown compact bind:value={lStyle} options={STYLE_OPTS} />
                {:else}
                  <Dropdown compact searchable bind:value={lSite} options={launcherSiteOpts} placeholder="选择站点" onchange={onLauncherSitePick} />
                  {#if !lSites.length && sites.length > 1}<button class="multi-add" title="多站会话：同时操作多个站点" onclick={enterMultiMode}>+多站</button>{/if}
                {/if}
              </div>
              <div class="cb-right">
                <Dropdown compact bind:value={lPerm} options={permOptsFor(lBrain, isSshConn)} tone={permTone(lPerm)} tip={permTipFor(lBrain, lPerm, isSshConn)} />
                <ModelFx options={comboOpts} value={`${lBrain}::${lModel}`} effort={lEffort} onpick={pickCombo} oneffort={(v: string) => { lEffort = v; prefs.effort = v; savePrefs(prefs); }} />
                <UsageRing />
                <button class="send" onclick={startChat} disabled={(!isSshConn && !lSite.trim() && lSites.length < 2) || !lDraft.trim() || !brainUsable(lBrain)} title="发送（Enter）">↑</button>
              </div>
            </div>
          </div>
<!-- 远程连接的档位警告不在这儿了：全都长在权限下拉的选项/触发器 tip 上（见 permTipFor）。
               挂在档位本身＝用户**拨之前**就读得到；挂在下面＝拨到底了才弹，亡羊补牢。 -->
          {#if lSites.length}
            <div class="tsites launcher-sites">
              {#each lSites as sl (sl)}
                <span class="tsite-chip"><SiteFav src={siteFav(sl)} label={sl} size={13} />{sites.find((x) => x.slug === sl)?.name || sl}<button type="button" aria-label="移除站点" onclick={() => removeLauncherSite(sl)}>×</button></span>
              {/each}
            </div>
          {/if}
          {#if !brainUsable(lBrain)}<p class="hint warn-text">所选厂商未就绪，点左下角设置里「去授权」。</p>{/if}
          {#if isCfConn && brains?.wrangler && !brains.wrangler.found}{@render wrNote()}{/if}
        </div>
      </div>

    {:else if view === 'schedule'}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <header class="thread-head" data-tauri-drag-region onmousedown={startDrag}>
        <div class="th-info"><b>排期</b><small>各站点待定时发布的内容 · 由 gcms 服务端到点自动发布</small></div>
        <button class="icon-btn" onclick={loadScheduled} disabled={schedLoading} title="刷新">{@render refreshIcon(schedLoading)}</button>
      </header>
      <div class="thread">
        <div class="sched-inner">
          {#if schedLoading && sched.length === 0}
            <p class="center-hint">读取排期…</p>
          {:else if schedError}
            <div class="err-note sched-err">{schedError}</div>
          {:else if sched.length === 0}
            <div class="sched-empty">
              <div class="cal-mark">🗓️</div>
              <b>还没有定时发布的内容</b>
              <p>在对话里让它「定时发布」（比如「这篇明天早上 9 点发」），排期就会出现在这里。</p>
            </div>
          {:else}
            <div class="sched-sticky">
            <div class="sched-density">
              {#each schedDensity as d (d.key)}
                <button class="sd-cell l{Math.min(d.count, 4)}" class:today={d.today} class:past={d.past} data-tip={d.tip} aria-label={d.tip} onclick={() => d.count && jumpToDay(d.key)}></button>
              {/each}
            </div>
            {#if schedTitleQ.trim()}
              <span class="sched-tq-chip">标题：{schedTitleQ.trim()}<button title="清除标题过滤，恢复全量" onclick={() => (schedTitleQ = '')}>×</button></span>
            {/if}
            {#if schedSites.length > 8}
              <div class="sched-filter-dd"><Dropdown compact searchable bare bind:value={schedSiteFilter} options={[{ value: '', label: `全部站点 · ${schedSites.length}` }, ...schedSites.map((sl) => { const st = sites.find((x) => x.slug === sl); return { value: sl, label: st?.name || sl, sub: st?.url ? hostOf(st.url) : sl, img: st?.favicon || st?.logo || faviconGuess(st?.url) }; })]} placeholder="全部站点" /></div>
            {:else if schedSites.length > 1}
              <div class="sched-filter">
                <button class="sf-chip sf-all" class:on={!schedSiteFilter} onclick={() => (schedSiteFilter = '')}><svg width="12" height="12" viewBox="0 0 16 16" fill="none"><rect x="2" y="2" width="5" height="5" rx="1.2" stroke="currentColor" stroke-width="1.3"/><rect x="9" y="2" width="5" height="5" rx="1.2" stroke="currentColor" stroke-width="1.3"/><rect x="2" y="9" width="5" height="5" rx="1.2" stroke="currentColor" stroke-width="1.3"/><rect x="9" y="9" width="5" height="5" rx="1.2" stroke="currentColor" stroke-width="1.3"/></svg>全部</button>
                {#each schedSites as sl (sl)}
                  <button class="sf-chip" class:on={schedSiteFilter === sl} onclick={() => (schedSiteFilter = schedSiteFilter === sl ? '' : sl)}><SiteFav src={siteFav(sl)} label={sl} size={12} />{sl}</button>
                {/each}
              </div>
            {/if}
            </div>
            {#each schedGroups as g (g.key)}
              <div class="grp sched-grp" id="sched-day-{g.key}">{g.label}<span class="sched-grp-n">{g.items.length} 条</span></div>
              {#each g.items as it (it.site_slug + '-' + it.id)}
                <div class="sched-item">
                  <div class="sched-body">
                    <b>{it.title}</b>
                    <small><SiteFav src={siteFav(it.site_slug)} label={it.site_slug} size={12} /><span class="cmono">{it.site_slug}</span> · {it.lang} · <span class="sched-t">{schedWhen(it)}</span>{#if it.status === 'published'}<span class="sched-done">已发布</span>{/if}</small>
                  </div>
                  <!-- 已发布 → 开真实访问链接；待发布 → 短期草稿链接。文案跟着变，别让人以为点开的是同一种东西 -->
                  <button class="link sched-open" onclick={() => openSchedItem(it)}>{it.status === 'published' ? '访问' : '预览'} ↗</button>
                </div>
              {/each}
            {/each}
          {/if}
        </div>
      </div>

    {:else if view === 'tasks'}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <header class="thread-head" data-tauri-drag-region onmousedown={startDrag}>
        <div class="th-info"><b>定时任务</b><small>到点自动开一个新对话执行 · 需保持 Pilot 在后台（托盘）运行</small></div>
        <button class="btn soft bare" onclick={openNewTask}>{@render plusIcon()}新建任务</button>
      </header>
      <div class="thread">
        <div class="sched-inner">
          {#if tasks.length === 0}
            <div class="sched-empty">
              <div class="cal-mark">⏰</div>
              <b>还没有定时任务</b>
              <p>建一个让它按时自动干活，比如「每天早上 9 点，围绕本周热点写一篇文章存草稿」。</p>
              <button class="btn soft bare" onclick={openNewTask} style="margin-top:16px">{@render plusIcon()}新建任务</button>
            </div>
          {:else}
            <!-- 概览行（与托管仪表盘同款 stat-strip；今日口径按 history 本地汇总，0 值段省略） -->
            <div class="md-board">
              <div class="stat-strip">
                <span class="ss-seg" use:tipAction={'定时任务总数'}><b class="ss-num">{taskAgg.total}</b><span class="ss-lbl">任务</span></span>
                <span class="ss-seg" use:tipAction={'处于启用状态的任务数'}><i class="ss-dot ok"></i><b class="ss-num">{taskAgg.enabled}</b><span class="ss-lbl">启用</span></span>
                <span class="ss-seg" use:tipAction={'今日零点起实际触发次数（不含顺延）'}><b class="ss-num">{taskAgg.ran}</b><span class="ss-lbl">今日已跑</span></span>
                {#if taskAgg.failed}<span class="ss-seg" use:tipAction={'今日运行失败的次数'}><i class="ss-dot err"></i><b class="ss-num">{taskAgg.failed}</b><span class="ss-lbl">失败</span></span>{/if}
                {#if taskAgg.deferred}<span class="ss-seg" use:tipAction={'撞订阅限额自动改期，不算失败'}><i class="ss-dot warn"></i><b class="ss-num">{taskAgg.deferred}</b><span class="ss-lbl">限额顺延</span></span>{/if}
                <span class="ss-seg" use:tipAction={'启用任务中最近一次将触发的时间'}><span class="ss-lbl">下次</span><b class="ss-num">{taskNextLabel}</b></span>
              </div>
            </div>
            {#each tasks as t (t.id)}
              <div class="task-card {t.enabled ? '' : 'off'} {hlTaskIds.includes(t.id) ? 'hl' : ''}">
                <div class="task-toggle">
                  <button class="switch {t.enabled ? 'on' : ''}" title={t.enabled ? '已启用' : '已暂停'} onclick={() => toggleTask(t)}><span></span></button>
                </div>
                <div class="task-body">
                  <b>{t.title}</b>
                  <div class="task-meta">
                    <SiteFav src={siteFav(t.site_slug)} label={t.site_slug} size={13} /><span class="cmono">{t.site_slug}</span>{#if (t.site_slugs?.length ?? 0) > 1}<span class="cmono" title={t.site_slugs?.join('、')}> 等 {t.site_slugs?.length} 站</span>{/if}
                    <span class="cdot">·</span>{@render brainTag(t.brain, brainLabel(t.brain))}
                    <span class="cdot">·</span>{periodLabel(t.interval_minutes)}
                    {#if t.enabled}<span class="cdot">·</span>下次 {fmtSched(new Date(t.next_run * 1000).toISOString())}{#if t.history?.[0]?.deferred && t.next_run * 1000 > Date.now()}<span class="defer-tag" title="撞到订阅限额，已自动顺延到恢复后重试">限额顺延</span>{/if}{/if}
                  </div>
                  {#if t.last_run}
                    <div class="task-last {t.last_status}">
                      上次 {fmt(t.last_run)} · {t.last_status === 'ok' ? '成功' : '失败'}{#if t.last_summary}：{t.last_summary}{/if}
                      {#if t.last_conv_id}<button class="link" onclick={() => openConv(t.last_conv_id)}>查看对话</button>{/if}
                      {#if t.history?.length}<button class="link" onclick={() => (taskHistoryFor = t)}>运行记录</button>{/if}
                    </div>
                  {/if}
                </div>
                <div class="task-actions">
                  <button class="icon-btn" title="立即运行" onclick={() => runTaskNow(t)} aria-label="立即运行">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M5 3.4l7.2 4.6L5 12.6z" fill="currentColor" /></svg>
                  </button>
                  <button class="icon-btn" title="编辑" onclick={() => openEditTask(t)} aria-label="编辑">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M11.5 2.5l2 2L6 12l-2.5.5L4 10l7.5-7.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" /></svg>
                  </button>
                  <button class="x sm" title="删除" onclick={() => deleteTask(t)}>×</button>
                </div>
              </div>
            {/each}
          {/if}
        </div>
      </div>

    {:else if view === 'managed'}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <header class="thread-head" data-tauri-drag-region onmousedown={startDrag}>
        <div class="th-info"><b>托管</b><small>AI 按周循环运营站点 · 边界机制化：发布、预算与修改均受控</small></div>
        <div class="th-actions">
          <button class="icon-btn" onclick={loadManaged} disabled={managedLoading} title="刷新">{@render refreshIcon(managedLoading)}</button>
          <button class="btn soft bare" onclick={openManagedWizard} disabled={!sites.length}>{@render plusIcon()}托管一个站点</button>
        </div>
      </header>
      <div class="thread">
        <div class="sched-inner">
          {#if managedOfConn.length === 0}
            <div class="sched-empty">
              <div class="cal-mark">{@render botIcon(34)}</div>
              <b>还没有托管的站点</b>
              <p>把站点交给 AI 按周循环运营：每天按计划写稿存草稿、每周自查审计；你只在待审队列里批准发布或打回。</p>
              <button class="btn soft bare" onclick={openManagedWizard} style="margin-top:16px" disabled={!sites.length}>{@render plusIcon()}开启托管</button>
            </div>
          {:else}
            <!-- 仪表盘：就一行汇总（0 值段省略）。各站例外（熔断/暂停/待审/预算≥80%）不再常驻一行，
                 收进「待审」段的 hover 弹层里（点条目定位到卡片，见 .ss-pop-menu）。 -->
            <div class="md-board">
              <div class="stat-strip">
                <span class="ss-seg" use:tipAction={'本连接下已开启托管的站点数'}><b class="ss-num">{managedOfConn.length}</b><span class="ss-lbl">托管站</span></span>
                <span class="ss-seg" use:tipAction={'正常运行中的托管站数'}><i class="ss-dot ok"></i><b class="ss-num">{mdAgg.run}</b><span class="ss-lbl">运行</span></span>
                {#if mdAgg.paused}<span class="ss-seg" use:tipAction={'已暂停的托管站数'}><i class="ss-dot off"></i><b class="ss-num">{mdAgg.paused}</b><span class="ss-lbl">暂停</span></span>{/if}
                {#if mdAgg.fused}<span class="ss-seg" use:tipAction={'周预算触顶自动停的站数'}><i class="ss-dot err"></i><b class="ss-num">{mdAgg.fused}</b><span class="ss-lbl">熔断</span></span>{/if}
                <span class="ss-seg" use:tipAction={'周一起发布＋新建草稿 / 各站周上限合计'}><b class="ss-num">{mdAgg.out}/{mdAgg.cap}</b><span class="ss-lbl">本周产出</span></span>
                <!-- 待审：各站例外（熔断/暂停/待审/预算）收进这里，鼠标移上去才展开，不再常驻一行占地方 -->
                {#if mdAgg.drafts}
                  <span class="ss-seg ss-pop">
                    <i class="ss-dot warn"></i><b class="ss-num">{mdAgg.drafts}</b><span class="ss-lbl">待审</span>
                    {#if mdExceptions.length}
                      <div class="ss-pop-menu">
                        {#each mdExceptions as ex (ex.m.id)}
                          <button class="md-exc" title="点击定位到该站卡片" onclick={() => document.getElementById(`mc-${ex.m.id}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })}>
                            <SiteFav src={siteFav(ex.m.site_slug)} label={ex.m.site_slug} size={13} /><span class="md-exc-name">{ex.m.site_name}</span>
                            {#each ex.tags as tg (tg.t)}<span class="md-exc-tag {tg.k}">{tg.t}</span>{/each}
                          </button>
                        {/each}
                      </div>
                    {/if}
                  </span>
                {/if}
                <span class="ss-seg" use:tipAction={'本周托管任务累计用量，预算熔断同口径'}><b class="ss-num">{mdTok(mdAgg.tokens)}</b><span class="ss-lbl">token</span></span>
              </div>
            </div>
            {#each managedOfConn as m (m.id)}
              {@const sum = mSummaries[m.id]}
              {@const drafts = mDrafts[m.id] ?? []}
              {@const reportTask = sum?.tasks.find((t) => t.title.includes('每周周报'))}
              {@const tP = reportTrend(m, 'published')}
              {@const tC = reportTrend(m, 'clicks')}
              <div class="task-card {m.paused ? 'off' : ''}" id={`mc-${m.id}`}>
                <div class="task-toggle">
                  <button class="switch {m.paused ? '' : 'on'}" title={m.paused ? '已暂停（配套任务全部停跑）' : '托管中'} onclick={() => toggleManaged(m)}><span></span></button>
                </div>
                <div class="task-body">
                  <b><SiteFav src={siteFav(m.site_slug)} label={m.site_slug} size={14} /> {m.site_name}<span class="md-lv-badge" title="点击调整托管等级"><Dropdown compact bare bind:value={m.level} options={LEVEL_OPTS} onchange={(v: string) => changeLevel(m, v)} /></span>{#if m.level === 'l3'}<span class="md-badge lv3">慎用</span>{/if}{#if m.paused}<span class="md-badge off">已暂停</span>{/if}</b>
                  <div class="task-meta">
                    本周发布 {sum ? sum.published_this_week : '…'} / 上限 {m.weekly_post_limit}
                    <span class="cdot">·</span>待审草稿 {sum ? sum.drafts : drafts.length} 篇
                    {#if sum?.drafts_discarded}<span class="cdot">·</span><span title="AI 标记报废、等你确认删除的草稿（不占待审队列）">待清理 {sum.drafts_discarded} 篇</span>{/if}
                    {#if sum && (m.token_weekly_budget || sum.week_tokens)}<span class="cdot">·</span>token {mdTok(sum.week_tokens)}{m.token_weekly_budget ? ` / ${mdTok(m.token_weekly_budget)}` : ''}{/if}
                    {#if m.review_notes.length}<span class="cdot">·</span>累计打回 {m.review_notes.length} 次{/if}
                    {#if tP.length >= 2}<span class="cdot">·</span><span class="mb-trend" title="每周发布数趋势（旧→新，来自周报归档）">发布{#each tP as v, i (i)}<i style="height:{trendBarPx(v, tP)}px"></i>{/each}</span>{/if}
                    {#if tC.length >= 2}<span class="mb-trend" title="每周点击数趋势（旧→新，来自周报归档）">点击{#each tC as v, i (i)}<i style="height:{trendBarPx(v, tC)}px"></i>{/each}</span>{/if}
                  </div>
                  {#if sum}
                    <div class="task-meta">
                      {#each sum.tasks as t (t.id)}
                        <span class="md-task {t.enabled ? '' : 'off'}" title={t.title}>{t.title.includes('每日') ? '每日内容' : t.title.includes('周报') ? '每周周报' : '每周审计'}{t.last_run ? `：上次${t.last_status === 'ok' ? '成功' : '失败'}` : '：还没跑过'}</span>
                      {/each}
                      {#if m.brain}
                        <span class="md-task" title="配套任务用的厂商/模型"><BrainIcon brain={m.brain} size={12} /> {modelDisp(m.brain, m.model)}{effortDisp(m.effort) ? ` · ${effortDisp(m.effort)}` : ''}</span>
                      {/if}
                    </div>
                  {/if}
                  {#if m.fused_at}
                    <p class="md-err">预算已熔断（本周 {mdTok(sum?.week_tokens ?? 0)} / {mdTok(m.token_weekly_budget)} tokens）——确认无误后用左侧开关手动恢复。</p>
                  {/if}
                  {#if m.demote_note}
                    <p class="md-warn">{m.demote_note}</p>
                  {/if}
                  <div class="md-actions">
                    <button class="link md-primary" onclick={() => (mQueueOpen[m.id] = !mQueueOpen[m.id])}>待审队列{drafts.length ? `（${drafts.length}）` : ''}</button>
                    <button class="link" onclick={() => { mPlanOpen[m.id] = !mPlanOpen[m.id]; if (mPlanDraft[m.id] === undefined) mPlanDraft[m.id] = m.plan; }}>运营计划</button>
                    {#if reportTask}
                      <button class="link" onclick={() => m.reports?.length ? (reportListFor = m) : (reportTask.last_conv_id ? openConv(reportTask.last_conv_id) : say('还没有周报——点「立即生成」跑一份'))}>查看周报{m.reports?.length ? `（${m.reports.length}）` : ''}</button>
                      <button class="link" onclick={() => genReportNow(reportTask.id)}>立即生成</button>
                    {/if}
                    <button class="link" onclick={() => adjustTasks(m)}>调整任务</button>
                  </div>
                  {#if mQueueOpen[m.id]}
                    <div class="md-queue">
                      {#if drafts.length === 0}
                        <p class="hint">暂无待审草稿——等每日任务跑出第一篇，或点上面「刷新」。</p>
                      {/if}
                      {#each groupDrafts(drafts) as g (g.lang)}
                        <div class="md-group">
                          <span>{g.lang || '默认语种'} · {g.items.length} 篇</span>
                          {#if g.items.length > 1}<button class="link" onclick={() => approveGroup(m, g.items)}>全部批准</button>{/if}
                        </div>
                        {#each g.items as d (d.id)}
                          <div class="md-row">
                            <span class="md-title" title={d.title}>{d.title}</span>
                            <span class="md-sub">{d.updated_at ? fmtSched(d.updated_at) : ''}</span>
                            <span class="md-btns">
                              <button class="link md-prev" onclick={() => managedPreview(m, d)}>预览 ↗</button>
                              <button class="btn sm" onclick={() => approveDraft(m, d)}>批准发布</button>
                              <button class="btn sm ghost" onclick={() => { rejectFor = { m, d }; rejectReason = ''; }}>打回</button>
                            </span>
                          </div>
                        {/each}
                      {/each}
                    </div>
                  {/if}
                  {#if mPlanOpen[m.id]}
                    <div class="md-plan">
                      <textarea class="tin" rows="8" bind:value={mPlanDraft[m.id]} placeholder="90 天运营计划（每日任务的方向依据）…"></textarea>
                      <div class="row-end">
                        <button class="btn sm ghost" onclick={() => (mPlanOpen[m.id] = false)}>收起</button>
                        <button class="btn sm" onclick={() => saveManagedPlan(m)}>保存并同步任务</button>
                      </div>
                    </div>
                  {/if}
                  {#if m.level === 'l3'}
                    <p class="md-foot-warn">⚠️ L3 存量维护：AI 可修改已发布内容、把低质旧文转草稿下线（每周 ≤
                      <input class="md-editq" type="number" min="1" max="20" bind:value={m.weekly_edit_limit} onchange={() => saveEditLimit(m)} title="每周存量修改上限（Pilot 触发前实测计数，配额用完当天禁改存量）" />
                      篇，Pilot 实测把关）。改动均有修订历史、可在后台一键回滚；请每周查看周报「观察名单」跟踪数据回落。删除/导航/资料/类型仍绝对禁止。</p>
                  {:else}
                    <p class="md-foot"><svg width="11" height="11" viewBox="0 0 16 16" fill="none"><path d="M8 1.8 13 3.6v4.1c0 3.2-2.1 5.4-5 6.5-2.9-1.1-5-3.3-5-6.5V3.6L8 1.8Z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/><path d="m5.8 8 1.6 1.6 2.8-3" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/></svg><span>{m.level === 'l0' ? '只产草稿、发布由你批准；绝不删除内容或改动站点结构。' : '常规文章可自动发布；绝不删除内容或改动站点结构，审计纪要等仍待你审。'}</span></p>
                  {/if}
                </div>
                <div class="task-actions">
                  <button class="x sm" title="关闭托管（删配套任务，内容不动）" onclick={() => disableManaged(m)}>×</button>
                </div>
              </div>
            {/each}
          {/if}
        </div>
      </div>

    {:else if view === 'templates'}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <header class="thread-head" data-tauri-drag-region onmousedown={startDrag}>
        <div class="th-info">
          <div class="th-line"><b>模板库</b><small class="tmpl-hint">把做好的站点存成模板，之后引用它快速起新项目</small></div>
          {#if templates.length}
            <div class="tmpl-chips">
              <button class="tmpl-chip {tmplCat === '全部' ? 'on' : ''}" onclick={() => (tmplCat = '全部')}>{@render catIcon('全部')}全部<span class="tc-n">{templates.length}</span></button>
              {#each tmplCats as c (c)}
                <button class="tmpl-chip {tmplCat === c ? 'on' : ''}" onclick={() => (tmplCat = c)}>{@render catIcon(c)}{c}<span class="tc-n">{templates.filter((t) => catOf(t) === c).length}</span></button>
              {/each}
            </div>
          {/if}
        </div>
        <button class="icon-btn" onclick={loadTemplates} disabled={templatesLoading} title="刷新">{@render refreshIcon(templatesLoading)}</button>
      </header>
      <div class="thread">
        <div class="tmpl-inner">
          {#if templates.length === 0}
            <div class="sched-empty">
              <div class="cal-mark">🧩</div>
              <b>还没有模板</b>
              <p>在一个 Cloudflare 站点项目对话里点「存模板」，做得好的站就能沉淀下来复用。</p>
            </div>
          {:else}
            <div class="tmpl-grid">
              {#each tmplShown as t (t.slug)}
                <div class="tmpl-card" id="tmpl-{t.slug}">
                  <div class="tmpl-thumb">
                    {#if tmplHtml[t.slug]}
                      <iframe class="tmpl-frame" srcdoc={tmplHtml[t.slug]} sandbox="allow-scripts" title={t.name} tabindex="-1" scrolling="no"></iframe>
                    {:else}
                      <span class="tmpl-letter">{t.name.slice(0, 1)}</span>
                    {/if}
                    <div class="tmpl-hover">
                      <button class="tmpl-act" aria-label="预览真实页面" onclick={() => previewTemplate(t)} data-tip="预览真实页面"><svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M1.6 8s2.4-4.4 6.4-4.4S14.4 8 14.4 8s-2.4 4.4-6.4 4.4S1.6 8 1.6 8Z" stroke="currentColor" stroke-width="1.2" /><circle cx="8" cy="8" r="1.9" stroke="currentColor" stroke-width="1.2" /></svg></button>
                      <button class="tmpl-act primary" aria-label="用它建新站" onclick={() => openUseTmpl(t)} data-tip="用它建新站"><svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M8 3.2v9.6M3.2 8h9.6" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg></button>
                      <!-- 随附模板不给删（后端也拒）：按钮摆着只会让人点了吃一个错 -->
                      {#if !t.builtin}
                        <button class="tmpl-act" aria-label="删除模板" onclick={() => delTemplate(t)} data-tip="删除模板"><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M3 4.6h10M6.4 4.6V3.3h3.2V4.6M4.6 4.6l.6 8.1h5.6l.6-8.1" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
                      {/if}
                    </div>
                  </div>
                  <div class="tmpl-body">
                    <b>{t.name}</b>
                    <!-- 随附模板 created_at 恒 0，relTime 会渲染成 1970 的「1/1」——改显「内置」 -->
                    <!-- 「N 页」只在多页时挂：模板的单位是「一个站」，几页是**引用前就该知道**的事 -->
                    <div class="tmpl-sub"><span class="tmpl-desc">{t.desc || ''}</span><span class="tmpl-meta">{#if t.pages > 1}<span class="tmpl-pg">{t.pages} 页</span>{/if}{t.builtin ? '内置' : relTime(t.created_at)}</span></div>
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </div>

    {:else if view === 'remote'}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <!-- 工作台头部只留一行（机器地址 + 状态）：标题「远程连接」是废话——侧栏已经写着你在哪台机器上。 -->
      <header class="thread-head slim" data-tauri-drag-region onmousedown={startDrag}>
        <div class="th-info"><small class="rhead-line">{activeConn?.ssh_user}@{activeConn?.ssh_host}:{activeConn?.ssh_port} · {termOn ? '已连接' : '未连接'}
          <button class="th-rfz" data-tip={termBusy ? '连接中…' : '重新连接'} aria-label="重新连接" disabled={termBusy} onclick={reconnectTerm}>{@render refreshIcon(termBusy)}</button>
          {#if stat}
            <span class="hstats">
              {@render loadStat('CPU', stat.cpu, stat.cpu === null ? '正在取样…（要两次采样才算得出占用）' : `CPU 占用 ${stat.cpu}%`)}
              {@render loadStat('内存', stat.memPct, `内存 ${stat.memText}`)}
              {@render loadStat('磁盘', stat.diskPct, `根分区 ${stat.diskText}`)}
            </span>
          {/if}</small></div>
        <div class="rhead-acts">
          <!-- 三个面板开关（VS Code 那套）：命令行 / 底部对话 / 右侧文件，各自可开可关、可同时开。 -->
          <button class="wb-tg" class:on={wb.term} aria-pressed={wb.term} data-tip="命令行窗口" aria-label="命令行窗口" onclick={toggleWbTerm}>
            {@render sshMark(15)}
          </button>
          <button class="wb-tg" class:on={wb.chat} aria-pressed={wb.chat} data-tip="底部对话框" aria-label="底部对话框" onclick={toggleWbChat}>
            <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><rect x="1.8" y="2.6" width="12.4" height="10.8" rx="2" stroke="currentColor" stroke-width="1.3" /><path d="M1.8 9.8h12.4" stroke="currentColor" stroke-width="1.3" /><rect x="1.8" y="9.8" width="12.4" height="3.6" fill="currentColor" opacity=".9" /></svg>
          </button>
          <button class="wb-tg" class:on={wb.files} aria-pressed={wb.files} data-tip="右侧文件" aria-label="右侧文件" onclick={toggleWbFiles}>
            <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><rect x="1.8" y="2.6" width="12.4" height="10.8" rx="2" stroke="currentColor" stroke-width="1.3" /><path d="M9.6 2.6v10.8" stroke="currentColor" stroke-width="1.3" /><path d="M9.6 2.6h2.6a2 2 0 0 1 2 2v6.8a2 2 0 0 1-2 2H9.6z" fill="currentColor" opacity=".9" /></svg>
          </button>
        </div>
      </header>
      <!-- 工作台：终端占主区，底部对话 / 右侧文件按需开，两条分隔线可拖。
           终端的尺寸变化交给 ResizeObserver 自动 fit（见 startTerm 上面那个 $effect）。 -->
      <div class="wb">
        <!-- 文件栏单开时把主区收起来（display:none）：终端就在里头，只藏不拆，xterm 和 SSH 会话都不掉。 -->
        <div class="wb-main" class:collapsed={wbFilesSolo}>
          <!-- 终端自己管滚动：别套 .thread（它的 overflow-y:auto 会和 xterm 打架）。
               ★ 关掉命令行只是 display:none **藏起来**，绝不拆 DOM —— 拆了 xterm 就没了，
               回来还得重连一次（连接是真的，掉一次要重登服务器）。 -->
          <div class="term-wrap" class:hid={!wb.term} class:with-chat={wb.chat}>
            <div class="term" bind:this={termEl}></div>
            {#if termBusy}
              <!-- 建连要走网络+认证，一两秒是常事，别让黑框干愣着。浮层而非写进终端：见 startTerm。 -->
              <div class="term-connecting"><span class="tc-spin"></span>正在连接 {activeConn?.ssh_user}@{activeConn?.ssh_host}…</div>
            {/if}
          </div>
          {#if wb.chat}
            {#if wb.term}
              <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
              <div class="wb-split h" title="拖动调整高度" role="separator" aria-orientation="horizontal" onmousedown={(e) => startWbResize(e, 'h')}></div>
            {/if}
            <!-- 命令行关着时对话吃满整个主区（固定高度只在两者共存时才有意义） -->
            <div class="wb-chat" class:solo={!wb.term} style={wb.term ? `height:${wb.chatH}px` : ''}>
              {#if activeConv && activeConv.conn_id === activeConnId}
                {@render convPane()}
              {:else}
                <!-- 还没有对话：这里就是起点（发出去就新建一条，走 startChat 那套）。
                     外层必须是 .composer-wrap —— 输入框的样式挂在 `.composer.big, .composer-wrap .composer` 上，
                     裸 .composer 一条都套不上（就是那次「样式缺失」）。 -->
                <div class="wb-start composer-wrap">
                  <div class="composer">
                    <textarea bind:this={wbStartEl} bind:value={lDraft} use:autogrow rows="2" placeholder="让 AI 在这台机器上做点什么…例如：看看装了什么，帮我装上 Docker"
                      oncompositionstart={() => (composing = true)} oncompositionend={() => (composing = false)}
                      onkeydown={(e) => onComposerKey(e, startChat)}></textarea>
                    <div class="composer-bar">
                      <div class="cb-left"><span class="ssh-target" title={activeConn?.ssh_os || '远端系统未知'}>{@render distroMark(distroOf(activeConn), 13)}{activeConn?.ssh_user}@{activeConn?.ssh_host}</span></div>
                      <div class="cb-right">
                        <Dropdown compact bind:value={lPerm} options={permOptsFor(lBrain, true)} tone={permTone(lPerm)} tip={permTipFor(lBrain, lPerm, true)} />
                        <ModelFx options={comboOpts} value={`${lBrain}::${lModel}`} effort={lEffort} onpick={pickCombo} oneffort={(v: string) => { lEffort = v; prefs.effort = v; savePrefs(prefs); }} />
                        <button class="send" onclick={startChat} disabled={!lDraft.trim() || !brainUsable(lBrain)} title="发送（Enter）">↑</button>
                      </div>
                    </div>
                  </div>
                  <!-- 档位警告见权限下拉的 tip（permTipFor）；这里只留「厂商没就绪」这种当下堵路的事。 -->
                  {#if !brainUsable(lBrain)}
                    <p class="hint warn-text">所选厂商未就绪，点左下角设置里「去授权」。</p>
                  {/if}
                </div>
              {/if}
            </div>
          {/if}
        </div>
        {#if wb.files}
          <!-- 文件栏单开时没有可拖的对象（主区已收起），分隔线也就不出 -->
          {#if !wbFilesSolo}
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <div class="wb-split v" title="拖动调整宽度" role="separator" aria-orientation="vertical" onmousedown={(e) => startWbResize(e, 'v')}></div>
          {/if}
          <aside class="files-wrap" class:solo={wbFilesSolo} style="{wbFilesSolo ? '' : `width:${wb.filesW}px;`} --fw-perm:{colW.perm}px; --fw-size:{colW.size}px; --fw-date:{colW.date}px">
          <div class="files-bar">
            <button class="fbtn" aria-label="上一级" data-tip="上一级" onclick={sftpUp} disabled={sftpBusy || !sftpPath || sftpPath === '/'}><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M8 12.5v-9M4.2 7.3 8 3.5l3.8 3.8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
            {#if sftpPathEdit}
              <input class="tin fpath" bind:this={sftpPathEl} bind:value={sftpPathDraft} spellcheck="false" autocapitalize="off" autocorrect="off" placeholder="/"
                onblur={() => (sftpPathEdit = false)}
                onkeydown={(e) => { if (e.key === 'Enter') commitPath(); else if (e.key === 'Escape') sftpPathEdit = false; }} />
            {:else}
              <!-- 面包屑：点哪一段跳哪一层。尾巴上那块空白本身是个按钮 ——
                   点它＝切成输入框直接敲路径（资源管理器那套），键盘也 Tab 得到。 -->
              <div class="fcrumbs" bind:this={crumbEl}>
                {#each sftpCrumbs as c, i (c.path)}
                  {#if i > 0}<span class="fcrumb-sep">›</span>{/if}
                  <button class="fcrumb" class:cur={c.path === sftpPath} disabled={sftpBusy} onclick={() => sftpGo(c.path)}>{c.name}</button>
                {/each}
                <button class="fcrumb-blank" aria-label="直接输入路径" data-tip="点这里可直接输入路径" onclick={startPathEdit}></button>
              </div>
            {/if}
            <button class="fbtn" aria-label="刷新" data-tip="刷新" onclick={sftpRefresh} disabled={sftpBusy}>{@render refreshIcon(sftpBusy)}</button>
            <button class="fbtn" aria-label="新建文件夹" data-tip="新建文件夹" onclick={() => openMkdir()} disabled={sftpBusy}><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M1.8 4.2A1.4 1.4 0 0 1 3.2 2.8h3l1.4 1.6h5.2a1.4 1.4 0 0 1 1.4 1.4v6a1.4 1.4 0 0 1-1.4 1.4H3.2a1.4 1.4 0 0 1-1.4-1.4v-7.6z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /><path d="M8 7.4v3.4M6.3 9.1h3.4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" /></svg></button>
            <button class="fbtn" aria-label="上传文件" data-tip="上传到当前目录" onclick={() => sftpUpload()} disabled={sftpBusy || !!sftpXfer}><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M8 10.4V3.6M4.8 6.4 8 3.2l3.2 3.2M3 12.8h10" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
          </div>
          {#if sftpXfer}<div class="files-note">{sftpXfer}</div>{/if}
          <!-- 出错就地给个重试：首次加载失败时 sftpPath 还是空的，刷新键（要有当前目录）帮不上忙 -->
          {#if sftpErr}<button type="button" class="err-note files-err" onclick={() => sftpGo(sftpPath)} title="点击重试">{sftpErr} · 点此重试</button>{/if}
          <div class="fhead">
            <button class="fh fh-name" onclick={() => setSort('name')}>名称{#if sftpSort.key === 'name'}<span class="fh-ar" class:desc={!sftpSort.asc}>^</span>{/if}</button>
            <span class="fh fh-perm">{@render colGrip('perm')}权限</span>
            <button class="fh fh-size" onclick={() => setSort('size')}>{@render colGrip('size')}大小{#if sftpSort.key === 'size'}<span class="fh-ar" class:desc={!sftpSort.asc}>^</span>{/if}</button>
            <button class="fh fh-date" onclick={() => setSort('mtime')}>{@render colGrip('date')}日期{#if sftpSort.key === 'mtime'}<span class="fh-ar" class:desc={!sftpSort.asc}>^</span>{/if}</button>
          </div>
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div class="files-list" oncontextmenu={(e) => openFctx(e, null)}>
            {#if sftpBusy && !sftpRows.length}
              <div class="files-empty">读取中…</div>
            {:else if !sftpRows.length}
              <div class="files-empty">{sftpErr ? '' : '空目录'}</div>
            {:else}
              {#each sftpRows as r (r.path)}
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <div class="frow" class:on={sftpSel === r.path} role="row" tabindex="-1"
                  onclick={() => (sftpSel = r.path)}
                  ondblclick={() => sftpOpenRow(r)}
                  oncontextmenu={(e) => { e.stopPropagation(); openFctx(e, r); }}
                  onkeydown={(e) => { if (e.key === 'Enter') sftpOpenRow(r); }}>
                  <span class="fname" style="padding-left:{r.depth * 15}px">
                    {#if r.dir && !r.link}
                      <button class="fchev" class:open={sftpOpenDirs.has(r.path)} aria-label="展开" onclick={(e) => { e.stopPropagation(); toggleDir(r); }}>
                        {#if sftpLoading[r.path]}<span class="fspin"></span>{:else}<svg width="9" height="9" viewBox="0 0 12 12" fill="none"><path d="M4.5 2.5 8 6l-3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>{/if}
                      </button>
                    {:else}
                      <span class="fchev-sp"></span>
                    {/if}
                    {#if r.link}
                      <!-- 软链：sftp 的 readdir 是 lstat 语义，指向目录的软链也归这里（点开时才知道） -->
                      <svg class="fic link" width="14" height="14" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.4" fill="currentColor" opacity=".14" /><path d="M5.6 10.4 10.4 5.6M6.6 5.6h3.8v3.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
                    {:else if r.dir}
                      <svg class="fic dir" width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M1.8 4.2A1.4 1.4 0 0 1 3.2 2.8h3l1.4 1.6h5.2a1.4 1.4 0 0 1 1.4 1.4v6a1.4 1.4 0 0 1-1.4 1.4H3.2a1.4 1.4 0 0 1-1.4-1.4v-7.6z" fill="currentColor" opacity=".16" /><path d="M1.8 4.2A1.4 1.4 0 0 1 3.2 2.8h3l1.4 1.6h5.2a1.4 1.4 0 0 1 1.4 1.4v6a1.4 1.4 0 0 1-1.4 1.4H3.2a1.4 1.4 0 0 1-1.4-1.4v-7.6z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /></svg>
                    {:else}
                      <svg class="fic" width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M4 1.8h5.2L12.8 5.4v8.8H4V1.8z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /><path d="M9.2 1.8v3.6h3.6" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /></svg>
                    {/if}
                    <span class="fname-t">{r.name}</span>
                  </span>
                  <span class="fperm">{r.perms}</span>
                  <span class="fsize">{r.dir && !r.link ? '·' : fmtSize(r.size)}</span>
                  <span class="fdate">{fmtStamp(r.mtime)}</span>
                </div>
              {/each}
            {/if}
          </div>
        </aside>
        {/if}
      </div>

    {:else if view === 'prompts'}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <header class="thread-head" data-tauri-drag-region onmousedown={startDrag}>
        <div class="th-info"><b>提示词库</b><small>常用建站需求，点「用它建站」填进对话框就能开始；也能存自己的。</small></div>
        <button class="btn soft bare" onclick={openNewPrompt}>{@render plusIcon()}新增提示词</button>
      </header>
      <div class="thread">
        <div class="sched-inner">
          <div class="prompt-grid">
            {#each allPrompts as p (p.id)}
              <div class="prompt-card">
                <div class="prompt-top">
                  <b class="prompt-title">{p.title}</b>
                  {#if p.builtin}<span class="prompt-tag">预设</span>{/if}
                </div>
                <p class="prompt-body">{p.body}</p>
                <div class="prompt-acts">
                  <button class="prompt-act" aria-label="用它建站" data-tip="用它建站" onclick={() => usePrompt(p)}><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M8 3.2v9.6M3.2 8h9.6" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg></button>
                  <button class="prompt-act" aria-label="复制提示词" data-tip="复制" onclick={() => copyPrompt(p)}><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><rect x="5.5" y="5.5" width="7.6" height="8" rx="1.4" stroke="currentColor" stroke-width="1.2" /><path d="M10.6 5.5V4.2A1.4 1.4 0 0 0 9.2 2.8H4A1.4 1.4 0 0 0 2.6 4.2v5.2A1.4 1.4 0 0 0 4 10.8h1.5" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" /></svg></button>
                  {#if !p.builtin}
                    <button class="prompt-act" aria-label="编辑提示词" data-tip="编辑" onclick={() => openEditPrompt(p)}><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10.8 2.9l2.3 2.3M11.4 2.3l2.3 2.3-8 8-3 .7.7-3 8-8z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /></svg></button>
                    <button class="prompt-act" aria-label="删除提示词" data-tip="删除" onclick={() => deletePrompt(p)}><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M3 4.6h10M6.4 4.6V3.3h3.2V4.6M4.6 4.6l.6 8.1h5.6l.6-8.1" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        </div>
      </div>

    {:else}
      <!-- 对话线程 -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <header class="thread-head" data-tauri-drag-region onmousedown={startDrag}>
        <div class="th-info">
          <b>{activeConv?.title}</b>
          <small>{#if activeConvIsCf}{@render cfMark(13)}{:else}<SiteFav src={siteFav(activeConv?.site_slug ?? '')} label={activeConv?.site_slug ?? ''} size={13} />{/if} {#if (activeConv?.site_slugs?.length ?? 0) > 1}<span data-tip={convSitesTip(activeConv)}>{activeConv?.site_name}</span>{:else}{activeConv?.site_name || activeConv?.site_slug}{/if} · {taskLabel(activeConv?.task_type ?? '')} · {@render brainTag(activeConv?.brain ?? 'claude', brainLabel(activeConv?.brain ?? '') + (activeConv?.brain === 'claude' && activeConv?.model ? ` ${activeConv.model}` : ''))}</small>
        </div>
        {#if activeSiteUrl}
          <button class="th-open" onclick={() => openUrl(activeSiteUrl)} title="打开 {activeSiteUrl}">{hostOf(activeSiteUrl)}<svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M6 3.5h6.5V10M12.2 3.8 3.8 12.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
        {/if}
      </header>

      {@render convPane()}
    {/if}
  </section>
</main>

<!-- 会话搜索（Claude Code 风格：顶部圆角搜索框 + 结果列表） -->
{#if searchOpen}
  <div class="mask" role="presentation" onclick={() => (searchOpen = false)}></div>
  <div class="search-box" role="dialog" aria-modal="true" aria-label="搜索会话">
      <div class="search-head">
        <svg class="si-ic" width="16" height="16" viewBox="0 0 18 18" fill="none">
          <circle cx="8" cy="8" r="5" stroke="currentColor" stroke-width="1.4" />
          <path d="M11.7 11.7L15 15" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
        </svg>
        <input bind:this={searchInput} bind:value={searchQ} placeholder="搜索会话、远程连接（名称/IP）、定时任务、托管站点、模板…" spellcheck="false" autocapitalize="off" autocorrect="off" />
        <kbd>esc</kbd>
      </div>
      <div class="search-list">
        {#if searchFlat.length === 0}
          <p class="search-empty">没有匹配结果</p>
        {:else}
          {#each searchSections as sec, si (sec.title)}
            {@const base = searchSections.slice(0, si).reduce((n, s) => n + s.entries.length, 0)}
            <div class="search-sec">{sec.title}</div>
            {#each sec.entries as it, i (`${sec.title}-${i}`)}
              <button class="search-item {base + i === searchIdx ? 'on' : ''}" onclick={() => pickEntry(it)} onmouseenter={() => (searchIdx = base + i)}>
                {#if it.kind === 'view'}
                  {#if it.view === 'schedule'}<svg width="15" height="15" viewBox="0 0 16 16" fill="none"><rect x="2.5" y="3" width="11" height="10.5" rx="1.5" stroke="currentColor" stroke-width="1.3" /><path d="M2.5 6h11M5.5 2v2M10.5 2v2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" /></svg>
                  {:else if it.view === 'tasks'}<svg width="15" height="15" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="5.5" stroke="currentColor" stroke-width="1.3" /><path d="M8 5v3l2 1.5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /></svg>
                  {:else}{@render botIcon(15)}{/if}
                  <span class="si-main"><b>{it.label}</b><small>切换到{it.label}视图</small></span>
                {:else if it.kind === 'task'}
                  <SiteFav src={siteFav(it.t.site_slug)} label={it.t.site_slug} size={15} />
                  <span class="si-main"><b>{it.t.title || '未命名任务'}</b><small>{it.t.site_name || it.t.site_slug}{#if (it.t.site_slugs?.length ?? 0) > 1} 等 {it.t.site_slugs?.length} 站{/if} · {periodLabel(it.t.interval_minutes)}</small></span>
                {:else if it.kind === 'managed'}
                  <SiteFav src={siteFav(it.m.site_slug)} label={it.m.site_slug} size={15} />
                  <span class="si-main"><b>{it.m.site_name}</b><small>{levelLabel(it.m.level)}{it.m.paused ? ' · 已暂停' : ''}</small></span>
                {:else if it.kind === 'conv'}
                  {@const site = sites.find((s) => s.slug === it.c.site_slug)}
                  <SiteFav src={siteFav(it.c.site_slug)} label={it.c.site_slug} size={15} />
                  <span class="si-main"><b>{it.c.title || '未命名会话'}</b><small>{#if (it.c.site_slugs?.length ?? 0) > 1}<span data-tip={convSitesTip(it.c)}>{it.c.site_name}</span>{:else}{it.c.site_name || it.c.site_slug}{#if site?.url} · {hostOf(site.url)}{/if}{/if}</small></span>
                  {@render brainTag(it.c.brain, brainLabel(it.c.brain))}
                {:else if it.kind === 'template'}
                  <!-- 与左栏「模板库」同一个图标：搜到的东西该看得出是同一样东西 -->
                  <svg width="15" height="15" viewBox="0 0 16 16" fill="none"><rect x="2.5" y="2.5" width="4.5" height="4.5" rx="1" stroke="currentColor" stroke-width="1.3" /><rect x="9" y="2.5" width="4.5" height="4.5" rx="1" stroke="currentColor" stroke-width="1.3" /><rect x="2.5" y="9" width="4.5" height="4.5" rx="1" stroke="currentColor" stroke-width="1.3" /><rect x="9" y="9" width="4.5" height="4.5" rx="1" stroke="currentColor" stroke-width="1.3" /></svg>
                  <span class="si-main"><b>{it.t.name}</b><small>{catOf(it.t)}{it.t.desc ? ` · ${it.t.desc}` : ''}</small></span>
                  {#if it.t.builtin}<span class="si-tag">内置</span>{/if}
                {:else if it.kind === 'conn'}
                  <!-- 与连接切换器同一个终端标 -->
                  {@render sshMark(15)}
                  <span class="si-main"><b>{it.c.name}</b><small>{it.c.ssh_user}@{it.c.ssh_host}{it.c.ssh_port && it.c.ssh_port !== 22 ? ':' + it.c.ssh_port : ''}{it.c.ssh_os ? ` · ${it.c.ssh_os}` : ''}</small></span>
                  {#if activeConnId === it.c.id}<span class="si-tag">当前</span>{/if}
                {:else}
                  <svg width="15" height="15" viewBox="0 0 18 18" fill="none"><circle cx="8" cy="8" r="5" stroke="currentColor" stroke-width="1.4" /><path d="M11.7 11.7L15 15" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></svg>
                  <span class="si-main"><b>在排期中查找「{it.q}」</b><small>切到排期视图并按标题过滤</small></span>
                {/if}
              </button>
            {/each}
          {/each}
        {/if}
      </div>
  </div>
{/if}

<!-- 消息气泡片段 -->
{#snippet bubble(m: Message, isLast: boolean)}
  {#if m.role === 'user'}
    {@const ua = splitAttachments(m.text)}
    <div class="msg user"><div class="ubody">
      {#if ua.body.trim()}<span class="ub-text">{@render richText(ua.body)}</span>{/if}
      {#if ua.atts.length}
        <div class="ub-atts" class:only={!ua.body.trim()}>
          {#each ua.atts as p (p)}
            {#if isImgPath(p) && thumbs[thumbKey(p)] !== ''}
              {#if thumbs[thumbKey(p)]}
                <button class="ub-att-img" data-tip={p.split('/').pop()} onclick={() => (lightbox = thumbs[thumbKey(p)])}><img src={thumbs[thumbKey(p)]} alt={p.split('/').pop()} onerror={() => (thumbs = { ...thumbs, [thumbKey(p)]: '' })} /></button>
              {:else}
                <!-- 数据未到：同尺寸占位，图片就位零布局跳动 -->
                <span class="ub-att-img ph" data-tip={p.split('/').pop()}></span>
              {/if}
            {:else}
              <span class="ub-att">
                <span class="ub-att-ic">
                  {#if isImgPath(p)}<svg width="13" height="13" viewBox="0 0 16 16" fill="none"><rect x="2.2" y="2.8" width="11.6" height="10.4" rx="1.6" stroke="currentColor" stroke-width="1.2" /><circle cx="5.7" cy="6.2" r="1.1" fill="currentColor" /><path d="M3 12.4l3.3-3.1 2.1 1.9 2.4-2.6 2.2 2.3" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round" /></svg>
                  {:else}<svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M9 1.8H4.5A1.3 1.3 0 0 0 3.2 3.1v9.8a1.3 1.3 0 0 0 1.3 1.3h7a1.3 1.3 0 0 0 1.3-1.3V5.5z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /><path d="M9 1.8v3.7h3.8" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /></svg>{/if}
                </span>
                <span class="ub-att-n" title={p}>{p.split('/').pop()}</span>
              </span>
            {/if}
          {/each}
        </div>
      {/if}
    </div></div>
  {:else}
    <div class="msg assistant">
      <div class="body">
        {#if m.tools.length}{@render cmds(m.tools)}{/if}
        {#if m.error && m.limit_reset != null}
          <div class="limit-card">
            <div class="limit-head">⏳ {brainTitle(activeConv?.brain)} 额度已用完</div>
            {#if (m.limit_reset ?? 0) > 0}
              <div class="limit-sub">预计 {fmtClock(m.limit_reset ?? 0)} 恢复{#if (m.limit_reset ?? 0) * 1000 > nowTick} · 还有 {fmtRemain((m.limit_reset ?? 0) * 1000 - nowTick)}{/if}</div>
            {:else}
              <div class="limit-sub">订阅套餐的时间窗限额已触顶，稍后会自动恢复；也可以稍等片刻手动重试。</div>
            {/if}
            {#if isLast && !viewBusy}
              <div class="limit-actions">
                {#if limitAuto[activeConvId]}
                  <button class="retry-btn is-armed" onclick={() => disarmLimitAuto(activeConvId)}>✓ 已排队，到点自动续跑 · 点击取消</button>
                {:else if (m.limit_reset ?? 0) * 1000 > nowTick}
                  <button class="retry-btn" onclick={() => armLimitAuto(activeConvId, m.limit_reset ?? 0)}><svg width="12" height="12" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="5.6" stroke="currentColor" stroke-width="1.4" /><path d="M8 5.2V8l2 1.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></svg>到点自动续跑</button>
                {/if}
                {#if activeConv?.session_ref}<button class="retry-btn" onclick={() => retry(activeConvId, true)}><svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M13 8a5 5 0 1 1-1.5-3.6M13 2v3h-3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>立即重试</button>{/if}
              </div>
              <div class="limit-hint">也可以在右下角把模型切到另一家继续。</div>
            {/if}
          </div>
        {:else if m.error}
          <div class="text is-err">{@render richText(m.text)}{#if isLast && !viewBusy}{#if activeConv?.session_ref && !retryExhausted[activeConvId]}<button class="retry-btn" onclick={() => retry(activeConvId, true)}><svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M13 8a5 5 0 1 1-1.5-3.6M13 2v3h-3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>重试</button>{/if}<button class="retry-btn" title="换一个全新会话原地续跑（自动带上历史摘要）——用于会话状态损坏、重试无效时" onclick={() => rebuildSession(activeConvId)}><svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2.6 8a5.4 5.4 0 0 1 9.3-3.7M13.4 8a5.4 5.4 0 0 1-9.3 3.7" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><path d="M11.6 1.6v2.9h2.9M4.4 14.4v-2.9H1.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>重建继续</button>{/if}</div>
        {:else}
          {@const ga = splitGenImages(m.text)}
          <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
          <div class="text md" onclick={mdClick} onauxclick={mdClick}>{@html mdRender(ga.body)}</div>
          {#if ga.atts.length}
            <!-- codex 生图产物：缩略图（点击放大）；读不到的退回文件名卡（点击访达定位） -->
            <div class="ub-atts gen-imgs">
              {#each ga.atts as p (p)}
                {#if isImgPath(p) && thumbs[thumbKey(p)] !== ''}
                  {#if thumbs[thumbKey(p)]}
                    <button class="ub-att-img" data-tip={p.split(/[\\/]/).pop()} onclick={() => (lightbox = thumbs[thumbKey(p)])}><img src={thumbs[thumbKey(p)]} alt={p.split(/[\\/]/).pop()} onerror={() => (thumbs = { ...thumbs, [thumbKey(p)]: '' })} /></button>
                  {:else}
                    <span class="ub-att-img ph" data-tip={p.split(/[\\/]/).pop()}></span>
                  {/if}
                {:else}
                  <button class="ub-att as-btn" title={p} onclick={() => void revealWorkdir(p)}>
                    <span class="ub-att-ic"><svg width="13" height="13" viewBox="0 0 16 16" fill="none"><rect x="2.2" y="2.8" width="11.6" height="10.4" rx="1.6" stroke="currentColor" stroke-width="1.2" /><circle cx="5.7" cy="6.2" r="1.1" fill="currentColor" /><path d="M3 12.4l3.3-3.1 2.1 1.9 2.4-2.6 2.2 2.3" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round" /></svg></span>
                    <span class="ub-att-n">{p.split(/[\\/]/).pop()}</span>
                  </button>
                {/if}
              {/each}
            </div>
          {/if}
        {/if}
        {#if m.proposal}
          <div class="proposal">
            <div class="proposal-head">⏰ AI 建议一个定时任务</div>
            <b class="proposal-title">{m.proposal.title}</b>
            <div class="proposal-meta">{periodLabel(m.proposal.every_minutes)}{#if m.proposal.first_run} · 首次 {fmtSched(m.proposal.first_run)}{/if}</div>
            <div class="proposal-prompt">{m.proposal.prompt}</div>
            {#if createdProposals.has(proposalKey(m.proposal))}
              <div class="proposal-done">✓ 已创建定时任务，可在左栏「定时任务」查看</div>
            {:else}
              <button class="btn primary small" onclick={() => m.proposal && openTaskFromProposal(m.proposal)}>创建定时任务…</button>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}
{/snippet}

{#snippet richText(text: string)}{#each text.split('\n') as line, i (i)}{#if i > 0}<br />{/if}{#each segs(line) as s, si (si)}{#if s.link}<button class="inlink" onclick={() => openUrl(s.t)}>{s.t}</button>{:else}{s.t}{/if}{/each}{/each}{/snippet}

{#snippet toolChip(t: ToolCall)}
  <div class="tool"><span class="tcode">{t.label}</span><span class="tdetail">{t.detail}</span></div>
{/snippet}

{#snippet brainTag(brain: string, label: string)}<span class="btag"><BrainIcon {brain} size={12} />{label}</span>{/snippet}

<!-- 只有「全部」「自建」带图标：这两个是**看什么范围**，后面那排是**按页型筛**。
     给每个都配图标反而把这层区别抹平了。 -->
{#snippet catIcon(c: string)}
  {#if c === '全部'}
    <svg class="tc-ic" width="11" height="11" viewBox="0 0 16 16" aria-hidden="true"><rect x="1.5" y="1.5" width="5.6" height="5.6" rx="1.3" fill="currentColor" /><rect x="8.9" y="1.5" width="5.6" height="5.6" rx="1.3" fill="currentColor" /><rect x="1.5" y="8.9" width="5.6" height="5.6" rx="1.3" fill="currentColor" /><rect x="8.9" y="8.9" width="5.6" height="5.6" rx="1.3" fill="currentColor" /></svg>
  {:else if c === CAT_MINE}
    <svg class="tc-ic" width="11" height="11" viewBox="0 0 16 16" fill="none" aria-hidden="true"><circle cx="8" cy="5" r="2.7" stroke="currentColor" stroke-width="1.5" /><path d="M2.9 14c0-2.7 2.3-4.4 5.1-4.4s5.1 1.7 5.1 4.4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
  {/if}
{/snippet}
{#snippet refreshIcon(spinning: boolean)}
  <svg class="rfz {spinning ? 'spin' : ''}" width="15" height="15" viewBox="0 0 24 24" fill="none">
    <path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round" />
    <path d="M21 3v5h-5" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
{/snippet}

{#snippet plusIcon()}<svg class="plus-ic" width="13" height="13" viewBox="0 0 14 14" fill="none"><path d="M7 2.4v9.2M2.4 7h9.2" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg>{/snippet}
<!-- 托管的机器人头图标（侧栏导航 + 空态复用，传 size）。viewBox 24 下 stroke 2 ≈ 侧栏 16 系图标的 1.3，视觉同粗。 -->
{#snippet botIcon(size: number)}<svg width={size} height={size} viewBox="0 0 24 24" fill="none"><path d="M12 8V4H8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /><rect x="4" y="8" width="16" height="12" rx="2" stroke="currentColor" stroke-width="2" stroke-linejoin="round" /><path d="M2 14h2M20 14h2M9 13v2M15 13v2" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>{/snippet}

{#snippet gearIcon()}<svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M2 5.3h3.5M9.3 5.3H14M2 10.7h5.1M10.9 10.7H14" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" /><circle cx="7.4" cy="5.3" r="1.6" stroke="currentColor" stroke-width="1.3" /><circle cx="9" cy="10.7" r="1.6" stroke="currentColor" stroke-width="1.3" /></svg>{/snippet}
{#snippet cfMark(size: number)}<svg class="cf-mark" width={size} height={size} viewBox="0 0 128 128"><path fill="#fff" d="m115.679 69.288l-15.591-8.94l-2.689-1.163l-63.781.436v32.381h82.061z" /><path fill="#f38020" d="M87.295 89.022c.763-2.617.472-5.015-.8-6.796c-1.163-1.635-3.125-2.58-5.488-2.689l-44.737-.581c-.291 0-.545-.145-.691-.363s-.182-.509-.109-.8c.145-.436.581-.763 1.054-.8l45.137-.581c5.342-.254 11.157-4.579 13.192-9.885l2.58-6.723c.109-.291.145-.581.073-.872c-2.906-13.158-14.644-22.97-28.672-22.97c-12.938 0-23.913 8.359-27.838 19.952a13.35 13.35 0 0 0-9.267-2.58c-6.215.618-11.193 5.597-11.811 11.811c-.145 1.599-.036 3.162.327 4.615C10.104 70.051 2 78.337 2 88.549c0 .909.073 1.817.182 2.726a.895.895 0 0 0 .872.763h82.57c.472 0 .909-.327 1.054-.8z" /><path fill="#faae40" d="M101.542 60.275c-.4 0-.836 0-1.236.036c-.291 0-.545.218-.654.509l-1.744 6.069c-.763 2.617-.472 5.015.8 6.796c1.163 1.635 3.125 2.58 5.488 2.689l9.522.581c.291 0 .545.145.691.363s.182.545.109.8c-.145.436-.581.763-1.054.8l-9.924.582c-5.379.254-11.157 4.579-13.192 9.885l-.727 1.853c-.145.363.109.727.509.727h34.089c.4 0 .763-.254.872-.654c.581-2.108.909-4.325.909-6.614c0-13.447-10.975-24.422-24.458-24.422" /></svg>{/snippet}
<!-- 对话主体（消息 + 输入框）。抽成 snippet 是为了两处共用同一份：
     独立的对话页，和远程工作台底部的对话面板。所有状态都是模块级的，直接用即可。
     注意 bind:this={threadEl} —— 同一时刻只会渲染一处（对话页与远程视图互斥），不会打架。 -->
{#snippet convPane()}
  <div class="thread" bind:this={threadEl}>
    <div class="thread-inner">
      {#each shownMessages as m, i (i)}
        {@render bubble(m, i === shownMessages.length - 1)}
      {/each}
      {#if viewBusy && liveView}
        <div class="msg assistant">
          <div class="body">
            {#if liveView.tools.length}{@render cmds(liveView.tools)}{/if}
            <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
            {#if liveView.text}<div class="text md" onclick={mdClick} onauxclick={mdClick}>{@html mdRender(liveView.text)}</div>{/if}
            {#if liveView.error}<div class="err-note">{liveView.error}</div>
            {:else}
              <div class="working" aria-label="思考中">
                <svg class="wl" viewBox="0 0 64 64" width="17" height="17" aria-hidden="true">
                  <path class="wl-trace" d="M44 24a14 14 0 1 0 0 16" fill="none" stroke="#a03c2b" stroke-width="7" stroke-linecap="round" pathLength="100" stroke-dasharray="100" />
                  <circle class="wl-dot" cx="45.5" cy="42" r="4.6" fill="#a03c2b" />
                </svg>
                <span class="working-t">{elapsedLabel(nowMs - liveView.startedAt)}</span>
              </div>
            {/if}
          </div>
        </div>
      {/if}
      {#each activePermits as p (p.id)}
        <div class="msg assistant">
          <div class="permit-card" class:danger={p.dangerous}>
            <div class="permit-head"><span class="permit-dot"></span>需要你确认这个操作{#if p.dangerous}<span class="permit-tag">危险 · 对线上生效</span>{/if}</div>
            <div class="permit-desc">{permitDesc(p)}</div>
            <div class="permit-meta">{p.tool === 'SSH' ? '远程命令' : p.tool}{#if p.tool === 'SSH' && p.arg} · {p.arg}{/if}{#if p.mode === 'ask'} · 询问档{/if}</div>
            {#if p.cmd}
              {#if p.tool === 'SSH'}
                <!-- 远程命令不给收起：它要在用户的真机上跑，不看着它就没法做决定。 -->
                <code class="permit-cmd">{p.cmd}</code>
              {:else if permitCmdOpen.has(p.id)}
                <button class="permit-more" onclick={() => togglePermitCmd(p.id)}>收起命令 ▴</button>
                <code class="permit-cmd">{p.cmd}</code>
              {:else}
                <button class="permit-more" onclick={() => togglePermitCmd(p.id)}>查看具体命令 ▸</button>
              {/if}
            {/if}
            <div class="permit-act">
              <button class="btn sm" onclick={() => respondPermit(p.id, false)}>拒绝</button>
              <button class="btn sm primary" onclick={() => respondPermit(p.id, true)}>批准</button>
            </div>
          </div>
        </div>
      {/each}
    </div>
  </div>

  <div class="composer-wrap">
    {#if activeConvIsCf}
      <div class="cf-bar">
        <span class="tipwrap" data-tip={cfReady ? '在本机跑起来看效果（关预览窗即停）' : '先让 AI 建出页面再预览'}><button class="cb-prev" onclick={startPreview} disabled={previewBusy || !cfReady}><svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M1.6 8s2.4-4.4 6.4-4.4S14.4 8 14.4 8s-2.4 4.4-6.4 4.4S1.6 8 1.6 8Z" stroke="currentColor" stroke-width="1.2" /><circle cx="8" cy="8" r="1.9" stroke="currentColor" stroke-width="1.2" /></svg>{previewBusy ? '启动中…' : '预览'}</button></span>
        <span class="tipwrap" data-tip={cfReady ? '发布到 Cloudflare 上线' : '先让 AI 建出页面再部署'}><button class="cb-prev" onclick={fillDeploy} disabled={viewBusy || !cfReady}><svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M8 12.5V4M4.5 7 8 3.5 11.5 7" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>部署</button></span>
        <span class="tipwrap" data-tip={cfReady ? '存成模板，以后一键复用' : '先让 AI 建出页面再存'}><button class="cb-prev dim" onclick={openSaveTmpl} disabled={!cfReady}><svg width="12" height="12" viewBox="0 0 16 16" fill="none"><rect x="2.6" y="2.6" width="4.3" height="4.3" rx="1" stroke="currentColor" stroke-width="1.3" /><rect x="9.1" y="2.6" width="4.3" height="4.3" rx="1" stroke="currentColor" stroke-width="1.3" /><rect x="2.6" y="9.1" width="4.3" height="4.3" rx="1" stroke="currentColor" stroke-width="1.3" /><rect x="9.1" y="9.1" width="4.3" height="4.3" rx="1" stroke="currentColor" stroke-width="1.3" /></svg>存模板</button></span>
      </div>
    {/if}
    <div class="composer">
      {#if queued && queued.convId === activeConvId}
        <div class="queued-row">
          <span class="queued-ic"><svg width="13" height="13" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.3" /><path d="M8 4.7V8l2.2 1.4" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /></svg></span>
          <span class="queued-t">等这轮结束后发送：{queued.text || '（仅附件）'}{#if queued.atts.length} · {queued.atts.length} 个文件{/if}</span>
          <button class="queued-btn" onclick={editQueued}>编辑</button>
          <button class="queued-x" aria-label="清除等待消息" onclick={clearQueued}>×</button>
        </div>
      {/if}
      {#if attachments.length}
        <div class="attach-row">
          {#each attachments as a, i (a.path)}
            <div class="attach-chip">
              {#if a.preview}<img class="attach-img" src={a.preview} alt="" />{:else}<span class="attach-ic"><svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M9 1.8H4.5A1.3 1.3 0 0 0 3.2 3.1v9.8a1.3 1.3 0 0 0 1.3 1.3h7a1.3 1.3 0 0 0 1.3-1.3V5.5z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /><path d="M9 1.8v3.7h3.8" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /></svg></span>{/if}
              <span class="attach-name" title={a.name}>{a.name}</span>
              <button class="attach-x" aria-label="移除附件" onclick={() => removeAttachment(i)}>×</button>
            </div>
          {/each}
          {#if attaching}<span class="attach-loading">上传中…</span>{/if}
        </div>
      {/if}
      <textarea bind:value={draft} bind:this={draftEl} use:autogrow rows="1" placeholder={viewBusy ? '输入下一条，回车排队，等这轮结束自动发送' : '继续说…（Enter 发送，Shift+Enter 换行，可粘贴/拖入文件）'}
        oncompositionstart={() => (composing = true)} oncompositionend={() => (composing = false)}
        onpaste={onComposerPaste} ondrop={onComposerDrop} ondragover={onComposerDragOver}
        onkeydown={(e) => onComposerKey(e, viewBusy ? queueMessage : send)}></textarea>
      <div class="composer-bar">
        <div class="cb-left">
          {#if activeConvIsCf}
            <span class="cb-ro" title="项目已固定"><span class="cb-ro-t">{activeConv?.site_slug}</span></span>
          {:else if isSshConn}
            <!-- 远程对话没有站点：这里放远端系统的图标 + 机器地址。
                 （不能走下面那支：SiteFav 拿到空 slug 会画出一个「?」占位符。） -->
            <span class="cb-ro" title={activeConn?.ssh_os || '远端系统未知'}>{@render distroMark(distroOf(activeConn), 15)}<span class="cb-ro-t">{activeConv?.site_name}</span></span>
          {:else}
            <span class="cb-ro" title={(activeConv?.site_slugs?.length ?? 0) > 1 ? convSitesTip(activeConv) : '会话的站点已固定，不可更改'}><SiteFav src={siteFav(activeConv?.site_slug ?? '')} label={activeConv?.site_slug ?? ''} size={15} /><span class="cb-ro-t">{activeConv?.site_name || activeConv?.site_slug}</span></span>
          {/if}
        </div>
        <div class="cb-right">
          <Dropdown compact bind:value={threadPerm} options={permOptsFor(activeConv?.brain ?? 'claude', isSshConn)} tone={permTone(threadPerm)} tip={permTipFor(activeConv?.brain ?? 'claude', threadPerm, isSshConn)} onchange={persistThreadPerm} />
          <!-- 模型下拉列全部厂商（图标区分）：同厂商下一轮生效；跨厂商确认后以历史摘要重建续跑 -->
          <ModelFx options={threadComboOpts} value={`${activeConv?.brain ?? 'claude'}::${threadModel}`} effort={threadEffort} lockModel={viewBusy} onpick={(v: string) => { void pickThreadCombo(v); }} oneffort={persistThreadEffort} />
          <UsageRing ctx={activeConv?.ctx_tokens ?? 0} limit={ctxLimitAdaptive(activeConv?.brain ?? 'claude', activeConv?.model ?? '', activeConv?.ctx_tokens ?? 0)} total={activeConv?.total_tokens ?? 0} />
          {#if viewBusy}
            {#if draft.trim() || attachments.length}
              <button class="send queue" onclick={queueMessage} title="排队：等这轮结束后自动发送">↑</button>
            {/if}
            <button class="send stop" onclick={stop} title="停止">■</button>
          {:else}
            <button class="send" onclick={send} disabled={!draft.trim() && !attachments.length} title="发送（Enter）">↑</button>
          {/if}
        </div>
      </div>
    </div>
  </div>
{/snippet}

<!-- 顶栏的一个负载读数：标签 + 百分比 + 一条细底纹。
     pct=null＝还没算出来（CPU 要两次采样求差，见 pollStats）。 -->
{#snippet loadStat(label: string, pct: number | null, tip: string)}
  <span class="hstat" class:warn={pct !== null && pct >= 75} class:hot={pct !== null && pct >= 90} data-tip={tip}>
    <span class="hstat-l">{label}</span>
    <span class="hstat-v">{pct === null ? '—' : pct + '%'}</span>
  </span>
{/snippet}

<!-- 列宽手柄：坐在定宽列的左边缘上（就是列头之间那根分隔线）。做成 button 是为了键盘也够得着
     （←/→ 微调），顺带免掉给 div 挂事件的一堆 a11y 警告。 -->
{#snippet colGrip(k: 'perm' | 'size' | 'date')}
  <button class="fh-grip" aria-label="调整列宽（左右方向键微调）" data-tip="拖动调整列宽"
    onmousedown={(e) => startColDrag(e, k)}
    onclick={(e) => e.stopPropagation()}
    onkeydown={(e) => onColKey(e, k)}></button>
{/snippet}

{#snippet sshMark(size: number)}<svg width={size} height={size} viewBox="0 0 16 16" fill="none"><rect x="1.6" y="2.6" width="12.8" height="10.8" rx="2" stroke="currentColor" stroke-width="1.3" /><path d="M4.4 6.2 6.6 8l-2.2 1.8M8.2 10.2h3.4" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /></svg>{/snippet}

<!-- 发行版图标：按 os-release 的 ID 选（distroOf 做归一化 + ID_LIKE 兜底）。
     都是简化几何形，认得出即可；认不出的发行版走最后那个通用「服务器」形。 -->
{#snippet distroMark(id: string, size: number)}
  {#if id === 'ubuntu'}
    <!-- Ubuntu：三点「朋友圈」 -->
    <svg class="distro" width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="8" cy="8" r="5.6" stroke="#E95420" stroke-width="1.5" />
      <circle cx="8" cy="2.9" r="1.75" fill="#E95420" /><circle cx="3.6" cy="10.6" r="1.75" fill="#E95420" /><circle cx="12.4" cy="10.6" r="1.75" fill="#E95420" />
    </svg>
  {:else if id === 'debian'}
    <!-- Debian：开口螺旋 -->
    <svg class="distro" width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path d="M11.6 4.2A5.2 5.2 0 1 0 12 11" stroke="#A80030" stroke-width="1.5" stroke-linecap="round" />
      <path d="M9.9 6.1a3 3 0 1 0 .5 3.6" stroke="#A80030" stroke-width="1.4" stroke-linecap="round" />
    </svg>
  {:else if id === 'alpine'}
    <!-- Alpine：山 -->
    <svg class="distro" width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="8" cy="8" r="6.2" fill="#0D597F" />
      <path d="M4 10.6 6.3 6.6l1.5 2.6M7.4 10.6l2.4-4.2 2.4 4.2z" stroke="#fff" stroke-width="1.1" stroke-linejoin="round" />
    </svg>
  {:else if id === 'fedora'}
    <svg class="distro" width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="8" cy="8" r="6.2" fill="#51A2DA" />
      <path d="M9.9 4.6h-.8a1.9 1.9 0 0 0-1.9 1.9v4a1.5 1.5 0 1 1-1.5-1.5h3.4" stroke="#fff" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  {:else if id === 'arch'}
    <svg class="distro" width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path d="M8 1.8 14 13.6c-1.6-.9-3-1.5-3.6-1.7L8 7.2l-2.4 4.7c-.6.2-2 .8-3.6 1.7L8 1.8z" fill="#1793D1" />
    </svg>
  {:else if id === 'centos' || id === 'rhel' || id === 'rocky' || id === 'almalinux'}
    <!-- RHEL 系：四色方块 -->
    <svg class="distro" width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect x="1.8" y="1.8" width="5.4" height="5.4" rx="1" fill="#932279" /><rect x="8.8" y="1.8" width="5.4" height="5.4" rx="1" fill="#EFA724" />
      <rect x="1.8" y="8.8" width="5.4" height="5.4" rx="1" fill="#79BCE8" /><rect x="8.8" y="8.8" width="5.4" height="5.4" rx="1" fill="#9CCD2A" />
    </svg>
  {:else}
    <!-- 认不出的（含没探到系统信息时）：中性服务器形 -->
    <svg class="distro" width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect x="2" y="2.6" width="12" height="4.6" rx="1.2" stroke="currentColor" stroke-width="1.2" />
      <rect x="2" y="8.8" width="12" height="4.6" rx="1.2" stroke="currentColor" stroke-width="1.2" />
      <circle cx="4.6" cy="4.9" r=".8" fill="currentColor" /><circle cx="4.6" cy="11.1" r=".8" fill="currentColor" />
    </svg>
  {/if}
{/snippet}

{#snippet wrNote()}<div class="wr-note"><svg class="wr-ic" width="15" height="15" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.6" stroke="currentColor" stroke-width="1.3" /><path d="M8 4.8v4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /><circle cx="8" cy="11.1" r="0.6" fill="currentColor" /></svg><span>{wrInstalling ? '正在从 npm 装 wrangler，首次约 1–2 分钟，别关窗…' : '还没装 wrangler，预览 / 部署要用'}</span><button class="wr-btn" onclick={installWrangler} disabled={wrInstalling}>{#if wrInstalling}<span class="wr-spin"></span>安装中 {elapsedLabel(wrElapsed)}{:else}一键安装{/if}</button></div>{/snippet}

{#snippet taskIcon(kind: string)}
  <span class="ts-ic">
    {#if kind === 'article'}
      <svg width="17" height="17" viewBox="0 0 20 20" fill="none"><path d="M12.4 3H6.2A1.7 1.7 0 0 0 4.5 4.7v10.6A1.7 1.7 0 0 0 6.2 17h7.6a1.7 1.7 0 0 0 1.7-1.7V6.1L12.4 3Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" /><path d="M12 3.3V6a1 1 0 0 0 1 1h2.7M7.6 10.4h4.8M7.6 13h4.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /></svg>
    {:else if kind === 'sitebuild'}
      <svg width="17" height="17" viewBox="0 0 20 20" fill="none"><rect x="3.3" y="3.3" width="13.4" height="13.4" rx="2.2" stroke="currentColor" stroke-width="1.4" /><path d="M3.5 7.6h13M7.6 7.8v8.8" stroke="currentColor" stroke-width="1.4" /></svg>
    {:else}
      <svg width="17" height="17" viewBox="0 0 20 20" fill="none"><path d="M4 6.6A2.6 2.6 0 0 1 6.6 4h6.8A2.6 2.6 0 0 1 16 6.6v3.9a2.6 2.6 0 0 1-2.6 2.6H8.2l-3.1 2.7a.42.42 0 0 1-.7-.32V6.6Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" /></svg>
    {/if}
  </span>
{/snippet}

{#snippet cmds(tools: ToolCall[])}
  <details class="cmds">
    <summary>
      <svg class="cmd-ic" width="13" height="13" viewBox="0 0 16 16" fill="none">
        <rect x="1.5" y="2.5" width="13" height="11" rx="2" stroke="currentColor" stroke-width="1.3" />
        <path d="M4.5 6l2 2-2 2M8.5 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
      <span>{tools.length} 条命令</span>
      <svg class="cmd-chev" width="11" height="11" viewBox="0 0 12 12" fill="none"><path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
    </summary>
    <div class="tools">{#each tools as t, i (i)}{@render toolChip(t)}{/each}</div>
  </details>
{/snippet}

<!-- 设置弹窗 -->
{#if setupOpen}
  <div class="mask" role="presentation" onclick={() => (setupOpen = false)}></div>
  <div class="sheet">
    <header class="sheet-head"><b>连接与模型</b><button class="x" onclick={() => (setupOpen = false)}>×</button></header>
    <div class="sheet-body">
      <div class="sec-head"><span>连接</span><span class="sec-acts"><button class="btn small ghost bare" onclick={openCfConnect}>{@render cfMark(13)} Cloudflare</button><button class="btn small ghost bare" onclick={importPack} disabled={importBusy}>{importBusy ? '导入中…' : '＋ 导入 gcms 技能包'}</button></span></div>
      {#if conns.length === 0}<p class="hint">还没有连接。导入 gcms 技能包，或连接 Cloudflare。</p>{/if}
      <div class="conn-list">
        {#each conns as c (c.id)}
          <div class="conn-row {activeConnId === c.id ? 'on' : ''}" role="button" tabindex="0"
            onclick={() => selectConn(c.id)} onkeydown={(e) => e.key === 'Enter' && selectConn(c.id)}
            oncontextmenu={(e) => openConnCtx(e, c)}>
            {#if c.kind === 'cloudflare'}{@render cfMark(22)}{:else if c.kind === 'ssh'}{@render sshMark(22)}{:else}<SiteMark size={22} />{/if}
            <span class="conn-main"><b>{c.name}</b>
              {#if c.kind === 'ssh'}
                <small class="cs-os">{@render distroMark(distroOf(c), 12)}{sshSub(c)}</small>
              {:else}
                <small>{c.key_prefix} · {c.kind === 'cloudflare' ? 'Cloudflare' : c.key_kind === 'gcmsp_' ? '平台' : '单站'}{#if activeConnId === c.id} · {sites.length} 站点{/if}</small>
              {/if}</span>
            {#if c.kind === 'ssh'}
              <button class="icon-btn sm" title="编辑连接（地址 / 用户 / 密码或密钥 / 指纹）" onclick={(e) => { e.stopPropagation(); openSshEdit(c); }}>
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10.8 2.9l2.3 2.3M11.4 2.3l2.3 2.3-8 8-3 .7.7-3 8-8z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /></svg>
              </button>
            {/if}
            {#if packUpdates[c.id]}
              <button class="pack-upd" title="技能包有新版 {packUpdates[c.id]}——一键就地升级，连接与对话全部保留" disabled={!!packUpdating[c.id]}
                onclick={(e) => { e.stopPropagation(); upgradePack(c.id); }}>{packUpdating[c.id] ? '升级中…' : `升级技能包 ${packUpdates[c.id]}`}</button>
            {/if}
            {#if activeConnId === c.id && c.kind !== 'cloudflare' && c.kind !== 'ssh'}
              <button class="icon-btn sm" title="刷新站点（技能包新增站点后点这里）" onclick={(e) => { e.stopPropagation(); refreshSites(); }}>{@render refreshIcon(discoveryLoading)}</button>
            {/if}
            <button class="x sm" title="删除连接" onclick={(e) => { e.stopPropagation(); removeConn(c.id); }}>×</button>
          </div>
        {/each}
      </div>

      <div class="sec-head mt"><span>本地模型</span><button class="icon-btn" onclick={refreshBrainsManual} title="刷新">{@render refreshIcon(brainsBusy)}</button></div>
      {#if brains}
        <div class="brains-list">
        {#each [{ b: 'claude' as Brain, st: brains.claude, name: 'Claude Code', cmd: CLAUDE_INSTALL_CMD }, { b: 'codex' as Brain, st: brains.codex, name: 'Codex', cmd: 'npm i -g @openai/codex' }, { b: 'grok' as Brain, st: brains.grok, name: 'Grok', cmd: GROK_INSTALL_CMD }] as r (r.b)}
          <div class="brain-block">
          <div class="brain-row">
            <span class="brain-ic"><BrainIcon brain={r.b} size={18} /></span>
            <span class="brain-main"><b>{r.name}</b>
              <small>{#if !r.st.found}未安装{:else if r.st.logged_in === false}未登录{:else}{r.st.version || '已就绪'}{/if}</small></span>
            <span class="brain-dot"><span class="dot {r.st.found && r.st.logged_in ? 'ok' : r.st.found ? 'warn' : 'off'}"></span></span>
            {#if r.st.found && r.st.logged_in === false}<button class="authbtn" onclick={() => authorize(r.b)} disabled={authWaiting === r.b}>{#if authWaiting === r.b}<span class="wr-spin"></span>等待授权…{:else}去授权 ↗{/if}</button>{/if}
          </div>
          {#if !r.st.found}
            <!-- 与主界面同款一键安装（同 invoke/进度态/错误处理），放在「未安装」状态文字下方、小号靠左；
                 手动命令降级为同行的复制小图标 -->
            <div class="brain-install">
              <button class="wr-btn" onclick={r.b === 'claude' ? installClaude : r.b === 'codex' ? installCodex : installGrok}
                disabled={r.b === 'claude' ? claudeInstalling : r.b === 'codex' ? codexInstalling : grokInstalling}>
                {#if (r.b === 'claude' && claudeInstalling) || (r.b === 'codex' && codexInstalling) || (r.b === 'grok' && grokInstalling)}
                  <span class="wr-spin"></span>{nodeBoot || `安装中 ${elapsedLabel(r.b === 'claude' ? claudeElapsed : r.b === 'codex' ? codexElapsed : grokElapsed)}`}
                {:else}一键安装{/if}
              </button>
              <button class="cli-copy" title={`手动安装命令：${r.cmd}（点击复制）`} aria-label="复制手动安装命令" onclick={() => copyCmd(r.cmd)}>
                {#if copiedCmd === r.cmd}
                  <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M3.5 8.5l3 3 6-7" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" /></svg>
                {:else}
                  <svg width="13" height="13" viewBox="0 0 16 16" fill="none"><rect x="5.4" y="5.4" width="8.2" height="8.2" rx="1.6" stroke="currentColor" stroke-width="1.3" /><path d="M10.6 5.4V4.1A1.6 1.6 0 0 0 9 2.5H4.1A1.6 1.6 0 0 0 2.5 4.1V9a1.6 1.6 0 0 0 1.6 1.6h1.3" stroke="currentColor" stroke-width="1.3" /></svg>
                {/if}
              </button>
            </div>
          {/if}
          {#if r.st.found}
            <div class="cust">
              <button class="cust-head" type="button" onclick={() => (customOpen[r.b] = !customOpen[r.b])}>
                <svg class="cust-chev" class:open={customOpen[r.b]} width="10" height="10" viewBox="0 0 12 12" fill="none"><path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
                自定义模型{#if customsOf(r.b).length}<span class="cust-n">{customsOf(r.b).length}</span>{/if}
              </button>
              {#if customOpen[r.b]}
                <div class="cust-body">
                  {#each customsOf(r.b) as id (id)}
                    <div class="cust-chip"><span class="cust-id">{id}</span><button class="cust-x" title="删除" onclick={() => removeCustom(r.b, id)}>×</button></div>
                  {/each}
                  <div class="cust-add">
                    <input class="tin" bind:value={customDraft[r.b]} placeholder={r.b === 'codex' ? '如 gpt-5.5 / o3' : r.b === 'grok' ? '如 grok-4.5' : '如 claude-opus-4-8'}
                      spellcheck="false" autocapitalize="off" autocorrect="off" onkeydown={(e) => e.key === 'Enter' && addCustom(r.b)} />
                    <button class="btn sm" onclick={() => addCustom(r.b)} disabled={!(customDraft[r.b] ?? '').trim()}>添加</button>
                  </div>
                </div>
              {/if}
            </div>
          {/if}
          </div>
        {/each}
        </div>
      {/if}
      <p class="hint tos">自定义模型 ID 会作为该厂商模型下拉里的附加档位（可加多个）；仅限本人订阅账户驱动本地官方 CLI，密钥存 {keystoreName}。</p>

      <div class="sec-head mt"><span>关于</span></div>
      <div class="brain-row">
        <span class="brain-ic"><AppIcon size={20} /></span>
        <span class="brain-main"><b>GCMS Pilot</b><small>{appVersion ? `v${appVersion}` : ''}{updMsg ? ` · ${updMsg}` : ''}</small></span>
        <button class="btn small ghost bare" onclick={runUpdate} disabled={updBusy}>{updBusy ? '检查中…' : '检查更新'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- 密钥输入 -->
{#if keyOpen}
  <div class="mask" role="presentation" onclick={() => !importBusy && (keyOpen = false)}></div>
  <div class="modal">
    <header class="sheet-head"><div><b>原始技能包 · 需要密钥</b><small class="dim">{keyBase}</small></div><button class="x" onclick={() => (keyOpen = false)} disabled={importBusy}>×</button></header>
    <div class="sheet-body">
      <p class="hint">粘贴 gcms 后台生成的密钥（gcmsp_…），只会存入 {keystoreName}，不写进任何文件。</p>
      <input class="tin" bind:value={keyVal} type="password" placeholder="gcmsp_…" autocomplete="off" disabled={importBusy} onkeydown={(e) => e.key === 'Enter' && confirmKey()} />
      {#if keyErr}<div class="err-note">{keyErr}</div>{/if}
      <div class="row-end">
        <button class="btn ghost" onclick={() => (keyOpen = false)} disabled={importBusy}>取消</button>
        <button class="btn primary" onclick={confirmKey} disabled={importBusy || !keyVal.trim()}>{importBusy ? '导入中…' : '导入'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- 连接 Cloudflare -->
{#if sshOpen}
  <div class="mask" role="presentation" onclick={() => (sshOpen = false)}></div>
  <div class="modal wide">
    <header class="sheet-head"><b>{sshEditId ? '编辑远程连接' : '新建远程连接'}</b><button class="x" onclick={() => (sshOpen = false)}>×</button></header>
    <div class="sheet-body">
      <p class="hint">{#if sshEditId}改完地址 / 用户 / 认证方式后要重新「测试连接」核对指纹再保存；只改名字可以直接保存。密码 / 私钥口令留空＝沿用 {keystoreName} 里已存的那条。{:else}SSH 到一台远程机器：能开终端、管文件、让 AI 帮你干活。密码 / 私钥口令只进 {keystoreName}，绝不落盘。{/if}</p>
      <div class="trow">
        <div class="tfield" style="flex:2"><span>主机</span><input class="tin" bind:value={sshF.host} placeholder="例如 1.2.3.4 或 server.example.com" spellcheck="false" autocapitalize="off" autocorrect="off" /></div>
        <div class="tfield" style="flex:1"><span>端口</span><input class="tin" type="number" bind:value={sshF.port} placeholder="22" /></div>
      </div>
      <div class="trow">
        <div class="tfield"><span>用户名</span><input class="tin" bind:value={sshF.user} placeholder="root" spellcheck="false" autocapitalize="off" autocorrect="off" /></div>
        <div class="tfield"><span>认证方式</span><Dropdown bind:value={sshF.auth} options={sshAuthOpts} /></div>
      </div>
      {#if sshF.auth === 'password'}
        <div class="tfield"><span>密码{#if sshEditId && sshF0.auth === 'password'}（留空＝不改）{/if}</span><input class="tin" type="password" bind:value={sshF.password} autocomplete="off" placeholder={sshEditId && sshF0.auth === 'password' ? '不改就别填' : ''} /></div>
      {:else}
        <div class="tfield"><span>私钥文件（只记住路径，不会拷贝你的私钥）</span>
          <div class="ssh-key-row">
            <input class="tin" bind:value={sshF.keyPath} placeholder="~/.ssh/id_ed25519" spellcheck="false" autocapitalize="off" autocorrect="off" />
            <button class="btn sm" onclick={pickSshKey}>选择…</button>
          </div>
        </div>
        <div class="tfield"><span>私钥口令（没有就留空{#if sshEditId && sshF0.auth === 'key'}；不改也留空{/if}）</span><input class="tin" type="password" bind:value={sshF.keyPass} autocomplete="off" /></div>
      {/if}
      <div class="tfield"><span>名称（可选）</span><input class="tin" bind:value={sshF.name} placeholder={sshF.user && sshF.host ? `${sshF.user}@${sshF.host}` : '留空自动用 user@host'} /></div>

      {#if sshErr}<div class="err-note">{sshErr}</div>{/if}

      {#if sshFp}
        <!-- TOFU：指纹必须由人确认过才保存（后端无指纹直接拒）。 -->
        <div class="ssh-fp" class:alarm={sshFpChanged}>
          {#if sshFpChanged}
            <!-- 这台机器的主机密钥变了。可能是重装/换机，也可能是有人在中间——不能轻轻放过。 -->
            <b class="fp-alarm">⚠ 这台机器的指纹变了！</b>
            <small>之前记住的是：</small>
            <code class="fp-old">{sshOldFp}</code>
            <small>现在连上看到的是：</small>
            <code>{sshFp}</code>
            <small>服务器重装 / 换机器会这样，<b>被中间人劫持也会这样</b>。请到服务器上执行
              <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code> 亲自核对；对不上就别保存。</small>
          {:else}
            <b>认证通过。请核对主机指纹：</b>
            <code>{sshFp}</code>
            <small>请确认它和你在服务器上执行 <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code> 看到的一致 —— 对不上可能是中间人。确认后此指纹会被记住，以后变了会直接拒连。</small>
          {/if}
        </div>
      {/if}

      <div class="row-end">
        <button class="btn ghost" onclick={() => (sshOpen = false)}>取消</button>
        {#if sshRenameOnly}
          <!-- 只改了名字：和「能不能连上」无关，不必为此去连一次机器 -->
          <button class="btn primary" onclick={saveSshEdit} disabled={sshConnecting}>{sshConnecting ? '保存中…' : '保存'}</button>
        {:else if !sshFp}
          <button class="btn primary" onclick={testSsh} disabled={sshTesting || !sshCanTest}>{sshTesting ? '连接中…' : '测试连接'}</button>
        {:else if sshEditId}
          <button class="btn primary" class:danger={sshFpChanged} onclick={saveSshEdit} disabled={sshConnecting}>{sshConnecting ? '保存中…' : sshFpChanged ? '我已核对，接受新指纹' : '指纹无误，保存'}</button>
        {:else}
          <button class="btn primary" onclick={confirmSsh} disabled={sshConnecting}>{sshConnecting ? '添加中…' : '指纹无误，添加'}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}

{#if connCtx}
  <!-- 连接切换器的右键菜单。菜单项按连接类型给：能做的才列，做不了的不摆在那儿灰着。 -->
  {@const c = connCtx.conn}
  <div class="ctx-menu fctx" style="left:{connCtx.x}px; top:{connCtx.y}px" role="menu" tabindex="-1">
    <button class="ctx-item" role="menuitem" onclick={() => openConnWindow(c.id)}>新窗口打开</button>
    {#if c.kind === 'ssh'}
      <button class="ctx-item" role="menuitem" onclick={() => { switcherOpen = false; selectConn(c.id); openRemote(); }}>远程终端</button>
      <div class="ctx-div"></div>
      <button class="ctx-item" role="menuitem" onclick={() => { switcherOpen = false; openSshEdit(c); }}>编辑连接…</button>
    {:else if c.kind === 'gcms'}
      <div class="ctx-div"></div>
      <button class="ctx-item" role="menuitem" onclick={() => { switcherOpen = false; selectConn(c.id); refreshSites(); }}>刷新站点</button>
      {#if packUpdates[c.id]}
        <button class="ctx-item" role="menuitem" onclick={() => upgradePack(c.id)} disabled={!!packUpdating[c.id]}>升级技能包 {packUpdates[c.id]}</button>
      {/if}
    {/if}
    <div class="ctx-div"></div>
    <button class="ctx-item danger" role="menuitem" onclick={() => { switcherOpen = false; removeConn(c.id); }}>删除连接…</button>
  </div>
{/if}

{#if fctxMenu}
  <!-- 远程文件右键菜单。挂最外层：列表自己是滚动容器，菜单放里面会被裁掉。 -->
  <div class="ctx-menu fctx" style="left:{fctxMenu.x}px; top:{fctxMenu.y}px" role="menu" tabindex="-1">
    {#if fctxMenu.row}
      {@const r = fctxMenu.row}
      <button class="ctx-item" role="menuitem" onclick={() => sftpOpenRow(r)}>{r.dir && !r.link ? '打开' : r.link ? '打开（跟随链接）' : '打开（编辑）'}</button>
      {#if !r.dir || r.link}
        <button class="ctx-item" role="menuitem" onclick={() => sftpDownload(r)} disabled={!!sftpXfer}>下载…</button>
      {/if}
      {#if r.dir && !r.link}
        <button class="ctx-item" role="menuitem" onclick={() => sftpUpload(r.path)} disabled={!!sftpXfer}>上传到这里…</button>
        <button class="ctx-item" role="menuitem" onclick={() => openMkdir(r.path)}>在这里新建文件夹</button>
      {/if}
      <div class="ctx-div"></div>
      <button class="ctx-item" role="menuitem" onclick={() => copyPath(r.path)}>复制路径</button>
      <button class="ctx-item" role="menuitem" onclick={() => openRename(r)}>重命名…</button>
      <div class="ctx-div"></div>
      <button class="ctx-item danger" role="menuitem" onclick={() => sftpDelete(r)}>删除…</button>
    {:else}
      <button class="ctx-item" role="menuitem" onclick={() => openMkdir()}>新建文件夹</button>
      <button class="ctx-item" role="menuitem" onclick={() => sftpUpload()} disabled={!!sftpXfer}>上传文件…</button>
      <div class="ctx-div"></div>
      <button class="ctx-item" role="menuitem" onclick={() => copyPath(sftpPath)}>复制当前路径</button>
      <button class="ctx-item" role="menuitem" onclick={sftpRefresh}>刷新</button>
    {/if}
  </div>
{/if}

{#if fsAsk}
  <div class="mask" role="presentation" onclick={() => !fsAsk?.busy && (fsAsk = null)}></div>
  <div class="modal">
    <header class="sheet-head"><b>{fsAsk.mode === 'mkdir' ? '新建文件夹' : `重命名「${fsAsk.from}」`}</b><button class="x" onclick={() => (fsAsk = null)} disabled={fsAsk.busy}>×</button></header>
    <div class="sheet-body">
      <p class="hint">位置：<code>{sftpPath}</code></p>
      <!-- svelte-ignore a11y_autofocus -->
      <input class="tin" bind:value={fsAsk.value} placeholder={fsAsk.mode === 'mkdir' ? '文件夹名' : '新名字'} spellcheck="false" autocapitalize="off" autocorrect="off" autofocus disabled={fsAsk.busy} onkeydown={(e) => e.key === 'Enter' && confirmFsAsk()} />
      {#if fsAsk.err}<div class="err-note">{fsAsk.err}</div>{/if}
      <div class="row-end">
        <button class="btn ghost" onclick={() => (fsAsk = null)} disabled={fsAsk.busy}>取消</button>
        <button class="btn primary" onclick={confirmFsAsk} disabled={fsAsk.busy || !fsAsk.value.trim()}>{fsAsk.busy ? '处理中…' : '确定'}</button>
      </div>
    </div>
  </div>
{/if}

{#if edOpen}
  <div class="mask" role="presentation" onclick={() => !edSaving && (edOpen = false)}></div>
  <div class="modal ed-modal">
    <header class="sheet-head"><div><b>编辑文件</b><small class="dim ed-path">{edPath}</small></div><button class="x" onclick={() => (edOpen = false)} disabled={edSaving}>×</button></header>
    <div class="sheet-body ed-body">
      {#if edLoading}
        <div class="files-empty">读取中…</div>
      {:else if edErr && edReadOnly}
        <div class="err-note">{edErr}</div>
      {:else}
        <textarea class="ed-ta" bind:value={edText} spellcheck="false" autocapitalize="off" disabled={edSaving}></textarea>
        {#if edErr}<div class="err-note">{edErr}</div>{/if}
      {/if}
      <div class="row-end">
        <button class="btn ghost" onclick={() => (edOpen = false)} disabled={edSaving}>{edReadOnly ? '关闭' : '取消'}</button>
        {#if !edReadOnly && !edLoading}
          <button class="btn primary" onclick={saveEdit} disabled={edSaving}>{edSaving ? '保存中…' : '保存'}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}

{#if cfOpen}
  <div class="mask" role="presentation" onclick={() => !cfConnecting && !cfVerifying && (cfOpen = false)}></div>
  <div class="modal cf-modal">
    <header class="sheet-head"><div><b>连接 {@render cfMark(15)} Cloudflare</b><small class="dim" style="margin-left:10px">token 只进钥匙串，绝不落盘</small></div><button class="x" onclick={() => (cfOpen = false)} disabled={cfConnecting}>×</button></header>
    <div class="sheet-body">
      <div class="cf-step">
        <p class="cf-step-t">① 在 Cloudflare 建一个 API Token（选 <b>Create Custom Token</b>）</p>
        <button class="cf-perms-toggle" type="button" onclick={() => (cfPermsOpen = !cfPermsOpen)}>
          <svg class="cf-chev" class:open={cfPermsOpen} width="10" height="10" viewBox="0 0 12 12" fill="none"><path d="M3 4.5 6 7.5 9 4.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
          所需权限（5 项）
        </button>
        {#if cfPermsOpen}
          <ul class="cf-perms">
            <li>Account · Cloudflare Pages · <b>Edit</b></li>
            <li>Account · Workers Scripts · <b>Edit</b></li>
            <li>Account · D1 · <b>Edit</b></li>
            <li>Zone · DNS · <b>Edit</b>（绑定自定义域名用）</li>
            <li>Zone · Zone · <b>Read</b></li>
          </ul>
        {/if}
        <button class="btn soft" onclick={() => openUrl(CF_TOKEN_URL)}>打开 {@render cfMark(14)} Cloudflare 令牌页 ↗</button>
      </div>
      <div class="cf-step">
        <p class="cf-step-t">② 把生成的 Token 粘到这里：</p>
        <div class="cf-row">
          <input class="tin" type="password" bind:value={cfToken} placeholder="粘贴 API Token" autocomplete="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && verifyCf()} />
          <button class="btn small primary" onclick={verifyCf} disabled={cfVerifying || !cfToken.trim()}>{cfVerifying ? '验证中…' : '验证'}</button>
        </div>
      </div>
      {#if cfAccounts.length}
        <div class="cf-step">
          <p class="cf-step-t">③ 选择账号 · 给连接起个名：</p>
          <Dropdown bind:value={cfAccountId} options={cfAccounts.map((a) => ({ value: a.id, label: a.name, sub: a.id }))} placeholder="选择账号" />
          <input class="tin" style="margin-top:7px" bind:value={cfName} placeholder="连接名称（可留默认）" />
          {#if cfZones.length}<p class="hint">可管理域名：{cfZones.slice(0, 6).map((z) => z.name).join('、')}{cfZones.length > 6 ? ` 等 ${cfZones.length} 个` : ''}</p>{:else}<p class="hint">没检测到可管理域名——绑定自定义域名需 Zone 权限，可稍后补 token。</p>{/if}
        </div>
      {/if}
      {#if brains?.wrangler && !brains.wrangler.found}{@render wrNote()}{/if}
      {#if cfErr}<div class="err-note">{cfErr}</div>{/if}
      <div class="row-end">
        <button class="btn small ghost" onclick={() => (cfOpen = false)} disabled={cfConnecting}>取消</button>
        <button class="btn small primary" onclick={confirmCf} disabled={cfConnecting || !cfAccountId}>{cfConnecting ? '连接中…' : '连接'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- 存为模板 -->
{#if saveTmplOpen}
  <div class="mask" role="presentation" onclick={() => !saveTmplBusy && (saveTmplOpen = false)}></div>
  <div class="modal">
    <header class="sheet-head"><b>存为模板</b><button class="x" onclick={() => (saveTmplOpen = false)} disabled={saveTmplBusy}>×</button></header>
    <div class="sheet-body">
      <p class="hint">当前项目会被复制成模板（自动去掉密钥、依赖、构建产物），之后可引用它快速建站。</p>
      <input class="tin" bind:value={saveTmplName} placeholder="模板名，如 minimal-landing" spellcheck="false" />
      <input class="tin" style="margin-top:7px" bind:value={saveTmplDesc} placeholder="一句话描述（可选）" />
      {#if saveTmplErr}<div class="err-note">{saveTmplErr}</div>{/if}
      <div class="row-end">
        <button class="btn ghost" onclick={() => (saveTmplOpen = false)} disabled={saveTmplBusy}>取消</button>
        <button class="btn primary" onclick={confirmSaveTmpl} disabled={saveTmplBusy || !saveTmplName.trim()}>{saveTmplBusy ? '保存中…' : '存为模板'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- 用模板建站 -->
{#if useTmplOpen}
  <div class="mask" role="presentation" onclick={() => !useTmplBusy && (useTmplOpen = false)}></div>
  <div class="modal">
    <header class="sheet-head"><div><b>用模板建站</b><small class="dim" style="margin-left:10px">{useTmplName}</small></div><button class="x" onclick={() => (useTmplOpen = false)} disabled={useTmplBusy}>×</button></header>
    <div class="sheet-body">
      {#if !isCfConn}<p class="hint warn-text">请先在左下角切到一个 Cloudflare 连接，再用模板建站。</p>{/if}
      <p class="hint">给新项目起个名，模板文件会拷进去，然后在对话里描述你的定制。</p>
      <input class="tin" bind:value={useTmplProject} placeholder="新项目名，如 my-landing" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && confirmUseTmpl()} />
      {#if useTmplErr}<div class="err-note">{useTmplErr}</div>{/if}
      <div class="row-end">
        <button class="btn ghost" onclick={() => (useTmplOpen = false)} disabled={useTmplBusy}>取消</button>
        <button class="btn primary" onclick={confirmUseTmpl} disabled={useTmplBusy || !useTmplProject.trim() || !isCfConn}>{useTmplBusy ? '创建中…' : '创建项目'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- 提示词 新建/编辑 -->
{#if promptEditOpen}
  <div class="mask" role="presentation" onclick={() => (promptEditOpen = false)}></div>
  <div class="modal wide">
    <header class="sheet-head"><b>{promptEditId ? '编辑提示词' : '新增提示词'}</b><button class="x" onclick={() => (promptEditOpen = false)}>×</button></header>
    <div class="sheet-body">
      <input class="tin" bind:value={promptEditTitle} placeholder="标题，如 手冲咖啡落地页" spellcheck="false" />
      <textarea class="tin" style="margin-top:7px; min-height:150px; line-height:1.5" bind:value={promptEditBody} placeholder="写清楚你要什么样的网站：给谁看、要哪些区块、风格方向，合适的话让它把表单存进 D1，结尾让它先给方案。"></textarea>
      <div class="row-end">
        <button class="btn ghost" onclick={() => (promptEditOpen = false)}>取消</button>
        <button class="btn primary" onclick={savePrompt} disabled={!promptEditTitle.trim() || !promptEditBody.trim()}>保存</button>
      </div>
    </div>
  </div>
{/if}

<!-- 定时任务 新建/编辑 -->
{#if taskModalOpen}
  <div class="mask" role="presentation" onclick={() => (taskModalOpen = false)}></div>
  <div class="modal wide">
    <header class="sheet-head"><b>{tf.id ? '编辑定时任务' : '新建定时任务'}</b><button class="x" onclick={() => (taskModalOpen = false)}>×</button></header>
    <div class="sheet-body">
      {#if tf.id && tf.connId !== activeConnId}
        <p class="hint">此任务属于连接「{tf.connName || tf.connId}」，编辑时保持不变。</p>
      {/if}
      <div class="tfield"><span>站点（可多选，每站各跑一轮）</span>
        <Dropdown bind:value={sitePick} options={taskSiteOpts} placeholder={tf.siteSlugs.length ? '继续添加站点…' : '选择站点'} onchange={addTaskSite} />
        {#if tf.siteSlugs.length}
          <div class="tsites">
            {#each tf.siteSlugs as s (s)}
              <span class="tsite-chip"><SiteFav src={siteFav(s)} label={s} size={13} />{taskSiteName(s)}<button type="button" aria-label="移除站点" onclick={() => removeTaskSite(s)}>×</button></span>
            {/each}
          </div>
        {/if}
      </div>
      <div class="trow">
        <div class="tfield"><span>类型</span><Dropdown bind:value={tf.taskType} options={taskTypeOpts} /></div>
        <div class="tfield"><span>任务名称（可选）</span><input class="tin" bind:value={tf.title} placeholder="例如：每日热点速写" /></div>
      </div>
      <div class="tfield"><span>厂商与模型（与对话中一致）</span>
        <div class="tfield-fx"><ModelFx options={comboOpts} value={`${tf.brain}::${tf.model}`} effort={tf.effort} onpick={(v: string) => { const i = v.indexOf('::'); if (i > 0) { tf.brain = v.slice(0, i); tf.model = v.slice(i + 2); } }} oneffort={(v: string) => (tf.effort = v)} /></div></div>
      <div class="tfield"><span>指令（每次到点就把这句话发给模型）</span>
        <textarea bind:value={tf.prompt} rows="3" placeholder="例如：围绕本周科技热点写一篇 800 字左右的中文文章，存草稿，完成后给我预览链接"></textarea></div>
      <div class="trow">
        <div class="tfield"><span>周期</span><Dropdown bind:value={tf.period} options={periodOpts} /></div>
        <div class="tfield"><span>首次运行（可选，留空则一个周期后）</span><input class="tin" type="datetime-local" bind:value={tf.firstRun} /></div>
      </div>
      <label class="tcheck"><input type="checkbox" bind:checked={tf.enabled} /><span>创建后立即启用</span></label>
      <div class="row-end">
        <button class="btn ghost" onclick={() => (taskModalOpen = false)}>取消</button>
        <button class="btn primary" onclick={saveTask} disabled={!tf.siteSlugs.length || !tf.prompt.trim()}>{tf.id ? '保存' : '创建'}</button>
      </div>
    </div>
  </div>
{/if}

{#if taskHistoryFor}
  <div class="mask" role="presentation" onclick={() => (taskHistoryFor = null)}></div>
  <div class="modal wide">
    <header class="sheet-head"><b>运行记录 · {taskHistoryFor.title}</b><button class="x" onclick={() => (taskHistoryFor = null)}>×</button></header>
    <div class="sheet-body">
      {#each taskHistoryFor.history ?? [] as r, i (`${r.ts}-${i}`)}
        <div class="trun">
          <div class="trun-head"><b>{fmt(r.ts)}</b><span class="trun-badge {r.deferred ? 'defer' : r.ok ? 'ok' : 'err'}">{#if r.deferred}<svg width="10" height="10" viewBox="0 0 16 16" fill="none" style="vertical-align:-1px"><circle cx="8" cy="8" r="5.6" stroke="currentColor" stroke-width="1.4" /><path d="M8 5.2v3l2 1.3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg> 顺延{:else}{r.ok ? '成功' : '失败'}{/if}</span>{#if r.summary}<span class="trun-sum" title={r.summary}>{r.summary}</span>{/if}</div>
          {#if r.sites?.length}
            <div class="trun-sites">
              {#each r.sites as s (s.slug)}
                {#if s.ok && s.conv_id}
                  <button class="trun-site" title="打开这次运行的对话" onclick={() => { taskHistoryFor = null; openConv(s.conv_id ?? ''); }}><SiteFav src={siteFav(s.slug)} label={s.slug} size={12} /><span>{s.slug}</span></button>
                {:else}
                  <span class="trun-site {s.deferred ? 'is-defer' : 'is-err'}" title={s.error || (s.deferred ? '限额顺延' : '失败')}><SiteFav src={siteFav(s.slug)} label={s.slug} size={12} /><span>{s.slug} {s.deferred ? '顺延' : '✕'}</span></span>
                {/if}
              {/each}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  </div>
{/if}

<!-- 托管预检软警示块（选站后机械预检：存量/GSC/密钥权限；只提醒不拦截，三步共用） -->
{#snippet mwPrecheckBlock()}
  {#if mwWarns.length}
    <div class="md-precheck">
      <b><svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M8 1.8 13 3.6v4.1c0 3.2-2.1 5.4-5 6.5-2.9-1.1-5-3.3-5-6.5V3.6L8 1.8Z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" /><path d="M8 5.4v3.2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" /><circle cx="8" cy="10.8" r="0.65" fill="currentColor" /></svg>开启前请注意（预检提醒，不拦截）</b>
      <ul>{#each mwWarns as w, i (i)}<li>{w}</li>{/each}</ul>
    </div>
  {/if}
{/snippet}

<!-- 托管 · 开启向导（3 步：选站 → 90 天计划 → 上限与边界确认） -->
{#if mwOpen}
  <div class="mask" role="presentation" onclick={() => !mwBusy && !mwGenBusy && (mwOpen = false)}></div>
  <div class="modal wide">
    <header class="sheet-head"><b>托管一个站点<small class="dim"> · 第 {mwStep}/3 步</small></b><button class="x" onclick={() => (mwOpen = false)} disabled={mwBusy}>×</button></header>
    <div class="sheet-body">
      {#if mwStep === 1}
        <div class="tfield"><span>选择站点（一个站一条托管）</span>
          <Dropdown searchable bind:value={mwSite} options={mwSiteOpts} placeholder="选择站点" />
        </div>
        <p class="hint">开启后会替这个站自动创建三个定时任务：每日内容（按计划写稿存草稿）+ 每周审计（自查质量/查重/内链）+ 每周周报（汇总本周数据）。模型与强度在第 3 步选择。</p>
        {@render mwPrecheckBlock()}
        <div class="row-end">{#if mwWarns.length}<span class="md-pre-ack">已知悉风险，仍可继续</span>{/if}<button class="btn ghost" onclick={() => (mwOpen = false)}>取消</button><button class="btn primary" onclick={() => (mwStep = 2)} disabled={!mwSite}>下一步</button></div>
      {:else if mwStep === 2}
        <div class="tfield"><span>90 天运营计划（每日任务的方向依据，可编辑，也可跳过手写）</span>
          <textarea class="tin" rows="10" bind:value={mwPlan} placeholder="点下面「生成 90 天计划」让 AI 摸底站点后起草，或直接手写：定位/内容支柱/每周节奏/关键词方向/前 4 周选题…"></textarea>
        </div>
        <div class="md-genrow">
          <button class="btn soft" onclick={mwGenPlan} disabled={mwGenBusy || !mwSite}>{#if mwGenBusy}<span class="wr-spin"></span>生成中（AI 正在摸底站点，约 1-3 分钟）…{:else}生成 90 天计划{/if}</button>
          <span class="hint">生成过程只读取站点数据，不会改动内容；对话会留在侧栏可追溯。</span>
        </div>
        {@render mwPrecheckBlock()}
        <div class="row-end">{#if mwWarns.length}<span class="md-pre-ack">已知悉风险，仍可继续</span>{/if}<button class="btn ghost" onclick={() => (mwStep = 1)}>上一步</button><button class="btn primary" onclick={() => (mwStep = 3)}>下一步{mwPlan.trim() ? '' : '（暂不填计划）'}</button></div>
      {:else}
        <div class="tfield"><span>模型与强度（三个配套任务共用，与对话选择器一致）</span>
          <div class="tfield-fx"><ModelFx options={comboOpts} value={`${mwBrain}::${mwModel}`} effort={mwEffort} onpick={(v: string) => { const i = v.indexOf('::'); if (i > 0) { mwBrain = v.slice(0, i); mwModel = v.slice(i + 2); } }} oneffort={(v: string) => (mwEffort = v)} /></div></div>
        <div class="trow">
          <div class="tfield"><span>托管等级</span><Dropdown bind:value={mwLevel} options={LEVEL_OPTS} /></div>
          <div class="tfield"><span>每周 token 预算（0＝不限，触顶自动熔断）</span>
            <input class="tin" type="number" min="0" step="50000" bind:value={mwBudget} /></div>
        </div>
        <div class="trow">
          <div class="tfield"><span>每周产出上限（发布＋新建草稿）</span>
            <input class="tin" type="number" min="1" max="50" bind:value={mwLimit} style="max-width:120px" />
            {#if Number(mwLimit) > 7}<span class="hint">新站爬坡期：当前上限 7 篇/周，防止批量灌站被搜索引擎判责——超出部分会被自动钳到 7（开启满 30 天放宽到 14，60 天后 50）。</span>{/if}
          </div>
          {#if mwLevel === 'l3'}
            <div class="tfield"><span>每周存量修改上限（L3 · Pilot 实测把关）</span>
              <input class="tin" type="number" min="1" max="20" bind:value={mwEditLimit} style="max-width:120px" />
            </div>
          {/if}
        </div>
        {#if mwLevel === 'l3'}
          <div class="md-danger">
            <b>⚠️ L3 高风险提示</b>
            <ul>
              <li>修改存量内容<b>可能损害既有搜索流量</b>，排名波动需自行承担。</li>
              <li>需要服务端 <b>≥ v1.3.23</b>（含内容修订历史，可在后台一键回滚）且密钥含 <b>stats:read</b>——否则 AI 拿不到 GSC/GA 数据、只能凭感觉改，<b>强烈不建议开启</b>（prompt 已写死：无数据禁改存量）。</li>
              <li>建议先在 L1/L2 稳定运行满 <b>2 周</b>、打回率低后再升 L3。</li>
            </ul>
          </div>
        {/if}
        <div class="md-bound">
          <b>边界说明（已机制化写入任务，请确认）</b>
          <ul>
            {#if mwLevel === 'l0'}
              <li>AI 产出<b>只到草稿</b>，等你在「托管 → 待审队列」里预览后批准；<b>绝不自行发布或定时发布</b>。</li>
            {:else}
              <li>{levelLabel(mwLevel)}：常规文章自检通过后<b>可直接发布</b>；审计纪要等仍存草稿待审{mwLevel === 'l2' ? '，且每周周报会附本周自动发布清单供你抽查' : ''}。打回率过高会<b>自动降级</b>{mwLevel === 'l3' ? '（L3→L2）' : '（→L0）'}。</li>
            {/if}
            {#if mwLevel === 'l3'}
              <li class="md-li-danger">L3 允许 AI <b>修改已发布的存量内容</b>并把低质旧文<b>转草稿下线</b>（绝不删除）；每周最多改 {mwEditLimit} 篇——Pilot 在任务触发前实测计数，配额用完当天禁改存量。</li>
            {/if}
            <li>每周产出（发布＋新建草稿）不超过 {mwLimit} 篇——Pilot 在任务触发前实测把关，达上限直接跳过。</li>
            <li><b>绝不</b>删除内容、修改导航/站点资料/语言设置、创建或启用内容类型。</li>
            {#if mwBudget > 0}<li>每周 token 预算 {mwBudget}：触顶自动暂停全部配套任务（熔断），恢复需手动。</li>{/if}
            <li>建议密钥勾选 <b>stats:read</b>——AI 将按真实搜索数据选题（只读；缺失时自动退回按运营计划选题）。</li>
            <li>建议为该连接使用<b>仅内容权限</b>的受限密钥（posts 读写＋发布即可，不给站点设置/导航/类型权限），从密钥层再兜一道底。</li>
          </ul>
        </div>
        {@render mwPrecheckBlock()}
        <div class="row-end">{#if mwWarns.length}<span class="md-pre-ack">已知悉风险，仍可继续</span>{/if}<button class="btn ghost" onclick={() => (mwStep = 2)}>上一步</button><button class="btn primary" onclick={mwEnable} disabled={mwBusy || !mwSite || !brainUsable(mwBrain as Brain)}>{mwBusy ? '开启中…' : '确认并开启托管'}</button></div>
      {/if}
    </div>
  </div>
{/if}

<!-- 托管 · 周报归档：历史列表（新到旧，周次+核心数字），点条目看渲染后的正文 -->
{#if reportListFor}
  <div class="mask" role="presentation" onclick={() => (reportListFor = null)}></div>
  <div class="modal wide">
    <header class="sheet-head"><b>周报归档 · {reportListFor.site_name}<small class="dim"> · {reportListFor.reports.length} 份</small></b><button class="x" onclick={() => (reportListFor = null)}>×</button></header>
    <div class="sheet-body">
      {#each reportListFor.reports as r, i (r.ts)}
        <button class="rp-item" onclick={() => (reportView = r)}>
          <span class="rp-date">{fmt(r.ts)}{#if i === 0}<span class="md-badge">最新</span>{/if}</span>
          <span class="rp-mets">{reportMetLine(r.metrics) || '（无指标块）'}</span>
        </button>
      {/each}
    </div>
  </div>
{/if}
{#if reportView}
  <div class="mask" role="presentation" onclick={() => (reportView = null)}></div>
  <div class="modal wide">
    <header class="sheet-head"><b>周报 · {fmt(reportView.ts)}</b><button class="x" onclick={() => (reportView = null)}>×</button></header>
    <div class="sheet-body">
      {#if reportMetLine(reportView.metrics)}<p class="hint">{reportMetLine(reportView.metrics)}</p>{/if}
      <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
      <div class="text md rp-body" onclick={mdClick} onauxclick={mdClick}>{@html mdRender(reportView.content)}</div>
    </div>
  </div>
{/if}

<!-- 托管 · 打回理由 -->
{#if rejectFor}
  <div class="mask" role="presentation" onclick={() => (rejectFor = null)}></div>
  <div class="modal">
    <header class="sheet-head"><b>打回《{rejectFor.d.title}》</b><button class="x" onclick={() => (rejectFor = null)}>×</button></header>
    <div class="sheet-body">
      <p class="hint">草稿不会被删除；理由会注入后续每日任务，让它规避同类问题（保留最近 20 条）。打回率过高会自动降级（L3→L2，L1/L2→L0）。</p>
      <textarea class="tin" rows="3" bind:value={rejectReason} placeholder="例如：标题太夸张 / 事实来源不足 / 和上周主题重复…"></textarea>
      <div class="row-end">
        <button class="btn ghost" onclick={() => (rejectFor = null)}>取消</button>
        <button class="btn" title="记录理由后立即触发一次每日任务，让 AI 带着意见返工" onclick={() => submitReject(true)} disabled={!rejectReason.trim()}>打回并立即返工</button>
        <button class="btn primary" onclick={() => submitReject(false)} disabled={!rejectReason.trim()}>记录打回</button>
      </div>
    </div>
  </div>
{/if}

<style>
  /* 远程工作台：终端主区 + 底部对话 + 右侧文件（VS Code 那套）。
     每层都 min-*:0 —— flex 子项默认 min-size:auto，不清零的话终端撑着不缩、面板挤不出来。 */
  .wb { flex: 1; min-height: 0; display: flex; }
  .wb-main { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; }
  /* 文件栏单开：主区收起（终端只藏不拆，DOM 还在），让文件栏独占整宽。 */
  .wb-main.collapsed { display: none; }
  /* 终端与对话的接缝不再用 border-top 那条黑白硬边，改由上面 .term-wrap.with-chat 的圆底+柔和阴影收口 */
  .wb-chat { flex: none; min-height: 0; display: flex; flex-direction: column; background: var(--bg); }
  .wb-chat.solo { flex: 1; }
  /* 起点输入框：贴着面板底（justify-content:flex-end），面板矮时也不顶头。
     ★ .composer 必须显式 width:100% —— 它自带 `margin:0 auto`，而这里是 flex 列容器，
     交叉轴上的 auto margin 会**压制 stretch**，不给宽度它就缩成 textarea 的默认宽（~192px）。
     对话页那边的 .composer-wrap 是普通块容器，没这问题。 */
  .wb-start { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; justify-content: flex-end; gap: 6px; }
  .wb-start .composer { width: 100%; }
  .wb-start .hint { width: 100%; max-width: 760px; margin: 0 auto; }
  /* 分隔线：命中区比看到的粗，好抓 */
  .wb-split { flex: none; background: transparent; z-index: 2; }
  .wb-split.h { height: 5px; margin-bottom: -5px; cursor: row-resize; }
  .wb-split.v { width: 5px; margin-right: -5px; cursor: col-resize; }
  .wb-split:hover { background: var(--accent-soft); }
  /* 面板开关（头部两枚 VS Code 式图标）：不给底，开关状态只用图标本身的深浅表示 */
  .wb-tg { display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; border: 0; background: transparent; color: var(--faint); cursor: pointer; -webkit-app-region: no-drag; }
  .wb-tg:hover { color: var(--dim); }
  .wb-tg.on { color: var(--text); }
  .wb-tg.on:hover { color: var(--accent-h); }
  /* 工作台头部：单行，比常规 thread-head 矮一截。
     ★ margin-top:0 是必须的 —— `.th-info small` 那 2px 是给它上面那行标题留的空隙，
     而这里标题已经去掉了，留着就是净偏移，文字会比右边的图标低 2px（对不上一条水平线）。 */
  /* 高度对齐左上角那排工具图标：`.win-tools` 是 fixed/top:0/height:30px，中心在 15px。
     这里最高的子元素是 24px 的面板开关 → 上下各留 3px 正好 30px，中心也落在 15px，
     于是「地址行 / 刷新键 / 三枚开关 / 折叠侧栏 / 搜索」全在同一条水平线上。 */
  /* 这条**必须自己写回 align-items:center**：上面 .thread-head 改成了 flex-start（为了让标题行
     对齐 15px 的图标带），而 slim 是靠 center 把「地址行 / 刷新键 / 三枚开关」这些不等高的子元素
     一起压到 15px 那条线上的 —— 被 flex-start 带走就全顶到 3px 去了。 */
  .thread-head.slim { padding-top: 3px; padding-bottom: 3px; align-items: center; container-type: inline-size; }
  /* 选择器要带上 .th-info small，否则压不过 `.th-info small` 那条（它带元素选择器，分更高）——
     实测：只写 .rhead-line 的话 margin-top 仍是 2px，文字比图标低 1px。 */
  .th-info small.rhead-line { display: flex; align-items: center; gap: 5px; margin-top: 0; }
  /* 终端：自己管滚动，容器不能给 overflow-y:auto（会和 xterm 打架）。 */
  .term-wrap { flex: 1; min-height: 0; overflow: hidden; background: #1c1917; padding: 8px 10px; }
  /* 关掉命令行＝只藏不拆：DOM 留着，xterm 和 SSH 会话都不掉，开回来立刻还在。 */
  .term-wrap.hid { display: none; }
  /* 「正在连接」浮层：盖在终端上，连上即消失，不进终端缓冲（所以不会留下擦不掉的一行）。 */
  .term-wrap { position: relative; }
  /* 终端**在对话框上方**时（with-chat）把接缝做软：一道朝下的柔和阴影落在对话面板上，
     替掉原来黑白直接相撞的硬边（不收圆角——直角保持齐整）。阴影几乎只朝下（负 spread 收掉左右溢出，
     正 offset 让顶边不糊）。z-index 让阴影压在对话面板之上（对话是后面的兄弟，默认会盖住它）。
     终端独占/无对话时不加，保持齐边。 */
  .term-wrap.with-chat { z-index: 1; box-shadow: 0 6px 12px -6px rgba(20, 16, 13, .4); }
  .term-connecting { position: absolute; left: 14px; top: 12px; display: flex; align-items: center; gap: 7px; color: #8b857c; font-size: 12px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; pointer-events: none; }
  .tc-spin { width: 9px; height: 9px; border: 1.5px solid #4b4640; border-top-color: #8b857c; border-radius: 50%; animation: spin .7s linear infinite; }
  .term { width: 100%; height: 100%; }
  /* xterm 6 的滚动条是**自绘**的（.xterm-scrollable-element > .scrollbar，从 VS Code 移植），
     宽度和颜色都只能从 Terminal 选项走 —— 见 startTerm 的 overviewRuler.width / theme.scrollbarSlider*。
     ::-webkit-scrollbar 那套对它完全无效（xterm 5 的 viewport 原生滚动条才吃那一套，别再写回来了）。
     这里只需要把 viewport 的底色抹成终端底色：xterm.css 给它钉了 background:#000
     （原话「On OS X this is required in order for the scroll bar to appear fully opaque」），
     不盖掉的话滚动条那一条会是黑的、和终端底色差一截。
     多带一级 .xterm 是故意的：与 xterm.css 的 `.xterm .xterm-viewport` 同分时靠后取胜，
     多一级才稳赢，不必动用 !important。 */
  :global(.term-wrap .xterm .xterm-viewport) { background-color: #1c1917; }
  /* 装饰栏：只是设 overviewRuler.width 改滚动条宽度时被顺带打开的（那个选项身兼两职）。
     它和滚动条**完全重叠**（实测两者都在同一 7px 上），却会在左沿画一条贯穿全高的 1px 竖线。
     我们从不用 decoration → 整个关掉。滚动条宽度来自 JS 选项，不受这条影响。 */
  :global(.term-wrap .xterm-decoration-overview-ruler) { display: none; }
  /* 远程视图头部：终端/文件 页签 */
  .rhead-acts { display: flex; align-items: center; gap: 10px; -webkit-app-region: no-drag; }
  .rseg { display: flex; background: var(--accent-soft); border-radius: 8px; padding: 2px; }
  .rseg-btn { border: 0; background: transparent; color: var(--dim); font-size: 12.5px; padding: 4px 14px; border-radius: 6px; cursor: pointer; }
  .rseg-btn.on { background: var(--bg); color: var(--text); box-shadow: 0 1px 2px rgba(0, 0, 0, .08); }
  /* 远程文件面板（工作台右栏；宽度由 wb.filesW 内联给） */
  .files-wrap { flex: none; min-height: 0; display: flex; flex-direction: column; border-left: 1px solid var(--border); background: var(--bg); }
  /* 单开时吃满整宽（撤掉固定宽 flex:none）；左边没东西了，左边框也去掉。 */
  .files-wrap.solo { flex: 1; min-width: 0; border-left: 0; }
  .files-bar { flex: none; display: flex; align-items: center; gap: 6px; padding: 10px 16px; border-bottom: 1px solid var(--border); }
  .fpath { flex: 1; min-width: 0; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
  /* 面包屑：不画框（它是一行路径，不是输入框——要输入点空白处会切成真的 input），
     撑满路径栏，路径深了横向滚动 */
  .fcrumbs { flex: 1; min-width: 0; display: flex; align-items: center; gap: 1px; overflow-x: auto; overflow-y: hidden; scrollbar-width: none; cursor: text; padding: 4px 2px; }
  .fcrumbs::-webkit-scrollbar { display: none; }
  .fcrumb { flex: none; border: 0; background: transparent; color: var(--dim); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; padding: 2px 5px; border-radius: 5px; cursor: pointer; white-space: nowrap; }
  .fcrumb:hover:not(:disabled) { background: var(--accent-soft); color: var(--text); }
  .fcrumb.cur { color: var(--text); font-weight: 500; }
  .fcrumb-sep { flex: none; color: var(--faint); font-size: 11px; user-select: none; }
  /* 面包屑尾巴的空白＝一个撑满剩余宽度的按钮（点它进输入模式）。min-width 保证路径很长时也还点得着。 */
  .fcrumb-blank { flex: 1; min-width: 28px; align-self: stretch; border: 0; background: transparent; cursor: text; border-radius: 5px; }
  .fcrumb-blank:focus-visible { outline: 2px solid var(--accent-soft); outline-offset: -2px; }
  .fbtn { display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; flex: none; border: 1px solid var(--border); background: var(--bg); color: var(--dim); border-radius: 6px; cursor: pointer; }
  .fbtn :global(svg) { width: 13px; height: 13px; }
  .fbtn:hover:not(:disabled) { color: var(--text); border-color: var(--border2); }
  .fbtn:disabled { opacity: .45; cursor: default; }
  .fbtn.sm { width: 22px; height: 22px; border: 0; border-radius: 5px; }
  .fbtn.sm:hover:not(:disabled) { background: var(--accent-soft); }
  .fbtn.danger:hover:not(:disabled) { color: var(--err); background: var(--err-soft); }
  .files-note { flex: none; padding: 6px 16px; font-size: 12px; color: var(--dim); border-bottom: 1px solid var(--border); }
  /* 现在是个重试按钮：清掉按钮默认样式，只留 err-note 的红底红字 + 左对齐 + 手型 */
  .files-err { flex: none; margin: 10px 16px 0; display: block; width: calc(100% - 32px); text-align: left; font: inherit; font-size: 13px; cursor: pointer; }
  .files-err:hover { background: var(--err-border); }
  /* 列表＝四列表格（名称/权限/大小/日期），行内缩进出树形。列宽用同一套变量对齐表头与行。
     ★ 窄面板里按容器宽度砍列：右栏默认才 380px，四列定宽合计就 300px，名称会被挤成 0
     （文件名全没了，只剩权限/大小/日期——那就本末倒置了）。名称永远优先。
     用容器查询而不是媒体查询：这里要看的是**面板自己的宽度**，不是窗口宽度（面板还能拖）。 */
  .files-wrap { --fw-perm: 96px; --fw-size: 76px; --fw-date: 128px; container-type: inline-size; }
  .fhead { flex: none; display: flex; align-items: stretch; gap: 0; padding: 0 14px; border-bottom: 1px solid var(--border); background: var(--rail); }
  .fh { position: relative; display: flex; align-items: center; gap: 3px; border: 0; background: transparent; color: var(--dim); font-size: 11.5px; padding: 5px 6px; text-align: left; }
  .fhead button.fh { cursor: pointer; }
  .fhead button.fh:hover { color: var(--text); }
  .fh + .fh { border-left: 1px solid var(--border); }
  /* 列宽手柄：骑在列头分隔线上，比线宽一点好抓 */
  .fh-grip { position: absolute; left: -4px; top: 0; bottom: 0; width: 8px; padding: 0; border: 0; background: transparent; cursor: col-resize; z-index: 1; }
  .fh-grip:hover { background: linear-gradient(to right, transparent 2px, var(--border2) 2px, var(--border2) 6px, transparent 6px); }
  .fh-grip:focus-visible { outline: 2px solid var(--accent-soft); outline-offset: -2px; }
  .fh-name { flex: 1; min-width: 0; }
  .fh-perm { width: var(--fw-perm); flex: none; }
  .fh-size { width: var(--fw-size); flex: none; justify-content: flex-end; }
  .fh-date { width: var(--fw-date); flex: none; }
  .fh-ar { font-size: 10px; line-height: 1; }
  .fh-ar.desc { transform: rotate(180deg); }
  .files-list { flex: 1; min-height: 0; overflow-y: auto; padding: 2px 6px 14px; }
  .files-empty { padding: 28px 0; text-align: center; color: var(--faint); font-size: 13px; }
  .frow { display: flex; align-items: center; padding: 0 8px; border-radius: 6px; cursor: default; user-select: none; }
  .frow:hover { background: var(--rail); }
  .frow.on { background: var(--accent-soft); }
  .fname { flex: 1; min-width: 0; display: flex; align-items: center; gap: 6px; color: var(--text); font-size: 13px; padding: 5px 0; }
  .fname-t { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fchev, .fchev-sp { width: 14px; height: 14px; flex: none; }
  .fchev { display: inline-flex; align-items: center; justify-content: center; border: 0; background: transparent; color: var(--faint); border-radius: 4px; cursor: pointer; padding: 0; transition: transform .12s; }
  .fchev:hover { color: var(--text); }
  .fchev.open { transform: rotate(90deg); }
  .fspin { width: 8px; height: 8px; border: 1.4px solid var(--border2); border-top-color: var(--dim); border-radius: 50%; animation: spin .7s linear infinite; }
  .fic { flex: none; color: var(--faint); }
  .fic.dir { color: #4a90d9; }
  .fic.link { color: #d98a2b; }
  .fperm { width: var(--fw-perm); flex: none; padding: 0 6px; color: var(--dim); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; }
  .fsize { width: var(--fw-size); flex: none; padding: 0 6px; text-align: right; color: var(--dim); font-size: 11.5px; font-variant-numeric: tabular-nums; }
  .fdate { width: var(--fw-date); flex: none; padding: 0 6px; color: var(--dim); font-size: 11.5px; font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

  /* ★ 砍列规则必须放在列样式**之后**：@container 不加优先级，和 `.fh{display:flex}` 同分，
     写在前面会被它盖掉（实测过：380px 下权限列照样 96px）。 */
  @container (max-width: 560px) { .fh-perm, .fperm { display: none; } }
  @container (max-width: 400px) { .fh-date, .fdate { display: none; } }
  @container (max-width: 300px) { .fh-size, .fsize { display: none; } }
  /* 远程文件右键菜单：复用全局 .ctx-menu/.ctx-item 的样式，只补两处差异 */
  .fctx { min-width: 172px; }
  .fctx .ctx-item { justify-content: flex-start; }
  .ctx-item.danger { color: var(--err); }
  .ctx-item.danger:hover:not(:disabled) { background: var(--err-soft); }
  /* 在线编辑器 */
  .ed-modal { width: min(760px, 94vw); }
  .ed-path { margin-left: 10px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; overflow-wrap: anywhere; }
  .ed-body { display: flex; flex-direction: column; gap: 10px; }
  .ed-ta { width: 100%; box-sizing: border-box; height: min(52vh, 480px); resize: vertical; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12.5px; line-height: 1.55; color: var(--text); background: var(--rail); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; outline: none; white-space: pre; overflow-wrap: normal; overflow-x: auto; }
  .ed-ta:focus { border-color: var(--border2); background: var(--bg); }
  /* 远端系统信息（页脚可点＝重新检测；下拉/设置里只读） */
  .foot-os, .cs-os { display: inline-flex; align-items: center; gap: 4px; min-width: 0; }
  .foot-os { cursor: pointer; border-radius: 5px; padding: 0 3px; margin-left: -3px; }
  .foot-os:hover { background: var(--accent-soft); color: var(--text); }
  .cs-os { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* ★ 上移 1px 不是玄学：align-items:center 对齐的是**盒子**，而文字行盒底下有一截没人用的
     降部空间（Ubuntu/22.04 这类没有 g/p/y），于是「看得见的字」整体偏上、图标按盒居中就显得低。
     发行版图标是纯圆/纯环，没有基线可言，只能按视觉中线手动补这 1px。 */
  :global(svg.distro) { flex: none; transform: translateY(-1px); }
  /* 头部的「重新连接」：只留图标，紧跟在连接状态后面 */
  .th-rfz { display: inline-flex; align-items: center; justify-content: center; width: 15px; height: 15px; border: 0; background: transparent; color: var(--faint); cursor: pointer; padding: 0; -webkit-app-region: no-drag; }
  .th-rfz :global(svg) { width: 12px; height: 12px; }
  .th-rfz:disabled { cursor: default; color: var(--dim); }
  /* 顶栏的远端负载：三个小读数。
     ★ 做得很紧（不画迷你条）是有原因的：应用最小窗口 820px、侧栏 240 → 头部只剩 532px，
     地址+刷新(215) + 三枚开关(84) + 间距(20) 已占 319，留给负载的只有 213px。
     带条的版本要 290px —— 于是在最小窗口下永远塞不下。 */
  /* 分隔线用伪元素画：直接给容器 border-left 会顶满整行高，太抢眼；这里只画 11px 的一小截。 */
  .hstats { position: relative; display: flex; align-items: center; gap: 9px; margin-left: 5px; padding-left: 10px; }
  .hstats::before { content: ''; position: absolute; left: 0; top: 50%; transform: translateY(-50%); width: 1px; height: 11px; background: var(--border2); }
  .hstat { display: inline-flex; align-items: center; gap: 3px; font-size: 11px; color: var(--dim); white-space: nowrap; cursor: default; }
  .hstat-l { color: var(--faint); }
  .hstat-v { font-variant-numeric: tabular-nums; }
  .hstat.warn { color: var(--warn); font-weight: 500; }
  .hstat.hot { color: var(--err); font-weight: 500; }
  /* 真挤不下了才收起（比如侧栏被拖到很宽）。看的是**头部自己的宽度**，不是窗口宽 ——
     侧栏可拖 190~460，窗口宽说明不了头部还剩多少。
     ★ 必须写在 .hstats 之后：@container 不加优先级，同分靠后取胜（上次在文件列表踩过）。 */
  @container (max-width: 470px) { .hstats { display: none; } }
  .th-rfz:hover { color: var(--text); }
  /* 启动页：远程连接的目标机器（占站点选择器的位子——那台机器就是本次对话的对象） */
  .ssh-target { display: inline-flex; align-items: center; gap: 6px; padding: 4px 10px; border: 1px solid var(--border); border-radius: 999px; background: var(--rail); color: var(--dim); font-size: 12px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .ssh-target :global(svg) { color: var(--faint); flex: none; }
  .ssh-host { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .92em; background: var(--accent-soft); border-radius: 5px; padding: 1px 5px; }
  .ssh-key-row { display: flex; gap: 6px; align-items: center; }
  .ssh-key-row .tin { flex: 1; min-width: 0; }
  .ssh-fp { display: flex; flex-direction: column; gap: 5px; padding: 9px 11px; border: 1px solid #cfc9ec; background: #f7f6ff; border-radius: 10px; }
  .ssh-fp b { font-size: 12.5px; }
  .ssh-fp > code { font-family: ui-monospace, monospace; font-size: 11.5px; color: var(--text); background: #fff; border: 1px solid var(--border); border-radius: 6px; padding: 4px 7px; overflow-wrap: anywhere; user-select: text; }
  .ssh-fp small { color: var(--dim); font-size: 11px; line-height: 1.5; }
  .ssh-fp small code { font-family: ui-monospace, monospace; font-size: 10.5px; background: #fff; border-radius: 4px; padding: 1px 4px; user-select: text; }
  :global(:root) {
    --bg: #ffffff; --rail: #faf9f7; --card: #ffffff;
    /* 浮层实底（更新吐司/附件 chip 等用）：此前未定义导致 var(--panel) 落空、浮层透明。 */
    --panel: #ffffff;
    --border: #ecebe6; --border2: #e1dfd8;
    --text: #26241f; --dim: #6f6b62; --faint: #a29d93;
    /* 强调色走暖黑/灰（Codex 式安静路线），不用蓝紫；风险色（红/琥珀）另有专用变量。 */
    --accent: #33302a; --accent-h: #1d1b17; --accent-soft: #edece7;
    --user-bg: #f3f1ec;
    --ok: #12805c; --warn: #b45309; --err: #dc2626;
    --err-soft: #fef2f2; --err-border: #f4cccc;
    --shadow-sm: 0 1px 2px rgba(30,25,15,.05);
    --shadow: 0 4px 16px rgba(30,25,15,.08);
    --shadow-lg: 0 16px 48px rgba(30,25,15,.16);
    /* 单行表单控件统一高度 token：.tin(单行)/.dd-trigger/.tfield-fx 壳全部吃它，别再靠 padding 推算。 */
    --ctl-h: 30px;
    /* composer 底栏 chip 统一高度 token：站点/权限（Dropdown compact）、模型（ModelFx 触发器）、
       用量圆环按钮全部定高——chip 内容各异（17px favicon/「低」徽章/纯文字），靠 padding 撑高必然参差。 */
    --chip-h: 24px;
  }
  :global(html, body) { margin: 0; height: 100%; background: var(--bg); color: var(--text);
    font: 15px/1.65 -apple-system, 'PingFang SC', 'Segoe UI', 'Helvetica Neue', sans-serif; -webkit-font-smoothing: antialiased; }
  :global(*) { box-sizing: border-box; }
  /* 去掉原生按钮外观（避免 WKWebView 给 <button> 画默认边框/底纹，即那个「黑框」）。 */
  :global(button) { -webkit-appearance: none; appearance: none; font: inherit; }
  /* 去掉点击时 WKWebView 的黑色焦点框；键盘可达性用 hover/active 态足够（桌面鼠标为主）。 */
  :global(button:focus), :global(button:focus-visible), :global([role='button']:focus),
  :global([role='button']:focus-visible), :global(summary:focus), :global(a:focus) { outline: none; box-shadow: none; }
  .app { display: flex; height: 100vh; overflow: hidden; }
  /* Windows：主内容左上角轻微圆角，跟原生标题栏之间更柔和（角落露出 rail 色）。macOS 无原生标题栏不需要。
     侧栏分隔线在 Windows 去掉——通高的 border-right 会在圆角上方留一截两侧同色的「悬空线头」；
     边界靠 rail/main 底色对比 + 圆角呈现（Codex 客户端同款做法）。 */
  .app.win { background: var(--rail); }
  /* 圆角回归：当初去掉它是因为页头有暖底、和 DWM 窗框几乎同色，圆角单独露成一道台阶；
     暖底已拿掉（0.2.22），.main 的白又直接顶到窗沿——12px 圆角恢复「白面板嵌在 rail 色
     窗框里」的收边（用户实测：背景拿掉后这里正常了）。
     顶栏左缘那条竖线也随之取消：白/rail 色差已经把边界撑起来了，再画线反而多余。 */
  .app.win .main { background: var(--bg); border-top-left-radius: 12px; }
  .app.win .rail { border-right: none; }

  /* 细滚动条（macOS overlay 风格）：细、圆角、透明轨道，thumb 用 padding-box 内缩显得更细。 */
  :global(::-webkit-scrollbar) { width: 10px; height: 10px; }
  :global(::-webkit-scrollbar-track) { background: transparent; }
  :global(::-webkit-scrollbar-thumb) { background-color: rgba(60, 54, 44, .22); border-radius: 8px; border: 3px solid transparent; background-clip: padding-box; }
  :global(::-webkit-scrollbar-thumb:hover) { background-color: rgba(60, 54, 44, .4); border: 2px solid transparent; background-clip: padding-box; }
  :global(::-webkit-scrollbar-corner) { background: transparent; }

  /* 融合标题栏：全宽透明拖拽条，红绿灯浮在其上，两列各自的底色透出来 */
  /* 拖拽条只盖住左栏顶部（红绿灯+工具区）；主区域顶部改由各页头/启动页自身承担拖拽，
     这样内容能贴近顶部又不被拖拽条挡住按钮。 */
  .titlebar { position: fixed; top: 0; left: 0; height: 30px; z-index: 6; }
  /* 顶部工具按钮：浮在拖拽条之上、紧挨红绿灯右侧。 */
  .win-tools { position: fixed; top: 0; left: 80px; height: 30px; display: flex; align-items: center; gap: 1px; z-index: 8; }
  /* 全屏无红绿灯：左移到与左栏菜单图标同一左边界（rail-head 8px + 按钮 padding 让开）。 */
  .win-tools.fs { left: 12px; }
  /* Windows 无红绿灯：靠左边缘，不飘在中间。 */
  .win-tools.win { left: 12px; }
  .wt { display: inline-flex; align-items: center; justify-content: center; width: 27px; height: 24px; border: none; background: none; border-radius: 6px; color: var(--dim); cursor: pointer; -webkit-app-region: no-drag; }
  .wt:hover { background: rgba(0, 0, 0, .06); color: var(--text); }
  .wt:disabled { opacity: .4; cursor: default; }
  .wt:disabled:hover { background: none; color: var(--dim); }
  /* 待更新图标：锚在侧栏右上角（跟随 rail 右边界），静默检查发现新版时出现。 */
  /* 待更新指示：并入 win-tools，样式同折叠/搜索（无底、灰色 + 悬停高亮），仅右上角一个柔和强调色小点。 */
  .wt.upd { position: relative; }
  .wt.upd:disabled { opacity: .55; }
  .upd-dot { position: absolute; top: 3px; right: 4px; width: 5px; height: 5px; border-radius: 50%; background: var(--accent); border: 1.5px solid var(--rail); }
  .upd-spin { animation: upd-spin .7s linear infinite; transform-origin: center; }
  @keyframes upd-spin { to { transform: rotate(360deg); } }
  /* 更新进度小药丸：点更新后顶部居中显示 状态文字 + 进度条，直到重启。 */
  .upd-toast { position: fixed; top: 38px; left: 50%; transform: translateX(-50%); z-index: 200; display: flex; align-items: center; gap: 8px; padding: 5px 12px; background: var(--panel); border: 1px solid var(--border); border-radius: 999px; box-shadow: var(--shadow-lg); font-size: 12px; color: var(--text); -webkit-app-region: no-drag; }
  .upd-toast .upd-spin { color: var(--accent); flex: none; }
  .upd-toast-msg { white-space: nowrap; }
  /* 下载进度条：实底浅灰轨道 + 主题色填充（容器 --panel 已实底，文字对比度由 --text/白底保证） */
  .upd-toast-bar { width: 84px; height: 5px; border-radius: 3px; background: var(--border2); overflow: hidden; flex: none; }
  .upd-toast-bar > span { display: block; height: 100%; background: var(--accent); border-radius: 3px; transition: width .2s ease; }

  /* ---- 左栏 ---- */
  .rail { position: relative; width: 240px; flex: none; display: flex; flex-direction: column; background: var(--rail); border-right: 1px solid var(--border); padding-top: 30px; }
  .rail.collapsed { display: none; }
  /* 右缘拖拽把手：改侧栏宽度。 */
  /* 只用 col-resize 光标提示，不画任何指示线（避免拖动时多出一条竖线）。 */
  .rail-resize { position: absolute; top: 30px; right: -3px; bottom: 0; width: 7px; cursor: col-resize; z-index: 5; }
  .rail-head { padding: 8px 8px 2px; display: flex; flex-direction: column; gap: 2px; }
  .newchat { display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 10px;
    background: none; color: var(--text); border: none; border-radius: 9px; font-size: 13.5px; font-weight: 550; cursor: pointer; text-align: left; }
  .newchat:hover { background: #f1efe9; }
  .newchat:disabled { opacity: .5; cursor: default; }
  .newchat svg { flex: none; color: var(--accent); }
  .railnav { display: flex; align-items: center; gap: 8px; width: 100%; padding: 5px 10px; background: none;
    border: none; border-radius: 9px; font-size: 13px; color: var(--dim); cursor: pointer; text-align: left; margin-top: -4px; }
  .railnav:hover { background: #f1efe9; color: var(--text); }
  .railnav.on { background: #e9e7e0; color: var(--text); font-weight: 550; }
  .railnav:disabled { opacity: .45; cursor: default; }
  .railnav svg { flex: none; }

  .convos { flex: 1; overflow-y: auto; padding: 2px 8px 8px; display: flex; flex-direction: column; gap: 1px; border-top: 1px solid transparent; transition: border-color .15s; }
  .convos > button.site-grp:first-of-type { padding-top: 3px; margin-top: 0; }
  .convos.scrolled { border-top-color: rgba(30, 25, 15, 0.07); }
  .rail-empty { color: var(--faint); font-size: 12px; padding: 10px 8px; line-height: 1.7; }
  .grp { font-size: 10.5px; font-weight: 600; letter-spacing: .04em; color: var(--faint); padding: 12px 10px 4px; text-transform: uppercase; }
  .grp:first-child { padding-top: 4px; }
  /* 站点分组头（可折叠） */
  .site-grp { width: 100%; display: flex; align-items: center; gap: 6px; padding: 9px 8px 4px 10px; margin-top: 2px; background: none; border: none; cursor: pointer; text-align: left; -webkit-appearance: none; appearance: none; }
  .site-grp:first-child { margin-top: 0; }
  .site-grp:hover .site-grp-name { color: var(--accent); }
  .site-grp-chev { color: var(--faint); flex: none; transition: transform .12s; }
  .site-grp-chev.collapsed { transform: rotate(-90deg); }
  .site-grp-name { flex: 0 1 auto; max-width: 58%; min-width: 0; font-size: 12.5px; font-weight: 600; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .site-grp-host { flex: 0 1 auto; min-width: 0; font-size: 10.5px; color: var(--accent); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  /* 线程头右上角：站点域名 + ↗，点开站点 */
  .th-open { flex: none; display: inline-flex; align-items: center; gap: 4px; background: none; border: none; padding: 4px 6px; border-radius: 7px; color: var(--accent); font: inherit; font-size: 12.5px; cursor: pointer; }
  .th-open:hover { background: #f1efe9; }
  .th-open svg { flex: none; }
  /* 任务类型子组标签 */
  /* padding-left 19：让对话行的厂商图标中心对齐分组头里的站点图标中心
     （分组头：10 左内边距 + 10 chevron + 6 gap，14px 图标中心落在 33px；
      对话行：8 外边距 + 19 左内边距，12px 图标中心也落在 33px）。顺带标题也更贴近站点名。 */
  .convo { position: relative; display: flex; align-items: center; gap: 6px; border-radius: 8px; margin: 0 8px; padding: 1px 6px 1px 19px; cursor: pointer; }
  .convo:hover { background: #f1efe9; }
  .convo.on { background: #e9e7e0; }
  .convo-body { flex: 1; min-width: 0; display: flex; align-items: center; gap: 6px; }
  .convo-bi { flex: none; display: inline-flex; align-items: center; }
  /* 对话标题：比分组头（.site-grp-name 12.5px/var(--text)）小半号、淡一档——让站点头当主、对话当项 */
  .convo-title { flex: 1; min-width: 0; font-size: 12px; font-weight: 500; color: var(--dim); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .convo-when { flex: none; font-size: 11px; color: var(--dim); }
  .cmono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 10.5px; color: var(--faint); overflow: hidden; text-overflow: ellipsis; }
  .cdot { color: var(--faint); }
  .mini-run { width: 6px; height: 6px; border-radius: 50%; background: var(--accent); animation: pulse 1.1s infinite; flex: none; }
  .mini-permit { font-size: 10px; font-weight: 600; color: #b45309; background: #faf1d8; padding: 0 5px; border-radius: 4px; flex: none; }
  .convo-x { display: none; background: none; border: none; color: var(--faint); font-size: 16px; line-height: 1; padding: 1px 2px 1px 4px; border-radius: 5px; cursor: pointer; flex: none; }
  .convo:hover .convo-x, .convo:focus-within .convo-x { display: block; }
  .convo:hover .convo-when, .convo:focus-within .convo-when { display: none; }
  .convo-x:hover { color: var(--dim); }

  .rail-foot { width: 100%; display: flex; align-items: center; gap: 8px; padding: 8px 12px; border: none; border-top: 1px solid var(--border); background: none; cursor: pointer; text-align: left; -webkit-appearance: none; appearance: none; box-shadow: none; }
  .rail-foot:focus, .rail-foot:active { outline: none; box-shadow: none; }
  .rail-foot:hover { background: #f1efe9; }
  .rail-foot.open { background: #f1efe9; border-top-color: transparent; }
  .foot-dots { display: flex; gap: 3px; }
  .foot-main { flex: 1; min-width: 0; }
  .foot-main b { display: block; font-size: 12.5px; line-height: 1.15; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .foot-main small { display: block; color: var(--dim); font-size: 10.5px; line-height: 1.1; }
  /* 站点数后的小刷新：尺寸与文字一致（10.5px），点击不触发上层连接切换器 */
  .foot-rfz { display: inline-flex; align-items: center; margin-left: 5px; vertical-align: -1.5px; color: var(--faint); cursor: pointer; }
  .foot-rfz:hover { color: var(--accent); }
  .foot-rfz .rfz { width: 10.5px; height: 10.5px; }
  .foot-chev { color: var(--faint); flex: none; transition: transform .15s; }
  .foot-chev.up { transform: rotate(180deg); }

  .foot-wrap { position: relative; }
  .conn-switch {
    position: absolute; left: 8px; right: 8px; bottom: calc(100% + 4px); z-index: 40;
    background: #fff; border: 1px solid var(--border2); border-radius: 12px;
    box-shadow: 0 10px 30px rgba(30,25,15,.15); padding: 5px;
    animation: pop .1s ease-out;
  }
  .cs-item {
    width: 100%; display: flex; align-items: center; gap: 9px;
    background: none; border: none; border-radius: 8px; padding: 5.5px 9px; cursor: pointer; text-align: left; font: inherit;
    color: var(--text);
  }
  .cs-item:hover { background: #f4f3ef; }
  .cs-item.on { background: #efeee9; }
  .cs-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .cs-main b { font-weight: 500; font-size: 13px; line-height: 1.2; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  /* 连接副标题（gcmsp_… · 平台 / Ubuntu … LTS 之类）：更小更淡，让连接名更突出 */
  .cs-main small { color: var(--faint); font-size: 10px; line-height: 1.25; }
  .cs-check { color: var(--accent); flex: none; font-size: 13px; }
  .cs-div { height: 1px; background: var(--border); margin: 3.5px 4px; }
  /* 动作行是单行：比原来(7px)收，但别收到发挤——padding 5.5px + line-height 1.3 落在约 28px，
     比原来 37px 紧一截、又留了呼吸感（23px 太挤，实测反馈）。连接行是两行，另按 .cs-item 定高。 */
  .cs-act {
    width: 100%; display: flex; align-items: center; gap: 8px;
    background: none; border: none; border-radius: 8px; padding: 5.5px 9px; cursor: pointer; text-align: left;
    font: inherit; font-size: 12.5px; line-height: 1.3; color: var(--dim);
  }
  .cs-act:hover { background: #f4f3ef; color: var(--text); }
  .cs-act :global(svg) { color: var(--faint); flex: none; }
  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
  .dot.ok { background: #16a34a; } .dot.warn { background: #d97706; } .dot.off { background: #cfccc4; }

  /* ---- 主区 ---- */
  .main { flex: 1; position: relative; display: flex; flex-direction: column; min-width: 0; padding-top: 0; }
  /* 吐司：pre-wrap 让「——详情——」多行错误可读；err 形态限高可滚动（安装失败输出末尾等） */
  .flash { position: absolute; top: 40px; left: 50%; transform: translateX(-50%); z-index: 40; background: #14231a; color: #fff; padding: 9px 16px; border-radius: 10px; font-size: 13px; box-shadow: var(--shadow); max-width: 70%; white-space: pre-wrap; overflow-wrap: anywhere; }
  .flash.err { max-height: 42vh; overflow-y: auto; }
  /* 自定义右键菜单（替换 WKWebView 默认英文菜单） */
  .ctx-menu { position: fixed; z-index: 120; min-width: 148px; background: #fff; border: 1px solid var(--border); border-radius: 11px; box-shadow: 0 12px 32px rgba(30,25,15,.16); padding: 5px; animation: pop .1s ease-out; }
  .ctx-item { width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 18px; background: none; border: none; border-radius: 7px; padding: 6px 10px; font: inherit; font-size: 13px; color: var(--text); cursor: pointer; text-align: left; }
  .ctx-item:hover:not(:disabled) { background: #f1efe9; }
  .ctx-item:disabled { color: var(--faint); cursor: default; }
  .ctx-div { height: 1px; background: var(--border); margin: 4px 6px; }
  /* 全局 tips 浮层（fixed，不受滚动容器裁剪） */
  .tipbox { position: fixed; z-index: 130; transform: translate(-50%, -100%); background: #26241f; color: #fff; font-size: 11px; line-height: 1.45; padding: 5px 9px; border-radius: 7px; width: max-content; max-width: 280px; white-space: pre-line; pointer-events: none; text-align: left; box-shadow: 0 5px 16px rgba(0, 0, 0, .18); animation: tipin .12s ease-out .3s both; }
  .tipbox.below { transform: translate(-50%, 0); }
  .imgtip { position: fixed; z-index: 130; transform: translate(-50%, -100%); padding: 5px; border-radius: 10px; background: #fff; border: 1px solid rgba(0, 0, 0, .1); box-shadow: 0 8px 24px rgba(0, 0, 0, .18); pointer-events: none; visibility: hidden; }
  .imgtip.ready { visibility: visible; animation: tipin .12s ease-out both; }
  .imgtip.below { transform: translate(-50%, 0); }
  /* min-* + contain：极端长宽比（整页截图/超宽横幅）装进保底盒子里，不再缩成一条细缝 */
  .imgtip img { display: block; max-width: 320px; max-height: 240px; min-width: 140px; min-height: 90px; object-fit: contain; border-radius: 6px; background: repeating-conic-gradient(#ececea 0 25%, #fff 0 50%) 0 0 / 14px 14px; }
  .lightbox { position: fixed; inset: 0; z-index: 140; background: rgba(24, 22, 18, .72); display: flex; align-items: center; justify-content: center; cursor: zoom-out; }
  .lightbox img { max-width: 86vw; max-height: 86vh; border-radius: 12px; background: repeating-conic-gradient(#ececea 0 25%, #fff 0 50%) 0 0 / 16px 16px; box-shadow: 0 24px 64px rgba(0, 0, 0, .4); }
  @keyframes tipin { from { opacity: 0; } to { opacity: 1; } }
  .tipwrap { display: inline-flex; }
  .flash.err { background: var(--err); }

  /* safe center：内容比可视区高时退回顶对齐可滚动，避免居中把顶部裁掉。 */
  .center { flex: 1; display: flex; align-items: safe center; justify-content: center; overflow-y: auto; padding: 24px; }
  /* 启动页：上下居中、左右铺满主区域宽度。 */
  .center.launch-center { align-items: safe center; justify-content: flex-start; padding: 24px 40px; }
  .center.launch-center .launcher { width: 100%; }
  .hero-card { text-align: center; max-width: 460px; }
  .hero-mark { display: flex; justify-content: center; margin-bottom: 6px; }
  .hero-card h1 { font-size: 22px; margin: 12px 0 8px; }
  .hero-card p { color: var(--dim); margin: 0 0 18px; }
  .cli-guide { margin-top: 22px; text-align: left; border-top: 1px solid var(--border); padding-top: 14px; }
  .cli-guide-h { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 11px; letter-spacing: .03em; text-transform: uppercase; color: var(--faint); font-weight: 600; margin-bottom: 6px; }
  .cli-redetect { display: inline-flex; align-items: center; gap: 4px; flex: none; background: none; border: none; padding: 2px 6px; border-radius: 6px; cursor: pointer; color: var(--dim); font: inherit; font-size: 11px; font-weight: 500; letter-spacing: 0; text-transform: none; }
  .cli-redetect:hover { background: rgba(0, 0, 0, .06); color: var(--text); }
  .cli-redetect:disabled { opacity: .55; cursor: default; }
  .cli-redetect :global(svg) { width: 12px; height: 12px; }
  .cli-row { display: flex; align-items: center; gap: 8px; padding: 3px 0; font-size: 13px; }
  .cli-name { width: 100px; flex: none; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .cli-copy { flex: none; width: 22px; height: 22px; display: inline-flex; align-items: center; justify-content: center; border: none; background: none; border-radius: 5px; color: var(--faint); cursor: pointer; }
  .cli-copy:hover { background: rgba(0, 0, 0, .06); color: var(--text); }
  .cli-tag { flex: none; font-size: 11px; padding: 1px 7px; border-radius: 6px; font-weight: 600; }
  .cli-tag.ok { color: #1a7f4b; background: #e7f4ec; }
  .cli-tag.warn { color: #9a6a00; background: #f7efd9; }
  .cli-tag.bad { color: #9a3b2f; background: #f6e7e3; }
  .cli-cmd { flex: 0 1 auto; min-width: 0; font-family: ui-monospace, monospace; font-size: 10.5px; color: var(--faint); background: #f6f5f1; padding: 3px 8px; border-radius: 5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; user-select: text; cursor: text; }
  /* 提高优先级压过 .hero-card p（后者 margin/color 更 specific，会把 top margin 顶成 0）。 */
  .cli-guide .cli-note { display: flex; align-items: center; gap: 5px; font-size: 11.5px; line-height: 1.5; color: var(--faint); margin: 12px 0 0; }
  .cli-note-ic { flex: none; color: var(--faint); }

  .launcher { width: min(680px, 100%); }
  .launcher h1 { font-size: 26px; margin: 0 0 6px; letter-spacing: -.01em; }
  .launcher .sub { color: var(--dim); margin: 0 0 22px; }
  .task-seg { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; margin-bottom: 30px; }
  .task-seg button { text-align: left; background: var(--card); border: 1px solid var(--border2); border-radius: 12px; padding: 11px 13px; cursor: pointer; display: flex; align-items: center; gap: 10px; transition: border-color .12s, background .12s; }
  .task-seg button:hover { border-color: #cfccc2; }
  .task-seg button.on { border-color: var(--accent); }
  .ts-ic { flex: none; width: 30px; height: 30px; border-radius: 8px; display: inline-flex; align-items: center; justify-content: center; background: #edecef; color: var(--dim); transition: background .12s, color .12s; }
  .task-seg button.on .ts-ic { color: var(--accent); }
  .ts-txt { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .task-seg b { font-size: 13.5px; }
  .task-seg small { color: var(--dim); font-size: 11.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  /* 表单控件统一尺度（桌面紧凑密度）：13px 字 + 5px 10px 内边距 + 8px 圆角，单行高约 30px。
     composer 聊天输入框不吃这套（下方 composer 专属规则钉回 14px 大字）。 */
  .tin, textarea { font-family: inherit; font-size: 13px; color: var(--text); background: #fff; border: 1.5px solid var(--border2); border-radius: 8px; padding: 5px 10px; }
  /* 单行输入（text/password/number/datetime-local）：显式统一高 --ctl-h，内容垂直居中；
     去掉竖向 padding 防 macOS 原生步进器/日期控件把高度顶开。textarea 不定高，不吃这条。 */
  input.tin { height: var(--ctl-h); box-sizing: border-box; padding-top: 0; padding-bottom: 0; line-height: normal; }
  .tin:focus, textarea:focus { outline: none; border-color: #b7b2a6; box-shadow: none; }

  /* 会话搜索（命令面板式；.mask 提供遮罩，box 居中于顶部） */
  .search-box { position: fixed; z-index: 61; top: 12vh; left: 50%; transform: translateX(-50%); width: min(560px, 92vw); background: #fff; border: 1px solid var(--border); border-radius: 14px; box-shadow: 0 24px 60px rgba(20, 15, 8, .28); overflow: hidden; display: flex; flex-direction: column; max-height: 66vh; }
  .search-head { display: flex; align-items: center; gap: 10px; padding: 12px 15px; border-bottom: 1px solid var(--border); }
  .search-head .si-ic { color: var(--faint); flex: none; }
  .search-head input { flex: 1; min-width: 0; border: none; outline: none; background: none; font: inherit; font-size: 15px; color: var(--text); }
  .search-head kbd { font: inherit; font-size: 10.5px; color: var(--faint); border: 1px solid var(--border2); border-radius: 5px; padding: 1px 6px; background: var(--rail); flex: none; }
  .search-list { overflow-y: auto; padding: 6px; }
  .search-sec { padding: 8px 10px 3px; font-size: 11px; font-weight: 600; letter-spacing: .04em; color: var(--faint); }
  /* 排期标题过滤小标签（仅全局搜索「在排期中查找」跳转后出现；点 × 清空恢复全量） */
  .sched-tq-chip { display: inline-flex; align-items: center; gap: 4px; width: fit-content; margin-top: 6px; padding: 2px 5px 2px 9px; border-radius: 999px; background: #fdf6e3; color: #9a6b00; font-size: 11.5px; }
  .sched-tq-chip button { border: none; background: none; color: inherit; font-size: 13px; line-height: 1; padding: 0 3px; cursor: pointer; border-radius: 999px; }
  .sched-tq-chip button:hover { background: #f4e8c8; }
  .search-empty { text-align: center; color: var(--faint); font-size: 13px; padding: 26px 12px; margin: 0; }
  .search-item { width: 100%; display: flex; align-items: center; gap: 10px; background: none; border: none; border-radius: 9px; padding: 9px 10px; cursor: pointer; text-align: left; color: var(--text); }
  .search-item:hover, .search-item.on { background: #f4f3ef; }
  .si-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .si-main b { font-weight: 500; font-size: 13.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .si-main small { color: var(--dim); font-size: 11.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .si-tag { flex: none; font-size: 10px; color: var(--faint); border: 1px solid var(--border2); border-radius: 4px; padding: 0 4px; line-height: 15px; }

  /* 输入框（仿 Claude Code：整块圆角边框，聚焦时高亮，发送按钮嵌在框内） */
  .composer.big, .composer-wrap .composer { position: relative; background: #fff; border: 1px solid var(--border2); border-radius: 12px; box-shadow: none; transition: border-color .12s, box-shadow .12s; }
  .composer.big:focus-within, .composer-wrap .composer:focus-within { border-color: #b7b2a6; box-shadow: none; }
  .composer.big textarea, .composer-wrap textarea { width: 100%; resize: none; border: none; background: none; box-shadow: none; padding: 14px 52px 14px 17px; font-size: 14px; line-height: 1.6; max-height: 200px; display: block; }
  .composer.big textarea:focus, .composer-wrap textarea:focus { outline: none; box-shadow: none; border: none; }
  .composer .send { position: absolute; right: 9px; bottom: 9px; width: 32px; height: 32px; border-radius: 50%; border: none; background: var(--accent); color: #fff; font-size: 16px; cursor: pointer; display: flex; align-items: center; justify-content: center; transition: background .12s, transform .08s; }
  .composer .send:hover { background: var(--accent-h); }
  .composer .send:active { transform: scale(.92); }
  .composer .send:disabled { background: #dcdad2; cursor: default; transform: none; }
  .composer .send.stop { background: var(--text); }
  /* composer：竖排 textarea + 底栏（启动页站点在左；会话页只读厂商在左，模型/发送在右）。 */
  .composer.big, .composer-wrap .composer { display: flex; flex-direction: column; }
  .composer.big textarea, .composer-wrap textarea { padding: 12px 16px 6px 16px; }
  .composer-bar { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 4px 8px 8px; }
  .cb-left { display: flex; align-items: center; gap: 1px; flex: 1; min-width: 0; }
  .cb-right { display: flex; align-items: center; gap: 3px; flex: none; }
  .composer-bar .send { position: static; width: 30px; height: 30px; margin-left: 3px; font-size: 15px; }
  /* 会话页只读徽标（站点/厂商，不可改）。 */
  .cb-ro { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; color: var(--dim); font-size: 13px; opacity: .9; min-width: 0; }
  .cb-ro-t { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  /* ---- 线程 ---- */
  /* ★ 标题要和左上角那排工具图标在同一条水平线上。
     `.win-tools` 是 fixed / top:0 / height:30px + align-items:center → **图标中心恒在 15px**
     （和 .thread-head.slim 那条注释是同一个基准）。标题行高 20.3 → 上内边距取 5px，
     标题中心落在 15.15，对上。原来 13px 时中心在 23.1 —— **每个页面都低 8px**。
     ★ 上边距被「对齐那条 fixed 图标带」锁死＝5px，所以**下边距只能跟着收成 5px**，
     不然上下就不对称（原来 5/13，下面明显空一截）。头因此比以前矮 8px，这是这条对齐的代价。
     ★ align-items 必须 flex-start，不能用 center：th-info 比右侧按钮（28px）矮时
     （例如只有标题、没有副标题的头），center 会让 th-info 跟着按钮居中，标题又掉到 26.8。 */
  /* ★ 和内容区之间那条缝：原来是一条 1px 实线直接把两块切开，太硬。**两个状态分开治**：
     ① 静止态（治「硬」）：页头与内容**同底不加色**——曾试过一层极淡暖底做色阶差，
        但那条色带在页面顶上像贴了块补丁（用户点名拿掉）；现在静止态只留几乎看不见的
        底线。**刻意不上 backdrop-filter**：实测它只会把卡片的红字糊成粉影漂在标题背后
        （是噪点不是层次），还要赔上脱流 + ResizeObserver + 三处 scrollIntoView 的静默回归。
     ② 滚动态（治「切」）：见 .elevated —— 内容真滑到底下时才淡入柔和阴影、把线淡掉。
        滚到顶时页头底下是**空的**，那时打阴影＝给不存在的遮挡关系编故事
        （同 .term-wrap.with-chat 那条「独占/无对话时不加，保持齐边」的分寸）。
     z-index：阴影得压在 .thread 之上 —— .thread 是后面的兄弟，默认会盖住它。 */
  .thread-head { flex: none; padding: 5px 24px; border-bottom: 1px solid rgba(30, 25, 15, .05); position: relative; z-index: 1; transition: border-bottom-color .18s ease; display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
  /* ★ 阴影用伪元素而不是 box-shadow：box-shadow 天然四周都有 —— `0 6px 12px -6px` 水平方向是
     「收缩 6 再模糊 6」，**正好到边**，于是左右两端各留一段 6px 渐弱，看着就是两个圆头戳在窗沿上。
     负 spread 再加也没用：加到不露头，朝下的阴影也就没了。
     伪元素 left:0/right:0 精确铺满、只朝下，左右**零溢出、零圆头**。 */
  .thread-head::after {
    content: ''; position: absolute; left: 0; right: 0; top: 100%; height: 6px;
    background: linear-gradient(rgba(20, 16, 13, .13), rgba(20, 16, 13, 0));
    opacity: 0; transition: opacity .18s ease; pointer-events: none;
  }
  /* 只在内容真滑到页头底下时才抬升。border 只改 color 不改宽度 —— 改宽度会让下面整块跳 1px。 */
  .thread-head.elevated { border-bottom-color: transparent; }
  .thread-head.elevated::after { opacity: 1; }
  /* 侧栏收起：红绿灯 + 悬浮的折叠/搜索钮压在内容区左上，页头统一左让位（全部视图受益）。
     mac 窗口态 140px（红绿灯≈70 + 两钮）；全屏/Windows 无红绿灯（钮在 left:12），100px 够。 */
  .app.rail-collapsed .thread-head { padding-left: 140px; }
  .app.rail-collapsed.fs .thread-head, .app.rail-collapsed.win .thread-head { padding-left: 100px; }
  /* 页头右侧操作聚拢成组贴右（th-info 撑开剩余空间，组内间距统一） */
  .th-actions { display: flex; align-items: center; gap: 8px; flex: none; }
  .th-info b { display: block; font-size: 15px; line-height: 1.35; }
  .th-info small { display: flex; align-items: center; gap: 5px; flex-wrap: wrap; color: var(--dim); font-size: 12px; margin-top: 2px; }
  .btag { display: inline-flex; align-items: center; gap: 4px; }
  .thread { flex: 1; overflow-y: auto; }
  .thread-inner { max-width: 760px; margin: 0 auto; padding: 22px 24px 8px; display: flex; flex-direction: column; gap: 20px; }

  .msg { display: flex; gap: 12px; }
  .msg.user { justify-content: flex-end; }
  .ubody { background: var(--user-bg); border-radius: 16px 16px 5px 16px; padding: 10px 14px; max-width: 78%; word-break: break-word; }
  .ub-text { white-space: pre-wrap; }
  .msg.assistant .body { flex: 1; min-width: 0; }
  .text { white-space: pre-wrap; word-break: break-word; }
  /* 助手消息的 Markdown 阅读版式（渲染出的 HTML 不在 Svelte 作用域内，须 :global） */
  .text.md { white-space: normal; font-size: 14px; line-height: 1.7; }
  .text.md :global(p) { margin: 0 0 10px; }
  .text.md :global(p:last-child), .text.md :global(ul:last-child), .text.md :global(ol:last-child),
  .text.md :global(pre:last-child), .text.md :global(table:last-child) { margin-bottom: 0; }
  .text.md :global(strong) { font-weight: 650; }
  .text.md :global(ul), .text.md :global(ol) { margin: 0 0 10px; padding-left: 22px; }
  .text.md :global(li) { margin: 3px 0; }
  .text.md :global(li::marker) { color: var(--faint); }
  .text.md :global(h1), .text.md :global(h2) { font-size: 16px; font-weight: 650; margin: 16px 0 6px; }
  .text.md :global(h3), .text.md :global(h4) { font-size: 14.5px; font-weight: 650; margin: 14px 0 5px; }
  .text.md :global(h1:first-child), .text.md :global(h2:first-child),
  .text.md :global(h3:first-child), .text.md :global(h4:first-child) { margin-top: 0; }
  .text.md :global(code) { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .88em; background: #f1efe9; padding: 1px 5px; border-radius: 5px; }
  .text.md :global(pre) { background: #f6f5f1; border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; overflow-x: auto; margin: 0 0 10px; }
  .text.md :global(pre code) { background: none; padding: 0; font-size: 12.5px; line-height: 1.6; }
  .text.md :global(a) { color: var(--accent); text-decoration: underline; text-underline-offset: 2px; cursor: pointer; }
  .text.md :global(table) { border-collapse: collapse; margin: 0 0 10px; font-size: 13px; display: block; overflow-x: auto; max-width: 100%; }
  .text.md :global(th), .text.md :global(td) { border: 1px solid var(--border2); padding: 5px 10px; text-align: left; }
  .text.md :global(th) { background: #f6f5f1; font-weight: 600; }
  .text.md :global(blockquote) { margin: 0 0 10px; padding: 2px 12px; border-left: 3px solid var(--border2); color: var(--dim); }
  .text.md :global(hr) { border: none; border-top: 1px solid var(--border); margin: 12px 0; }
  .text.md :global(img) { max-width: 100%; border-radius: 8px; }
  .text.is-err { color: var(--err); }
  /* 重试：无边框，内联跟在红色错误文字后面 */
  .retry-btn { display: inline-flex; align-items: center; gap: 4px; margin-left: 10px; padding: 0; background: none; border: none; color: var(--accent); font: inherit; font-size: 12.5px; cursor: pointer; vertical-align: baseline; }
  .retry-btn:hover { text-decoration: underline; }
  .retry-btn svg { flex: none; }
  /* 额度耗尽卡：明确状态 + 倒计时 + 到点自动续跑 */
  .limit-card { border: 1px solid var(--border2, #e2dfd7); border-left: 3px solid #c98d70; border-radius: 10px; padding: 10px 13px; background: #fbf9f5; max-width: 520px; }
  .limit-head { font-size: 13.5px; font-weight: 600; color: var(--text, #26241f); }
  .limit-sub { margin-top: 3px; font-size: 12.5px; color: var(--dim, #6b675f); }
  .limit-actions { margin-top: 8px; display: flex; flex-wrap: wrap; gap: 4px 8px; }
  .limit-actions .retry-btn { margin-left: 0; }
  .limit-actions .retry-btn.is-armed { color: #3e7a4e; }
  .limit-hint { margin-top: 6px; font-size: 11.5px; color: var(--faint, #9b968c); }
  /* 命令列表：默认收起，点击展开 */
  .cmds { margin-bottom: 9px; }
  .cmds summary { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; list-style: none; width: fit-content;
    font-size: 12px; color: var(--dim); border-radius: 8px; padding: 4px 8px; margin-left: -8px; user-select: none; }
  .cmds summary::-webkit-details-marker { display: none; }
  .cmds summary:hover { background: #f1efe9; }
  .cmds .cmd-ic { color: var(--accent); flex: none; }
  .cmds .cmd-chev { flex: none; transition: transform .15s; }
  .cmds[open] .cmd-chev { transform: rotate(180deg); }
  .cmds .tools { margin: 7px 0 0; }
  .tools { display: flex; flex-direction: column; gap: 5px; margin-bottom: 8px; }
  .tool { display: flex; gap: 8px; align-items: baseline; background: #f6f5f1; border: 1px solid var(--border); border-radius: 8px; padding: 5px 10px; font-size: 12px; }
  .tcode { color: var(--accent); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; flex: none; font-weight: 600; }
  .tdetail { color: var(--dim); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .inlink { background: none; border: none; color: var(--accent); text-decoration: underline; cursor: pointer; padding: 0; font: inherit; word-break: break-all; }
  .err-note { color: var(--err); background: var(--err-soft); border: 1px solid var(--err-border); border-radius: 8px; padding: 8px 10px; font-size: 13px; }
  /* 加载：logo 的 C 一笔描出、圆点落定，循环；后面带本轮耗时。尊重"减弱动态"。 */
  .working { display: flex; align-items: center; gap: 7px; padding: 3px 0; }
  .working:not(:first-child) { margin-top: 12px; }
  .wl { flex: none; }
  .working-t { font-size: 12px; color: var(--faint); font-variant-numeric: tabular-nums; }
  .wl-trace { animation: wldraw 1.5s ease-in-out infinite; }
  @keyframes wldraw { 0% { stroke-dashoffset: 100; opacity: 1; } 55% { stroke-dashoffset: 0; opacity: 1; } 82% { stroke-dashoffset: 0; opacity: 1; } 100% { stroke-dashoffset: 0; opacity: 0; } }
  .wl-dot { transform-box: fill-box; transform-origin: center; animation: wldot 1.5s ease-in-out infinite; }
  @keyframes wldot { 0%, 45% { opacity: 0; transform: scale(.2); } 62% { opacity: 1; transform: scale(1); } 82% { opacity: 1; } 100% { opacity: 0; } }
  @media (prefers-reduced-motion: reduce) { .wl-trace, .wl-dot { animation: none; } .wl-trace { stroke-dashoffset: 0; } .wl-dot { opacity: 1; } }
  @keyframes pulse { 50% { opacity: .35; } }

  /* 排期视图 */
  .sched-inner { max-width: 720px; margin: 0 auto; padding: 18px 24px 24px; }
  /* 密度条 + 站点筛选钉在滚动区顶部 */
  .sched-sticky { position: sticky; top: 0; z-index: 5; background: var(--bg); margin: -18px -4px 8px; padding: 14px 4px 2px; }
  /* 排期密度条：未来 6 周每天一格，深浅=条数 */
  /* 列数跟着格子数走（auto-fit），别再写死 42 —— 加了「过去一周」之后写死的列数会把格子挤扁。
     从 SCHED_PAST_DAYS 推的话又得让 JS 和 CSS 两头对同一个数，迟早对不上。 */
  .sched-density { display: grid; grid-template-columns: repeat(auto-fit, minmax(0, 1fr)); grid-auto-flow: column; gap: 3px; margin: 2px 0 12px; }
  .sd-cell { aspect-ratio: 1; border: none; border-radius: 3px; background: #ecebe6; padding: 0; cursor: default; }
  .sd-cell.l1 { background: #e4c7b4; cursor: pointer; }
  .sd-cell.l2 { background: #d19a76; cursor: pointer; }
  .sd-cell.l3 { background: #b96a44; cursor: pointer; }
  .sd-cell.l4 { background: #a03c2b; cursor: pointer; }
  /* 过去的日子调淡：它们是「已经发生的」，不该和未来的排期抢注意力 */
  .sd-cell.past { opacity: .55; }
  .sd-cell.today { box-shadow: inset 0 0 0 1px #c98d70; background: #f3e7dd; }
  .sd-cell.today.l1, .sd-cell.today.l2, .sd-cell.today.l3, .sd-cell.today.l4 { box-shadow: inset 0 0 0 1px rgba(255,255,255,.55); }
  .sched-grp { scroll-margin-top: 96px; } /* 跳转落点让开顶部钉住的密度条 */
  .sched-grp-n { margin-left: 8px; font-weight: 400; color: var(--faint); font-size: 11px; }
  /* 排期站点筛选：安静的文字胶囊，选中才着色 */
  .sched-filter { display: flex; flex-wrap: wrap; gap: 2px; margin: 4px 0 6px; }
  .sf-chip { display: inline-flex; align-items: center; gap: 5px; border: none; background: none; padding: 3px 9px; border-radius: 999px; font: inherit; font-size: 12px; color: var(--dim, #6b675f); cursor: pointer; }
  .sf-chip:hover { background: #f1efe9; color: var(--text, #26241f); }
  /* 选中态：不上底色，一圈淡边（inset ring 不引起布局跳动） */
  .sf-chip.on { background: none; box-shadow: inset 0 0 0 1px #e6cabb; color: var(--accent); }
  .sf-chip.sf-all { background: none; padding-left: 0; }
  .sf-chip.sf-all.on { background: none; box-shadow: none; color: var(--accent); font-weight: 600; }
  .sched-body small { display: inline-flex; align-items: center; gap: 4px; }
  .sched-t { color: var(--accent); font-weight: 500; }
  .sched-done { margin-left: 6px; color: var(--ok); font-size: 10px; border: 1px solid currentColor; border-radius: 3px; padding: 0 3px; line-height: 13px; }
  .sched-filter-dd { display: inline-block; margin: 4px 0 6px; }
  .sched-grp { padding: 16px 2px 6px; }
  .sched-grp:first-child { padding-top: 4px; }
  .sched-item { display: flex; align-items: center; gap: 14px; padding: 11px 14px; background: var(--card);
    border: 1px solid var(--border); border-radius: 11px; margin-bottom: 8px; box-shadow: var(--shadow-sm); }
  .sched-time { flex: none; width: 128px; font-size: 12.5px; color: var(--accent); font-weight: 550;
    font-variant-numeric: tabular-nums; }
  .sched-body { flex: 1; min-width: 0; }
  .sched-body b { display: block; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sched-body small { color: var(--dim); font-size: 11.5px; }
  .sched-open { flex: none; border: none; background: none; padding: 0; font-size: 12px; color: var(--accent); cursor: pointer; }
  .sched-err { max-width: 720px; margin: 18px auto; }
  .center-hint { text-align: center; color: var(--dim); padding: 40px 0; }
  .sched-empty { text-align: center; color: var(--dim); padding: 12vh 24px; }
  .sched-empty .cal-mark { font-size: 34px; }
  .sched-empty .cal-mark svg { display: block; margin: 0 auto; }
  .sched-empty b { display: block; margin: 12px 0 6px; color: var(--text); font-size: 16px; }
  .sched-empty p { margin: 0 auto; max-width: 380px; font-size: 13px; }

  /* AI 提议的定时任务卡 */
  .proposal { margin-top: 10px; border: 1px solid #ddd9ce; background: #f7f6f2; border-radius: 12px; padding: 12px 14px; display: flex; flex-direction: column; gap: 5px; align-items: flex-start; }
  .proposal-head { font-size: 12px; color: var(--accent); font-weight: 600; }
  .proposal-title { font-size: 14px; }
  .proposal-meta { font-size: 12px; color: var(--dim); }
  .proposal-prompt { font-size: 12.5px; color: var(--text); background: #fff; border: 1px solid var(--border); border-radius: 8px; padding: 7px 9px; width: 100%; white-space: pre-wrap; word-break: break-word; }
  .proposal .btn { margin-top: 3px; }
  .proposal-done { margin-top: 3px; display: inline-flex; align-items: center; gap: 5px; font-size: 12.5px; font-weight: 500; color: #16a34a; }

  /* 定时任务 */
  .task-card { display: flex; align-items: flex-start; gap: 12px; padding: 13px 14px; background: var(--card);
    border: 1px solid var(--border); border-radius: 12px; margin-bottom: 8px; box-shadow: var(--shadow-sm); }
  .task-card.off { opacity: .62; }
  .task-toggle { padding-top: 1px; }
  .switch { width: 34px; height: 20px; border-radius: 12px; border: none; background: #d3d0c8; cursor: pointer; padding: 0; position: relative; transition: background .15s; }
  .switch.on { background: var(--accent); }
  .switch span { position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 50%; background: #fff; transition: transform .15s; box-shadow: var(--shadow-sm); }
  .switch.on span { transform: translateX(14px); }
  .task-body { flex: 1; min-width: 0; }
  .task-body b { font-weight: 550; }
  .task-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; font-size: 12px; color: var(--dim); margin-top: 3px; }
  .task-meta .cdot { color: var(--faint); }
  .task-last { font-size: 12px; margin-top: 5px; color: var(--dim); display: flex; gap: 6px; flex-wrap: wrap; align-items: baseline; }
  .task-last.ok { color: var(--ok); } .task-last.error { color: var(--err); }
  .task-actions { display: flex; align-items: center; gap: 4px; flex: none; }

  .modal.wide { width: min(520px, 94vw); }
  .trow { display: flex; gap: 12px; }
  .tfield { display: flex; flex-direction: column; gap: 5px; flex: 1; min-width: 0; }
  .tfield > span { font-size: 12px; color: var(--dim); }
  /* 任务运行记录 */
  .trun { border: 1px solid var(--border); border-radius: 10px; padding: 9px 11px; }
  .trun-head { display: flex; align-items: center; gap: 8px; font-size: 12.5px; min-width: 0; }
  .trun-badge { flex: none; padding: 1px 7px; border-radius: 999px; font-size: 11px; }
  .trun-badge.ok { background: #e7f0e8; color: #3e7a4e; }
  .trun-badge.err { background: #f7e8e4; color: #a03c2b; }
  /* 订阅限额顺延（非失败语义）：琥珀 warn 色系 + 小时钟 */
  .trun-badge.defer { background: #fdf6e3; color: #9a6b00; display: inline-flex; align-items: center; gap: 3px; }
  .trun-site.is-defer { color: #9a6b00; }
  .defer-tag { margin-left: 6px; padding: 0 6px; border-radius: 999px; background: #fdf6e3; color: #9a6b00; font-size: 10.5px; vertical-align: 1px; }
  .trun-sum { color: var(--dim); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; min-width: 0; }
  .trun-sites { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 7px; }
  .trun-site { display: inline-flex; align-items: center; gap: 5px; padding: 3px 9px; border: 1px solid var(--border2); border-radius: 999px; font-size: 12px; background: var(--card); color: inherit; font-family: inherit; cursor: pointer; }
  .trun-site:hover { border-color: var(--accent); color: var(--accent); }
  .trun-site.is-err { cursor: help; color: #a03c2b; border-color: #ecd4cc; background: #fbf3f0; }
  /* 多站点胶囊 */
  .tsites { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 2px; }
  .tsite-chip { display: inline-flex; align-items: center; gap: 5px; padding: 3px 6px 3px 8px; border: 1px solid var(--border2); border-radius: 999px; font-size: 12px; background: var(--card); }
  .tsite-chip button { border: none; background: none; padding: 0 2px; font-size: 13px; line-height: 1; color: var(--faint); cursor: pointer; }
  .tsite-chip button:hover { color: var(--accent); }
  .launcher-sites { margin-top: 7px; }
  .multi-add { flex: none; border: none; background: none; padding: 2px 4px; font-size: 11.5px; color: var(--faint); cursor: pointer; border-radius: 6px; }
  .multi-add:hover { color: var(--accent); background: #f1efe9; }
  /* 任务弹窗里的 模型+强度（对话同款 ModelFx），套输入框外观（--ctl-h 定高，内容 flex 垂直居中） */
  .tfield-fx { border: 1px solid var(--border2); border-radius: 8px; padding: 0 3px; background: var(--card); height: var(--ctl-h); box-sizing: border-box; display: flex; align-items: center; }
  .tfield-fx :global(.fx) { width: 100%; }
  .tfield-fx :global(.fx-trigger) { width: 100%; justify-content: flex-start; font-size: 13px; padding: 4px 8px; }
  .tfield-fx :global(.fx-chev) { margin-left: auto; }
  .tcheck { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
  .tcheck input { width: auto; }

  .composer-wrap { flex: none; padding: 10px 24px 20px; }
  .composer-wrap .composer { max-width: 760px; margin: 0 auto; }

  /* ---- 按钮 / 弹窗 ---- */
  /* 动作按钮统一尺度：13px 字 + 6px 14px 内边距，高约 30px（与输入框/下拉同高，主次同高）。 */
  .btn { background: #fff; color: var(--text); border: 1.5px solid var(--border2); border-radius: 8px; padding: 6px 14px; cursor: pointer; font-size: 13px; line-height: 1.25; }
  .btn:hover { background: #f6f5f1; } .btn:disabled { opacity: .5; cursor: default; }
  .btn.primary { background: var(--accent); border-color: var(--accent); color: #fff; } .btn.primary:hover { background: var(--accent-h); }
  .btn.ghost { border-color: var(--border); }
  .btn.small { padding: 4px 10px; font-size: 12px; }
  .btn.sm { padding: 5px 12px; font-size: 12px; border-radius: 8px; }
  .btn.soft { display: inline-flex; align-items: center; gap: 6px; padding: 3px 12px; background: #fff; color: var(--text); border: 1px solid var(--border2); border-radius: 9px; font-weight: 500; }
  .btn.soft:hover { background: var(--rail); border-color: #cfccc2; }
  .btn.soft .plus-ic { color: var(--dim); }
  /* 无边框动作按钮（头部次要动作）：去边框去底，仅 hover 有淡底 */
  .btn.bare { border: none; background: none; }
  .btn.bare:hover { background: var(--rail); border-color: transparent; }
  .authbtn { display: inline-flex; align-items: center; gap: 3px; background: none; border: none; padding: 1px 2px; color: var(--accent); font-size: 12px; font-weight: 550; line-height: 1; cursor: pointer; }
  .authbtn:hover { color: var(--accent-h); }
  .permit-card { border: 1px solid var(--border2); border-radius: 11px; overflow: hidden; background: #fff; max-width: 480px; }
  .permit-card.danger { border-color: #f0d9a8; }
  .permit-head { display: flex; align-items: center; gap: 7px; padding: 8px 12px; font-size: 12px; font-weight: 600; color: var(--text); background: #f6f5f1; border-bottom: 0.5px solid var(--border); }
  .permit-card.danger .permit-head { color: #8a5a08; background: #fdf6e6; border-bottom-color: #f0e3c4; }
  .permit-dot { width: 7px; height: 7px; border-radius: 50%; background: #b45309; flex: none; }
  .permit-tag { margin-left: auto; font-size: 10px; font-weight: 600; color: #b45309; background: #faf1d8; padding: 1px 7px; border-radius: 5px; }
  .permit-desc { padding: 10px 12px 0; font-size: 13px; font-weight: 550; color: var(--text); line-height: 1.55; word-break: break-word; }
  .permit-meta { padding: 3px 12px 0; font-size: 11px; color: var(--dim); }
  .permit-more { display: block; margin: 7px 12px 0; padding: 0; border: 0; background: none; font-size: 11px; color: var(--dim); cursor: pointer; text-align: left; }
  .permit-more:hover { color: var(--text); }
  .permit-cmd { display: block; margin: 6px 12px 0; font-size: 12px; background: #17130f; color: #e8dcc8; padding: 8px 10px; border-radius: 7px; white-space: pre-wrap; word-break: break-all; max-height: 180px; overflow-y: auto; }
  .permit-act { display: flex; gap: 8px; padding: 10px 12px 12px; }
  .permit-act .btn { flex: 1; }
  .cf-mark { flex: none; display: inline-block; vertical-align: middle; }
  .hero-btns { display: flex; gap: 10px; flex-wrap: wrap; justify-content: center; }
  .sec-acts { display: inline-flex; gap: 6px; align-items: center; }
  .sec-acts .btn { display: inline-flex; align-items: center; gap: 5px; }
  .cf-modal { width: min(440px, 94vw); max-height: 88vh; }
  .cf-step { border-top: 0.5px solid var(--border); padding-top: 12px; margin-top: 6px; }
  .cf-step:first-child { border-top: none; padding-top: 0; margin-top: 0; }
  .cf-step-t { font-size: 13px; color: var(--text); margin: 0 0 7px; line-height: 1.5; }
  .cf-perms { margin: 0 0 9px; padding-left: 0; list-style: none; display: flex; flex-direction: column; gap: 3px; }
  .cf-perms li { font-size: 12px; color: var(--dim); padding-left: 13px; position: relative; }
  .cf-perms li::before { content: '·'; position: absolute; left: 4px; color: #f6821f; font-weight: 700; }
  .cf-perms li b { color: var(--text); font-weight: 550; }
  .cf-row { display: flex; gap: 8px; }
  .cf-row .tin { flex: 1; }
  .wr-note { display: flex; align-items: center; gap: 7px; width: fit-content; max-width: 100%; margin-top: 12px; font-size: 12px; color: #8a5a08; }
  .wr-note .wr-ic { flex: none; color: #d9a400; }
  .wr-note span { color: #8a5a08; }
  .wr-btn { flex: none; border: none; background: #d9a400; color: #fff; font-size: 11px; font-weight: 500; padding: 3px 9px; border-radius: 6px; cursor: pointer; }
  .wr-btn:hover { background: #c08f00; }
  .wr-btn:disabled { opacity: .85; cursor: default; }
  .wr-spin { display: inline-block; width: 10px; height: 10px; border: 1.6px solid #fff; border-right-color: transparent; border-radius: 50%; animation: rspin .7s linear infinite; margin-right: 5px; vertical-align: -1px; }
  .cf-perms-toggle { display: inline-flex; align-items: center; gap: 5px; background: none; border: none; padding: 0 0 8px; font-size: 12px; color: var(--dim); cursor: pointer; }
  .cf-perms-toggle:hover { color: var(--text); }
  .cf-chev { flex: none; transition: transform .12s; }
  .cf-chev.open { transform: rotate(180deg); }
  .cf-proj-in { border: none; background: none; font-size: 13px; color: var(--text); padding: 3px 6px; min-width: 150px; outline: none; }
  .cf-proj-in::placeholder { color: #b3ada2; }
  .cb-prev { position: relative; display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; font-size: 12px; padding: 3px 8px; border: none; background: none; color: var(--accent); cursor: pointer; font-weight: 500; border-radius: 6px; }
  .cb-prev:hover:not(:disabled) { background: var(--rail); }
  .cb-prev:disabled { opacity: .45; cursor: default; }
  .cb-prev.dim { color: var(--dim); font-weight: 400; }
  .cb-prev svg { flex: none; }
  /* 自定义悬停提示（不用系统 title） */
  .cf-bar { max-width: 760px; margin: 0 auto 6px; display: flex; align-items: center; gap: 2px; flex-wrap: wrap; }
  /* 等待消息（排队）条：琥珀色调，区别于普通输入 */
  .queued-row { display: flex; align-items: center; gap: 8px; margin: 10px 12px 0; padding: 6px 8px 6px 11px; background: #fbf6ea; border: 1px solid #f0e2c0; border-radius: 9px; font-size: 12.5px; }
  .queued-ic { flex: none; color: #b45309; display: inline-flex; }
  .queued-t { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--dim); }
  .queued-btn { flex: none; border: none; background: none; color: var(--accent); font-size: 12px; cursor: pointer; padding: 2px 4px; }
  .queued-btn:hover { text-decoration: underline; }
  .queued-x { flex: none; border: none; background: none; color: var(--faint); font-size: 15px; line-height: 1; cursor: pointer; padding: 0 3px; }
  .queued-x:hover { color: var(--err); }
  .send.queue { background: var(--accent); color: #fff; }
  .attach-row { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding: 10px 14px 2px; }
  .attach-chip { display: inline-flex; align-items: center; gap: 6px; max-width: 190px; padding: 3px 4px 3px 6px; background: var(--panel); border: 1px solid var(--border2); border-radius: 8px; font-size: 12px; }
  .attach-img { width: 24px; height: 24px; object-fit: cover; border-radius: 4px; flex: none; }
  .attach-ic { color: var(--dim); flex: none; display: inline-flex; }
  .attach-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); }
  .attach-x { flex: none; border: none; background: none; color: var(--dim); cursor: pointer; font-size: 14px; line-height: 1; padding: 0 2px; }
  /* 用户气泡里的附件卡（只读，白底衬在浅色气泡上） */
  .ub-atts { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
  .ub-atts.only { margin-top: 0; }
  .ub-att { display: inline-flex; align-items: center; gap: 6px; max-width: 220px; padding: 4px 10px 4px 8px; background: #fff; border: 1px solid var(--border2); border-radius: 8px; font-size: 12px; }
  .ub-att-img { padding: 0; border: 1px solid var(--border2); border-radius: 10px; overflow: hidden; cursor: zoom-in; line-height: 0; background: repeating-conic-gradient(#ececea 0 25%, #fff 0 50%) 0 0 / 14px 14px; display: inline-flex; align-items: center; justify-content: center; min-width: 44px; min-height: 34px; }
  /* 按图片原始比例呈现（纯像素上限、不裁切），宽 logo/竖图都能看到全貌；点击仍可放大。
     注意不能用 max-width:min(…,100%)：容器 shrink-to-fit，百分比循环引用会让整条声明失效。 */
  .ub-att-img img { display: block; max-width: 260px; max-height: 180px; width: auto; height: auto; }
  .ub-att-img.ph { display: inline-block; width: 128px; height: 96px; cursor: default; animation: phpulse 1.2s ease-in-out infinite; }
  /* 助手消息里的生图产物：与用户附件同款卡片，气泡外基底微调 */
  .gen-imgs { margin-top: 6px; }
  .ub-att.as-btn { cursor: pointer; font: inherit; font-size: 12px; color: inherit; }
  .ub-att.as-btn:hover { border-color: var(--border3, var(--border2)); }
  @keyframes phpulse { 0%, 100% { opacity: .55; } 50% { opacity: .9; } }
  .ub-att-ic { flex: none; display: inline-flex; color: var(--dim); }
  .ub-att-n { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); }
  .attach-x:hover { color: var(--err); }
  .attach-loading { font-size: 11px; color: var(--dim); }
  /* 提示词库 */
  .prompt-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(272px, 1fr)); gap: 12px; }
  .prompt-card { display: flex; flex-direction: column; background: var(--card); border: 1px solid var(--border2); border-radius: 12px; padding: 13px 14px 11px; }
  .prompt-top { display: flex; align-items: center; gap: 7px; margin-bottom: 6px; }
  .prompt-title { font-size: 13.5px; font-weight: 600; color: var(--text); flex: 1; min-width: 0; }
  .prompt-tag { flex: none; font-size: 10px; font-weight: 600; color: var(--dim); background: #e7e4dd; padding: 1px 6px; border-radius: 5px; }
  .prompt-body { margin: 0 0 11px; font-size: 12.5px; line-height: 1.5; color: var(--dim); display: -webkit-box; -webkit-line-clamp: 5; line-clamp: 5; -webkit-box-orient: vertical; overflow: hidden; }
  .prompt-acts { display: flex; align-items: center; gap: 4px; margin-top: auto; }
  .prompt-act { position: relative; width: 28px; height: 28px; display: inline-flex; align-items: center; justify-content: center; border: none; border-radius: 8px; background: none; color: var(--dim); cursor: pointer; }
  .prompt-act:hover { color: var(--text); background: var(--rail); }
  .prompt-act svg { flex: none; }
  /* 模板库单独一个更宽的容器：grid 本来就是 auto-fill（会自己长列），真正卡住它的是
     .sched-inner 的 720px —— 三列要 3*272+2*12+48 = 876px，720 差得远，所以窗口再宽也只有两列。
     日程页还得用窄栏（那是读列表的），所以不改 .sched-inner，另起一个。 */
  .tmpl-inner { max-width: 1180px; margin: 0 auto; padding: 18px 24px 24px; }
  .th-line { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  /* 提示挪到标题**同一行**了。`.th-info small` 那 2px 上间距是给「标题下面那行」准备的，
     这里得清掉；选择器带上 .tmpl-hint 才压得过它（它带元素选择器，分更高）。 */
  .th-info small.tmpl-hint { margin-top: 0; color: var(--faint); font-size: 11.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  /* 负边距抵掉筹码自己的内边距 —— 那 8px/3px 是**点击热区**，不该算进视觉间距：
     - 左：不抵的话整排比上面的「模板库」缩进 8px，看着就是没对齐。
     - 下：不抵的话筹码文字离底边比标题离顶边多出 3px 的死空（上 6 / 下 10.5），上下就不一致。 */
  .tmpl-chips { display: flex; flex-wrap: wrap; gap: 2px; margin-top: 5px; margin-left: -8px; margin-bottom: -3px; }
  /* 无外框、无底：选中只靠**颜色加重**（--dim → --text）。
     ★ 故意不动 font-weight：字重一变宽度就变，点一下整排筹码会跟着挪位。 */
  .tmpl-chip { display: inline-flex; align-items: center; gap: 3px; padding: 3px 8px; border: 0; border-radius: 999px; background: transparent; color: var(--dim); font-size: 12px; cursor: pointer; }
  .tmpl-chip:hover { color: var(--text); }
  .tmpl-chip:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
  .tmpl-chip.on { color: var(--text); }
  .tc-ic { flex: none; margin-right: 1px; opacity: 0.7; }
  .tc-n { font-size: 10px; opacity: 0.55; font-variant-numeric: tabular-nums; }
  /* ★ 240 是**默认窗口能放下两列**倒推出来的，别再往上调：
     默认 820 宽、左栏 240 → 内容 580，减 48 内边距 = 532。两列 = 2*240+12 = 492 ✓。
     上一版写 272 要 556 > 532，就差这一点，默认窗口直接掉成**一列**。
     再宽才 3 列（≈1050）、4 列（≈1300，到 1180 封顶就不再加）。 */
  .tmpl-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 12px; }
  /* 从全局搜索跳过来时闪一下：一屏十几张卡，跳到了也认不出是哪张 */
  @keyframes tmplFlash { from { box-shadow: 0 0 0 2px var(--accent); } to { box-shadow: 0 0 0 2px transparent; } }
  .tmpl-card.tmpl-flash { animation: tmplFlash 1.4s ease-out; }
  .tmpl-card { display: flex; flex-direction: column; background: var(--card); border: 1px solid var(--border2); border-radius: 12px; overflow: hidden; }
  .tmpl-thumb { position: relative; height: 158px; overflow: hidden; background: #fff; display: flex; align-items: center; justify-content: center; border-bottom: 1px solid var(--border); }
  .tmpl-frame { position: absolute; top: 0; left: 0; width: 1280px; height: 860px; border: 0; transform: scale(0.23); transform-origin: top left; pointer-events: none; background: #fff; }
  .tmpl-letter { font-size: 40px; font-weight: 600; color: #a03c2b; }
  .tmpl-hover { position: absolute; inset: 0; display: flex; align-items: flex-end; justify-content: center; gap: 8px; padding: 10px; background: linear-gradient(to top, rgba(0, 0, 0, .4), rgba(0, 0, 0, .05) 45%, transparent 70%); opacity: 0; transition: opacity .12s; }
  .tmpl-card:hover .tmpl-hover { opacity: 1; }
  .tmpl-act { position: relative; width: 26px; height: 26px; display: inline-flex; align-items: center; justify-content: center; border: none; border-radius: 7px; background: rgba(255, 255, 255, .95); color: var(--text); cursor: pointer; box-shadow: 0 2px 8px rgba(0, 0, 0, .15); }
  .tmpl-act.primary { background: var(--accent); color: #fff; }
  .tmpl-act:hover { transform: translateY(-1px); }
  .tmpl-act svg { flex: none; width: 13px; height: 13px; }
  .tmpl-body { padding: 10px 12px 12px; flex: 1; }
  .tmpl-body b { font-size: 13px; }
  .tmpl-sub { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; margin-top: 4px; }
  .tmpl-desc { flex: 1; min-width: 0; font-size: 12px; color: var(--dim); line-height: 1.4; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .tmpl-meta { flex: none; display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: #b3ada2; }
  .tmpl-pg { color: var(--dim); background: var(--border); border-radius: 4px; padding: 0 4px; line-height: 15px; }
  .site-grp-fav { width: 14px; height: 14px; border-radius: 3px; flex: none; object-fit: cover; }
  .plus-ic { flex: none; }
  .icon-btn { background: none; border: none; cursor: pointer; padding: 6px; border-radius: 8px; color: var(--dim); display: inline-flex; align-items: center; justify-content: center; }
  .icon-btn:hover { background: #f1efe9; color: var(--text); }
  .icon-btn:disabled { opacity: .55; cursor: default; }
  .rfz { display: block; }
  .rfz.spin { animation: rspin .8s linear infinite; }
  @keyframes rspin { to { transform: rotate(360deg); } }
  @keyframes pop { from { opacity: 0; transform: translateY(4px); } }
  .x { background: none; border: none; color: var(--faint); font-size: 20px; cursor: pointer; line-height: 1; }
  .x:hover { color: var(--err); } .x.sm { font-size: 15px; }

  .mask { position: fixed; inset: 0; background: rgba(25,20,10,.28); z-index: 50; }
  .sheet, .modal { position: fixed; z-index: 60; background: var(--bg); border: 1px solid var(--border); box-shadow: var(--shadow-lg); display: flex; flex-direction: column; }
  .sheet { top: 0; right: 0; bottom: 0; width: min(400px, 92vw); border-radius: 0; }
  /* margin:auto 居中而非 transform——transform 会给内部 position:fixed 的下拉菜单
     制造 containing block，菜单被钳进弹窗坐标系再被 overflow:hidden 裁掉（遮挡）。 */
  .modal { inset: 0; margin: auto; height: fit-content; max-height: 88vh; width: min(440px, 92vw); border-radius: 14px; overflow: hidden; }
  .sheet-head { display: flex; justify-content: space-between; align-items: center; padding: 15px 18px; border-bottom: 1px solid var(--border); }
  .sheet-body { padding: 16px 18px; overflow-y: auto; display: flex; flex-direction: column; gap: 7px; }
  .sec-head { display: flex; justify-content: space-between; align-items: center; font-size: 11px; letter-spacing: .03em; text-transform: uppercase; color: var(--faint); font-weight: 600; margin-bottom: 1px; }
  .sec-head.mt { margin-top: 12px; }
  .conn-list { display: flex; flex-direction: column; gap: 5px; }
  .conn-row { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border: 1px solid var(--border); border-radius: 11px; cursor: pointer; transition: border-color .12s, background .12s; }
  .conn-row:hover { background: #faf9f6; }
  .conn-row.on { border-color: #cfc9ec; background: #f7f6ff; }
  .conn-row :global(.sm) { border-radius: 6px; }
  .conn-main { flex: 1; min-width: 0; } .conn-main b { display: block; font-size: 13.5px; } .conn-main small { display: block; color: var(--dim); font-size: 11px; }
  /* ★ 上面这条 display:block（0,1,1）会压过 .cs-os 的 inline-flex（0,1,0）：设置弹窗的连接行里
     发行版图标于是退回**行内基线对齐**——12px 的图标坐在 11px 文字的基线上，整个上蹿一截。
     这里把 flex 垂直居中赢回来（用 flex 而非 inline-flex：副标题本来就独占一行）。 */
  .conn-main small.cs-os { display: flex; align-items: center; }
  /* 技能包更新：设置行的一键升级按钮 + 切换器里的小圆点徽标 */
  .pack-upd { flex: none; border: none; background: var(--accent); color: #fff; font-size: 11px; font-weight: 550; padding: 3px 10px; border-radius: 7px; cursor: pointer; }
  .pack-upd:hover { background: var(--accent-h); }
  .pack-upd:disabled { opacity: .6; cursor: default; }
  .pack-dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; background: var(--accent); margin-left: 6px; vertical-align: 1px; }
  .icon-btn.sm { padding: 4px; border-radius: 7px; }
  .brain-row { display: flex; align-items: center; gap: 11px; padding: 0; }
  /* 状态点放进与 .icon-btn 同宽(27px)的居中盒，右缘、图标中心都与本地模型区的刷新图标对齐。 */
  .brain-dot { flex: none; width: 27px; display: inline-flex; align-items: center; justify-content: center; }
  .brain-ic { flex: none; width: 22px; height: 22px; display: inline-flex; align-items: center; justify-content: center; }
  .brain-main { flex: 1; min-width: 0; } .brain-main b { display: block; font-size: 13.5px; line-height: 1.15; } .brain-main small { display: block; color: var(--dim); font-size: 11px; line-height: 1.1; }
  /* 自定义模型：折叠管理器（对齐大脑名，缩进 33px = 图标 22 + gap 11） */
  /* 大脑列表：整列作为一个 sheet-body 子项，块间距自控（不吃 sheet-body 的 7px gap）。 */
  .brains-list { display: flex; flex-direction: column; gap: 14px; }
  /* 每个大脑（行 + 自定义模型）成一块，块内贴紧。 */
  .brain-block { display: flex; flex-direction: column; gap: 0; }
  /* 未安装行：一键安装挪到状态文字下方（缩进对齐 brain-main），复制小图标同行在旁 */
  .brain-install { display: flex; align-items: center; gap: 5px; margin: 3px 0 2px 33px; }
  .cust { margin: 0 0 0 33px; }
  .cust-head { display: inline-flex; align-items: center; gap: 5px; background: none; border: none; padding: 0; cursor: pointer; font: inherit; font-size: 11.5px; color: var(--dim); -webkit-appearance: none; appearance: none; }
  .cust-head:hover { color: var(--text); }
  .cust-chev { color: var(--faint); flex: none; transition: transform .12s; }
  .cust-chev:not(.open) { transform: rotate(-90deg); }
  .cust-n { min-width: 15px; height: 15px; padding: 0 4px; display: inline-flex; align-items: center; justify-content: center; background: var(--border); color: var(--dim); border-radius: 8px; font-size: 10px; font-weight: 600; }
  .cust-body { display: flex; flex-direction: column; gap: 5px; margin: 4px 0 4px; }
  .cust-chip { display: flex; align-items: center; gap: 6px; background: #f6f5f1; border: 1px solid var(--border); border-radius: 8px; padding: 4px 6px 4px 9px; }
  .cust-id { flex: 1; min-width: 0; font-family: ui-monospace, monospace; font-size: 11.5px; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .cust-x { flex: none; background: none; border: none; color: var(--faint); font-size: 15px; line-height: 1; padding: 0 3px; border-radius: 5px; cursor: pointer; }
  .cust-x:hover { color: var(--err); background: #fff; }
  .cust-add { display: flex; gap: 6px; align-items: center; }
  .cust-add .tin { flex: 1; min-width: 0; width: auto; }
  .cust-add .btn { flex: none; }
  .hint { color: var(--dim); font-size: 12px; margin: 2px 0; line-height: 1.6; }
  .hint.mono { font-family: ui-monospace, monospace; font-size: 11px; color: var(--faint); background: #f6f5f1; padding: 5px 8px; border-radius: 6px; overflow-wrap: anywhere; }
  .hint.tos { color: var(--faint); margin-top: 12px; }
  .warn-text { color: var(--warn); }
  .dim { color: var(--dim); }
  .tin { width: 100%; }
  .row-end { display: flex; justify-content: flex-end; align-items: center; gap: 7px; margin-top: 2px; }

  /* 概览条容器（托管仪表盘 / 定时任务顶部共用）：裸化的安静统计行——无底无框，
     左缘与下方卡片列表对齐；各站例外收进「待审」的 hover 弹层（.ss-pop-menu）。趋势小柱在各站卡片数据行里 */
  .md-board { display: flex; flex-direction: column; gap: 3px; margin: 0 0 12px; padding: 2px 0 4px; }
  /* 数据条（可复用）：每段=数字(13px/600/正文色)+标签(11px faint)，段间 1px 细竖线，状态段带 6px 色点 */
  .stat-strip { display: flex; align-items: center; flex-wrap: wrap; row-gap: 4px; }
  .ss-seg { display: inline-flex; align-items: baseline; gap: 5px; min-width: 0; }
  .ss-seg + .ss-seg { margin-left: 18px; }
  .ss-num { font-size: 13px; font-weight: 600; color: var(--text); font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 320px; }
  .ss-lbl { font-size: 11px; color: var(--faint); white-space: nowrap; }
  .ss-dot { width: 6px; height: 6px; border-radius: 50%; flex: none; align-self: center; }
  .ss-dot.ok { background: #3e9463; }
  .ss-dot.off { background: #b7b2a9; }
  .ss-dot.err { background: #c94f37; }
  .ss-dot.warn { background: #d9a400; }
  /* 共享 hover 气泡（$lib/tip action 挂到 body 上）：深色小圆角、白字、单行、不挡交互 */
  :global(.ui-tip) { position: fixed; z-index: 96; pointer-events: none; background: #26241f; color: #fff; font-size: 11px; line-height: 1.5; padding: 3px 9px; border-radius: 6px; width: max-content; max-width: 340px; white-space: normal; box-shadow: 0 4px 14px rgba(30, 25, 15, .22); }
  .md-exc { display: flex; align-items: center; gap: 6px; width: fit-content; max-width: 100%; padding: 2px 6px; margin-left: -6px; border: none; background: none; border-radius: 7px; font-size: 12px; color: var(--dim); cursor: pointer; text-align: left; }
  .md-exc:hover { background: #efede7; }
  .md-exc-name { color: var(--text); font-weight: 500; }
  .md-exc-tag { flex: none; padding: 0 7px; border-radius: 999px; font-size: 10.5px; }
  .md-exc-tag.err { background: #f7e8e4; color: #a03c2b; }
  .md-exc-tag.warn { background: #fdf6e3; color: #9a6b00; }
  .md-exc-tag.off { background: #eee9df; color: var(--dim); }
  /* 「待审」悬停弹层：各站例外从常驻一行收进这里，移到「待审」上才展开。 */
  .ss-seg.ss-pop { position: relative; cursor: default; }
  .ss-seg.ss-pop:hover .ss-lbl { color: var(--dim); }
  .ss-pop-menu {
    display: none; position: absolute; top: calc(100% + 5px); left: -8px; z-index: 60;
    min-width: 170px; flex-direction: column; gap: 1px; padding: 4px;
    background: #fff; border: 1px solid var(--border2); border-radius: 10px; box-shadow: 0 8px 26px rgba(30, 25, 15, .16);
  }
  /* 透明桥：填掉触发器与弹层之间那 5px 间隙，鼠标移过去不会中断 hover（弹层是 .ss-pop 的后代） */
  .ss-pop-menu::before { content: ''; position: absolute; left: 0; right: 0; top: -5px; height: 5px; }
  .ss-seg.ss-pop:hover .ss-pop-menu { display: flex; }
  .ss-pop-menu .md-exc { width: 100%; margin-left: 0; }
  .mb-trend { display: inline-flex; align-items: flex-end; gap: 2px; height: 12px; font-size: 10.5px; color: var(--faint); }
  .mb-trend i { display: inline-block; width: 3px; background: var(--accent); border-radius: 1px; opacity: .7; }
  /* 周报归档列表与正文 */
  .rp-item { width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 10px; border: none; background: none; border-radius: 8px; text-align: left; cursor: pointer; font: inherit; }
  .rp-item:hover { background: #f4f3ef; }
  .rp-date { flex: none; display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; font-weight: 500; color: var(--text); font-variant-numeric: tabular-nums; }
  .rp-mets { min-width: 0; font-size: 12px; color: var(--dim); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .rp-body { font-size: 13px; }

  /* 托管：卡片徽标 / 待审队列 / 计划编辑 / 向导边界说明 */
  .md-badge { margin-left: 8px; font-size: 10.5px; padding: 1px 7px; border-radius: 999px; background: #e8f0e4; color: #3c6b32; vertical-align: 1px; }
  .md-badge.off { background: #eee9df; color: var(--dim); }
  .md-task { font-size: 11.5px; padding: 1px 8px; border-radius: 999px; background: #f1efe9; color: var(--dim); margin-right: 6px; }
  .md-task.off { opacity: 0.55; text-decoration: line-through; }
  /* 通用文字链按钮（任务卡「查看对话/运行记录」、托管卡操作等）——此前从未定义,一直是系统默认灰凸样式 */
  .link { border: none; background: none; padding: 0; font: inherit; font-size: 12.5px; color: var(--accent); cursor: pointer; white-space: nowrap; }
  .link:hover { text-decoration: underline; }
  .md-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 6px; margin-top: 10px; padding-top: 9px; border-top: 1px solid var(--border, #ecebe6); }
  .md-actions .link { padding: 3px 9px; border-radius: 999px; font-size: 12px; color: var(--dim, #6b675f); }
  .md-actions .link:hover { background: #f1efe9; color: var(--text, #26241f); text-decoration: none; }
  .md-actions .link.md-primary { color: var(--accent); box-shadow: inset 0 0 0 1px #e6cabb; }
  /* 等级选择器伪装成头部徽标（bare 触发器 + 绿底胶囊） */
  .md-lv-badge { display: inline-block; margin-left: 8px; vertical-align: 1px; }
  .md-lv-badge :global(.dd) { width: auto; }
  .md-lv-badge :global(.dd-trigger.bare) { font-size: 10.5px; padding: 1px 7px; border-radius: 999px; background: #e8f0e4; color: #3c6b32; }
  .md-foot { display: flex; align-items: flex-start; gap: 5px; margin: 8px 0 0; font-size: 11px; color: var(--faint, #9b968c); }
  .md-foot svg { flex: none; margin-top: 3px; }
  .md-queue { margin-top: 8px; border: 1px solid var(--line, #e6e2d8); border-radius: 10px; padding: 4px 10px; background: #fbfaf7; }
  .md-row { display: flex; align-items: center; gap: 10px; padding: 7px 0; border-bottom: 1px dashed var(--line, #e6e2d8); }
  .md-row:last-child { border-bottom: none; }
  .md-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
  .md-sub { flex: none; color: var(--faint); font-size: 11.5px; }
  .md-btns { flex: none; display: flex; align-items: center; gap: 6px; }
  /* 预览：无边框无底的文字链（带 ↗），跟旁边两个实心按钮并排——次要动作退成链接 */
  .md-prev { flex: none; color: var(--dim); }
  .md-prev:hover { color: var(--accent); }
  .md-plan { margin-top: 8px; display: flex; flex-direction: column; gap: 8px; }
  .md-plan textarea { font-size: 12.5px; line-height: 1.6; }
  .md-genrow { display: flex; align-items: center; gap: 10px; margin: 6px 0 10px; }
  .md-bound { border: 1px solid #e5d9b8; background: #fdf9ec; border-radius: 10px; padding: 10px 14px; font-size: 12.5px; margin: 8px 0; }
  .md-bound ul { margin: 6px 0 0; padding-left: 18px; display: flex; flex-direction: column; gap: 4px; }
  .md-badge.lv { background: #e7ecf7; color: #3a5da8; }
  .md-err { color: var(--err, #c0392b); font-size: 12px; margin: 4px 0 0; }
  .md-warn { color: #9a6b00; font-size: 12px; margin: 4px 0 0; }
  .md-group { display: flex; align-items: center; justify-content: space-between; padding: 6px 0 2px; font-size: 11.5px; color: var(--faint); }
  .md-lv { margin-left: auto; }
  .task-card.hl { outline: 2px solid #d9a441; outline-offset: 2px; border-radius: 12px; }
  .md-badge.lv3 { background: #f7e5e0; color: #a8402a; }
  .md-foot-warn { margin-top: 8px; font-size: 12px; line-height: 1.7; color: #9a6b00; background: #fdf6e3; border: 1px solid #ecd9a0; border-radius: 8px; padding: 6px 10px; }
  .md-editq { width: 44px; padding: 0 4px; font-size: 12px; border: 1px solid #ecd9a0; border-radius: 5px; background: #fff; color: inherit; text-align: center; }
  .md-danger { border: 1px solid #e2b4a8; background: #fdf1ee; border-radius: 10px; padding: 10px 14px; font-size: 12.5px; margin: 8px 0; color: #7c2d1a; }
  .md-danger ul { margin: 6px 0 0; padding-left: 18px; display: flex; flex-direction: column; gap: 4px; }
  .md-li-danger { color: #a8402a; }
  /* 托管预检软警示（warn 色系，对齐 md-foot-warn；只提醒不拦截） */
  .md-precheck { border: 1px solid #ecd9a0; background: #fdf6e3; border-radius: 10px; padding: 10px 14px; font-size: 12.5px; line-height: 1.7; margin: 8px 0; color: #9a6b00; }
  .md-precheck b { display: inline-flex; align-items: center; gap: 5px; }
  .md-precheck ul { margin: 6px 0 0; padding-left: 18px; display: flex; flex-direction: column; gap: 4px; }
  .md-pre-ack { margin-right: auto; align-self: center; font-size: 11.5px; color: var(--dim); }
</style>
