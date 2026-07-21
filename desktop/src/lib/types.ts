export interface Connection {
  id: string;
  name: string;
  /** 用户自定义备注；缺省/空串时继续显示脱敏 Key。 */
  remark?: string;
  /** gcms（导入技能包）| cloudflare（CF token 建站）| ssh（远程机器）。旧连接缺省即 gcms。 */
  kind: string;
  /** 一键导入的 Pilot 运营助手所绑定的来源 SSH 连接。 */
  source_ssh_id?: string;
  api_base: string;
  skill_dir: string;
  /** SSH 连接此项恒为空：它的秘密是密码/口令，显示前缀等于把密码印在 UI 上。 */
  key_prefix: string;
  key_kind: string;
  /** Cloudflare 账号 id（仅 kind=cloudflare）。 */
  account_id: string;
  /** 这条 Cloudflare 连接被明确选中管理的 Zone。 */
  preferred_zones?: string[];
  /** 以下仅 kind=ssh。 */
  ssh_host?: string;
  ssh_port?: number;
  ssh_user?: string;
  /** password | key */
  ssh_auth?: string;
  ssh_key_path?: string;
  /** 已确认的主机指纹（TOFU）；连接时必须匹配。 */
  ssh_fingerprint?: string;
  /** 远端系统（os-release 的 PRETTY_NAME，如 "Ubuntu 24.04.1 LTS"）；空 = 还没探过。 */
  ssh_os?: string;
  /** 发行版 id（os-release 的 ID，如 ubuntu/debian）——UI 据此选发行版图标。 */
  ssh_os_id?: string;
  /** 技能包版本（=服务端版本）；空 = 未知。 */
  pack_version?: string;
  created_at: string;
}

export interface Site {
  id: number;
  slug: string;
  name: string;
  capabilities: unknown;
  api_base: string;
  url?: string;
  logo?: string;
  favicon?: string;
}

export interface Discovery {
  items: Site[];
  all_sites: boolean;
  synthetic?: boolean;
}

export type ImportOutcome =
  | { status: 'imported'; connection: Connection }
  | { status: 'upgraded'; connection: Connection }
  | { status: 'needs_key'; api_base: string };

export type Brain = 'claude' | 'codex' | 'grok';

export interface BrainStatus {
  found: boolean;
  path: string;
  version: string;
  logged_in: boolean | null;
  account: string;
  detail: string;
}

export interface BrainsInfo {
  claude: BrainStatus;
  codex: BrainStatus;
  /** xAI Grok CLI（ACP 接入）；登录态看 ~/.grok/auth.json。 */
  grok: BrainStatus;
  /** Cloudflare 部署工具（wrangler）；用 env token，logged_in 恒为 null。 */
  wrangler: BrainStatus;
  /** 无头截图用的浏览器（Chrome/Edge/Chromium，可选能力）。 */
  browser: BrainStatus;
  /** Node.js（npm 装 Codex/wrangler 的前置；Claude Code 原生安装不需要）。 */
  node: BrainStatus;
  path_env: string;
}

// ---- 对话 ----

/** remote = 远程连接（SSH）的运维对话：没有站点，对象是那台机器。 */
export type TaskType = 'article' | 'sitebuild' | 'free' | 'remote';

export interface ToolCall {
  label: string;
  detail: string;
}

export interface TaskProposal {
  title: string;
  prompt: string;
  every_minutes: number;
  first_run: string;
}

export interface Message {
  role: 'user' | 'assistant';
  text: string;
  tools: ToolCall[];
  ts: number;
  hidden: boolean;
  error: boolean;
  proposal?: TaskProposal | null;
  /** 本轮因订阅额度/限流失败：恢复时间戳秒（0=拿不到时间）。null/undefined=非限额错误。 */
  limit_reset?: number | null;
}

// （定时任务的多站点/强度字段见 ScheduledTask）

export interface Conversation {
  id: string;
  conn_id: string;
  conn_name: string;
  site_slug: string;
  site_name: string;
  task_type: TaskType;
  brain: Brain;
  model: string;
  /** 权限档位：plan | ask | auto | full。空串＝旧会话＝full。 */
  perm_mode: string;
  /** 思考等级（推理强度）：'' 默认 | low | medium | high。 */
  effort?: string;
  /** 多站会话：站点 slug 清单（>1 时为跨站会话）。 */
  site_slugs?: string[];
  site_names?: string[];
  session_ref: string;
  title: string;
  messages: Message[];
  status: 'idle' | 'running';
  created_at: number;
  updated_at: number;
  /** 最近一轮上下文 token（≈当前会话大小），0＝无数据。 */
  ctx_tokens?: number;
  /** 本会话累计处理 token。 */
  total_tokens?: number;
}

export type TurnEvent =
  | { type: 'delta'; text: string }
  | { type: 'tool'; label: string; detail: string }
  | { type: 'done'; ok: boolean; error: string };

export interface ScheduledItem {
  site_slug: string;
  site_name: string;
  id: number;
  title: string;
  lang: string;
  published_at: string;
  /** **相对路径**（`/zh/posts/xxx`），不是完整链接——要配 discovery 里那个站的域名才能开。 */
  url: string;
  /** `scheduled`（待发布）| `published`（已发布，最近一周）。 */
  status: string;
  /** 所属集合：`posts` / `links` / `pages` / 扩展类型前缀（`products` 等）。 */
  collection: string;
}

/** 单个站点在某次触发中的结果。 */
export interface TaskRunSite { slug: string; ok: boolean; conv_id?: string; error?: string; deferred?: boolean; executor?: string; fallback_used?: boolean; }
/** 一次触发的运行记录（新到旧，最多 20 条）。deferred＝订阅限额顺延（非失败语义）。 */
export interface TaskRun { ts: number; ok: boolean; summary: string; sites?: TaskRunSite[]; deferred?: boolean; }

export interface ScheduledTask {
  id: string;
  conn_id: string;
  conn_name: string;
  site_slug: string;
  site_name: string;
  /** 多站点任务：到点对每个站点各跑一轮；空/缺省 = 单站（site_slug）。 */
  site_slugs?: string[];
  site_names?: string[];
  task_type: TaskType;
  brain: Brain;
  model: string;
  /** 思考等级：'' 默认 | low | medium | high */
  effort?: string;
  /** 主模型因额度、限流或不可用而且尚未写入时，最多自动接管一次。 */
  fallback_brain?: Brain | '';
  fallback_model?: string;
  fallback_effort?: string;
  title: string;
  prompt: string;
  interval_minutes: number;
  next_run: number;
  enabled: boolean;
  last_run: number;
  last_status: string;
  last_summary: string;
  last_conv_id: string;
  runs: number;
  /** 运行记录（新到旧，最多 20 条）。 */
  history?: TaskRun[];
  created_at: number;
  updated_at: number;
}
