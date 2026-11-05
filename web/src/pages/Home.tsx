// Home: projects by host or by group, as bento cards or a list, filtered and sorted from
// the toolbar (DESIGN.md §3.11). Arrow keys move between projects, Enter opens, Delete asks
// to remove; Ctrl/⌘+O adds a project.

import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { api, errorMessage } from '../api';
import { machineStatus } from '../board/sessionStatus';
import { AppHeader } from '../components/AppHeader';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { Icon, type IconName } from '../components/Icon';
import { Menu } from '../components/Menu';
import { prefs } from '../lib/format';
import { takeSearchParam } from '../lib/router';
import { ArchiveDialog } from '../home/ArchiveDialog';
import { AddProjectDialog } from '../home/AddProjectDialog';
import { DeleteGroupDialog, GroupDialog } from '../home/GroupDialogs';
import { allTags, groupPath, groupTree, hasTag, useCollapsedGroups, withProjects, type GroupNode } from '../home/groups';
import { openProject, ProjectCard } from '../home/ProjectCard';
import type { Project, ProjectGroup } from '../lib/types';
import { isActive } from '../agents/state';
import { store, useMachines, useProjectGroups, useProjects, useProjectsReady, useRunningSessions } from '../store';

type HomeView = 'grid' | 'list';
type HomeSort = 'recent' | 'name';
type HomeBy = 'host' | 'group';

/** Below this the whole list fits on a screen: nothing to filter or sort. */
const TOOLS_FROM = 4;

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

function savedBy(): HomeBy | null {
  const v = prefs.get('home.by');
  return v === 'host' || v === 'group' ? v : null;
}

interface GroupSectionProps {
  node: GroupNode;
  collapsed: Set<string>;
  onToggle: (id: string) => void;
  /** A filter is on: everything that matched stays open and nothing is empty. */
  filtering: boolean;
  renderProjects: (list: Project[]) => ReactNode;
  onSubgroup: (g: ProjectGroup) => void;
  onEdit: (g: ProjectGroup) => void;
  onDelete: (g: ProjectGroup) => void;
}

function GroupSection({ node, collapsed, onToggle, filtering, renderProjects, onSubgroup, onEdit, onDelete }: GroupSectionProps) {
  const g = node.group;
  const open = filtering || !collapsed.has(g.id);
  const Title = `h${Math.min(node.depth + 2, 6)}` as 'h2';
  const empty = !node.projects.length && !node.children.length;
  return (
    <section className="home-group" aria-labelledby={`grp-${g.id}`}>
      <Title className="home-group-title" id={`grp-${g.id}`}>
        <button type="button" className="home-group-toggle" aria-expanded={open} disabled={filtering} onClick={() => onToggle(g.id)}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} />
          <span className="truncate">{g.name}</span>
        </button>
        <span className="home-group-count">{plural(node.total, 'project')}</span>
        <Menu
          label={`Actions for group ${g.name}`}
          items={[
            { label: 'New sub-group…', icon: 'plus', onSelect: () => onSubgroup(g) },
            { label: 'Edit group…', icon: 'pencil', onSelect: () => onEdit(g) },
            'sep',
            { label: 'Delete group…', icon: 'trash', danger: true, onSelect: () => onDelete(g) },
          ]}
        />
      </Title>
      {open && (
        <>
          {node.projects.length > 0 && renderProjects(node.projects)}
          {empty && <p className="home-group-note">No projects here yet. A project’s menu moves it into a group.</p>}
          {node.children.map((c) => (
            <GroupSection key={c.group.id} node={c} collapsed={collapsed} onToggle={onToggle} filtering={filtering} renderProjects={renderProjects} onSubgroup={onSubgroup} onEdit={onEdit} onDelete={onDelete} />
          ))}
        </>
      )}
    </section>
  );
}

export function HomePage() {
  const projects = useProjects();
  const ready = useProjectsReady();
  const machines = useMachines();
  const groupList = useProjectGroups();
  const [adding, setAdding] = useState(() => takeSearchParam('add') === '1');
  const [archive, setArchive] = useState(false);
  const [error, setError] = useState('');
  const [removeId, setRemoveId] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [tag, setTag] = useState('');
  const [view, setView] = useState<HomeView>(() => (prefs.get('home.view') === 'list' ? 'list' : 'grid'));
  const [sort, setSort] = useState<HomeSort>(() => (prefs.get('home.sort') === 'name' ? 'name' : 'recent'));
  const [chosenBy, setChosenBy] = useState<HomeBy | null>(savedBy);
  const [collapsed, toggleGroup] = useCollapsedGroups();
  const [newGroup, setNewGroup] = useState<{ parentId?: string } | null>(null);
  const [editGroup, setEditGroup] = useState<ProjectGroup | null>(null);
  const [deleteGroup, setDeleteGroup] = useState<ProjectGroup | null>(null);
  const gridRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api
      .projects()
      .then((list) => store.seedProjects(list))
      .catch((err) => setError(errorMessage(err)));
    api
      .machines()
      .then((list) => store.seedMachines(list))
      .catch(() => undefined);
  }, []);

  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'o' && !document.querySelector('.modal-layer')) {
        e.preventDefault();
        setAdding(true);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // until a choice is made, groups are the view as soon as there are any
  const by: HomeBy = chosenBy ?? (groupList.length ? 'group' : 'host');
  const tags = useMemo(() => allTags(projects), [projects]);
  // a tag that is no longer on any project filters nothing
  const activeTag = tags.some((t) => t.tag.toLowerCase() === tag.toLowerCase()) ? tag : '';
  const needle = query.trim().toLowerCase();
  const filtering = !!needle || !!activeTag;

  const shown = useMemo(() => {
    // useProjects already gives the most recently opened first
    const list = projects.filter((p) => (!activeTag || hasTag(p, activeTag)) && (!needle || [p.name, p.path, groupPath(groupList, p.groupId), ...(p.tags ?? [])].some((s) => s.toLowerCase().includes(needle))));
    if (sort === 'name') list.sort((a, b) => a.name.localeCompare(b.name));
    return list;
  }, [projects, groupList, needle, activeTag, sort]);

  const hosts = useMemo(() => {
    const order = new Map(machines.map((m, i) => [m.id, i]));
    const map = new Map<string, Project[]>();
    for (const p of shown) {
      const list = map.get(p.machineId);
      if (list) list.push(p);
      else map.set(p.machineId, [p]);
    }
    return [...map.entries()].sort((a, b) => (order.get(a[0]) ?? 99) - (order.get(b[0]) ?? 99));
  }, [shown, machines]);

  const tree = useMemo(() => {
    const t = groupTree(groupList, shown);
    return filtering ? { ...t, roots: withProjects(t.roots) } : t;
  }, [groupList, shown, filtering]);

  const mostRecent = projects[0]?.id;
  const activeConsoles = useRunningSessions().filter(isActive).length;
  const machineCount = new Set(projects.map((p) => p.machineId)).size;

  const changeView = (v: HomeView) => {
    prefs.set('home.view', v);
    setView(v);
  };
  const changeSort = (v: HomeSort) => {
    prefs.set('home.sort', v);
    setSort(v);
  };
  const changeBy = (v: HomeBy) => {
    prefs.set('home.by', v);
    setChosenBy(v);
  };
  const clearFilter = () => {
    setQuery('');
    setTag('');
  };

  // arrow keys between cards (geometric: nearest card in that direction)
  const onGridKey = useCallback((e: KeyboardEvent<HTMLDivElement>) => {
    const keys = ['ArrowRight', 'ArrowLeft', 'ArrowDown', 'ArrowUp'];
    const cur = (document.activeElement as HTMLElement | null)?.closest<HTMLElement>('.project-card');
    if (!cur) return;
    if (e.key === 'Delete' && (document.activeElement as HTMLElement).classList.contains('project-open')) {
      e.preventDefault();
      setRemoveId(cur.dataset.project ?? null);
      return;
    }
    if (!keys.includes(e.key) || !(document.activeElement as HTMLElement).classList.contains('project-open')) return;
    e.preventDefault();
    const cards = Array.from(gridRef.current?.querySelectorAll<HTMLElement>('.project-card') ?? []);
    const r0 = cur.getBoundingClientRect();
    const c0 = { x: r0.left + r0.width / 2, y: r0.top + r0.height / 2 };
    let best: HTMLElement | null = null;
    let bestD = Infinity;
    for (const c of cards) {
      if (c === cur) continue;
      const r = c.getBoundingClientRect();
      const cx = r.left + r.width / 2;
      const cy = r.top + r.height / 2;
      const dx = cx - c0.x;
      const dy = cy - c0.y;
      const ok =
        e.key === 'ArrowRight' ? dx > 4 && Math.abs(dy) < r0.height / 2 : e.key === 'ArrowLeft' ? dx < -4 && Math.abs(dy) < r0.height / 2 : e.key === 'ArrowDown' ? dy > 4 : dy < -4;
      if (!ok) continue;
      const d = e.key === 'ArrowDown' || e.key === 'ArrowUp' ? Math.abs(dy) * 1000 + Math.abs(dx) : Math.abs(dx);
      if (d < bestD) {
        bestD = d;
        best = c;
      }
    }
    best?.querySelector<HTMLElement>('.project-open')?.focus();
  }, []);

  const machineIcon = (id: string): IconName => (machines.find((m) => m.id === id)?.kind === 'ssh' ? 'server' : 'monitor');

  /** Cards or rows. Each view names on the card what its headings do not say. */
  const renderProjects = (list: Project[]) => {
    const items = list.map((p) => {
      const m = machines.find((x) => x.id === p.machineId);
      const st = m ? machineStatus(m) : null;
      const path = by === 'host' ? groupPath(groupList, p.groupId) : '';
      return (
        <ProjectCard
          key={p.id}
          project={p}
          row={view === 'list'}
          active={p.id === mostRecent}
          // only a machine known to be away: the list can load before the machines do
          offline={!!st && st.color !== 'success'}
          place={by === 'group' ? { icon: machineIcon(p.machineId), label: m?.name ?? p.machineId, title: `On ${m?.name ?? p.machineId}` } : path ? { icon: 'layers', label: path.split(' / ').pop() ?? path, title: `In ${path}` } : undefined}
          activeTag={activeTag}
          onTag={(t) => setTag(t.toLowerCase() === activeTag.toLowerCase() ? '' : t)}
          removeRequested={removeId === p.id}
          onRemoveHandled={() => setRemoveId(null)}
        />
      );
    });
    return view === 'grid' ? (
      <div className="bento project-grid">{items}</div>
    ) : (
      <div className="card project-list">
        <div className="project-list-head" aria-hidden="true">
          <span>Project</span>
          <span>Git</span>
          <span>Terminals</span>
          <span>Opened</span>
        </div>
        {items}
      </div>
    );
  };

  const nothingShown = filtering && shown.length === 0;

  return (
    <div className="page">
      <AppHeader />
      <main className="content">
        <div className="page-head">
          <div>
            <h1>Projects</h1>
            {projects.length > 0 ? (
              <p>
                {plural(projects.length, 'project')}
                {machineCount > 1 && ` on ${machineCount} machines`}
                {activeConsoles > 0 && ` · ${plural(activeConsoles, 'console')} active`}
              </p>
            ) : (
              <p>Folders on your machines. Each project keeps its own boards and terminals.</p>
            )}
          </div>
          <div className="row">
            <Button icon="archive" onClick={() => setArchive(true)}>
              Archive
            </Button>
            <Button icon="layers" onClick={() => setNewGroup({})}>
              New group
            </Button>
            <Button variant="primary" icon="plus" onClick={() => setAdding(true)} title="Add project (Ctrl+O)">
              Add project
            </Button>
          </div>
        </div>

        {error && (
          <div className="alert mb-4" role="alert">
            <Icon name="alertCircle" />
            <span>Could not load projects: {error}</span>
          </div>
        )}

        {!ready && !error && (
          <div className="center-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Loading projects…
          </div>
        )}

        {ready && projects.length === 0 && groupList.length === 0 && (
          <div className="card home-empty">
            <div className="empty-state">
              <span className="empty-icon">
                <Icon name="folder" />
              </span>
              <h2>No projects yet</h2>
              <p>Add a folder from one of your machines to get a board for it.</p>
              <Button variant="primary" icon="plus" onClick={() => setAdding(true)}>
                Add project
              </Button>
            </div>
          </div>
        )}

        {(projects.length >= TOOLS_FROM || groupList.length > 0 || tags.length > 0) && (
          <div className="home-tools">
            <div className="home-search">
              <Icon name="search" />
              <input
                className="control control-sm"
                type="search"
                value={query}
                placeholder="Filter by name, folder, group or tag"
                aria-label="Filter projects by name, folder, group or tag"
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Escape') setQuery('');
                }}
              />
            </div>
            <span className="spacer" />
            <SegmentedControl<HomeBy>
              size="sm"
              label="Arrange projects by"
              value={by}
              onChange={changeBy}
              options={[
                { value: 'host', label: 'Host', icon: 'server', title: 'By the machine each project is on' },
                { value: 'group', label: 'Group', icon: 'layers', title: 'By your groups' },
              ]}
            />
            <SegmentedControl<HomeSort>
              size="sm"
              label="Sort projects"
              value={sort}
              onChange={changeSort}
              options={[
                { value: 'recent', label: 'Recent', title: 'Most recently opened first' },
                { value: 'name', label: 'Name', title: 'A to Z' },
              ]}
            />
            <SegmentedControl<HomeView>
              size="sm"
              label="Show projects as"
              value={view}
              onChange={changeView}
              options={[
                { value: 'grid', label: 'Cards', icon: 'grid' },
                { value: 'list', label: 'List', icon: 'rows' },
              ]}
            />
          </div>
        )}

        {tags.length > 0 && (
          <div className="home-tags" role="group" aria-label="Show only a tag">
            {tags.map((t) => {
              const on = t.tag.toLowerCase() === activeTag.toLowerCase();
              return (
                <button key={t.tag} type="button" className={`project-tag is-button ${on ? 'is-on' : ''}`} aria-pressed={on} onClick={() => setTag(on ? '' : t.tag)}>
                  <span className="truncate">{t.tag}</span>
                  <span className="project-tag-count tabular">{t.count}</span>
                </button>
              );
            })}
          </div>
        )}

        {nothingShown && (
          <div className="center-msg" role="status">
            <p>{needle ? <>No projects match “{query.trim()}”{activeTag ? ` with the tag ${activeTag}` : ''}.</> : <>No projects with the tag {activeTag}.</>}</p>
            <Button size="sm" onClick={clearFilter}>
              Clear filter
            </Button>
          </div>
        )}

        <div ref={gridRef} onKeyDown={onGridKey}>
          {by === 'host' &&
            hosts.map(([machineId, list]) => {
              const m = machines.find((x) => x.id === machineId);
              const st = m ? machineStatus(m) : null;
              return (
                <section key={machineId} className="home-group" aria-labelledby={`grp-${machineId}`}>
                  <h2 className="home-group-title" id={`grp-${machineId}`}>
                    <Icon name={machineIcon(machineId)} />
                    {m?.name ?? machineId}
                    {st && (
                      <span className="status">
                        <span className={`dot dot-${st.color}`} aria-hidden="true" />
                        {st.label}
                      </span>
                    )}
                    <span className="home-group-count">{plural(list.length, 'project')}</span>
                  </h2>
                  {renderProjects(list)}
                </section>
              );
            })}

          {by === 'group' && (
            <>
              {tree.roots.map((n) => (
                <GroupSection
                  key={n.group.id}
                  node={n}
                  collapsed={collapsed}
                  onToggle={toggleGroup}
                  filtering={filtering}
                  renderProjects={renderProjects}
                  onSubgroup={(g) => setNewGroup({ parentId: g.id })}
                  onEdit={setEditGroup}
                  onDelete={setDeleteGroup}
                />
              ))}
              {tree.loose.length > 0 && (
                <section className="home-group" aria-labelledby="grp-loose">
                  <h2 className="home-group-title" id="grp-loose">
                    <Icon name="folder" />
                    All projects
                    <span className="home-group-count">{plural(tree.loose.length, 'project')}</span>
                  </h2>
                  {renderProjects(tree.loose)}
                </section>
              )}
            </>
          )}
        </div>
      </main>

      {adding && (
        <AddProjectDialog
          onClose={() => setAdding(false)}
          onAdded={(p) => {
            setAdding(false);
            openProject(p);
          }}
        />
      )}
      {archive && <ArchiveDialog onClose={() => setArchive(false)} />}
      {newGroup && <GroupDialog parentId={newGroup.parentId} onClose={() => setNewGroup(null)} onSaved={() => changeBy('group')} />}
      {editGroup && <GroupDialog group={editGroup} onClose={() => setEditGroup(null)} />}
      {deleteGroup && <DeleteGroupDialog group={deleteGroup} onClose={() => setDeleteGroup(null)} />}
    </div>
  );
}
