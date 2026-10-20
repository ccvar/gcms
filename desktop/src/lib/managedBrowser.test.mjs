import {test} from 'node:test';
import assert from 'node:assert/strict';
import {fileURLToPath} from 'node:url';
import {compile} from 'svelte/compiler';
import {svelte} from '@sveltejs/vite-plugin-svelte';
import {createServer} from 'vite';
const {chromium}=await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const fixture=compile(`<script>
import ManagedCardMenu from '/src/lib/ManagedCardMenu.svelte';
import ManagedSettings from '/src/lib/ManagedSettings.svelte';
import ManagedSitePicker from '/src/lib/ManagedSitePicker.svelte';
let paused=$state(false),editing=$state(false),selected=$state([]);
const options=[{value:'claude::opus',label:'Opus',icon:'claude'},{value:'codex::gpt-6-sol',label:'GPT-6 Sol',icon:'codex'}];
const targets=[{id:'a',site_name:'Alpha',mode:'growth',brain:'claude',model:'opus',effort:'high',level:'l0',weekly_post_limit:3,weekly_edit_limit:2,token_weekly_budget:0},{id:'b',site_name:'Beta',mode:'plan',brain:'codex',model:'gpt-6-sol',effort:'high',level:'l1',weekly_post_limit:8,weekly_edit_limit:4,token_weekly_budget:500000}];
window.patches=[];window.deleted=0;
</script>
<div style="max-width:800px;margin:32px auto;background:white;border:1px solid #e2dfd7;border-radius:16px;padding:20px">
<div style="display:flex;align-items:center;justify-content:space-between"><h2>Alpha · 托管</h2><ManagedCardMenu {paused} onedit={()=>editing=true} ontoggle={()=>paused=!paused} ondelete={()=>window.deleted++}/></div>
<ManagedSitePicker options={[{value:'a',label:'Alpha',sub:'alpha.test'},{value:'b',label:'Beta',sub:'beta.test'},{value:'c',label:'Managed',sub:'已在托管中',disabled:true}]} value={selected} onchange={v=>selected=v}/>
<p id="selected">{selected.join(',')}</p><button id="outside">其他区域</button>
</div>
{#if editing}<ManagedSettings {targets} {options} levels={[{value:'l0',label:'L0 试运行'},{value:'l1',label:'L1 自动发布'}]} onsave={p=>window.patches.push(p)} onclose={()=>editing=false}/>{/if}`,{filename:'ManagedFixture.svelte',generate:'client',css:'injected'}).js.code;
test('managed controls: icon menu, pause/resume, multi-selection, selective mode patch and responsive modal',async()=>{
  const server=await createServer({configFile:false,root:fileURLToPath(new URL('../../',import.meta.url)),server:{host:'127.0.0.1',port:0},plugins:[svelte({configFile:false}),{
    name:'managed-fixture',resolveId(id){if(id==='/managed-fixture.js')return '\0managed-fixture';},load(id){if(id==='\0managed-fixture')return fixture;},
    configureServer(server){server.middlewares.use(async(req,res,next)=>{if(req.url!=='/__managed')return next();res.setHeader('Content-Type','text/html');res.end(await server.transformIndexHtml(req.url,`<!doctype html><style>:root{--text:#302e28;--dim:#77736b;--border:#ecebe6;--border2:#e2dfd7;--bg:white;--rail:#f5f4f0;--accent:#302e28;--err:#c53b32}body{font-family:system-ui;margin:16px;background:#faf9f7}</style><div id="app"></div><script type="module">import{mount}from'svelte';import Fixture from'/managed-fixture.js';mount(Fixture,{target:document.querySelector('#app')});window.ready=true;</script>`));});}
  }]});
  let browser;
  try {
    await server.listen();browser=await chromium.launch({headless:true,channel:process.env.PLAYWRIGHT_CHANNEL || 'chrome'});
    const page=await browser.newPage({viewport:{width:1200,height:950}});const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto(`http://127.0.0.1:${server.httpServer.address().port}/__managed`);await page.waitForFunction(()=>window.ready);
    await page.getByRole('checkbox',{name:'全选可用站点'}).check();assert.equal(await page.locator('#selected').textContent(),'a,b');
    assert.equal(await page.getByRole('checkbox',{name:'Managed 已在托管中'}).isDisabled(),true);
    await page.getByRole('textbox',{name:'搜索可托管站点'}).fill('beta');await page.getByRole('checkbox',{name:'全选搜索结果'}).uncheck();assert.equal(await page.locator('#selected').textContent(),'a');
    await page.getByRole('button',{name:'托管设置',exact:true}).click();
    assert.equal(await page.getByRole('menuitem').count(),3);assert.equal(await page.getByRole('menuitem').locator('svg').count(),3);
    assert.equal(await page.getByRole('button',{name:'托管设置',exact:true}).locator('svg').count(),1);
    assert.deepEqual(await page.getByRole('menuitem').evaluateAll(items=>items.map(el=>{
      const icon=el.querySelector('svg').getBoundingClientRect(),label=el.querySelector('span').getBoundingClientRect();
      return {width:icon.width,height:icon.height,centered:Math.abs(icon.y+icon.height/2-label.y-label.height/2)<1};
    })),Array.from({length:3},()=>({width:12,height:12,centered:true})));
    assert.equal(await page.getByRole('menu').locator('.divider').evaluate(el=>el.getBoundingClientRect().height),1);
    await page.screenshot({path:'/tmp/pilot-managed-menu-alignment.png'});
    await page.getByRole('menuitem',{name:'暂停托管'}).click();await page.getByRole('button',{name:'托管设置',exact:true}).click();
    assert.equal(await page.getByRole('menuitem',{name:'恢复托管'}).count(),1);
    await page.keyboard.press('Escape');assert.equal(await page.getByRole('menu').count(),0);
    await page.getByRole('button',{name:'托管设置',exact:true}).click();await page.getByRole('menuitem',{name:'编辑托管'}).click();
    assert.equal(await page.getByRole('button',{name:'确认修改…'}).isDisabled(),true);
    const modeCheckbox=page.getByRole('checkbox',{name:/^托管模式/});
    const modeRow=page.getByRole('dialog').locator('.setting').filter({has:modeCheckbox});
    assert.equal(await modeRow.locator('.mixed').textContent(),'各站当前值不同');
    assert.equal(await modeCheckbox.isChecked(),false);
    await page.getByRole('spinbutton',{name:'每周产出上限',exact:true}).fill('9');
    await page.getByRole('button',{name:'确认修改…'}).click();
    assert.deepEqual(await page.evaluate(()=>window.patches),[{weekly_post_limit:9}]);
    await page.getByRole('checkbox',{name:'备用模型',exact:true}).check();
    await page.getByRole('button',{name:'确认修改…'}).click();
    assert.deepEqual(await page.evaluate(()=>window.patches[1]),{weekly_post_limit:9,fallback:{brain:'',model:'',effort:''}});
    // Mixed modes stay untouched unless explicitly selected. Selecting a different
    // mode marks only this setting; unchecking it removes it from the saved patch.
    await page.getByRole('button',{name:'托管模式',exact:true}).click();
    await page.getByRole('option',{name:/计划托管/}).click();
    assert.equal(await modeCheckbox.isChecked(),true);
    await page.getByRole('button',{name:'确认修改…'}).click();
    assert.deepEqual(await page.evaluate(()=>window.patches[2]),{weekly_post_limit:9,fallback:{brain:'',model:'',effort:''},mode:'plan'});
    await modeCheckbox.uncheck();
    await page.getByRole('button',{name:'确认修改…'}).click();
    assert.deepEqual(await page.evaluate(()=>window.patches[3]),{weekly_post_limit:9,fallback:{brain:'',model:'',effort:''}});
    await page.getByRole('button',{name:'托管模式',exact:true}).click();
    await page.getByRole('option',{name:/增长托管/}).click();
    assert.equal(await modeCheckbox.isChecked(),true);
    await page.getByRole('button',{name:'确认修改…'}).click();
    assert.deepEqual(await page.evaluate(()=>window.patches[4]),{weekly_post_limit:9,fallback:{brain:'',model:'',effort:''},mode:'growth'});
    for(const width of [1200,520]) {
      await page.setViewportSize({width,height:950});
      assert.equal(await page.getByRole('dialog').evaluate(el=>el.scrollWidth>el.clientWidth),false);
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      await page.getByRole('dialog').screenshot({path:`/tmp/pilot-managed-settings-${width}.png`});
    }
    await page.keyboard.press('Escape');assert.equal(await page.getByRole('dialog').count(),0);assert.equal(await page.evaluate(()=>window.deleted),0);
    assert.deepEqual(errors,[]);
  } finally {await browser?.close();await server.close();}
});
