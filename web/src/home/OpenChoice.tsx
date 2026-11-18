// Opening a project (DESIGN.md §3.19): the hub's ui.openProjectsIn decides where it goes. "board"
// and "code" go there; "" (the default) asks with a modal, "Open <project>", whose "Remember my
// choice for every project" writes the setting, so every browser follows it. A project card's menu
// always offers both views, whatever the setting says.
//
// Asking happens for a Home card, the header's project menu, and a link to a project that names
// no view (/p/<id>, typed or opened from outside: lib/router.tsx isEntryLink). The app's own ways
// to a board (a console's jump, a workspace tab, a merge) keep going to the board.

import { useEffect, useState, useSyncExternalStore, type KeyboardEvent as ReactKeyboardEvent } from 'react';
import { api, errorMessage } from '../api';
import { loadSettings, patchSettings, settingsSupported } from '../agents/settings';
import { warmProject } from '../agents/warm';
import { Button } from '../components/Button';
import { Checkbox } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { boardOf, boardPath, codePath, lastWorkspace, navigate, takeEntryLink } from '../lib/router';
import type { OpenProjectsIn, Project } from '../lib/types';
import { store, useProject, useProjectsReady, useSettings } from '../store';
import '../styles/codeview.css';

type View = 'board' | 'code';

/** The board a project opens on: the one opened last in this browser, else its first. */
export function lastBoardOf(p: Project) {
  return boardOf(p.workspaces, null, lastWorkspace(p.id));
}

/** The project's board, the last one opened. */
export function openBoard(p: Project, replace = false) {
  const ws = lastBoardOf(p);
  warmProject(p.id, ws?.id); // its agents start while the board loads
  navigate(boardPath(p.id, ws?.id), replace);
}

/** The project's Code view. */
export function openCode(p: Project, replace = false) {
  navigate(codePath(p.id), replace);
}

export function openIn(p: Project, view: View, replace = false) {
  if (view === 'code') openCode(p, replace);
  else openBoard(p, replace);
}

/** The hub's answer; a hub without settings opens boards as before. */
function setting(): OpenProjectsIn {
  const v = store.getState().settings?.ui?.openProjectsIn;
  if (v === 'board' || v === 'code') return v;
  return settingsSupported() === false ? 'board' : '';
}

export function useOpenProjectsIn(): OpenProjectsIn {
  const s = useSettings();
  const v = s?.ui?.openProjectsIn;
  return v === 'board' || v === 'code' ? v : '';
}

// the project being asked about, for the one dialog of the app (OpenChoiceHost)
let asking: Project | null = null;
const listeners = new Set<() => void>();
function setAsking(p: Project | null) {
  asking = p;
  listeners.forEach((l) => l());
}
const subscribe = (fn: () => void) => {
  listeners.add(fn);
  return () => listeners.delete(fn);
};

/** Open a project the way the hub's setting says, or ask. */
export function openProject(p: Project) {
  const v = setting();
  if (v) openIn(p, v);
  else setAsking(p);
}

/** Small pictures of the two views, in the theme's colours. */
function Picture({ view }: { view: View }) {
  return view === 'board' ? (
    <svg className="oc-pic" viewBox="0 0 300 150" aria-hidden="true">
      <rect className="oc-pic-bg" x="0.5" y="0.5" width="299" height="149" rx="8" />
      <rect className="oc-pic-card" x="14" y="18" width="120" height="78" rx="6" />
      <rect className="oc-pic-card" x="150" y="30" width="130" height="90" rx="6" />
      <rect className="oc-pic-dash" x="40" y="108" width="90" height="28" rx="5" />
      <rect className="oc-pic-bar" x="80" y="126" width="170" height="12" rx="6" />
    </svg>
  ) : (
    <svg className="oc-pic" viewBox="0 0 300 150" aria-hidden="true">
      <rect className="oc-pic-bg" x="0.5" y="0.5" width="299" height="149" rx="8" />
      <rect className="oc-pic-side" x="1" y="1" width="16" height="148" />
      <rect className="oc-pic-panel" x="17" y="1" width="60" height="148" />
      <rect className="oc-pic-line" x="86" y="14" width="110" height="5" rx="2.5" />
      <rect className="oc-pic-line" x="86" y="26" width="150" height="5" rx="2.5" />
      <rect className="oc-pic-line" x="86" y="38" width="90" height="5" rx="2.5" />
      <rect className="oc-pic-term" x="78" y="97" width="160" height="1.5" />
      <rect className="oc-pic-panel" x="238" y="1" width="61" height="148" />
    </svg>
  );
}

const VIEWS: { view: View; title: string; text: string }[] = [
  { view: 'board', title: 'Board view', text: 'Consoles, agents, cards and the lead on one canvas. As today.' },
  { view: 'code', title: 'Code view', text: 'A full code editor: files, terminals, agents and the lead in one window, no board.' },
];

/**
 * "Open <project>": Board view or Code view. replace: the page that asked is a link being
 * answered, so the view takes its place in the history.
 */
export function OpenChoiceDialog({ project, onClose, replace = false }: { project: Project; onClose: () => void; replace?: boolean }) {
  const [view, setView] = useState<View>('board');
  // ticked, as the owner approved it (mockup 1): one answer for every project, changed in Settings
  const [remember, setRemember] = useState(true);
  const [busy, setBusy] = useState(false);

  const open = async (v: View) => {
    if (busy) return;
    if (remember) {
      setBusy(true);
      try {
        await patchSettings({ ui: { openProjectsIn: v } });
      } catch (err) {
        toast.error(`Could not remember the choice: ${errorMessage(err)}`);
      }
    }
    openIn(project, v, replace);
    onClose();
  };

  const onKey = (e: ReactKeyboardEvent, i: number) => {
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown' || e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
      e.preventDefault();
      const next = VIEWS[(i + (e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : VIEWS.length - 1)) % VIEWS.length].view;
      setView(next);
      (e.currentTarget.parentElement?.querySelector(`[data-view="${next}"]`) as HTMLElement | null)?.focus();
    }
  };

  return (
    <Modal
      title={`Open “${project.name}”`}
      icon="folder"
      size="lg"
      className="open-choice"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="open-choice-form" disabled={busy}>
            {view === 'code' ? 'Open in Code view' : 'Open in Board view'}
          </Button>
        </>
      }
    >
      <form
        id="open-choice-form"
        className="oc-form"
        onSubmit={(e) => {
          e.preventDefault();
          void open(view);
        }}
      >
        <p className="text-2" id="open-choice-q">
          How do you want to work on this project?
        </p>
        <div className="oc-views" role="radiogroup" aria-labelledby="open-choice-q">
          {VIEWS.map((o, i) => (
            <button
              key={o.view}
              type="button"
              role="radio"
              data-view={o.view}
              aria-checked={view === o.view}
              tabIndex={view === o.view ? 0 : -1}
              className="oc-view"
              onClick={() => setView(o.view)}
              onDoubleClick={() => void open(o.view)}
              onKeyDown={(e) => onKey(e, i)}
            >
              <Picture view={o.view} />
              <span className="oc-view-head">
                <Icon name={o.view === 'code' ? 'code' : 'grid'} />
                <span className="oc-view-title">{o.title}</span>
                <span className="oc-view-check" aria-hidden="true">
                  {view === o.view && <Icon name="check" />}
                </span>
              </span>
              <span className="oc-view-text">{o.text}</span>
            </button>
          ))}
        </div>
        <div className="oc-remember">
          <Checkbox checked={remember} onChange={setRemember} label="Remember my choice for every project" />
          <p className="oc-remember-hint">Change it any time in Settings › General › Open projects in. A project card’s menu always offers the other view.</p>
        </div>
      </form>
    </Modal>
  );
}

/** The app's one "Open <project>" dialog, for openProject() from anywhere. */
export function OpenChoiceHost() {
  const project = useSyncExternalStore(subscribe, () => asking);
  // gone meanwhile (removed in another tab): nothing to open
  const live = useProject(project?.id ?? '');
  if (!project) return null;
  return <OpenChoiceDialog key={project.id} project={live ?? project} onClose={() => setAsking(null)} />;
}

/**
 * A link to a project with no view (/p/<id>), the page this tab opened on: the setting's view, or
 * the question. A project not found, or a hub without the setting, goes on to the board page,
 * which says so or opens the board as before: answering the link (takeEntryLink) makes App render
 * the board page in this one's place, mounted once.
 */
export function EntryChoice({ projectId }: { projectId: string }) {
  const project = useProject(projectId);
  const ready = useProjectsReady();
  const settings = useSettings();
  const [checked, setChecked] = useState(() => settings !== null || settingsSupported() !== null);
  const [decided, setDecided] = useState<'ask' | null>(null);

  useEffect(() => {
    if (!store.getState().projectsReady) {
      api
        .projects()
        .then((list) => store.seedProjects(list))
        .catch(() => takeEntryLink());
    }
  }, []);
  useEffect(() => {
    if (!checked) void loadSettings().finally(() => setChecked(true));
  }, [checked]);

  useEffect(() => {
    if (decided) return;
    if (ready && !project) {
      takeEntryLink();
      return;
    }
    if (!project || !(checked || settings)) return;
    const v = setting();
    if (v) {
      // the view first, then the link answered: App renders that view's page, once
      openIn(project, v, true);
      takeEntryLink();
    } else setDecided('ask');
  }, [decided, ready, project, checked, settings]);

  return (
    <>
      <div className="center-msg full" role="status">
        <span className="spinner" aria-hidden="true" />
        Opening {project?.name ?? 'the project'}…
      </div>
      {decided === 'ask' && project && (
        <OpenChoiceDialog
          project={project}
          replace
          onClose={() => {
            takeEntryLink();
            // the dialog opened a view, or was cancelled: then Home
            if (window.location.pathname === `/p/${encodeURIComponent(project.id)}`) navigate('/', true);
          }}
        />
      )}
    </>
  );
}
