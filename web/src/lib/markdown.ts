// Safe markdown rendering for live markdown cards (lazy chunk).
// markdown-it with raw HTML disabled, then DOMPurify as a second line of defence.
// Links open in a new tab without referrer; relative images resolve to the file API.

import DOMPurify from 'dompurify';
import MarkdownIt from 'markdown-it';

const md = new MarkdownIt({ html: false, linkify: true, typographer: false, breaks: false });

let imageBase: ((src: string) => string | null) | null = null;
let keepAnchors = false;

const ALERT_TITLE: Record<string, string> = { NOTE: 'Note', TIP: 'Tip', IMPORTANT: 'Important', WARNING: 'Warning', CAUTION: 'Caution' };

DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    const href = node.getAttribute('href') ?? '';
    if (href.startsWith('#')) {
      // in-page anchors would scroll the app, not the card, unless the page handles them
      if (!keepAnchors) node.removeAttribute('href');
    } else {
      node.setAttribute('target', '_blank');
      node.setAttribute('rel', 'noopener noreferrer nofollow');
    }
  }
  if (node.tagName === 'IMG') {
    const src = node.getAttribute('src') ?? '';
    const mapped = imageBase ? imageBase(src) : null;
    if (mapped) node.setAttribute('src', mapped);
    else node.removeAttribute('src'); // remote images are blocked by the CSP anyway
    node.setAttribute('loading', 'lazy');
  }
});

/**
 * Render markdown to sanitised HTML.
 * @param resolveImage maps a relative image src to a same-origin URL (or null to drop it)
 */
export function renderMarkdown(text: string, resolveImage?: (src: string) => string | null, opts: { anchors?: boolean } = {}): string {
  imageBase = resolveImage ?? null;
  keepAnchors = !!opts.anchors;
  try {
    // task lists: "- [x] done" (the sanitiser forbids real inputs, and these don't toggle)
    const raw = md
      .render(text)
      .replace(/<li>(<p>)?\[( |x|X)\]\s/g, (_m, p, box) => `<li class="task-item">${p ?? ''}<span class="task-box" aria-label="${box === ' ' ? 'To do' : 'Done'}">${box === ' ' ? '☐' : '☑'}</span> `)
      // GitHub alerts: "> [!NOTE]"
      .replace(/<blockquote>\s*<p>\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*/g, (_m, kind: string) => `<blockquote class="md-alert md-alert-${kind.toLowerCase()}"><p class="md-alert-title">${ALERT_TITLE[kind]}</p><p>`);
    return DOMPurify.sanitize(raw, { USE_PROFILES: { html: true }, FORBID_TAGS: ['style', 'form', 'input'] });
  } finally {
    imageBase = null;
  }
}
