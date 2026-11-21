// The files the composer's "@" list offers (fileMentions.ts): the console's parts' file lists, read
// once the list opens and kept briefly, or, for a folder that is no part's, the folder typed so
// far. No new hub route: the Code view's file list and the machine's ls.

import { useDeferredValue, useEffect, useMemo, useState } from 'react';
import { api, errorMessage } from '../api';
import type { DirEntry, Session } from '../lib/types';
import { useProject } from '../store';
import { filesApi, type FileListing } from '../studio/api';
import { consolePaths, dirToList, fileSourceOf, matchEntries, matchFiles, splitDirQuery, type FileOption } from './fileMentions';

const KEEP_MS = 30_000;
const partLists = new Map<string, { at: number; list: Promise<FileListing> }>();
const dirLists = new Map<string, { at: number; list: Promise<DirEntry[]> }>();

function kept<T>(cache: Map<string, { at: number; list: Promise<T> }>, key: string, load: () => Promise<T>): Promise<T> {
  const had = cache.get(key);
  if (had && Date.now() - had.at < KEEP_MS) return had.list;
  const list = load();
  cache.set(key, { at: Date.now(), list });
  // a failure is not kept: the next "@" asks again
  list.catch(() => cache.get(key)?.list === list && cache.delete(key));
  return list;
}

export interface FileOptions {
  options: FileOption[];
  /** The list is being read. */
  loading: boolean;
  /** Why some files may be missing ("cut short", a part that did not answer); '' when all is there. */
  note: string;
  /** The console has a folder to list. */
  available: boolean;
}

/** The "@" suggestions for `query` (null: the list is closed, nothing is read). */
export function useFileOptions(session: Session | undefined, query: string | null): FileOptions {
  const project = useProject(session?.projectId);
  // a console on a server works there, its CLI on This PC: the server's folder is where its files are
  const cwd = session?.remoteCwd || session?.cwd || '';
  const root = session?.root;
  const partId = session?.partId;
  const machineId = session?.remoteMachineId || session?.machineId || '';
  const fresh = session ? fileSourceOf({ cwd, root, partId, machineId }, project) : null;
  // the same source while its key is: a project event does not read the lists again
  const source = useMemo(() => fresh, [fresh?.key]); // eslint-disable-line react-hooks/exhaustive-deps
  const open = query !== null && !!source;
  const deferred = useDeferredValue(query);

  // a project's files: every part the console reaches, read once per source
  const [paths, setPaths] = useState<{ key: string; list: string[]; note: string } | null>(null);
  useEffect(() => {
    if (!open || source?.kind !== 'parts') return;
    let stop = false;
    const multi = source.parts.length > 1;
    void Promise.all(
      source.parts.map((p) =>
        kept(partLists, p.ref, () => filesApi.list(p.ref)).then(
          (l) => ({ list: consolePaths(l.items ?? [], p), note: l.truncated ? `${multi ? p.prefix.slice(0, -1) : 'The list'} is cut short: it is very large.` : '' }),
          (err) => ({ list: [] as string[], note: `${multi ? `${p.prefix.slice(0, -1)}: ` : ''}${errorMessage(err)}` }),
        ),
      ),
    ).then((rs) => {
      if (!stop) setPaths({ key: source.key, list: rs.flatMap((r) => r.list), note: rs.map((r) => r.note).filter(Boolean).join(' ') });
    });
    return () => {
      stop = true;
    };
  }, [open, source]);

  // a folder that is no part's: the folder typed so far
  const dirTyped = deferred !== null && source?.kind === 'dir' ? splitDirQuery(deferred).dir : null;
  const [entries, setEntries] = useState<{ key: string; list: DirEntry[]; note: string } | null>(null);
  useEffect(() => {
    if (!open || source?.kind !== 'dir' || dirTyped === null) return;
    const key = `${source.key}|${dirTyped}`;
    const abs = dirToList(source.cwd, dirTyped);
    if (abs === null) {
      setEntries({ key, list: [], note: 'Only files inside the console’s folder are listed.' });
      return;
    }
    let stop = false;
    void kept(dirLists, `${source.machineId}|${abs}`, () => api.listDir(source.machineId, abs).then((l) => l.entries ?? [])).then(
      (list) => !stop && setEntries({ key, list, note: '' }),
      (err) => !stop && setEntries({ key, list: [], note: errorMessage(err) }),
    );
    return () => {
      stop = true;
    };
  }, [open, source, dirTyped]);

  return useMemo<FileOptions>(() => {
    if (deferred === null || !source) return { options: [], loading: false, note: '', available: !!source };
    if (source.kind === 'parts') {
      if (paths?.key !== source.key) return { options: [], loading: true, note: '', available: true };
      return { options: matchFiles(deferred, paths.list), loading: false, note: paths.note, available: true };
    }
    const { dir, name } = splitDirQuery(deferred);
    if (entries?.key !== `${source.key}|${dir}`) return { options: [], loading: true, note: '', available: true };
    return { options: matchEntries(entries.list, dir, name), loading: false, note: entries.note, available: true };
  }, [deferred, source, paths, entries]);
}
