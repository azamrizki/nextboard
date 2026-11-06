// A long press is a right-click (DESIGN.md §3.0). Android Chrome fires contextmenu for one by
// itself, other browsers do not, so this fires it after HOLD ms of a still finger. Whichever comes
// first opens the menu and the other is dropped, and the lift that ends a long press is not a tap.
//
// Only where a right-click means something: the board, and elements marked data-longpress. Text
// fields keep the browser's own long press (select, paste).

const HOLD = 500;
const SLOP = 10;
const SCOPE = '.board-canvas, [data-longpress]';
const SKIP = 'input, textarea, select, [contenteditable="true"], [data-no-longpress]';

export function installLongPress(root: Document | HTMLElement = document): () => void {
  let timer = 0;
  let press: { id: number; x: number; y: number; target: Element } | null = null;
  let firedAt = 0;

  const clear = () => {
    window.clearTimeout(timer);
    press = null;
  };
  const down = (e: Event) => {
    const p = e as PointerEvent;
    if (p.pointerType !== 'touch') return;
    // a second finger makes it a gesture, not a press
    if (press) {
      clear();
      return;
    }
    const target = p.target as Element | null;
    if (!target?.closest?.(SCOPE) || target.closest(SKIP)) return;
    press = { id: p.pointerId, x: p.clientX, y: p.clientY, target };
    timer = window.setTimeout(() => {
      const at = press;
      clear();
      if (!at || !at.target.isConnected) return;
      firedAt = Date.now();
      at.target.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, composed: true, clientX: at.x, clientY: at.y, button: 2, view: window }));
      navigator.vibrate?.(8);
    }, HOLD);
  };
  const move = (e: Event) => {
    const p = e as PointerEvent;
    if (press && p.pointerId === press.id && Math.hypot(p.clientX - press.x, p.clientY - press.y) > SLOP) clear();
  };
  const up = (e: Event) => {
    if (press && (e as PointerEvent).pointerId === press.id) clear();
  };
  const nativeMenu = (e: Event) => {
    if (!e.isTrusted) return;
    // the browser's came first: it opens the menu, this one stands down
    if (press) {
      clear();
      return;
    }
    if (Date.now() - firedAt < 1000) {
      e.preventDefault();
      e.stopPropagation();
    }
  };
  const click = (e: Event) => {
    if (Date.now() - firedAt < 800) {
      e.preventDefault();
      e.stopPropagation();
      firedAt = 0;
    }
  };

  root.addEventListener('pointerdown', down, true);
  root.addEventListener('pointermove', move, true);
  root.addEventListener('pointerup', up, true);
  root.addEventListener('pointercancel', up, true);
  root.addEventListener('contextmenu', nativeMenu, true);
  root.addEventListener('click', click, true);
  return () => {
    clear();
    root.removeEventListener('pointerdown', down, true);
    root.removeEventListener('pointermove', move, true);
    root.removeEventListener('pointerup', up, true);
    root.removeEventListener('pointercancel', up, true);
    root.removeEventListener('contextmenu', nativeMenu, true);
    root.removeEventListener('click', click, true);
  };
}
