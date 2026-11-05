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
  const before = document.activeElement as HTMLElement | null;
  const box = document.createElement('textarea');
  box.value = text;
  box.setAttribute('readonly', '');
  box.style.position = 'fixed';
  box.style.opacity = '0';
  document.body.append(box);
  box.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  box.remove();
  before?.focus?.();
  return ok;
}
