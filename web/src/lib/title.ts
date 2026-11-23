// The page's title is two parts: the page's own (pageTitle(), "nextboard — NextBoard") and,
// before it, the status of the agents and the usage (tab/TabStatus.tsx, "2 idle · 45%"). A
// page sets its part, the status its own, and neither writes over the other.

let page = typeof document !== 'undefined' ? document.title : '';
let status = '';

function write() {
  if (typeof document === 'undefined') return;
  const t = status ? `${status} · ${page}` : page;
  if (document.title !== t) document.title = t;
}

/** The page's own title (from pageTitle()). */
export function setPageTitle(title: string): void {
  page = title;
  write();
}

/** The status before it, "" for none. */
export function setTitleStatus(text: string): void {
  status = text;
  write();
}
