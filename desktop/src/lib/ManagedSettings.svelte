<script lang="ts">
  import ModelFx from './ModelFx.svelte';
  import { effortAfterModelPick, type ModelOption } from './modelSelection';
  import Dropdown from './Dropdown.svelte';
  import { untrack } from 'svelte';
  import { settingLabels, validateManagedPatch, type ManagedSettingsPatch, type ManagedSettingsTarget, type SettingKey } from './managedSettings';
  let { targets, options, levels, busy = false, error = '', onsave, onclose }: {
    targets: ManagedSettingsTarget[];
    options: ModelOption[];
    levels: {value:string;label:string;sub?:string}[];
    busy?: boolean; error?: string;
    onsave: (patch: ManagedSettingsPatch) => void; onclose: () => void;
  } = $props();
  // Seed once; subsequent data refreshes must not overwrite unsaved input.
  const first = untrack(()=>targets[0]);
  const batch = $derived(targets.length > 1);
  let selected = $state<SettingKey[]>([]);
  let primary = $state(`${first.brain}::${first.model}`);
  let effort = $state(first.effort);
  let fallback = $state(`${first.fallback_brain || 'codex'}::${first.fallback_model || ''}`);
  let fallbackEffort = $state(first.fallback_effort || '');
  let fallbackEnabled = $state(!!first.fallback_brain);
  let level = $state(first.level);
  let posts = $state(first.weekly_post_limit);
  let edits = $state(first.weekly_edit_limit);
  let budget = $state(first.token_weekly_budget);
  let localError = $state('');
  let dialog = $state<HTMLDivElement>();
  $effect(()=>{dialog?.focus();});
  function keydown(e: KeyboardEvent) {
    if (document.querySelector('.confirm-layer')) return;
    if (e.key==='Escape' && !busy && !dialog?.querySelector('.fx-menu,.dd-menu')) { e.preventDefault(); onclose(); }
    if(e.key==='Tab' && dialog) {
      const els=Array.from(dialog.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled),[tabindex="0"]')).filter(el=>el.getClientRects().length);
      const first=els[0],last=els.at(-1);
      if(e.shiftKey && (document.activeElement===first || document.activeElement===dialog)){e.preventDefault();last?.focus();}
      else if(!e.shiftKey && document.activeElement===last){e.preventDefault();first?.focus();}
    }
  }
  function mark(key: SettingKey) { if (!selected.includes(key)) selected = [...selected, key]; }
  function mixed(key: SettingKey) {
    const value=(t: ManagedSettingsTarget)=>key==='primary'?[t.brain,t.model,t.effort]:key==='fallback'?[t.fallback_brain || '',t.fallback_model || '',t.fallback_effort || '']:t[key];
    return targets.some(t=>JSON.stringify(value(t))!==JSON.stringify(value(targets[0])));
  }
  function model(value: string, effort: string) { const i = value.indexOf('::'); return {brain:value.slice(0,i),model:value.slice(i+2),effort}; }
  function submit() {
    const all: ManagedSettingsPatch = {primary:model(primary,effort),fallback:fallbackEnabled?model(fallback,fallbackEffort):{brain:'',model:'',effort:''},level,weekly_post_limit:posts,weekly_edit_limit:edits,token_weekly_budget:budget};
    const patch = Object.fromEntries(selected.map(key => [key,all[key]])) as ManagedSettingsPatch;
    localError = validateManagedPatch(patch, targets);
    if (!localError) onsave(patch);
  }
</script>

<div class="layer">
  <button class="backdrop" aria-label="关闭托管设置" disabled={busy} onclick={onclose}></button>
  <div class="dialog" bind:this={dialog} onkeydown={keydown} role="dialog" aria-modal="true" aria-labelledby="managed-settings-title" tabindex="-1">
    <header><div><h2 id="managed-settings-title">{batch ? `批量编辑 · ${targets.length} 个站点` : `编辑托管 · ${first.site_name}`}</h2><p>只修改勾选项；运营计划、任务时间和运行状态保持不变。</p></div><button class="close" disabled={busy} aria-label="关闭托管设置" onclick={onclose}>×</button></header>
    <div class="body">
      {#if batch}<p class="sites" title={targets.map(t=>t.site_name).join('、')}>{targets.map(t=>t.site_name).join('、')}</p>{/if}
      <fieldset disabled={busy}>
        {#each Object.keys(settingLabels) as raw}
          {@const key = raw as SettingKey}
          <div class="setting" class:chosen={selected.includes(key)}>
            <label class="field-label"><input type="checkbox" checked={selected.includes(key)} onchange={e => selected = e.currentTarget.checked ? [...selected,key] : selected.filter(k=>k!==key)} /><span>{settingLabels[key]}{#if mixed(key)}<small class="mixed">各站当前值不同</small>{/if}</span></label>
            <div class="control">
              {#if key === 'primary'}
                <ModelFx {options} value={primary} {effort} onpick={(v:string)=>{effort=effortAfterModelPick(primary,v,effort,options);primary=v;mark(key);}} oneffort={(v:string)=>{effort=v;mark(key);}} />
              {:else if key === 'fallback'}
                <label class="fallback"><input type="checkbox" bind:checked={fallbackEnabled} onchange={()=>mark(key)} />启用备用模型</label>
                {#if fallbackEnabled}<ModelFx {options} value={fallback} effort={fallbackEffort} onpick={(v:string)=>{fallbackEffort=effortAfterModelPick(fallback,v,fallbackEffort,options);fallback=v;mark(key);}} oneffort={(v:string)=>{fallbackEffort=v;mark(key);}} />{/if}
              {:else if key === 'level'}
                <Dropdown value={level} options={levels} onchange={v=>{level=v;mark(key);}} ariaLabel="托管级别" />
              {:else if key === 'weekly_post_limit'}
                <input aria-label="每周产出上限" type="number" min="1" max="50" bind:value={posts} oninput={()=>mark(key)} /><small>每站 1–50 篇；仍受爬坡与实际配额限制</small>
              {:else if key === 'weekly_edit_limit'}
                <input aria-label="每周存量修改上限" type="number" min="1" max="20" bind:value={edits} oninput={()=>mark(key)} /><small>每站 1–20 篇，仅 L3 生效</small>
              {:else}
                <input aria-label="每周 token 预算" type="number" min="0" step="1" bind:value={budget} oninput={()=>mark(key)} /><small>每站独立计算，0 表示不限；不会自动解除熔断</small>
              {/if}
            </div>
          </div>
        {/each}
      </fieldset>
      {#if error || localError}<p class="error" role="alert">{error || localError}</p>{/if}
    </div>
    <footer><span>{selected.length ? `将修改 ${selected.length} 项设置` : '请勾选要修改的设置'}</span><button disabled={busy} onclick={onclose}>取消</button><button class="primary" disabled={busy || !selected.length} onclick={submit}>{busy ? '保存中…' : '确认修改…'}</button></footer>
  </div>
</div>

<style>
  .mixed{display:block;font-size:10px;font-weight:400;margin-top:3px}
  .layer{position:fixed;inset:0;z-index:220;display:grid;place-items:center;padding:24px}
  .backdrop{position:absolute;inset:0;background:#0005;border:0}
  .dialog{position:relative;background:var(--bg);color:var(--text);border:1px solid var(--border2);border-radius:16px;width:min(640px,100%);max-height:calc(100vh - 48px);display:flex;flex-direction:column;box-shadow:0 20px 80px #0002}
  header{display:flex;justify-content:space-between;padding:20px 24px 16px;border-bottom:1px solid var(--border)}
  h2{font-size:17px;margin:0} p{margin:6px 0 0;font-size:12px;color:var(--dim);line-height:1.5} .close{border:0;font-size:22px;background:none;align-self:start}
  .body{padding:4px 24px 16px;overflow:auto} .sites{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;padding:8px 0}
  fieldset{border:0;padding:0;margin:0;min-width:0}.setting{display:grid;grid-template-columns:160px minmax(0,1fr);gap:16px;padding:16px 0;border-bottom:1px solid var(--border)}
  .field-label{display:flex;align-items:center;gap:8px;font-size:13px;align-self:start;min-height:30px}.chosen .field-label{font-weight:600}.control{display:flex;flex-direction:column;align-items:stretch;gap:6px;min-width:0}.fallback{font-size:12px;display:flex;align-items:center;gap:8px}
  input[type=number]{width:100%;box-sizing:border-box;border:1px solid var(--border2);border-radius:7px;padding:7px 9px;color:var(--text);background:var(--bg);font:inherit;font-size:13px} input[type=checkbox]{accent-color:var(--accent)} small{font-size:11px;color:var(--dim);line-height:1.5}
  footer{display:flex;gap:8px;align-items:center;padding:14px 24px;border-top:1px solid var(--border)}footer span{font-size:12px;color:var(--dim);margin-right:auto}button{cursor:pointer;color:inherit}footer button{border:1px solid var(--border2);background:var(--bg);padding:7px 12px;border-radius:7px;font:inherit;font-size:12px}footer .primary{background:var(--accent);color:white;border-color:var(--accent)}button:disabled{opacity:.5;cursor:default}.error{white-space:pre-wrap;color:var(--err)}
  @media(max-width:560px){.setting{grid-template-columns:1fr;gap:6px}header,.body,footer{padding-left:16px;padding-right:16px}}
</style>
