// Thin bar under a console header (docs/API-agents.md §17): "Agent exited (code N) —
// Restart · Resume · Pick conversation" while the console is at the shell, and the
// "Resume conversation?" offer after a hub restart (resumeOnRestart: ask).

import { useState } from 'react';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import type { Agent, Session } from '../lib/types';
import { canContinue, canPickInCli, canResumeExact, hasTranscript } from './catalog';
import { isAgentConsole } from './state';

interface AgentBarProps {
  session: Session;
  agent: Agent | undefined;
  onRestart: () => void;
  onResume: () => void;
  onPick: () => void;
}

/** Dismissed bars per session + exit time, so a later exit shows the bar again. */
const dismissed = new Set<string>();

export function AgentBar({ session, agent, onRestart, onResume, onPick }: AgentBarProps) {
  const key = `${session.id}@${session.stateSince ?? ''}`;
  const [hidden, setHidden] = useState(() => dismissed.has(key));
  if (session.status !== 'running' || session.agentState !== 'shell' || !isAgentConsole(session) || session.manualAgent) return null;
  if (hidden && dismissed.has(key)) return null;

  const name = agent?.name ?? session.name;
  const resumable = canResumeExact(agent) || canContinue(agent);
  const pickable = hasTranscript(agent) || canPickInCli(agent);
  const dismiss = () => {
    dismissed.add(key);
    setHidden(true);
  };

  if (session.resumeOffer) {
    return (
      <div className="agent-bar is-offer nodrag" role="region" aria-label="Resume offer">
        <Icon name="resume" />
        <span className="agent-bar-text truncate">Resume the {name} conversation from before the hub restarted?</span>
        <Button size="sm" variant="primary" onClick={onResume}>
          Resume
        </Button>
        <Button size="sm" variant="ghost" onClick={dismiss}>
          Not now
        </Button>
      </div>
    );
  }

  const code = session.agentExitCode;
  const missing = code === 127;
  const text =
    code === null || code === undefined
      ? `${name} is not running — this console is at the shell.`
      : missing
        ? `${name} was not found (code 127). Install it or set its path in Settings › Agents.`
        : `${name} exited (code ${code}).`;

  return (
    <div className={`agent-bar nodrag ${code ? 'is-error' : ''}`} role="region" aria-label="Agent exited">
      <Icon name={code ? 'alertCircle' : 'terminal'} />
      <span className="agent-bar-text truncate" title={text}>
        {text}
      </span>
      <span className="agent-bar-actions">
        <Button size="sm" variant="ghost" icon="restart" onClick={onRestart}>
          Restart
        </Button>
        {resumable && (
          <Button size="sm" variant="ghost" icon="resume" onClick={onResume}>
            Resume
          </Button>
        )}
        {pickable && (
          <Button size="sm" variant="ghost" icon="history" onClick={onPick}>
            Pick conversation
          </Button>
        )}
      </span>
      <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Hide this bar" onClick={dismiss} />
    </div>
  );
}
