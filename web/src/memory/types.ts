// Memory of a project and of all projects (docs/API-orchestration.md §12).

export interface GalaxySection {
  title: string;
  chars: number;
}

export interface GalaxyDoc {
  name: string;
  kind: 'main' | 'sub' | 'note';
  title: string;
  description?: string;
  critical?: boolean;
  chars: number;
  facts: number;
  mtime?: string;
  sections: GalaxySection[];
  links: string[];
}

export interface GalaxyOwner {
  projectId?: string;
  name: string;
  docs: GalaxyDoc[];
}

export interface GalaxyView {
  global: GalaxyOwner;
  projects: GalaxyOwner[];
}

export interface MemoryDocument {
  name: string;
  kind: 'main' | 'sub' | 'note';
  title: string;
  description?: string;
  critical?: boolean;
  content: string;
  mtime: string;
}

export interface MemoryHit {
  projectId?: string;
  project?: string;
  doc: string;
  title: string;
  section?: string;
  text: string;
  critical?: boolean;
  global?: boolean;
  score: number;
}

export interface InitSub {
  slug: string;
  title: string;
  description: string;
  content: string;
}

export interface InitResult {
  facts: { kind: string; text: string }[];
  subs: InitSub[];
  critical: string[];
}

/** A document, wherever it is: owner '' is the memory of all projects. */
export interface DocRef {
  owner: string;
  name: string;
}
