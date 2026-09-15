import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import {leaves,pane,removePane,replacePane,splitPane} from './conversationLayout.ts';

const source=fs.readFileSync(new URL('../routes/+page.svelte',import.meta.url),'utf8');
const canvasSource=fs.readFileSync(new URL('./ConversationCanvas.svelte',import.meta.url),'utf8');
const slice=(from,to)=>{const a=source.indexOf(from),b=source.indexOf(to,a);assert.ok(a>=0&&b>a);return source.slice(a,b);};
const code=ts.transpileModule(slice('  function splitConversation(', '  // 会话可切换模型；'),{compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;
function harness(){
  const c=id=>({id,conn_id:`conn-${id}`,site_slug:`site-${id}`,model:'test',brain:'codex',messages:[],status:'idle'});
  const a=c('a'),b=c('b'),calls=[],results={};
  const ctx={splitSnapshots:{a,b},splitDrafts:{a:{text:'A draft',files:[],sticky:true},b:{text:'B draft',files:[],sticky:true}},splitErrors:{},splitQueued:{},convos:[a,b],activeConv:a,activeConvId:'a',threadModel:'test',threadPerm:'full',threadEffort:'',running:{},lives:{},autoRetried:{},retryExhausted:{},ATT_MARKER:'ATTACHMENTS',
    conversationLayout:splitPane(pane('a','pa'),'pa','b','right'),splitFocused:'pa',leaves,pane,removePane,replacePane,splitPane,
    $state:v=>v,tick:()=>Promise.resolve(),
    optimisticUser:text=>({role:'user',text}),makeChannel:id=>({id}),
    beginTurn:(id,c)=>{ctx.running[id]=c.conn_id;ctx.lives[id]={startedAt:42};ctx.splitSnapshots[id]=c;},
    endTurn:(c,id)=>{ctx.splitSnapshots[id]=c;delete ctx.running[id];delete ctx.lives[id];},
    refreshConvos:async()=>{},maybeAutoRetry:()=>{},failTurn:async(e,id)=>{delete ctx.running[id];ctx.restoreSplitQueue(id);},
    invoke:async(command,args)=>{calls.push({command,args});if(command==='send_message')return new Promise((resolve,reject)=>{results[args.convId]={resolve,reject};});return 'saved/file.txt';},
    conversationProject:c=>c.site_slug,attachmentFilename:f=>f.name,openConv:()=>Promise.resolve(),say:()=>{},view:'thread',draft:'',attachments:[],queued:null,splitSuspended:false,splitVisible:true,conns:[],Date,Uint8Array,
  };
  vm.createContext(ctx);vm.runInContext(code,ctx);return{ctx,calls,results};
}
test('simultaneous sends stay bound to their conversations after focus switches',async()=>{
  const {ctx,calls,results}=harness();
  const first=ctx.splitSubmit('a');ctx.activeConvId='b';ctx.activeConv=ctx.splitSnapshots.b;
  const second=ctx.splitSubmit('b');
  assert.deepEqual(calls.map(c=>[c.args.convId,c.args.message]),[['a','A draft'],['b','B draft']]);
  results.a.resolve({...ctx.splitSnapshots.a,messages:[{role:'assistant',text:'A done'}]});await first;
  assert.equal(ctx.activeConvId,'b');assert.equal(ctx.splitSnapshots.a.messages[0].text,'A done');
  results.b.resolve({...ctx.splitSnapshots.b,messages:[{role:'assistant',text:'B done'}]});await second;
  assert.equal(ctx.splitSnapshots.b.messages[0].text,'B done');
});
test('queue is per conversation and preserves a newer unsent draft when it drains',()=>{
  const {ctx,calls}=harness();ctx.running.a='conn-a';ctx.lives.a={startedAt:42};
  ctx.splitSubmit('a');assert.equal(calls.length,0);assert.equal(ctx.splitQueued.a.text,'A draft');
  ctx.splitDrafts.a.text='newer A';ctx.activeConvId='b';delete ctx.running.a;
  ctx.finishSplitQueue('a',{messages:[{role:'user',text:'previous'}]},false,42);
  assert.equal(calls[0].args.convId,'a');assert.equal(calls[0].args.message,'A draft');
  assert.equal(ctx.splitDrafts.a.text,'newer A');assert.equal(ctx.splitDrafts.b.text,'B draft');
});
test('failure, stale turn, duplicate or stopping restores queue without sending',async()=>{
  for(const [failed,turn,text] of [[true,42,'previous'],[false,43,'previous'],[false,42,'A draft']]) {
    const {ctx,calls}=harness();ctx.splitQueued.a={text:'A draft',files:[],startedAt:42};ctx.splitDrafts.a.text='newer';
    ctx.finishSplitQueue('a',{messages:[{role:'user',text}]},failed,turn);
    assert.equal(calls.length,0);assert.equal(ctx.splitDrafts.a.text,'A draft\n\nnewer');
  }
  const {ctx,calls}=harness();ctx.running.a='conn-a';ctx.splitQueued.a={text:'pending',files:[],startedAt:42};
  await ctx.splitStop('a');assert.equal(calls[0].command,'cancel_turn');assert.equal(calls[0].args.convId,'a');assert.equal(ctx.splitQueued.a,undefined);
});
test('closing an active pane neither cancels work nor drops drafts',()=>{
  const {ctx,calls}=harness();ctx.running.a='conn-a';ctx.closeSplit('pa');
  assert.equal(calls.length,0);assert.equal(ctx.running.a,'conn-a');assert.equal(ctx.splitDrafts.a.text,'A draft');
  assert.equal(leaves(ctx.conversationLayout).length,1);assert.equal(ctx.activeConvId,'b');
});
test('last remaining pane stays an ordinary conversation with no alternate view',()=>{
  const {ctx,calls}=harness();ctx.closeSplit('pa');
  const last=leaves(ctx.conversationLayout)[0];ctx.closeSplit(last.id);
  assert.equal(leaves(ctx.conversationLayout).length,1);
  assert.equal(ctx.activeConvId,'b');assert.equal(calls.length,0);
  assert.doesNotMatch(source,/splitSuspended|expandSplit|split-entry|返回分屏|<ConversationPane/);
  assert.match(source,/@render conversationHeader\(c,paneId\)/);
  assert.match(source,/@render convPane\(c,paneId\)/);
  assert.match(source,/@render convPane\(activeConv\)/);
});
test('upload completion appends to the captured conversation, never the focused one',async()=>{
  const {ctx,calls}=harness();let resolve;
  ctx.invoke=(command,args)=>{calls.push({command,args});return new Promise(r=>resolve=r);};
  const uploading=ctx.splitUpload(ctx.splitSnapshots.a,{size:3,name:'test.txt',arrayBuffer:async()=>new ArrayBuffer(3)});
  await new Promise(r=>setImmediate(r));ctx.activeConvId='b';resolve('attachments/test.txt');await uploading;
  assert.equal(calls[0].args.connId,'conn-a');assert.equal(ctx.splitDrafts.a.files.length,1);assert.equal(ctx.splitDrafts.b.files.length,0);
});

test('real turn lifecycle updates background results without stealing focus, even after its pane closes',()=>{
  const {ctx}=harness();
  Object.assign(ctx,{
    retryTimers:{},clearTimeout,clearStructuredGcmsUnlock:()=>{},clearSitebuildAutoSyncRetry:()=>{},
    sitebuildAutoSyncGeneration:new Map(),sitebuildAutoSyncPending:new Map(),sitebuildTurnDiscoveryBaselines:new Map(),
    sitebuildToolsExecutedCreate:()=>false,sitebuildCreateWasRejected:()=>false,
    queued:null,scrollSoon:()=>{},checkCfReady:()=>{},activeConnId:'conn-b',
  });
  const lifecycle=ts.transpileModule(slice('  function beginTurn(', '  async function failTurn('),{compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;
  vm.runInContext(lifecycle,ctx);
  ctx.closeSplit('pa');assert.equal(ctx.activeConvId,'b');
  const updated={...ctx.splitSnapshots.a,messages:[{role:'user',text:'background request'}],updated_at:1};
  ctx.beginTurn('a',updated);
  assert.equal(ctx.activeConvId,'b');assert.equal(ctx.running.a,'conn-a');
  const finished={...updated,messages:[...updated.messages,{role:'assistant',text:'completed in background'}],updated_at:2};
  ctx.endTurn(finished,'a');
  assert.equal(ctx.activeConvId,'b');assert.equal(ctx.splitSnapshots.a.messages.length,2);
  assert.equal(ctx.convos.find(c=>c.id==='a').messages.length,2);assert.equal(ctx.running.a,undefined);
});

test('pane actions render conversation-bound file and approval handlers',()=>{
  assert.match(source,/openGcmsControlUnlock\(activeConversationUnlockAction,activeConv\)/);
  assert.match(source,/mdClick\(e,context\)/);
  assert.match(source,/thumbKey\(p,context\)/);
  assert.match(source,/onpointerdown=\{\(e\)=>startConversationPointerDrag\(e,c.id\)\}/);
});

test('conversation header keeps model and site identity on one compact row',()=>{
  assert.match(source,/class="conversation-head-title"[\s\S]*?<BrainIcon brain=\{activeConv\?\.brain\?\?'claude'\}/);
  assert.match(source,/class="th-open conversation-site-link"[\s\S]*?<SiteFav src=\{headerSiteIcon\}/);
  assert.doesNotMatch(slice('{#snippet conversationHeader', '{/snippet}'),/<small/);
  assert.match(source,/\.conversation-head-title>span:last-child\{[^}]*text-overflow:ellipsis;[^}]*white-space:nowrap/);
  assert.match(source,/\.conversation-head-model\{[^}]*line-height:0\}/);
  assert.doesNotMatch(source,/\.conversation-head-model\{[^}]*transform:/);
  assert.match(source,/\.conversation-site-link\{max-width:52%;min-width:0;flex:none\}/);
  assert.match(source,/\.thread-head\.conversation-thread-head\s*\{[^}]*height: 30px; min-height: 30px;[^}]*align-items: center/);
});

test('split canvas always fits its viewport instead of creating page-level scrollbars',()=>{
  assert.doesNotMatch(canvasSource,/style:min-width|style:min-height|minimumSize/);
  assert.match(canvasSource,/\.canvas-scroll\{[^}]*overflow:hidden;[^}]*min-width:0/);
  assert.match(canvasSource,/\.canvas\{[^}]*width:100%;height:100%;min-width:0;min-height:0/);
});

test('drag feedback keeps the conversation ghost and restores the blurred split target',()=>{
  assert.match(source,/class="conversation-drag-ghost"[\s\S]*?<BrainIcon brain=\{dragConversation\.brain\}/);
  assert.match(source,/splitDragPoint=\{x:event\.clientX,y:event\.clientY\}/);
  assert.match(canvasSource,/const next=splitPane\(layout,destination\.id,dragConversation,destination\.edge/);
  assert.match(canvasSource,/class="drop-preview"[\s\S]*?<span>分屏视图<\/span>/);
  assert.match(canvasSource,/backdrop-filter:blur\(4px\)/);
  assert.match(source,/class=\{`single-split-preview \$\{singleDropEdge\}`\}[\s\S]*?<span>分屏视图<\/span>/);
});

test('dropping outward from a boundary pane appends to the full canvas',()=>{
  assert.match(canvasSource,/local==='right'&&Math\.abs\(r\.right-bounds\.right\)<2/);
  assert.match(canvasSource,/hover=promoted\?\{id:layout\.id,edge:promoted\}:\{id,edge:local\}/);
});

test('settings replies update the originating conversation after focus changes',async()=>{
  const {ctx,calls}=harness();let finish;
  ctx.invoke=(command,args)=>{calls.push({command,args});return new Promise(resolve=>finish=resolve);};
  const setting=ctx.splitSetting(ctx.splitSnapshots.a,'effort','high');
  ctx.activeConvId='b';ctx.activeConv=ctx.splitSnapshots.b;
  finish({...ctx.splitSnapshots.a,effort:'high'});await setting;
  assert.equal(calls[0].args.convId,'a');assert.equal(ctx.splitSnapshots.a.effort,'high');
  assert.equal(ctx.activeConv.id,'b');assert.equal(ctx.splitSnapshots.b.effort,undefined);
});
