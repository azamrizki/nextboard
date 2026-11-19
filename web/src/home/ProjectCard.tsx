// One project on Home: a bento card, or a row of the list view (same markup, DESIGN.md
// §3.11). Cover across the top of a card; icon or monogram, name and folder; git state on one
// line; terminals, boards and last opened at the foot. The whole card opens the project, or in
// select mode picks it (§3.18).

import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react';
import { useWarmIntent } from '../agents/warm';
import { api, errorMessage } from '../api';
import { Icon, type IconName } from '../components/Icon';
import { Menu } from '../components/Menu';
import { NAME_MAX } from '../components/ProjectBar';
import { SelectBox, type SelectMode } from '../components/SelectBar';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgoShort } from '../lib/format';
import { lastWorkspace } from '../lib/router';
import { projectParts } from '../lib/parts';
import { renameTo, useRenameDraft } from '../lib/renameDraft';
import type { GitInfo, Project } from '../lib/types';
import { isActive } from '../agents/state';
import { store, useBadgeOf, useMachines, useRunningCount } from '../store';
import { gitApi } from '../studio/api';
import { ArtDialog } from './ArtDialog';
import { BadgeDialog, BadgeMark } from './badges';
import { FolderPath } from './FolderPath';
import { openBoard, openCode, openProject } from './OpenChoice';
import { OrganizeDialog } from './GroupDialogs';
import { PartsDialog } from './PartsDialog';
import { PartsGitLine, PartsLine, partsGitState, usePartsGit } from './PartsLine';
import { ProjectCover, ProjectIcon } from './ProjectArt';
import { RemoveDialog } from './RemoveDialog';

export { projectMonogram } from './ProjectArt';

/** A folder that is no repository itself but holds some (docs/API-studio.md §3.6). */
interface NestedRepos {
  count: number;
  changed: number;
}

interface GitState {
  at: number;
  info: GitInfo | null;
  error: string;
  nested: NestedRepos | null;
}

// Git status is fetched per card; cache it briefly so going back to Home is instant.
const gitCache = new Map<string, GitState>();
const GIT_TTL = 30_000;

async function readGit(id: string, signal: AbortSignal): Promise<GitState> {
  const info = await api.projectGit(id, signal);
  let nested: NestedRepos | null = null;
  if (!info.isRepo && !info.error) {
    // Only worth a scan where the plain answer would be "no repository".
    const list = await gitApi.repos(id).catch(() => null);
    if (list?.items?.length) nested = { count: list.items.length, changed: list.items.reduce((n, r) => n + (r.changed || 0), 0) };
  }
  return { at: Date.now(), info, error: '', nested };
}

/** The machine being offline is its own answer: no request that can only fail. */
function useGit(id: string, offline: boolean, skip = false) {
  const [state, setState] = useState(() => gitCache.get(id) ?? null);
  useEffect(() => {
    if (offline || skip) return;
    const c = gitCache.get(id);
    if (c && Date.now() - c.at < GIT_TTL) return;
    const ctrl = new AbortController();
    readGit(id, ctrl.signal)
      .then((v) => {
        gitCache.set(id, v);
        setState(v);
      })
      .catch((err) => {
        if (ctrl.signal.aborted) return;
        const v = { at: Date.now(), info: null, error: errorMessage(err), nested: null };
        gitCache.set(id, v);
        setState(v);
      });
    return () => ctrl.abort();
  }, [id, offline, skip]);
  return state;
}

// opening asks the hub's ui.openProjectsIn which view (OpenChoice.tsx)
export { openProject };

/** Clean or how many files changed, as a dot and a word: on a card in its foot, in a row in the Git column. */
export function GitStateMark({ text, dirty }: { text: string; dirty: boolean }) {
  return (
    <span className={`project-state ${dirty ? 'is-dirty' : 'is-clean'}`}>
      <span className={`dot ${dirty ? 'dot-warning' : 'dot-success'}`} aria-hidden="true" />
      {text}
    </span>
  );
}

function gitStateOf(git: GitState | null, offline: boolean): { text: string; dirty: boolean } | null {
  if (offline || !git || git.error || git.info?.error) return null;
  const changed = git.nested ? git.nested.changed : git.info?.isRepo ? git.info.changed : null;
  if (changed == null) return null;
  return { text: changed ? `${changed} changed` : 'Clean', dirty: changed > 0 };
}

function ChangedBadge({ changed }: { changed: number }) {
  return <GitStateMark text={changed ? `${changed} changed` : 'Clean'} dirty={changed > 0} />;
}

/** Where the git stands. A card shows Clean or changed in its foot instead (`noState`). */
function GitLine({ git, offline, noState }: { git: GitState | null; offline: boolean; noState?: boolean }) {
  const info = git?.info;
  let body;
  if (offline) body = <span className="project-note">Machine offline</span>;
  else if (!git) body = <span className="project-note">Checking git…</span>;
  else if (git.error || info?.error) body = <span className="project-warn">Git unavailable</span>;
  else if (git.nested) {
    body = (
      <>
        <span className="project-branch">
          <Icon name="branch" />
          <span className="project-branch-name">{git.nested.count === 1 ? '1 repository' : `${git.nested.count} repositories`}</span>
        </span>
        {!noState && <ChangedBadge changed={git.nested.changed} />}
      </>
    );
  } else if (!info?.isRepo) body = <span className="project-note">No git repository</span>;
  else {
    body = (
      <>
        <span className="project-branch mono">
          <Icon name="branch" />
          <span className="project-branch-name">{info.branch || 'detached'}</span>
        </span>
        {info.ahead > 0 && (
          <span className="project-sync tabular">
            <Icon name="arrowUp" />
            {info.ahead}
            <span className="sr-only"> ahead</span>
          </span>
        )}
        {info.behind > 0 && (
          <span className="project-sync is-behind tabular">
            <Icon name="arrowDown" />
            {info.behind}
            <span className="sr-only"> behind</span>
          </span>
        )}
        {!noState && <ChangedBadge changed={info.changed} />}
      </>
    );
  }
  return <div className="project-git">{body}</div>;
}

function terminalsText(p: Project, active: number, idle: number): string {
  if (active > 0) return idle > 0 ? `${active} active · ${idle} idle` : `${active} active`;
  if (idle > 0) return `${idle} idle`;
  if (p.runningCount > 0) return `${p.runningCount} open`;
  if (p.sessionCount > 0) return p.sessionCount === 1 ? '1 terminal' : `${p.sessionCount} terminals`;
  return 'No terminals';
}

/** A card shows this many tags, and says how many more there are. */
const TAGS_SHOWN = 3;

/** Where the project is, when the heading above it does not say: its host or its group. */
function ProjectMarks({ p, place, activeTag, onTag }: { p: Project; place?: ProjectCardProps['place']; activeTag?: string; onTag?: (tag: string) => void }) {
  const tags = p.tags ?? [];
  if (!place && !tags.length) return null;
  const more = tags.slice(TAGS_SHOWN);
  return (
    <div className="project-marks">
      {place && (
        <span className="project-place" title={place.title}>
          <Icon name={place.icon} />
          <span className="truncate">{place.label}</span>
        </span>
      )}
      {tags.slice(0, TAGS_SHOWN).map((t) => {
        const on = t.toLowerCase() === activeTag?.toLowerCase();
        return (
          <button key={t} type="button" className={`project-tag is-button ${on ? 'is-on' : ''}`} aria-pressed={on} title={`Show only ${t}`} onClick={() => onTag?.(t)}>
            <span className="truncate">{t}</span>
          </button>
        );
      })}
      {more.length > 0 && (
        <span className="project-tag-more tabular" title={more.join(', ')}>
          +{more.length}
        </span>
      )}
    </div>
  );
}

interface ProjectCardProps {
  project: Project;
  /** A row of the list view instead of a card of its own. */
  row?: boolean;
  /** Most recently opened project. */
  active?: boolean;
  /** Its machine isn't reachable right now. */
  offline?: boolean;
  place?: { icon: IconName; label: string; title: string };
  activeTag?: string;
  onTag?: (tag: string) => void;
  removeRequested?: boolean;
  onRemoveHandled?: () => void;
  /** Select mode of the list (§3.18): a click picks instead of opening. */
  select?: SelectMode;
}

export function ProjectCard({ project: p, row, active, offline = false, place, activeTag, onTag, removeRequested, onRemoveHandled, select }: ProjectCardProps) {
  // a project in several parts: every part equal, its git added up (§3.11)
  const multi = projectParts(p).length > 1;
  const git = useGit(p.id, offline, multi);
  const partsGit = usePartsGit(p, multi);
  const machines = useMachines();
  // under Group the card names where it is: for a project on several machines, how many, never
  // just the first part's one
  const machinesPlace: ProjectCardProps['place'] =
    multi && place && place.icon !== 'layers'
      ? (() => {
          const ids = [...new Set(projectParts(p).map((pt) => pt.machineId))];
          const names = ids.map((id) => machines.find((m) => m.id === id)?.name ?? id);
          return { icon: 'server', label: `${ids.length} machine${ids.length === 1 ? '' : 's'}`, title: `On ${names.join(', ')}` };
        })()
      : undefined;
  // counts, not the list: a card renders again only when its own numbers change
  const consoleCount = useRunningCount((s) => s.projectId === p.id);
  const activeCount = useRunningCount((s) => s.projectId === p.id && isActive(s));
  const idleCount = consoleCount - activeCount;
  const [renaming, setRenaming] = useState(false);
  const draft = useRenameDraft(p.name);
  const [confirm, setConfirm] = useState(false);
  const [organizing, setOrganizing] = useState(false);
  const [partsOpen, setPartsOpen] = useState(false);
  const [artOpen, setArtOpen] = useState(false);
  const [badgeOpen, setBadgeOpen] = useState(false);
  const badge = useBadgeOf(p);
  const live = useRef(false);
  const selecting = !!select?.on;
  const selected = !!select?.has(p.id);

  useEffect(() => {
    if (removeRequested) {
      setConfirm(true);
      onRemoveHandled?.();
    }
  }, [removeRequested, onRemoveHandled]);

  const rename = async () => {
    if (!live.current) return;
    live.current = false;
    setRenaming(false);
    // renamed elsewhere while the field was open and not typed in: nothing to save
    const v = renameTo(draft.typed(), p.name, NAME_MAX);
    draft.reset();
    if (!v) return;
    try {
      store.upsertProject(await api.updateProject(p.id, { name: v }));
    } catch (err) {
      toast.error(`Could not rename ${p.name}: ${errorMessage(err)}`);
    }
  };

  const wsCount = p.workspaces?.length ?? 0;
  // a card says Clean or changed in its foot; a row says it in its Git column
  const footState = row ? null : multi ? partsGitState(partsGit) : gitStateOf(git, offline);
  const branch = multi ? '' : git?.info?.isRepo ? git.info.branch : '';
  // a hand on its card: its asleep agents start before the click lands (ROADMAP #50)
  const warmIntent = useWarmIntent(p.id, p.workspaces?.find((w) => w.id === lastWorkspace(p.id))?.id ?? p.workspaces?.[0]?.id);

  const onOpen = (e: ReactMouseEvent) => {
    // Shift, Ctrl or ⌘ start select mode from a card, as in a file manager
    if (select && (selecting || e.shiftKey || e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      select.click(p.id, e);
      return;
    }
    openProject(p);
  };

  return (
    <article
      className={`project-card ${row ? 'project-row' : 'card'} ${active ? 'is-active' : ''} ${selecting ? 'is-selecting' : ''} ${selected ? 'is-selected' : ''}`}
      data-project={p.id}
      aria-label={p.name}
      {...select?.pressProps(p.id)}
    >
      {!row && <ProjectCover project={p} />}
      {selecting && <SelectBox checked={selected} />}
      <header className="project-head">
        <div className="project-id">
          <ProjectIcon project={p} />
          <div className="project-title">
            <div className="project-name-line">
            {renaming ? (
              <input
                className="control control-sm"
                value={draft.value}
                autoFocus
                maxLength={NAME_MAX}
                aria-label="Project name"
                onFocus={(e) => e.currentTarget.select()}
                onChange={(e) => draft.set(e.target.value)}
                onBlur={() => void rename()}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  if (e.key === 'Enter') e.currentTarget.blur();
                  if (e.key === 'Escape') {
                    live.current = false;
                    draft.reset();
                    setRenaming(false);
                  }
                }}
              />
            ) : (
              <h3 className="truncate">
                <button
                  type="button"
                  className="card-link project-open"
                  {...(selecting ? { role: 'checkbox', 'aria-checked': selected } : warmIntent)}
                  onClick={onOpen}
                  // a Shift-click picks a run of cards; it must not select their text
                  onMouseDown={(e) => {
                    if (e.shiftKey) e.preventDefault();
                  }}
                  onDoubleClick={(e) => {
                    e.preventDefault();
                  }}
                  // the button covers the card, so this tooltip speaks for everything cut short on it
                  title={selecting ? `${selected ? 'Clear' : 'Select'} ${p.name}` : `Open ${p.name}\n${multi ? projectParts(p).map((pt) => `${pt.name}: ${pt.path}`).join('\n') : p.path}${branch ? `\nBranch ${branch}` : ''}`}
                >
                  {p.name}
                  {/* a card shows its badge on the cover, which a screen reader skips */}
                  {badge && !row && <span className="sr-only">, {badge.name}</span>}
                </button>
              </h3>
            )}
            {badge && row && !renaming && <BadgeMark badge={badge} className="row-badge" />}
            </div>
            {multi ? <PartsLine project={p} git={partsGit} onEdit={() => setPartsOpen(true)} machinesShown={!!machinesPlace} /> : <FolderPath path={p.path} />}
            <ProjectMarks p={p} place={machinesPlace ?? place} activeTag={activeTag} onTag={onTag} />
          </div>
        </div>
        <Menu
          className="card-menu"
          label={`Actions for ${p.name}`}
          items={[
            // both views, whatever the hub's setting says (DESIGN.md §3.19)
            { label: 'Open in Code view', icon: 'code', onSelect: () => openCode(p) },
            { label: 'Open in Board view', icon: 'grid', onSelect: () => openBoard(p) },
            'sep',
            {
              label: 'Rename',
              icon: 'pencil',
              onSelect: () => {
                live.current = true;
                draft.reset();
                setRenaming(true);
              },
            },
            { label: 'Group and tags…', icon: 'tag', onSelect: () => setOrganizing(true) },
            { label: 'Badge…', icon: 'bookmark', onSelect: () => setBadgeOpen(true) },
            { label: 'Parts…', icon: 'layers', onSelect: () => setPartsOpen(true) },
            { label: 'Cover & icon…', icon: 'image', onSelect: () => setArtOpen(true) },
            ...(select ? [{ label: 'Select', icon: 'selectAll' as const, onSelect: () => select.start(p.id) }] : []),
            'sep',
            { label: 'Remove project…', icon: 'trash', danger: true, onSelect: () => setConfirm(true) },
          ]}
        />
      </header>

      {multi ? <PartsGitLine git={partsGit} noState={!row} /> : <GitLine git={git} offline={offline} noState={!row} />}

      <footer className="project-foot">
        <span className="project-activity">
          {activeCount > 0 ? (
            <span className="status">
              <span className="dot dot-busy" aria-hidden="true" />
              {terminalsText(p, activeCount, idleCount)}
            </span>
          ) : (
            <span className="status">
              <span className="dot dot-grey" aria-hidden="true" />
              {terminalsText(p, 0, idleCount)}
            </span>
          )}
          {wsCount > 1 && <span className="project-boards">{wsCount} boards</span>}
        </span>
        {!row && footState && <GitStateMark {...footState} />}
        <span className="project-time tabular" title={`Opened ${formatDateTime(p.lastOpenedAt)}`}>
          {active && <span className="project-last">Last opened · </span>}
          {timeAgoShort(p.lastOpenedAt)}
        </span>
      </footer>

      {organizing && <OrganizeDialog project={p} onClose={() => setOrganizing(false)} />}
      {partsOpen && <PartsDialog project={p} onClose={() => setPartsOpen(false)} />}
      {artOpen && <ArtDialog project={p} onClose={() => setArtOpen(false)} />}
      {badgeOpen && <BadgeDialog project={p} onClose={() => setBadgeOpen(false)} />}
      {confirm && <RemoveDialog projects={[p]} onClose={() => setConfirm(false)} />}
    </article>
  );
}
