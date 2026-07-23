<script lang="ts">
  type Kind = 'warning' | 'danger';

  let {
    title,
    message,
    account = 'admin',
    instance = '',
    kind = 'warning',
    confirmText = '确认操作',
    busy = false,
    error = '',
    onConfirm,
    onCancel,
  }: {
    title: string;
    message: string;
    account?: string;
    instance?: string;
    kind?: Kind;
    confirmText?: string;
    busy?: boolean;
    error?: string;
    onConfirm: (password: string) => void;
    onCancel: () => void;
  } = $props();

  let password = $state('');
  let visible = $state(false);
  let input = $state<HTMLInputElement | null>(null);

  $effect(() => {
    input?.focus();
  });

  function cancel() {
    if (busy) return;
    password = '';
    onCancel();
  }

  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (busy || !password) return;
    onConfirm(password);
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      event.preventDefault();
      cancel();
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

<div class="password-layer" role="presentation">
  <button class="password-backdrop" type="button" aria-label="关闭密码确认框" onclick={cancel}></button>
  <dialog open class="password-card" role="alertdialog" aria-labelledby="gcms-password-title" aria-describedby="gcms-password-message">
    <form onsubmit={submit}>
      <div class="password-head">
        <div class="password-icon {kind}" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none"><rect x="5" y="10" width="14" height="10" rx="2.5" stroke="currentColor" stroke-width="1.7"/><path d="M8.5 10V7.5a3.5 3.5 0 0 1 7 0V10M12 14v2" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/></svg>
        </div>
        <div class="password-copy">
          <h2 id="gcms-password-title">{title}</h2>
          <p id="gcms-password-message">{message}</p>
        </div>
      </div>

      <div class="password-context">
        <span>GCMS 账号 <b>{account || 'admin'}</b></span>
        {#if instance}<i>·</i><span title={instance}>{instance}</span>{/if}
      </div>

      <label class="password-field">
        <span>登录密码</span>
        <div class="password-input-wrap">
          <input
            bind:this={input}
            bind:value={password}
            type={visible ? 'text' : 'password'}
            autocomplete="current-password"
            autocapitalize="none"
            spellcheck="false"
            placeholder="输入 GCMS 后台登录密码"
            disabled={busy}
            aria-invalid={error ? 'true' : 'false'}
          />
          <button class="password-eye" type="button" tabindex="-1" aria-label={visible ? '隐藏密码' : '显示密码'} title={visible ? '隐藏密码' : '显示密码'} onclick={() => (visible = !visible)} disabled={busy}>
            {#if visible}
              <svg viewBox="0 0 24 24" fill="none"><path d="m4 4 16 16M10.6 10.7a2 2 0 0 0 2.7 2.7M9.2 5.5A9.8 9.8 0 0 1 12 5c5.5 0 8.5 7 8.5 7a15.3 15.3 0 0 1-2.2 3.3M6.3 7.1A15.5 15.5 0 0 0 3.5 12s3 7 8.5 7c1 0 1.9-.2 2.7-.5" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>
            {:else}
              <svg viewBox="0 0 24 24" fill="none"><path d="M3.5 12s3-7 8.5-7 8.5 7 8.5 7-3 7-8.5 7-8.5-7-8.5-7Z" stroke="currentColor" stroke-width="1.7"/><circle cx="12" cy="12" r="2.6" stroke="currentColor" stroke-width="1.7"/></svg>
            {/if}
          </button>
        </div>
      </label>

      {#if error}<p class="password-error" role="alert">{error}</p>{/if}
      <p class="password-note">密码仅用于本次远端校验，不会保存到 Pilot。</p>

      <div class="password-actions">
        <button class="password-cancel" type="button" onclick={cancel} disabled={busy}>取消</button>
        <button class="password-submit {kind}" type="submit" disabled={busy || !password}>
          {busy ? '验证中…' : confirmText}
        </button>
      </div>
    </form>
  </dialog>
</div>

<style>
  .password-layer { position: fixed; inset: 0; z-index: 1010; display: grid; place-items: center; padding: 16px; isolation: isolate; }
  .password-backdrop { position: absolute; inset: 0; border: 0; padding: 0; background: color-mix(in srgb, #171817 40%, transparent); backdrop-filter: blur(3px); cursor: default; }
  .password-card { position: relative; width: min(320px, calc(100vw - 32px)); margin: 0; padding: 13px 14px 11px; border: 1px solid color-mix(in srgb, var(--border2) 78%, #fff); border-radius: 13px; background: var(--bg); box-shadow: 0 16px 46px rgba(32, 28, 20, .2), 0 2px 8px rgba(32, 28, 20, .08); animation: password-rise .2s cubic-bezier(.2,.8,.2,1); }
  .password-head { display: flex; align-items: flex-start; gap: 8px; }
  .password-icon { flex: none; width: 29px; height: 29px; display: grid; place-items: center; border-radius: 8px; }
  .password-icon svg { width: 18px; height: 18px; }
  .password-icon.warning { color: #9b6c1d; background: #fbf1dc; }
  .password-icon.danger { color: #b03e31; background: #fae7e3; }
  .password-copy { min-width: 0; }
  .password-copy h2 { margin: 0; color: var(--text); font-size: 15.5px; line-height: 1.35; letter-spacing: -.02em; }
  .password-copy p { margin: 3px 0 0; color: var(--dim); font-size: 12px; line-height: 1.48; white-space: pre-line; overflow-wrap: anywhere; }
  .password-context { display: flex; align-items: center; gap: 5px; min-width: 0; margin: 9px 0 6px 37px; color: var(--dim); font-size: 11.5px; }
  .password-context span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .password-context b { color: var(--text); font-weight: 650; }
  .password-context i { color: var(--border2); font-style: normal; }
  .password-field { display: block; }
  .password-field > span { display: block; margin: 0 0 4px 2px; color: var(--dim); font-size: 11.5px; font-weight: 600; }
  .password-input-wrap { position: relative; }
  .password-input-wrap input { width: 100%; height: 34px; box-sizing: border-box; padding: 0 37px 0 10px; border: 1px solid var(--border2); border-radius: 8px; background: var(--bg); color: var(--text); font: inherit; font-size: 12.5px; outline: none; transition: border-color .12s, box-shadow .12s; }
  .password-input-wrap input:focus { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
  .password-input-wrap input[aria-invalid="true"] { border-color: color-mix(in srgb, var(--err) 62%, var(--border2)); }
  .password-eye { position: absolute; right: 4px; top: 4px; width: 27px; height: 26px; display: grid; place-items: center; border: 0; border-radius: 6px; background: transparent; color: var(--dim); cursor: pointer; }
  .password-eye:hover { color: var(--text); background: var(--rail); }
  .password-eye svg { width: 16px; height: 16px; }
  .password-error { margin: 6px 1px 0; padding: 6px 8px; border-radius: 7px; color: var(--err); background: color-mix(in srgb, var(--err) 8%, transparent); font-size: 11.5px; line-height: 1.4; overflow-wrap: anywhere; }
  .password-note { margin: 5px 1px 0; color: var(--faint); font-size: 10.5px; line-height: 1.4; }
  .password-actions { display: flex; justify-content: flex-end; gap: 6px; margin-top: 9px; }
  .password-actions button { min-width: 60px; height: 27px; padding: 0 9px; border-radius: 6px; font: inherit; font-size: 11.5px; font-weight: 650; cursor: pointer; }
  .password-actions button:disabled { opacity: .48; cursor: default; }
  .password-cancel { border: 1px solid var(--border2); background: transparent; color: var(--text); }
  .password-cancel:hover:not(:disabled) { background: var(--rail); }
  .password-submit { border: 1px solid var(--accent); background: var(--accent); color: #fff; }
  .password-submit:hover:not(:disabled) { background: var(--accent-h); border-color: var(--accent-h); }
  .password-submit.danger { border-color: var(--err); background: var(--err); }
  .password-actions button:focus-visible { outline: 2px solid var(--accent-soft); outline-offset: 2px; }
  @keyframes password-rise { from { opacity: 0; transform: translateY(7px) scale(.98); } }
</style>
