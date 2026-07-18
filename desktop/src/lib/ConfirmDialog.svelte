<script lang="ts">
  type Kind = 'warning' | 'danger' | 'info';

  let {
    title = '请确认操作',
    message,
    kind = 'warning',
    confirmText = '确认',
    cancelText = '取消',
    onConfirm,
    onCancel,
  }: {
    title?: string;
    message: string;
    kind?: Kind;
    confirmText?: string;
    cancelText?: string;
    onConfirm: () => void;
    onCancel: () => void;
  } = $props();

  let confirmButton = $state<HTMLButtonElement | null>(null);

  $effect(() => {
    confirmButton?.focus();
  });

  function onKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      event.preventDefault();
      onCancel();
    } else if (event.key === 'Enter' && event.target === document.body) {
      event.preventDefault();
      onConfirm();
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

<div class="confirm-layer" role="presentation">
  <button class="confirm-backdrop" type="button" aria-label="关闭确认框" onclick={onCancel}></button>
  <dialog open class="confirm-card" role="alertdialog" aria-labelledby="confirm-title" aria-describedby="confirm-message">
    <div class="confirm-head">
      <div class="confirm-icon {kind}" aria-hidden="true">
        {#if kind === 'info'}
          <svg viewBox="0 0 24 24" fill="none"><circle cx="12" cy="12" r="9" stroke="currentColor" stroke-width="1.8"/><path d="M12 10.8v5.1M12 7.7v.2" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>
        {:else}
          <svg viewBox="0 0 24 24" fill="none"><path d="M12 3.4 21 19a1.4 1.4 0 0 1-1.2 2.1H4.2A1.4 1.4 0 0 1 3 19L12 3.4Z" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"/><path d="M12 9v5M12 17.3v.2" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>
        {/if}
      </div>
      <div class="confirm-copy">
        <h2 id="confirm-title">{title}</h2>
        <p id="confirm-message">{message}</p>
      </div>
    </div>
    <div class="confirm-actions">
      <button class="confirm-cancel" type="button" onclick={onCancel}>{cancelText}</button>
      <button bind:this={confirmButton} class="confirm-submit {kind}" type="button" onclick={onConfirm}>{confirmText}</button>
    </div>
  </dialog>
</div>

<style>
  .confirm-layer { position: fixed; inset: 0; z-index: 1000; display: grid; place-items: center; padding: 16px; isolation: isolate; }
  .confirm-backdrop { position: absolute; inset: 0; border: 0; padding: 0; background: color-mix(in srgb, #171817 38%, transparent); backdrop-filter: blur(3px); cursor: default; animation: confirm-fade .16s ease-out; }
  .confirm-card { position: relative; width: fit-content; min-width: 250px; max-width: min(320px, calc(100vw - 32px)); margin: 0; padding: 12px 14px 10px; border: 1px solid color-mix(in srgb, var(--border2) 78%, #fff); border-radius: 12px; background: var(--bg); box-shadow: 0 14px 42px rgba(32, 28, 20, .19), 0 2px 8px rgba(32, 28, 20, .08); animation: confirm-rise .2s cubic-bezier(.2,.8,.2,1); }
  .confirm-head { display: flex; align-items: flex-start; gap: 7px; min-width: 0; }
  .confirm-icon { flex: none; width: 28px; height: 28px; display: grid; place-items: center; border-radius: 7px; }
  .confirm-icon svg { width: 18px; height: 18px; }
  .confirm-icon.warning { color: #a57622; background: #fbf1dc; }
  .confirm-icon.danger { color: #b03e31; background: #fae7e3; }
  .confirm-icon.info { color: #54779a; background: #e8f0f8; }
  .confirm-copy h2 { margin: 0; color: var(--text); font-size: 16px; line-height: 1.35; letter-spacing: -.02em; }
  .confirm-copy p { margin: 4px 0 0; color: var(--dim); font-size: 12.5px; line-height: 1.5; white-space: pre-line; overflow-wrap: anywhere; }
  .confirm-actions { display: flex; justify-content: flex-end; gap: 6px; margin-top: 10px; }
  .confirm-actions button { min-width: 56px; height: 26px; padding: 0 8px; border-radius: 6px; font: inherit; font-size: 11.5px; font-weight: 600; cursor: pointer; transition: background .12s, border-color .12s, transform .12s; }
  .confirm-actions button:active { transform: translateY(1px); }
  .confirm-cancel { border: 1px solid var(--border2); background: transparent; color: var(--text); }
  .confirm-cancel:hover { background: var(--rail); }
  .confirm-submit { border: 1px solid var(--accent); background: var(--accent); color: #fff; }
  .confirm-submit:hover { background: var(--accent-h); border-color: var(--accent-h); }
  .confirm-submit.danger { border-color: var(--err); background: var(--err); }
  .confirm-submit.danger:hover { filter: brightness(.94); }
  .confirm-actions button:focus-visible { outline: 2px solid var(--accent-soft); outline-offset: 2px; }
  @keyframes confirm-fade { from { opacity: 0; } }
  @keyframes confirm-rise { from { opacity: 0; transform: translateY(7px) scale(.98); } }
  @media (max-width: 480px) { .confirm-card { padding: 11px 12px 9px; } .confirm-actions { margin-top: 9px; } .confirm-actions button { flex: 1; } }
</style>
