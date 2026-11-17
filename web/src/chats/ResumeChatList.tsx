// "Resume a chat" (mockup 4): the chats of one place, those of one agent first picked, with Show
// for a live one and Resume for a closed one. A chat resumes only where it started, so the list
// follows the place: the New console form's tab and the editor panes' "Resume a chat…" use it.

import { useMemo, useState } from 'react';
import { agentById, useAgentCatalog } from '../agents/catalog';
import { PlacePicker } from '../board/PlacePicker';
import { Icon } from '../components/Icon';
import { projectParts } from '../lib/parts';
import { isMultiPart, ROOT_VALUE } from '../lib/placement';
import type { Project } from '../lib/types';
import { useStoreSessions } from '../orch/hooks';
import { ChatGroups, useChatDialogs } from './ChatRows';
import { filterChats } from './helpers';
import { useProjectChats } from './store';
import type { ChatView } from './types';
import '../styles/chats.css';

interface Props {
  project: Project;
  workspaceId: string;
  /** placeValue of the place (ROOT_VALUE or a part id); "" in a single-part project: every chat. */
  place: string;
  /** Pick another place (a Root editor card, the New console form); absent: the place is fixed. */
  onPlace?: (v: string) => void;
  /** The agent picked for a new console: its chats are listed ("" = every agent). */
  agent: string;
  onResume: (c: ChatView) => void;
  /** After Show / Wake took the user to the console. */
  onShown?: () => void;
}

export function ResumeChatList({ project, workspaceId, place, onPlace, agent: initialAgent, onResume, onShown }: Props) {
  const sessions = useStoreSessions();
  const { items, status, error } = useProjectChats(project.id);
  const [q, setQ] = useState('');
  const [agent, setAgent] = useState(initialAgent);
  const dialogs = useChatDialogs(project, workspaceId, onResume, onShown);
  const multi = isMultiPart(project);
  const where = multi ? place || ROOT_VALUE : '';
  const here = useMemo(() => filterChats(items, { place: where }), [items, where]);
  const catalog = useAgentCatalog();
  const agents = useMemo(() => [...new Set(here.map((c) => c.agent))].map((id) => ({ id, name: agentById(id)?.name ?? id })).sort((a, b) => a.name.localeCompare(b.name)), [here, catalog.agents]); // eslint-disable-line react-hooks/exhaustive-deps
  // the agent picked has no chat here: the others are still worth showing
  const byAgent = agent && agents.some((a) => a.id === agent) ? agent : '';
  const shown = filterChats(here, { q, agent: byAgent });
  const placeName = !multi ? project.name : where === ROOT_VALUE ? 'Root' : (projectParts(project).find((p) => p.id === where)?.name ?? 'this part');

  return (
    <div className="resume-list">
      {multi && (onPlace ? <PlacePicker project={project} value={where} onChange={onPlace} /> : null)}
      <div className="chats-filters">
        <div className="input-group has-lead grow">
          <span className="ig-lead">
            <Icon name="search" />
          </span>
          <input className="control control-sm" type="search" placeholder={`Search chats in ${placeName}…`} aria-label={`Search chats in ${placeName}`} value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <select className="control control-sm chats-agent" aria-label="Agent" value={byAgent} onChange={(e) => setAgent(e.target.value)}>
          <option value="">All agents</option>
          {agents.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
      </div>
      <div className="resume-list-box">
        {status === 'loading' && !items.length && (
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Loading chats…
          </div>
        )}
        {status === 'unsupported' && <div className="list-msg">This hub does not keep chats yet. Update NextBoard on the hub.</div>}
        {status === 'error' && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>Could not load the chats: {error}</span>
          </div>
        )}
        {status === 'ready' && !shown.length && <div className="list-msg">{here.length ? `No chat matches${q.trim() ? ` “${q.trim()}”` : ''}.` : `No chat was started in ${placeName} yet.`}</div>}
        {shown.length > 0 && <ChatGroups chats={shown} project={project} sessions={sessions} handlers={dialogs.handlers} compact showPlace={false} label={`Chats in ${placeName}`} />}
      </div>
      {dialogs.node}
    </div>
  );
}
