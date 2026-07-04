export interface Connection {
  id: string;
  name: string;
  api_base: string;
  skill_dir: string;
  key_prefix: string;
  key_kind: string;
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
}

export interface Conversation {
  id: string;
  conn_id: string;
  conn_name: string;
  site_slug: string;
  site_name: string;
  task_type: TaskType;
  brain: Brain;
  model: string;
  session_ref: string;
  title: string;
  messages: Message[];
  status: 'idle' | 'running';
  created_at: number;
  updated_at: number;
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

export interface ScheduledTask {
  id: string;
  conn_id: string;
  conn_name: string;
  site_slug: string;
  site_name: string;
  task_type: TaskType;
  brain: Brain;
  model: string;
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
  created_at: number;
  updated_at: number;
}
