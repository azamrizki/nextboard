// A clock for what is on screen: ticks while the element is in view and the tab is shown, and
// stops otherwise, so a page full of countdowns costs nothing where nobody looks.

/**
 * Calls tick every `ms` while el is in view and the document is visible; once more as soon as it
 * shows again after a pause, so what it shows is never stale. Returns the stop.
 */
export function tickWhileVisible(el: Element, ms: number, tick: () => void): () => void {
  let inView = true; // until the observer says otherwise
  let timer = 0;
  let paused = false;
  const update = () => {
    const on = inView && !document.hidden;
    if (on && !timer) {
      if (paused) tick();
      paused = false;
      timer = window.setInterval(tick, ms);
    } else if (!on && timer) {
      window.clearInterval(timer);
      timer = 0;
      paused = true;
    }
  };
  const io =
    typeof IntersectionObserver === 'function'
      ? new IntersectionObserver((entries) => {
          inView = entries[entries.length - 1]?.isIntersecting ?? inView;
          update();
        })
      : null;
  io?.observe(el);
  document.addEventListener('visibilitychange', update);
  update();
  return () => {
    io?.disconnect();
    document.removeEventListener('visibilitychange', update);
    if (timer) window.clearInterval(timer);
    timer = 0;
  };
}
