import { test } from 'node:test';
import assert from 'node:assert/strict';
import { inspectEditorText, serializeEditorText, detectEditorLanguage } from './fileEditor.ts';
import { languageExtension } from './codeEditorRuntime.ts';
import { EditorState } from '@codemirror/state';
import { foldable, getIndentation, indentUnit } from '@codemirror/language';

test('detect server configs and common code without assuming every .conf is Nginx', () => {
  for (const [path, language] of [
    ['/etc/caddy/Caddyfile', 'caddy'], ['/etc/caddy/conf.d/site.caddy', 'caddy'],
    ['/etc/nginx/conf.d/site.conf', 'nginx'], ['/tmp/app.conf', 'text'], ['/tmp/.env.production', 'ini'],
    ['test.JSON', 'json'], ['compose.yaml', 'yaml'], ['main.tsx', 'typescript'], ['app.py', 'python'],
  ]) assert.equal(detectEditorLanguage(path), language);
});

test('round-trip UTF-8 BOM, CRLF, LF, CR, whitespace and final newline unchanged', () => {
  for (const raw of ['', '\uFEFF', 'x', '  x  \n\n', '\uFEFF{\r\n\t"a": 1\r\n}\r\n', 'a\rb\r']) {
    const format = inspectEditorText(raw);
    assert.equal(format.mixed, false);
    assert.equal(serializeEditorText(format.text, format), raw);
  }
});

test('mixed newlines are explicitly flagged, indentation is inferred without changing content', () => {
  const raw = 'site {\r\n\treverse_proxy :9000\n}\n';
  const format = inspectEditorText(raw);
  assert.equal(format.mixed, true);
  assert.equal(format.eol, '\r\n');
  assert.equal(format.indent, '\t');
  assert.equal(inspectEditorText('a:\n  b: 1\n').indent, '  ');
  assert.equal(inspectEditorText('a {\n    b\n        c\n}').indent, '    ');
});

test('Caddy folding ignores placeholders, quoted braces and comments; nesting indents', async () => {
  const doc = 'example.com {\n  header X-Test "}" # }\n  redir https://example.com{uri}\n  handle {\n    respond "ok"\n  }\n}';
  const state = EditorState.create({ doc, extensions: [await languageExtension('caddy'), indentUnit.of('  ')] });
  assert.deepEqual(foldable(state, 0, state.doc.line(1).to), { from: 13, to: doc.length - 1 });
  assert.equal(getIndentation(state, state.doc.line(2).from), 2);
  assert.equal(getIndentation(state, state.doc.line(5).from), 4);
  assert.equal(getIndentation(state, state.doc.line(6).from), 2);
  assert.equal(foldable(state, state.doc.line(3).from, state.doc.line(3).to), null);
});

test('all offered language modules load', async () => {
  const { editorLanguages } = await import('./fileEditor.ts');
  for (const [id] of editorLanguages) assert.ok(await languageExtension(id));
});

test('explicit JSON formatting preserves large numbers and invalid input is rejected', async () => {
  const { format } = await import('prettier/standalone');
  const plugins = [await import('prettier/plugins/babel'), await import('prettier/plugins/estree')];
  const result = await format('{"id":12345678901234567890,"enabled":true}', { parser: 'json', plugins });
  assert.match(result, /12345678901234567890/);
  await assert.rejects(format('{"invalid":}', { parser: 'json', plugins }));
});
