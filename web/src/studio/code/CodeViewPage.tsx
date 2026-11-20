// The Code view (route /p/<project>/code, DESIGN.md §3.19): the project's code editor full screen,
// with no app header, no board, no rails and no command bar. It is the same workbench as the
// project's Root editor cards, with the same tabs and live editor state (plan decision 3a): the
// card's nodeId is "", which CodeStudio takes as no card, the Root placement.
//
// The editor spawns its terminals and agents into a board and reads it (wires, orchestrator
// cards), so the page binds one: the project's board opened last in this browser, else its first,
// with a board controller and a UI store and no canvas drawn. BoardEnvContext gives the same
// binding to what the editor's tool tabs show (toolEnv.ts).
//
// Above the workbench, a top bar: the project switcher (its menu opens the picked project's Code
// view), Board | Code, quick open, the usage, consoles and Live pills, and ⋯ (Machines, Devices,
// Memory, Settings, theme, About this hub, Log out). A phone keeps the switcher, Board | Code and
// ⋯; the bottom navigation is the workbench's (CodePhone.tsx).

import { useCallback, useEffect, useMemo, useState } from 'react';
import { ReactFlowProvider } from '@xyflow/react';
import '../../styles/orch.css';
import '../../styles/phase7.css';
import '../../styles/tiles.css';
import '../../styles/codeview.css';
import { api, errorMessage } from '../../api';
import { BoardController, rectOf } from '../../board/controller';
import { boundsOf } from '../../board/geometry';
import { BoardEnvContext, UiStore, type BoardEnv } from '../../board/ui';
import { BrandMark, ConnStatus } from '../../components/AppHeader';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { LiveChip } from '../../components/LiveChip';
import { MoreMenu } from '../../components/MoreMenu';
import { NeedsYouChip } from '../../components/NeedsYouChip';
import { EditorProjectSwitch } from '../../components/ProjectBar';
import { openBoard } from '../../home/OpenChoice';
import { pageTitle } from '../../lib/instance';
import { boardOf, codePath, lastWorkspace, Link, navigate } from '../../lib/router';
import type { Project, Workspace as Board } from '../../lib/types';
import { store, useProject, useProjects, useProjectsReady } from '../../store';
import { UsagePill } from '../../usage/UsagePill';
import CodeStudio, { type PageBar } from './CodeStudio';
import DiagramLander from './tools/DiagramLander';
import { useWs } from './useWs';

/** Agent CLIs run on This PC: their usage is read there (as in the app header). */
const USAGE_MACHINE = 'local';

export default function CodeViewPage({ projectId }: { projectId: string }) {
  const project = useProject(projectId);
  const ready = useProjectsReady();
  const projects = useProjects();
  const [error, setError] = useState('');

  // Seed from REST when the events snapshot hasn't delivered projects (yet), as the board does.
  useEffect(() => {
    if (!store.getState().projectsReady) {
      api
        .projects()
        .then((list) => store.seedProjects(list))
        .catch((err) => setError(errorMessage(err)));
    }
    if (!store.getState().ready) {
      api
        .sessions()
        .then((list) => store.seedSessions(list))
        .catch(() => undefined);
    }
  }, []);

  // Opening a project bumps it to the top of Home, in either view.
  useEffect(() => {
    api
      .updateProject(projectId, { opened: true })
      .then((p) => store.upsertProject(p))
      .catch(() => undefined);
  }, [projectId]);

  // a project merged into another: its old link opens the Code view there
  useEffect(() => {
    if (project || !ready) return;
    const into = projects.find((p) => p.aliases?.includes(projectId));
    if (into) navigate(codePath(into.id), true);
  }, [project, ready, projects, projectId]);

  useEffect(() => {
    document.title = pageTitle(project ? `Code · ${project.name}` : undefined);
    return () => {
      document.title = pageTitle();
    };
  }, [project]);

  // the board is pinned once bound: a board opened in another tab (lastws) does not move the editor,
  // with its terminals, tool tabs and drafts, to another board; only a board that goes does
  const [pinned, setPinned] = useState('');
  const board = project ? boardOf(project.workspaces, pinned, lastWorkspace(project.id)) : undefined;
  useEffect(() => {
    if (board && board.id !== pinned) setPinned(board.id);
  }, [board, pinned]);

  let body;
  if (project && board) {
    body = <CodeViewBinding key={board.id} project={project} board={board} />;
  } else if ((ready && !project) || error) {
    body = (
      <div className="board-status card" role="alert">
        <div className="empty-state">
          <span className="empty-icon">
            <Icon name="alertCircle" />
          </span>
          <h3>{error ? 'Could not load projects' : 'Project not found'}</h3>
          <p>{error || 'It may have been removed on another device.'}</p>
          <Button variant="primary" icon="home" onClick={() => navigate('/')}>
            Back to projects
          </Button>
        </div>
      </div>
    );
  } else {
    body = <CodeViewLoading text="Loading project…" />;
  }
  return <div className="page page-code">{body}</div>;
}

function CodeViewLoading({ text }: { text: string }) {
  return (
    <div className="board-status status" role="status">
      <span className="spinner" aria-hidden="true" />
      {text}
    </div>
  );
}

/** The board the editor works in: a controller and a UI store, as a board page has, with no canvas. */
function CodeViewBinding({ project, board }: { project: Project; board: Board }) {
  const [ctl, setCtl] = useState<BoardController | null>(null);
  useEffect(() => {
    const c = new BoardController(board.id, project);
    setCtl(c);
    void c.sync.load();
    const off = store.onEvent((ev) => {
      if (ev.type === 'boardOps' && ev.workspaceId === board.id) c.sync.receive(ev);
      else if (ev.type === 'snapshot') void c.sync.load(); // reconnected: we may have missed ops
    });
    return () => {
      off();
      c.dispose();
    };
    // the project object changes often (counts); the controller only needs the latest one
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [board.id]);
  if (ctl) ctl.project = project;
  const [ui] = useState(() => new UiStore());

  // with no viewport, what lands on the board goes right of what is on it
  const viewCenter = useCallback(() => {
    const box = ctl ? boundsOf(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf)) : null;
    return box ? { x: box.x + box.w + 400, y: box.y + 240 } : { x: 400, y: 300 };
  }, [ctl]);
  const env = useMemo<BoardEnv | null>(
    () => (ctl ? { ctl, ui, project, viewCenter, frame: () => undefined, reveal: () => undefined, cardMenu: () => [] } : null),
    [ctl, ui, project, viewCenter],
  );
  const topBar = useCallback((bar: PageBar) => <CodeViewBar project={project} bar={bar} />, [project]);

  if (!ctl || !env) return <CodeViewLoading text="Loading the editor…" />;
  return (
    <ReactFlowProvider>
      <BoardEnvContext.Provider value={env}>
        <CodeStudio project={project} ctl={ctl} ui={ui} nodeId="" viewCenter={viewCenter} filled={false} mode="page" topBar={topBar} />
        {/* diagrams drawn from the Diagrams tab land on this board, the tab open or not (lane C) */}
        <DiagramLander ctl={ctl} viewCenter={viewCenter} />
      </BoardEnvContext.Provider>
    </ReactFlowProvider>
  );
}

/** Board | Code: the board is the one opened last; this page is Code. */
function ViewSwitch({ project }: { project: Project }) {
  return (
    <div className="segmented sm cv-views" role="group" aria-label="View">
      <button type="button" className="segmented-item" title="Open the board, the one opened last" onClick={() => openBoard(project)}>
        <Icon name="grid" />
        <span className="cv-views-label">Board</span>
      </button>
      <span className="segmented-item" aria-current="page" title="Code view">
        <Icon name="code" />
        <span className="cv-views-label">Code</span>
      </span>
    </div>
  );
}

/** The Git branch of the active file's part, with what is not pushed or pulled; opens Source Control. */
function BranchChip({ ws, onOpen }: { ws: PageBar['ws']; onOpen: () => void }) {
  const st = ws.activePart.git;
  if (!st?.isRepo) return null;
  return (
    <button type="button" className="cv-chip cv-branch" onClick={onOpen} title={`Branch ${st.branch || 'detached'}. Open Source Control`}>
      <Icon name="branch" />
      <span className="truncate">{st.branch || 'detached'}</span>
      {!!(st.ahead || st.behind) && (
        <span className="cv-chip-sub tabular">
          {st.ahead ? `↑${st.ahead}` : ''}
          {st.behind ? `↓${st.behind}` : ''}
        </span>
      )}
      <Icon name="chevronDown" className="chev" />
    </button>
  );
}

/** Run: a task of the project (the picker), Run again, and Start debugging. */
function RunControls({ bar }: { bar: PageBar }) {
  const { ws, run } = bar;
  const last = ws.taskSelected ? ws.taskSelected.split('\u0001').pop() : '';
  return (
    <span className="cv-run" role="group" aria-label="Run">
      <button type="button" className="cv-chip" onClick={() => run('tasks.run')} title="Run a task of the project">
        <Icon name="checklist" />
        <span className="truncate">{last || 'Run task'}</span>
        <Icon name="chevronDown" className="chev" />
      </button>
      <span className="cv-run-sep" aria-hidden="true" />
      <Button size="sm" variant="ghost" iconOnly icon="play" className="cv-play" aria-label="Run without debugging (Ctrl+F5)" title="Run without debugging · Ctrl+F5" onClick={() => run('debug.run')} />
      <Button size="sm" variant="ghost" iconOnly icon="bug" className="cv-debug" aria-label="Start debugging (F5)" title="Start debugging · F5" onClick={() => run('debug.start')} />
    </span>
  );
}

/** The Code view's top bar, over the workbench: on the page's backdrop, as in the islands design. */
function CodeViewBar({ project, bar }: { project: Project; bar: PageBar }) {
  const { ws, quickOpen, phone } = bar;
  useWs(ws);
  const dirty = [...ws.docs.values()].filter((d) => d.dirty).length;
  return (
    <header className={`cv-bar ${phone ? 'is-phone' : ''}`}>
      {!phone && (
        <Link to="/" className="cv-brand" aria-label="All projects" title="All projects">
          <BrandMark />
        </Link>
      )}
      <EditorProjectSwitch project={project} target="code" className="cv-switch" />
      {!phone && <BranchChip ws={ws} onOpen={() => bar.view('scm')} />}
      <ViewSwitch project={project} />
      <span className="spacer" />
      {!phone && (
        <button type="button" className="wb-command-center cv-quick" onClick={() => quickOpen('')} title="Go to file · Ctrl+P">
          <Icon name="search" />
          <span className="truncate">Go to file, or &gt; for commands</span>
          <kbd>Ctrl P</kbd>
        </button>
      )}
      <span className="spacer" />
      {!phone && <RunControls bar={bar} />}
      {!phone && dirty > 0 && (
        <Button size="sm" variant="primary" icon="save" onClick={() => void ws.saveAll()} title="Save all · Ctrl+Alt+S">
          Save {dirty}
        </Button>
      )}
      {!phone && (
        <span className="cv-pills">
          <UsagePill machineId={USAGE_MACHINE} />
          <NeedsYouChip />
          <LiveChip />
          <ConnStatus />
        </span>
      )}
      {!phone && (
        <>
          <Button size="sm" variant="ghost" iconOnly icon="search" className="cv-icon" aria-label="Search in files (Ctrl+Shift+F)" title="Search in files · Ctrl+Shift+F" onClick={() => bar.view('search')} />
          <Button size="sm" variant="ghost" iconOnly icon="settings" className="cv-icon" aria-label="Settings" title="NextBoard settings" onClick={() => navigate('/settings')} />
        </>
      )}
      <MoreMenu project={project} avatar={!phone} />
    </header>
  );
}
