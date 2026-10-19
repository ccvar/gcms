export type ApiProtocol = 'openai_responses' | 'anthropic_messages' | 'openai_chat';

export interface ApiProfile {
  id: string;
  name: string;
  protocol: ApiProtocol;
  base_url: string;
  enabled: boolean;
  models: string[];
  default_model: string;
  max_output_tokens: number;
  key_configured: boolean;
}

export type ApiProfileInput = Omit<ApiProfile, 'id' | 'key_configured'> & { id?: string };

export interface ApiTestResult {
  ok: boolean;
  message: string;
  model_count: number;
}

export const API_PROTOCOL_OPTIONS: { value: ApiProtocol; label: string; sub: string; baseUrl: string; name: string; icon?: string }[] = [
  { value: 'openai_responses', label: 'OpenAI', sub: 'Responses API', baseUrl: 'https://api.openai.com/v1', name: 'OpenAI 官方', icon: 'openai' },
  { value: 'anthropic_messages', label: 'Anthropic', sub: 'Claude Messages API', baseUrl: 'https://api.anthropic.com/v1', name: 'Claude 官方', icon: 'claude' },
  { value: 'openai_chat', label: '其他兼容接口', sub: 'Chat Completions API', baseUrl: '', name: '自定义 API' },
];

export function apiProtocolLabel(protocol: ApiProtocol): string {
  return API_PROTOCOL_OPTIONS.find(option => option.value === protocol)?.sub ?? protocol;
}

export function parseApiModels(text: string): string[] {
  return [...new Set(text.split(/[\n,，]+/).map(model => model.trim()).filter(Boolean))];
}

export function normalizeApiEndpoint(endpoint: string): string {
  return endpoint.trim().replace(/\/+$/, '');
}

export function apiEndpointChanged(profile: ApiProfile | undefined, input: Pick<ApiProfileInput, 'base_url' | 'protocol'>): boolean {
  return !!profile && (normalizeApiEndpoint(profile.base_url) !== normalizeApiEndpoint(input.base_url) || profile.protocol !== input.protocol);
}

export function apiEndpointHost(endpoint: string): string {
  try { return new URL(endpoint).host; } catch { return endpoint; }
}

/** This object is deliberately allow-listed: an editor's key never enters profile metadata. */
export function apiProfileInput(profile: ApiProfile): ApiProfileInput {
  return {
    id: profile.id,
    name: profile.name,
    protocol: profile.protocol,
    base_url: profile.base_url,
    enabled: profile.enabled,
    models: [...profile.models],
    default_model: profile.default_model,
    max_output_tokens: profile.max_output_tokens,
  };
}

export function validateApiProfileInput(input: ApiProfileInput): string {
  if (!input.name.trim()) return '请填写连接名称。';
  if (!API_PROTOCOL_OPTIONS.some(option => option.value === input.protocol)) return '请选择支持的 API 协议。';
  let url: URL;
  try { url = new URL(input.base_url.trim()); } catch { return '请填写有效的 API 接口地址。'; }
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback)) return 'API 接口必须使用 HTTPS；本机地址可使用 HTTP。';
  if (url.username || url.password || url.search || url.hash) return '接口地址不能包含账号、密码、查询参数或片段。';
  if (!Number.isInteger(input.max_output_tokens) || input.max_output_tokens < 1 || input.max_output_tokens > 1_000_000) return '最大输出需为 1–1,000,000 之间的整数。';
  if (input.models.some(model => /\s/.test(model))) return '模型 ID 不能包含空白字符，请每行填写一个模型。';
  if (input.default_model && !input.models.includes(input.default_model)) return '请从已添加的模型中选择默认模型。';
  return '';
}
