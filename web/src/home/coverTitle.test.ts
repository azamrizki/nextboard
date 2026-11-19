import { describe, expect, it } from 'vitest';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { coverTitle, ProjectCover } from './ProjectArt';
import type { Project } from '../lib/types';

// The same cases as the hub's TestDisplayName (internal/art), so both drop the same labels.
describe('coverTitle', () => {
  it('drops working labels around the name', () => {
    const cases: Record<string, string> = {
      'PROD - Lumen': 'Lumen',
      'DEV - Orchard': 'Orchard',
      tidepool: 'tidepool',
      'shop (staging)': 'shop',
      'api-dev': 'api',
      '[UAT] Harbor Lounge': 'Harbor Lounge',
      'Prod: Tide pool': 'Tide pool',
      PROD: 'PROD',
      'dev - ': '',
      'Say "hi"\nnow': 'Say hi now',
      'Production Planner': 'Production Planner',
    };
    for (const [name, want] of Object.entries(cases)) expect(coverTitle(name), name).toBe(want);
  });
  it('keeps at most 40 characters', () => {
    expect(coverTitle('x'.repeat(60))).toHaveLength(40);
  });
});


// Every cover gets the name set over it (from the project's name now, so a rename shows at once),
// an AI cover also its tagline, the same colours on every project; no cover, nothing.
describe('ProjectCover', () => {
  const base = { id: 'p_a', name: 'PROD - Lumen', machineId: 'local', path: '/srv/lumen' } as unknown as Project;
  const html = (cover: Project['cover']) => renderToStaticMarkup(createElement(ProjectCover, { project: { ...base, cover } }));
  it('sets the title over a background cover', () => {
    const out = html({ assetId: 'a_1', source: 'ai', at: '', title: true, tagline: 'Lights that follow you', accent: '#88E500' });
    expect(out).toContain('class="cover-title">Lumen<');
    expect(out).toContain('class="cover-tagline">Lights that follow you<');
  });
  it('ignores a stored accent, and keeps the text of the tagline as text', () => {
    const out = html({ assetId: 'a_1', source: 'ai', at: '', title: true, tagline: '<b>x</b> y', accent: 'red;background:url(x)' });
    expect(out).not.toContain('--cover-accent');
    expect(out).toContain('&lt;b&gt;x&lt;/b&gt; y');
  });
  it('sets the name over an uploaded or older cover too, without a tagline', () => {
    for (const cover of [{ assetId: 'a_1', source: 'upload', at: '' }, { assetId: 'a_1', source: 'ai', at: '' }] as const) {
      const out = html(cover);
      expect(out).toContain('class="cover-title">Lumen<');
      expect(out).not.toContain('cover-tagline');
    }
  });
  it('sets nothing without a cover', () => {
    expect(html(undefined)).not.toContain('cover-frame');
  });
});
