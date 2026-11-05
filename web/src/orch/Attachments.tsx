// Files pasted into an order (docs/API-studio.md §7.9): a chip in the box and a picture in the
// thread. The agent needs the path, so it still travels in the order's text, but nobody has to read
// it: the box adds it on send and the thread takes it out again.

import { useEffect, useRef, useState, type RefObject } from 'react';
import { api, projectAttachUrl } from '../api';
import { loadImageSize, sendPasted } from '../board/files';
import { isImagePath } from '../board/kinds';
import { Icon } from '../components/Icon';
import { formatBytes } from '../git/common';

export interface Attachment {
  key: number;
  name: string;
  /** Preview of a picture: the pasted bytes, or the hub's copy for an earlier order. */
  url?: string;
  size?: number;
  w?: number;
  h?: number;
  /** Where it is on This PC. Absent while it is on its way. */
  path?: string;
}

export interface AttachedFile {
  path: string;
  project: string;
  name: string;
}

// A whole word of the text, so a sentence that talks about such a folder stays a sentence.
const ATTACHED = /(^|\s)(\/\S*\/attachments\/project-([\w-]+)\/([^\s/]+))(?=\s|$)/g;

/** An order's text without the paths of its attachments, and those attachments. */
export function splitAttached(text: string): { text: string; files: AttachedFile[] } {
  const files: AttachedFile[] = [];
  const rest = text.replace(ATTACHED, (_all, _lead: string, path: string, project: string, name: string) => {
    files.push({ path, project, name });
    return '';
  });
  return files.length ? { text: rest.trim(), files } : { text, files };
}

let nextKey = 1;

/** What is attached to the order being written. It lives next to the text, in whoever sends it. */
export function useAttachments(projectId: string) {
  const [items, setItems] = useState<Attachment[]>([]);
  const blobs = useRef(new Map<number, string>());

  const patch = (key: number, set: Partial<Attachment>) => setItems((list) => list.map((a) => (a.key === key ? { ...a, ...set } : a)));
  const freeBlob = (key: number) => {
    const url = blobs.current.get(key);
    if (url) URL.revokeObjectURL(url);
    blobs.current.delete(key);
  };
  const remove = (key: number) => {
    freeBlob(key);
    setItems((list) => list.filter((a) => a.key !== key));
  };
  const clear = () => {
    for (const key of blobs.current.keys()) freeBlob(key);
    setItems([]);
  };
  const measure = (key: number, url: string) => {
    loadImageSize(url).then(
      ({ w, h }) => patch(key, { w, h }),
      () => patch(key, { url: undefined }),
    );
  };

  useEffect(() => clear, [projectId]);

  const add = (files: File[]) => {
    const fresh = files.map((file) => {
      const key = nextKey++;
      const url = file.type.startsWith('image/') ? URL.createObjectURL(file) : undefined;
      if (url) {
        blobs.current.set(key, url);
        measure(key, url);
      }
      return { key, file, item: { key, name: file.name, url, size: file.size } satisfies Attachment };
    });
    setItems((list) => [...list, ...fresh.map((f) => f.item)]);
    void (async () => {
      for (const { key, file } of fresh) {
        const [st] = await sendPasted([file], (f) => api.attachToProject(projectId, f));
        if (st) patch(key, { path: st.path, name: st.name, size: st.size });
        else remove(key);
      }
    })();
  };

  /** "Edit and send again": the attachments of an earlier order come back as chips. */
  const take = (text: string): string => {
    const { text: rest, files } = splitAttached(text);
    const known = new Set(items.map((a) => a.path));
    const fresh = files
      .filter((f) => !known.has(f.path))
      .map((f) => {
        const key = nextKey++;
        const url = isImagePath(f.name) ? projectAttachUrl(f.project, f.name) : undefined;
        if (url) measure(key, url);
        return { key, name: f.name, url, path: f.path } satisfies Attachment;
      });
    if (fresh.length) setItems((list) => [...list, ...fresh]);
    return rest;
  };

  const paths = items.flatMap((a) => (a.path ? [a.path] : []));
  const busy = items.some((a) => !a.path);
  return {
    items,
    projectId,
    add,
    remove,
    clear,
    take,
    /** The text the agents get: what was typed, then where the files are. */
    compose: (text: string) => [text, ...paths].filter(Boolean).join(' '),
    /** An order may be only a file. One still on its way would be left out, so it waits. */
    canSend: (text: string) => !busy && (!!text.trim() || paths.length > 0),
  };
}

export type Attachments = ReturnType<typeof useAttachments>;

function chipMeta(a: Attachment): string {
  if (!a.path) return 'Attaching…';
  if (a.w && a.h) return `${a.w}×${a.h}`;
  return a.size !== undefined ? formatBytes(a.size) : '';
}

/** The chips at the top of the order's box. */
export function AttachChips({ att, inputRef }: { att: Attachments; inputRef: RefObject<HTMLTextAreaElement | null> }) {
  if (!att.items.length) return null;
  return (
    <ul
      className="attach-chips"
      aria-label="Attached files"
      onMouseDown={(e) => {
        // chips and text read as one box, so the room next to a chip belongs to the text
        if (e.target !== e.currentTarget) return;
        e.preventDefault();
        inputRef.current?.focus();
      }}
    >
      {att.items.map((a) => {
        const picture = a.url ? <img src={a.url} alt="" /> : <Icon name="file" />;
        return (
          <li key={a.key} className="attach-chip" title={a.name}>
            {a.url && a.path ? (
              <a className="attach-thumb" href={projectAttachUrl(att.projectId, a.name)} target="_blank" rel="noreferrer" aria-label={`Open ${a.name}`}>
                {picture}
              </a>
            ) : (
              <span className="attach-thumb">{picture}</span>
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
        );
      })}
    </ul>
  );
}

function AttachedView({ file }: { file: AttachedFile }) {
  // kept a week on This PC, so an old thread outlives its pictures
  const [broken, setBroken] = useState(false);
  const url = projectAttachUrl(file.project, file.name);
  if (isImagePath(file.name) && !broken) {
    return (
      <a className="msg-attached-img" href={url} target="_blank" rel="noreferrer" title={file.name}>
        <img src={url} alt={file.name} loading="lazy" onError={() => setBroken(true)} />
      </a>
    );
  }
  return (
    <a className="attach-chip msg-attached-file" href={url} target="_blank" rel="noreferrer" title={file.path}>
      <span className="attach-thumb">
        <Icon name="file" />
      </span>
      <span className="attach-name truncate">{file.name}</span>
    </a>
  );
}

/** The attachments of an order in the thread, above its text. */
export function AttachedFiles({ files }: { files: AttachedFile[] }) {
  return (
    <div className="msg-attached">
      {files.map((f) => (
        <AttachedView key={f.path} file={f} />
      ))}
    </div>
  );
}
