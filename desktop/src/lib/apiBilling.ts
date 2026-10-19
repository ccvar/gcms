export type BillingProvider = 'openai' | 'anthropic' | 'unsupported';

export interface BillingSettings {
  provider: BillingProvider;
  credential_configured: boolean;
  billing_url: string | null;
}

export interface BillingSnapshot {
  fetched_at: string;
  period_start: string;
  period_end: string;
  currency: 'USD';
  month_cost: number | null;
  today_cost: number | null;
  month_input_tokens: number | null;
  month_output_tokens: number | null;
  cost_error: string | null;
  usage_error: string | null;
}

/** Missing data must never look like a verified zero balance or zero spend. */
export function formatBillingCost(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—';
  if (value > 0 && value < 0.000001) return '< $0.000001';
  if (value < 0 && value > -0.000001) return '> -$0.000001';
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 6 }).format(value);
}

export function formatBillingTokens(value: number | null | undefined): string {
  if (value == null || !Number.isSafeInteger(value) || value < 0) return '—';
  return new Intl.NumberFormat('en-US').format(value);
}

export function formatBillingTime(value: string | null | undefined): string {
  if (!value) return '—';
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '—';
  return `${date.toISOString().slice(5, 16).replace('T', ' ')} UTC`;
}

export function billingPeriodLabel(snapshot: Pick<BillingSnapshot, 'period_start' | 'period_end'>): string {
  const start = new Date(snapshot.period_start), end = new Date(snapshot.period_end);
  if (!Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime())) return '组织账户 · UTC';
  return `${start.toISOString().slice(0, 10)} 至 ${end.toISOString().slice(0, 16).replace('T', ' ')} UTC`;
}

/** Only allow official billing destinations even if a malformed IPC response reaches the UI. */
export function safeBillingUrl(settings: BillingSettings | null): string | null {
  if (!settings?.billing_url) return null;
  try {
    const url = new URL(settings.billing_url);
    if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || url.port) return null;
    if (settings.provider === 'openai' && url.hostname === 'platform.openai.com' && (url.pathname === '/settings/organization/billing' || url.pathname.startsWith('/settings/organization/billing/'))) return url.href;
    if (settings.provider === 'anthropic' && ['platform.claude.com', 'console.anthropic.com'].includes(url.hostname) && url.pathname === '/settings/billing') return url.href;
  } catch { /* Not a usable official billing link. */ }
  return null;
}
