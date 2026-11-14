// Project board route: /p/:projectId/w/:workspaceId. Resolves the project and tab from
// the live store (seeded over REST), remembers the last tab per project, and renders one
// BoardView per workspace (switching tabs leaves every terminal running).

import { useEffect, useMemo, useRef, useState } from 'react';
import { ReactFlowProvider } from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import '@fontsource/caveat/400.css';
import '../styles/board.css';
import '../styles/tiles.css';
import '../styles/orch.css';
import '../styles/phase7.css';
import { warmProject } from '../agents/warm';
import { api, errorMessage } from '../api';
import { BoardView } from '../board/BoardView';
import { AppHeader } from '../components/AppHeader';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { prefs } from '../lib/format';
import { boardPath, lastWorkspace, navigate, takeSearchParam } from '../lib/router';
import { store, useProject, useProjects, useProjectsReady } from '../store';

export function BoardPage({ projectId, workspaceId }: { projectId: string; workspaceId: string }) {
  const project = useProject(projectId);
  const ready = useProjectsReady();
  const [error, setError] = useState('');
  // ?focus=<session> (live chip, needs-you jump): consumed once per workspace — switching
  // tabs within a project keeps this page mounted
  const focusRef = useRef<{ wid: string; sid: string | null } | null>(null);
  if (!focusRef.current || focusRef.current.wid !== workspaceId) focusRef.current = { wid: workspaceId, sid: takeSearchParam('focus') };
  const focus = focusRef.current.sid;

  // Seed from REST when the events snapshot hasn't delivered projects (yet).
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

  // Its asleep agents start now, while the board loads, not when their terminals connect
  // (ROADMAP #50); opening from Home or a hover has usually asked already.
  useEffect(() => {
    if (workspaceId) warmProject(projectId, workspaceId);
  }, [projectId, workspaceId]);

  // Opening a project bumps it to the top of Home.
  useEffect(() => {
    api
      .updateProject(projectId, { opened: true })
      .then((p) => store.upsertProject(p))
      .catch(() => undefined);
  }, [projectId]);

  // a project merged into another (ROADMAP #49): its old link opens the same board there
  const projects = useProjects();
  useEffect(() => {
    if (project || !ready) return;
    const into = projects.find((p) => p.aliases?.includes(projectId));
    if (into) navigate(boardPath(into.id, workspaceId) + window.location.search, true);
  }, [project, ready, projects, projectId, workspaceId]);

  const workspaces = useMemo(() => project?.workspaces ?? [], [project]);
  const ws = workspaces.find((w) => w.id === workspaceId);

  useEffect(() => {
    if (!project || ws || workspaces.length === 0) return;
    const last = lastWorkspace(project.id);
    const target = workspaces.find((w) => w.id === last) ?? workspaces[0];
    // a link's query (?card=…) is for the board it lands on
    navigate(boardPath(project.id, target.id) + window.location.search, true);
  }, [project, ws, workspaces]);

  useEffect(() => {
    if (ws) prefs.set(`lastws.${projectId}`, ws.id);
  }, [projectId, ws]);

  useEffect(() => {
    document.title = project ? `${ws ? `${ws.name} · ` : ''}${project.name} — NextBoard` : 'NextBoard';
    return () => {
      document.title = 'NextBoard';
    };
  }, [project, ws]);

  let body;
  if (ws && project) {
    body = <BoardView key={ws.id} project={project} workspace={ws} focusSession={focus} />;
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
    body = (
      <div className="board-status status" role="status">
        <span className="spinner" aria-hidden="true" />
        Loading project…
      </div>
    );
  }

  return (
    <div className="page page-board">
      <ReactFlowProvider>{body}</ReactFlowProvider>
      <AppHeader project={project} workspaceId={ws?.id} />
    </div>
  );
}
