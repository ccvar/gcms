import { basicSetup } from 'codemirror';
import { Compartment, EditorState, Prec, type Extension } from '@codemirror/state';
import { EditorView, keymap, Decoration, ViewPlugin, type DecorationSet, type ViewUpdate } from '@codemirror/view';
import { indentWithTab, undo, redo, undoDepth, redoDepth } from '@codemirror/commands';
import { StreamLanguage, indentUnit, foldService, type StreamParser } from '@codemirror/language';
import { openSearchPanel } from '@codemirror/search';
import type { EditorLanguage } from './fileEditor';

// Caddy's block delimiters must not include placeholders such as {uri}.
const caddy: StreamParser<{ depth: number; quote: string }> = {
  startState: () => ({ depth: 0, quote: '' }),
  token(stream, state) {
    if (state.quote) {
      let escaped = false;
      while (!stream.eol()) {
        const char = stream.next();
        if (char === state.quote && !escaped) { state.quote = ''; break; }
        escaped = char === '\\' && !escaped;
      }
      return 'string';
    }
    if (stream.eatSpace()) return null;
    if (stream.match(/#.*/)) return 'comment';
    if (stream.match(/\{[^\s{}]+\}/)) return 'variableName';
    if (stream.match(/\{/)) { state.depth++; return 'bracket'; }
    if (stream.match(/\}/)) { state.depth = Math.max(0, state.depth - 1); return 'bracket'; }
    if (stream.match(/["`]/)) { state.quote = stream.current(); return 'string'; }
    if (stream.match(/@[\w-]+/)) return 'variableName';
    if (stream.match(/\b\d+(?:\.\d+)*(?:ms|s|m|h|kb|mb)?\b/i)) return 'number';
    if (stream.match(/(?:https?:\/\/)?[\w.*-]+\.[\w.*:-]+/)) return 'link';
    if (stream.match(/[\w-]+/)) return 'keyword';
    stream.next(); return null;
  },
  indent: (state, after, context) => Math.max(0, state.depth - (/^\s*}/.test(after) ? 1 : 0)) * context.unit,
  languageData: { commentTokens: { line: '#' }, indentOnInput: /^\s*\}$/ },
};

// A conservative block fold for legacy server configurations: quoted strings,
// comments and inline placeholders don't count as block delimiters.
const configFolding = foldService.of((state, from, to) => {
  const first = state.sliceDoc(from, to);
  if (!/\{\s*(?:#.*)?$/.test(first)) return null;
  let depth = 0, quote = '', escaped = false, comment = false, opening = -1;
  for (let lineNo = state.doc.lineAt(from).number; lineNo <= state.doc.lines; lineNo++) {
    const line = state.doc.line(lineNo);
    comment = false;
    for (let i = 0; i < line.length; i++) {
      const char = line.text[i];
      if (comment) break;
      if (quote) {
        if (char === quote && !escaped) quote = '';
        escaped = char === '\\' && !escaped;
        continue;
      }
      if (char === '#') { comment = true; continue; }
      if (char === '"' || char === "'" || char === '`') { quote = char; escaped = false; continue; }
      if (char === '{') {
        if (depth === 0) opening = line.from + i;
        depth++;
      } else if (char === '}' && depth > 0 && --depth === 0) {
        if (opening >= from && opening <= to && line.from + i > to) return { from: opening + 1, to: line.from + i };
        opening = -1;
      }
    }
    if (opening < 0 && line.from >= to) return null;
  }
  return null;
});

export async function languageExtension(language: EditorLanguage): Promise<Extension> {
  switch (language) {
    case 'caddy': return [StreamLanguage.define(caddy), configFolding];
    case 'nginx': return [StreamLanguage.define((await import('@codemirror/legacy-modes/mode/nginx')).nginx), configFolding];
    case 'json': return (await import('@codemirror/lang-json')).json();
    case 'yaml': return (await import('@codemirror/lang-yaml')).yaml();
    case 'toml': return StreamLanguage.define((await import('@codemirror/legacy-modes/mode/toml')).toml);
    case 'ini': return StreamLanguage.define((await import('@codemirror/legacy-modes/mode/properties')).properties);
    case 'shell': return StreamLanguage.define((await import('@codemirror/legacy-modes/mode/shell')).shell);
    case 'javascript': case 'typescript': return (await import('@codemirror/lang-javascript')).javascript({ jsx: true, typescript: language === 'typescript' });
    case 'html': return (await import('@codemirror/lang-html')).html();
    case 'css': return (await import('@codemirror/lang-css')).css();
    case 'xml': return (await import('@codemirror/lang-xml')).xml();
    case 'markdown': return (await import('@codemirror/lang-markdown')).markdown();
    case 'python': return (await import('@codemirror/lang-python')).python();
    default: return [];
  }
}

function indentationGuides(view: EditorView) {
  const marks = [];
  let lastLine = 0;
  for (const range of view.visibleRanges) {
    for (let pos = range.from; pos <= range.to;) {
      const line = view.state.doc.lineAt(pos);
      if (line.number !== lastLine) {
        const length = line.text.match(/^[\t ]+/)?.[0].length || 0;
        if (length) marks.push(Decoration.mark({ class: 'cm-indent-guide' }).range(line.from, line.from + length));
        lastLine = line.number;
      }
      pos = line.to + 1;
    }
  }
  return Decoration.set(marks);
}
const guides = ViewPlugin.fromClass(class {
  decorations: DecorationSet;
  constructor(view: EditorView) { this.decorations = indentationGuides(view); }
  update(update: ViewUpdate) {
    if (update.docChanged || update.viewportChanged) this.decorations = indentationGuides(update.view);
  }
}, { decorations: value => value.decorations });

export function createCodeEditor(parent: HTMLElement, options: {
  value: string;
  onchange: (text: string) => void;
  onstatus: (status: { line: number; column: number; lines: number; undo: boolean; redo: boolean }) => void;
  onsave: () => void;
}) {
  const language = new Compartment(), settings = new Compartment();
  const status = (state: EditorState) => {
    const head = state.selection.main.head, line = state.doc.lineAt(head);
    options.onstatus({ line: line.number, column: head - line.from + 1, lines: state.doc.lines, undo: undoDepth(state) > 0, redo: redoDepth(state) > 0 });
  };
  const view = new EditorView({ parent, state: EditorState.create({ doc: options.value, extensions: [
    basicSetup, guides, language.of([]), settings.of([]),
    Prec.highest(keymap.of([{ key: 'Mod-s', run: () => { options.onsave(); return true; }, preventDefault: true }, indentWithTab])),
    EditorView.contentAttributes.of({ 'aria-label': '文件内容', spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off' }),
    EditorState.phrases.of({
      'Find': '查找', 'Replace': '替换为', 'next': '下一个', 'previous': '上一个', 'all': '全选匹配',
      'match case': '区分大小写', 'by word': '全字匹配', 'regexp': '正则表达式',
      'replace': '替换', 'replace all': '全部替换', 'close': '关闭',
      'Go to line': '跳转到行', 'go': '跳转', 'Fold line': '折叠此行', 'Unfold line': '展开此行',
    }),
    EditorView.updateListener.of(update => {
      if (update.docChanged) options.onchange(update.state.doc.toString());
      if (update.docChanged || update.selectionSet || update.transactions.length) status(update.state);
    }),
  ] }) });
  status(view.state);
  let request = 0, destroyed = false;
  return {
    view,
    async language(value: EditorLanguage) {
      const id = ++request;
      const extension = await languageExtension(value);
      if (!destroyed && request === id) view.dispatch({ effects: language.reconfigure(extension) });
    },
    configure(readonly: boolean, wrap: boolean, indent: string) {
      view.dispatch({ effects: settings.reconfigure([
        EditorState.readOnly.of(readonly), EditorView.editable.of(!readonly),
        indentUnit.of(indent), EditorState.tabSize.of(indent === '\t' ? 4 : indent.length),
        wrap ? EditorView.lineWrapping : [],
      ]) });
    },
    replace(text: string) { view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, userEvent: 'input.format' }); },
    search() { openSearchPanel(view); },
    undo() { undo(view); view.focus(); },
    redo() { redo(view); view.focus(); },
    focus() { view.focus(); },
    destroy() { destroyed = true; ++request; view.destroy(); },
  };
}
