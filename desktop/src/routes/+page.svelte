<script lang="ts">
  import { invoke, Channel } from '@tauri-apps/api/core';
  import { getCurrentWindow } from '@tauri-apps/api/window';
  import { open, confirm as confirmDialog } from '@tauri-apps/plugin-dialog';
  import { openUrl } from '@tauri-apps/plugin-opener';
  import BrainIcon from '$lib/BrainIcon.svelte';
  import SiteMark from '$lib/SiteMark.svelte';
  import type {
    Connection, Discovery, Site, BrainsInfo, Brain, ImportOutcome,
    Conversation, Message, TaskType, TurnEvent, ToolCall, ScheduledItem, ScheduledTask, TaskProposal,
  } from '$lib/types';
  import { loadPrefs, savePrefs } from '$lib/defaults';
  import Dropdown from '$lib/Dropdown.svelte';

  // ---------- setup ----------
  let conns = $state<Connection[]>([]);
  let activeConnId = $state('');
  let discovery = $state<Discovery | null>(null);
  let discoveryLoading = $state(false);
  let brains = $state<BrainsInfo | null>(null);
  let importBusy = $state(false);
  let setupOpen = $state(false);
  let flash = $state('');
  let flashKind = $state<'ok' | 'err'>('ok');

  const activeConn = $derived(conns.find((c) => c.id === activeConnId) ?? null);
  const sites = $derived(discovery?.items ?? []);

  // ---------- conversations ----------
  let convos = $state<Conversation[]>([]);
  let activeConvId = $state('');
  let activeConv = $state<Conversation | null>(null);
  let view = $state<'launcher' | 'thread' | 'schedule' | 'tasks'>('launcher');

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

  interface TaskForm {
    id: string | null; connId: string; connName: string; site: string; taskType: string; brain: string; model: string;
    title: string; prompt: string; period: string; firstRun: string; enabled: boolean;
  }
  let taskModalOpen = $state(false);
  let tf = $state<TaskForm>(freshTaskForm());
  function freshTaskForm(): TaskForm {
    return {
      id: null, connId: activeConnId, connName: activeConn?.name ?? '', site: sites[0]?.slug ?? '', taskType: 'article',
      brain: brainUsable('claude') ? 'claude' : brainUsable('codex') ? 'codex' : 'claude', model: 'sonnet',
      title: '', prompt: '', period: '1440', firstRun: '', enabled: true,
    };
  }
  function openNewTask() { tf = freshTaskForm(); taskModalOpen = true; }
  // AI 在对话里提议的定时任务 → 用当前会话的站点/模型预填，弹确认卡让用户确认/微调。
  function openTaskFromProposal(p: TaskProposal) {
    const c = activeConv;
    if (!c) return;
    const firstRunSecs = p.first_run && !isNaN(new Date(p.first_run).getTime()) ? Math.floor(new Date(p.first_run).getTime() / 1000) : 0;
    tf = {
      id: null, connId: c.conn_id, connName: c.conn_name, site: c.site_slug,
      taskType: c.task_type === 'free' ? 'free' : 'article', brain: c.brain, model: c.model || 'sonnet',
      title: p.title, prompt: p.prompt, period: String(p.every_minutes || 1440),
      firstRun: firstRunSecs ? toLocalInput(firstRunSecs) : '', enabled: true,
    };
    taskModalOpen = true;
  }
  function openEditTask(t: ScheduledTask) {
    // 编辑保留任务原本所属的连接，绝不改绑到当前活动连接。
    tf = {
      id: t.id, connId: t.conn_id, connName: t.conn_name, site: t.site_slug, taskType: t.task_type, brain: t.brain, model: t.model || 'sonnet',
      title: t.title, prompt: t.prompt, period: String(t.interval_minutes),
      firstRun: t.next_run ? toLocalInput(t.next_run) : '', enabled: t.enabled,
    };
    taskModalOpen = true;
  }
  // 站点选项：编辑跨连接任务时，活动连接的 discovery 里可能没有它的站点，
  // 补一个当前值兜底，保证原站点不被下拉清空。
  const taskSiteOpts = $derived.by(() => {
    const opts = tf.connId === activeConnId ? siteOpts : [];
    if (tf.site && !opts.some((o) => o.value === tf.site)) {
      return [{ value: tf.site, label: tf.site, sub: '当前' }, ...opts];
    }
    return opts;
  });
  function toLocalInput(secs: number): string {
    const d = new Date(secs * 1000); const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  }
  async function saveTask() {
    if (!tf.site || !tf.prompt.trim()) { say('请填写站点和指令', 'err'); return; }
    if (!brainUsable(tf.brain as Brain)) { say('所选模型未就绪，去设置里授权', 'err'); return; }
    // 新建用当前活动连接；编辑保留任务原连接。
    const connId = tf.connId || activeConnId;
    const site = sites.find((s) => s.slug === tf.site);
    const siteName = tf.connId === activeConnId ? (site?.name || tf.site) : (tf.site);
    const firstRun = tf.firstRun ? Math.floor(new Date(tf.firstRun).getTime() / 1000) : 0;
    try {
      await invoke('save_task', {
        id: tf.id, connId, siteSlug: tf.site, siteName,
        taskType: tf.taskType, brain: tf.brain, model: tf.brain === 'claude' ? tf.model : '',
        title: tf.title, prompt: tf.prompt, intervalMinutes: parseInt(tf.period) || 1440, firstRun, enabled: tf.enabled,
      });
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
  let prefs = loadPrefs();
  let lSite = $state('');
  let lTask = $state<TaskType>(prefs.taskType);
  let lBrain = $state<Brain>(prefs.brain);
  let lModel = $state(prefs.model);
  let lDraft = $state('');

  // ---------- composer / live turn ----------
  let draft = $state('');
  let busy = $state(false);
  let busyConvId = $state('');
  let live = $state<{ text: string; tools: ToolCall[]; error: string }>({ text: '', tools: [], error: '' });
  let threadEl = $state<HTMLDivElement | null>(null);

  const viewingBusy = $derived(busy && activeConvId === busyConvId);

  function startDrag(e: MouseEvent) { if (e.button === 0) getCurrentWindow().startDragging().catch(() => {}); }
  function say(m: string, k: 'ok' | 'err' = 'ok') { flash = m; flashKind = k; setTimeout(() => (flash = ''), k === 'err' ? 8000 : 4000); }
  function brainUsable(b: Brain): boolean { const s = b === 'claude' ? brains?.claude : brains?.codex; return !!s && s.found && s.logged_in !== false; }
  function hostOf(u: string): string { try { return new URL(u).host; } catch { return u; } }

  async function refreshConns() { try { conns = await invoke('list_connections'); if (!activeConnId && conns.length) selectConn(conns[0].id); } catch (e) { say(String(e), 'err'); } }
  async function refreshBrains() { try { brains = await invoke('detect_brains'); } catch (e) { say(String(e), 'err'); } }
  async function refreshConvos() { try { convos = await invoke('list_conversations'); } catch (e) { say(String(e), 'err'); } }

  let selSeq = 0;
  async function selectConn(id: string) {
    const seq = ++selSeq; activeConnId = id; discovery = null; discoveryLoading = true;
    try { const r = await invoke<Discovery>('discover_sites', { connId: id }); if (seq === selSeq) discovery = r; }
    catch (e) { if (seq === selSeq) say(String(e), 'err'); }
    finally { if (seq === selSeq) { discoveryLoading = false; if (!lSite && discovery?.items.length) lSite = discovery.items[0].slug; } }
  }

  $effect(() => { refreshConns(); refreshBrains(); refreshConvos(); });
  $effect(() => {
    const need = !!brains && ((brains.claude.found && brains.claude.logged_in === false) || (brains.codex.found && brains.codex.logged_in === false));
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
      keyOpen = false; say(`已导入「${o.connection.name}」`); await refreshConns(); await selectConn(o.connection.id);
    } catch (e) { if (keyOpen) keyErr = String(e); else say(String(e), 'err'); }
    finally { importBusy = false; }
  }
  async function confirmKey() {
    const k = keyVal.trim(); if (!k) return;
    if (!k.startsWith('gcmsp_') && !k.startsWith('gcms_')) { keyErr = '密钥前缀应为 gcmsp_ 或 gcms_'; return; }
    keyErr = ''; await doImport(keyZip, k);
  }
  async function removeConn(id: string) {
    const yes = await confirmDialog('删除这个连接？技能包目录与钥匙串里的密钥都会清除。', { title: '删除连接', kind: 'warning' });
    if (!yes) return;
    try { await invoke('remove_connection', { id }); if (activeConnId === id) { activeConnId = ''; discovery = null; } await refreshConns(); } catch (e) { say(String(e), 'err'); }
  }
  async function authorize(b: Brain) { try { await invoke('open_brain_login', { brain: b }); say('已打开终端，完成授权后自动刷新'); } catch (e) { say(String(e), 'err'); } }

  // ---------- 对话导航 ----------
  function newChat() {
    if (busy) return;
    view = 'launcher'; activeConvId = ''; activeConv = null; lDraft = '';
    if (!lSite && sites.length) lSite = sites[0].slug;
    if (!brainUsable(lBrain)) lBrain = brainUsable('claude') ? 'claude' : brainUsable('codex') ? 'codex' : 'claude';
  }
  async function openConv(id: string) {
    const c = await invoke<Conversation | null>('get_conversation', { id });
    if (!c) { await refreshConvos(); return; }
    activeConv = c; activeConvId = id; view = 'thread';
    scrollSoon(true);
  }
  async function deleteConv(id: string) {
    try { await invoke('delete_conversation', { id }); if (activeConvId === id) { activeConvId = ''; activeConv = null; view = 'launcher'; } await refreshConvos(); } catch (e) { say(String(e), 'err'); }
  }

  // ---------- 运行一轮 ----------
  function makeChannel(): Channel<TurnEvent> {
    const ch = new Channel<TurnEvent>();
    ch.onmessage = (ev) => {
      if (ev.type === 'delta') { live.text += ev.text; scrollSoon(); }
      else if (ev.type === 'tool') { live.tools = [...live.tools, { label: ev.label, detail: ev.detail }]; scrollSoon(); }
      else if (ev.type === 'done') { if (!ev.ok) live.error = ev.error; }
    };
    return ch;
  }
  function optimisticUser(text: string): Message {
    return { role: 'user', text, tools: [], ts: Math.floor(Date.now() / 1000), hidden: false, error: false };
  }
  // 乐观：立刻把用户消息塞进当前/合成会话，activeConvId 立即等于 convId，
  // 首轮也能流式渲染 + 停止（cancel 键对准真正的注册表 key）。
  function beginTurn(convId: string, optimistic: Conversation) {
    activeConv = optimistic; activeConvId = convId;
    busy = true; busyConvId = convId; live = { text: '', tools: [], error: '' };
    view = 'thread'; scrollSoon(true);
  }
  function endTurn(conv: Conversation | null) {
    // 仅当用户仍停留在这条会话时用权威结果覆盖，避免打断已切走的用户。
    if (conv && activeConvId === busyConvId) { activeConv = conv; activeConvId = conv.id; }
    busy = false; busyConvId = ''; live = { text: '', tools: [], error: '' };
  }
  async function failTurn(e: unknown, convId: string) {
    busy = false; busyConvId = ''; live = { text: '', tools: [], error: '' };
    say(String(e), 'err');
    await refreshConvos();
    const reloaded = await invoke<Conversation | null>('get_conversation', { id: convId });
    if (reloaded) { if (activeConvId === convId) activeConv = reloaded; }
    else if (activeConvId === convId) { activeConv = null; view = 'launcher'; }
  }

  async function startChat() {
    if (busy || !lSite || !lDraft.trim() || !brainUsable(lBrain)) return;
    const site = sites.find((s) => s.slug === lSite);
    prefs = { brain: lBrain, model: lModel, taskType: lTask }; savePrefs(prefs);
    const text = lDraft.trim();
    const id = crypto.randomUUID();
    const now = Math.floor(Date.now() / 1000);
    beginTurn(id, {
      id, conn_id: activeConnId, conn_name: activeConn?.name ?? '', site_slug: lSite, site_name: site?.name || lSite,
      task_type: lTask, brain: lBrain, model: lBrain === 'claude' ? lModel : '', session_ref: '',
      title: text.slice(0, 30), messages: [optimisticUser(text)], status: 'running', created_at: now, updated_at: now,
    });
    lDraft = '';
    try {
      const conv = await invoke<Conversation>('start_conversation', {
        convId: id, connId: activeConnId, siteSlug: lSite, siteName: site?.name || lSite,
        taskType: lTask, brain: lBrain, model: lBrain === 'claude' ? lModel : '',
        message: text, onEvent: makeChannel(),
      });
      await refreshConvos(); endTurn(conv);
    } catch (e) { await failTurn(e, id); }
  }

  async function send() {
    if (busy || !activeConv || !draft.trim()) return;
    const text = draft.trim(); draft = '';
    const id = activeConv.id;
    beginTurn(id, { ...activeConv, messages: [...activeConv.messages, optimisticUser(text)], status: 'running' });
    try {
      const conv = await invoke<Conversation>('send_message', { convId: id, message: text, onEvent: makeChannel() });
      await refreshConvos(); endTurn(conv);
    } catch (e) { await failTurn(e, id); }
  }

  async function stop() { if (busyConvId) { try { await invoke('cancel_turn', { convId: busyConvId }); } catch { /* */ } } }

  function scrollSoon(force = false) {
    requestAnimationFrame(() => {
      if (!threadEl) return;
      const near = threadEl.scrollHeight - threadEl.scrollTop - threadEl.clientHeight < 160;
      if (force || near) threadEl.scrollTop = threadEl.scrollHeight;
    });
  }
  function onComposerKey(e: KeyboardEvent, fn: () => void) {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); fn(); }
  }

  // linkify + 段落
  const urlRe = /(https?:\/\/[^\s)"'」』】>，。；：！？、（）《》]+)/g;
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

  function taskLabel(t: string): string { return t === 'sitebuild' ? '新站建设' : t === 'article' ? '内容创作' : '自由对话'; }
  function brainLabel(b: string): string { return b === 'codex' ? 'Codex' : 'Claude'; }
  function fmt(secs: number): string { return new Date(secs * 1000).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }); }
  function fmtSched(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString('zh-CN', { weekday: 'short', month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }
  const schedGroups = $derived.by(() => {
    const now = Date.now();
    const t0 = new Date(); t0.setHours(0, 0, 0, 0); const day = t0.getTime();
    const g: { label: string; items: ScheduledItem[] }[] = [
      { label: '待发布', items: [] }, { label: '今天', items: [] }, { label: '明天', items: [] },
      { label: '本周内', items: [] }, { label: '更晚', items: [] },
    ];
    for (const it of sched) {
      const ms = new Date(it.published_at).getTime();
      if (isNaN(ms)) { g[0].items.push(it); continue; }
      if (ms < now) g[0].items.push(it);
      else if (ms < day + 864e5) g[1].items.push(it);
      else if (ms < day + 2 * 864e5) g[2].items.push(it);
      else if (ms < day + 7 * 864e5) g[3].items.push(it);
      else g[4].items.push(it);
    }
    return g.filter((x) => x.items.length);
  });

  const shownMessages = $derived((activeConv?.messages ?? []).filter((m) => !m.hidden));

  // 下拉选项
  const siteOpts = $derived(sites.map((s) => ({ value: s.slug, label: s.name || s.slug, sub: s.slug, img: s.logo || '' })));
  const brainOpts = $derived([
    { value: 'claude', label: 'Claude', icon: 'claude', disabled: !brainUsable('claude'), sub: brainUsable('claude') ? '' : brains?.claude.found ? '未登录' : '未安装' },
    { value: 'codex', label: 'OpenAI Codex', icon: 'codex', disabled: !brainUsable('codex'), sub: brainUsable('codex') ? '' : brains?.codex.found ? '未登录' : '未安装' },
  ]);
  const modelOpts = [
    { value: 'sonnet', label: 'Sonnet', sub: '性价比之选' },
    { value: 'opus', label: 'Opus', sub: '最强' },
    { value: 'haiku', label: 'Haiku', sub: '最快' },
  ];

  // 会话按日期分组（后端已按 updated_at 倒序）
  const grouped = $derived.by(() => {
    const t0 = new Date(); t0.setHours(0, 0, 0, 0);
    const day = t0.getTime();
    const groups = [
      { label: '今天', items: [] as Conversation[] },
      { label: '昨天', items: [] as Conversation[] },
      { label: '过去 7 天', items: [] as Conversation[] },
      { label: '更早', items: [] as Conversation[] },
    ];
    for (const c of convos) {
      const ms = c.updated_at * 1000;
      if (ms >= day) groups[0].items.push(c);
      else if (ms >= day - 864e5) groups[1].items.push(c);
      else if (ms >= day - 7 * 864e5) groups[2].items.push(c);
      else groups[3].items.push(c);
    }
    return groups.filter((g) => g.items.length);
  });
</script>

<main class="app">
  <!-- 融合式标题栏：透明拖拽条，红绿灯浮在内容上（macOS Overlay） -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="titlebar" data-tauri-drag-region aria-hidden="true" onmousedown={startDrag}></div>

  <!-- 左栏 -->
  <aside class="rail">
    <div class="rail-head">
      <button class="newchat" onclick={newChat} disabled={busy || !activeConn} title="新对话">
        <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
          <path d="M11.5 2.5l2 2L6 12l-2.5.5L4 10l7.5-7.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
          <path d="M2.5 13.5h11" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
        </svg>
        新对话
      </button>
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
    </div>

    <div class="convos">
      {#if convos.length === 0}
        <p class="rail-empty">还没有对话。<br />选好站点和模型，直接说你想做什么。</p>
      {/if}
      {#each grouped as g (g.label)}
        <div class="grp">{g.label}</div>
        {#each g.items as c (c.id)}
          <div class="convo {activeConvId === c.id ? 'on' : ''}" role="button" tabindex="0"
            onclick={() => openConv(c.id)} onkeydown={(e) => e.key === 'Enter' && openConv(c.id)}>
            <div class="convo-body">
              <span class="convo-title">{c.title}</span>
              <span class="convo-meta"><span class="cmono">{c.site_slug}</span><span class="cdot">·</span>{@render brainTag(c.brain, brainLabel(c.brain))}{#if c.status === 'running'}<span class="mini-run"></span>{/if}</span>
            </div>
            <button class="convo-x" title="删除对话" onclick={(e) => { e.stopPropagation(); deleteConv(c.id); }}>×</button>
          </div>
        {/each}
      {/each}
    </div>

    <button class="rail-foot" onclick={() => (setupOpen = true)}>
      <SiteMark size={20} />
      <span class="foot-main">
        <b>{activeConn?.name ?? '未连接'}</b>
        <small>{activeConn ? `${sites.length} 个站点` : '点此导入技能包'}</small>
      </span>
      <svg class="foot-gear" width="16" height="16" viewBox="0 0 16 16" fill="none">
        <path d="M2 5h5.2M10.8 5H14" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
        <path d="M2 11h2.8M8.4 11H14" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
        <circle cx="9" cy="5" r="1.7" stroke="currentColor" stroke-width="1.3" />
        <circle cx="6.6" cy="11" r="1.7" stroke="currentColor" stroke-width="1.3" />
      </svg>
    </button>
  </aside>

  <!-- 主区 -->
  <section class="main">
    {#if flash}<div class="flash {flashKind}">{flash}</div>{/if}

    {#if !activeConn}
      <div class="center">
        <div class="hero-card">
          <div class="hero-mark">✦</div>
          <h1>开始之前，先导入技能包</h1>
          <p>在 gcms 后台「平台密钥」页下载技能包 zip，导入后即可用本地 Claude / Codex 为你的站点干活。</p>
          <button class="btn primary lg" onclick={importPack} disabled={importBusy}>{importBusy ? '导入中…' : '导入技能包'}</button>
        </div>
      </div>

    {:else if view === 'launcher'}
      <div class="center">
        <div class="launcher">
          <h1>想让它帮你做点什么？</h1>
          <p class="sub">选好站点和模型，像聊天一样把需求说清楚，它会边聊边把事情做掉。</p>

          <div class="task-seg">
            {#each [['article', '内容创作', '策划并撰写文章'], ['sitebuild', '新站建设', '从零搭建整个站点'], ['free', '自由对话', '任意内容运营']] as t (t[0])}
              <button class:on={lTask === t[0]} onclick={() => (lTask = t[0] as TaskType)}>
                <b>{t[1]}</b><small>{t[2]}</small>
              </button>
            {/each}
          </div>

          <div class="launch-row">
            <div class="pick"><span>站点</span><Dropdown bind:value={lSite} options={siteOpts} placeholder="选择站点" /></div>
            <div class="pick"><span>模型</span><Dropdown bind:value={lBrain} options={brainOpts} /></div>
            {#if lBrain === 'claude'}
              <div class="pick"><span>档位</span><Dropdown bind:value={lModel} options={modelOpts} /></div>
            {/if}
          </div>

          <div class="composer big">
            <textarea bind:value={lDraft} rows="3"
              placeholder={lTask === 'sitebuild' ? '例如：帮我搭一个介绍露营装备的中文站，风格轻松，先给我一个方案' : '例如：帮我写一篇 2026 年 macOS 效率工具盘点，先列个提纲'}
              onkeydown={(e) => onComposerKey(e, startChat)}></textarea>
            <button class="send" onclick={startChat} disabled={!lSite || !lDraft.trim() || !brainUsable(lBrain)} title="发送（Enter）">↑</button>
          </div>
          {#if !brainUsable(lBrain)}<p class="hint warn-text">所选模型未就绪，点左下角设置里「去授权」。</p>{/if}
        </div>
      </div>

    {:else if view === 'schedule'}
      <header class="thread-head">
        <div class="th-info"><b>排期</b><small>各站点待定时发布的内容 · 由 gcms 服务端到点自动发布</small></div>
        <button class="btn ghost small" onclick={loadScheduled} disabled={schedLoading}>{schedLoading ? '刷新中…' : '刷新'}</button>
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
            {#each schedGroups as g (g.label)}
              <div class="grp sched-grp">{g.label}</div>
              {#each g.items as it (it.site_slug + '-' + it.id)}
                <div class="sched-item">
                  <div class="sched-time">{fmtSched(it.published_at)}</div>
                  <div class="sched-body">
                    <b>{it.title}</b>
                    <small><span class="cmono">{it.site_slug}</span> · {it.lang}</small>
                  </div>
                  {#if it.url}<button class="link sched-open" onclick={() => openUrl(it.url)}>打开 ↗</button>{/if}
                </div>
              {/each}
            {/each}
          {/if}
        </div>
      </div>

    {:else if view === 'tasks'}
      <header class="thread-head">
        <div class="th-info"><b>定时任务</b><small>到点自动开一个新对话执行 · 需保持 Pilot 在后台（托盘）运行</small></div>
        <button class="btn primary small" onclick={openNewTask}>＋ 新建任务</button>
      </header>
      <div class="thread">
        <div class="sched-inner">
          {#if tasks.length === 0}
            <div class="sched-empty">
              <div class="cal-mark">⏰</div>
              <b>还没有定时任务</b>
              <p>建一个让它按时自动干活，比如「每天早上 9 点，围绕本周热点写一篇文章存草稿」。</p>
              <button class="btn primary" onclick={openNewTask} style="margin-top:14px">＋ 新建任务</button>
            </div>
          {:else}
            {#each tasks as t (t.id)}
              <div class="task-card {t.enabled ? '' : 'off'}">
                <div class="task-toggle">
                  <button class="switch {t.enabled ? 'on' : ''}" title={t.enabled ? '已启用' : '已暂停'} onclick={() => toggleTask(t)}><span></span></button>
                </div>
                <div class="task-body">
                  <b>{t.title}</b>
                  <div class="task-meta">
                    <span class="cmono">{t.site_slug}</span> · {@render brainTag(t.brain, brainLabel(t.brain))} · {periodLabel(t.interval_minutes)}
                    {#if t.enabled}· 下次 {fmtSched(new Date(t.next_run * 1000).toISOString())}{/if}
                  </div>
                  {#if t.last_run}
                    <div class="task-last {t.last_status}">
                      上次 {fmt(t.last_run)} · {t.last_status === 'ok' ? '成功' : '失败'}{#if t.last_summary}：{t.last_summary}{/if}
                      {#if t.last_conv_id}<button class="link" onclick={() => openConv(t.last_conv_id)}>查看对话</button>{/if}
                    </div>
                  {/if}
                </div>
                <div class="task-actions">
                  <button class="btn small ghost" onclick={() => runTaskNow(t)}>立即运行</button>
                  <button class="btn small ghost" onclick={() => openEditTask(t)}>编辑</button>
                  <button class="x sm" title="删除" onclick={() => deleteTask(t)}>×</button>
                </div>
              </div>
            {/each}
          {/if}
        </div>
      </div>

    {:else}
      <!-- 对话线程 -->
      <header class="thread-head">
        <div class="th-info">
          <b>{activeConv?.title}</b>
          <small>{activeConv?.site_name || activeConv?.site_slug} · {taskLabel(activeConv?.task_type ?? '')} · {@render brainTag(activeConv?.brain ?? 'claude', brainLabel(activeConv?.brain ?? '') + (activeConv?.brain === 'claude' && activeConv?.model ? ` ${activeConv.model}` : ''))}</small>
        </div>
      </header>

      <div class="thread" bind:this={threadEl}>
        <div class="thread-inner">
          {#each shownMessages as m, i (i)}
            {@render bubble(m)}
          {/each}
          {#if viewingBusy}
            <div class="msg assistant">
              <div class="body">
                {#if live.tools.length}{@render cmds(live.tools)}{/if}
                {#if live.text}<div class="text">{@render richText(live.text)}</div>{/if}
                {#if live.error}<div class="err-note">{live.error}</div>
                {:else}<div class="typing"><span></span><span></span><span></span></div>{/if}
              </div>
            </div>
          {/if}
        </div>
      </div>

      <div class="composer-wrap">
        <div class="composer">
          <textarea bind:value={draft} rows="1" placeholder="继续说…（Enter 发送，Shift+Enter 换行）"
            disabled={busy} onkeydown={(e) => onComposerKey(e, send)}></textarea>
          {#if busy && viewingBusy}
            <button class="send stop" onclick={stop} title="停止">■</button>
          {:else}
            <button class="send" onclick={send} disabled={busy || !draft.trim()} title="发送（Enter）">↑</button>
          {/if}
        </div>
      </div>
    {/if}
  </section>
</main>

<!-- 消息气泡片段 -->
{#snippet bubble(m: Message)}
  {#if m.role === 'user'}
    <div class="msg user"><div class="ubody">{@render richText(m.text)}</div></div>
  {:else}
    <div class="msg assistant">
      <div class="body">
        {#if m.tools.length}{@render cmds(m.tools)}{/if}
        <div class="text {m.error ? 'is-err' : ''}">{@render richText(m.text)}</div>
        {#if m.proposal}
          <div class="proposal">
            <div class="proposal-head">⏰ AI 建议一个定时任务</div>
            <b class="proposal-title">{m.proposal.title}</b>
            <div class="proposal-meta">{periodLabel(m.proposal.every_minutes)}{#if m.proposal.first_run} · 首次 {fmtSched(m.proposal.first_run)}{/if}</div>
            <div class="proposal-prompt">{m.proposal.prompt}</div>
            <button class="btn primary small" onclick={() => m.proposal && openTaskFromProposal(m.proposal)}>创建定时任务…</button>
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
      <div class="sec-head"><span>连接</span><button class="btn small" onclick={importPack} disabled={importBusy}>{importBusy ? '导入中…' : '＋导入技能包'}</button></div>
      {#if conns.length === 0}<p class="hint">还没有连接。导入 gcms 平台技能包 zip。</p>{/if}
      {#each conns as c (c.id)}
        <label class="conn-row {activeConnId === c.id ? 'on' : ''}">
          <input type="radio" name="conn" checked={activeConnId === c.id} onchange={() => { selectConn(c.id); }} />
          <span class="conn-main"><b>{c.name}</b><small>{c.key_prefix} · {c.key_kind === 'gcmsp_' ? '平台' : '单站'}</small></span>
          <button class="x sm" onclick={(e) => { e.preventDefault(); removeConn(c.id); }}>×</button>
        </label>
      {/each}

      <div class="sec-head mt"><span>本地模型</span><button class="btn small ghost" onclick={refreshBrains}>刷新</button></div>
      {#if brains}
        {#each [{ b: 'claude' as Brain, st: brains.claude, name: 'Claude Code', cmd: 'npm i -g @anthropic-ai/claude-code' }, { b: 'codex' as Brain, st: brains.codex, name: 'OpenAI Codex', cmd: 'npm i -g @openai/codex' }] as r (r.b)}
          <div class="brain-row">
            <span class="dot {r.st.found && r.st.logged_in ? 'ok' : r.st.found ? 'warn' : 'off'}"></span>
            <BrainIcon brain={r.b} size={17} />
            <span class="brain-main"><b>{r.name}</b>
              <small>{#if !r.st.found}未安装{:else if r.st.logged_in === false}未登录{:else}{r.st.version || '已就绪'}{/if}</small></span>
            {#if r.st.found && r.st.logged_in === false}<button class="btn small primary" onclick={() => authorize(r.b)}>去授权</button>{/if}
          </div>
          {#if !r.st.found}<p class="hint mono">安装：{r.cmd}</p>{/if}
        {/each}
      {/if}
      <p class="hint tos">仅限本人订阅账户驱动本地官方 CLI；密钥保存在 macOS 钥匙串。</p>
    </div>
  </div>
{/if}

<!-- 密钥输入 -->
{#if keyOpen}
  <div class="mask" role="presentation" onclick={() => !importBusy && (keyOpen = false)}></div>
  <div class="modal">
    <header class="sheet-head"><div><b>原始技能包 · 需要密钥</b><small class="dim">{keyBase}</small></div><button class="x" onclick={() => (keyOpen = false)} disabled={importBusy}>×</button></header>
    <div class="sheet-body">
      <p class="hint">粘贴 gcms 后台生成的密钥（gcmsp_…），只会存入 macOS 钥匙串，不写进任何文件。</p>
      <input class="tin" bind:value={keyVal} type="password" placeholder="gcmsp_…" autocomplete="off" disabled={importBusy} onkeydown={(e) => e.key === 'Enter' && confirmKey()} />
      {#if keyErr}<div class="err-note">{keyErr}</div>{/if}
      <div class="row-end">
        <button class="btn ghost" onclick={() => (keyOpen = false)} disabled={importBusy}>取消</button>
        <button class="btn primary" onclick={confirmKey} disabled={importBusy || !keyVal.trim()}>{importBusy ? '导入中…' : '导入'}</button>
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
      <div class="trow">
        <div class="tfield"><span>站点</span><Dropdown bind:value={tf.site} options={taskSiteOpts} placeholder="选择站点" /></div>
        <div class="tfield"><span>类型</span><Dropdown bind:value={tf.taskType} options={taskTypeOpts} /></div>
      </div>
      <div class="trow">
        <div class="tfield"><span>模型</span><Dropdown bind:value={tf.brain} options={brainOpts} /></div>
        {#if tf.brain === 'claude'}<div class="tfield"><span>档位</span><Dropdown bind:value={tf.model} options={modelOpts} /></div>{/if}
      </div>
      <div class="tfield"><span>任务名称（可选）</span><input class="tin" bind:value={tf.title} placeholder="例如：每日热点速写" /></div>
      <div class="tfield"><span>指令（每次到点就把这句话发给模型）</span>
        <textarea bind:value={tf.prompt} rows="3" placeholder="例如：围绕本周科技热点写一篇 800 字左右的中文文章，存草稿，完成后给我预览链接"></textarea></div>
      <div class="trow">
        <div class="tfield"><span>周期</span><Dropdown bind:value={tf.period} options={periodOpts} /></div>
        <div class="tfield"><span>首次运行（可选，留空则一个周期后）</span><input class="tin" type="datetime-local" bind:value={tf.firstRun} /></div>
      </div>
      <label class="tcheck"><input type="checkbox" bind:checked={tf.enabled} /><span>创建后立即启用</span></label>
      <div class="row-end">
        <button class="btn ghost" onclick={() => (taskModalOpen = false)}>取消</button>
        <button class="btn primary" onclick={saveTask} disabled={!tf.site || !tf.prompt.trim()}>{tf.id ? '保存' : '创建'}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  :global(:root) {
    --bg: #ffffff; --rail: #faf9f7; --card: #ffffff;
    --border: #ecebe6; --border2: #e1dfd8;
    --text: #26241f; --dim: #6f6b62; --faint: #a29d93;
    --accent: #4f46e5; --accent-h: #4338ca; --accent-soft: #eef0fe;
    --user-bg: #f3f1ec;
    --ok: #12805c; --warn: #b45309; --err: #dc2626;
    --err-soft: #fef2f2; --err-border: #f4cccc;
    --shadow-sm: 0 1px 2px rgba(30,25,15,.05);
    --shadow: 0 4px 16px rgba(30,25,15,.08);
    --shadow-lg: 0 16px 48px rgba(30,25,15,.16);
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

  /* 融合标题栏：全宽透明拖拽条，红绿灯浮在其上，两列各自的底色透出来 */
  .titlebar { position: fixed; top: 0; left: 0; right: 0; height: 30px; z-index: 6; }

  /* ---- 左栏 ---- */
  .rail { width: 260px; flex: none; display: flex; flex-direction: column; background: var(--rail); border-right: 1px solid var(--border); padding-top: 30px; }
  .rail-head { padding: 8px 12px 10px; display: flex; flex-direction: column; gap: 3px; }
  .newchat { display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 12px;
    background: none; color: var(--text); border: none; border-radius: 9px; font-size: 13.5px; font-weight: 550; cursor: pointer; text-align: left; }
  .newchat:hover { background: #f1efe9; }
  .newchat:disabled { opacity: .5; cursor: default; }
  .newchat svg { flex: none; color: var(--accent); }
  .railnav { display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 12px; background: none;
    border: none; border-radius: 9px; font-size: 13px; color: var(--dim); cursor: pointer; text-align: left; margin-top: -4px; }
  .railnav:hover { background: #f1efe9; color: var(--text); }
  .railnav.on { background: #eae7ff; color: var(--accent); font-weight: 550; }
  .railnav:disabled { opacity: .45; cursor: default; }
  .railnav svg { flex: none; }

  .convos { flex: 1; overflow-y: auto; padding: 4px 8px 8px; display: flex; flex-direction: column; gap: 1px; }
  .rail-empty { color: var(--faint); font-size: 12px; padding: 10px 8px; line-height: 1.7; }
  .grp { font-size: 10.5px; font-weight: 600; letter-spacing: .04em; color: var(--faint); padding: 12px 8px 4px; text-transform: uppercase; }
  .grp:first-child { padding-top: 4px; }
  .convo { position: relative; display: flex; align-items: center; gap: 6px; border-radius: 8px; padding: 7px 9px; cursor: pointer; }
  .convo:hover { background: #f1efe9; }
  .convo.on { background: #e9e7e0; }
  .convo-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .convo-title { font-size: 13px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .convo-meta { font-size: 11px; color: var(--dim); display: flex; align-items: center; gap: 4px; white-space: nowrap; overflow: hidden; }
  .cmono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 10.5px; color: var(--faint); overflow: hidden; text-overflow: ellipsis; }
  .cdot { color: var(--faint); }
  .mini-run { width: 6px; height: 6px; border-radius: 50%; background: var(--accent); animation: pulse 1.1s infinite; flex: none; }
  .convo-x { background: none; border: none; color: var(--faint); font-size: 16px; line-height: 1; opacity: 0; padding: 1px 4px; border-radius: 5px; cursor: pointer; flex: none; }
  .convo:hover .convo-x { opacity: 1; }
  .convo-x:hover { color: var(--err); background: #fff; }

  .rail-foot { display: flex; align-items: center; gap: 9px; padding: 10px 12px; border: none; border-top: 1px solid var(--border); background: none; cursor: pointer; text-align: left; -webkit-appearance: none; appearance: none; box-shadow: none; }
  .rail-foot:focus, .rail-foot:active { outline: none; box-shadow: none; }
  .rail-foot:hover { background: #f1efe9; }
  .foot-dots { display: flex; gap: 3px; }
  .foot-main { flex: 1; min-width: 0; }
  .foot-main b { display: block; font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .foot-main small { color: var(--dim); font-size: 11px; }
  .foot-gear { color: var(--faint); font-size: 14px; }
  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
  .dot.ok { background: #16a34a; } .dot.warn { background: #d97706; } .dot.off { background: #cfccc4; }

  /* ---- 主区 ---- */
  .main { flex: 1; position: relative; display: flex; flex-direction: column; min-width: 0; padding-top: 30px; }
  .flash { position: absolute; top: 40px; left: 50%; transform: translateX(-50%); z-index: 40; background: #14231a; color: #fff; padding: 9px 16px; border-radius: 10px; font-size: 13px; box-shadow: var(--shadow); max-width: 70%; }
  .flash.err { background: var(--err); }

  .center { flex: 1; display: flex; align-items: center; justify-content: center; overflow-y: auto; padding: 24px; }
  .hero-card { text-align: center; max-width: 420px; }
  .hero-mark { font-size: 40px; color: var(--accent); }
  .hero-card h1 { font-size: 22px; margin: 12px 0 8px; }
  .hero-card p { color: var(--dim); margin: 0 0 20px; }

  .launcher { width: min(680px, 100%); }
  .launcher h1 { font-size: 26px; margin: 0 0 6px; letter-spacing: -.01em; }
  .launcher .sub { color: var(--dim); margin: 0 0 22px; }
  .task-seg { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; margin-bottom: 16px; }
  .task-seg button { text-align: left; background: var(--card); border: 1.5px solid var(--border); border-radius: 12px; padding: 12px 14px; cursor: pointer; display: flex; flex-direction: column; gap: 2px; transition: border-color .12s, background .12s; }
  .task-seg button:hover { border-color: var(--border2); }
  .task-seg button.on { border-color: var(--accent); background: var(--accent-soft); }
  .task-seg b { font-size: 14px; }
  .task-seg small { color: var(--dim); font-size: 12px; }

  .launch-row { display: flex; gap: 12px; margin-bottom: 14px; flex-wrap: wrap; }
  .pick { display: flex; flex-direction: column; gap: 5px; flex: 1; min-width: 140px; }
  .pick > span { font-size: 12px; color: var(--dim); }
  .tin, textarea { font-family: inherit; font-size: 14px; color: var(--text); background: #fff; border: 1.5px solid var(--border2); border-radius: 10px; padding: 9px 11px; }
  .tin:focus, textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }

  /* 输入框（仿 Claude Code：整块圆角边框，聚焦时高亮，发送按钮嵌在框内） */
  .composer.big, .composer-wrap .composer { position: relative; background: #fff; border: 1px solid var(--border); border-radius: 22px; box-shadow: none; transition: border-color .12s, box-shadow .12s; }
  .composer.big:focus-within, .composer-wrap .composer:focus-within { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
  .composer.big textarea, .composer-wrap textarea { width: 100%; resize: none; border: none; background: none; box-shadow: none; padding: 14px 52px 14px 17px; line-height: 1.6; max-height: 200px; display: block; }
  .composer.big textarea:focus, .composer-wrap textarea:focus { outline: none; box-shadow: none; border: none; }
  .composer .send { position: absolute; right: 9px; bottom: 9px; width: 32px; height: 32px; border-radius: 50%; border: none; background: var(--accent); color: #fff; font-size: 16px; cursor: pointer; display: flex; align-items: center; justify-content: center; transition: background .12s, transform .08s; }
  .composer .send:hover { background: var(--accent-h); }
  .composer .send:active { transform: scale(.92); }
  .composer .send:disabled { background: #dcdad2; cursor: default; transform: none; }
  .composer .send.stop { background: var(--text); }

  /* ---- 线程 ---- */
  .thread-head { flex: none; padding: 11px 24px; border-bottom: 1px solid var(--border); display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .th-info b { display: block; font-size: 15px; line-height: 1.35; }
  .th-info small { display: block; color: var(--dim); font-size: 12px; line-height: 1.45; margin-top: 1px; }
  .btag { display: inline-flex; align-items: center; gap: 4px; }
  .thread { flex: 1; overflow-y: auto; }
  .thread-inner { max-width: 760px; margin: 0 auto; padding: 22px 24px 8px; display: flex; flex-direction: column; gap: 20px; }

  .msg { display: flex; gap: 12px; }
  .msg.user { justify-content: flex-end; }
  .ubody { background: var(--user-bg); border-radius: 16px 16px 5px 16px; padding: 10px 14px; max-width: 78%; white-space: pre-wrap; word-break: break-word; }
  .msg.assistant .body { flex: 1; min-width: 0; }
  .text { white-space: pre-wrap; word-break: break-word; }
  .text.is-err { color: var(--err); }
  /* 命令列表：默认收起，点击展开 */
  .cmds { margin-bottom: 9px; }
  .cmds summary { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; list-style: none; width: fit-content;
    font-size: 12px; color: var(--dim); background: var(--panel); border: 1px solid var(--border); border-radius: 8px; padding: 4px 10px; user-select: none; }
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
  .typing { display: flex; gap: 4px; padding: 4px 0; }
  .typing span { width: 6px; height: 6px; border-radius: 50%; background: var(--faint); animation: bounce 1.2s infinite; }
  .typing span:nth-child(2) { animation-delay: .15s; } .typing span:nth-child(3) { animation-delay: .3s; }
  @keyframes bounce { 0%, 60%, 100% { opacity: .3; transform: translateY(0); } 30% { opacity: 1; transform: translateY(-3px); } }
  @keyframes pulse { 50% { opacity: .35; } }

  /* 排期视图 */
  .sched-inner { max-width: 720px; margin: 0 auto; padding: 18px 24px 24px; }
  .sched-grp { padding: 16px 2px 6px; }
  .sched-grp:first-child { padding-top: 4px; }
  .sched-item { display: flex; align-items: center; gap: 14px; padding: 11px 14px; background: var(--card);
    border: 1px solid var(--border); border-radius: 11px; margin-bottom: 8px; box-shadow: var(--shadow-sm); }
  .sched-time { flex: none; width: 128px; font-size: 12.5px; color: var(--accent); font-weight: 550;
    font-variant-numeric: tabular-nums; }
  .sched-body { flex: 1; min-width: 0; }
  .sched-body b { display: block; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sched-body small { color: var(--dim); font-size: 11.5px; }
  .sched-open { flex: none; text-decoration: none; font-size: 12px; }
  .sched-err { max-width: 720px; margin: 18px auto; }
  .center-hint { text-align: center; color: var(--dim); padding: 40px 0; }
  .sched-empty { text-align: center; color: var(--dim); padding: 12vh 24px; }
  .sched-empty .cal-mark { font-size: 34px; }
  .sched-empty b { display: block; margin: 12px 0 6px; color: var(--text); font-size: 16px; }
  .sched-empty p { margin: 0 auto; max-width: 380px; font-size: 13px; }

  /* AI 提议的定时任务卡 */
  .proposal { margin-top: 10px; border: 1px solid #d9d5f0; background: #f6f5ff; border-radius: 12px; padding: 12px 14px; display: flex; flex-direction: column; gap: 5px; align-items: flex-start; }
  .proposal-head { font-size: 12px; color: var(--accent); font-weight: 600; }
  .proposal-title { font-size: 14px; }
  .proposal-meta { font-size: 12px; color: var(--dim); }
  .proposal-prompt { font-size: 12.5px; color: var(--text); background: #fff; border: 1px solid var(--border); border-radius: 8px; padding: 7px 9px; width: 100%; white-space: pre-wrap; word-break: break-word; }
  .proposal .btn { margin-top: 3px; }

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
  .task-meta { font-size: 12px; color: var(--dim); margin-top: 2px; }
  .task-last { font-size: 12px; margin-top: 5px; color: var(--dim); display: flex; gap: 6px; flex-wrap: wrap; align-items: baseline; }
  .task-last.ok { color: var(--ok); } .task-last.error { color: var(--err); }
  .task-actions { display: flex; align-items: center; gap: 4px; flex: none; }

  .modal.wide { width: min(520px, 94vw); }
  .trow { display: flex; gap: 12px; }
  .tfield { display: flex; flex-direction: column; gap: 5px; flex: 1; min-width: 0; }
  .tfield > span { font-size: 12px; color: var(--dim); }
  .tcheck { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
  .tcheck input { width: auto; }

  .composer-wrap { flex: none; padding: 10px 24px 20px; }
  .composer-wrap .composer { max-width: 760px; margin: 0 auto; }

  /* ---- 按钮 / 弹窗 ---- */
  .btn { background: #fff; color: var(--text); border: 1.5px solid var(--border2); border-radius: 9px; padding: 7px 14px; cursor: pointer; font-size: 13px; }
  .btn:hover { background: #f6f5f1; } .btn:disabled { opacity: .5; cursor: default; }
  .btn.primary { background: var(--accent); border-color: var(--accent); color: #fff; } .btn.primary:hover { background: var(--accent-h); }
  .btn.ghost { border-color: var(--border); }
  .btn.small { padding: 4px 10px; font-size: 12px; }
  .btn.lg { padding: 10px 22px; font-size: 15px; }
  .x { background: none; border: none; color: var(--faint); font-size: 20px; cursor: pointer; line-height: 1; }
  .x:hover { color: var(--err); } .x.sm { font-size: 15px; }

  .mask { position: fixed; inset: 0; background: rgba(25,20,10,.28); z-index: 50; }
  .sheet, .modal { position: fixed; z-index: 60; background: var(--bg); border: 1px solid var(--border); box-shadow: var(--shadow-lg); display: flex; flex-direction: column; }
  .sheet { top: 0; right: 0; bottom: 0; width: min(400px, 92vw); border-radius: 0; }
  .modal { top: 50%; left: 50%; transform: translate(-50%, -50%); width: min(440px, 92vw); border-radius: 14px; overflow: hidden; }
  .sheet-head { display: flex; justify-content: space-between; align-items: center; padding: 15px 18px; border-bottom: 1px solid var(--border); }
  .sheet-body { padding: 16px 18px; overflow-y: auto; display: flex; flex-direction: column; gap: 8px; }
  .sec-head { display: flex; justify-content: space-between; align-items: center; font-size: 12px; color: var(--dim); font-weight: 600; }
  .sec-head.mt { margin-top: 14px; }
  .conn-row { display: flex; align-items: center; gap: 10px; padding: 9px 10px; border: 1.5px solid var(--border); border-radius: 10px; cursor: pointer; }
  .conn-row.on { border-color: var(--accent); background: var(--accent-soft); }
  .conn-main { flex: 1; min-width: 0; } .conn-main b { display: block; font-size: 13.5px; } .conn-main small { color: var(--dim); font-size: 11px; }
  .brain-row { display: flex; align-items: center; gap: 9px; padding: 6px 2px; }
  .brain-main { flex: 1; } .brain-main b { display: block; font-size: 13.5px; } .brain-main small { color: var(--dim); font-size: 11px; }
  .hint { color: var(--dim); font-size: 12px; margin: 2px 0; line-height: 1.6; }
  .hint.mono { font-family: ui-monospace, monospace; font-size: 11px; color: var(--faint); background: #f6f5f1; padding: 5px 8px; border-radius: 6px; }
  .hint.tos { color: var(--faint); margin-top: 12px; }
  .warn-text { color: var(--warn); }
  .dim { color: var(--dim); }
  .tin { width: 100%; }
  .row-end { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
</style>
