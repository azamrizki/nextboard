// Documents (E2): labels, progress wording and the print/PDF route.

import type { DocType, Effort } from '../types';

export const DOC_LABEL: Record<DocType, string> = {
  overview: 'Project overview',
  onboarding: 'Onboarding guide',
  architecture: 'Architecture',
  readme: 'README',
  api: 'API reference',
  dataModel: 'Data model',
  security: 'Security overview',
  testing: 'Testing guide',
  releaseNotes: 'Release notes',
  custom: 'Ask for anything',
};

export const DOC_HINT: Record<DocType, string> = {
  overview: 'What it is, how it’s built, where to start',
  onboarding: 'Setup, workflow and first tasks for a new teammate',
  architecture: 'Components, data flow and the main decisions',
  readme: 'A README you could commit',
  api: 'Endpoints or public functions with examples',
  dataModel: 'Entities, fields and how they relate',
  security: 'Secrets, auth, inputs and risky spots',
  testing: 'How tests are organised and run',
  releaseNotes: 'What changed recently, from the repo',
  custom: 'Your own question about the code',
};

export const EFFORT_LABEL: Record<Effort, string> = { quick: 'Quick', balanced: 'Balanced', thorough: 'Thorough' };
export const EFFORT_WORDS: Record<Effort, string> = { quick: '400–700 words', balanced: '700–1,100 words', thorough: '900–1,500 words' };

export const PHASE_TEXT: Record<string, string> = {
  launching: 'Launching',
  thinking: 'Thinking',
  writing: 'Writing',
  finishing: 'Finishing',
};

export function docLabel(type: string): string {
  return DOC_LABEL[type as DocType] ?? type;
}

export function durationText(ms: number | null | undefined): string {
  if (!ms) return 'not timed';
  const s = Math.round(ms / 1000);
  if (s < 60) return `≈ ${s}s`;
  return `≈ ${Math.round(s / 60)} min`;
}

export type Paper = 'a4' | 'letter';

export function printDocUrl(docId: string, versionId: string, paper: Paper, embed = false): string {
  return `/print/documents/${encodeURIComponent(docId)}/${encodeURIComponent(versionId)}?paper=${paper}${embed ? '&embed=1' : ''}`;
}

export function printMarkdownUrl(machineId: string, path: string, paper: Paper): string {
  return `/print/markdown?machineId=${encodeURIComponent(machineId)}&path=${encodeURIComponent(path)}&paper=${paper}`;
}

/** Images in generated documents become labelled placeholders (§5). */
export function placeholderImages(html: string): string {
  return html.replace(/<img\b[^>]*?>/gi, (tag) => {
    const alt = (/\balt="([^"]*)"/i.exec(tag)?.[1] ?? '').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    return `<span class="img-ph" role="img" aria-label="Image placeholder">Image${alt ? `: ${alt}` : ''}</span>`;
  });
}

export function download(name: string, text: string, type = 'text/markdown') {
  const url = URL.createObjectURL(new Blob([text], { type: `${type};charset=utf-8` }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export const fileSafe = (s: string) => s.replace(/[^\w.-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 60) || 'document';
