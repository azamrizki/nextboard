// Project / global memory documents (docs/API-orchestration.md §12), cached per project
// and refetched on "memory" events.

import { useEffect, useState } from 'react';
import { orchApi } from './api';
import { is404, useOrch } from './store';
import type { MemoryBoth } from './types';

const cache = new Map<string, { rev: number; data: MemoryBoth }>();

export interface MemoryView {
  data: MemoryBoth | null;
  status: 'loading' | 'ready' | 'error' | 'unsupported';
  error: string;
  reload: () => void;
}

export function useMemoryDoc(projectId: string): MemoryView {
  const rev = useOrch((s) => s.memoryRev[projectId] ?? 0);
  const cached = cache.get(projectId);
  const [data, setData] = useState<MemoryBoth | null>(cached?.data ?? null);
  const [status, setStatus] = useState<MemoryView['status']>(cached ? 'ready' : 'loading');
  const [error, setError] = useState('');
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    const c = cache.get(projectId);
    if (c && c.rev === rev && nonce === 0) {
      setData(c.data);
      setStatus('ready');
      return;
    }
    let alive = true;
    orchApi
      .memory(projectId)
      .then((d) => {
        if (!alive) return;
        cache.set(projectId, { rev, data: d });
        setData(d);
        setStatus('ready');
      })
      .catch((err) => {
        if (!alive) return;
        setStatus(is404(err) ? 'unsupported' : 'error');
        setError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      alive = false;
    };
  }, [projectId, rev, nonce]);

  return { data, status, error, reload: () => setNonce((n) => n + 1) };
}

export function setMemoryCache(projectId: string, scope: 'project' | 'global', doc: { content: string; mtime: string }) {
  const c = cache.get(projectId);
  if (c) cache.set(projectId, { rev: c.rev, data: { ...c.data, [scope]: doc } });
}

/** Bullet facts of a memory document ("- …" / "* …" lines). */
export function factsOf(content: string): { section: string; text: string }[] {
  const out: { section: string; text: string }[] = [];
  let section = '';
  for (const line of content.split('\n')) {
    const h = /^##\s+(.+)$/.exec(line);
    if (h) {
      section = h[1].trim();
      continue;
    }
    const b = /^\s*[-*]\s+(.+)$/.exec(line);
    if (b) out.push({ section, text: b[1].trim() });
  }
  return out;
}
