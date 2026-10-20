import capabilities from './apiReasoningCapabilities.json' with { type: 'json' };

// Shared with the Rust request builder: API tiers are not CLI tiers. Verified
// against official docs on 2026-10-20; unknown models keep provider defaults.
// https://platform.claude.com/docs/en/build-with-claude/effort
// https://platform.claude.com/docs/en/build-with-claude/thinking
// https://developers.openai.com/api/docs/guides/reasoning#reasoning-effort
// https://developers.openai.com/api/docs/models (individual catalogued models)
// Chat tiers reflect Pilot's function-calling workflow: GPT-6 Sol/Luna only
// support tools at none, and Astra/6.1 Sol require Responses for tools.
export function apiReasoningModelMatches(model: string, id: string): boolean {
  return model === id || (model.startsWith(id + '-') && /^\d{4}-\d{2}-\d{2}$|^\d{8}$/.test(model.slice(id.length + 1)));
}

export function apiReasoningLevels(protocol: string | undefined, model: string): string[] {
  const capability = capabilities.find(entry => entry.protocol === protocol
    && entry.models.some(id => apiReasoningModelMatches(model, id)));
  return capability ? [...capability.levels] : [];
}

/** API choices are namespaced so upgrading cannot activate previously ignored CLI effort. */
export function apiEffortValue(effort: string): string {
  return effort.startsWith('api:') ? effort.slice(4) : '';
}

/** Used on intentional model changes; never silently writes persisted choices on mount. */
export function normalizeApiEffort(protocol: string | undefined, model: string, effort: string): string {
  return apiReasoningLevels(protocol, model).includes(apiEffortValue(effort)) ? effort : '';
}
