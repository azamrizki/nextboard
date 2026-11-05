// One project on Home: a bento card, or a row of the list view (same markup, DESIGN.md
// §3.11). Monogram, name and folder; git state on one line; terminals, boards and last
// opened at the foot. The whole card opens the project.

import { useEffect, useRef, useState } from 'react';
import { api, errorMessage } from '../api';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon, type IconName } from '../components/Icon';
import { Menu } from '../components/Menu';
import { NAME_MAX } from '../components/ProjectBar';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgoShort } from '../lib/format';
import { boardPath, lastWorkspace, navigate } from '../lib/router';
import type { GitInfo, Project } from '../lib/types';
import { isActive } from '../agents/state';
import { store, useRunningSessions } from '../store';
import { gitApi } from '../studio/api';
import { OrganizeDialog } from './GroupDialogs';

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
function useGit(id: string, offline: boolean) {
  const [state, setState] = useState(() => gitCache.get(id) ?? null);
  useEffect(() => {
    if (offline) return;
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
  }, [id, offline]);
  return state;
}

export function openProject(p: Project) {
  const last = lastWorkspace(p.id);
  const ws = p.workspaces?.find((w) => w.id === last) ?? p.workspaces?.[0];
  navigate(boardPath(p.id, ws?.id));
}

/** One or two capitals from the name: "demo-backoffice" → DB, "nextboard" → N. */
export function projectMonogram(name: string): string {
  const words = name.split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  const first = (w: string | undefined) => (w ? Array.from(w)[0].toUpperCase() : '');
  return first(words[0]) + first(words[1]) || '?';
}

const MONOGRAM_TONES = 6;

/** By id, so a project keeps its colour when renamed. */
function monogramTone(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return h % MONOGRAM_TONES;
}

/** The folder with its last part always visible: long paths lose their middle. */
function FolderPath({ path }: { path: string }) {
  const cut = path.replace(/\/+$/, '').lastIndexOf('/') + 1;
  return (
    <span className="project-path mono">
      <span className="path-head">{path.slice(0, cut)}</span>
      <span className="path-tail">{path.slice(cut)}</span>
    </span>
  );
}

function ChangedBadge({ changed }: { changed: number }) {
  return <span className={`badge ${changed ? 'badge-warning' : 'badge-success'}`}>{changed ? `${changed} changed` : 'Clean'}</span>;
}

function GitLine({ git, offline }: { git: GitState | null; offline: boolean }) {
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
        <ChangedBadge changed={git.nested.changed} />
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
        <ChangedBadge changed={info.changed} />
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
}

export function ProjectCard({ project: p, row, active, offline = false, place, activeTag, onTag, removeRequested, onRemoveHandled }: ProjectCardProps) {
  const git = useGit(p.id, offline);
  const consoles = useRunningSessions().filter((s) => s.projectId === p.id);
  const activeCount = consoles.filter(isActive).length;
  const idleCount = consoles.length - activeCount;
  const [renaming, setRenaming] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [organizing, setOrganizing] = useState(false);
  const live = useRef(false);

  useEffect(() => {
    if (removeRequested) {
      setConfirm(true);
      onRemoveHandled?.();
    }
  }, [removeRequested, onRemoveHandled]);

  const rename = async (name: string) => {
    if (!live.current) return;
    live.current = false;
    setRenaming(false);
    const v = name.trim().slice(0, NAME_MAX);
    if (!v || v === p.name) return;
    try {
      store.upsertProject(await api.updateProject(p.id, { name: v }));
    } catch (err) {
      toast.error(`Could not rename ${p.name}: ${errorMessage(err)}`);
    }
  };

  const remove = async () => {
    await api.deleteProject(p.id);
    store.removeProject(p.id);
    toast.success(`${p.name} removed. Its files are untouched.`);
  };

  const wsCount = p.workspaces?.length ?? 0;
  const branch = git?.info?.isRepo ? git.info.branch : '';

  return (
    <article className={`project-card ${row ? 'project-row' : 'card'} ${active ? 'is-active' : ''}`} data-project={p.id} aria-label={p.name}>
      <header className="project-head">
        <div className="project-id">
          <span className={`project-mono tone-${monogramTone(p.id)}`} aria-hidden="true">
            {projectMonogram(p.name)}
          </span>
          <div className="project-title">
            {renaming ? (
              <input
                className="control control-sm"
                defaultValue={p.name}
                autoFocus
                maxLength={NAME_MAX}
                aria-label="Project name"
                onFocus={(e) => e.currentTarget.select()}
                onBlur={(e) => void rename(e.target.value)}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  if (e.key === 'Enter') e.currentTarget.blur();
                  if (e.key === 'Escape') {
                    live.current = false;
                    setRenaming(false);
                  }
                }}
              />
            ) : (
              <h3 className="truncate">
                <button
                  type="button"
                  className="card-link project-open"
                  onClick={() => openProject(p)}
                  onDoubleClick={(e) => {
                    e.preventDefault();
                  }}
                  // the button covers the card, so this tooltip speaks for everything cut short on it
                  title={`Open ${p.name}\n${p.path}${branch ? `\nBranch ${branch}` : ''}`}
                >
                  {p.name}
                </button>
              </h3>
            )}
            <FolderPath path={p.path} />
            <ProjectMarks p={p} place={place} activeTag={activeTag} onTag={onTag} />
          </div>
        </div>
        <Menu
          className="card-menu"
          label={`Actions for ${p.name}`}
          items={[
            { label: 'Open', icon: 'folderOpen', onSelect: () => openProject(p) },
            {
              label: 'Rename',
              icon: 'pencil',
              onSelect: () => {
                live.current = true;
                setRenaming(true);
              },
            },
            { label: 'Group and tags…', icon: 'tag', onSelect: () => setOrganizing(true) },
            'sep',
            { label: 'Remove project…', icon: 'trash', danger: true, onSelect: () => setConfirm(true) },
          ]}
        />
      </header>

      <GitLine git={git} offline={offline} />

      <footer className="project-foot">
        <span className="project-activity">
          {activeCount > 0 ? (
            <span className="status">
              <span className="dot dot-busy" aria-hidden="true" />
              {terminalsText(p, activeCount, idleCount)}
            </span>
          ) : (
            <span>{terminalsText(p, 0, idleCount)}</span>
          )}
          {wsCount > 1 && <span className="project-boards">{wsCount} boards</span>}
        </span>
        <span className="project-time tabular" title={`Opened ${formatDateTime(p.lastOpenedAt)}`}>
          {active && <span className="project-last">Last opened · </span>}
          {timeAgoShort(p.lastOpenedAt)}
        </span>
      </footer>

      {organizing && <OrganizeDialog project={p} onClose={() => setOrganizing(false)} />}
      {confirm && (
        <ConfirmDialog title={`Remove “${p.name}”?`} confirmLabel="Remove project" danger onClose={() => setConfirm(false)} onConfirm={remove}>
          <p>
            {p.sessionCount > 0 ? (
              <>
                Its <strong>{p.sessionCount === 1 ? 'terminal' : `${p.sessionCount} terminals`}</strong> will end
                {p.runningCount ? ` (${p.runningCount} running)` : ''}.{' '}
              </>
            ) : null}
            Boards move to the archive and desktop apps shown only on them close. The folder <code>{p.path}</code> and its files stay on disk.
          </p>
        </ConfirmDialog>
      )}
    </article>
  );
}
