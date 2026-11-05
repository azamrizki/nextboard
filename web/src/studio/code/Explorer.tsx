// File tree of the Code workbench: folders load when opened, so node_modules and vendor
// cost nothing until asked for. Git colours and letters on the rows, ignored files dimmed,
// new/rename inline in the tree, drag to move, keyboard as in VS Code. Copy, Cut and Paste
// work on files and folders; Paste also takes files from the computer.

import { useEffect, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties, type DragEvent, type KeyboardEvent } from 'react';
import { clipboardFiles } from '../../board/files';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { ContextMenu, type MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import type { TreeEntry } from '../types';
import { fileClip } from './fileClip';
import { iconSrc, useIconTheme } from './iconTheme';
import { useWs } from './useWs';
import { dirOf, extOf, nameOf, type Workspace } from './workspace';

const EXT_TONE: Record<string, string> = {};
const tone = (color: string, exts: string[]) => exts.forEach((e) => (EXT_TONE[e] = color));
tone('var(--syn-title)', ['ts', 'tsx', 'mts', 'cts', 'go', 'c', 'h', 'cc', 'cpp', 'hpp', 'swift', 'dart']);
tone('var(--syn-number)', ['js', 'jsx', 'mjs', 'cjs', 'json', 'jsonc', 'json5', 'py', 'java', 'kt', 'kts']);
tone('var(--syn-attr)', ['css', 'scss', 'less', 'html', 'htm', 'vue', 'svelte', 'xml', 'svg']);
tone('var(--syn-keyword)', ['php', 'rb', 'cs', 'lua', 'sql', 'graphql', 'proto']);
tone('var(--syn-meta)', ['rs', 'yaml', 'yml', 'toml', 'ini', 'env', 'lock']);
tone('var(--syn-string)', ['sh', 'bash', 'zsh', 'png', 'jpg', 'jpeg', 'gif', 'webp', 'ico', 'mp4', 'webm', 'mp3']);
tone('var(--c-primary-300)', ['md', 'markdown', 'mdx', 'txt', 'rst']);

export const fileColor = (name: string) => EXT_TONE[extOf(name)];

/** A file's or folder's icon: from the icon theme when one is chosen. */
export function FileIcon({ name, dir, open }: { name: string; dir?: boolean; open?: boolean }) {
  useIconTheme();
  const src = iconSrc(name, !!dir, !!open);
  if (src) return <img className="wb-icon wb-icon-img" src={src} alt="" draggable={false} />;
  return <Icon name={dir ? (open ? 'folderOpen' : 'folder') : 'file'} className="wb-icon" style={dir ? undefined : { color: fileColor(name) }} />;
}

interface Props {
  ws: Workspace;
  onAddFile: (path: string) => void;
  onAddFolder: (dir: string) => void;
  onTerminal: (dir: string) => void;
  onAsk: (path: string) => void;
}

interface Row {
  kind: 'entry' | 'input' | 'msg';
  key: string;
  depth: number;
  path: string;
  entry?: TreeEntry;
  msg?: string;
  error?: boolean;
}

function InlineInput({ ws, placeholder }: { ws: Workspace; placeholder: string }) {
  const ref = useRef<HTMLInputElement>(null);
  const committed = useRef(false);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.focus();
    // renaming selects the name without its extension, as in VS Code
    const dot = el.value.lastIndexOf('.');
    el.setSelectionRange(0, dot > 0 ? dot : el.value.length);
  }, []);
  const commit = async () => {
    if (committed.current) return;
    committed.current = true;
    if (!(await ws.commitInput(ref.current?.value ?? ''))) {
      committed.current = false;
      ref.current?.focus();
    }
  };
  return (
    <input
      ref={ref}
      className="wb-row-input mono"
      aria-label={placeholder}
      placeholder={placeholder}
      defaultValue={ws.input?.value ?? ''}
      spellCheck={false}
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === 'Enter') void commit();
        else if (e.key === 'Escape') {
          committed.current = true;
          ws.cancelInput();
        }
      }}
      onBlur={() => void commit()}
    />
  );
}

export function Explorer({ ws, onAddFile, onAddFolder, onTerminal, onAsk }: Props) {
  useWs(ws);
  const [menu, setMenu] = useState<{ x: number; y: number; path: string; entry: TreeEntry | null } | null>(null);
  const [trash, setTrash] = useState<string | null>(null);
  const [dropOn, setDropOn] = useState<string | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const clip = useSyncExternalStore(fileClip.subscribe, fileClip.get);

  const rows = useMemo(() => {
    const out: Row[] = [];
    const inputRow = (dir: string, depth: number) => {
      const inp = ws.input;
      if (inp && inp.kind !== 'rename' && inp.dir === dir) out.push({ kind: 'input', key: `input:${dir}`, depth, path: dir });
    };
    const walk = (dir: string, depth: number) => {
      inputRow(dir, depth);
      const slot = ws.items.get(dir);
      if (!slot || slot === 'loading') {
        out.push({ kind: 'msg', key: `msg:${dir}`, depth, path: dir, msg: 'Loading…' });
        return;
      }
      if (!Array.isArray(slot)) {
        out.push({ kind: 'msg', key: `msg:${dir}`, depth, path: dir, msg: slot.error, error: true });
        return;
      }
      for (const e of slot) {
        if (e.hidden && !ws.showExcluded) continue;
        const p = dir === '/' ? `/${e.name}` : `${dir}/${e.name}`;
        if (ws.input?.kind === 'rename' && ws.input.path === p) {
          out.push({ kind: 'input', key: `input:${p}`, depth, path: p, entry: e });
        } else {
          out.push({ kind: 'entry', key: p, depth, path: p, entry: e });
        }
        if (e.dir && ws.expanded.has(p)) walk(p, depth + 1);
      }
      if (ws.truncated.has(dir)) out.push({ kind: 'msg', key: `more:${dir}`, depth, path: dir, msg: 'This folder has more entries than can be listed.' });
      if (!slot.length && depth === 0 && !ws.input) out.push({ kind: 'msg', key: 'empty', depth, path: dir, msg: 'This folder is empty.' });
    };
    walk(ws.root, 0);
    return out;
    // the workspace mutates in place: its version is the dependency
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, ws.version]);

  useEffect(() => {
    if (ws.selected) listRef.current?.querySelector('.wb-row.is-selected')?.scrollIntoView({ block: 'nearest' });
  }, [ws.revealTick, ws.selected]);

  /** New items land in the selected folder, or next to the selected file. */
  const targetDir = () => {
    const sel = ws.selected;
    if (!sel || !ws.inside(sel)) return ws.root;
    return ws.items.has(sel) || rows.some((r) => r.path === sel && r.entry?.dir) ? sel : dirOf(sel);
  };

  const failed = (err: unknown) => toast.error(err instanceof Error ? err.message : String(err));

  /** The tree's path also goes on the clipboard: pasted into a console or an order, it says which file. */
  const putAside = (path: string, cut: boolean) => {
    fileClip.set({ machineId: ws.mid, path, cut });
    void navigator.clipboard?.writeText(path).catch(() => undefined);
  };

  /** From the menu the browser only hands over pictures, and only on https. */
  const pasteFromMenu = async (dir: string) => {
    if (fileClip.get()) return ws.pasteClip(dir);
    const items = await navigator.clipboard?.read?.().catch(() => null);
    const files: File[] = [];
    for (const item of items ?? []) {
      const type = item.types.find((t) => t.startsWith('image/'));
      if (type) files.push(new File([await item.getType(type)], `image.${type.slice(6).split('+')[0]}`, { type }));
    }
    if (files.length) await ws.pasteFiles(dir, files);
    else toast.info('Press Ctrl+V in the file tree to paste from your computer.');
  };

  // Keys arrive as clipboard events, on the document: their target follows the page's text
  // selection, not the focus. The inline rename box has the focus when it is its own paste.
  const target = useRef(targetDir);
  target.current = targetDir;
  useEffect(() => {
    const focused = () => document.activeElement === listRef.current;
    const onCopy = (e: ClipboardEvent) => {
      const sel = ws.selected;
      if (!focused() || !sel || !ws.inside(sel) || sel === ws.root) return;
      e.preventDefault();
      e.clipboardData?.setData('text/plain', sel);
      fileClip.set({ machineId: ws.mid, path: sel, cut: e.type === 'cut' });
    };
    const onPaste = (e: ClipboardEvent) => {
      if (!focused()) return;
      e.preventDefault();
      const dir = target.current();
      const files = clipboardFiles(e.clipboardData);
      void (files.length ? ws.pasteFiles(dir, files) : ws.pasteClip(dir)).catch(failed);
    };
    document.addEventListener('copy', onCopy);
    document.addEventListener('cut', onCopy);
    document.addEventListener('paste', onPaste);
    return () => {
      document.removeEventListener('copy', onCopy);
      document.removeEventListener('cut', onCopy);
      document.removeEventListener('paste', onPaste);
    };
  }, [ws]);

  // moved away by another tile's tree: this one still lists it
  const lastClip = useRef(clip);
  useEffect(() => {
    const last = lastClip.current;
    lastClip.current = clip;
    if (last?.cut && !clip && ws.items.has(dirOf(last.path))) void ws.loadDir(dirOf(last.path));
  }, [clip, ws]);

  const openEntry = (r: Row, pin: boolean) => {
    if (!r.entry) return;
    ws.selected = r.path;
    if (r.entry.dir) ws.toggleDir(r.path);
    // a preview leaves the keys with the tree, as in VS Code: Ctrl+C after a click means the file
    else ws.open(r.path, { pin, keepFocus: !pin });
  };

  const items = (path: string, e: TreeEntry | null): MenuEntry[] => {
    const dir = !e || e.dir ? path : dirOf(path);
    const list: MenuEntry[] = [];
    if (e && !e.dir) {
      list.push(
        { label: 'Open', icon: 'file', shortcut: 'Enter', onSelect: () => ws.open(path, { pin: true }) },
        { label: 'Open to the Side', icon: 'layout', onSelect: () => ws.open(path, { pin: true, side: true }) },
        { label: 'Compare with HEAD', icon: 'branch', disabled: !ws.gitMark(path), onSelect: () => ws.openDiff(path, 'head') },
        { label: 'Local History', icon: 'history', onSelect: () => ws.openView({ kind: 'history', path }) },
        'sep',
      );
    }
    list.push(
      { label: 'New File…', icon: 'file', onSelect: () => ws.startInput('file', dir) },
      { label: 'New Folder…', icon: 'folder', onSelect: () => ws.startInput('folder', dir) },
      { label: 'Open in Terminal', icon: 'terminal', onSelect: () => onTerminal(dir) },
      'sep',
    );
    if (e) {
      list.push(
        { label: 'Cut', icon: 'scissors', shortcut: 'Ctrl+X', onSelect: () => putAside(path, true) },
        { label: 'Copy', icon: 'copy', shortcut: 'Ctrl+C', onSelect: () => putAside(path, false) },
      );
    }
    list.push({ label: 'Paste', icon: 'clipboard', shortcut: 'Ctrl+V', onSelect: () => void pasteFromMenu(dir).catch(failed) });
    if (e) {
      list.push(
        'sep',
        { label: 'Copy Path', icon: 'link', onSelect: () => void navigator.clipboard?.writeText(path).then(() => toast.success('Path copied.')) },
        { label: 'Copy Relative Path', icon: 'link', onSelect: () => void navigator.clipboard?.writeText(ws.rel(path)).then(() => toast.success('Path copied.')) },
        { label: 'Ask an Agent About This', icon: 'sparkle', onSelect: () => onAsk(path) },
        e.dir ? { label: 'Add Folder to Board', icon: 'plus', onSelect: () => onAddFolder(path) } : { label: 'Add to Board', icon: 'plus', disabled: e.binary, onSelect: () => onAddFile(path) },
      );
      if (!e.dir) list.push({ label: 'Duplicate', icon: 'duplicate', disabled: e.binary, onSelect: () => void ws.duplicate(path).catch((err) => toast.error(String(err))) });
      list.push(
        'sep',
        { label: 'Rename…', icon: 'pencil', shortcut: 'F2', onSelect: () => ws.startInput('rename', dirOf(path), path) },
        { label: 'Move to Trash…', icon: 'trash', danger: true, shortcut: 'Del', onSelect: () => setTrash(path) },
      );
    }
    return list;
  };

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const visible = rows.filter((r) => r.kind === 'entry');
    if (!visible.length) return;
    const i = visible.findIndex((r) => r.path === ws.selected);
    const cur = visible[i];
    const select = (r: Row | undefined) => {
      if (!r) return;
      ws.selected = r.path;
      ws.revealTick++;
      ws.changed();
    };
    switch (e.key) {
      case 'ArrowDown':
        select(visible[Math.min(i + 1, visible.length - 1)]);
        break;
      case 'ArrowUp':
        select(visible[Math.max(i - 1, 0)]);
        break;
      case 'Home':
        select(visible[0]);
        break;
      case 'End':
        select(visible[visible.length - 1]);
        break;
      case 'ArrowRight':
        if (cur?.entry?.dir) {
          if (ws.expanded.has(cur.path)) select(visible[i + 1]);
          else ws.toggleDir(cur.path, true);
        }
        break;
      case 'ArrowLeft':
        if (cur?.entry?.dir && ws.expanded.has(cur.path)) ws.toggleDir(cur.path, false);
        else if (cur) select(visible.find((r) => r.path === dirOf(cur.path)));
        break;
      case 'Enter':
        if (cur) openEntry(cur, true);
        break;
      case ' ':
        if (cur) openEntry(cur, false);
        break;
      case 'F2':
        if (cur) ws.startInput('rename', dirOf(cur.path), cur.path);
        break;
      case 'Delete':
        if (cur) setTrash(cur.path);
        break;
      case 'Escape':
        if (!clip?.cut) return;
        fileClip.set(null);
        break;
      case 'F10':
        if (!e.shiftKey || !cur?.entry) return;
        {
          const box = listRef.current?.querySelector('.wb-row.is-selected')?.getBoundingClientRect();
          setMenu({ x: (box?.left ?? 40) + 24, y: box?.bottom ?? 80, path: cur.path, entry: cur.entry });
        }
        break;
      case 'ContextMenu':
        if (!cur?.entry) return;
        {
          const box = listRef.current?.querySelector('.wb-row.is-selected')?.getBoundingClientRect();
          setMenu({ x: (box?.left ?? 40) + 24, y: box?.bottom ?? 80, path: cur.path, entry: cur.entry });
        }
        break;
      default:
        return;
    }
    e.preventDefault();
  };

  const dropTo = (dir: string, e: DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDropOn(null);
    const from = e.dataTransfer.getData('application/x-nextboard-path');
    if (!from || from === dir || dirOf(from) === dir || dir.startsWith(`${from}/`)) return;
    void ws.moveTo(from, `${dir}/${nameOf(from)}`).catch((err) => toast.error(err instanceof Error ? err.message : String(err)));
  };

  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2 className="truncate" title={ws.root}>
          {ws.name}
        </h2>
        <Button size="sm" variant="ghost" iconOnly icon="file" aria-label="New file" title="New file" onClick={() => ws.startInput('file', targetDir())} />
        <Button size="sm" variant="ghost" iconOnly icon="folder" aria-label="New folder" title="New folder" onClick={() => ws.startInput('folder', targetDir())} />
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh" title="Refresh" onClick={() => ws.refreshTree()} />
        <Button size="sm" variant="ghost" iconOnly icon="chevronUp" aria-label="Collapse folders" title="Collapse folders" onClick={() => ws.collapseAll()} />
        <Button
          size="sm"
          variant={ws.showExcluded ? 'ghost' : 'secondary'}
          iconOnly
          icon="eye"
          aria-label={ws.showExcluded ? 'Hide dotfiles and build folders' : 'Show dotfiles and build folders'}
          aria-pressed={!ws.showExcluded}
          title={ws.showExcluded ? 'Hide dotfiles and build folders' : 'Show dotfiles and build folders'}
          onClick={() => {
            ws.showExcluded = !ws.showExcluded;
            ws.changed();
          }}
        />
      </header>
      <div
        ref={listRef}
        className={`wb-tree nowheel ${dropOn === ws.root ? 'is-drop' : ''}`}
        role="tree"
        aria-label="Project files"
        tabIndex={0}
        onKeyDown={onKey}
        onContextMenu={(e) => {
          if (e.target !== e.currentTarget) return;
          e.preventDefault();
          setMenu({ x: e.clientX, y: e.clientY, path: ws.root, entry: null });
        }}
        onDragOver={(e) => {
          e.preventDefault();
          setDropOn(ws.root);
        }}
        onDragLeave={(e) => {
          if (e.target === e.currentTarget) setDropOn(null);
        }}
        onDrop={(e) => dropTo(ws.root, e)}
      >
        {rows.map((r) => {
          const style = { '--depth': r.depth } as CSSProperties;
          if (r.kind === 'msg') {
            return (
              <div key={r.key} className={`wb-row-msg ${r.error ? 'is-error' : ''}`} style={style}>
                {r.msg}
              </div>
            );
          }
          if (r.kind === 'input') {
            const isDir = ws.input?.kind === 'folder' || (ws.input?.kind === 'rename' && r.entry?.dir);
            return (
              <div key={r.key} className="wb-row is-input" style={style}>
                <span className="wb-chev" aria-hidden="true" />
                <Icon name={isDir ? 'folder' : 'file'} className="wb-icon" />
                <InlineInput ws={ws} placeholder={ws.input?.kind === 'rename' ? 'New name' : ws.input?.kind === 'folder' ? 'Folder name' : 'File name (a/b.ts makes folders)'} />
              </div>
            );
          }
          const e = r.entry!;
          const open = e.dir && ws.expanded.has(r.path);
          const mark = e.dir ? undefined : ws.gitMark(r.path);
          const dirTone = e.dir ? ws.gitDirTone(r.path) : undefined;
          const dim = ws.ignored.has(r.path) || e.hidden;
          const selected = ws.selected === r.path;
          const cut = clip?.cut && clip.machineId === ws.mid && (r.path === clip.path || r.path.startsWith(`${clip.path}/`));
          return (
            <div
              key={r.key}
              role="treeitem"
              aria-level={r.depth + 1}
              aria-expanded={e.dir ? open : undefined}
              aria-selected={selected}
              className={`wb-row ${selected ? 'is-selected' : ''} ${dim ? 'is-dim' : ''} ${cut ? 'is-cut' : ''} ${mark ? `git-${mark.tone}` : ''} ${dirTone ? `git-${dirTone}` : ''} ${dropOn === r.path ? 'is-drop' : ''}`}
              style={style}
              title={r.path}
              draggable
              onDragStart={(ev) => {
                ev.dataTransfer.setData('application/x-nextboard-path', r.path);
                ev.dataTransfer.effectAllowed = 'move';
              }}
              onDragOver={(ev) => {
                if (!e.dir) return;
                ev.preventDefault();
                ev.stopPropagation();
                setDropOn(r.path);
              }}
              onDrop={(ev) => dropTo(e.dir ? r.path : dirOf(r.path), ev)}
              onClick={() => openEntry(r, false)}
              onDoubleClick={() => !e.dir && ws.open(r.path, { pin: true })}
              onContextMenu={(ev) => {
                ev.preventDefault();
                ev.stopPropagation();
                ws.selected = r.path;
                ws.changed();
                setMenu({ x: ev.clientX, y: ev.clientY, path: r.path, entry: e });
              }}
            >
              {e.dir ? <Icon name={open ? 'chevronDown' : 'chevronRight'} className="wb-chev" /> : <span className="wb-chev" aria-hidden="true" />}
              <FileIcon name={e.name} dir={e.dir} open={open} />
              <span className="wb-row-name truncate">{e.name}</span>
              {!e.dir && ws.reviewMark(r.path) && <span className="wb-rv-mark" title={`Changed by ${ws.reviewMark(r.path)!.join(', ')}. Check it in Review.`} aria-label={`Changed by ${ws.reviewMark(r.path)!.join(', ')}`} />}
              {mark && <span className="wb-row-mark">{mark.letter}</span>}
              {dirTone && <span className="wb-row-dot" aria-hidden="true" />}
            </div>
          );
        })}
      </div>
      {menu && <ContextMenu x={menu.x} y={menu.y} label="File actions" items={items(menu.path, menu.entry)} onClose={() => setMenu(null)} />}
      {trash && (
        <ConfirmDialog
          title={`Move ${nameOf(trash)} to the trash?`}
          confirmLabel="Move to trash"
          danger
          onClose={() => setTrash(null)}
          onConfirm={() => ws.trash(trash)}
        >
          <p>It moves to the project’s trash folder (.git/nextboard-trash or .nextboard-trash), so you can bring it back by hand.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
