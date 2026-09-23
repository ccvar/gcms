import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';

const page=fs.readFileSync(new URL('../routes/+page.svelte',import.meta.url),'utf8');
const fx=fs.readFileSync(new URL('./ModelFx.svelte',import.meta.url),'utf8');
function evaluate(source,start,end,expression,context={}) {
  const from=source.indexOf(start),to=source.indexOf(end,from);
  assert.ok(from>=0&&to>from);
  return vm.runInNewContext(ts.transpileModule(source.slice(from,to)+`\nJSON.stringify(${expression})`,
    {compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText,context);
}
test('new presets use exact model IDs and preserve legacy choices',()=>{
  const lists=JSON.parse(evaluate(page,'  const CLAUDE_MODELS =','  // Grok（订阅通道）',
    '({claude:CLAUDE_MODELS,codex:CODEX_MODELS})'));
  for(const [family,id,label] of [['claude','claude-opus-5-5','Opus 5.5'],
    ['codex','gpt-6-sol','GPT-6 Sol'],['codex','gpt-6-luna','GPT-6 Luna']]) {
    assert.equal(lists[family].find(m=>m.value===id)?.label,label);
    assert.equal(lists[family].filter(m=>m.value===id).length,1);
  }
  assert.ok(lists.claude.some(m=>m.value==='claude-opus-5'));
  assert.ok(lists.codex.some(m=>m.value==='gpt-5.6-sol'));
  assert.equal(lists.codex[0].value,'');
  assert.match(page,/return b === 'claude' \? 'claude-sonnet-5' : ''/);
});
test('Grok 4.7 is selectable without changing existing or default models',()=>{
  const models=JSON.parse(evaluate(page,'  const GROK_MODELS =','  function modelOptsFor','GROK_MODELS'));
  assert.equal(models.find(m=>m.value==='grok-4.7')?.label,'Grok 4.7');
  assert.ok(models.some(m=>m.value==='grok-4.6'));
  assert.equal(models[0].value,'');
});
test('GPT-6 Codex effort levels follow subscription capabilities, Luna excludes ultra',()=>{
  for(const [model,expected] of [['gpt-6-sol',['','low','medium','high','xhigh','max','ultra']],
    ['gpt-6-luna',['','low','medium','high','xhigh','max']]]) {
    const stops=JSON.parse(evaluate(fx,'  type Stop =','  let open =','stops.map(s=>s.v)',
      {value:'codex::'+model,$derived:v=>v}));
    assert.deepEqual(stops,expected);
  }
});
test('Opus 5.5 fast mode is offered consistently by picker and launcher',()=>{
  for(const [source,name] of [[page,'FAST_MODELS'],[fx,'FAST_OK']]) {
    const match=source.match(new RegExp(`const ${name} = (\\[[^;]+\\]);`));
    assert.ok(match);
    assert.ok(vm.runInNewContext(match[1]).includes('claude-opus-5-5'));
  }
});
test('Luna displays max for a retained Sol ultra setting instead of falsely showing automatic',()=>{
  const state=JSON.parse(evaluate(fx,'  const effectiveEffort =','  /** 拖动中的',
    '({effectiveEffort,idx})',{brain:'codex',model:'gpt-6-luna',effort:'ultra',
      stops:[{v:''},{v:'max'}],$derived:v=>v}));
  assert.deepEqual(state,{effectiveEffort:'max',idx:1});
});
