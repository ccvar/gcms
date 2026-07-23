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
  /** 平台站点状态；旧版 GCMS 可能不返回。 */
  status?: 'enabled' | 'disabled' | string;
  /** 默认站点是已经存在的平台主入口，不参与“新站上线准备”提示。 */
  is_default?: boolean;
  /** 站点用途分类；旧版 GCMS 可能尚未返回。 */
  site_kind?: 'content' | 'general' | 'factory' | 'dtc' | string;
  /** 当前外观主题的轻量摘要；完整状态按需从站点主题接口读取。 */
  theme?: SiteThemeSummary | null;
  /** 站点是否允许平台自动化；关闭时仍可出现在管理列表，但不会进入对话选站范围。 */
  management_automation_enabled?: boolean;
  capabilities: unknown;
  api_base: string;
  url?: string;
  logo?: string;
  favicon?: string;
  /** 与 GCMS 后台站点卡片一致：启用语种、默认语种全部内容、全站待发布。 */
  language_count?: number;
  content_count?: number;
  pending_count?: number;
  /** 新站上线准备的只读事实；旧版 GCMS 未返回时由 Pilot 降级为“待检查”，不臆测缺失。 */
  readiness?: {
    /** 站点名称、副标题和简介均已脱离首装样板。旧版 GCMS 可能不返回。 */
    site_info?: boolean;
    /** 未完成的站点信息字段：name | tagline | description。 */
    site_info_missing?: string[];
    public_url: boolean;
    https: boolean;
    logo: boolean;
    favicon: boolean;
    share_image: boolean;
    published_content: boolean;
  };
  /** 站点卡片的脱敏接入摘要；不包含 OAuth / Bot Token 或账号密钥。 */
  integrations?: {
    analytics: {
      configured: boolean;
      enabled: boolean;
      active_users?: number;
      sessions?: number;
      status?: string;
      fetched_at?: string;
    };
    search_console: {
      configured: boolean;
      enabled: boolean;
      clicks?: number;
      impressions?: number;
      status?: string;
      fetched_at?: string;
    };
    telegram: {
      configured: boolean;
      auto_push: boolean;
      channel?: string;
      channel_url?: string;
      uses_shared_bot?: boolean;
    };
  };
  /** GCMS 返回的真实部署/运行摘要；与 GCMS 后台站点卡片同源。 */
  deployment?: SiteDeployment;
  /** 公网访问配置的后台进度摘要；用于关闭向导后继续提示 DNS / HTTPS / 橙云状态。 */
  public_access?: SitePublicAccessSummary | null;
}

export interface SiteThemeSummary {
  /** 新版发现接口使用 id；theme 兼容站点主题状态接口的字段名。 */
  id?: string;
  theme?: string;
  name?: string;
  description?: string;
  family?: string;
  family_name?: string;
  category?: string;
  layout?: string;
  previous_theme?: string;
  can_rollback?: boolean;
}

export interface ThemeCatalogOption {
  key: string;
  type: string;
  label: string;
  description?: string;
  localized?: boolean;
  max?: number;
  enabled_key?: string;
  example?: string;
}

/** GCMS 主题目录中仍可直接选择的单个主题/皮肤。 */
export interface ThemeCatalogItem {
  id: string;
  name: string;
  description?: string;
  category?: string;
  layout?: string;
  family?: string;
  family_name?: string;
  accent?: string;
  background?: string;
  radius?: string | number;
  options?: ThemeCatalogOption[];
  selected?: boolean;
}

/** 主题家族中的配色皮肤；字段保持可选以兼容旧版仅返回 items 的 GCMS。 */
export interface ThemeSkin {
  id: string;
  name: string;
  description?: string;
  category?: string;
  layout?: string;
  accent?: string;
  background?: string;
  radius?: string | number;
  selected?: boolean;
}

export interface ThemeFamily {
  id: string;
  name: string;
  description?: string;
  category?: string;
  categories?: string[];
  layout?: string;
  selected?: boolean;
  active?: ThemeSkin;
  skins?: ThemeSkin[];
}

export interface ThemeCatalog {
  /** 旧版和新版均保留的扁平主题目录。 */
  items?: ThemeCatalogItem[];
  /** 新版 GCMS 提供的面向 Pilot 的主题家族。 */
  families?: ThemeFamily[];
  /** 平台默认站点主题；不能代替目标站点的 SiteThemeState。 */
  selected_theme?: string;
  /** 兼容主题详情响应。 */
  theme?: ThemeCatalogItem;
}

export interface SiteThemeState {
  site_id?: number;
  site_status?: string;
  theme: string;
  name?: string;
  description?: string;
  family?: string;
  family_name?: string;
  category?: string;
  layout?: string;
  previous_theme?: string;
  can_rollback?: boolean;
  /** 新版 GCMS 可返回的乐观并发版本。 */
  revision?: string;
}

export interface ThemeMutationWarning {
  code?: string;
  message: string;
  severity?: 'info' | 'warning' | 'error' | string;
}

export interface ThemeMutationThemeRef {
  id?: string;
  name?: string;
  family?: string;
  family_name?: string;
  category?: string;
  layout?: string;
}

export interface ThemeMutationResult {
  ok?: boolean;
  dry_run?: boolean;
  site_id?: number;
  site_status?: string;
  action?: 'apply' | 'rollback' | string;
  previous_theme?: string;
  current_theme?: string;
  target_theme?: string;
  from?: ThemeMutationThemeRef;
  to?: ThemeMutationThemeRef;
  theme?: string;
  layout?: string;
  changed?: boolean;
  category_changed?: boolean;
  layout_changed?: boolean;
  live_site?: boolean;
  execution_requires_unlock?: boolean;
  unlock_operation?: string;
  can_rollback?: boolean;
  revision?: string;
  warnings?: Array<string | ThemeMutationWarning>;
  impact?: Record<string, unknown>;
}

export interface ThemePreviewURL {
  preview_url: string;
  expires_at?: string;
  ttl_seconds?: number;
  site_id?: number;
  theme_id?: string;
  current_theme?: string;
}

export interface CloudflareZone {
  id: string;
  name: string;
  account_id?: string;
  account?: string;
  account_name?: string;
  authorization_id?: string;
}

export interface CloudflareAuthorization {
  id: string;
  label: string;
  purpose?: string;
  source?: string;
  configured: boolean;
  account_id?: string;
  account?: string;
  zone_count?: number;
  zones?: CloudflareZone[];
  created_at?: string;
  updated_at?: string;
}

export interface CloudflareGlobalConfig {
  configured?: boolean;
  dns_configured: boolean;
  deploy_configured: boolean;
  account?: string;
  zone?: string;
  authorization_count?: number;
  zone_count?: number;
  zones?: CloudflareZone[];
  authorizations: CloudflareAuthorization[];
}

export interface SiteDeploymentDomain {
  host: string;
  scheme?: string;
  primary: boolean;
  redirect_to_primary: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface SiteDeployment {
  site_id?: number;
  provider: 'cloudflare' | 'server' | 'pending' | string;
  public_url?: string;
  primary_domain?: string;
  domains?: SiteDeploymentDomain[];
  authorization_id?: string;
  account_id?: string;
  account?: string;
  zone_id?: string;
  zone?: string;
  deploy_mode?: string;
  project?: string;
  auto_sync?: boolean;
  sync_mode?: string;
  status?: {
    state?: string;
    service?: string;
    running?: boolean;
    published?: boolean;
    configured?: boolean;
    message?: string;
    step?: string;
    updated_at?: string;
    last_publish_at?: string;
    last_purge_at?: string;
  };
  summary?: {
    pending: boolean;
    text: string;
    title: string;
    parts?: { label: string; value: string }[];
  };
  runtime?: {
    db_path?: string;
    upload_dir?: string;
    base_url?: string;
    site_updated_at?: string;
  };
}

export interface SitePublicAccessDomainRef {
  scheme?: string;
  host: string;
}

export interface SitePublicAccessStatus {
  site_id: number;
  site_status?: string;
  /** Opaque identity of the current public-access attempt. */
  generation?: string;
  /** Opaque snapshot of the current primary and redirect-domain set. */
  domain_fingerprint?: string;
  /** Server-side hint only; execution performs the same checks again. */
  can_clear_unverified?: boolean;
  /** Set only after HTTPS returned 200 from GCMS itself. */
  verified_at?: number;
  domain?: {
    site_id?: number;
    primary_domain?: SitePublicAccessDomainRef | null;
    redirect_domains?: SitePublicAccessDomainRef[];
  };
  provider?: string;
  dns?: {
    status?: boolean | string;
    provider?: string;
    name_servers?: string[];
    a_records?: string[];
    proxied?: boolean;
  };
  caddy?: {
    available?: boolean;
    status?: string;
    kind?: string;
    running?: boolean;
    integrated?: boolean;
    can_auto_sync?: boolean;
  };
  https?: {
    status?: string;
    ok?: boolean;
    reason?: string;
  };
  cloudflare_proxy?: {
    /** 用户是否要求在源站 HTTPS 验证通过后开启橙云。 */
    requested?: boolean;
    /** 当前 DNS 是否已由 Cloudflare 代理。 */
    actual?: boolean;
    status?: 'disabled' | 'pending' | 'enabling' | 'enabled' | 'failed' | string;
    error?: string;
    updated_at?: number;
  };
  error?: string;
}

export interface SitePublicAccessSummary {
  state: 'pending' | 'attention' | 'ready' | string;
  stage: 'dns' | 'https' | 'proxy' | 'ready' | string;
  host?: string;
  message?: string;
  updated_at?: number;
  generation?: string;
  domain_fingerprint?: string;
  /** Hint for showing the recovery action; the server revalidates on use. */
  can_clear?: boolean;
}

export interface SitePublicAccessApplyResult {
  ok?: boolean;
  messages?: string[];
  status?: SitePublicAccessStatus;
}

export interface Discovery {
  items: Site[];
  /** 当前密钥成员范围内的完整站点列表，包含已关闭站点；旧版 GCMS 缺省时回退 items。 */
  lifecycle_items?: Site[];
  all_sites: boolean;
  synthetic?: boolean;
  /** 用户主动刷新统计时由新版 GCMS 返回；普通发现不触发 Google 请求。 */
  stats_refresh?: { requested: boolean; refreshed: number; failed: number };
  /** 平台级全局接入摘要；旧版 GCMS 不返回时 Pilot 显示“待检查”。 */
  platform?: {
    google: {
      oauth_configured: boolean;
      authorized_accounts: number;
      analytics_accounts: number;
      search_console_accounts: number;
      data_range: { mode: string; days: number; from?: string; to?: string; label: string };
    };
    telegram: { shared_bot_configured: boolean };
    cloudflare: CloudflareGlobalConfig;
  };
}

export interface GoogleAccountOption {
  account_id: string;
  label: string;
  email: string;
}

export interface GlobalIntegrationsConfig {
  google: {
    oauth_configured: boolean;
    client_id: string;
    client_secret_configured: boolean;
    redirect_url: string;
    data_range: { mode: 'days' | 'custom'; days: number; from?: string; to?: string; label: string };
    analytics_accounts: GoogleAccountOption[];
    search_console_accounts: GoogleAccountOption[];
    authorize_url: string;
  };
  telegram: {
    shared_bot_configured: boolean;
  };
  cloudflare: CloudflareGlobalConfig;
}

export interface SiteGoogleIntegrationConfig {
  configured: boolean;
  enabled: boolean;
  account_id?: string;
  measurement_id?: string;
  property?: string;
  data_stream?: string;
  needs_verification?: boolean;
  verify_url?: string;
}

export interface SiteIntegrationsConfig {
  analytics: SiteGoogleIntegrationConfig;
  search_console: SiteGoogleIntegrationConfig;
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

/** article/free 为旧会话兼容值；workspace 是不注入任何连接或站点上下文的自由对话。 */
export type TaskType = 'siteops' | 'sitebuild' | 'workspace' | 'remote' | 'article' | 'free';

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
  /** 固定的侧栏创建位置；workspace 只用它分组，运行时仍与该连接的站点和凭据隔离。 */
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
  /** 自由对话显式选择的本地工作目录；空/缺省表示会话隔离目录。 */
  workspace_dir?: string;
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
