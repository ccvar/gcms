<script lang="ts">
  import { invoke, Channel } from '@tauri-apps/api/core';
  import { getCurrentWindow } from '@tauri-apps/api/window';
  import { getVersion } from '@tauri-apps/api/app';
  import { open, confirm as confirmDialog } from '@tauri-apps/plugin-dialog';
  import { openUrl, revealItemInDir } from '@tauri-apps/plugin-opener';
  import { check as checkUpdate } from '@tauri-apps/plugin-updater';
  import { relaunch } from '@tauri-apps/plugin-process';
  import BrainIcon from '$lib/BrainIcon.svelte';
  import SiteMark from '$lib/SiteMark.svelte';
  import SiteFav from '$lib/SiteFav.svelte';
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

  // ---------- conversations ----------
  let convos = $state<Conversation[]>([]);
  let activeConvId = $state('');
  let activeConv = $state<Conversation | null>(null);
  // 进行中会话可切换模型（同厂商档位）；threadModel 跟随活动会话、改动即持久化。
  let threadModel = $state('');
  async function persistThreadModel(m: string) {
    if (!activeConv || m === activeConv.model) return;
    try {
      const u = await invoke<Conversation | null>('set_conversation_model', { convId: activeConv.id, model: m });
      if (u && activeConvId === u.id) activeConv = u;
    } catch (e) { say(String(e), 'err'); }
  }
  // 进行中会话可切换权限档位：claude 的钩子/参数和 codex 的 sandbox 都是每轮下发的，
  // 改完从下一轮（含排队消息）生效；正在跑的那轮已带旧档位启动，改动拦不住它（想拦先停止）。
  let threadPerm = $state('');
  async function persistThreadPerm(p: string) {
    if (!activeConv || p === (activeConv.perm_mode || 'full')) return;
    try {
      const u = await invoke<Conversation | null>('set_conversation_perm_mode', { convId: activeConv.id, permMode: p });
      // 回写 threadPerm：若这段 RPC 期间轮次刚好收尾，endTurn 可能已把下拉打回旧值，这里以落库值自愈。
      if (u && activeConvId === u.id) { activeConv = u; threadPerm = u.perm_mode || 'full'; }
      if (viewBusy) say('权限档位已切换，从下一轮开始生效（不影响正在跑的这轮）');
    } catch (e) { say(String(e), 'err'); }
  }
  let view = $state<'launcher' | 'thread' | 'schedule' | 'tasks' | 'templates' | 'prompts'>('launcher');

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
    id: string | null; connId: string; connName: string; site: string; taskType: string; brain: string; model: string; modelCustom: string;
    title: string; prompt: string; period: string; firstRun: string; enabled: boolean;
  }
  let taskModalOpen = $state(false);
  let tf = $state<TaskForm>(freshTaskForm());
  // 已从对话提议卡成功创建过的定时任务（按内容 key，持久化）→ 卡片显示「已创建」防重复点。
  let createdProposals = $state(loadCreatedProposals());
  let pendingProposalKey = $state('');
  function proposalKey(p: TaskProposal): string { return `${p.title}|${p.prompt}|${p.every_minutes}|${p.first_run}`; }
  function loadCreatedProposals(): Set<string> { try { return new Set<string>(JSON.parse(localStorage.getItem('gcms.pilot.createdProposals') || '[]')); } catch { return new Set(); } }
  function markProposalCreated(key: string) { if (!key) return; const n = new Set(createdProposals); n.add(key); createdProposals = n; try { localStorage.setItem('gcms.pilot.createdProposals', JSON.stringify([...n])); } catch { /* */ } }
  function freshTaskForm(): TaskForm {
    const brain = brainUsable('claude') ? 'claude' : brainUsable('codex') ? 'codex' : 'claude';
    return {
      id: null, connId: activeConnId, connName: activeConn?.name ?? '', site: sites[0]?.slug ?? '', taskType: 'article',
      brain, model: defaultModelFor(brain), modelCustom: '',
      title: '', prompt: '', period: '1440', firstRun: '', enabled: true,
    };
  }
  // 存的是「有效模型」单串。回填时据引擎还原：是该引擎预设档位 → 档位；否则 → 自定义 ID。
  function splitModel(b: string, m: string): { model: string; modelCustom: string } {
    return isPresetModel(b, m) ? { model: m, modelCustom: '' } : { model: defaultModelFor(b), modelCustom: m || '' };
  }
  function openNewTask() { pendingProposalKey = ''; tf = freshTaskForm(); taskModalOpen = true; }
  // AI 在对话里提议的定时任务 → 用当前会话的站点/模型预填，弹确认卡让用户确认/微调。
  function openTaskFromProposal(p: TaskProposal) {
    const c = activeConv;
    if (!c) return;
    pendingProposalKey = proposalKey(p);
    const firstRunSecs = p.first_run && !isNaN(new Date(p.first_run).getTime()) ? Math.floor(new Date(p.first_run).getTime() / 1000) : 0;
    tf = {
      id: null, connId: c.conn_id, connName: c.conn_name, site: c.site_slug,
      taskType: c.task_type === 'free' ? 'free' : 'article', brain: c.brain, ...splitModel(c.brain, c.model),
      title: p.title, prompt: p.prompt, period: String(p.every_minutes || 1440),
      firstRun: firstRunSecs ? toLocalInput(firstRunSecs) : '', enabled: true,
    };
    taskModalOpen = true;
  }
  function openEditTask(t: ScheduledTask) {
    pendingProposalKey = '';
    // 编辑保留任务原本所属的连接，绝不改绑到当前活动连接。
    tf = {
      id: t.id, connId: t.conn_id, connName: t.conn_name, site: t.site_slug, taskType: t.task_type, brain: t.brain, ...splitModel(t.brain, t.model),
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
      const model = tf.modelCustom.trim() || tf.model;
      await invoke('save_task', {
        id: tf.id, connId, siteSlug: tf.site, siteName,
        taskType: tf.taskType, brain: tf.brain, model,
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
  async function startPreview() {
    if (!activeConv || previewBusy) return;
    previewBusy = true;
    say('正在启动本地预览…（约 2 秒后打开预览窗）');
    try {
      await invoke('cf_preview_start', { connId: activeConv.conn_id, project: activeConv.site_slug });
      say('本地预览已打开（:8788）·关掉预览窗即停止');
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
  let claudeInstalling = $state(false);
  let claudeElapsed = $state(0);
  let claudeTimer: ReturnType<typeof setInterval> | undefined;
  async function installClaude() {
    if (claudeInstalling) return;
    claudeInstalling = true; claudeElapsed = 0;
    const t0 = Date.now();
    claudeTimer = setInterval(() => { claudeElapsed = Date.now() - t0; }, 500);
    try { const m = await invoke<string>('install_claude'); say(m); await refreshBrainsManual(); }
    catch (e) { say(String(e), 'err'); }
    finally { claudeInstalling = false; if (claudeTimer) { clearInterval(claudeTimer); claudeTimer = undefined; } }
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
    for (const b of ['claude', 'codex'] as Brain[]) {
      const usable = brainUsable(b);
      const note = usable ? '' : brains?.[b].found ? '未登录' : '未安装';
      for (const m of launcherModelOpts(b)) {
        out.push({ value: `${b}::${m.value}`, label: m.label, sub: note || m.sub, icon: b, disabled: !usable });
      }
    }
    return out;
  });
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
  function permOptsFor(brain: string) {
    if (brain !== 'codex') return permOpts;
    return permOpts.map((o) => (o.value === 'ask' || o.value === 'auto') ? { ...o, disabled: true, sub: 'Codex 不支持逐命令确认' } : o);
  }
  // 选中的档位若在 Codex 下不可用，落到「全自动」（保持已选值始终合法，不显示一个灰着的当前项）。
  $effect(() => { if (lBrain === 'codex' && (lPerm === 'ask' || lPerm === 'auto')) lPerm = 'full'; });
  // 待批工具调用：询问/自动档下钩子把请求写在后端 pending 目录，UI 轮询渲染批准卡。
  type Permit = { id: string; conv: string; tool: string; cmd: string; desc: string; arg: string; dangerous: boolean; mode: string; ts: number };
  // 批准卡标题：优先用模型自己写的操作说明（Bash 的 description），没有就按工具合成一句人话。
  function permitDesc(p: Permit): string {
    if (p.desc) return p.desc;
    if (p.tool === 'WebFetch') return p.arg ? `抓取网页：${p.arg}` : '抓取一个网页';
    if (p.tool === 'WebSearch') return p.arg ? `联网搜索：${p.arg}` : '联网搜索';
    if (p.tool === 'Write' || p.tool === 'Edit' || p.tool === 'NotebookEdit') return p.arg ? `写入文件：${p.arg}` : '写入文件';
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
  let customDraft = $state<Record<string, string>>({ claude: '', codex: '' });
  let customOpen = $state<Record<string, boolean>>({ claude: false, codex: false });
  function customsOf(b: string): string[] { return (b === 'codex' ? prefs.customCodexIds : prefs.customClaudeIds) ?? []; }
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
  $effect(() => { if (!isPresetModel(tf.brain, tf.model)) tf.model = defaultModelFor(tf.brain); });
  // 首次识别出本地 CLI 后：若默认厂商不可用（只装/登录了另一个），把 composer 默认厂商切到可用的那个；之后尊重手动选择。
  let brainAutoSet = false;
  $effect(() => {
    if (!brains || brainAutoSet) return;
    brainAutoSet = true;
    if (!brainUsable(lBrain)) lBrain = brainUsable('claude') ? 'claude' : brainUsable('codex') ? 'codex' : lBrain;
  });
  // 自定义模型输入框的占位示例，按当前引擎给不同提示。
  function modelPlaceholder(b: string): string {
    return b === 'codex' ? '如 gpt-5.3-codex-spark / o3（留空用上方模型）' : '如 claude-opus-4-8（留空用上方模型）';
  }

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
  // 会话大小/用量：上下文按厂商上限估算（Claude ~200k、Codex 的 gpt-5.x ~272k，近似值）。
  function ctxLimit(brain: string): number { return brain === 'codex' ? 272000 : 200000; }
  function fmtTokens(n: number): string {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1).replace(/\.0$/, '') + 'M';
    if (n >= 1000) return Math.round(n / 1000) + 'k';
    return String(n);
  }

  // 拖动窗口：忽略交互元素（按钮/输入等），否则点它们会误触发拖动。
  function startDrag(e: MouseEvent) {
    if (e.button !== 0) return;
    const t = e.target as HTMLElement;
    if (t.closest('button, a, input, textarea, select, [role="button"], [data-no-drag]')) return;
    getCurrentWindow().startDragging().catch(() => {});
  }

  // Windows 无 macOS 红绿灯（窗口控件在右侧），顶部工具按钮改靠左对齐，别飘在中间。
  const isWindows = typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent);
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

  // 会话搜索：按标题 / 站点名 / slug / 域名匹配。
  let searchOpen = $state(false);
  let searchQ = $state('');
  let searchInput = $state<HTMLInputElement | null>(null);
  function openSearch() { searchOpen = true; searchQ = ''; requestAnimationFrame(() => searchInput?.focus()); }
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
  function pickSearch(id: string) { searchOpen = false; openConv(id); }
  $effect(() => {
    if (!searchOpen) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') searchOpen = false; };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  });
  function say(m: string, k: 'ok' | 'err' = 'ok') { flash = m; flashKind = k; setTimeout(() => (flash = ''), k === 'err' ? 8000 : 4000); }
  function brainUsable(b: Brain): boolean { const s = b === 'claude' ? brains?.claude : brains?.codex; return !!s && s.found && s.logged_in !== false; }
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

  async function refreshConns() { try { conns = await invoke('list_connections'); if (!activeConnId && conns.length) selectConn(conns[0].id); } catch (e) { say(String(e), 'err'); } }
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
      if (view === 'thread' || view === 'schedule' || view === 'tasks' || ((view === 'templates' || view === 'prompts') && conn?.kind !== 'cloudflare')) view = 'launcher';
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
  type Template = { slug: string; name: string; desc: string; created_at: number };
  let templates = $state<Template[]>([]);
  let templatesLoading = $state(false);
  let tmplHtml = $state<Record<string, string>>({}); // 每个模板的入口 HTML，做真实缩略图
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
  async function delTemplate(t: Template) {
    const yes = await confirmDialog(`删除模板「${t.name}」？`, { title: '删除模板', kind: 'warning' });
    if (!yes) return;
    try { await invoke('delete_template', { slug: t.slug }); await loadTemplates(); } catch (e) { say(String(e), 'err'); }
  }
  async function previewTemplate(t: Template) {
    say('正在启动模板预览…（约 2 秒后打开预览窗）');
    try { await invoke('cf_preview_template', { slug: t.slug }); say('模板预览已打开（:8788）·关掉预览窗即停'); } catch (e) { say(String(e), 'err'); }
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
  async function authorize(b: Brain) { try { await invoke('open_brain_login', { brain: b }); say('已打开终端，完成授权后自动刷新'); } catch (e) { say(String(e), 'err'); } }

  // ---------- 对话导航 ----------
  function newChat() {
    view = 'launcher'; activeConvId = ''; activeConv = null; lDraft = '';
    if (!lSite && sites.length) lSite = sites[0].slug;
    if (!brainUsable(lBrain)) lBrain = brainUsable('claude') ? 'claude' : brainUsable('codex') ? 'codex' : 'claude';
  }
  async function openConv(id: string) {
    const c = await invoke<Conversation | null>('get_conversation', { id });
    if (!c) { await refreshConvos(); return; }
    // 打开的对话可能属于别的连接（从搜索/任务链接进来）——切到它自己的连接，否则侧栏会把它过滤掉。
    if (c.conn_id !== activeConnId) activeConnId = c.conn_id;
    activeConv = c; activeConvId = id; threadModel = c.model; threadPerm = c.perm_mode || 'full'; view = 'thread';
    attachments = []; queued = null; // 换会话清掉未发送的附件 / 等待消息
    expandSite(c.site_slug);
    checkCfReady();
    scrollSoon(true);
  }
  async function deleteConv(id: string) {
    // 对话进行中不删：否则删掉会话行会孤儿掉后台在跑的 CLI 子进程 + 触发「会话丢失」。先停止再删。
    if (running[id]) { say('对话进行中，请先点停止再删除。', 'err'); return; }
    try { await invoke('delete_conversation', { id }); if (activeConvId === id) { activeConvId = ''; activeConv = null; view = 'launcher'; } await refreshConvos(); } catch (e) { say(String(e), 'err'); }
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
  // 首轮也能流式渲染 + 停止（cancel 键对准真正的注册表 key）。
  function beginTurn(convId: string, optimistic: Conversation) {
    if (retryTimers[convId]) { clearTimeout(retryTimers[convId]); delete retryTimers[convId]; } // 任何新一轮开始都取消该会话待触发的自动重连
    lives[convId] = { text: '', tools: [], error: '', failed: false, startedAt: Date.now() };
    running[convId] = optimistic.conn_id;
    activeConv = optimistic; activeConvId = convId; threadModel = optimistic.model; threadPerm = optimistic.perm_mode || 'full';
    cfReady = false; // 本轮跑完再重新判定是否已建出内容
    view = 'thread'; scrollSoon(true);
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
      activeConv = fresh; threadModel = fresh.model; threadPerm = fresh.perm_mode || 'full';
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
    if (!sentQueued && !failed && conv && conv.brain === 'claude' && (conv.ctx_tokens ?? 0) >= ctxLimit('claude') * 0.9) {
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
    else if (activeConvId === convId) { activeConv = null; view = 'launcher'; }
  }

  async function startChat() {
    if (!lSite.trim() || !lDraft.trim() || !brainUsable(lBrain)) return;
    const site = sites.find((s) => s.slug === lSite);
    const taskType = isCfConn ? 'sitebuild' : lTask;
    prefs.brain = lBrain; prefs.model = lModel; prefs.taskType = lTask; prefs.perm = lPerm; savePrefs(prefs);
    const model = lModel;
    // CF 建站：把所选视觉风格的 tokens 指令拼进首条消息（可见、可追溯）。
    const styleDir = isCfConn && lStyle ? STYLE_DIRECTIVES[lStyle] : '';
    const text = lDraft.trim() + (styleDir ? `\n\n【视觉风格】${styleDir}` : '');
    const id = crypto.randomUUID();
    const now = Math.floor(Date.now() / 1000);
    const optimistic: Conversation = {
      id, conn_id: activeConnId, conn_name: activeConn?.name ?? '', site_slug: lSite, site_name: site?.name || lSite,
      task_type: taskType, brain: lBrain, model, perm_mode: lPerm, session_ref: '',
      title: text.slice(0, 30), messages: [optimisticUser(text)], status: 'running', created_at: now, updated_at: now,
    };
    // 立刻塞进侧栏，这样即便用户随后切走/新开对话，这条新会话也带着 running 圈可见，不必等这一轮跑完才出现。
    convos = [optimistic, ...convos];
    delete autoRetried[id];
    beginTurn(id, optimistic);
    lDraft = '';
    try {
      const conv = await invoke<Conversation>('start_conversation', {
        convId: id, connId: activeConnId, siteSlug: lSite, siteName: site?.name || lSite,
        taskType, brain: lBrain, model, permMode: lPerm,
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
  // 从消息正文里拆出「真正的文字」和「附件路径列表」——AI 收到的是完整正文，气泡里只显示干净文字 + 附件卡。
  function splitAttachments(text: string): { body: string; atts: string[] } {
    let cut = text.indexOf('\n\n' + ATT_MARKER);
    let sep = 2;
    if (cut < 0) {
      if (text.startsWith(ATT_MARKER)) { cut = 0; sep = 0; }
      else return { body: text, atts: [] };
    }
    const body = text.slice(0, cut);
    const atts = text.slice(cut + sep).split('\n').slice(1).map((l) => l.replace(/^-\s*/, '').trim()).filter(Boolean);
    return { body, atts };
  }
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
      if (m.role !== 'user') continue;
      for (const p of splitAttachments(m.text).atts) if (isImgPath(p)) ensureThumb(c.conn_id, c.site_slug, p);
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
      // 工作目录内图片 → data URI（负缓存空串＝确认不是可预览图片，当普通元素处理）
      let rel = '';
      if (!u) { const r = relWorkPath(s); if (r && isImgPath(r)) rel = r; }
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

  // ---------- 自定义右键菜单 ----------
  // 替换 WKWebView 默认英文菜单（含 Inspect Element / AutoFill 等）：
  // 输入框里给 剪切/复制/粘贴/全选；选中了消息文字给 复制；其它地方不出菜单。
  type CtxTarget = HTMLInputElement | HTMLTextAreaElement;
  let ctxMenu = $state<{ x: number; y: number; target: CtxTarget | null; canCopy: boolean } | null>(null);
  function onCtxMenu(e: MouseEvent) {
    e.preventDefault();
    const t = e.target as HTMLElement;
    const editable = t.closest('textarea, input[type="text"], input:not([type])') as CtxTarget | null;
    const sel = window.getSelection()?.toString() ?? '';
    if (!editable && !sel) { ctxMenu = null; return; }
    const canCopy = editable ? editable.selectionStart !== editable.selectionEnd : sel.length > 0;
    // 贴边收敛，别溢出窗口
    const x = Math.min(e.clientX, window.innerWidth - 160);
    const y = Math.min(e.clientY, window.innerHeight - 170);
    ctxMenu = { x, y, target: editable, canCopy };
  }
  function closeCtx() { ctxMenu = null; }
  function ctxCut() { const el = ctxMenu?.target; closeCtx(); if (el) { el.focus(); document.execCommand('cut'); } }
  function ctxCopy() {
    const m = ctxMenu; closeCtx();
    if (m?.target) { m.target.focus(); document.execCommand('copy'); }
    else { const s = window.getSelection()?.toString(); if (s) void navigator.clipboard.writeText(s); }
  }
  async function ctxPaste() {
    const el = ctxMenu?.target; closeCtx(); if (!el) return;
    el.focus();
    try {
      const txt = await navigator.clipboard.readText();
      if (!txt) return;
      const st = el.selectionStart ?? el.value.length;
      const en = el.selectionEnd ?? st;
      el.setRangeText(txt, st, en, 'end');
      el.dispatchEvent(new Event('input', { bubbles: true })); // 让 bind:value 感知
    } catch { say('无法读取剪贴板，请用 ⌘V 粘贴', 'err'); }
  }
  function ctxSelectAll() { const el = ctxMenu?.target; closeCtx(); if (el) { el.focus(); el.select(); } }
  $effect(() => {
    if (!ctxMenu) return;
    const close = () => (ctxMenu = null);
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') close(); };
    const closeOutside = (e: MouseEvent) => { if (!(e.target as HTMLElement).closest('.ctx-menu')) close(); };
    window.addEventListener('mousedown', closeOutside, true);
    window.addEventListener('keydown', onKey);
    window.addEventListener('scroll', close, true);
    window.addEventListener('resize', close);
    return () => {
      window.removeEventListener('mousedown', closeOutside, true);
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('scroll', close, true);
      window.removeEventListener('resize', close);
    };
  });

  // ---------- 助手消息 Markdown 渲染 ----------
  // 模型输出的 **粗体**/列表/表格/代码块 之前是裸文本，读感远不如 Claude 客户端。
  // marked(GFM+breaks) 渲染 → DOMPurify 消毒（模型会读网页/站点内容，注入面必须过滤）→ {@html}。
  marked.setOptions({ gfm: true, breaks: true });
  function mdRender(text: string): string {
    return DOMPurify.sanitize(marked.parse(text, { async: false }) as string);
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
      // 工作目录内的相对路径：图片→应用内放大；其他文件→文件管理器里定位
      const rel = relWorkPath(raw);
      if (rel && isImgPath(rel)) { openWorkdirLightbox(rel); return; }
      if (rel) { void revealWorkdir(rel); }
      return;
    }
    const code = t.closest('code');
    if (code && !code.closest('pre')) {
      if (window.getSelection()?.toString()) return; // 用户在拖选复制路径文本：不劫持成打开动作
      const s = (code.textContent ?? '').trim();
      const u = codeImgUrl(s);
      if (u) { e.preventDefault(); openUrl(u); return; }
      const rel = relWorkPath(s);
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
  function openWorkdirLightbox(rel: string) {
    const c = activeConv;
    if (!c) return;
    loadWorkdirImg(c.conn_id, c.site_slug, rel).then((d) => { if (d) lightbox = d; });
  }
  async function revealWorkdir(rel: string) {
    const c = activeConv;
    if (!c) return;
    try {
      const abs = await invoke<string>('resolve_workdir_file', { connId: c.conn_id, project: c.site_slug, path: rel });
      await revealItemInDir(abs);
    } catch { say('没找到这个文件（可能还没生成或已移动）', 'err'); }
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
  const siteOpts = $derived(sites.map((s) => ({ value: s.slug, label: s.name || s.slug, sub: s.url ? hostOf(s.url) : '未绑定域名', img: s.favicon || s.logo || faviconGuess(s.url) })));
  const brainOpts = $derived([
    { value: 'claude', label: 'Claude', icon: 'claude', disabled: !brainUsable('claude'), sub: brainUsable('claude') ? '' : brains?.claude.found ? '未登录' : '未安装' },
    { value: 'codex', label: 'Codex', icon: 'codex', disabled: !brainUsable('codex'), sub: brainUsable('codex') ? '' : brains?.codex.found ? '未登录' : '未安装' },
  ]);
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
  function modelOptsFor(b: string) { return b === 'codex' ? CODEX_MODELS : CLAUDE_MODELS; }
  // launcher / 会话里可选：预设档位 + 该厂商的全局自定义模型 ID（定时任务表单仍只用预设 + 自己的 modelCustom）。
  function launcherModelOpts(b: string) { return [...modelOptsFor(b), ...customsOf(b).map((id) => ({ value: id, label: id, sub: '自定义' }))]; }
  function defaultModelFor(b: string): string { return b === 'codex' ? '' : 'sonnet'; }
  function isPresetModel(b: string, m: string): boolean { return modelOptsFor(b).some((o) => o.value === m); }
  function isLauncherModel(b: string, m: string): boolean { return launcherModelOpts(b).some((o) => o.value === m); }

  // 会话按「站点 → 任务类型」两级分组：站点按最近活动倒序；任务类型固定顺序，只留有会话的。
  const TASK_ORDER = ['article', 'sitebuild', 'free'];
  const grouped = $derived.by(() => {
    const bySite = new Map<string, { slug: string; name: string; recent: number; convs: Conversation[] }>();
    for (const c of convos) {
      if (c.conn_id !== activeConnId) continue; // 侧栏只显示当前连接的对话，别串场
      const key = c.site_slug || '(未指定站点)';
      let g = bySite.get(key);
      if (!g) { g = { slug: key, name: c.site_name || c.site_slug || key, recent: 0, convs: [] }; bySite.set(key, g); }
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
<main class="app" class:win={isWindows}>
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
      {#if !isCfConn}
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

    <div class="convos">
      {#if convos.length === 0}
        <p class="rail-empty">还没有对话。<br />选好站点和模型，直接说你想做什么。</p>
      {/if}
      {#each grouped as g (g.slug)}
        <button class="site-grp" onclick={() => toggleSite(g.slug)} title={g.name}>
          <svg class="site-grp-chev" class:collapsed={collapsedSites.has(g.slug)} width="10" height="10" viewBox="0 0 12 12" fill="none"><path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>
          {#if isCfConn}{#if g.url}<img class="site-grp-fav" src={faviconOf(g.url)} alt="" onerror={(e) => ((e.currentTarget as HTMLImageElement).style.display = 'none')} />{/if}{:else}<SiteFav src={siteFav(g.slug)} label={g.slug} size={14} />{/if}
          <span class="site-grp-name">{g.name}</span>
          {#if g.url}<span class="site-grp-host" title={hostOf(g.url)}>{hostOf(g.url)}</span>{/if}
        </button>
        {#if !collapsedSites.has(g.slug)}
          {#each g.subs as sub (sub.type)}
            {#each sub.items as c (c.id)}
              <div class="convo {activeConvId === c.id ? 'on' : ''}" role="button" tabindex="0"
                onclick={() => openConv(c.id)} onkeydown={(e) => e.key === 'Enter' && openConv(c.id)}>
                <div class="convo-body">
                  <span class="convo-bi" title={brainLabel(c.brain)}><BrainIcon brain={c.brain} size={12} /></span>
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
            <button class="cs-item {activeConnId === c.id ? 'on' : ''}" onclick={() => { selectConn(c.id); switcherOpen = false; }}>
              {#if c.kind === 'cloudflare'}{@render cfMark(18)}{:else}<SiteMark size={18} />{/if}
              <span class="cs-main"><b>{c.name}{#if packUpdates[c.id]}<span class="pack-dot" title="技能包有新版，去「连接与模型设置」一键升级"></span>{/if}</b><small>{c.key_prefix} · {c.kind === 'cloudflare' ? 'Cloudflare' : c.key_kind === 'gcmsp_' ? '平台' : '单站'}</small></span>
              {#if activeConnId === c.id}<span class="cs-check">✓</span>{/if}
            </button>
          {/each}
          <div class="cs-div"></div>
          <button class="cs-act" onclick={() => { switcherOpen = false; importPack(); }}>{@render plusIcon()}导入技能包</button>
          <button class="cs-act" onclick={() => { switcherOpen = false; openCfConnect(); }}>{@render cfMark(15)}连接 Cloudflare</button>
          <button class="cs-act" onclick={() => { switcherOpen = false; setupOpen = true; }}>{@render gearIcon()}连接与模型设置…</button>
        </div>
      {/if}
    <button class="rail-foot" class:open={switcherOpen} onclick={() => { if (conns.length === 0) { setupOpen = true; } else { switcherOpen = !switcherOpen; } }}>
      {#if isCfConn}{@render cfMark(18)}{:else if activeConn && sites.length}<SiteFav src={siteFav(sites[0].slug)} label={sites[0].slug} size={18} />{:else}<AppIcon size={18} />{/if}
      <span class="foot-main">
        <b>{activeConn?.name ?? '未连接'}</b>
        <small>{#if isCfConn}{cfProjects.length} 个项目{cfSiteCount ? ` · ${cfSiteCount} 个站点` : ''}{:else if activeConn}{sites.length} 个站点<span class="foot-rfz" role="button" tabindex="-1" title="刷新站点（技能包新增站点后点这里）"
          onclick={(e) => { e.stopPropagation(); refreshSites(); }}
          onkeydown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); refreshSites(); } }}>{@render refreshIcon(discoveryLoading)}</span>{:else}点此导入技能包{/if}</small>
      </span>
      {#if conns.length === 0}
        <svg class="foot-gear" width="16" height="16" viewBox="0 0 16 16" fill="none">
          <path d="M2 5h5.2M10.8 5H14" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
          <path d="M2 11h2.8M8.4 11H14" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
          <circle cx="9" cy="5" r="1.7" stroke="currentColor" stroke-width="1.3" />
          <circle cx="6.6" cy="11" r="1.7" stroke="currentColor" stroke-width="1.3" />
        </svg>
      {:else}
        <svg class="foot-chev" class:up={switcherOpen} width="13" height="13" viewBox="0 0 12 12" fill="none">
          <path d="M2.75 7.5L6 4.25L9.25 7.5" stroke="currentColor" stroke-width="1.15" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      {/if}
    </button>
    </div>
  </aside>

  <!-- 主区 -->
  <section class="main">
    {#if flash}<div class="flash {flashKind}">{flash}</div>{/if}

    {#if ctxMenu}
      {@const m = ctxMenu}
      <div class="ctx-menu" style="left:{m.x}px; top:{m.y}px" role="menu">
        {#if m.target}
          <button class="ctx-item" disabled={!m.canCopy} onclick={ctxCut}>剪切<span class="ctx-kbd">{isWindows ? 'Ctrl+X' : '⌘X'}</span></button>
          <button class="ctx-item" disabled={!m.canCopy} onclick={ctxCopy}>复制<span class="ctx-kbd">{isWindows ? 'Ctrl+C' : '⌘C'}</span></button>
          <button class="ctx-item" onclick={ctxPaste}>粘贴<span class="ctx-kbd">{isWindows ? 'Ctrl+V' : '⌘V'}</span></button>
          <div class="ctx-div"></div>
          <button class="ctx-item" onclick={ctxSelectAll}>全选<span class="ctx-kbd">{isWindows ? 'Ctrl+A' : '⌘A'}</span></button>
        {:else}
          <button class="ctx-item" onclick={ctxCopy}>复制<span class="ctx-kbd">{isWindows ? 'Ctrl+C' : '⌘C'}</span></button>
        {/if}
      </div>
    {/if}

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
          <p>导入 gcms 技能包为你的站点做内容，或连接 Cloudflare 让本地 Claude / Codex 帮你建站并部署。</p>
          <div class="hero-btns">
            <button class="btn soft hero-import" onclick={importPack} disabled={importBusy}>{@render plusIcon()}{importBusy ? '导入中…' : '导入技能包'}</button>
            <button class="btn soft hero-import" onclick={openCfConnect}>{@render cfMark(15)}连接 Cloudflare</button>
          </div>
          {#if brains}
            <div class="cli-guide" data-no-drag>
              <div class="cli-guide-h"><span>本地 CLI 准备（至少装好并登录一个）</span><button class="cli-redetect" onclick={refreshBrainsManual} disabled={brainsBusy} title="装好 / 登录完点这里重新检测（会重新读取 PATH）">{@render refreshIcon(brainsBusy)}<span>重新检测</span></button></div>
              {#each [{ b: 'claude' as Brain, st: brains.claude, name: 'Claude Code', cmd: CLAUDE_INSTALL_CMD }, { b: 'codex' as Brain, st: brains.codex, name: 'Codex', cmd: 'npm i -g @openai/codex' }] as r (r.b)}
                <div class="cli-row">
                  <BrainIcon brain={r.b} size={16} />
                  <span class="cli-name">{r.name}</span>
                  {#if !r.st.found}
                    <span class="cli-tag bad">未安装</span>
                    {#if r.b === 'claude'}
                      <button class="wr-btn" onclick={installClaude} disabled={claudeInstalling}>{#if claudeInstalling}<span class="wr-spin"></span>安装中 {elapsedLabel(claudeElapsed)}{:else}一键安装{/if}</button>
                    {:else if brains && !brains.node.found}
                      <button class="node-need" title="Codex 通过 npm 安装，需要先装 Node.js（含 npm）。点击打开官网下载。" onclick={() => openUrl('https://nodejs.org/')}>先装 Node.js ↗</button>
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
                    <span class="cli-tag warn">未登录</span><button class="authbtn" onclick={() => authorize(r.b)}>去授权 ↗</button>
                  {:else}
                    <span class="cli-tag ok">✓ {r.st.version || '已就绪'}</span>
                  {/if}
                </div>
              {/each}
              <p class="cli-note"><svg class="cli-note-ic" width="13" height="13" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.4" stroke="currentColor" stroke-width="1.3" /><path d="M8 7.3v3.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><circle cx="8" cy="4.8" r="0.95" fill="currentColor" /></svg><span>安装/登录后状态灯自动变绿；密钥只进 macOS 钥匙串，绝不落盘。</span></p>
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
            <textarea bind:value={lDraft} rows="3"
              placeholder={isCfConn ? '例如：做个卖手冲咖啡的落地页，深色调，留个邮箱订阅表单存到 D1，先给我方案' : lTask === 'sitebuild' ? '例如：帮我搭一个介绍露营装备的中文站，风格轻松，先给我一个方案' : '例如：帮我写一篇 2026 年 macOS 效率工具盘点，先列个提纲'}
              oncompositionstart={() => (composing = true)} oncompositionend={() => (composing = false)}
              onkeydown={(e) => onComposerKey(e, startChat)}></textarea>
            <div class="composer-bar">
              <div class="cb-left">
                {#if isCfConn}
                  <input class="cf-proj-in" bind:value={lSite} placeholder="项目名，如 coffee-landing" spellcheck="false" autocapitalize="off" autocorrect="off" />
                  <Dropdown compact bind:value={lStyle} options={STYLE_OPTS} />
                {:else}
                  <Dropdown compact searchable bind:value={lSite} options={siteOpts} placeholder="选择站点" />
                {/if}
              </div>
              <div class="cb-right">
                <Dropdown compact bind:value={lPerm} options={permOptsFor(lBrain)} tone={permTone(lPerm)} />
                <Dropdown compact value={`${lBrain}::${lModel}`} options={comboOpts} onchange={pickCombo} />
                <button class="send" onclick={startChat} disabled={!lSite.trim() || !lDraft.trim() || !brainUsable(lBrain)} title="发送（Enter）">↑</button>
              </div>
            </div>
          </div>
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
            {#each tasks as t (t.id)}
              <div class="task-card {t.enabled ? '' : 'off'}">
                <div class="task-toggle">
                  <button class="switch {t.enabled ? 'on' : ''}" title={t.enabled ? '已启用' : '已暂停'} onclick={() => toggleTask(t)}><span></span></button>
                </div>
                <div class="task-body">
                  <b>{t.title}</b>
                  <div class="task-meta">
                    <SiteFav src={siteFav(t.site_slug)} label={t.site_slug} size={13} /><span class="cmono">{t.site_slug}</span>
                    <span class="cdot">·</span>{@render brainTag(t.brain, brainLabel(t.brain))}
                    <span class="cdot">·</span>{periodLabel(t.interval_minutes)}
                    {#if t.enabled}<span class="cdot">·</span>下次 {fmtSched(new Date(t.next_run * 1000).toISOString())}{/if}
                  </div>
                  {#if t.last_run}
                    <div class="task-last {t.last_status}">
                      上次 {fmt(t.last_run)} · {t.last_status === 'ok' ? '成功' : '失败'}{#if t.last_summary}：{t.last_summary}{/if}
                      {#if t.last_conv_id}<button class="link" onclick={() => openConv(t.last_conv_id)}>查看对话</button>{/if}
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

    {:else if view === 'templates'}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <header class="thread-head" data-tauri-drag-region onmousedown={startDrag}>
        <div class="th-info"><b>模板库</b><small>把做好的站点存成模板，之后引用它快速起新项目</small></div>
        <button class="icon-btn" onclick={loadTemplates} disabled={templatesLoading} title="刷新">{@render refreshIcon(templatesLoading)}</button>
      </header>
      <div class="thread">
        <div class="sched-inner">
          {#if templates.length === 0}
            <div class="sched-empty">
              <div class="cal-mark">🧩</div>
              <b>还没有模板</b>
              <p>在一个 Cloudflare 站点项目对话里点「存模板」，做得好的站就能沉淀下来复用。</p>
            </div>
          {:else}
            <div class="tmpl-grid">
              {#each templates as t (t.slug)}
                <div class="tmpl-card">
                  <div class="tmpl-thumb">
                    {#if tmplHtml[t.slug]}
                      <iframe class="tmpl-frame" srcdoc={tmplHtml[t.slug]} sandbox="allow-scripts" title={t.name} tabindex="-1" scrolling="no"></iframe>
                    {:else}
                      <span class="tmpl-letter">{t.name.slice(0, 1)}</span>
                    {/if}
                    <div class="tmpl-hover">
                      <button class="tmpl-act" aria-label="预览真实页面" onclick={() => previewTemplate(t)} data-tip="预览真实页面"><svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M1.6 8s2.4-4.4 6.4-4.4S14.4 8 14.4 8s-2.4 4.4-6.4 4.4S1.6 8 1.6 8Z" stroke="currentColor" stroke-width="1.2" /><circle cx="8" cy="8" r="1.9" stroke="currentColor" stroke-width="1.2" /></svg></button>
                      <button class="tmpl-act primary" aria-label="用它建新站" onclick={() => openUseTmpl(t)} data-tip="用它建新站"><svg width="15" height="15" viewBox="0 0 16 16" fill="none"><path d="M8 3.2v9.6M3.2 8h9.6" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg></button>
                      <button class="tmpl-act" aria-label="删除模板" onclick={() => delTemplate(t)} data-tip="删除模板"><svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M3 4.6h10M6.4 4.6V3.3h3.2V4.6M4.6 4.6l.6 8.1h5.6l.6-8.1" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
                    </div>
                  </div>
                  <div class="tmpl-body">
                    <b>{t.name}</b>
                    <div class="tmpl-sub"><span class="tmpl-desc">{t.desc || ''}</span><span class="tmpl-meta">{relTime(t.created_at)}</span></div>
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
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
          <small>{#if activeConvIsCf}{@render cfMark(13)}{:else}<SiteFav src={siteFav(activeConv?.site_slug ?? '')} label={activeConv?.site_slug ?? ''} size={13} />{/if} {activeConv?.site_name || activeConv?.site_slug} · {taskLabel(activeConv?.task_type ?? '')} · {@render brainTag(activeConv?.brain ?? 'claude', brainLabel(activeConv?.brain ?? '') + (activeConv?.brain === 'claude' && activeConv?.model ? ` ${activeConv.model}` : ''))}</small>
        </div>
        {#if activeSiteUrl}
          <button class="th-open" onclick={() => openUrl(activeSiteUrl)} title="打开 {activeSiteUrl}">{hostOf(activeSiteUrl)}<svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M6 3.5h6.5V10M12.2 3.8 3.8 12.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg></button>
        {/if}
      </header>

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
                {#if liveView.text}<div class="text md" onclick={mdClick}>{@html mdRender(liveView.text)}</div>{/if}
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
                <div class="permit-meta">{p.tool}{#if p.mode === 'ask'} · 询问档{/if}</div>
                {#if p.cmd}
                  {#if permitCmdOpen.has(p.id)}
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
          <textarea bind:value={draft} rows="1" placeholder={viewBusy ? '输入下一条，回车排队，等这轮结束自动发送' : '继续说…（Enter 发送，Shift+Enter 换行，可粘贴/拖入文件）'}
            oncompositionstart={() => (composing = true)} oncompositionend={() => (composing = false)}
            onpaste={onComposerPaste} ondrop={onComposerDrop} ondragover={onComposerDragOver}
            onkeydown={(e) => onComposerKey(e, viewBusy ? queueMessage : send)}></textarea>
          <div class="composer-bar">
            <div class="cb-left">
              {#if activeConvIsCf}
                <span class="cb-ro" title="项目已固定"><span class="cb-ro-t">{activeConv?.site_slug}</span></span>
              {:else}
                <span class="cb-ro" title="会话的站点已固定，不可更改"><SiteFav src={siteFav(activeConv?.site_slug ?? '')} label={activeConv?.site_slug ?? ''} size={15} /><span class="cb-ro-t">{activeConv?.site_name || activeConv?.site_slug}</span></span>
              {/if}
            </div>
            <div class="cb-right">
              <Dropdown compact bind:value={threadPerm} options={permOptsFor(activeConv?.brain ?? 'claude')} tone={permTone(threadPerm)} onchange={persistThreadPerm} />
              <!-- 厂商随会话固定：并进模型下拉（图标标识厂商），只列本厂商的模型档 -->
              <Dropdown compact bind:value={threadModel} options={launcherModelOpts(activeConv?.brain ?? 'claude').map((o) => ({ ...o, icon: activeConv?.brain ?? 'claude' }))} onchange={persistThreadModel} disabled={viewBusy} />
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
        {#if activeConv && ((activeConv.ctx_tokens ?? 0) > 0 || (activeConv.total_tokens ?? 0) > 0)}
          <div class="usage-line">
            {#if (activeConv.ctx_tokens ?? 0) > 0}
              {@const lim = ctxLimit(activeConv.brain)}
              {@const pct = Math.min(100, Math.round(((activeConv.ctx_tokens ?? 0) / lim) * 100))}
              <span class="usage-seg" title="当前会话上下文占用">
                <span class="usage-lbl">上下文</span>
                <span class="usage-bar"><span class="usage-fill" class:warn={pct >= 70 && pct < 90} class:danger={pct >= 90} style="width:{Math.max(3, pct)}%"></span></span>
                <span class="usage-num">{fmtTokens(activeConv.ctx_tokens ?? 0)}/{fmtTokens(lim)}</span>
              </span>
            {/if}
            {#if (activeConv.total_tokens ?? 0) > 0}<span class="usage-cum">本会话累计 {fmtTokens(activeConv.total_tokens ?? 0)} tokens</span>{/if}
          </div>
        {/if}
      </div>
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
        <input bind:this={searchInput} bind:value={searchQ} placeholder="按标题、站点名或域名搜索会话…" spellcheck="false" autocapitalize="off" autocorrect="off" />
        <kbd>esc</kbd>
      </div>
      <div class="search-list">
        {#if searchResults.length === 0}
          <p class="search-empty">没有匹配的会话</p>
        {:else}
          {#each searchResults as c (c.id)}
            {@const site = sites.find((s) => s.slug === c.site_slug)}
            <button class="search-item {activeConvId === c.id ? 'on' : ''}" onclick={() => pickSearch(c.id)}>
              <SiteFav src={siteFav(c.site_slug)} label={c.site_slug} size={15} />
              <span class="si-main"><b>{c.title || '未命名会话'}</b><small>{c.site_name || c.site_slug}{#if site?.url} · {hostOf(site.url)}{/if}</small></span>
              {@render brainTag(c.brain, brainLabel(c.brain))}
            </button>
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
        {#if m.error}
          <div class="text is-err">{@render richText(m.text)}{#if isLast && !viewBusy}{#if activeConv?.session_ref && !retryExhausted[activeConvId]}<button class="retry-btn" onclick={() => retry(activeConvId, true)}><svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M13 8a5 5 0 1 1-1.5-3.6M13 2v3h-3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>重试</button>{/if}<button class="retry-btn" title="换一个全新会话原地续跑（自动带上历史摘要）——用于会话状态损坏、重试无效时" onclick={() => rebuildSession(activeConvId)}><svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2.6 8a5.4 5.4 0 0 1 9.3-3.7M13.4 8a5.4 5.4 0 0 1-9.3 3.7" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" /><path d="M11.6 1.6v2.9h2.9M4.4 14.4v-2.9H1.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>重建继续</button>{/if}</div>
        {:else}
          <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
          <div class="text md" onclick={mdClick}>{@html mdRender(m.text)}</div>
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

{#snippet refreshIcon(spinning: boolean)}
  <svg class="rfz {spinning ? 'spin' : ''}" width="15" height="15" viewBox="0 0 16 16" fill="none">
    <path d="M13.6 8a5.6 5.6 0 1 1-1.7-4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
    <path d="M13.9 2.3V5.1H11.1" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
{/snippet}

{#snippet plusIcon()}<svg class="plus-ic" width="13" height="13" viewBox="0 0 14 14" fill="none"><path d="M7 2.4v9.2M2.4 7h9.2" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg>{/snippet}

{#snippet gearIcon()}<svg width="13" height="13" viewBox="0 0 16 16" fill="none"><path d="M2 5.3h3.5M9.3 5.3H14M2 10.7h5.1M10.9 10.7H14" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" /><circle cx="7.4" cy="5.3" r="1.6" stroke="currentColor" stroke-width="1.3" /><circle cx="9" cy="10.7" r="1.6" stroke="currentColor" stroke-width="1.3" /></svg>{/snippet}
{#snippet cfMark(size: number)}<svg class="cf-mark" width={size} height={size} viewBox="0 0 128 128"><path fill="#fff" d="m115.679 69.288l-15.591-8.94l-2.689-1.163l-63.781.436v32.381h82.061z" /><path fill="#f38020" d="M87.295 89.022c.763-2.617.472-5.015-.8-6.796c-1.163-1.635-3.125-2.58-5.488-2.689l-44.737-.581c-.291 0-.545-.145-.691-.363s-.182-.509-.109-.8c.145-.436.581-.763 1.054-.8l45.137-.581c5.342-.254 11.157-4.579 13.192-9.885l2.58-6.723c.109-.291.145-.581.073-.872c-2.906-13.158-14.644-22.97-28.672-22.97c-12.938 0-23.913 8.359-27.838 19.952a13.35 13.35 0 0 0-9.267-2.58c-6.215.618-11.193 5.597-11.811 11.811c-.145 1.599-.036 3.162.327 4.615C10.104 70.051 2 78.337 2 88.549c0 .909.073 1.817.182 2.726a.895.895 0 0 0 .872.763h82.57c.472 0 .909-.327 1.054-.8z" /><path fill="#faae40" d="M101.542 60.275c-.4 0-.836 0-1.236.036c-.291 0-.545.218-.654.509l-1.744 6.069c-.763 2.617-.472 5.015.8 6.796c1.163 1.635 3.125 2.58 5.488 2.689l9.522.581c.291 0 .545.145.691.363s.182.545.109.8c-.145.436-.581.763-1.054.8l-9.924.582c-5.379.254-11.157 4.579-13.192 9.885l-.727 1.853c-.145.363.109.727.509.727h34.089c.4 0 .763-.254.872-.654c.581-2.108.909-4.325.909-6.614c0-13.447-10.975-24.422-24.458-24.422" /></svg>{/snippet}
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
            onclick={() => selectConn(c.id)} onkeydown={(e) => e.key === 'Enter' && selectConn(c.id)}>
            {#if c.kind === 'cloudflare'}{@render cfMark(22)}{:else}<SiteMark size={22} />{/if}
            <span class="conn-main"><b>{c.name}</b>
              <small>{c.key_prefix} · {c.kind === 'cloudflare' ? 'Cloudflare' : c.key_kind === 'gcmsp_' ? '平台' : '单站'}{#if activeConnId === c.id && c.kind !== 'cloudflare'} · {sites.length} 站点{/if}</small></span>
            {#if packUpdates[c.id]}
              <button class="pack-upd" title="技能包有新版 {packUpdates[c.id]}——一键就地升级，连接与对话全部保留" disabled={!!packUpdating[c.id]}
                onclick={(e) => { e.stopPropagation(); upgradePack(c.id); }}>{packUpdating[c.id] ? '升级中…' : `升级技能包 ${packUpdates[c.id]}`}</button>
            {/if}
            {#if activeConnId === c.id && c.kind !== 'cloudflare'}
              <button class="icon-btn sm" title="刷新站点（技能包新增站点后点这里）" onclick={(e) => { e.stopPropagation(); refreshSites(); }}>{@render refreshIcon(discoveryLoading)}</button>
            {/if}
            <button class="x sm" title="删除连接" onclick={(e) => { e.stopPropagation(); removeConn(c.id); }}>×</button>
          </div>
        {/each}
      </div>

      <div class="sec-head mt"><span>本地模型</span><button class="icon-btn" onclick={refreshBrainsManual} title="刷新">{@render refreshIcon(brainsBusy)}</button></div>
      {#if brains}
        <div class="brains-list">
        {#each [{ b: 'claude' as Brain, st: brains.claude, name: 'Claude Code', cmd: CLAUDE_INSTALL_CMD }, { b: 'codex' as Brain, st: brains.codex, name: 'Codex', cmd: 'npm i -g @openai/codex' }] as r (r.b)}
          <div class="brain-block">
          <div class="brain-row">
            <span class="brain-ic"><BrainIcon brain={r.b} size={18} /></span>
            <span class="brain-main"><b>{r.name}</b>
              <small>{#if !r.st.found}未安装{:else if r.st.logged_in === false}未登录{:else}{r.st.version || '已就绪'}{/if}</small></span>
            <span class="brain-dot"><span class="dot {r.st.found && r.st.logged_in ? 'ok' : r.st.found ? 'warn' : 'off'}"></span></span>
            {#if r.st.found && r.st.logged_in === false}<button class="authbtn" onclick={() => authorize(r.b)}>去授权 ↗</button>{/if}
          </div>
          {#if !r.st.found}<p class="hint mono">安装：{r.cmd}</p>{/if}
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
                    <input class="tin" bind:value={customDraft[r.b]} placeholder={r.b === 'codex' ? '如 gpt-5.5 / o3' : '如 claude-opus-4-8'}
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
      <p class="hint tos">自定义模型 ID 会作为该厂商模型下拉里的附加档位（可加多个）；仅限本人订阅账户驱动本地官方 CLI，密钥存 macOS 钥匙串。</p>

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

<!-- 连接 Cloudflare -->
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
      <div class="trow">
        <div class="tfield"><span>站点</span><Dropdown bind:value={tf.site} options={taskSiteOpts} placeholder="选择站点" /></div>
        <div class="tfield"><span>类型</span><Dropdown bind:value={tf.taskType} options={taskTypeOpts} /></div>
      </div>
      <div class="trow">
        <div class="tfield"><span>厂商</span><Dropdown bind:value={tf.brain} options={brainOpts} /></div>
        <div class="tfield"><span>模型</span><Dropdown bind:value={tf.model} options={modelOptsFor(tf.brain)} /></div>
      </div>
      <div class="tfield"><span>自定义模型 ID（可选，留空用上面模型）</span>
        <input class="tin" bind:value={tf.modelCustom} placeholder={modelPlaceholder(tf.brain)} spellcheck="false" autocapitalize="off" autocorrect="off" /></div>
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
    /* 强调色走暖黑/灰（Codex 式安静路线），不用蓝紫；风险色（红/琥珀）另有专用变量。 */
    --accent: #33302a; --accent-h: #1d1b17; --accent-soft: #edece7;
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
  /* Windows：主内容左上角轻微圆角，跟原生标题栏之间更柔和（角落露出 rail 色）。macOS 无原生标题栏不需要。
     侧栏分隔线在 Windows 去掉——通高的 border-right 会在圆角上方留一截两侧同色的「悬空线头」；
     边界靠 rail/main 底色对比 + 圆角呈现（Codex 客户端同款做法）。 */
  .app.win { background: var(--rail); }
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
  .upd-toast-bar { width: 84px; height: 4px; border-radius: 2px; background: var(--border); overflow: hidden; flex: none; }
  .upd-toast-bar > span { display: block; height: 100%; background: var(--accent); border-radius: 2px; transition: width .2s ease; }

  /* ---- 左栏 ---- */
  .rail { position: relative; width: 240px; flex: none; display: flex; flex-direction: column; background: var(--rail); border-right: 1px solid var(--border); padding-top: 30px; }
  .rail.collapsed { display: none; }
  /* 右缘拖拽把手：改侧栏宽度。 */
  /* 只用 col-resize 光标提示，不画任何指示线（避免拖动时多出一条竖线）。 */
  .rail-resize { position: absolute; top: 30px; right: -3px; bottom: 0; width: 7px; cursor: col-resize; z-index: 5; }
  .rail-head { padding: 8px 8px 8px; display: flex; flex-direction: column; gap: 2px; }
  .newchat { display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 10px;
    background: none; color: var(--text); border: none; border-radius: 9px; font-size: 13.5px; font-weight: 550; cursor: pointer; text-align: left; }
  .newchat:hover { background: #f1efe9; }
  .newchat:disabled { opacity: .5; cursor: default; }
  .newchat svg { flex: none; color: var(--accent); }
  .railnav { display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 10px; background: none;
    border: none; border-radius: 9px; font-size: 13px; color: var(--dim); cursor: pointer; text-align: left; margin-top: -4px; }
  .railnav:hover { background: #f1efe9; color: var(--text); }
  .railnav.on { background: #e9e7e0; color: var(--text); font-weight: 550; }
  .railnav:disabled { opacity: .45; cursor: default; }
  .railnav svg { flex: none; }

  .convos { flex: 1; overflow-y: auto; padding: 4px 8px 8px; display: flex; flex-direction: column; gap: 1px; }
  .rail-empty { color: var(--faint); font-size: 12px; padding: 10px 8px; line-height: 1.7; }
  .grp { font-size: 10.5px; font-weight: 600; letter-spacing: .04em; color: var(--faint); padding: 12px 10px 4px; text-transform: uppercase; }
  .grp:first-child { padding-top: 4px; }
  /* 站点分组头（可折叠） */
  .site-grp { width: 100%; display: flex; align-items: center; gap: 6px; padding: 9px 8px 4px; margin-top: 2px; background: none; border: none; cursor: pointer; text-align: left; -webkit-appearance: none; appearance: none; }
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
  .convo { position: relative; display: flex; align-items: center; gap: 6px; border-radius: 8px; margin: 0 8px; padding: 1px 6px 1px 16px; cursor: pointer; }
  .convo:hover { background: #f1efe9; }
  .convo.on { background: #e9e7e0; }
  .convo-body { flex: 1; min-width: 0; display: flex; align-items: center; gap: 6px; }
  .convo-bi { flex: none; display: inline-flex; align-items: center; }
  .convo-title { flex: 1; min-width: 0; font-size: 13px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
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
  .foot-gear { color: var(--faint); font-size: 14px; }
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
    background: none; border: none; border-radius: 8px; padding: 7px 9px; cursor: pointer; text-align: left; font: inherit;
    color: var(--text);
  }
  .cs-item:hover { background: #f4f3ef; }
  .cs-item.on { background: #efeee9; }
  .cs-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .cs-main b { font-weight: 500; font-size: 13px; line-height: 1.2; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .cs-main small { color: var(--dim); font-size: 11px; line-height: 1.25; }
  .cs-check { color: var(--accent); flex: none; font-size: 13px; }
  .cs-div { height: 1px; background: var(--border); margin: 5px 4px; }
  .cs-act {
    width: 100%; display: flex; align-items: center; gap: 8px;
    background: none; border: none; border-radius: 8px; padding: 7px 9px; cursor: pointer; text-align: left;
    font: inherit; font-size: 12.5px; color: var(--dim);
  }
  .cs-act:hover { background: #f4f3ef; color: var(--text); }
  .cs-act :global(svg) { color: var(--faint); flex: none; }
  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
  .dot.ok { background: #16a34a; } .dot.warn { background: #d97706; } .dot.off { background: #cfccc4; }

  /* ---- 主区 ---- */
  .main { flex: 1; position: relative; display: flex; flex-direction: column; min-width: 0; padding-top: 0; }
  .flash { position: absolute; top: 40px; left: 50%; transform: translateX(-50%); z-index: 40; background: #14231a; color: #fff; padding: 9px 16px; border-radius: 10px; font-size: 13px; box-shadow: var(--shadow); max-width: 70%; }
  /* 自定义右键菜单（替换 WKWebView 默认英文菜单） */
  .ctx-menu { position: fixed; z-index: 120; min-width: 148px; background: #fff; border: 1px solid var(--border); border-radius: 11px; box-shadow: 0 12px 32px rgba(30,25,15,.16); padding: 5px; animation: pop .1s ease-out; }
  .ctx-item { width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 18px; background: none; border: none; border-radius: 7px; padding: 6px 10px; font: inherit; font-size: 13px; color: var(--text); cursor: pointer; text-align: left; }
  .ctx-item:hover:not(:disabled) { background: #f1efe9; }
  .ctx-item:disabled { color: var(--faint); cursor: default; }
  .ctx-kbd { font-size: 11px; color: var(--faint); }
  .ctx-div { height: 1px; background: var(--border); margin: 4px 6px; }
  /* 全局 tips 浮层（fixed，不受滚动容器裁剪） */
  .tipbox { position: fixed; z-index: 130; transform: translate(-50%, -100%); background: #26241f; color: #fff; font-size: 11px; line-height: 1.45; padding: 5px 9px; border-radius: 7px; width: max-content; max-width: 240px; white-space: normal; pointer-events: none; box-shadow: 0 5px 16px rgba(0, 0, 0, .18); animation: tipin .12s ease-out .3s both; }
  .tipbox.below { transform: translate(-50%, 0); }
  .imgtip { position: fixed; z-index: 130; transform: translate(-50%, -100%); padding: 5px; border-radius: 10px; background: #fff; border: 1px solid rgba(0, 0, 0, .1); box-shadow: 0 8px 24px rgba(0, 0, 0, .18); pointer-events: none; visibility: hidden; }
  .imgtip.ready { visibility: visible; animation: tipin .12s ease-out both; }
  .imgtip.below { transform: translate(-50%, 0); }
  .imgtip img { display: block; max-width: 260px; max-height: 200px; border-radius: 6px; background: repeating-conic-gradient(#ececea 0 25%, #fff 0 50%) 0 0 / 14px 14px; }
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
  .task-seg button.on { border-color: var(--accent); background: var(--accent-soft); }
  .ts-ic { flex: none; width: 30px; height: 30px; border-radius: 8px; display: inline-flex; align-items: center; justify-content: center; background: #edecef; color: var(--dim); transition: background .12s, color .12s; }
  .task-seg button.on .ts-ic { background: #fff; color: var(--accent); }
  .ts-txt { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .task-seg b { font-size: 13.5px; }
  .task-seg small { color: var(--dim); font-size: 11.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  .tin, textarea { font-family: inherit; font-size: 14px; color: var(--text); background: #fff; border: 1.5px solid var(--border2); border-radius: 10px; padding: 9px 11px; }
  .tin:focus, textarea:focus { outline: none; border-color: #b7b2a6; box-shadow: none; }

  /* 会话搜索（命令面板式；.mask 提供遮罩，box 居中于顶部） */
  .search-box { position: fixed; z-index: 61; top: 12vh; left: 50%; transform: translateX(-50%); width: min(560px, 92vw); background: #fff; border: 1px solid var(--border); border-radius: 14px; box-shadow: 0 24px 60px rgba(20, 15, 8, .28); overflow: hidden; display: flex; flex-direction: column; max-height: 66vh; }
  .search-head { display: flex; align-items: center; gap: 10px; padding: 12px 15px; border-bottom: 1px solid var(--border); }
  .search-head .si-ic { color: var(--faint); flex: none; }
  .search-head input { flex: 1; min-width: 0; border: none; outline: none; background: none; font: inherit; font-size: 15px; color: var(--text); }
  .search-head kbd { font: inherit; font-size: 10.5px; color: var(--faint); border: 1px solid var(--border2); border-radius: 5px; padding: 1px 6px; background: var(--rail); flex: none; }
  .search-list { overflow-y: auto; padding: 6px; }
  .search-empty { text-align: center; color: var(--faint); font-size: 13px; padding: 26px 12px; margin: 0; }
  .search-item { width: 100%; display: flex; align-items: center; gap: 10px; background: none; border: none; border-radius: 9px; padding: 9px 10px; cursor: pointer; text-align: left; color: var(--text); }
  .search-item:hover, .search-item.on { background: #f4f3ef; }
  .si-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .si-main b { font-weight: 500; font-size: 13.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .si-main small { color: var(--dim); font-size: 11.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  /* 输入框（仿 Claude Code：整块圆角边框，聚焦时高亮，发送按钮嵌在框内） */
  .composer.big, .composer-wrap .composer { position: relative; background: #fff; border: 1px solid var(--border2); border-radius: 12px; box-shadow: none; transition: border-color .12s, box-shadow .12s; }
  .composer.big:focus-within, .composer-wrap .composer:focus-within { border-color: #b7b2a6; box-shadow: none; }
  .composer.big textarea, .composer-wrap textarea { width: 100%; resize: none; border: none; background: none; box-shadow: none; padding: 14px 52px 14px 17px; line-height: 1.6; max-height: 200px; display: block; }
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
  .thread-head { flex: none; padding: 13px 24px; border-bottom: 1px solid var(--border); display: flex; align-items: center; justify-content: space-between; gap: 12px; }
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
  .tcheck { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
  .tcheck input { width: auto; }

  .composer-wrap { flex: none; padding: 10px 24px 20px; }
  /* 输入框下方：会话大小（上下文占用）+ 本会话累计 token */
  .usage-line { display: flex; align-items: center; gap: 12px; margin: 7px 4px 0; font-size: 11px; color: var(--faint); }
  .usage-seg { display: inline-flex; align-items: center; gap: 6px; min-width: 0; }
  .usage-lbl { color: var(--dim); }
  .usage-bar { width: 78px; height: 4px; border-radius: 2px; background: var(--border); overflow: hidden; flex: none; }
  .usage-fill { display: block; height: 100%; background: var(--accent); border-radius: 2px; }
  .usage-fill.warn { background: var(--warn); }
  .usage-fill.danger { background: var(--err); }
  .usage-num { font-variant-numeric: tabular-nums; }
  .composer-wrap .composer { max-width: 760px; margin: 0 auto; }

  /* ---- 按钮 / 弹窗 ---- */
  .btn { background: #fff; color: var(--text); border: 1.5px solid var(--border2); border-radius: 9px; padding: 7px 14px; cursor: pointer; font-size: 13px; }
  .btn:hover { background: #f6f5f1; } .btn:disabled { opacity: .5; cursor: default; }
  .btn.primary { background: var(--accent); border-color: var(--accent); color: #fff; } .btn.primary:hover { background: var(--accent-h); }
  .btn.ghost { border-color: var(--border); }
  .btn.small { padding: 4px 10px; font-size: 12px; }
  .btn.lg { padding: 10px 22px; font-size: 15px; }
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
  .cf-modal .tin { padding: 6px 10px; font-size: 12.5px; }
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
  /* Codex 前置：没装 Node 时的引导小按钮（点开 nodejs.org） */
  .node-need { flex: none; border: none; background: #faf1d8; color: #b45309; font-size: 11px; font-weight: 550; padding: 3px 9px; border-radius: 6px; cursor: pointer; }
  .node-need:hover { background: #f5e7bd; }
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
  .ub-att-img { padding: 0; border: 1px solid var(--border2); border-radius: 10px; overflow: hidden; cursor: zoom-in; line-height: 0; background: repeating-conic-gradient(#ececea 0 25%, #fff 0 50%) 0 0 / 14px 14px; }
  .ub-att-img img { display: block; width: 128px; height: 96px; object-fit: cover; }
  .ub-att-img.ph { display: inline-block; width: 128px; height: 96px; cursor: default; animation: phpulse 1.2s ease-in-out infinite; }
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
  .tmpl-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; }
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
  .tmpl-meta { flex: none; font-size: 11px; color: #b3ada2; }
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
  .modal { top: 50%; left: 50%; transform: translate(-50%, -50%); width: min(440px, 92vw); border-radius: 14px; overflow: hidden; }
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
  .cust-add .tin { flex: 1; min-width: 0; width: auto; font-size: 12px; line-height: 1.3; padding: 5px 10px; border-radius: 8px; }
  .cust-add .btn { flex: none; }
  .hint { color: var(--dim); font-size: 12px; margin: 2px 0; line-height: 1.6; }
  .hint.mono { font-family: ui-monospace, monospace; font-size: 11px; color: var(--faint); background: #f6f5f1; padding: 5px 8px; border-radius: 6px; overflow-wrap: anywhere; }
  .hint.tos { color: var(--faint); margin-top: 12px; }
  .warn-text { color: var(--warn); }
  .dim { color: var(--dim); }
  .tin { width: 100%; }
  .row-end { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
</style>
