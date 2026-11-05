// Rendered markdown for the studios (markdown-it without raw HTML + DOMPurify, loaded on
// first use). Relative links can be intercepted (markdown studio opens .md files in
// place); images either resolve through a callback or become labelled placeholders.

import { useMemo, type MouseEvent } from 'react';
import { useMarkdown } from '../board/lazy';
import { placeholderImages } from './documents/model';

interface Props {
  text: string;
  className?: string;
  /** Map a relative image src to a same-origin URL; omitted = labelled placeholders. */
  resolveImage?: (src: string) => string | null;
  /** Relative link clicked (not http/mailto): return true when handled. */
  onLink?: (href: string) => boolean;
  /** Keep "#heading" links: the page scrolls them itself through onLink. */
  anchors?: boolean;
}

/** YAML frontmatter (--- … ---) is metadata, not part of the page. */
export function stripFrontmatter(text: string): string {
  const m = /^\uFEFF?---\r?\n[\s\S]*?\r?\n---[ \t]*(\r?\n|$)/.exec(text);
  return m ? text.slice(m[0].length) : text;
}

export function MarkdownView({ text, className, resolveImage, onLink, anchors }: Props) {
  const md = useMarkdown();
  const html = useMemo(() => {
    if (!md) return '';
    const out = md.renderMarkdown(stripFrontmatter(text), resolveImage, { anchors });
    return resolveImage ? out : placeholderImages(out);
  }, [md, text, resolveImage, anchors]);
  const click = (e: MouseEvent<HTMLDivElement>) => {
    if (!onLink) return;
    const a = (e.target as HTMLElement).closest('a');
    const href = a?.getAttribute('href') ?? '';
    if (!href || /^[a-z]+:/i.test(href) || href.startsWith('//')) return;
    if (onLink(decodeURIComponent(href))) e.preventDefault();
  };
  if (!md) return <p className="fs-xs text-3">Rendering…</p>;
  // eslint-disable-next-line react/no-danger
  return <div className={`md-doc ${className ?? ''}`} onClick={click} dangerouslySetInnerHTML={{ __html: html }} />;
}
