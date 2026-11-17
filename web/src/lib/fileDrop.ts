// Files dropped from the computer. Every place that takes a pasted file takes a dropped one the
// same way (docs/DESIGN.md §3.13 "Files from the computer"): a console gets the file on its machine and
// its path typed, the lead's box attaches it, an editor saves it next to the open file and writes
// its name, the file tree saves it in the folder under the pointer. Anywhere else on a board the
// board's own drop makes cards of them.

import { useCallback, useRef, type RefCallback } from 'react';
import { toast } from '../components/Toast';

/** Whether a drag carries files from the computer (not a tab, a tree row, a card or text). */
export function dragHasFiles(dt: DataTransfer | null | undefined): boolean {
  return !!dt && Array.from(dt.types ?? []).includes('Files');
}

/** The files of a drop, in order. A folder is said and left out: a browser hands over no folder's files. */
export function droppedFiles(dt: DataTransfer | null | undefined): File[] {
  if (!dt) return [];
  const items = Array.from(dt.items ?? []);
  if (!items.length) return Array.from(dt.files ?? []);
  const files: File[] = [];
  for (const item of items) {
    if (item.kind !== 'file') continue;
    const file = item.getAsFile();
    if (item.webkitGetAsEntry?.()?.isDirectory) toast.error(`${file?.name ?? 'A folder'} is a folder. Drop the files inside it instead.`);
    else if (file) files.push(file);
  }
  return files;
}

/** The attribute a drop place carries (its value: what a drop there does, shown while files are over it). */
export const FILE_DROP_ATTR = 'data-file-drop';

const HIDE_AFTER_MS = 300;

/**
 * Makes el a place files can be dropped on: while files are dragged over it, it gets the class
 * is-file-over and shows label; a drop calls onFiles. It listens before anything inside it (an
 * editor or a terminal has its own drop handling) and keeps the drag from the board behind it.
 * While accept() says no, the drag passes through as if el did not take files. Returns the cleanup.
 */
export function watchFileDrop(el: HTMLElement, onFiles: (files: File[], e: DragEvent) => void, opts: { label?: string; accept?: () => boolean } = {}): () => void {
  const accept = opts.accept ?? (() => true);
  let timer = 0;
  const show = (on: boolean) => {
    window.clearTimeout(timer);
    el.classList.toggle('is-file-over', on);
    // a drag that ends elsewhere (Escape, another window) sends no leave here: dragover stops
    if (on) timer = window.setTimeout(() => el.classList.remove('is-file-over'), HIDE_AFTER_MS);
  };
  const mine = (e: DragEvent) => dragHasFiles(e.dataTransfer) && accept();
  const over = (e: DragEvent) => {
    if (!mine(e)) return;
    e.preventDefault();
    e.stopPropagation();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
    show(true);
  };
  const leave = (e: DragEvent) => {
    const to = e.relatedTarget as Node | null;
    if (!to || !el.contains(to)) show(false);
  };
  const drop = (e: DragEvent) => {
    show(false);
    if (!mine(e)) return;
    e.preventDefault();
    e.stopPropagation();
    const files = droppedFiles(e.dataTransfer);
    if (files.length) onFiles(files, e);
  };
  el.setAttribute(FILE_DROP_ATTR, opts.label ?? 'Drop files here');
  el.addEventListener('dragenter', over, true);
  el.addEventListener('dragover', over, true);
  el.addEventListener('dragleave', leave, true);
  el.addEventListener('drop', drop, true);
  return () => {
    window.clearTimeout(timer);
    el.classList.remove('is-file-over');
    el.removeAttribute(FILE_DROP_ATTR);
    el.removeEventListener('dragenter', over, true);
    el.removeEventListener('dragover', over, true);
    el.removeEventListener('dragleave', leave, true);
    el.removeEventListener('drop', drop, true);
  };
}

/**
 * watchFileDrop for React: a ref for one element or several (each gets its own listeners).
 * onFiles null = not now (a busy or disabled box): the drag passes through.
 */
export function useFileDrop<T extends HTMLElement = HTMLElement>(onFiles: ((files: File[]) => void) | null | undefined, label?: string): RefCallback<T> {
  const cb = useRef(onFiles);
  cb.current = onFiles;
  return useCallback(
    (el: T | null) =>
      el
        ? watchFileDrop(el, (files) => cb.current?.(files), {
            label,
            accept: () => !!cb.current,
          })
        : undefined,
    [label],
  );
}
