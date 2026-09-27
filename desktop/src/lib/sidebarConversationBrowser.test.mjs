import {test} from 'node:test';
import assert from 'node:assert/strict';
import {fileURLToPath} from 'node:url';
import {readFileSync} from 'node:fs';
import {compile} from 'svelte/compiler';
import {svelte} from '@sveltejs/vite-plugin-svelte';
import {createServer} from 'vite';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const source=readFileSync(new URL('../routes/+page.svelte',import.meta.url),'utf8');
const batchStyle=source.match(/  \.md-batch-trigger \{[^}]+\}/)[0];
const batchMarkup=source.match(/<button class="btn sm ghost md-batch-trigger"[^]*?<\/button>/)[0];
const batchBarMarkup=source.match(/<div class="md-batch-bar">[^]*?<\/div>/)[0];
const batchBarStyles=[...source.matchAll(/  \.md-batch-bar[^\{]*\{[^}]+\}/g)].map(m=>m[0]).join('\n');
const smallButtonStyle=source.match(/  \.btn\.sm \{[^}]+\}/)[0];
const fixture=compile(`<script>
import SidebarConversationList from '/src/lib/SidebarConversationList.svelte';
import SettingsIcon from '/src/lib/SettingsIcon.svelte';
let scope=$state('workspace-a'),limit=$state(9),managedBatch=$state(false),managedSelected=$state([]);
const managedOfConn=[{id:'alpha'},{id:'beta'}];
const managedSelectedSites=$derived(managedOfConn.filter(m=>managedSelected.includes(m.id)));
function openManagedEditor(){}
const items=Array.from({length:9},(_,i)=>({id:String(i+1),updated_at:i+1}));
</script>
{#snippet settingsIcon()}<SettingsIcon />{/snippet}
${batchMarkup}
{#if managedBatch}${batchBarMarkup}{/if}
<button onclick={()=>scope=scope==='workspace-a'?'workspace-b':'workspace-a'}>切换工作区</button>
<button onclick={()=>limit=6}>只剩六条</button>
{#each ['Alpha','Beta'] as label (JSON.stringify([scope,label]))}
<section aria-label={label}>
<h2>{label}</h2>
<SidebarConversationList items={items.slice(0,limit)} {label}>
{#snippet row(c)}<button class="conversation" draggable="true">对话 {c.id}</button>{/snippet}
</SidebarConversationList>
</section>
{/each}
<style>
.btn{border:1px solid #ddd;box-shadow:0 1px 2px #ccc;background:white;border-radius:8px;padding:6px 10px}
${smallButtonStyle}
${batchStyle}
${batchBarStyles}
section{width:260px;background:#faf9f7;border-radius:8px;padding:8px;margin-top:16px}
h2{font-size:14px;margin:4px 10px} .conversation{display:block;margin:2px 8px;padding:4px 19px;border:0;background:none;color:#555}
</style>`,{filename:'SidebarFixture.svelte',generate:'client',css:'injected'}).js.code;

test('sidebar defaults to six recent conversations, expands independently, resets across workspaces, and batch button has no frame',async()=>{
  assert.match(source,/JSON\.stringify\(\[inSkillWorkspace \? 'skills' : activeConnId, g\.slug\]\)/);
  assert.match(source,/<SidebarConversationList items=\{g\.subs\.flatMap\(sub => sub\.items\)\}/);
  const server=await createServer({configFile:false,root:fileURLToPath(new URL('../../',import.meta.url)),server:{host:'127.0.0.1',port:0},plugins:[svelte({configFile:false}),{
    name:'sidebar-fixture',resolveId(id){if(id==='/sidebar-fixture.js')return '\0sidebar-fixture';},load(id){if(id==='\0sidebar-fixture')return fixture;},
    configureServer(server){server.middlewares.use(async(req,res,next)=>{if(req.url!=='/__sidebar')return next();res.setHeader('Content-Type','text/html');res.end(await server.transformIndexHtml(req.url,`<!doctype html><style>:root{--dim:#77736b;--text:#302e28;--soft:#ebe8e1}body{font-family:system-ui}</style><div id="app"></div><script type="module">import{mount}from'svelte';import Fixture from'/sidebar-fixture.js';mount(Fixture,{target:document.querySelector('#app')});window.ready=true;</script>`));});}
  }]});
  let browser;
  try{
    await server.listen();browser=await chromium.launch({headless:true,channel:process.env.PLAYWRIGHT_CHANNEL || 'chrome'});
    const page=await browser.newPage({viewport:{width:600,height:900}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/__sidebar`);await page.waitForFunction(()=>window.ready);
    const a=page.getByRole('region',{name:'Alpha'}),b=page.getByRole('region',{name:'Beta'});
    assert.deepEqual(await a.locator('.conversation').allTextContents(),['对话 9','对话 8','对话 7','对话 6','对话 5','对话 4']);
    const more=a.getByRole('button',{name:'展开Alpha的其余 3 条对话'});
    assert.equal(await more.getAttribute('aria-expanded'),'false');
    await page.screenshot({path:'/tmp/pilot-sidebar-six-conversations.png'});
    await more.focus();await page.keyboard.press('Enter');assert.equal(await a.locator('.conversation').count(),9);
    assert.equal(await b.locator('.conversation').count(),6);
    await a.getByRole('button',{name:'收起Alpha的更多对话'}).click();assert.equal(await a.locator('.conversation').count(),6);
    await more.click();await page.getByRole('button',{name:'切换工作区'}).click();assert.equal(await a.locator('.conversation').count(),6);
    await page.getByRole('button',{name:'只剩六条'}).click();assert.equal(await a.locator('.conversation').count(),6);assert.equal(await a.locator('.conversation-more').count(),0);
    assert.deepEqual(await page.locator('.md-batch-trigger').evaluate(el=>({border:getComputedStyle(el).borderTopWidth,shadow:getComputedStyle(el).boxShadow})),{border:'0px',shadow:'none'});
    await page.getByRole('button',{name:'批量操作',exact:true}).click();assert.equal(await page.getByRole('button',{name:'退出批量操作'}).count(),1);
    const bar=page.locator('.md-batch-bar'),edit=bar.getByRole('button',{name:'修改托管设置'});
    assert.ok((await bar.boundingBox()).height<=28);
    assert.ok((await edit.boundingBox()).height<=20);
    assert.equal(await edit.isDisabled(),true);
    await bar.getByRole('checkbox',{name:'全选'}).check();
    assert.match(await bar.textContent(),/已选 2 个站点/);assert.equal(await edit.isEnabled(),true);
    await page.screenshot({path:'/tmp/pilot-compact-batch-bar.png'});
    await page.setViewportSize({width:360,height:900});
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
    assert.deepEqual(errors,[]);
  }finally{await browser?.close();await server.close();}
});
