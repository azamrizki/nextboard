// Copy text. The async clipboard exists only on HTTPS and localhost, and a hub is often
// reached over plain HTTP on a private network, so the old way stays as the fallback.

export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // denied: the old way may still work
    }
  }
  return copyWithSelection(text);
}

/**
 * The old way: select the text in a hidden box and ask the browser to copy. Safari on iPhone and
 * iPad selects nothing in a read-only box and ignores select(): there the box stays editable, its
 * contents are selected as a range and with setSelectionRange, and it is 16px so that focusing it
 * does not zoom the page.
 */
function copyWithSelection(text: string): boolean {
  const before = document.activeElement as HTMLElement | null;
  const box = document.createElement('textarea');
  box.value = text;
  box.setAttribute('aria-hidden', 'true');
  box.inputMode = 'none'; // no on-screen keyboard for the moment it has the focus
  Object.assign(box.style, { position: 'fixed', top: '0', left: '0', width: '1px', height: '1px', padding: '0', border: '0', opacity: '0', fontSize: '16px' });
  document.body.append(box);
  let ok = false;
  try {
    box.focus({ preventScroll: true });
    box.select();
    const range = document.createRange();
    range.selectNodeContents(box);
    const sel = window.getSelection();
    sel?.removeAllRanges();
    sel?.addRange(range);
    box.setSelectionRange(0, text.length);
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  box.remove();
  window.getSelection()?.removeAllRanges();
  before?.focus?.({ preventScroll: true });
  return ok;
}
