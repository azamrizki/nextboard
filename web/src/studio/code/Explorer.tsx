// File tree of the Code workbench: folders load when opened, so node_modules and vendor
// cost nothing until asked for. Git colours and letters on the rows, ignored files dimmed,
// new/rename inline in the tree, drag to move, keyboard as in VS Code. Copy, Cut and Paste
// work on files and folders; Paste also takes files from the computer.

import { useEffect, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties, type DragEvent, type KeyboardEvent } from 'react';
import { clipboardFiles } from '../../board/files';
import { dragHasFiles, droppedFiles } from '../../lib/fileDrop';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { ContextMenu, type MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { useMachines } from '../../store';
import { downloadMachineFile, MAX_DOWNLOAD } from '../../lib/download';
import { fileClip, type FileClip } from './fileClip';
import { iconSrc, useIconTheme } from './iconTheme';
import { isPartRoot } from './partGroups';
import { asFileKey, cleanAbs, fileKey, isRootKey, joinKey, keyUnder, LOCAL_MACHINE, parentKey, splitKey, type FileKey } from './paths';
import { useWs } from './useWs';
import { extOf, nameOf, type ViewTreeEntry, type Workspace } from './workspace';

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

/** The badge a file type gets when no icon theme is chosen (the islands design): two letters on its tone. */
const BADGE: Record<string, string> = {};
const badge = (text: string, exts: string[]) => exts.forEach((e) => (BADGE[e] = text));
badge('TS', ['ts', 'mts', 'cts']);
badge('TX', ['tsx']);
badge('JS', ['js', 'mjs', 'cjs']);
badge('JX', ['jsx']);
badge('{}', ['json', 'jsonc', 'json5']);
badge('MD', ['md', 'markdown', 'mdx']);
badge('CS', ['css', 'scss', 'less']);
badge('<>', ['html', 'htm', 'xml', 'svg']);
badge('GO', ['go']);
badge('PY', ['py']);
badge('RS', ['rs']);
badge('PH', ['php']);
badge('RB', ['rb']);
badge('VU', ['vue']);
badge('SV', ['svelte']);
badge('JV', ['java']);
badge('KT', ['kt', 'kts']);
badge('C', ['c', 'h']);
badge('C+', ['cc', 'cpp', 'hpp']);
badge('SW', ['swift']);
badge('DT', ['dart']);
badge('YM', ['yaml', 'yml']);
badge('TM', ['toml']);
badge('SH', ['sh', 'bash', 'zsh']);
badge('SQ', ['sql']);
badge('LK', ['lock']);
badge('EN', ['env']);
badge('TX', ['txt']);
const badgeOf = (name: string) => BADGE[extOf(name)] ?? (name.startsWith('.env') ? 'EN' : '');

/** A file's or folder's icon: from the icon theme when one is chosen, else a type badge or a plain icon. */
export function FileIcon({ name, dir, open }: { name: string; dir?: boolean; open?: boolean }) {
  useIconTheme();
  const src = iconSrc(name, !!dir, !!open);
  if (src) return <img className="wb-icon wb-icon-img" src={src} alt="" draggable={false} />;
  if (dir) return <Icon name={open ? 'folderOpen' : 'folder'} className="wb-icon wb-icon-dir" />;
  const text = badgeOf(name);
  const color = fileColor(name);
  if (text) {
    return (
      <span className="wb-icon wb-fbadge" style={{ '--fb': color ?? 'var(--c-grey)' } as CSSProperties} aria-hidden="true">
        {text}
      </span>
    );
  }
  return <Icon name="file" className="wb-icon" style={{ color }} />;
}

interface Props {
  ws: Workspace;
  /** Absent: no board to add to (the Code view, DESIGN.md §3.19). */
  onAddFile?: (path: string) => void;
  onAddFolder?: (dir: string) => void;
  onTerminal: (dir: string) => void;
  onAsk: (path: string) => void;
}

interface Row {
  kind: 'entry' | 'input' | 'msg';
  key: string;
  depth: number;
  /** The row's file key (in a Root card the top rows are the parts' folders). */
  path: FileKey;
  entry?: ViewTreeEntry;
  msg?: string;
  error?: boolean;
}

/** The key of what Copy or Cut put aside (it keeps the machine and the real path). */
const clipKey = (c: FileClip): FileKey | null => (c.path.startsWith('//') ? asFileKey(c.path) : cleanAbs(c.path) ? fileKey(c.machineId || LOCAL_MACHINE, c.path) : null);

const ROOT_FIRST = 'Pick a part’s folder first: the root of the project is a view of its parts.';

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
  const machines = useMachines();
  const [menu, setMenu] = useState<{ x: number; y: number; path: FileKey; entry: ViewTreeEntry | null } | null>(null);
  const [trash, setTrash] = useState<FileKey | null>(null);
  const [dropOn, setDropOn] = useState<string | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const clip = useSyncExternalStore(fileClip.subscribe, fileClip.get);
  const clipped = clip ? clipKey(clip) : null;
  const multi = ws.paths.multi;
  const machineName = (id: string) => (id === LOCAL_MACHINE || !id ? 'This PC' : (machines.find((m) => m.id === id)?.name ?? id));

  const rows = useMemo(() => {
    const out: Row[] = [];
    const inputRow = (dir: FileKey, depth: number) => {
      const inp = ws.input;
      if (inp && inp.kind !== 'rename' && inp.dir === dir) out.push({ kind: 'input', key: `input:${dir}`, depth, path: dir });
    };
    const walk = (dir: FileKey, depth: number) => {
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
        // a Root card's top rows are the parts, with their folder's key
        const p = e.key ?? joinKey(dir, e.name);
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
    walk(ws.rootKey, 0);
    return out;
    // the workspace mutates in place: its version is the dependency
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, ws.version]);

  useEffect(() => {
    if (ws.selected) listRef.current?.querySelector('.wb-row.is-selected')?.scrollIntoView({ block: 'nearest' });
  }, [ws.revealTick, ws.selected]);

  /** New items land in the selected folder, or next to the selected file (in a Root card, nothing lands at the root itself). */
  const targetDir = (): FileKey => {
    const sel = ws.selected ? asFileKey(ws.selected) : null;
    if (!sel || isRootKey(sel) || !ws.paths.mountOf(sel)) return ws.rootKey;
    return ws.items.has(sel) || rows.some((r) => r.path === sel && r.entry?.dir) ? sel : (parentKey(sel) ?? ws.rootKey);
  };

  const failed = (err: unknown) => toast.error(err instanceof Error ? err.message : String(err));

  /** The tree's path also goes on the clipboard: pasted into a console or an order, it says which file. */
  const putAside = (path: FileKey, cut: boolean) => {
    const { machineId, abs } = splitKey(path);
    fileClip.set({ machineId, path: abs, cut });
    void navigator.clipboard?.writeText(abs).catch(() => undefined);
  };

  /** From the menu the browser only hands over pictures, and only on https. */
  const pasteFromMenu = async (dir: FileKey) => {
    if (isRootKey(dir)) {
      toast.info(ROOT_FIRST);
      return;
    }
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
      const sel = ws.selected ? asFileKey(ws.selected) : null;
      // a part's own folder stays where it is
      if (!focused() || !sel || isPartRoot(ws.paths, sel) || !ws.paths.mountOf(sel)) return;
      e.preventDefault();
      const { machineId, abs } = splitKey(sel);
      e.clipboardData?.setData('text/plain', abs);
      fileClip.set({ machineId, path: abs, cut: e.type === 'cut' });
    };
    const onPaste = (e: ClipboardEvent) => {
      if (!focused()) return;
      e.preventDefault();
      const dir = target.current();
      if (isRootKey(dir)) {
        toast.info(ROOT_FIRST);
        return;
      }
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
    const from = last?.cut && !clip ? clipKey(last) : null;
    const dir = from ? parentKey(from) : null;
    if (dir && ws.items.has(dir)) void ws.loadDir(dir);
  }, [clip, ws]);

  const openEntry = (r: Row, pin: boolean) => {
    if (!r.entry) return;
    ws.selected = r.path;
    if (r.entry.dir) ws.toggleDir(r.path);
    // a preview leaves the keys with the tree, as in VS Code: Ctrl+C after a click means the file
    else ws.open(r.path, { pin, keepFocus: !pin });
  };

  const items = (path: FileKey, e: ViewTreeEntry | null): MenuEntry[] => {
    const dir = !e || e.dir ? path : (parentKey(path) ?? ws.rootKey);
    const atRoot = isRootKey(dir);
    // a part's own folder (a Root card's top row): no cut, rename or trash from here
    const own = isPartRoot(ws.paths, path);
    const list: MenuEntry[] = [];
    if (e && !e.dir) {
      list.push(
        { label: 'Open', icon: 'file', shortcut: 'Enter', onSelect: () => ws.open(path, { pin: true }) },
        { label: 'Open to the Side', icon: 'layout', onSelect: () => ws.open(path, { pin: true, side: true }) },
        { label: 'Compare with HEAD', icon: 'branch', disabled: !ws.partOf(path)?.gitMark(path), onSelect: () => ws.openDiff(path, 'head') },
        { label: 'Local History', icon: 'history', onSelect: () => ws.openView({ kind: 'history', path }) },
        {
          label: 'Download',
          icon: 'download',
          disabled: e.size > MAX_DOWNLOAD,
          title: e.size > MAX_DOWNLOAD ? 'Larger than 20 MB: copy it with a terminal instead' : 'Save this file on this device',
          onSelect: () => {
            const { machineId, abs } = splitKey(path);
            downloadMachineFile(machineId, abs);
          },
        },
        'sep',
      );
    }
    list.push(
      { label: 'New File…', icon: 'file', disabled: atRoot, title: atRoot ? ROOT_FIRST : undefined, onSelect: () => ws.startInput('file', dir) },
      { label: 'New Folder…', icon: 'folder', disabled: atRoot, title: atRoot ? ROOT_FIRST : undefined, onSelect: () => ws.startInput('folder', dir) },
      // at the root: a terminal at the project root, which reaches every part
      { label: atRoot ? 'Open in Terminal at the Root' : 'Open in Terminal', icon: 'terminal', onSelect: () => onTerminal(dir) },
      'sep',
    );
    if (e && !own) {
      list.push(
        { label: 'Cut', icon: 'scissors', shortcut: 'Ctrl+X', onSelect: () => putAside(path, true) },
        { label: 'Copy', icon: 'copy', shortcut: 'Ctrl+C', onSelect: () => putAside(path, false) },
      );
    }
    list.push({ label: 'Paste', icon: 'clipboard', shortcut: 'Ctrl+V', disabled: atRoot, onSelect: () => void pasteFromMenu(dir).catch(failed) });
    if (e) {
      list.push(
        'sep',
        // the real path on its machine; the relative one as the card shows it ("admin/src/x.ts" in a Root card)
        { label: 'Copy Path', icon: 'link', onSelect: () => void navigator.clipboard?.writeText(splitKey(path).abs).then(() => toast.success('Path copied.')) },
        { label: 'Copy Relative Path', icon: 'link', onSelect: () => void navigator.clipboard?.writeText(ws.relLabel(path)).then(() => toast.success('Path copied.')) },
        { label: 'Ask an Agent About This', icon: 'sparkle', onSelect: () => onAsk(path) },
      );
      if (e.dir ? onAddFolder : onAddFile) {
        list.push(e.dir ? { label: 'Add Folder to Board', icon: 'plus', onSelect: () => onAddFolder?.(path) } : { label: 'Add to Board', icon: 'plus', disabled: e.binary, onSelect: () => onAddFile?.(path) });
      }
      if (!e.dir) list.push({ label: 'Duplicate', icon: 'duplicate', disabled: e.binary, onSelect: () => void ws.duplicate(path).catch((err) => toast.error(String(err))) });
      if (!own) {
        list.push(
          'sep',
          { label: 'Rename…', icon: 'pencil', shortcut: 'F2', onSelect: () => ws.startInput('rename', parentKey(path) ?? ws.rootKey, path) },
          { label: 'Move to Trash…', icon: 'trash', danger: true, shortcut: 'Del', onSelect: () => setTrash(path) },
        );
      }
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
        else if (cur) select(visible.find((r) => r.path === parentKey(cur.path)));
        break;
      case 'Enter':
        if (cur) openEntry(cur, true);
        break;
      case ' ':
        if (cur) openEntry(cur, false);
        break;
      case 'F2':
        if (cur && !isPartRoot(ws.paths, cur.path)) ws.startInput('rename', parentKey(cur.path) ?? ws.rootKey, cur.path);
        break;
      case 'Delete':
        if (cur && !isPartRoot(ws.paths, cur.path)) setTrash(cur.path);
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

  const dropTo = (dir: FileKey, e: DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setDropOn(null);
    // files from the computer are saved in that folder, as pasted ones are
    if (dragHasFiles(e.dataTransfer)) {
      const files = droppedFiles(e.dataTransfer);
      if (!files.length) return;
      if (isRootKey(dir)) {
        toast.info(ROOT_FIRST);
        return;
      }
      void ws.pasteFiles(dir, files).catch((err) => toast.error(err instanceof Error ? err.message : String(err)));
      return;
    }
    const from = asFileKey(e.dataTransfer.getData('application/x-nextboard-path'));
    if (!from || isRootKey(from) || isPartRoot(ws.paths, from) || parentKey(from) === dir || keyUnder(dir, from)) return;
    if (isRootKey(dir)) {
      toast.info(ROOT_FIRST);
      return;
    }
    void ws.moveTo(from, joinKey(dir, nameOf(from))).catch((err) => toast.error(err instanceof Error ? err.message : String(err)));
  };
  const atRoot = isRootKey(targetDir());
  const partHere = !multi && ws.viewKey !== ws.projectId ? ws.parts[0] : null;

  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2 className="truncate" title={`${ws.name}${partHere ? ` · ${partHere.name}` : ''}: ${multi ? 'every part of the project, each a folder' : splitKey(ws.rootKey).abs}`}>
          Project
          {partHere ? ` · ${partHere.name}` : ''}
        </h2>
        <Button size="sm" variant="ghost" iconOnly icon="file" aria-label="New file" title={atRoot ? ROOT_FIRST : 'New file'} disabled={atRoot} onClick={() => ws.startInput('file', targetDir())} />
        <Button size="sm" variant="ghost" iconOnly icon="folder" aria-label="New folder" title={atRoot ? ROOT_FIRST : 'New folder'} disabled={atRoot} onClick={() => ws.startInput('folder', targetDir())} />
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
        className={`wb-tree nowheel ${dropOn === ws.rootKey ? 'is-drop' : ''}`}
        role="tree"
        aria-label="Project files"
        tabIndex={0}
        onKeyDown={onKey}
        onContextMenu={(e) => {
          if (e.target !== e.currentTarget) return;
          e.preventDefault();
          setMenu({ x: e.clientX, y: e.clientY, path: ws.rootKey, entry: null });
        }}
        onDragOver={(e) => {
          e.preventDefault();
          e.stopPropagation(); // files dragged over the tree are the tree's, not the board's
          // nothing lands at a Root card's root
          if (!multi) setDropOn(ws.rootKey);
        }}
        onDragLeave={(e) => {
          if (e.target === e.currentTarget) setDropOn(null);
        }}
        onDrop={(e) => dropTo(ws.rootKey, e)}
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
          const part = ws.mountedPartOf(r.path);
          const mark = e.dir ? undefined : part?.gitMark(r.path);
          const dirTone = e.dir ? part?.gitDirTone(r.path) : undefined;
          const dim = !!part?.ignored.has(r.path) || e.hidden;
          const selected = ws.selected === r.path;
          const cut = clip?.cut && !!clipped && keyUnder(r.path, clipped);
          const review = e.dir ? undefined : part?.reviewMark(r.path);
          // a Root card's part row: its machine and folder
          const top = e.part ? part : null;
          const remote = !!top && top.machineId !== LOCAL_MACHINE;
          return (
            <div
              key={r.key}
              role="treeitem"
              aria-level={r.depth + 1}
              aria-expanded={e.dir ? open : undefined}
              aria-selected={selected}
              className={`wb-row ${top ? 'is-part' : ''} ${selected ? 'is-selected' : ''} ${dim ? 'is-dim' : ''} ${cut ? 'is-cut' : ''} ${mark ? `git-${mark.tone}` : ''} ${dirTone ? `git-${dirTone}` : ''} ${dropOn === r.path ? 'is-drop' : ''}`}
              style={style}
              title={top ? `${top.name}: ${top.root} on ${machineName(top.machineId)}` : ws.label(r.path)}
              draggable={!top}
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
              onDrop={(ev) => dropTo(e.dir ? r.path : (parentKey(r.path) ?? ws.rootKey), ev)}
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
              {top ? (
                // a part's row: its machine and its git state in two fixed columns, so the rows line up
                <span className="wb-part-end">
                  <span className="wb-part-machine">
                    <Icon name={remote ? 'server' : 'monitor'} />
                    <span className="truncate">{machineName(top.machineId)}</span>
                  </span>
                  <span className={`wb-part-dot ${dirTone ? '' : 'is-none'}`} aria-hidden="true" />
                </span>
              ) : (
                <>
                  {review && <span className="wb-rv-mark" title={`Changed by ${review.join(', ')}. Check it in Review.`} aria-label={`Changed by ${review.join(', ')}`} />}
                  {mark && <span className="wb-row-mark">{mark.letter}</span>}
                  {dirTone && <span className="wb-row-dot" aria-hidden="true" />}
                </>
              )}
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
