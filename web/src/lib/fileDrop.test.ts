// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { dragHasFiles, droppedFiles, FILE_DROP_ATTR, watchFileDrop } from './fileDrop';

vi.mock('../components/Toast', () => ({ toast: { error: vi.fn(), info: vi.fn() } }));

/** A drag carrying these files (jsdom has no DataTransfer): what a browser hands over. */
function transfer(files: File[], folders: string[] = []) {
  const items = [
    ...files.map((f) => ({ kind: 'file', getAsFile: () => f, webkitGetAsEntry: () => ({ isDirectory: false }) })),
    ...folders.map((name) => ({ kind: 'file', getAsFile: () => new File([], name), webkitGetAsEntry: () => ({ isDirectory: true }) })),
  ];
  return { types: ['Files'], items, files, dropEffect: 'none' } as unknown as DataTransfer;
}

function fire(el: Element, type: string, dt: DataTransfer | null, init: { relatedTarget?: Element | null } = {}) {
  const e = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(e, 'dataTransfer', { value: dt });
  Object.defineProperty(e, 'relatedTarget', { value: init.relatedTarget ?? null });
  el.dispatchEvent(e);
  return e;
}

afterEach(() => {
  document.body.innerHTML = '';
});

describe('file drops', () => {
  const file = new File(['hi'], 'notes.txt', { type: 'text/plain' });

  it('tells files from other drags', () => {
    expect(dragHasFiles(transfer([file]))).toBe(true);
    expect(dragHasFiles({ types: ['application/x-nextboard-tab'] } as unknown as DataTransfer)).toBe(false);
    expect(dragHasFiles({ types: ['text/plain'] } as unknown as DataTransfer)).toBe(false);
    expect(dragHasFiles(null)).toBe(false);
  });

  it('leaves folders out of a drop', () => {
    expect(droppedFiles(transfer([file], ['src'])).map((f) => f.name)).toEqual(['notes.txt']);
  });

  it('takes a drop before what is inside, and keeps it from the board behind', () => {
    const board = document.createElement('div');
    const zone = document.createElement('div');
    const inner = document.createElement('div');
    zone.append(inner);
    board.append(zone);
    document.body.append(board);
    const boardDrop = vi.fn();
    const innerDrop = vi.fn();
    board.addEventListener('drop', boardDrop);
    inner.addEventListener('drop', innerDrop);
    const got = vi.fn();
    const stop = watchFileDrop(zone, got, { label: 'Drop to attach' });
    expect(zone.getAttribute(FILE_DROP_ATTR)).toBe('Drop to attach');

    const over = fire(inner, 'dragover', transfer([file]));
    expect(over.defaultPrevented).toBe(true);
    expect(zone.classList.contains('is-file-over')).toBe(true);

    const drop = fire(inner, 'drop', transfer([file]));
    expect(drop.defaultPrevented).toBe(true);
    expect(got).toHaveBeenCalledOnce();
    expect(got.mock.calls[0][0].map((f: File) => f.name)).toEqual(['notes.txt']);
    expect(innerDrop).not.toHaveBeenCalled();
    expect(boardDrop).not.toHaveBeenCalled();
    expect(zone.classList.contains('is-file-over')).toBe(false);

    stop();
    expect(zone.hasAttribute(FILE_DROP_ATTR)).toBe(false);
    fire(inner, 'drop', transfer([file]));
    expect(got).toHaveBeenCalledOnce();
    expect(boardDrop).toHaveBeenCalledOnce();
  });

  it('lets other drags and a refused drop pass through', () => {
    const board = document.createElement('div');
    const zone = document.createElement('div');
    board.append(zone);
    document.body.append(board);
    const boardDrop = vi.fn();
    board.addEventListener('drop', boardDrop);
    const got = vi.fn();
    let open = true;
    watchFileDrop(zone, got, { accept: () => open });

    // a tab or a tree row being moved is not a file
    const tab = fire(zone, 'drop', { types: ['application/x-nextboard-tab'], items: [], files: [] } as unknown as DataTransfer);
    expect(tab.defaultPrevented).toBe(false);
    open = false;
    const refused = fire(zone, 'drop', transfer([file]));
    expect(refused.defaultPrevented).toBe(false);
    expect(got).not.toHaveBeenCalled();
    expect(boardDrop).toHaveBeenCalledTimes(2);
  });

  it('drops its outline when the drag leaves it, not when it moves inside', () => {
    const zone = document.createElement('div');
    const a = document.createElement('span');
    const b = document.createElement('span');
    zone.append(a, b);
    const outside = document.createElement('div');
    document.body.append(zone, outside);
    watchFileDrop(zone, vi.fn());
    fire(a, 'dragover', transfer([file]));
    fire(a, 'dragleave', transfer([file]), { relatedTarget: b });
    expect(zone.classList.contains('is-file-over')).toBe(true);
    fire(b, 'dragleave', transfer([file]), { relatedTarget: outside });
    expect(zone.classList.contains('is-file-over')).toBe(false);
  });
});
