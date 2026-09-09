export const editorLanguages = [
  ['text', '纯文本'], ['caddy', 'Caddyfile'], ['nginx', 'Nginx'],
  ['json', 'JSON'], ['yaml', 'YAML'], ['toml', 'TOML'], ['ini', 'INI / .env'],
  ['shell', 'Shell'], ['javascript', 'JavaScript'], ['typescript', 'TypeScript'],
  ['html', 'HTML'], ['css', 'CSS'], ['xml', 'XML'], ['markdown', 'Markdown'], ['python', 'Python'],
] as const;
export type EditorLanguage = typeof editorLanguages[number][0];

export function detectEditorLanguage(path: string): EditorLanguage {
  const name = path.split('/').pop()?.toLowerCase() || '';
  if (name === 'caddyfile' || name.endsWith('.caddy') || name.startsWith('caddyfile.')) return 'caddy';
  if (name === 'nginx.conf' || (/\/nginx\//i.test(path) && name.endsWith('.conf'))) return 'nginx';
  if (/\.(json|jsonc|json5)$/.test(name)) return 'json';
  if (/\.ya?ml$/.test(name)) return 'yaml';
  if (/\.toml$/.test(name)) return 'toml';
  if (/\.(ini|properties|service|socket|timer)$/.test(name) || /^\.env(?:\.|$)/.test(name)) return 'ini';
  if (/\.(sh|bash|zsh)$/.test(name) || /^\.(bashrc|zshrc|profile|bash_profile)$/.test(name)) return 'shell';
  if (/\.[cm]?jsx?$/.test(name)) return 'javascript';
  if (/\.[cm]?tsx?$/.test(name)) return 'typescript';
  if (/\.html?$/.test(name)) return 'html';
  if (/\.css$/.test(name)) return 'css';
  if (/\.(xml|svg)$/.test(name)) return 'xml';
  if (/\.(md|markdown)$/.test(name)) return 'markdown';
  if (/\.py$/.test(name)) return 'python';
  return 'text';
}

export function inspectEditorText(raw: string) {
  const bom = raw.startsWith('\uFEFF');
  const body = bom ? raw.slice(1) : raw;
  const breaks = body.match(/\r\n|\r|\n/g) || [];
  const eol = breaks[0] || '\n';
  const text = body.replace(/\r\n|\r/g, '\n');
  const indents = text.split('\n').map(line => line.match(/^[\t ]+(?=\S)/)?.[0]).filter(Boolean) as string[];
  const tabs = indents.filter(indent => indent.startsWith('\t')).length;
  const spaces = indents.filter(indent => /^ +$/.test(indent)).map(indent => indent.length);
  const indent = tabs > spaces.length ? '\t' : spaces.some(n => n % 4 !== 0) ? '  ' : spaces.length ? '    ' : '  ';
  return { text, bom, eol, mixed: new Set(breaks).size > 1, indent };
}

export function serializeEditorText(text: string, format: { bom: boolean; eol: string }) {
  return (format.bom ? '\uFEFF' : '') + text.replace(/\r\n|\r|\n/g, format.eol);
}

export const editorFormatParsers: Partial<Record<EditorLanguage, string>> = {
  json: 'json', yaml: 'yaml', javascript: 'babel', typescript: 'typescript', html: 'html', css: 'css',
};
