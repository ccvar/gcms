import { normalizeApiEffort } from './apiReasoning';
import type { ApiProtocol } from './apiProfiles';

export type ModelOption = {
  value: string;
  label: string;
  sub?: string;
  icon?: string;
  disabled?: boolean;
  apiProtocol?: ApiProtocol;
};

/** Switching transports must not silently inherit CLI-only reasoning settings. */
export function effortAfterModelPick(previous: string, next: string, effort: string, options: ModelOption[]): string {
  const wasApi = previous.startsWith('api:');
  const isApi = next.startsWith('api:');
  if (wasApi !== isApi) return '';
  if (!isApi) return effort;
  const protocol = options.find(option => option.value === next)?.apiProtocol;
  const previousProtocol = options.find(option => option.value === previous)?.apiProtocol;
  if (!protocol || protocol !== previousProtocol) return '';
  const separator = next.indexOf('::');
  return normalizeApiEffort(protocol, separator >= 0 ? next.slice(separator + 2) : '', effort);
}
