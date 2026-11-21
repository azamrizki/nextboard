// "@" in NextCode's composer lists the files of the console's working folder and inserts
// "@relative/path ", which Claude Code (and Codex) read as a file to look at. The files come from
// where the Code view's Go to file takes them (GET /api/projects/{ref}/files, every part's list for
// a root console); a console outside every part's folder browses its folder with the machine's ls.
// Matching is Go to file's fuzzy match. Pure, so tests can read it.

import { isRoot, partRef, placementOfSession, projectParts } from '../lib/parts';
import type { Project, Session } from '../lib/types';
import { fuzzyTop } from '../studio/code/fuzzy';
import { cleanAbs, under } from '../studio/code/paths';

/** The most suggestions shown at once. */
export const MAX_FILE_OPTIONS = 50;

/** One part's file list, as the console sees it: `prefix` goes in front, only what is under `sub`. */
export interface PartFiles {
  /** The part's ref ("<project>~<part>") for GET /api/projects/{ref}/files. */
  ref: string;
  /** "admin/" for a root console (the part's entry in the root folder), else "". */
  prefix: string;
  /** The console's folder inside the part ("" = the part's folder). */
  sub: string;
}

export type FileSource =
  | { kind: 'parts'; key: string; parts: PartFiles[] }
  /** A folder that is no part's: listed level by level. */
  | { kind: 'dir'; key: string; machineId: string; cwd: string };

/** Where a console's files are listed from; null when it has no folder. */
export function fileSourceOf(s: Pick<Session, 'cwd' | 'root' | 'partId' | 'machineId'>, project: Project | undefined): FileSource | null {
  const cwd = cleanAbs(s.cwd || '');
  const place = project ? placementOfSession(s, project) : null;
  if (project && place && isRoot(place)) {
    const parts = projectParts(project).map((pt) => ({ ref: partRef(project, pt), prefix: `${pt.name}/`, sub: '' }));
    return { kind: 'parts', key: `root:${project.id}:${parts.map((p) => p.ref).join(',')}`, parts };
  }
  if (project && place && !isRoot(place)) {
    const part = projectParts(project).find((pt) => pt.id === place.partId);
    const root = part ? cleanAbs(part.path) : null;
    if (part && root !== null && cwd !== null && under(cwd, root)) {
      const sub = cwd === root ? '' : cwd.slice(root === '/' ? 1 : root.length + 1);
      const ref = partRef(project, part);
      return { kind: 'parts', key: `part:${ref}:${sub}`, parts: [{ ref, prefix: '', sub }] };
    }
  }
  if (cwd === null) return null;
  const machineId = s.machineId || 'local';
  return { kind: 'dir', key: `dir:${machineId}:${cwd}`, machineId, cwd };
}

/** A part's files (relative to the part) as the console names them: under its folder, with the prefix. */
export function consolePaths(items: readonly string[], part: Pick<PartFiles, 'prefix' | 'sub'>): string[] {
  const out: string[] = [];
  const lead = part.sub ? `${part.sub}/` : '';
  for (const rel of items) {
    if (lead && !rel.startsWith(lead)) continue;
    out.push(part.prefix + rel.slice(lead.length));
  }
  return out;
}

export interface FileOption {
  /** What goes after "@": relative to the console's folder, a folder ends with "/". */
  path: string;
  dir: boolean;
  /** Matched characters, for highlighting. */
  positions: number[];
}

/** The files that fit what follows "@", best first (Go to file's fuzzy match). */
export function matchFiles(query: string, paths: readonly string[], limit = MAX_FILE_OPTIONS): FileOption[] {
  return fuzzyTop(query, paths, limit).map(({ path, hit }) => ({ path, dir: false, positions: hit.positions }));
}

/** "src/comp" → the folder "src" and the name typed so far "comp" (browsing a folder level by level). */
export function splitDirQuery(query: string): { dir: string; name: string } {
  const cut = query.lastIndexOf('/');
  return cut < 0 ? { dir: '', name: query } : { dir: query.slice(0, cut), name: query.slice(cut + 1) };
}

/** The absolute folder to list for a typed folder under cwd; null when it would leave cwd. */
export function dirToList(cwd: string, dir: string): string | null {
  if (!dir) return cwd;
  const abs = cleanAbs(`${cwd}/${dir}`);
  return abs !== null && under(abs, cwd) ? abs : null;
}

/** One folder's entries that fit the name typed, folders first: their paths from cwd. */
export function matchEntries(entries: readonly { name: string; dir: boolean }[], dir: string, name: string, limit = MAX_FILE_OPTIONS): FileOption[] {
  const lead = dir ? `${dir}/` : '';
  const shown = entries.filter((e) => name.startsWith('.') || !e.name.startsWith('.'));
  const hits = fuzzyTop(name, shown.map((e) => e.name), shown.length);
  const byName = new Map(shown.map((e) => [e.name, e]));
  const opts = hits.map(({ path, hit }) => {
    const e = byName.get(path)!;
    return { path: `${lead}${e.name}${e.dir ? '/' : ''}`, dir: e.dir, positions: hit.positions.map((p) => p + lead.length) };
  });
  // an empty name: folders first, then files, each by name
  if (!name) opts.sort((a, b) => Number(b.dir) - Number(a.dir) || a.path.localeCompare(b.path));
  return opts.slice(0, limit);
}

/** The "@word" the caret is in: `start` is the "@"; null when the caret is in no such word. */
export function fileQueryAt(text: string, caret: number): { start: number; query: string } | null {
  const before = text.slice(0, caret);
  const start = before.lastIndexOf('@');
  if (start < 0) return null;
  if (start > 0 && !/\s/.test(before[start - 1])) return null;
  const query = before.slice(start + 1);
  return /\s/.test(query) ? null : { start, query };
}

/**
 * The text with the "@word" being typed replaced by the path picked: a file ends with a space, a
 * folder does not, so its files can be listed next. The word's rest after the caret goes too.
 */
export function insertFile(text: string, caret: number, start: number, opt: Pick<FileOption, 'path' | 'dir'>): { text: string; caret: number } {
  let end = caret;
  while (end < text.length && !/\s/.test(text[end])) end++;
  const token = `@${opt.path}${opt.dir ? '' : ' '}`;
  const rest = text.slice(end);
  return { text: text.slice(0, start) + token + (opt.dir ? rest : rest.replace(/^\s/, '')), caret: start + token.length };
}
