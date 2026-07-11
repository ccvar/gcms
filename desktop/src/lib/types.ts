export interface Connection {
  id: string;
  name: string;
  /** gcms（导入技能包）| cloudflare（CF token 建站）。旧连接缺省即 gcms。 */
  kind: string;
  api_base: string;
  skill_dir: string;
  key_prefix: string;
  key_kind: string;
  /** Cloudflare 账号 id（仅 kind=cloudflare）。 */
  account_id: string;
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

export type Brain = 'claude' | 'codex';

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
  /** Cloudflare 部署工具（wrangler）；用 env token，logged_in 恒为 null。 */
  wrangler: BrainStatus;
  /** 无头截图用的浏览器（Chrome/Edge/Chromium，可选能力）。 */
  browser: BrainStatus;
  /** Node.js（npm 装 Codex/wrangler 的前置；Claude Code 原生安装不需要）。 */
  node: BrainStatus;
  path_env: string;
}

// ---- 对话 ----

export type TaskType = 'article' | 'sitebuild' | 'free';

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
  url: string;
}

/** 单个站点在某次触发中的结果。 */
export interface TaskRunSite { slug: string; ok: boolean; conv_id?: string; error?: string; }
/** 一次触发的运行记录（新到旧，最多 20 条）。 */
export interface TaskRun { ts: number; ok: boolean; summary: string; sites?: TaskRunSite[]; }

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
