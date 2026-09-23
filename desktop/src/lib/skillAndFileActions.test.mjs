import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';

const page = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
function section(start, end) {
  const a = page.indexOf(start), b = page.indexOf(end, a);
  assert.ok(a >= 0 && b > a);
  return ts.transpileModule(page.slice(a, b), {compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;
}
const row = {path:'/config/site.json',name:'site.json',dir:false};
function renameHarness() {
  const calls = [], ctx = {$state:v=>v, sftpConnId:'server-a', sftpSel:'', fctxMenu:{},
    sftpOpenDirs:new Set(), isSftpDeleting:()=>false, parentOf:p=>p.slice(0,p.lastIndexOf('/')),
    sftpJoin:(dir,name)=>dir+'/'+name, loadDir:async()=>{}, say:()=>{},
    invoke:async (command,args)=>{calls.push({command,args});return [];}};
  vm.createContext(ctx);
  vm.runInContext(section('  let fileRename =','  $effect(() => {\n    if (fileRename'),ctx);
  ctx.getEdit=()=>vm.runInContext('fileRename',ctx);
  ctx.openRename(row);
  return {ctx,calls};
}
test('inline rename validates names, skips unchanged names, and prevents overwrite',async()=>{
  for (const name of ['', '.', '..', 'x/y', 'bad\0name']) {
    const {ctx,calls}=renameHarness(); ctx.getEdit().value=name;
    await ctx.saveFileRename(); assert.equal(calls.length,0); assert.ok(ctx.getEdit().err);
  }
  const {ctx,calls}=renameHarness();
  await ctx.saveFileRename(); assert.equal(ctx.getEdit(),null); assert.equal(calls.length,0);
  ctx.openRename(row);ctx.getEdit().value='exists.json';
  ctx.invoke=async()=>[{name:'exists.json'}];
  await ctx.saveFileRename(); assert.match(ctx.getEdit().err,/同名/); assert.equal(ctx.getEdit().busy,false);
});
test('inline rename submits once, preserves server target and newer edit on async completion',async()=>{
  const {ctx,calls}=renameHarness(); let finish;
  ctx.invoke=async(command,args)=>{calls.push({command,args});if(command==='sftp_list')return [];return new Promise(r=>finish=r);};
  ctx.getEdit().value='new.json'; const pending=ctx.saveFileRename();
  await Promise.resolve(); await ctx.saveFileRename();
  assert.equal(calls.filter(c=>c.command==='sftp_rename').length,1);
  assert.deepEqual(JSON.parse(JSON.stringify(calls[1].args)),{connId:'server-a',from:row.path,to:'/config/new.json'});
  ctx.sftpConnId='server-b'; vm.runInContext('fileRename = null',ctx);ctx.openRename(row);
  finish(); await pending; assert.equal(ctx.getEdit().connId,'server-b');
});
test('inline rename keeps failures editable',async()=>{
  const {ctx}=renameHarness();ctx.getEdit().value='new.json';ctx.invoke=async()=>{throw Error('permission denied');};
  await ctx.saveFileRename();assert.match(ctx.getEdit().err,/permission denied/);assert.equal(ctx.getEdit().busy,false);
});
test('skill removal requires explicit confirmation; cancellation never calls backend',async()=>{
  for(const answer of [false,true]) {
    const calls=[],skill={id:'demo',name:'Demo'};
    const ctx={skillToggleBusy:{},installedSkills:[skill],lSkillId:'demo',
      confirmDialog:async(message,options)=>{calls.push({message,options});return answer;},
      invoke:async(command,args)=>{calls.push({command,args});},say:()=>{}};
    vm.createContext(ctx);vm.runInContext(section('  async function removeInstalledSkill','  // ---------- 技能包更新'),ctx);
    await ctx.removeInstalledSkill(skill);
    assert.equal(calls[0].options.confirmText,'移除');
    assert.match(calls[0].message,/原始技能包和对话记录会保留/);
    assert.equal(calls.filter(c=>c.command==='remove_skill').length,answer?1:0);
    assert.equal(ctx.installedSkills.length,answer?0:1);
    assert.equal(ctx.skillToggleBusy.demo,undefined);
  }
});
test('remote deletion requires confirmation and keeps the original server if switched while confirming',async()=>{
  for(const answer of [false,true]) {
    const calls=[];
    const ctx={sftpConnId:'server-a',isSftpDeleting:()=>false,sftpDeleting:new Set(),
      confirmDialog:async(message,options)=>{calls.push({message,options});ctx.sftpConnId='server-b';return answer;},
      sftpDeletingKey:(id,path)=>id+':'+path,
      invoke:async(command,args)=>calls.push({command,args}),say:()=>{}};
    vm.createContext(ctx);vm.runInContext(section('  async function sftpDelete(', '  async function sftpDownload('),ctx);
    await ctx.sftpDelete(row);
    assert.equal(calls[0].options.confirmText,'删除');
    const removed=calls.filter(c=>c.command==='sftp_remove');
    assert.equal(removed.length,answer?1:0);
    if(answer)assert.equal(removed[0].args.connId,'server-a');
  }
});
