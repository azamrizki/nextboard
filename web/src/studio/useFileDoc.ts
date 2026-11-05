// One open file in a studio: load (≤ 800 KB, text only), keep the BOM and line endings,
// track edits against the saved text, save with ifMtime (conflict → Reload / Keep mine),
// ask once per studio session before saving through a link that leaves the project,
// and keep a draft in IndexedDB until it is saved.

import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError, errorMessage, isCode } from '../api';
import { fsApi, type Confirm } from './api';
import { drafts, type Draft } from './drafts';

export const MAX_OPEN = 800 * 1024;

export interface FileDoc {
  status: 'loading' | 'ready' | 'error';
  error: string;
  /** Text as loaded from disk (without BOM). */
  saved: string;
  /** Current text (without BOM). */
  text: string;
  mtime: string;
  size: number;
  bom: boolean;
  crlf: boolean;
  readOnly: string;
  conflict: boolean;
  /** A draft newer than the file exists (offer to restore). */
  recovered: Draft | null;
  saving: boolean;
  /** Save was refused: real path outside the project (ask, then allowOutside). */
  outside: boolean;
}

const EMPTY: FileDoc = {
  status: 'loading',
  error: '',
  saved: '',
  text: '',
  mtime: '',
  size: 0,
  bom: false,
  crlf: false,
  readOnly: '',
  conflict: false,
  recovered: null,
  saving: false,
  outside: false,
};

/** Session-wide "save outside the project" answers per machine (cleared on reload). */
const allowOutside = new Set<string>();

export function useFileDoc(machineId: string, path: string | null, projectId: string, run?: <T>(fn: (c: Confirm) => Promise<T>) => Promise<T | null>) {
  const [doc, setDoc] = useState<FileDoc>(EMPTY);
  const ref = useRef(doc);
  ref.current = doc;
  const [rev, setRev] = useState(0);

  const load = useCallback(async () => {
    if (!path) return;
    setDoc({ ...EMPTY });
    try {
      const st = await api.fsStat(machineId, path);
      if (st.size > MAX_OPEN) {
        setDoc({ ...EMPTY, status: 'ready', size: st.size, mtime: st.mtime, readOnly: `This file is ${Math.round(st.size / 1024)} KB; the studio opens files up to 800 KB.` });
        return;
      }
      const r = await api.fsRead(machineId, path, MAX_OPEN + 1);
      if (r.binary) {
        setDoc({ ...EMPTY, status: 'ready', size: r.size, mtime: r.mtime, readOnly: 'Binary or non-UTF-8 file: shown read-only.' });
        return;
      }
      const bom = r.content.startsWith('﻿');
      const text = bom ? r.content.slice(1) : r.content;
      const d = await drafts.get(machineId, path);
      const recovered = d && d.content !== text ? d : null;
      if (d && !recovered) void drafts.remove(machineId, path);
      setDoc({ ...EMPTY, status: 'ready', saved: text, text, mtime: r.mtime, size: r.size, bom, crlf: text.includes('\r\n'), recovered });
      setRev((n) => n + 1);
    } catch (err) {
      setDoc({ ...EMPTY, status: 'error', error: errorMessage(err) });
    }
  }, [machineId, path]);

  useEffect(() => {
    void load();
  }, [load]);

  // draft while dirty (debounced)
  useEffect(() => {
    if (!path || doc.status !== 'ready' || doc.readOnly || doc.text === doc.saved) return;
    const t = window.setTimeout(() => void drafts.put(machineId, path, { content: doc.text, baseMtime: doc.mtime, at: Date.now() }), 500);
    return () => window.clearTimeout(t);
  }, [machineId, path, doc.text, doc.saved, doc.status, doc.readOnly, doc.mtime]);

  const setText = useCallback((text: string) => setDoc((d) => (d.text === text ? d : { ...d, text })), []);

  const save = useCallback(
    async (opts: { force?: boolean; outside?: boolean } = {}): Promise<boolean> => {
      const d = ref.current;
      if (!path || d.status !== 'ready' || d.readOnly) return false;
      setDoc((x) => ({ ...x, saving: true }));
      try {
        let ifMtime = d.mtime || undefined;
        if (opts.force) ifMtime = (await api.fsStat(machineId, path)).mtime || undefined;
        const out = opts.outside || allowOutside.has(machineId);
        const body = { path, content: (d.bom ? '﻿' : '') + d.text, ifMtime, projectId, allowOutside: out || undefined };
        const write = (c: Confirm) => fsApi.write(machineId, { ...body, ...c });
        const st = run ? await run(write) : await write({});
        if (!st) {
          setDoc((x) => ({ ...x, saving: false }));
          return false;
        }
        if (opts.outside) allowOutside.add(machineId);
        void drafts.remove(machineId, path);
        setDoc((x) => ({ ...x, saving: false, saved: d.text, mtime: st.mtime, size: st.size, conflict: false, outside: false, recovered: null }));
        return true;
      } catch (err) {
        if (isCode(err, 'mtime_mismatch') || (err instanceof ApiError && err.status === 409)) {
          setDoc((x) => ({ ...x, saving: false, conflict: true }));
        } else if (isCode(err, 'outside_project')) {
          setDoc((x) => ({ ...x, saving: false, outside: true }));
        } else if (isCode(err, 'body_too_large') || (err instanceof ApiError && err.status === 413)) {
          setDoc((x) => ({ ...x, saving: false, error: 'The file is too large to save through NextBoard.' }));
        } else if (isCode(err, 'no_sftp')) {
          setDoc((x) => ({ ...x, saving: false, readOnly: 'This machine has no SFTP, so files can only be read.' }));
        } else {
          setDoc((x) => ({ ...x, saving: false, error: errorMessage(err) }));
        }
        return false;
      }
    },
    [machineId, path, projectId, run],
  );

  const keepOutsideReadOnly = useCallback(() => setDoc((x) => ({ ...x, outside: false, readOnly: 'Real path is outside the project: read-only for this session.' })), []);
  const restoreDraft = useCallback(() => {
    setDoc((x) => (x.recovered ? { ...x, text: x.recovered.content, recovered: null } : x));
    setRev((n) => n + 1);
  }, []);
  const dropDraft = useCallback(() => {
    if (path) void drafts.remove(machineId, path);
    setDoc((x) => ({ ...x, recovered: null }));
  }, [machineId, path]);
  const clearError = useCallback(() => setDoc((x) => ({ ...x, error: '' })), []);

  /** The text the editor should show was replaced (load, restore): remount/refill. */
  return { doc, rev, setText, save, reload: load, keepOutsideReadOnly, restoreDraft, dropDraft, clearError, dirty: doc.status === 'ready' && doc.text !== doc.saved };
}
