// Header controls inside a project: project switcher + workspace tabs (add, double-click
// or F2 to rename ≤ 24 chars, close with confirmation, Ctrl+Shift+←/→ to reorder).

import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { api, errorMessage } from '../api';
import { groupChain, groupTree, useCollapsedGroups, type GroupNode } from '../home/groups';
import { boardPath, navigate } from '../lib/router';
import type { CloseAllPreview, Project, Workspace } from '../lib/types';
import { isActive } from '../agents/state';
import { store, useMachines, useProjectGroups, useProjects, useSessions } from '../store';
import { ConfirmDialog } from './ConfirmDialog';
import { Icon } from './Icon';
import { ContextMenu, type MenuEntry } from './Menu';
import { toast } from './Toast';

export const NAME_MAX = 24;

const count = (n: number, noun: string) => `${n} ${noun}${n === 1 ? '' : 's'}`;

/** Asks before closing every board of a project; the hub says what would end. */
function CloseAllBoards({ project, onClose }: { project: Project; onClose: () => void }) {
  const [what, setWhat] = useState<CloseAllPreview | null>(null);
  useEffect(() => {
    let live = true;
    api
      .closeAllPreview(project.id)
      .then((p) => {
        if (live) setWhat(p);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [project.id]);

  const boards = what?.boards ?? project.workspaces?.length ?? 1;
  const nothing = !!what && boards === 1 && !what.tiles && !what.sessions && !what.apps && !what.loops;

  const close = async () => {
    if (nothing) return;
    const r = await api.closeAllBoards(project.id);
    store.upsertProject({ ...project, workspaces: [r.workspace] });
    navigate(boardPath(project.id, r.workspace.id), true);
    toast.success(`${count(r.closed, 'board')} closed.${r.archived ? ` ${r.archived} in Home › Archive.` : ''}`);
  };

  return (
    <ConfirmDialog title={`Close all boards of “${project.name}”?`} confirmLabel="Close all boards" danger onClose={onClose} onConfirm={close}>
      {nothing ? (
        <p>This project has one empty board, so nothing changes.</p>
      ) : (
        <div className="stack">
          <p>
            {boards === 1 ? (
              <>
                <strong>The board</strong> closes and one empty board stays.
              </>
            ) : (
              <>
                <strong>All {boards} boards</strong> close and one empty board stays.
              </>
            )}
          </p>
          {what ? (
            <ul className="plain">
              {what.sessions > 0 && (
                <li>
                  <strong>{count(what.sessions, 'terminal')}</strong> {what.sessions === 1 ? 'ends' : 'end'}
                  {what.running ? ` (${what.running} running)` : ''}
                </li>
              )}
              {what.apps > 0 && (
                <li>
                  <strong>{count(what.apps, 'desktop app')}</strong> {what.apps === 1 ? 'closes' : 'close'}
                </li>
              )}
              {what.loops > 0 && (
                <li>
                  <strong>{count(what.loops, 'running loop')}</strong> {what.loops === 1 ? 'stops' : 'stop'}
                </li>
              )}
              {what.tiles > 0 && (
                <li>
                  <strong>{count(what.tiles, 'tile')}</strong> {what.tiles === 1 ? 'moves' : 'move'} to the archive
                </li>
              )}
            </ul>
          ) : (
            <p>Terminals, desktop apps and running loops on them end.</p>
          )}
          <p>Restore tiles from Home › Archive; terminals do not come back. Files on disk are not touched.</p>
        </div>
      )}
    </ConfirmDialog>
  );
}

/** The projects by group, each group open or closed; those with no group under All projects. */
function SwitchMenu({ x, y, project, onClose, onCloseAll }: { x: number; y: number; project: Project; onClose: () => void; onCloseAll: () => void }) {
  const projects = useProjects();
  const machines = useMachines();
  const groups = useProjectGroups();
  // the current project is always in sight when the menu opens
  const [collapsed, toggle] = useCollapsedGroups(groupChain(groups, project.groupId).map((g) => g.id));
  const { roots, loose } = useMemo(() => groupTree(groups, projects), [groups, projects]);
  const machineName = (id: string) => machines.find((m) => m.id === id)?.name ?? id;

  const projectRow = (p: Project, indent: number): MenuEntry => ({
    label: p.name,
    icon: 'folder',
    meta: machineName(p.machineId),
    indent,
    title: `${p.name}\n${machineName(p.machineId)} · ${p.path}`,
    checked: p.id === project.id ? true : undefined,
    onSelect: () => navigate(boardPath(p.id)),
  });
  const groupRows = (n: GroupNode): MenuEntry[] => {
    const open = !collapsed.has(n.group.id);
    return [
      {
        label: n.group.name,
        icon: open ? 'chevronDown' : 'chevronRight',
        meta: String(n.total),
        indent: n.depth,
        expanded: open,
        keepOpen: true,
        onSelect: () => toggle(n.group.id),
      },
      ...(open ? [...n.projects.map((p) => projectRow(p, n.depth + 1)), ...n.children.flatMap(groupRows)] : []),
    ];
  };

  const items: MenuEntry[] = [
    { label: 'Home', icon: 'home', onSelect: () => navigate('/') },
    'sep',
    ...roots.flatMap(groupRows),
    ...(loose.length ? [{ type: 'heading', label: 'All projects' } as const, ...loose.map((p) => projectRow(p, 0))] : []),
    'sep',
    { label: 'Add project…', icon: 'plus', onSelect: () => navigate('/?add=1') },
    { label: 'Settings', icon: 'sliders', onSelect: () => navigate('/settings') },
    'sep',
    { label: 'Close all boards…', icon: 'close', danger: true, onSelect: onCloseAll },
  ];

  return <ContextMenu x={x} y={y} label="Switch project" items={items} onClose={onClose} anchored className="project-switch-menu" />;
}

export function ProjectSwitcher({ project }: { project: Project }) {
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const [closingAll, setClosingAll] = useState(false);
  const ref = useRef<HTMLButtonElement>(null);

  return (
    <>
      <button
        ref={ref}
        type="button"
        className="project-switch"
        aria-haspopup="menu"
        aria-expanded={!!menu}
        title={`${project.name} — ${project.path}`}
        onClick={() => {
          const r = ref.current!.getBoundingClientRect();
          setMenu(menu ? null : { x: r.left, y: r.bottom + 6 });
        }}
      >
        <Icon name="folder" />
        <span className="truncate">{project.name}</span>
        <Icon name="chevronDown" className="chev" />
      </button>
      {menu && <SwitchMenu x={menu.x} y={menu.y} project={project} onClose={() => setMenu(null)} onCloseAll={() => setClosingAll(true)} />}
      {closingAll && <CloseAllBoards project={project} onClose={() => setClosingAll(false)} />}
    </>
  );
}

export function WorkspaceTabs({ project, activeId }: { project: Project; activeId?: string }) {
  const workspaces = project.workspaces ?? [];
  const sessions = useSessions();
  const [renaming, setRenaming] = useState<string | null>(null);
  const [closing, setClosing] = useState<Workspace | null>(null);
  const [adding, setAdding] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);

  // keep the active tab visible
  useEffect(() => {
    listRef.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  }, [activeId]);

  const terminalsIn = (w: Workspace) => Math.max(w.sessionCount ?? 0, sessions.filter((s) => s.workspaceId === w.id).length);

  const add = async () => {
    if (adding) return;
    setAdding(true);
    try {
      const w = await api.createWorkspace(project.id);
      store.upsertProject({ ...project, workspaces: [...workspaces, w] });
      navigate(boardPath(project.id, w.id));
    } catch (err) {
      toast.error(`Could not add a workspace: ${errorMessage(err)}`);
    } finally {
      setAdding(false);
    }
  };

  const rename = async (w: Workspace, name: string) => {
    setRenaming(null);
    const v = name.trim().slice(0, NAME_MAX);
    if (!v || v === w.name) return;
    store.upsertProject({ ...project, workspaces: workspaces.map((x) => (x.id === w.id ? { ...x, name: v } : x)) });
    try {
      await api.updateWorkspace(w.id, { name: v });
    } catch (err) {
      toast.error(`Could not rename the workspace: ${errorMessage(err)}`);
    }
  };

  const move = async (w: Workspace, index: number) => {
    if (index < 0 || index >= workspaces.length) return;
    const list = workspaces.filter((x) => x.id !== w.id);
    list.splice(index, 0, w);
    store.upsertProject({ ...project, workspaces: list });
    try {
      await api.updateWorkspace(w.id, { index });
    } catch (err) {
      toast.error(`Could not move the workspace: ${errorMessage(err)}`);
    }
  };

  const close = async (w: Workspace) => {
    await api.deleteWorkspace(w.id);
    const rest = workspaces.filter((x) => x.id !== w.id);
    store.upsertProject({ ...project, workspaces: rest });
    if (w.id === activeId && rest.length) {
      const i = Math.max(0, workspaces.findIndex((x) => x.id === w.id) - 1);
      navigate(boardPath(project.id, rest[Math.min(i, rest.length - 1)].id), true);
    }
    toast.success(`Workspace “${w.name}” closed. Its board is in the archive.`);
  };

  const onTabKey = (e: KeyboardEvent<HTMLElement>, w: Workspace, i: number) => {
    const tabs = Array.from(listRef.current?.querySelectorAll<HTMLElement>('[role="tab"]') ?? []);
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
      e.preventDefault();
      void move(w, i + (e.key === 'ArrowLeft' ? -1 : 1));
      return;
    }
    if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
      e.preventDefault();
      tabs[(i + (e.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length]?.focus();
    } else if (e.key === 'Home' || e.key === 'End') {
      e.preventDefault();
      tabs[e.key === 'Home' ? 0 : tabs.length - 1]?.focus();
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      navigate(boardPath(project.id, w.id));
    } else if (e.key === 'F2') {
      e.preventDefault();
      setRenaming(w.id);
    } else if (e.key === 'Delete' && workspaces.length > 1) {
      e.preventDefault();
      setClosing(w);
    }
  };

  return (
    <div className="ws-tabs-wrap">
      <div className="ws-tabs" role="tablist" aria-label={`Workspaces of ${project.name}`} ref={listRef}>
        {workspaces.map((w, i) => {
          const active = w.id === activeId;
          const n = terminalsIn(w);
          const working = sessions.filter((s) => s.workspaceId === w.id && isActive(s)).length;
          return (
            <div key={w.id} className={`ws-tab ${active ? 'active' : ''}`}>
              {renaming === w.id ? (
                <input
                  className="control control-sm ws-rename"
                  defaultValue={w.name}
                  autoFocus
                  maxLength={NAME_MAX}
                  aria-label="Workspace name"
                  onFocus={(e) => e.currentTarget.select()}
                  onBlur={(e) => void rename(w, e.target.value)}
                  onKeyDown={(e) => {
                    e.stopPropagation();
                    if (e.key === 'Enter') e.currentTarget.blur();
                    else if (e.key === 'Escape') setRenaming(null);
                  }}
                />
              ) : (
                <button
                  type="button"
                  role="tab"
                  aria-selected={active}
                  tabIndex={active ? 0 : -1}
                  className="ws-tab-btn"
                  title={`${w.name}${n ? ` · ${working} of ${n} terminal${n === 1 ? '' : 's'} active` : ''} — double-click to rename`}
                  onClick={() => navigate(boardPath(project.id, w.id))}
                  onDoubleClick={() => setRenaming(w.id)}
                  onKeyDown={(e) => onTabKey(e, w, i)}
                >
                  <span className="truncate">{w.name}</span>
                  {working > 0 && <span className="ws-count tabular" aria-label={`${working} active`}>{working}</span>}
                </button>
              )}
              {workspaces.length > 1 && renaming !== w.id && (
                <button
                  type="button"
                  className="ws-close"
                  tabIndex={-1}
                  aria-label={`Close workspace ${w.name}`}
                  title="Close workspace"
                  onClick={() => setClosing(w)}
                >
                  <Icon name="close" />
                </button>
              )}
            </div>
          );
        })}
      </div>
      <button type="button" className="ws-add" aria-label="New workspace" title="New workspace" disabled={adding} onClick={() => void add()}>
        <Icon name="plus" />
      </button>
      {closing && (
        <ConfirmDialog
          title={`Close “${closing.name}”?`}
          confirmLabel="Close workspace"
          danger
          onClose={() => setClosing(null)}
          onConfirm={() => close(closing)}
        >
          {(() => {
            const n = terminalsIn(closing);
            return (
              <p>
                {n > 0 ? (
                  <>
                    <strong>{n === 1 ? '1 terminal' : `${n} terminals`}</strong> in this workspace will end.{' '}
                  </>
                ) : null}
                Its board moves to the archive; you can restore it from Home › Archive. Desktop apps shown only here close. Files on disk are not touched.
              </p>
            );
          })()}
        </ConfirmDialog>
      )}
    </div>
  );
}
