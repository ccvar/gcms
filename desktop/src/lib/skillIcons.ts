import { invoke } from '@tauri-apps/api/core';

const cache = new Map<string, { expires: number; promise: Promise<string[]> }>();

export function declaredSkillIcons(html: string, domain: string): string[] {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  const urls: string[] = [];
  for (const link of doc.querySelectorAll('link[rel][href]')) {
    const rel = (link.getAttribute('rel') || '').toLowerCase().split(/\s+/);
    if (!rel.includes('icon') && !rel.includes('apple-touch-icon')) continue;
    try {
      const url = new URL(link.getAttribute('href') || '', `https://${domain}/`);
      if (url.protocol === 'https:' && !url.username && !url.password && !url.port
        && [domain, `www.${domain}`].includes(url.hostname)) urls.push(url.href);
    } catch { /* Invalid or non-web icon; continue with the next candidate. */ }
  }
  return [...new Set(urls)];
}

export function loadSkillIcons(domain: string): Promise<string[]> {
  const saved = cache.get(domain);
  if (saved && saved.expires > Date.now()) return saved.promise;
  const promise = invoke<string>('skill_site_head', { domain })
    .then(html => declaredSkillIcons(html, domain)).catch(() => []);
  cache.set(domain, { expires: Date.now() + 5 * 60_000, promise });
  return promise;
}
