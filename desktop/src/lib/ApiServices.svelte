<script lang="ts">
  import { tick } from 'svelte';
  import { invoke } from '@tauri-apps/api/core';
  import BrainIcon from './BrainIcon.svelte';
  import Dropdown from './Dropdown.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import ApiServiceMenu from './ApiServiceMenu.svelte';
  import ApiBilling from './ApiBilling.svelte';
  import { API_PROTOCOL_OPTIONS, apiEndpointChanged, apiEndpointHost, apiMaxOutputAfterProtocolChange, apiProfileInput, apiProtocolLabel, defaultApiMaxOutput, normalizeApiEndpoint, parseApiModels, validateApiProfileInput, type ApiProfile, type ApiProfileInput, type ApiProtocol, type ApiTestResult } from './apiProfiles';

  let { profiles, onchange }: { profiles: ApiProfile[]; onchange: (profiles: ApiProfile[]) => void } = $props();

  let editorId = $state<string | null>(null);
  let original = $state<ApiProfile>();
  let name = $state('');
  let protocol = $state<ApiProtocol>('openai_responses');
  let baseUrl = $state('https://api.openai.com/v1');
  let apiKey = $state('');
  let modelText = $state('');
  let defaultModel = $state('');
  let maxOutput = $state(defaultApiMaxOutput('openai_responses'));
  let maxOutputEdited = $state(false);
  let advanced = $state(false);
  let busy = $state('');
  let busyLabel = $state('');
  let error = $state('');
  let editorError = $state('');
  let editorNotice = $state('');
  let suggestions = $state<string[]>([]);
  let modelQuery = $state('');
  let deleteTarget = $state<ApiProfile>();
  let nameInput = $state<HTMLInputElement>();
  let results = $state<Record<string, { ok: boolean; message: string }>>({});
  const models = $derived(parseApiModels(modelText));
  const endpointChanged = $derived(apiEndpointChanged(original, { base_url: baseUrl, protocol }));
  const displayedSuggestions = $derived(suggestions.filter(model => model.toLowerCase().includes(modelQuery.trim().toLowerCase())));

  function replaceProfile(profile: ApiProfile) {
    const found = profiles.some(item => item.id === profile.id);
    onchange(found ? profiles.map(item => item.id === profile.id ? profile : item) : [...profiles, profile]);
  }

  async function edit(profile?: ApiProfile) {
    if (busy) return;
    original = profile;
    editorId = profile?.id ?? '';
    name = profile?.name ?? 'OpenAI 官方';
    protocol = profile?.protocol ?? 'openai_responses';
    baseUrl = profile?.base_url ?? 'https://api.openai.com/v1';
    apiKey = '';
    modelText = profile?.models.join('\n') ?? '';
    defaultModel = profile?.default_model ?? '';
    maxOutput = profile?.max_output_tokens ?? defaultApiMaxOutput(protocol);
    maxOutputEdited = false;
    advanced = !!profile && baseUrl !== API_PROTOCOL_OPTIONS.find(option => option.value === protocol)?.baseUrl;
    editorError = ''; editorNotice = ''; suggestions = []; modelQuery = '';
    await tick();
    nameInput?.focus();
  }

  function closeEditor() {
    if (busy) return;
    editorId = null; original = undefined; apiKey = ''; editorError = ''; editorNotice = ''; suggestions = [];
  }

  function changeProtocol(value: string) {
    const previous = API_PROTOCOL_OPTIONS.find(option => option.value === protocol);
    const next = API_PROTOCOL_OPTIONS.find(option => option.value === value);
    if (!next) return;
    if (!name.trim() || name === previous?.name) name = next.name;
    maxOutput = apiMaxOutputAfterProtocolChange(next.value, maxOutput, { existing: !!original, edited: maxOutputEdited });
    protocol = next.value;
    baseUrl = next.baseUrl;
    advanced = !next.baseUrl;
    suggestions = []; editorNotice = '';
  }

  function draft(): ApiProfileInput {
    return {
      ...(editorId ? { id: editorId } : {}), name: name.trim(), protocol,
      base_url: normalizeApiEndpoint(baseUrl), enabled: original?.enabled ?? true,
      models, default_model: models.includes(defaultModel) ? defaultModel : models[0] ?? '',
      max_output_tokens: Number(maxOutput),
    };
  }

  async function persist(): Promise<ApiProfile | null> {
    const profile = draft();
    editorError = validateApiProfileInput(profile);
    if (editorError) return null;
    if (original?.key_configured && endpointChanged && !apiKey.trim()) {
      editorError = '接口地址或协议已改变，请重新输入该接口的 API Key，旧密钥不会自动发送到新地址。';
      return null;
    }
    try {
      const saved = await invoke<ApiProfile>('save_api_profile', { profile, apiKey: apiKey.trim() || null });
      apiKey = '';
      original = saved; editorId = saved.id; defaultModel = saved.default_model;
      replaceProfile(saved);
      const next = { ...results }; delete next[saved.id]; results = next;
      return saved;
    } catch (cause) { editorError = String(cause); return null; }
  }

  async function save() {
    if (busy) return;
    busy = editorId || 'new'; busyLabel = '保存中…'; editorNotice = '';
    const saved = await persist();
    busy = ''; busyLabel = '';
    if (saved) closeEditor();
  }

  async function discover() {
    if (busy) return;
    if (!apiKey.trim() && !original?.key_configured) { editorError = '请先填写 API Key，再保存并读取模型。'; return; }
    busy = editorId || 'new'; busyLabel = '读取模型中…'; editorNotice = ''; suggestions = [];
    const saved = await persist();
    if (saved) {
      try {
        suggestions = await invoke<string[]>('list_api_models', { id: saved.id });
        editorNotice = suggestions.length ? `已保存连接，读取到 ${suggestions.length} 个模型。请选择支持对话与工具调用的模型。` : '连接已保存，但接口未返回模型。可以手动填写模型 ID。';
      } catch (cause) { editorError = `连接已保存，读取模型失败：${String(cause)}。也可以手动填写模型 ID。`; }
    }
    busy = ''; busyLabel = '';
  }

  function toggleModel(model: string) {
    modelText = (models.includes(model) ? models.filter(item => item !== model) : [...models, model]).join('\n');
    if (!parseApiModels(modelText).includes(defaultModel)) defaultModel = parseApiModels(modelText)[0] ?? '';
  }

  async function test(profile: ApiProfile) {
    if (busy) return;
    busy = profile.id; busyLabel = '测试连接中…'; error = '';
    try {
      const result = await invoke<ApiTestResult>('test_api_profile', { id: profile.id });
      results = { ...results, [profile.id]: { ok: result.ok, message: result.message } };
    } catch (cause) { results = { ...results, [profile.id]: { ok: false, message: String(cause) } }; }
    finally { busy = ''; busyLabel = ''; }
  }

  async function toggle(profile: ApiProfile) {
    if (busy) return;
    busy = profile.id; busyLabel = '保存中…'; error = '';
    try {
      const saved = await invoke<ApiProfile>('save_api_profile', { profile: { ...apiProfileInput(profile), enabled: !profile.enabled }, apiKey: null });
      replaceProfile(saved);
      if (editorId === profile.id) original = saved;
    } catch (cause) { error = String(cause); }
    finally { busy = ''; busyLabel = ''; }
  }

  async function remove() {
    const profile = deleteTarget;
    if (!profile || busy) return;
    deleteTarget = undefined; busy = profile.id; busyLabel = '删除中…'; error = '';
    try {
      await invoke('delete_api_profile', { id: profile.id });
      onchange(profiles.filter(item => item.id !== profile.id));
      const next = { ...results }; delete next[profile.id]; results = next;
      if (editorId === profile.id) { editorId = null; original = undefined; apiKey = ''; }
    } catch (cause) { error = String(cause); }
    finally { busy = ''; busyLabel = ''; }
  }

  function status(profile: ApiProfile): string {
    if (busy === profile.id) return busyLabel;
    if (!profile.enabled) return '已停用';
    if (!profile.key_configured) return '待配置密钥';
    if (!profile.models.length) return '待添加模型';
    return results[profile.id] ? results[profile.id].ok ? '连接已验证' : '连接失败' : '已配置 · 未测试';
  }
</script>

<section class="api-services" aria-label="API 服务">
  <div class="section-head"><span>API 服务</span><button type="button" class="add" disabled={!!busy} onclick={() => edit()}><svg viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>添加</button></div>
  {#if error}<p class="message error" role="alert">{error}</p>{/if}
  {#if !profiles.length && editorId === null}<p class="empty">使用自己的 API Key 接入 OpenAI、Claude 或兼容服务。</p>{/if}
  <div class="service-list">
    {#each profiles as profile (profile.id)}
      <div class="service" class:disabled={!profile.enabled}>
        <div class="service-row">
          <span class="service-icon" aria-hidden="true">{@render providerIcon(profile.protocol)}</span>
          <div class="service-copy"><b title={profile.name}>{profile.name}</b><small title={`${apiProtocolLabel(profile.protocol)} · ${profile.base_url}${profile.default_model ? ` · ${profile.default_model}` : ''}`}>{apiEndpointHost(profile.base_url)}{profile.default_model ? ` · ${profile.default_model}` : ` · ${apiProtocolLabel(profile.protocol)}`}</small></div>
          <span class="connection-state" class:verified={profile.enabled && results[profile.id]?.ok} class:failed={profile.enabled && results[profile.id]?.ok === false} title={results[profile.id]?.message ?? status(profile)}><i aria-hidden="true"></i><span>{status(profile)}</span></span>
          <ApiServiceMenu name={profile.name} enabled={profile.enabled} busy={!!busy} onedit={() => edit(profile)} ontest={() => test(profile)} ontoggle={() => toggle(profile)} ondelete={() => deleteTarget = profile} />
        </div>
        {#if results[profile.id]}<p class="test-result" class:error={!results[profile.id].ok} role="status">{results[profile.id].message}</p>{/if}
        {#if editorId === profile.id}{@render editor()}
        {:else}{#key `${profile.id}:${profile.protocol}:${profile.base_url}`}<ApiBilling id={profile.id} name={profile.name} disabled={!!busy} />{/key}{/if}
      </div>
    {/each}
  </div>
  {#if editorId === ''}{@render editor()}{/if}
  <p class="section-note">API 独立计费；密钥只存入系统凭据库。API 连接及密钥暂不随配置导出。测试连接只读取模型列表，不会调用模型生成内容。</p>
  <p class="section-note">支持文本对话、技能、GCMS 和 SSH 工具。图片 / PDF 识别和 Cloudflare 授权操作请暂用本地客户端。</p>
</section>

{#snippet providerIcon(value: ApiProtocol)}
  {#if value === 'anthropic_messages'}<BrainIcon brain="claude" size={18} />
  {:else if value === 'openai_responses'}<BrainIcon brain="openai" size={18} />
  {:else}<svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="m6.5 5-5 5 5 5M13.5 5l5 5-5 5M12 3 8 17" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>{/if}
{/snippet}

{#snippet editor()}
  <form class="editor" aria-label={editorId ? '编辑 API 连接' : '添加 API 连接'} onsubmit={event => { event.preventDefault(); void save(); }}>
    <div class="fields">
      <div class="field"><span id="api-protocol-label">服务类型</span><Dropdown value={protocol} options={API_PROTOCOL_OPTIONS} ariaLabel="API 服务类型" disabled={!!busy} onchange={changeProtocol} /></div>
      <label class="field"><span>连接名称</span><input bind:this={nameInput} bind:value={name} maxlength="80" placeholder="例如 OpenAI 工作账号" disabled={!!busy} /></label>
      <label class="field full"><span>API Key{original?.key_configured ? ' · 已安全保存' : ''}</span><input type="password" bind:value={apiKey} autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" placeholder={original?.key_configured && !endpointChanged ? '留空保留现有密钥' : '输入该接口的 API Key'} disabled={!!busy} /></label>
      {#if original?.key_configured && endpointChanged}<p class="inline-note warning full">接口地址或协议已改变，需要重新输入 API Key。</p>{/if}
      <div class="field full"><div class="field-head"><label for="api-model-ids">可选模型</label><button type="button" class="link" disabled={!!busy} onclick={discover}>{busyLabel === '读取模型中…' ? busyLabel : '保存并读取模型'}</button></div><textarea id="api-model-ids" bind:value={modelText} rows="2" placeholder="每行一个模型 ID，也可从服务端读取后添加" disabled={!!busy}></textarea></div>
      {#if suggestions.length}
        <div class="discovery full">
          <input bind:value={modelQuery} aria-label="筛选 API 模型" placeholder="筛选返回的模型…" disabled={!!busy} />
          <div class="model-options">
            {#each displayedSuggestions as model (model)}<label><input type="checkbox" checked={models.includes(model)} onchange={() => toggleModel(model)} disabled={!!busy} /><span title={model}>{model}</span></label>{/each}
            {#if !displayedSuggestions.length}<small>没有匹配的模型</small>{/if}
          </div>
        </div>
      {/if}
      {#if models.length}<div class="field full"><span>默认模型</span><Dropdown value={models.includes(defaultModel) ? defaultModel : models[0]} options={models.map(model => ({ value: model, label: model }))} ariaLabel="API 默认模型" disabled={!!busy} onchange={value => defaultModel = value} /></div>{/if}
    </div>
    <button type="button" class="advanced-toggle" aria-expanded={advanced} disabled={!!busy} onclick={() => advanced = !advanced}><svg class:open={advanced} viewBox="0 0 12 12" fill="none" aria-hidden="true"><path d="m4.5 3 3 3-3 3" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></svg>高级设置<span>{advanced ? '' : '接口地址 · 输出上限'}</span></button>
    {#if advanced}<div class="fields advanced"><label class="field full"><span>API 基础地址</span><input bind:value={baseUrl} type="url" placeholder="https://api.example.com/v1" autocomplete="off" spellcheck="false" disabled={!!busy} /><small>填写基础地址，不包含 /responses、/messages 或 /chat/completions。</small></label><label class="field"><span>单次最大输出 token</span><input type="number" bind:value={maxOutput} oninput={() => maxOutputEdited = true} aria-describedby="api-max-output-help" min="1" max="1000000" step="1" disabled={!!busy} /></label><p id="api-max-output-help" class="inline-note full">上限包含模型思考与工具调用参数；长代码任务可在模型支持时设为 32768。{#if protocol === 'anthropic_messages'}Claude 达到上限时，当前响应最多按原上限重试一次，不会自动提高上限。{/if}</p></div>{/if}
    {#if editorNotice}<p class="message" role="status">{editorNotice}</p>{/if}
    {#if editorError}<p class="message error" role="alert">{editorError}</p>{/if}
    <div class="editor-footer"><span>仅配置支持工具调用的对话模型。</span><div><button type="button" class="button" onclick={closeEditor} disabled={!!busy}>取消</button><button type="submit" class="button primary" disabled={!!busy}>{busy ? busyLabel : '保存'}</button></div></div>
  </form>
{/snippet}

{#if deleteTarget}
  <ConfirmDialog title="删除 API 连接？" message={`将删除「${deleteTarget.name}」及保存的模型 API Key、账单查询密钥，不会删除已有对话。使用此连接的任务需要重新选择模型。`} kind="danger" confirmText="删除连接" onConfirm={() => void remove()} onCancel={() => deleteTarget = undefined} />
{/if}

<style>
  .api-services { margin-top:20px; min-width:0; }
  .section-head { display:flex; align-items:center; justify-content:space-between; gap:12px; color:var(--faint); font-size:11px; font-weight:600; margin-bottom:10px; }
  .add,.link,.advanced-toggle { display:inline-flex; align-items:center; gap:5px; background:none; border:0; font:inherit; cursor:pointer; color:var(--dim); }
  .add { padding:3px 5px; border-radius:6px; font-size:11.5px; font-weight:400; }
  .add:hover,.link:hover { color:var(--text); background:var(--rail); }
  .add svg { display:block; width:13px; height:13px; }
  button:disabled { opacity:.45; cursor:default; }
  .service-list { display:flex; flex-direction:column; gap:14px; }
  .service-row { display:flex; align-items:center; gap:11px; min-width:0; }
  .service-icon { width:22px; height:22px; flex:none; display:flex; align-items:center; justify-content:center; color:var(--dim); }
  .service-icon :global(svg) { display:block; }
  .service-copy { min-width:0; flex:1; display:flex; flex-direction:column; gap:2px; }
  .service-copy b { font-size:13.5px; line-height:1.25; font-weight:600; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .service-copy small { font-size:11px; line-height:1.3; color:var(--dim); overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
  .disabled .service-copy,.disabled .service-icon { opacity:.55; }
  .connection-state { display:inline-flex; align-items:center; gap:5px; color:var(--faint); font-size:10.5px; white-space:nowrap; }
  .connection-state i { display:block; width:6px; height:6px; border-radius:50%; background:currentColor; flex:none; }
  .connection-state.verified { color:var(--ok,#24965c); }
  .connection-state.failed { color:var(--err); }
  .empty,.section-note { margin:0; color:var(--faint); font-size:11px; line-height:1.5; }
  .section-note { margin-top:10px; font-size:10.5px; }
  .test-result { margin:5px 0 0 33px; color:var(--dim); font-size:10.5px; line-height:1.4; overflow-wrap:anywhere; }
  .editor { padding:11px 0 0 33px; margin-top:5px; min-width:0; }
  .fields { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:9px 11px; }
  .field { display:flex; flex-direction:column; gap:4px; min-width:0; }
  .field > span,.field-head > label { color:var(--dim); font-size:11px; line-height:1.3; }
  .full { grid-column:1 / -1; }
  input:not([type="checkbox"]),textarea { box-sizing:border-box; width:100%; min-width:0; border:1px solid var(--border2,#e1dfd8); border-radius:8px; color:var(--text); background:var(--bg,#fff); font:inherit; font-size:12px; outline:none; }
  input:not([type="checkbox"]) { height:30px; padding:0 9px; }
  textarea { padding:7px 9px; line-height:1.5; resize:vertical; min-height:50px; max-height:160px; }
  input:focus,textarea:focus { border-color:var(--accent); }
  input:disabled,textarea:disabled { opacity:.55; }
  input::placeholder,textarea::placeholder { color:var(--faint); }
  .field-head { display:flex; justify-content:space-between; align-items:center; gap:8px; }
  .link { font-size:10.5px; padding:2px 4px; border-radius:5px; }
  .advanced-toggle { margin-top:9px; padding:3px 0; font-size:11px; }
  .advanced-toggle > span { color:var(--faint); margin-left:3px; font-size:10.5px; }
  .advanced-toggle svg { width:11px; height:11px; flex:none; transition:transform .15s; }
  .advanced-toggle svg.open { transform:rotate(90deg); }
  .advanced { margin-top:7px; }
  .field small,.inline-note { color:var(--faint); font-size:10px; line-height:1.4; margin:0; }
  .inline-note.warning { color:var(--warn,#a66d12); }
  .discovery { min-width:0; border:1px solid var(--border); border-radius:8px; padding:6px; }
  .discovery > input { height:26px; background:var(--rail); border-color:transparent; font-size:11px; }
  .model-options { max-height:112px; overflow:auto; padding-top:4px; }
  .model-options label { display:flex; align-items:center; gap:6px; font-size:11px; padding:4px 3px; cursor:pointer; min-width:0; }
  .model-options span { min-width:0; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
  .model-options input { margin:0; accent-color:var(--accent); flex:none; }
  .model-options small { font-size:11px; color:var(--faint); padding:5px; display:block; }
  .message { margin:8px 0 0; color:var(--dim); font-size:11px; line-height:1.5; overflow-wrap:anywhere; }
  .error { color:var(--err); }
  .editor-footer { display:flex; align-items:center; justify-content:space-between; gap:10px; margin-top:10px; }
  .editor-footer > span { color:var(--faint); font-size:10px; line-height:1.4; }
  .editor-footer > div { display:flex; align-items:center; gap:6px; flex:none; }
  .button { height:27px; border:1px solid var(--border2); border-radius:7px; background:transparent; padding:0 10px; color:var(--text); font:inherit; font-size:11.5px; cursor:pointer; }
  .button:hover:not(:disabled) { background:var(--rail); }
  .button.primary { color:#fff; background:var(--accent); border-color:var(--accent); }
  .button.primary:hover:not(:disabled) { background:var(--accent-h); }
  @media (max-width:560px) { .connection-state > span { display:none; } .fields { grid-template-columns:minmax(0,1fr); } .editor { padding-left:0; } }
</style>
