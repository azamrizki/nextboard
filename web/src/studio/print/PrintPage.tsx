// Print route for PDFs (docs/API-studio.md §5, §8): /print/documents/<doc>/<version> and
// /print/markdown?machineId=&path=, with ?paper=a4|letter. Always light: a cover page
// (title, agent, project, date), contents when there are 3 or more headings, a running
// header and page numbers (@page margin boxes), every h1 on a new page, images as
// labelled placeholders (repo images for markdown files). Opens the browser's print
// dialog once rendered ("Save as PDF"); ?embed=1 only previews (paged view).

import { useEffect, useMemo, useState } from 'react';
import { api, errorMessage, fsRawUrl } from '../../api';
import { useMarkdown } from '../../board/lazy';
import { useProject } from '../../store';
import { docApi } from '../api';
import { placeholderImages, docLabel, type Paper } from '../documents/model';
import { stripFrontmatter } from '../MarkdownView';
import './print.css';

interface Source {
  title: string;
  markdown: string;
  meta: { label: string; value: string }[];
  /** Resolve relative images (markdown files only). */
  imageBase?: { machineId: string; dir: string };
}

function headingsOf(md: string): { level: number; text: string }[] {
  const out: { level: number; text: string }[] = [];
  let fence = false;
  for (const line of md.split('\n')) {
    if (/^\s*(```|~~~)/.test(line)) fence = !fence;
    if (fence) continue;
    const m = /^(#{1,2})\s+(.+?)\s*#*\s*$/.exec(line);
    if (m) out.push({ level: m[1].length, text: m[2].replace(/[*_`]/g, '') });
  }
  return out;
}

const cssString = (s: string) => `"${s.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, ' ')}"`;

interface Props {
  kind: 'documents' | 'markdown';
  docId?: string;
  versionId?: string;
  /** Rendered inside the app (Documents › Pages): no toolbar, no print dialog, no page rules. */
  inline?: boolean;
  paper?: Paper;
}

export default function PrintPage({ kind, docId, versionId, inline, paper: paperProp }: Props) {
  const params = new URLSearchParams(inline ? '' : window.location.search);
  const paper: Paper = paperProp ?? (params.get('paper') === 'letter' ? 'letter' : 'a4');
  const embed = inline || params.get('embed') === '1';
  const [src, setSrc] = useState<Source | null>(null);
  const [projectId, setProjectId] = useState<string | undefined>();
  const project = useProject(projectId);
  const [error, setError] = useState('');
  const md = useMarkdown();

  useEffect(() => {
    if (inline) return;
    document.documentElement.classList.add('print-mode');
    return () => document.documentElement.classList.remove('print-mode');
  }, [inline]);

  useEffect(() => {
    let stop = false;
    (async () => {
      try {
        if (kind === 'documents' && docId && versionId) {
          const [doc, ver] = await Promise.all([docApi.get(docId), docApi.version(docId, versionId)]);
          if (stop) return;
          setProjectId(doc.projectId);
          const title = doc.title || docLabel(doc.type);
          setSrc({
            title,
            markdown: ver.markdown,
            meta: [
              { label: 'Written by', value: ver.version.agent === 'codex' ? 'Codex' : 'Claude Code' },
              { label: 'Date', value: new Date(ver.version.at).toLocaleDateString(undefined, { dateStyle: 'long' }) },
              ...(ver.version.state === 'incomplete' ? [{ label: 'Status', value: 'Draft (incomplete)' }] : []),
            ],
          });
        } else {
          const machineId = params.get('machineId') ?? '';
          const path = params.get('path') ?? '';
          const r = await api.fsRead(machineId, path, 2_000_000);
          if (stop) return;
          const h1 = /^#\s+(.+)$/m.exec(r.content)?.[1];
          const name = path.split('/').pop() ?? path;
          setSrc({
            title: h1?.trim() || name.replace(/\.(md|markdown|mdx)$/i, ''),
            markdown: r.content,
            meta: [
              { label: 'File', value: path },
              { label: 'Date', value: new Date(r.mtime || Date.now()).toLocaleDateString(undefined, { dateStyle: 'long' }) },
            ],
            imageBase: { machineId, dir: path.slice(0, path.lastIndexOf('/') + 1) },
          });
        }
      } catch (err) {
        if (!stop) setError(errorMessage(err));
      }
    })();
    return () => {
      stop = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, docId, versionId]);

  // the cover already shows the title: drop a leading "# Title"
  const body = useMemo(() => {
    if (!src) return '';
    const lines = stripFrontmatter(src.markdown.replace(/^\uFEFF/, '')).split('\n');
    const first = lines.findIndex((l) => l.trim());
    if (first >= 0 && /^#\s+/.test(lines[first]) && lines[first].replace(/^#\s+/, '').trim() === src.title) lines.splice(first, 1);
    return lines.join('\n');
  }, [src]);
  const heads = useMemo(() => headingsOf(body), [body]);

  const html = useMemo(() => {
    if (!md || !src) return '';
    const base = src.imageBase;
    const out = md.renderMarkdown(
      body,
      base
        ? (s) => {
            if (/^[a-z]+:/i.test(s) || s.startsWith('//')) return null;
            const p = s.startsWith('/') ? s : base.dir + s;
            return fsRawUrl(base.machineId, p);
          }
        : undefined,
    );
    return base ? out : placeholderImages(out);
  }, [md, src, body]);

  // ids for the contents links
  const withIds = useMemo(() => {
    let i = 0;
    return html.replace(/<h([12])>/g, (_m, l) => `<h${l} id="sec-${i++}">`);
  }, [html]);

  const projectName = project?.name ?? '';
  useEffect(() => {
    if (!src || inline) return;
    document.title = `${src.title}${projectName ? ` — ${projectName}` : ''}`;
  }, [src, projectName, inline]);

  useEffect(() => {
    if (!withIds || embed) return;
    const t = window.setTimeout(() => {
      void document.fonts?.ready.then(() => window.print());
    }, 400);
    return () => window.clearTimeout(t);
  }, [withIds, embed]);

  if (error) {
    return (
      <div className="print-root">
        <p className="print-error" role="alert">
          Could not load the page to print: {error}
        </p>
      </div>
    );
  }
  if (!src || !withIds) {
    return (
      <div className="print-root">
        <p className="print-loading" role="status">
          Preparing the PDF…
        </p>
      </div>
    );
  }
  const pageCss = `@page { size: ${paper === 'letter' ? 'letter' : 'A4'}; margin: 20mm 18mm 22mm;
  @top-left { content: ${cssString(src.title)}; font: 500 8.5pt var(--font-ui); color: var(--print-ink-3); }
  @top-right { content: ${cssString(projectName)}; font: 500 8.5pt var(--font-ui); color: var(--print-ink-3); }
  @bottom-center { content: counter(page) " / " counter(pages); font: 500 8.5pt var(--font-ui); color: var(--print-ink-3); } }
  @page :first { @top-left { content: none; } @top-right { content: none; } @bottom-center { content: none; } }`;
  const meta = projectName && !src.imageBase ? [{ label: 'Project', value: projectName }, ...src.meta] : src.meta;
  return (
    <div className={`print-root paper-${paper} ${embed ? 'is-embed' : ''} ${inline ? 'is-inline' : ''}`}>
      {!inline && <style>{pageCss}</style>}
      {!embed && (
        <div className="print-toolbar">
          <span className="print-toolbar-title">{src.title}</span>
          <span className="print-spacer" />
          <a className="print-btn" href={`?${new URLSearchParams({ ...Object.fromEntries(params), paper: paper === 'a4' ? 'letter' : 'a4' }).toString()}`}>
            Switch to {paper === 'a4' ? 'Letter' : 'A4'}
          </a>
          <button type="button" className="print-btn is-primary" onClick={() => window.print()}>
            Print or save as PDF
          </button>
        </div>
      )}
      <article className="print-doc">
        <section className="print-cover">
          <p className="cover-kicker">{src.imageBase ? 'Markdown' : 'Document'}</p>
          <h1 className="cover-title">{src.title}</h1>
          <dl className="cover-meta">
            {meta.map((m) => (
              <div key={m.label}>
                <dt>{m.label}</dt>
                <dd>{m.value}</dd>
              </div>
            ))}
          </dl>
          <p className="cover-foot">Made with NextBoard</p>
        </section>
        {heads.length >= 3 && (
          <nav className="print-toc" aria-label="Contents">
            <h2>Contents</h2>
            <ol>
              {heads.map((h, i) => (
                <li key={i} className={`toc-l${h.level}`}>
                  <a href={`#sec-${i}`}>{h.text}</a>
                </li>
              ))}
            </ol>
          </nav>
        )}
        {/* eslint-disable-next-line react/no-danger */}
        <div className="print-body md-doc" dangerouslySetInnerHTML={{ __html: withIds }} />
      </article>
    </div>
  );
}
