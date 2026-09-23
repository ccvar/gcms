import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';
import { compile } from 'svelte/compiler';
import { createServer } from 'vite';

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const pageSource = fs.readFileSync(new URL('../routes/+page.svelte', import.meta.url), 'utf8');
const from = pageSource.indexOf('  let fileRename =');
const to = pageSource.indexOf('  async function confirmFsAsk()', from);
const markupFrom = pageSource.indexOf('                    {#if renaming && fileRename}');
const markupTo = pageSource.indexOf('                    {#if deleteTarget}', markupFrom);
assert.ok(from >= 0 && to > from && markupFrom >= 0 && markupTo > markupFrom);
const logic = ts.transpileModule(pageSource.slice(from, to), {compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;
const cardsFrom = pageSource.indexOf('            <div class="skill-list">');
const cardsTo = pageSource.indexOf('            {#if filteredSkills.length === 0}', cardsFrom);
const cards = pageSource.slice(cardsFrom,cardsTo).replace(/<SkillFav[^>]+\/>/g,'<span>◈</span>');
const trash = pageSource.match(/\{#snippet trashIcon\(size: number\)\}[\s\S]+?\{\/snippet\}/)[0].replace('size: number','size');
const css = pageSource.slice(pageSource.indexOf('  .skill-list {'),pageSource.indexOf('  .thread-inner {'))
  + '\n' + pageSource.split('\n').filter(line=>/^  \.switch[ .{]/.test(line)).join('\n')
  + '\n@media(max-width:760px){' + pageSource.slice(pageSource.indexOf('    .skill-card {'),pageSource.indexOf('    .sites-page {',pageSource.indexOf('    .skill-card {'))) + '}';
const fixture = `<script>
  let sftpConnId = $state('server-a'), sftpSel = '', fctxMenu = null, sftpOpenDirs = new Set();
  const r = {name:'site.json',path:'/config/site.json',dir:false};
  let deleting=false;
  const parentOf=p=>p.slice(0,p.lastIndexOf('/')), sftpJoin=(dir,name)=>dir+'/'+name;
  const isSftpDeleting=()=>false, loadDir=async()=>{}, say=()=>{};
  window.calls=[];window.opens=0;
  const invoke=async(command,args)=>{window.calls.push({command,args});return [];};
  ${logic}
  const renaming=$derived(fileRename?.path===r.path);
  const filteredSkills=[
    {id:'blockvar',name:'blockvar-content',site_domain:'blockvar.com',description:"Manage the BlockVar Go+SQLite site's bilingual guides, project opportunities and content.",has_scripts:true,enabled:true,version:'',sha256:'a107ae9ae52310b7'},
    {id:'dzpto',name:'dzpto-content-ops',site_domain:'dzpto.com',description:'Plan, draft, localize, validate, publish, monitor, and refresh evidence-backed content.',has_scripts:false,enabled:true,version:'',sha256:'704135cf213aab11'},
    {id:'vibgg',name:'vibgg-content-ops',site_domain:'vibgg.com',description:'Plan, draft, localize, validate, publish, monitor, and refresh evidence-backed content.',has_scripts:true,enabled:false,version:'1.2',sha256:'34018e59478e31b8'}];
  const skillToggleBusy={},compactDigest=v=>v.slice(0,8)+'…'+v.slice(-4),skillVersion=v=>'v'+v;
  const toggleInstalledSkill=()=>{},removeInstalledSkill=()=>{};
</script>
<button onclick={()=>openRename(r)}>重命名</button>
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="frow" ondblclick={()=>window.opens++}>
${pageSource.slice(markupFrom,markupTo)}
</div>
<button id="outside">其他位置</button>
<div id="cards" style="margin:24px auto;max-width:812px">${cards}</div>${trash}
<style>${css}</style>`;
const compiled = compile(fixture, {filename:'RenameFixture.svelte', generate:'client', css:'injected'}).js.code;

test('real inline filename markup supports double-click, Escape, Enter and blur; icons follow declared paths', async()=>{
  const server=await createServer({configFile:false,root:fileURLToPath(new URL('../../',import.meta.url)),
    server:{host:'127.0.0.1',port:0},plugins:[{
      name:'skill-files-fixture',
      resolveId(id){if(id==='/rename-fixture.js')return '\0rename-fixture';},
      load(id){if(id==='\0rename-fixture')return compiled;},
      configureServer(server){server.middlewares.use(async(req,res,next)=>{
        if(req.url!=='/__skill-files')return next();
        res.setHeader('Content-Type','text/html');res.end(await server.transformIndexHtml(req.url, `<!doctype html><style>:root{--text:#302e28;--dim:#77736b;--faint:#a29e95;--border:#ecebe6;--card:white;--soft:#faf9f7;--accent:#302e28;--err:#c53b32;--err-soft:#fff0ed}body{font-family:system-ui;margin:16px}</style><div id="app"></div><script type="module">
          import {mount} from 'svelte';import Fixture from '/rename-fixture.js';
          import {declaredSkillIcons} from '/src/lib/skillIcons.ts';
          window.icons=declaredSkillIcons;mount(Fixture,{target:document.querySelector('#app')});window.ready=true;
        </script>`));
      });}
    }]});
  let browser;
  try {
    await server.listen();browser=await chromium.launch({headless:true,channel:process.env.PLAYWRIGHT_CHANNEL||'chrome'});
    const page=await browser.newPage();page.setDefaultTimeout(10000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/__skill-files`);
    await page.waitForFunction(()=>window.ready).catch(e=>{throw new Error(`${e.message}; page errors: ${errors.join('; ')}`);});
    await page.getByRole('button',{name:'site.json',exact:true}).dblclick();
    const input=page.getByRole('textbox',{name:'重命名 site.json'});
    assert.equal(await input.count(),1);assert.equal(await page.evaluate(()=>window.opens),0);
    assert.deepEqual(await input.evaluate(n=>[n.selectionStart,n.selectionEnd]),[0,4]);
    await input.fill('cancel.json');await input.press('Escape');
    assert.equal(await input.count(),0);assert.equal(await page.evaluate(()=>window.calls.length),0);
    await page.getByRole('button',{name:'重命名',exact:true}).click();
    await input.fill('renamed.json');await input.press('Enter');await page.waitForFunction(()=>window.calls.length===2);
    assert.equal(await input.count(),0);
    assert.deepEqual(await page.evaluate(()=>window.calls[1]),{command:'sftp_rename',args:{connId:'server-a',from:'/config/site.json',to:'/config/renamed.json'}});
    await page.getByRole('button',{name:'重命名',exact:true}).click();
    await input.fill('blur.json');await page.locator('#outside').click();await page.waitForFunction(()=>window.calls.length===4);
    assert.equal(await input.count(),0);
    assert.deepEqual(await page.evaluate(()=>window.icons(`<link rel="icon" href="/assets/icon.2309a4f605.svg"><link rel="apple-touch-icon" href="/icon-180.png"><link rel="icon" href="https://other.example/icon"><link rel="icon" href="javascript:alert(1)">`,'vibgg.com')),
      ['https://vibgg.com/assets/icon.2309a4f605.svg','https://vibgg.com/icon-180.png']);
    for(const width of [1000,520]) {
      await page.setViewportSize({width,height:650});
      const metrics=await page.locator('.skill-card').evaluateAll(cards=>cards.map(card=>{
        const control=card.querySelector('.skill-card-control'),toggle=control.querySelector('.switch'),remove=control.querySelector('.skill-remove');
        const a=toggle.getBoundingClientRect(),b=remove.getBoundingClientRect();
        return {delta:Math.abs((a.y+a.height/2)-(b.y+b.height/2)),x:a.x,overflow:card.scrollWidth>card.clientWidth,
          iconOnly:remove.textContent.trim()===''&&!!remove.querySelector('svg'),riskInControls:!!control.querySelector('.skill-risk')};
      }));
      for(const metric of metrics){assert.ok(metric.delta<1);assert.equal(metric.x,metrics[0].x);assert.equal(metric.overflow,false);assert.ok(metric.iconOnly);assert.equal(metric.riskInControls,false);}
      if(width===1000)await page.locator('#cards').screenshot({path:'/tmp/pilot-skill-cards-layout.png'});
    }
    assert.deepEqual(errors,[]);
  } finally {await browser?.close();await server.close();}
});
