import { describe, expect, it } from 'vitest';
import type { ProjectPart } from '../lib/types';
import { factsWhere, groupFacts, isPartSlug, partDescription, partMemories, partReserved, partSlug, routedFacts, splitPartDocs } from './parts';

const admin: ProjectPart = { id: 'pt_abcde23456', name: 'admin', machineId: 'local', path: '/srv/shop/admin' };
const front: ProjectPart = { id: 'pt_zyxw98765k', name: 'frontend', machineId: 'm_devbox', path: '/home/web/front' };
const machine = (id: string) => (id === 'local' ? 'This PC' : 'devbox');
const two = partMemories([admin, front], machine);

const doc = (name: string, over: Partial<{ kind: string; critical: boolean; part: string; chars: number }> = {}) => ({ name, kind: 'sub', chars: 10, ...over });

describe('part memory names', () => {
  it('is "part-" and the id without its prefix, as the hub has it', () => {
    expect(partSlug('pt_abcde23456')).toBe('part-abcde23456');
    expect(isPartSlug(partSlug(admin.id))).toBe(true);
    // a name a person would give a page is not one
    expect(isPartSlug('part-time-work')).toBe(false);
    expect(isPartSlug('part-ABCDE23456')).toBe(false);
    expect(isPartSlug('design')).toBe(false);
  });

  it('a new page of a project cannot take a name the hub keeps for parts', () => {
    // only a name a part's page could have is kept: "part-time-work" is an ordinary page
    expect(partReserved('p_shop', 'part-time-work')).toBe(false);
    expect(partReserved('p_shop', 'part-ab23cd45ef')).toBe(true);
    // a project in one part keeps no part names
    expect(partReserved('p_shop', 'part-ab23cd45ef', 1)).toBe(false);
    expect(partReserved('p_shop', 'partners')).toBe(false);
    // the memory of all projects has no parts
    expect(partReserved('', 'part-time-work')).toBe(false);
  });

  it('a project of one part has no part memories at all', () => {
    expect(partMemories([admin], machine)).toEqual([]);
    expect(partMemories([], machine)).toEqual([]);
  });

  it('each part has its page, named and placed as it is now', () => {
    expect(two).toEqual([
      { id: admin.id, name: 'admin', machine: 'This PC', path: '/srv/shop/admin', slug: 'part-abcde23456' },
      { id: front.id, name: 'frontend', machine: 'devbox', path: '/home/web/front', slug: 'part-zyxw98765k' },
    ]);
    expect(partDescription(two[1])).toBe('What holds for the frontend part only (devbox, /home/web/front).');
  });
});

describe('splitPartDocs', () => {
  const docs = [
    doc('main', { kind: 'main' }),
    doc('critical', { critical: true }),
    doc('design'),
    doc('part-abcde23456', { part: admin.id }),
    doc('note:x.md', { kind: 'note' }),
  ];

  it('gives each current part its page, or none, and keeps part pages out of the other pages', () => {
    const s = splitPartDocs(docs, two);
    expect(s.parts.map((r) => [r.part.name, r.doc?.name])).toEqual([
      ['admin', 'part-abcde23456'],
      ['frontend', undefined],
    ]);
    expect(s.pages.map((d) => d.name)).toEqual(['design']);
  });

  it('a page of a part that is gone is one more page, not lost', () => {
    const s = splitPartDocs([...docs, doc('part-gone56789k', { part: 'pt_gone56789k' })], two);
    expect(s.pages.map((d) => d.name)).toEqual(['design', 'part-gone56789k']);
  });

  it('finds a part page by its name when the listing does not say whose it is', () => {
    const s = splitPartDocs([doc('part-zyxw98765k')], two);
    expect(s.parts[1].doc?.name).toBe('part-zyxw98765k');
    expect(s.pages).toEqual([]);
  });

  it('in a project of one part every page is listed as before', () => {
    const s = splitPartDocs(docs, []);
    expect(s.parts).toEqual([]);
    expect(s.pages.map((d) => d.name)).toEqual(['design', 'part-abcde23456']);
  });
});

describe('groupFacts', () => {
  const facts = [
    { kind: 'Commands', text: 'make test', part: '' },
    { kind: 'Commands', text: 'npm run dev', part: front.id },
    { kind: 'Gotchas', text: 'php 8.3 only', part: admin.id },
    { kind: 'Context', text: 'from a part removed since', part: 'pt_gone56789k' },
    { kind: 'Structure', text: 'two folders' },
  ];

  it('puts the whole project first, then each part in the parts’ order, keeping the indexes', () => {
    const g = groupFacts(facts, two);
    expect(g.map((x) => [x.part, x.label, x.items.map((i) => i.index)])).toEqual([
      ['', 'Whole project', [0, 3, 4]],
      [admin.id, 'admin', [2]],
      [front.id, 'frontend', [1]],
    ]);
  });

  it('leaves out a part with no facts', () => {
    expect(groupFacts([facts[1]], two).map((x) => x.label)).toEqual(['frontend']);
  });

  it('a project of one part has one group', () => {
    const g = groupFacts(facts, []);
    expect(g).toHaveLength(1);
    expect(g[0].items).toHaveLength(5);
  });
});

describe('routedFacts', () => {
  it('keeps a current part, and sends the rest to the whole project without a part', () => {
    const out = routedFacts(
      [
        { kind: 'Commands', text: 'a', part: admin.id },
        { kind: 'Commands', text: 'b', part: '' },
        { kind: 'Commands', text: 'c', part: 'pt_gone56789k' },
        { kind: 'Commands', text: 'd' },
      ],
      two,
    );
    expect(out).toEqual([
      { kind: 'Commands', text: 'a', part: admin.id },
      { kind: 'Commands', text: 'b' },
      { kind: 'Commands', text: 'c' },
      { kind: 'Commands', text: 'd' },
    ]);
  });

  it('a project of one part sends facts as it always did', () => {
    expect(routedFacts([{ kind: 'Commands', text: 'a', part: '' }], [])).toEqual([{ kind: 'Commands', text: 'a' }]);
    expect(JSON.stringify(routedFacts([{ kind: 'Commands', text: 'a', part: admin.id }], []))).toBe('[{"kind":"Commands","text":"a"}]');
  });

  it('says where the facts went', () => {
    expect(
      factsWhere(
        [
          { kind: 'Commands', text: 'a', part: front.id },
          { kind: 'Commands', text: 'b' },
          { kind: 'Commands', text: 'c', part: front.id },
        ],
        two,
      ),
    ).toBe('1 to the whole project, 2 to frontend');
  });
});
