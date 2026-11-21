// What is attached to the NextCode message being written (DESIGN.md §3.13): pasted, dropped or
// picked files become chips in the composer, each saved on This PC as it comes (POST
// /api/sessions/{id}/attach, 20 MB at most, as the command bar's). They stay per console while this
// page is open, like the draft, so they follow the chat between the board, a Code view tab and a
// phone. On send their paths go on the message's last line (attach.ts).

import { useSyncExternalStore, type RefObject } from 'react';
import { api } from '../api';
import { loadImageSize, sendPasted } from '../board/files';
import { isImagePath } from '../board/kinds';
import { Icon } from '../components/Icon';
import { formatBytes } from '../git/common';
import type { AttachedPath } from './attach';
import '../styles/orch.css';

export interface ChatAttachment {
  key: number;
  name: string;
  /** Preview of a picture (the pasted bytes). */
  url?: string;
  size?: number;
  w?: number;
  h?: number;
  /** Where it is on This PC. Absent while it is on its way. */
  path?: string;
}

// ---- per console, while this page is open ----

const lists = new Map<string, ChatAttachment[]>();
const blobs = new Map<number, string>();
const listeners = new Set<() => void>();
const NONE: ChatAttachment[] = [];
let nextKey = 1;

const emit = () => listeners.forEach((l) => l());
const subscribe = (fn: () => void) => {
  listeners.add(fn);
  return () => listeners.delete(fn);
};

function set(sessionId: string, fn: (list: ChatAttachment[]) => ChatAttachment[]) {
  const next = fn(lists.get(sessionId) ?? NONE);
  if (next.length) lists.set(sessionId, next);
  else lists.delete(sessionId);
  emit();
}

const patch = (sessionId: string, key: number, p: Partial<ChatAttachment>) => set(sessionId, (list) => list.map((a) => (a.key === key ? { ...a, ...p } : a)));

function freeBlob(key: number) {
  const url = blobs.get(key);
  if (url) URL.revokeObjectURL(url);
  blobs.delete(key);
}

function remove(sessionId: string, key: number) {
  freeBlob(key);
  set(sessionId, (list) => list.filter((a) => a.key !== key));
}

function clear(sessionId: string) {
  for (const a of lists.get(sessionId) ?? []) freeBlob(a.key);
  set(sessionId, () => NONE);
}

function add(sessionId: string, files: File[]) {
  const fresh = files.map((file) => {
    const key = nextKey++;
    const url = file.type.startsWith('image/') ? URL.createObjectURL(file) : undefined;
    if (url) {
      blobs.set(key, url);
      loadImageSize(url).then(
        ({ w, h }) => patch(sessionId, key, { w, h }),
        () => patch(sessionId, key, { url: undefined }),
      );
    }
    return { key, file, item: { key, name: file.name, url, size: file.size } satisfies ChatAttachment };
  });
  set(sessionId, (list) => [...list, ...fresh.map((f) => f.item)]);
  void (async () => {
    // one at a time, in order; what fails is said (sendPasted) and its chip goes
    for (const { key, file } of fresh) {
      const [st] = await sendPasted([file], (f) => api.attachToSession(sessionId, f));
      if (!(lists.get(sessionId) ?? NONE).some((a) => a.key === key)) continue; // removed meanwhile
      if (st) patch(sessionId, key, { path: st.path, name: st.name, size: st.size });
      else remove(sessionId, key);
    }
  })();
}

/** The files attached to one console's next message. */
export function useChatAttachments(sessionId: string) {
  const items = useSyncExternalStore(subscribe, () => lists.get(sessionId) ?? NONE);
  const paths = items.flatMap((a) => (a.path ? [a.path] : []));
  return {
    items,
    add: (files: File[]) => files.length > 0 && add(sessionId, files),
    remove: (key: number) => remove(sessionId, key),
    clear: () => clear(sessionId),
    /** Saved on This PC, in order. */
    paths,
    /** A file still on its way: Send waits for it. */
    busy: items.some((a) => !a.path),
  };
}

export type ChatAttachments = ReturnType<typeof useChatAttachments>;

function chipMeta(a: ChatAttachment): string {
  if (!a.path) return 'Attaching…';
  if (a.w && a.h) return `${a.w}×${a.h}`;
  return a.size !== undefined ? formatBytes(a.size) : '';
}

/** The chips at the top of the composer. */
export function ComposerChips({ att, inputRef }: { att: ChatAttachments; inputRef: RefObject<HTMLTextAreaElement | null> }) {
  if (!att.items.length) return null;
  return (
    <ul
      className="attach-chips nc-attach"
      aria-label="Attached files"
      onMouseDown={(e) => {
        // chips and text read as one box, so the room next to a chip belongs to the text
        if (e.target !== e.currentTarget) return;
        e.preventDefault();
        inputRef.current?.focus();
      }}
    >
      {att.items.map((a) => (
        <li key={a.key} className="attach-chip" title={a.path ?? a.name}>
          {a.url ? (
            <a className="attach-thumb" href={a.url} target="_blank" rel="noreferrer" aria-label={`Open ${a.name}`}>
              <img src={a.url} alt="" />
            </a>
          ) : (
            <span className="attach-thumb">
              <Icon name="file" />
            </span>
          )}
          <span className="attach-name truncate">{a.name}</span>
          {/* the same place: the size until the pointer comes to remove it */}
          <span className="attach-end">
            <span className="attach-meta tabular" role={a.path ? undefined : 'status'}>
              {chipMeta(a)}
            </span>
            <button type="button" className="tchip-x" aria-label={`Remove ${a.name}`} title="Remove" onClick={() => att.remove(a.key)}>
              <Icon name="close" />
            </button>
          </span>
        </li>
      ))}
    </ul>
  );
}

/** The files of a sent message, under its text: names only (the hub keeps no viewable copy of a console's files). */
export function SentFiles({ files }: { files: AttachedPath[] }) {
  return (
    <ul className="nc-sent-files" aria-label="Attached files">
      {files.map((f) => (
        <li key={f.path} className="attach-chip" title={f.path}>
          <span className="attach-thumb">
            <Icon name={isImagePath(f.name) ? 'image' : 'file'} />
          </span>
          <span className="attach-name truncate">{f.name}</span>
        </li>
      ))}
    </ul>
  );
}
