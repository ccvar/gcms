<script lang="ts">
  import { invoke } from '@tauri-apps/api/core';
  import { onMount } from 'svelte';
  import Dropdown from './Dropdown.svelte';
  import type { Connection, Site } from './types';

  type Binding = {
    id: string; conn_id: string; api_base: string; device_name: string;
    default_site_id: number; last_seen_at: number; last_error: string; connected: boolean;
  };

  let { connection, sites = [], appVersion = '', brain = 'codex', model = '', effort = '', onopen, showTrigger = true }:
    { connection: Connection; sites?: Site[]; appVersion?: string; brain?: string; model?: string; effort?: string; onopen?: () => void; showTrigger?: boolean } = $props();
  let binding = $state<Binding | null>(null);
  let control: HTMLElement;
  let open = $state(false);
  let busy = $state(false);
  let error = $state('');
  let deviceName = $state('');
  let defaultSiteId = $state('0');

  function hostOf(url?: string): string {
    if (!url) return '';
    try { return new URL(url).host; } catch { return url.replace(/^https?:\/\//i, '').replace(/\/.*$/, ''); }
  }
  function faviconGuess(url?: string): string {
    if (!url) return '';
    try { return `${new URL(url).origin}/favicon.ico`; } catch { return ''; }
  }
  function siteOptions() {
    return [
      { value: '0', label: '不设置', sub: '按当前授权临时选择' },
      ...sites.map((site) => ({
        value: String(site.id),
        label: site.name || site.slug,
        sub: hostOf(site.url) || '未绑定域名',
        img: site.favicon || site.logo || faviconGuess(site.url),
      })),
    ];
  }

  async function load() {
    try {
      const all = await invoke<Binding[]>('pilot_console_status');
      binding = all.find(item => item.conn_id === connection.id) ?? null;
      defaultSiteId = String(binding?.default_site_id ?? 0);
    } catch (e) { error = String(e); }
  }
  $effect(() => { connection.id; void load(); });
  onMount(() => {
    const close = (event: PointerEvent) => {
      if (open && control && !control.contains(event.target as Node)) open = false;
    };
    const openRequested = (event: Event) => {
      if ((event as CustomEvent<string>).detail !== connection.id) return;
      open = true;
      onopen?.();
      void load();
    };
    window.addEventListener('pointerdown', close);
    window.addEventListener('pilot-console-open', openRequested);
    return () => {
      window.removeEventListener('pointerdown', close);
      window.removeEventListener('pilot-console-open', openRequested);
    };
  });

  function toggle(event: MouseEvent) {
    event.stopPropagation();
    open = !open;
    if (open) {
      onopen?.();
      void load();
    }
  }

  async function bind() {
    busy = true; error = '';
    try {
      binding = await invoke<Binding>('pilot_console_bind', {
        connId: connection.id, deviceName, pilotVersion: appVersion || 'unknown',
        defaultSiteId: Number(defaultSiteId), defaultBrain: brain, defaultModel: model, defaultEffort: effort,
      });
    } catch (e) { error = String(e); } finally { busy = false; }
  }
  async function unbind() {
    if (!binding || !confirm('解除后 GCMS 旧设备凭据会立即失效；技能包连接和历史对话会保留。确定解除？')) return;
    busy = true; error = '';
    try { await invoke('pilot_console_unbind', { bindingId: binding.id }); binding = null; }
    catch (e) { error = String(e); } finally { busy = false; }
  }
  async function reconnect() {
    await invoke('pilot_console_reconnect'); await new Promise(r => setTimeout(r, 800)); await load();
  }
  async function setDefault(value: string) {
    if (!binding) return;
    const siteId = Number(value);
    try {
      binding = await invoke<Binding>('pilot_console_set_default_site', { bindingId: binding.id, siteId });
      defaultSiteId = String(siteId);
    } catch (e) { error = String(e); }
  }
</script>

<div class="pilot-control" bind:this={control}>
  {#if showTrigger}
    <button
      class="pilot-trigger"
      class:active={open}
      class:bound={!!binding}
      aria-label="Pilot 控制台"
      aria-haspopup="dialog"
      aria-expanded={open}
      title={binding ? `Pilot 控制台 · ${binding.connected ? '在线' : '离线'}` : '绑定 Pilot 控制台'}
      onclick={toggle}
    >
      <svg width="15" height="15" viewBox="0 0 18 18" fill="none" aria-hidden="true">
        <rect x="2.25" y="2.75" width="13.5" height="10.5" rx="2" stroke="currentColor" stroke-width="1.35" />
        <path d="M6.5 15.25h5M9 13.4v1.85M5.1 6.1h7.8M5.1 8.75h4.8" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" />
      </svg>
      {#if binding}<i class:online={binding.connected} class:error={!!binding.last_error}></i>{/if}
    </button>
  {/if}

  {#if open}
    <section class="pilot-binding-panel" class:bound={!!binding} aria-label="Pilot 控制台设置">
      <header class="pb-head">
        <span class="pb-mark">
          <svg width="19" height="19" viewBox="0 0 18 18" fill="none" aria-hidden="true">
            <rect x="2.25" y="2.75" width="13.5" height="10.5" rx="2" stroke="currentColor" stroke-width="1.35" />
            <path d="M6.5 15.25h5M9 13.4v1.85M5.1 6.1h7.8M5.1 8.75h4.8" stroke="currentColor" stroke-width="1.25" stroke-linecap="round" />
          </svg>
        </span>
        <span class="pb-title">
          <b>Pilot 控制台</b>
          <small>{binding ? (binding.connected ? '手机网页已可控制这台 Pilot' : '绑定已保留，等待设备重新上线') : '让手机浏览器远程发起真实对话'}</small>
        </span>
        {#if binding}<em class:online={binding.connected}>{binding.connected ? '在线' : '离线'}</em>{/if}
        <button class="pb-close" aria-label="收起 Pilot 控制台" title="收起" onclick={() => (open = false)}>×</button>
      </header>

      {#if binding}
        <div class="pb-summary">
          <div><span>设备</span><b>{binding.device_name || '未命名设备'}</b></div>
          <div><span>GCMS</span><b title={binding.api_base}>{binding.api_base.replace(/\/pilot$/, '').replace(/^https?:\/\//, '')}</b></div>
        </div>
        {#if sites.length}
          <details class="pb-advanced">
            <summary><span>更多设置</span><small>默认站点：{sites.find((site) => String(site.id) === defaultSiteId)?.name || '未设置'}</small></summary>
            <label class="pb-field">
              <span>新对话默认站点 <small>仅用于未明确选站点的新对话，不改变权限</small></span>
              <div class="pb-dropdown">
                <Dropdown
                  searchable={sites.length > 6}
                  menuCompact
                  bind:value={defaultSiteId}
                  options={siteOptions()}
                  onchange={setDefault}
                />
              </div>
            </label>
          </details>
        {/if}
        {#if binding.last_error}<p class="pb-error">{binding.last_error}。请检查 GCMS 可访问性、技能包权限或版本。</p>{/if}
        <div class="pb-actions">
          <button class="pb-button" onclick={reconnect} disabled={busy}>重新连接</button>
          <button class="pb-button danger" onclick={unbind} disabled={busy}>解除绑定</button>
        </div>
      {:else}
        <div class="pb-intro"><b>绑定当前 GCMS 技能包</b><span>使用现有授权范围，不开放本机端口，也不依赖 SSH 或 Cloudflare。</span></div>
        <div class="pb-form">
          <label class="pb-field">
            <span>设备名称 <small>选填</small></span>
            <input bind:value={deviceName} placeholder="例如：工作室 Mac" />
          </label>
          <button class="pb-primary" onclick={bind} disabled={busy}>
            {#if busy}<span class="pb-spinner"></span>正在绑定…{:else}绑定 Pilot 控制台{/if}
          </button>
        </div>
        {#if sites.length}
          <details class="pb-advanced unbound">
            <summary><span>更多设置</span><small>可选：为新对话预设站点</small></summary>
            <label class="pb-field">
              <span>新对话默认站点 <small>不改变技能包授权范围</small></span>
              <div class="pb-dropdown">
                <Dropdown
                  searchable={sites.length > 6}
                  menuCompact
                  bind:value={defaultSiteId}
                  options={siteOptions()}
                />
              </div>
            </label>
          </details>
        {/if}
      {/if}
      {#if error}<p class="pb-error">{error}</p>{/if}
    </section>
  {/if}
</div>

<style>
  .pilot-control{display:contents}
  .pilot-trigger{position:relative;display:inline-grid;place-items:center;flex:none;width:27px;height:27px;padding:0;border:1px solid transparent;border-radius:7px;background:transparent;color:var(--dim);cursor:pointer;transition:background .14s,border-color .14s,color .14s,box-shadow .14s}
  .pilot-trigger:hover{background:#f0f2f7;color:#315cff}.pilot-trigger.active{border-color:#cbd5ff;background:#eef2ff;color:#315cff;box-shadow:0 0 0 2px #315cff12}.pilot-trigger.bound{color:#53627d}
  .pilot-trigger i{position:absolute;right:2px;bottom:2px;width:6px;height:6px;border:1.5px solid var(--surface);border-radius:50%;background:#d58b29}.pilot-trigger i.online{background:#23a765}.pilot-trigger i.error{background:#c84f42}
  .pilot-binding-panel{order:20;flex:0 0 100%;box-sizing:border-box;margin:2px 0 1px;padding:12px;border:1px solid #dfe3ea;border-radius:13px;background:linear-gradient(180deg,#fbfcfe 0%,#f8f9fc 100%);box-shadow:0 8px 22px #2633480b;cursor:default}.pilot-binding-panel.bound{border-color:#d7def3;background:linear-gradient(180deg,#fbfcff 0%,#f6f8ff 100%)}
  .pb-head{display:flex;align-items:center;gap:10px}.pb-mark{display:grid;place-items:center;flex:none;width:34px;height:34px;border-radius:10px;background:#e9eeff;color:#315cff}.pb-title{display:grid;min-width:0;gap:2px;flex:1}.pb-title b{font-size:13px;line-height:1.2}.pb-title small{overflow:hidden;color:var(--faint);font-size:10.5px;line-height:1.35;text-overflow:ellipsis;white-space:nowrap}.pb-head em{flex:none;padding:3px 7px;border-radius:99px;background:#fff1dc;color:#96631c;font-size:9.5px;font-style:normal;font-weight:650}.pb-head em.online{background:#e6f6ec;color:#26774a}
  .pb-close{display:grid;place-items:center;flex:none;width:24px;height:24px;padding:0;border:0;border-radius:7px;background:transparent;color:var(--faint);font-size:17px;line-height:1;cursor:pointer}.pb-close:hover{background:#eceff4;color:var(--text)}
  .pb-intro{display:grid;gap:2px;margin:11px 0 9px;padding:8px 10px;border-radius:9px;background:#fff}.pb-intro b{font-size:11px}.pb-intro span{color:var(--dim);font-size:9.5px;line-height:1.4}
  .pb-form{display:flex;align-items:flex-end;gap:8px}.pb-form .pb-field{flex:1}.pb-field{display:grid;min-width:0;gap:4px;color:var(--dim);font-size:9.5px;font-weight:600}.pb-field>span{display:flex;align-items:baseline;gap:5px}.pb-field>span small{color:var(--faint);font-size:8.5px;font-weight:400}.pb-field input{width:100%;height:30px;box-sizing:border-box;border:1px solid #d9dee7;border-radius:7px;outline:none;background:#fff;padding:0 9px;color:var(--text);font:inherit;font-size:10px;transition:border-color .14s,box-shadow .14s}.pb-field input:focus{border-color:#90a6f3;box-shadow:0 0 0 2px #315cff17}.pb-dropdown{--ctl-h:30px}.pb-dropdown :global(.dd-trigger){border-color:#d9dee7;border-radius:7px;padding:0 9px;font-size:10px}.pb-dropdown :global(.dd-trigger.open){border-color:#90a6f3;box-shadow:0 0 0 2px #315cff17}
  .pb-primary{display:flex;align-items:center;justify-content:center;flex:none;gap:6px;width:132px;height:30px;border:0;border-radius:7px;background:#315cff;color:#fff;font-size:10px;font-weight:650;cursor:pointer;box-shadow:0 3px 9px #315cff1f}.pb-primary:hover:not(:disabled){background:#274ee3}.pb-primary:disabled{opacity:.58;cursor:default}
  .pb-spinner{width:11px;height:11px;border:1.5px solid #ffffff66;border-top-color:#fff;border-radius:50%;animation:spin .7s linear infinite}
  .pb-summary{display:grid;grid-template-columns:minmax(0,.8fr) minmax(0,1.2fr);gap:8px;margin:12px 0}.pb-summary div{display:grid;gap:3px;min-width:0;padding:9px 10px;border:1px solid #e6e9ef;border-radius:9px;background:#fff}.pb-summary span{color:var(--faint);font-size:9px}.pb-summary b{overflow:hidden;font-size:10.5px;text-overflow:ellipsis;white-space:nowrap}
  .pb-advanced{margin-top:9px;border-top:1px solid #e6e9ef;padding-top:7px}.pb-advanced summary{display:flex;align-items:center;gap:7px;color:var(--dim);font-size:9.5px;cursor:pointer;list-style:none}.pb-advanced summary::-webkit-details-marker{display:none}.pb-advanced summary::before{content:'›';color:var(--faint);font-size:14px;line-height:1;transition:transform .14s}.pb-advanced[open] summary::before{transform:rotate(90deg)}.pb-advanced summary span{font-weight:600}.pb-advanced summary small{overflow:hidden;color:var(--faint);font-size:8.5px;text-overflow:ellipsis;white-space:nowrap}.pb-advanced .pb-field{margin-top:8px}.pb-advanced.unbound{margin-top:8px}
  .pb-actions{display:flex;justify-content:flex-end;gap:6px;margin-top:9px}.pb-button{height:28px;padding:0 10px;border:1px solid #d9dee7;border-radius:7px;background:#fff;color:var(--text);font-size:9.5px;font-weight:550;cursor:pointer}.pb-button:hover:not(:disabled){border-color:#b8c1d0;background:#f8f9fb}.pb-button.danger{color:#a54a40}.pb-button:disabled{opacity:.55;cursor:default}
  .pb-error{margin:9px 0 0;padding:8px 10px;border:1px solid #efd2cd;border-radius:8px;background:#fff5f3;color:#a44136;font-size:9.5px;line-height:1.5}
  @keyframes spin{to{transform:rotate(360deg)}}
  @media(max-width:520px){.pb-form{align-items:stretch;flex-direction:column}.pb-primary{align-self:flex-end}.pb-summary{grid-template-columns:1fr}.pb-title small{white-space:normal}}
</style>
