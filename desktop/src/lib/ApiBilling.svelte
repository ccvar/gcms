<script lang="ts">
  import { onDestroy } from 'svelte';
  import { invoke } from '@tauri-apps/api/core';
  import { openUrl } from '@tauri-apps/plugin-opener';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { billingPeriodLabel, formatBillingCost, formatBillingTime, formatBillingTokens, safeBillingUrl, type BillingSettings, type BillingSnapshot } from './apiBilling';

  let { id, name, disabled = false }: { id: string; name: string; disabled?: boolean } = $props();
  let open = $state(false);
  let settings = $state<BillingSettings | null>(null);
  let snapshot = $state<BillingSnapshot | null>(null);
  let busy = $state<'settings' | 'query' | 'save' | 'remove' | ''>('');
  let error = $state('');
  let notice = $state('');
  let keyEditor = $state(false);
  let apiKey = $state('');
  let confirmRemoval = $state(false);
  let generation = 0;
  let alive = true;
  const billingUrl = $derived(safeBillingUrl(settings));

  onDestroy(() => { alive = false; generation += 1; apiKey = ''; });

  function current(request: number, profileId: string): boolean { return alive && open && generation === request && id === profileId; }

  async function toggle() {
    if (busy === 'save' || busy === 'remove') return;
    open = !open;
    generation += 1; apiKey = ''; keyEditor = false; confirmRemoval = false;
    snapshot = null; settings = null; error = ''; notice = ''; busy = '';
    if (open) await loadSettings();
  }

  async function loadSettings() {
    const request = ++generation, profileId = id;
    busy = 'settings'; error = '';
    try {
      const result = await invoke<BillingSettings>('get_api_billing_settings', { id: profileId });
      if (current(request, profileId)) settings = result;
    } catch (cause) { if (current(request, profileId)) error = String(cause); }
    finally { if (current(request, profileId)) busy = ''; }
  }

  async function refresh() {
    if (busy || disabled || !settings?.credential_configured || settings.provider === 'unsupported') return;
    const request = ++generation, profileId = id;
    busy = 'query'; error = ''; notice = ''; snapshot = null;
    try {
      const result = await invoke<BillingSnapshot>('query_api_billing', { id: profileId });
      if (current(request, profileId)) snapshot = result;
    } catch (cause) { if (current(request, profileId)) error = String(cause); }
    finally { if (current(request, profileId)) busy = ''; }
  }

  async function saveKey() {
    if (busy || disabled || !apiKey.trim()) return;
    const request = ++generation, profileId = id;
    const key = apiKey.trim();
    apiKey = ''; busy = 'save'; error = ''; notice = ''; snapshot = null;
    try {
      const result = await invoke<BillingSettings>('save_api_billing_key', { id: profileId, apiKey: key });
      if (current(request, profileId)) { settings = result; keyEditor = false; notice = '查询密钥已保存；点击刷新查询用量。'; }
    } catch (cause) { if (current(request, profileId)) error = String(cause); }
    finally { if (current(request, profileId)) busy = ''; }
  }

  async function removeKey() {
    if (busy || disabled) return;
    const request = ++generation, profileId = id;
    confirmRemoval = false; apiKey = ''; busy = 'remove'; error = ''; notice = ''; snapshot = null;
    try {
      const result = await invoke<BillingSettings>('delete_api_billing_key', { id: profileId });
      if (current(request, profileId)) { settings = result; keyEditor = false; notice = '已移除查询密钥，模型调用不受影响。'; }
    } catch (cause) { if (current(request, profileId)) error = String(cause); }
    finally { if (current(request, profileId)) busy = ''; }
  }

  async function showBilling() {
    if (!billingUrl) return;
    const profileId = id, request = generation;
    try { await openUrl(billingUrl); }
    catch { if (current(request, profileId)) error = '无法打开账单页面，请稍后重试。'; }
  }
</script>

<div class="api-billing">
  <button type="button" class="disclosure" aria-label={`${name} 用量与账单`} aria-expanded={open} disabled={disabled || busy === 'save' || busy === 'remove'} onclick={toggle}>
    <svg class:expanded={open} viewBox="0 0 12 12" fill="none" aria-hidden="true"><path d="m4.5 3 3 3-3 3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg><span>用量与账单</span>
  </button>
  {#if open}
    <div class="billing-detail" aria-label={`${name} 账单信息`} aria-busy={!!busy}>
      {#if busy === 'settings'}<p class="note" role="status">读取查询设置…</p>{/if}
      {#if settings}
        <div class="balance-row"><span>剩余额度</span><span class="unavailable">暂不支持查询</span>{#if billingUrl}<button type="button" class="link" onclick={showBilling}>查看账单<svg viewBox="0 0 12 12" fill="none" aria-hidden="true"><path d="M4.5 2.5h5v5M9.3 2.7 2.7 9.3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" /></svg></button>{/if}</div>
        {#if settings.provider === 'unsupported'}
          <p class="note">此兼容服务尚未适配账单查询。请到服务商后台查看余额与用量。</p>
        {:else}
          <div class="report-head"><span>组织账户 · UTC</span><button type="button" class="link refresh" disabled={!!busy || disabled || !settings.credential_configured} onclick={refresh}><svg viewBox="0 0 14 14" fill="none" aria-hidden="true"><path d="M11.6 5A4.8 4.8 0 1 0 11.8 9M11.7 1.8v3.5H8.2" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" /></svg>{busy === 'query' ? '查询中…' : '刷新'}</button></div>
          <dl class="metrics">
            <div><dt>本月费用 <span>USD</span></dt><dd>{formatBillingCost(snapshot?.month_cost)}</dd></div>
            <div><dt>今日费用 <span>USD</span></dt><dd>{formatBillingCost(snapshot?.today_cost)}</dd></div>
            <div><dt>本月对话输入 <span>token</span></dt><dd>{formatBillingTokens(snapshot?.month_input_tokens)}</dd></div>
            <div><dt>本月对话输出 <span>token</span></dt><dd>{formatBillingTokens(snapshot?.month_output_tokens)}</dd></div>
          </dl>
          {#if snapshot}<p class="note fetched" title={billingPeriodLabel(snapshot)}>更新于 {formatBillingTime(snapshot.fetched_at)}</p>{/if}
          {#if snapshot?.cost_error}<p class="error" role="alert">费用查询：{snapshot.cost_error}</p>{/if}
          {#if snapshot?.usage_error}<p class="error" role="alert">对话用量查询：{snapshot.usage_error}</p>{/if}
          <p class="note">按查询密钥所属组织统计，并非此模型 Key 或 Pilot 单独用量；报表可能延迟。{settings.provider === 'anthropic' ? '费用不含 Priority Tier。' : '对话 token 仅统计 Completions 用量接口覆盖的内容。'}</p>
          <div class="credential-row"><span>{settings.credential_configured ? '查询密钥已保存' : '需单独配置组织级查询密钥'}</span><button type="button" class="link" disabled={!!busy || disabled} onclick={() => { keyEditor = !keyEditor; apiKey = ''; error = ''; notice = ''; }}>{keyEditor ? '取消' : settings.credential_configured ? '更换' : '配置'}</button>{#if settings.credential_configured}<button type="button" class="icon-button" title="移除查询密钥" aria-label={`移除 ${name} 的查询密钥`} disabled={!!busy || disabled} onclick={() => confirmRemoval = true}><svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M3.4 4.5h9.2M6 2.5h4l.6 2M6.3 6.6v5M9.7 6.6v5M4.2 4.5l.6 9h6.4l.6-9" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" stroke-linejoin="round" /></svg></button>{/if}</div>
          {#if keyEditor}
            <form class="key-form" onsubmit={event => { event.preventDefault(); void saveKey(); }}>
              <label for={`billing-key-${id}`}>Admin API Key · 账单查询专用</label><div class="key-input"><input id={`billing-key-${id}`} type="password" bind:value={apiKey} autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" placeholder="输入独立的组织级查询密钥" disabled={!!busy || disabled} /><button type="submit" class="save" disabled={!apiKey.trim() || !!busy || disabled}>{busy === 'save' ? '保存中…' : '保存'}</button></div>
              <p class="note warning">此密钥权限较高，仅保存到系统凭据库并用于官方用量接口，不用于聊天。{settings.provider === 'anthropic' ? '普通工作区 Key 不可用于此查询。' : '普通模型调用 Key 不可用于此查询。'}</p>
            </form>
          {/if}
        {/if}
      {/if}
      {#if error}<p class="error" role="alert">{error}</p>{#if !settings && !busy}<button type="button" class="link" disabled={disabled} onclick={loadSettings}>重试</button>{/if}{/if}
      {#if notice}<p class="note" role="status">{notice}</p>{/if}
    </div>
  {/if}
</div>

{#if confirmRemoval}<ConfirmDialog title="移除查询密钥？" message={`将从系统凭据库移除「${name}」的账单查询密钥。模型 API Key 和已有对话不会改变。`} kind="danger" confirmText="移除密钥" onConfirm={() => void removeKey()} onCancel={() => confirmRemoval = false} />{/if}

<style>
  .api-billing { margin:4px 0 0 33px; min-width:0; font-size:11px; line-height:1.45; }
  button { font:inherit; cursor:pointer; }
  button:disabled { opacity:.45; cursor:default; }
  .disclosure,.link,.icon-button { display:inline-flex; align-items:center; justify-content:center; gap:4px; background:none; border:0; color:var(--dim); border-radius:5px; padding:2px 3px; }
  .disclosure { padding-left:0; }
  .disclosure:hover,.link:hover,.icon-button:hover { color:var(--text); background:var(--rail); }
  button:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
  svg { display:block; flex:none; width:12px; height:12px; }
  .disclosure svg { width:11px; height:11px; transition:transform .15s; }
  .disclosure svg.expanded { transform:rotate(90deg); }
  .billing-detail { margin-top:5px; padding:7px 0 1px; border-top:1px solid var(--border); min-width:0; }
  .balance-row,.report-head,.credential-row { display:flex; align-items:center; gap:7px; min-width:0; flex-wrap:wrap; }
  .balance-row { color:var(--dim); }
  .balance-row .link { margin-left:auto; white-space:nowrap; }
  .unavailable { color:var(--faint); }
  .report-head { margin-top:8px; color:var(--faint); font-size:10.5px; justify-content:space-between; }
  .metrics { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:7px 14px; margin:4px 0 6px; }
  .metrics > div { min-width:0; }
  dt { color:var(--dim); font-size:10.5px; }
  dt span { color:var(--faint); font-size:9.5px; }
  dd { margin:1px 0 0; color:var(--text); font-size:13px; font-weight:600; font-variant-numeric:tabular-nums; overflow-wrap:anywhere; }
  .note,.error { margin:5px 0 0; color:var(--faint); font-size:10.5px; line-height:1.45; overflow-wrap:anywhere; }
  .error { color:var(--err); }
  .warning { color:var(--dim); }
  .credential-row { margin-top:7px; gap:6px; color:var(--dim); font-size:10.5px; }
  .credential-row .icon-button { padding:3px; }
  .credential-row .icon-button:hover { color:var(--err); background:var(--err-soft); }
  .key-form { margin-top:6px; }
  .key-form label { display:block; color:var(--dim); margin-bottom:4px; }
  .key-input { display:flex; gap:5px; min-width:0; }
  input { box-sizing:border-box; min-width:0; flex:1; height:28px; border:1px solid var(--border2); border-radius:7px; padding:0 8px; background:var(--bg,#fff); color:var(--text); font:inherit; outline:none; }
  input:focus { border-color:var(--accent); }
  input::placeholder { color:var(--faint); }
  .save { flex:none; border:1px solid var(--border2); background:var(--bg,#fff); color:var(--text); border-radius:7px; padding:0 8px; }
  .save:hover:not(:disabled) { background:var(--rail); }
  @media (max-width:400px) { .api-billing { margin-left:0; } }
</style>
