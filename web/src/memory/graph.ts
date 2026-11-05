// Turns every main memory into the nodes and links the galaxy draws: all projects
// at the core, each project a star on a spiral arm, its pages in orbit around it,
// their headings as small moons.

import type { GalaxyDoc, GalaxyOwner, GalaxyView } from './types';

export type NodeKind = 'core' | 'star' | 'critical' | 'page' | 'note' | 'section';
export type LinkKind = 'galaxy' | 'orbit' | 'section' | 'wiki';

export interface MemNode {
  id: string;
  kind: NodeKind;
  owner: string;
  doc: string;
  section?: string;
  title: string;
  description?: string;
  chars: number;
  facts: number;
  mtime?: string;
  parent: string | null;
  /** The project's colour. */
  hue: string;
  /** Order among its siblings, for a stable first layout. */
  order: number;
  siblings: number;
}

export interface MemLink {
  source: string;
  target: string;
  kind: LinkKind;
  hue: string;
}

/** Fixed order per project (checked with the dataviz palette validator). A project past the end gets the neutral, never a made-up hue. */
export const PROJECT_HUES = ['#5b8cff', '#16b8a9', '#b07cf0', '#cf9326', '#e866a8', '#55b552'];
export const NEUTRAL_HUE = '#9aa6c4';
export const CORE_HUE = '#ffd98a';
export const CRITICAL_HUE = '#ff5a6e';

export const nodeId = (owner: string, doc: string, section?: string) => `${owner || 'global'}/${doc}${section ? `#${section}` : ''}`;

function addOwner(o: GalaxyOwner, hue: string, nodes: MemNode[], links: MemLink[], coreId: string | null) {
  const owner = o.projectId ?? '';
  const main = o.docs.find((d) => d.kind === 'main');
  const mainId = nodeId(owner, 'main');
  const rest = o.docs.filter((d) => d.kind !== 'main');
  nodes.push({
    id: mainId,
    kind: owner ? 'star' : 'core',
    owner,
    doc: 'main',
    title: o.name,
    description: owner ? 'Main memory of this project.' : 'Main memory of all projects.',
    chars: main?.chars ?? 0,
    facts: main?.facts ?? 0,
    mtime: main?.mtime,
    parent: coreId,
    hue,
    order: 0,
    siblings: 1,
  });
  if (coreId) links.push({ source: coreId, target: mainId, kind: 'galaxy', hue });

  const addSections = (d: GalaxyDoc, parent: string) => {
    d.sections.forEach((s, i) => {
      const id = nodeId(owner, d.name, s.title);
      nodes.push({ id, kind: 'section', owner, doc: d.name, section: s.title, title: s.title, chars: s.chars, facts: 0, parent, hue, order: i, siblings: d.sections.length });
      links.push({ source: parent, target: id, kind: 'section', hue });
    });
  };
  if (main) addSections(main, mainId);
  rest.forEach((d, i) => {
    const id = nodeId(owner, d.name);
    nodes.push({
      id,
      kind: d.critical ? 'critical' : d.kind === 'note' ? 'note' : 'page',
      owner,
      doc: d.name,
      title: d.title,
      description: d.description,
      chars: d.chars,
      facts: d.facts,
      mtime: d.mtime,
      parent: mainId,
      hue: d.critical ? CRITICAL_HUE : hue,
      order: i,
      siblings: rest.length,
    });
    links.push({ source: mainId, target: id, kind: 'orbit', hue: d.critical ? CRITICAL_HUE : hue });
    addSections(d, id);
  });
  for (const d of o.docs) {
    for (const to of d.links) links.push({ source: nodeId(owner, d.name), target: nodeId(owner, to), kind: 'wiki', hue });
  }
}

export function buildGraph(view: GalaxyView): { nodes: MemNode[]; links: MemLink[] } {
  const nodes: MemNode[] = [];
  const links: MemLink[] = [];
  addOwner(view.global, CORE_HUE, nodes, links, null);
  const coreId = nodeId('', 'main');
  view.projects.forEach((p, i) => addOwner(p, PROJECT_HUES[i] ?? NEUTRAL_HUE, nodes, links, coreId));
  return { nodes, links };
}
