<script lang="ts">
  import { onMount } from 'svelte';
  import Dropdown from './Dropdown.svelte';
  import { detectEditorLanguage, editorLanguages, editorFormatParsers, type EditorLanguage } from './fileEditor';
  import type { createCodeEditor } from './codeEditorRuntime';

  let { value = $bindable(''), path, indent = '  ', readonly = false, busy = $bindable(false), eol = 'LF', onsave }: {
    value: string; path: string; indent?: string; readonly?: boolean; busy?: boolean; eol?: string; onsave: () => void;
  } = $props();
  let container: HTMLDivElement;
  let editor = $state<ReturnType<typeof createCodeEditor> | null>(null);
  let language = $state<EditorLanguage>('text');
  let indentation = $state('  ');
  let wrap = $state(false);
  let fontSize = $state(13);
  let error = $state('');
  let status = $state({ line: 1, column: 1, lines: 1, undo: false, redo: false });
  let destroyed = false;
  const languageOptions = editorLanguages.map(([value, label]) => ({ value, label }));
  const indentOptions = [{ value: '  ', label: '2 空格' }, { value: '    ', label: '4 空格' }, { value: '\t', label: 'Tab' }];

  onMount(() => {
    language = detectEditorLanguage(path);
    indentation = indent;
    void import('./codeEditorRuntime').then(({ createCodeEditor }) => {
      if (destroyed) return;
      editor = createCodeEditor(container, {
        value, onchange: text => { value = text; }, onstatus: next => { status = next; }, onsave,
      });
      editor.focus();
    }).catch(e => { if (!destroyed) error = `编辑器加载失败：${String(e)}`; });
    return () => { destroyed = true; editor?.destroy(); };
  });

  $effect(() => { editor?.configure(readonly || busy, wrap, indentation); });
  $effect(() => {
    void editor?.language(language).catch(e => { if (!destroyed) error = `语法高亮加载失败：${String(e)}`; });
  });

  async function formatDocument() {
    const parser = editorFormatParsers[language];
    if (!editor || readonly || busy || !parser) return;
    busy = true; error = '';
    const original = value;
    try {
      // Local-only formatting: the file contents never leave the WebView.
      const prettier = await import('prettier/standalone');
      const plugins = parser === 'yaml' ? [await import('prettier/plugins/yaml')]
        : parser === 'html' ? [await import('prettier/plugins/html')]
        : parser === 'css' ? [await import('prettier/plugins/postcss')]
        : parser === 'typescript' ? [await import('prettier/plugins/typescript'), await import('prettier/plugins/estree')]
        : [await import('prettier/plugins/babel'), await import('prettier/plugins/estree')];
      const result = await prettier.format(original, {
        parser, plugins, tabWidth: indentation === '\t' ? 4 : indentation.length,
        useTabs: indentation === '\t', endOfLine: 'lf',
      });
      if (!destroyed && value === original && result !== original) editor.replace(result);
    } catch (e) {
      if (!destroyed) error = `无法格式化，原文未改动：${String(e)}`;
    } finally {
      if (!destroyed) { busy = false; editor?.focus(); }
    }
  }
</script>

<div class="file-code-editor" style:--editor-font-size={`${fontSize}px`} style:--editor-indent={`${indentation === '\t' ? 4 : indentation.length}ch`}>
  <div class="editor-tools" role="toolbar" aria-label="文件编辑工具">
    <div class="editor-picker"><span>语言</span><Dropdown compact menuCompact ariaLabel="文件语言" value={language} options={languageOptions} disabled={busy} onchange={value => { language = value as EditorLanguage; }} /></div>
    <div class="tool-group">
      <button type="button" onclick={() => editor?.undo()} disabled={!status.undo || readonly || busy} title="撤销 · Ctrl / ⌘ Z">撤销</button>
      <button type="button" onclick={() => editor?.redo()} disabled={!status.redo || readonly || busy} title="重做 · Ctrl / ⌘ Shift Z">重做</button>
      <button type="button" onclick={() => editor?.search()} disabled={!editor} title="查找替换 · Ctrl / ⌘ F">查找 / 替换</button>
    </div>
    <button type="button" onclick={formatDocument} disabled={!editor || readonly || busy || !editorFormatParsers[language]}
      title={editorFormatParsers[language] ? '仅修改编辑区，不自动保存；可撤销' : '此语言暂无可靠格式化支持，请使用 Tab / Shift+Tab 调整缩进'}>{busy ? '格式化中…' : '格式化'}</button>
    <div class="tool-group appearance">
      <button type="button" class:active={wrap} aria-pressed={wrap} onclick={() => { wrap = !wrap; }}>自动换行</button>
      <div class="editor-picker"><span>缩进</span><Dropdown compact menuCompact ariaLabel="缩进方式" bind:value={indentation} options={indentOptions} disabled={busy} /></div>
      <button type="button" onclick={() => { fontSize = Math.max(11, fontSize - 1); }} disabled={fontSize <= 11} aria-label="缩小编辑器字号">A−</button>
      <span class="font-size">{fontSize}</span>
      <button type="button" onclick={() => { fontSize = Math.min(22, fontSize + 1); }} disabled={fontSize >= 22} aria-label="放大编辑器字号">A+</button>
    </div>
  </div>
  {#if error}<div class="editor-error" role="alert">{error}</div>{/if}
  <div class="code-surface" bind:this={container}></div>
  {#if !editor && !error}<div class="editor-loading" role="status">正在加载代码编辑器…</div>{/if}
  <div class="editor-status">
    <span>行 {status.line}，列 {status.column}<span class="status-separator">·</span>共 {status.lines} 行</span>
    <span>{eol}<span class="status-separator">·</span>Tab 缩进 / Shift+Tab 反缩进<span class="status-separator">·</span>Ctrl / ⌘ S 保存</span>
  </div>
</div>

<style>
  .file-code-editor { flex: 1; min-height: 0; min-width: 0; display: flex; flex-direction: column; overflow: hidden; background: var(--bg, #fff); }
  .editor-tools { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; padding: 9px 14px; border-bottom: 1px solid var(--border, #e8e5df); background: var(--rail, #f8f7f4); flex: none; }
  .editor-picker, .tool-group { display: inline-flex; align-items: center; gap: 5px; }
  .editor-picker { --chip-h: 28px; }
  .editor-picker > span { color: var(--dim, #77746c); font-size: 11px; }
  .editor-picker :global(.dd-trigger.compact) { font-size: 11.5px; border-radius: 5px; }
  .tool-group { border-left: 1px solid var(--border, #e8e5df); padding-left: 7px; margin-left: 2px; }
  .appearance { margin-left: auto; }
  button { font: inherit; font-size: 11.5px; line-height: 1.3; color: var(--text, #302d28); background: transparent; border: 1px solid transparent; border-radius: 5px; min-height: 28px; padding: 4px 7px; }
  button { cursor: pointer; white-space: nowrap; }
  button:hover:not(:disabled), button.active { background: var(--accent-soft, #f3ebe5); color: var(--accent, #a34635); }
  button:disabled { opacity: .4; cursor: default; }
  button:focus-visible { outline: 2px solid var(--accent, #a34635); outline-offset: 1px; }
  .font-size { font-size: 11px; color: var(--dim, #77746c); font-variant-numeric: tabular-nums; }
  .code-surface { flex: 1; min-height: 0; min-width: 0; overflow: hidden; }
  .code-surface :global(.cm-editor) { height: 100%; color: var(--text, #302d28); background: var(--bg, #fff); font-size: var(--editor-font-size); }
  .code-surface :global(.cm-editor.cm-focused) { outline: none; }
  .code-surface :global(.cm-scroller) { overflow: auto; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; line-height: 1.7; }
  .code-surface :global(.cm-content) { padding: 12px 0 40px; caret-color: var(--text, #302d28); }
  .code-surface :global(.cm-line) { padding: 0 16px 0 10px; }
  .code-surface :global(.cm-gutters) { color: var(--faint, #99958c); background: var(--rail, #f8f7f4); border-right: 1px solid var(--border, #e8e5df); }
  .code-surface :global(.cm-lineNumbers .cm-gutterElement) { padding-left: 12px; min-width: 35px; }
  .code-surface :global(.cm-activeLine), .code-surface :global(.cm-activeLineGutter) { background: color-mix(in srgb, var(--accent, #a34635) 5%, transparent); }
  .code-surface :global(.cm-indent-guide) { background-image: linear-gradient(to right, color-mix(in srgb, var(--dim, #77746c) 22%, transparent) 1px, transparent 1px); background-size: var(--editor-indent) 100%; }
  .code-surface :global(.cm-matchingBracket) { outline: 1px solid var(--accent, #a34635); background: var(--accent-soft, #f3ebe5); }
  .code-surface :global(.cm-panels) { font-family: inherit; font-size: 12px; background: var(--rail, #f8f7f4); color: var(--text, #302d28); border-color: var(--border, #e8e5df); }
  .code-surface :global(.cm-search) { padding: 8px 28px 8px 10px; }
  .code-surface :global(.cm-textfield) { color: var(--text, #302d28); background: var(--bg, #fff); border: 1px solid var(--border, #e8e5df); border-radius: 4px; }
  .code-surface :global(.cm-button) { color: var(--text, #302d28); background: var(--bg, #fff); border: 1px solid var(--border, #e8e5df); border-radius: 4px; text-transform: none; }
  .editor-status { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 4px 12px; padding: 6px 14px; font-size: 10.5px; color: var(--dim, #77746c); border-top: 1px solid var(--border, #e8e5df); flex: none; }
  .status-separator { opacity: .55; padding: 0 7px; }
  .editor-error { padding: 8px 14px; background: var(--err-soft, #fff0ee); color: var(--err, #b23b30); font-size: 12px; white-space: pre-wrap; max-height: 110px; overflow: auto; }
  .editor-loading { padding: 10px 14px; font-size: 12px; color: var(--dim, #77746c); }
  @media (max-width: 720px) { .appearance { margin-left: 0; } .editor-status > span:last-child { display: none; } }
</style>
