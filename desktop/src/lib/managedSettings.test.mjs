import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import {managedModeName, managedModeSwitchNotice, settingLabels, validateManagedPatch} from './managedSettings.ts';

const targets=[{id:'a',site_name:'Alpha',brain:'claude',model:'opus',effort:'high',fallback_brain:'codex',fallback_model:'gpt-6-sol',fallback_effort:'high'}];
test('bulk settings validates integers and per-site fallback conflicts',()=>{
  assert.equal(validateManagedPatch({weekly_post_limit:8},targets),'');
  for(const patch of [{},{weekly_post_limit:0},{weekly_post_limit:51},{weekly_edit_limit:21},{token_weekly_budget:-1},{weekly_post_limit:1.5}])assert.ok(validateManagedPatch(patch,targets));
  assert.match(validateManagedPatch({primary:{brain:'codex',model:'gpt-6-sol',effort:'high'}},targets),/Alpha/);
  assert.equal(validateManagedPatch({primary:{brain:'codex',model:'gpt-6-sol',effort:'high'},fallback:{brain:'',model:'',effort:''}},targets),'');
});
test('mode settings allow both directions and reject unknown modes',()=>{
  assert.equal(settingLabels.mode,'托管模式');
  for(const mode of ['plan','growth']){
    assert.equal(validateManagedPatch({mode},targets),'');
    assert.equal(validateManagedPatch({mode,weekly_post_limit:8},targets),'');
  }
  for(const mode of ['data','hybrid','',null]){
    assert.match(validateManagedPatch({mode},targets),/托管模式无效/);
  }
});
const page=fs.readFileSync(new URL('../routes/+page.svelte',import.meta.url),'utf8');
function code(from,to){const a=page.indexOf(from),b=page.indexOf(to,a);assert.ok(a>=0&&b>a);return ts.transpileModule(page.slice(a,b),{compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;}
function modeContext(overrides={}){
  const calls=[],messages=[],refreshes=[];
  const ctx={
    managedModeBusy:{},managedEditBusy:false,managedModeOf:m=>m.mode==='growth'?'growth':'plan',
    managedModeName,managedModeSwitchNotice,mOpenPanel:{a:'growth'},
    confirmDialog:async()=>true,
    invoke:async(command,args)=>{calls.push({command,args});return [{id:'a',error:null}];},
    say:(...args)=>messages.push(args),loadManaged:async()=>{refreshes.push('managed');},
    loadTasks:async()=>{refreshes.push('tasks');},...overrides,
  };
  vm.createContext(ctx);
  vm.runInContext(code('  async function changeManagedMode(', '  async function saveManagedSettings('),ctx);
  return {ctx,calls,messages,refreshes};
}
test('mode change cancellation preserves mode and performs no backend calls',async()=>{
  const site={id:'a',conn_id:'original',site_name:'Alpha',mode:'growth'};
  const {ctx,calls,refreshes}=modeContext({confirmDialog:async(message,opts)=>{
    assert.match(message,/从增长托管切换为计划托管/);
    assert.match(message,/不会立即执行任务/);
    assert.equal(opts.confirmText,'切换为计划托管');
    return false;
  }});
  await ctx.changeManagedMode(site,'plan');
  assert.equal(site.mode,'growth');assert.equal(calls.length,0);assert.equal(refreshes.length,0);
  assert.equal(ctx.mOpenPanel.a,'growth');assert.equal(ctx.managedModeBusy.a,false);
});
test('mode change snapshots site and connection in both directions without optimistic mutation',async()=>{
  for(const [old,next] of [['growth','plan'],['plan','growth']]){
    const site={id:'a',conn_id:'original',site_name:'Alpha',mode:old};
    const {ctx,calls,messages,refreshes}=modeContext({confirmDialog:async()=>{
      site.conn_id='different-workspace';return true;
    }});
    await ctx.changeManagedMode(site,next);
    assert.deepEqual(JSON.parse(JSON.stringify(calls)),[
      {command:'managed_update_settings',args:{connId:'original',ids:['a'],patch:{mode:next}}},
    ]);
    assert.equal(site.mode,old);assert.equal(ctx.mOpenPanel.a,undefined);
    assert.match(messages[0][0],new RegExp(managedModeName(next)));
    assert.deepEqual(refreshes,['managed','tasks']);assert.equal(ctx.managedModeBusy.a,false);
  }
});
test('mode change guards duplicates, same-mode and invalid selections',async()=>{
  for(const [overrides,next] of [
    [{managedModeBusy:{a:true}},'plan'],[{managedEditBusy:true},'plan'],
    [{},'growth'],[{},'invalid'],
  ]){
    const {ctx,calls}=modeContext({...overrides,confirmDialog:async()=>assert.fail('unexpected confirmation')});
    await ctx.changeManagedMode({id:'a',mode:'growth'},next);
    assert.equal(calls.length,0);
  }
});
test('failed or missing mode result refreshes persisted state and does not close old panel',async()=>{
  for(const invoke of [
    async()=>[{id:'a',error:'任务正在运行'}],
    async()=>[],
    async()=>{throw Error('offline');},
  ]){
    const site={id:'a',conn_id:'original',site_name:'Alpha',mode:'growth'};
    const {ctx,messages,refreshes}=modeContext({invoke});
    await ctx.changeManagedMode(site,'plan');
    assert.equal(site.mode,'growth');assert.equal(ctx.mOpenPanel.a,'growth');
    assert.equal(messages.length,1);assert.match(messages[0][0],/切换托管模式失败/);
    assert.equal(messages[0][1],'err');assert.deepEqual(refreshes,['managed','tasks']);
    assert.equal(ctx.managedModeBusy.a,false);
  }
});
test('multi-site creation snapshots connection, keeps independent plans and retries only failures',async()=>{
  const calls=[];let first=true;
  const ctx={mwConnId:'original',activeConnId:'original',mwSites:['a','b'],mwSite:'a',mwOpen:true,mwCreateError:'',mwCreateProgress:'',
    mwSiteDrafts:{a:{plan:'Alpha plan',prompts:{daily:'Alpha custom'},edited:{daily:true}},b:{plan:'Beta plan',prompts:{},edited:{}}},
    sites:[{slug:'a',name:'Alpha'},{slug:'b',name:'Beta'}],saveMwSiteDraft:()=>{},blankManagedPrompts:()=>({daily:'',audit:'',report:''}),
    loadManaged:async()=>{},loadTasks:async()=>{},say:()=>{},
    invoke:async(command,args)=>{calls.push({command,args});ctx.mwConnId='changed';if(args.siteSlug==='b'&&first){first=false;throw Error('offline');}}};
  vm.createContext(ctx);vm.runInContext(code('  async function mwCreateForSites(', '  interface TaskForm'),ctx);
  await ctx.mwCreateForSites('managed_enable',{brain:'claude',plan:'wrong shared plan',customDailyPrompt:'wrong shared prompt'});
  assert.equal(calls.length,2);assert.ok(calls.every(c=>c.args.connId==='original'));
  assert.equal(calls[0].args.plan,'Alpha plan');assert.equal(calls[1].args.plan,'Beta plan');
  assert.equal(calls[0].args.customDailyPrompt,'Alpha custom');assert.equal(calls[1].args.customDailyPrompt,'');
  assert.equal(ctx.mwSites.join(','),'b');assert.equal(ctx.mwSite,'b');assert.equal(ctx.mwOpen,true);assert.match(ctx.mwCreateError,/Beta.*offline/);
  ctx.mwConnId='original';await ctx.mwCreateForSites('managed_enable',{});
  assert.equal(calls.length,3);assert.equal(calls[2].args.siteSlug,'b');assert.equal(ctx.mwOpen,false);
});
test('managed delete cancellation never invokes backend',async()=>{
  for(const yes of [false,true]){
    const calls=[];const ctx={confirmDialog:async(message,opts)=>{assert.match(message,/站点内容与草稿一概不动/);assert.equal(opts.confirmText,'删除托管');return yes;},invoke:async(...a)=>calls.push(a),loadManaged:async()=>{},loadTasks:async()=>{},say:()=>{}};
    vm.createContext(ctx);vm.runInContext(code('  async function disableManaged(', '  async function saveManagedPlan('),ctx);
    await ctx.disableManaged({id:'a',site_name:'Alpha'});assert.equal(calls.length,yes?1:0);
  }
});
