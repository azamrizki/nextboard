// Home: projects by host or by group, as bento cards or a list, filtered and sorted from
// the toolbar (DESIGN.md §3.11), with an Archived view and a Groups view. Select mode picks
// several to act on at once (§3.18). Arrow keys move between projects, Enter opens, Delete asks
// to remove; Ctrl/⌘+O adds a project.

import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { api, errorMessage } from '../api';
import { machineStatus } from '../board/sessionStatus';
import { AppHeader } from '../components/AppHeader';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { Icon, type IconName } from '../components/Icon';
import { Menu, type MenuEntry } from '../components/Menu';
import { SelectBar, SelectToggle, useSelectMode } from '../components/SelectBar';
import { toast } from '../components/Toast';
import { prefs } from '../lib/format';
import { takeSearchParam } from '../lib/router';
import { ArchiveDialog } from '../home/ArchiveDialog';
import { ArchivedList } from '../home/ArchivedList';
import { AddProjectDialog } from '../home/AddProjectDialog';
import { DeleteGroupDialog, DeleteGroupsDialog, GroupDialog } from '../home/GroupDialogs';
import { GroupList } from '../home/GroupList';
import { allTags, flatGroups, groupPath, groupTree, hasTag, useCollapsedGroups, withProjects, type GroupNode } from '../home/groups';
import { openProject, ProjectCard } from '../home/ProjectCard';
import { RemoveDialog, restoreProjects } from '../home/RemoveDialog';
import type { RemoveChoice } from '../home/remove';
import type { Project, ProjectGroup } from '../lib/types';
import { isActive } from '../agents/state';
import { store, useArchivedProjects, useMachines, useProjectGroups, useProjects, useProjectsReady, useRunningSessions } from '../store';

type HomeView = 'grid' | 'list';
type HomeSort = 'recent' | 'name';
type HomeBy = 'host' | 'group';
/** Which list the page shows: the active projects, the archived ones, or the groups. */
type HomeTab = 'active' | 'archived' | 'groups';

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
  const [tab, setTab] = useState<HomeTab>('active');
  const archived = useArchivedProjects();
  const [removing, setRemoving] = useState<{ ids: string[]; archived: boolean; choice: RemoveChoice } | null>(null);
  const [deletingGroups, setDeletingGroups] = useState<string[] | null>(null);
  const [restoring, setRestoring] = useState(false);
  const gridRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api
      .projects()
      .then((list) => store.seedProjects(list))
      .catch((err) => setError(errorMessage(err)));
    api
      .archivedProjects()
      .then((list) => store.seedArchivedProjects(list))
      .catch(() => undefined);
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

  const archivedShown = useMemo(
    () => archived.filter((p) => !needle || [p.name, p.path, groupPath(groupList, p.groupId), ...(p.tags ?? [])].some((s) => s.toLowerCase().includes(needle))),
    [archived, groupList, needle],
  );
  // every group with its count over all active projects; a filter matches the name or the path
  const groupRows = useMemo(
    () => flatGroups(groupTree(groupList, projects).roots).filter((n) => !needle || groupPath(groupList, n.group.id).toLowerCase().includes(needle)),
    [groupList, projects, needle],
  );

  // What is on screen, top to bottom, for Shift runs and select-all: a folded group hides its
  // projects. What the view holds, folded or not, is what a selection may keep.
  const { order, present } = useMemo(() => {
    if (tab === 'archived') {
      const ids = archivedShown.map((p) => p.id);
      return { order: ids, present: ids };
    }
    if (tab === 'groups') {
      const ids = groupRows.map((n) => n.group.id);
      return { order: ids, present: ids };
    }
    const all = shown.map((p) => p.id);
    if (by === 'host') return { order: hosts.flatMap(([, list]) => list.map((p) => p.id)), present: all };
    const walk = (n: GroupNode): string[] => (filtering || !collapsed.has(n.group.id) ? [...n.projects.map((p) => p.id), ...n.children.flatMap(walk)] : []);
    return { order: [...tree.roots.flatMap(walk), ...tree.loose.map((p) => p.id)], present: all };
  }, [tab, archivedShown, groupRows, shown, by, hosts, tree, filtering, collapsed]);
  const select = useSelectMode(order, present);

  const changeTab = (t: HomeTab) => {
    select.stop();
    setTab(t);
  };
  // a view that empties itself (the last archived one restored) gives way to the projects
  useEffect(() => {
    if (tab === 'archived' && archived.length === 0 && !select.on) setTab('active');
    if (tab === 'groups' && groupList.length === 0) setTab('active');
  }, [tab, archived.length, groupList.length, select.on]);

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

  const projectById = (id: string) => projects.find((p) => p.id === id) ?? archived.find((p) => p.id === id);
  const pickedProjects = (ids: string[]) => ids.map(projectById).filter((p): p is Project => !!p);

  const moveTo = async (groupId: string) => {
    const ids = select.picked;
    const results = await Promise.allSettled(ids.map((id) => api.updateProject(id, { groupId })));
    let moved = 0;
    results.forEach((r) => {
      if (r.status === 'fulfilled') {
        store.upsertProject(r.value);
        moved++;
      }
    });
    const where = groupId ? groupPath(groupList, groupId) : 'All projects';
    if (moved === ids.length) toast.success(`${plural(moved, 'project')} moved to ${where}.`);
    else toast.error(`${moved} of ${ids.length} moved to ${where}; the others were refused.`);
    select.stop();
  };

  const groupOptions = useMemo(() => flatGroups(groupTree(groupList, []).roots), [groupList]);
  const moveItems = (): MenuEntry[] => [
    { label: 'No group (All projects)', icon: 'folder', onSelect: () => void moveTo('') },
    ...(groupOptions.length ? (['sep'] as MenuEntry[]) : []),
    ...groupOptions.map((n) => ({ label: n.group.name, icon: 'layers' as const, indent: n.depth, onSelect: () => void moveTo(n.group.id) })),
  ];

  const restorePicked = async () => {
    const list = pickedProjects(select.picked);
    setRestoring(true);
    try {
      const out = await restoreProjects(list);
      if (out.done.length) toast.success(`${out.done.length === 1 ? list.find((p) => p.id === out.done[0])?.name : plural(out.done.length, 'project')} restored.`);
      if (out.failed.length) toast.error(`Not restored: ${out.failed.map((f) => `${f.name} (${f.error})`).join(', ')}.`);
      select.stop();
    } catch (err) {
      toast.error(`Could not restore: ${errorMessage(err)}`);
    } finally {
      setRestoring(false);
    }
  };

  const showGroup = (g: ProjectGroup) => {
    changeTab('active');
    changeBy('group');
    clearFilter();
    requestAnimationFrame(() => document.getElementById(`grp-${g.id}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' }));
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
          select={select}
        />
      );
    });
    return view === 'grid' ? (
      <div className={`bento project-grid ${select.on ? 'is-selecting' : ''}`}>{items}</div>
    ) : (
      <div className={`card project-list ${select.on ? 'is-selecting' : ''}`}>
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

  const nothingShown = tab === 'active' && filtering && shown.length === 0;
  const tabs = [
    { value: 'active' as const, label: <>Projects <span className="seg-count tabular">{projects.length}</span></>, title: 'The projects in use' },
    ...(archived.length || tab === 'archived' ? [{ value: 'archived' as const, label: <>Archived <span className="seg-count tabular">{archived.length}</span></>, title: 'Archived projects: Restore brings one back as it was' }] : []),
    ...(groupList.length ? [{ value: 'groups' as const, label: <>Groups <span className="seg-count tabular">{groupList.length}</span></>, title: 'Every group, to rename or delete several' }] : []),
  ];
  const listed = tab === 'active' ? projects.length : tab === 'archived' ? archived.length : groupList.length;
  const label = tab === 'groups' ? 'groups' : 'projects';
  const none = select.count === 0;

  return (
    <div className={`page ${select.on ? 'has-select-bar' : ''}`}>
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
            <Button icon="archive" onClick={() => setArchive(true)} title="Closed and cleared boards, to restore into a project">
              Board archive
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

        {tab === 'active' && !ready && !error && (
          <div className="center-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Loading projects…
          </div>
        )}

        {tab === 'active' && ready && projects.length === 0 && groupList.length === 0 && (
          <div className="card home-empty">
            <div className="empty-state">
              <span className="empty-icon">
                <Icon name="folder" />
              </span>
              <h2>{archived.length ? 'No projects in use' : 'No projects yet'}</h2>
              <p>Add a folder from one of your machines to get a board for it.</p>
              <Button variant="primary" icon="plus" onClick={() => setAdding(true)}>
                Add project
              </Button>
            </div>
          </div>
        )}

        {(projects.length > 0 || archived.length > 0 || groupList.length > 0) && (
          <div className="home-views">
            {tabs.length > 1 && <SegmentedControl<HomeTab> size="sm" label="Show" value={tab} onChange={changeTab} options={tabs} className="home-tabs" />}
            <span className="spacer" />
            <SelectToggle mode={select} label={label} disabled={listed === 0} />
          </div>
        )}

        {/* the other views filter with the same words: their own box, while there is enough to filter or a filter is on */}
        {tab !== 'active' && ((tab === 'archived' ? archived.length : groupList.length) >= TOOLS_FROM || !!needle) && (
          <div className="home-tools">
            <div className="home-search">
              <Icon name="search" />
              <input
                className="control control-sm"
                type="search"
                value={query}
                placeholder={tab === 'archived' ? 'Filter by name, folder, group or tag' : 'Filter by group'}
                aria-label={tab === 'archived' ? 'Filter archived projects' : 'Filter groups'}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === 'Escape' && setQuery('')}
              />
            </div>
          </div>
        )}

        {tab === 'archived' && <ArchivedList projects={archivedShown} filtering={!!needle} select={select} onDelete={(list) => setRemoving({ ids: list.map((p) => p.id), archived: true, choice: 'delete' })} />}

        {tab === 'groups' && (
          <GroupList
            rows={groupRows}
            filtering={!!needle}
            select={select}
            onOpen={showGroup}
            onSubgroup={(g) => setNewGroup({ parentId: g.id })}
            onEdit={setEditGroup}
            onDelete={setDeleteGroup}
            onNew={() => setNewGroup({})}
          />
        )}

        {tab === 'active' && (projects.length >= TOOLS_FROM || groupList.length > 0 || tags.length > 0) && (
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

        {tab === 'active' && tags.length > 0 && (
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

        <div ref={gridRef} onKeyDown={onGridKey} hidden={tab !== 'active'}>
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
      {removing && (
        <RemoveDialog
          projects={pickedProjects(removing.ids)}
          archived={removing.archived}
          initial={removing.choice}
          onClose={() => setRemoving(null)}
          onDone={() => select.stop()}
        />
      )}
      {deletingGroups && (
        <DeleteGroupsDialog groups={groupList.filter((g) => deletingGroups.includes(g.id))} onClose={() => setDeletingGroups(null)} onDone={() => select.stop()} />
      )}

      <SelectBar mode={select} label={label}>
        {tab === 'active' && (
          <>
            <Button size="sm" icon="archive" disabled={none} onClick={() => setRemoving({ ids: select.picked, archived: false, choice: 'archive' })}>
              Archive
            </Button>
            <Button size="sm" variant="danger-ghost" icon="trash" disabled={none} onClick={() => setRemoving({ ids: select.picked, archived: false, choice: 'delete' })}>
              Delete permanently
            </Button>
            {none ? (
              <Button size="sm" variant="ghost" icon="layers" disabled>
                Move to group
              </Button>
            ) : (
              <Menu label="Move to group" icon="layers" triggerLabel="Move to group" items={moveItems} className="select-bar-menu" />
            )}
          </>
        )}
        {tab === 'archived' && (
          <>
            <Button size="sm" icon="restart" disabled={none} loading={restoring} onClick={() => void restorePicked()}>
              Restore
            </Button>
            <Button size="sm" variant="danger-ghost" icon="trash" disabled={none || restoring} onClick={() => setRemoving({ ids: select.picked, archived: true, choice: 'delete' })}>
              Delete permanently
            </Button>
          </>
        )}
        {tab === 'groups' && (
          <Button size="sm" variant="danger-ghost" icon="trash" disabled={none} onClick={() => setDeletingGroups(select.picked)}>
            Delete groups
          </Button>
        )}
      </SelectBar>
    </div>
  );
}
