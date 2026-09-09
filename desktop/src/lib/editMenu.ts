const textInputTypes = new Set(['text', 'search', 'url', 'tel', 'email', 'password', 'number']);

/** Resolve the actual editing host, not a syntax-highlight span inside it.
 * Never read the clipboard here: native menu actions retain OS paste handling.
 */
export function editMenuTarget(target: EventTarget | null): { element: HTMLElement; editable: boolean } | null {
  const element = target instanceof Element ? target : target instanceof Node ? target.parentElement : null;
  if (!element || element.closest('[inert]')) return null;
  const control = element.closest('input, textarea');
  if (control instanceof HTMLInputElement || control instanceof HTMLTextAreaElement) {
    if (control.matches(':disabled') || control.closest('[aria-disabled="true"]') || (control instanceof HTMLInputElement && !textInputTypes.has(control.type))) return null;
    return { element: control, editable: !control.readOnly && control.getAttribute('aria-readonly') !== 'true' };
  }
  // isContentEditable observes inheritance, plaintext-only and false islands.
  // Merely matching an ancestor [contenteditable] would allow false islands.
  if (!(element instanceof HTMLElement) || !element.isContentEditable) return null;
  let host = element;
  while (host.parentElement?.isContentEditable) host = host.parentElement;
  if (element.closest('[aria-disabled="true"]')) return null;
  return { element: host, editable: !element.closest('[aria-readonly="true"]') };
}
