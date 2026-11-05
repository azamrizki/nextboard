// The ring's Spawn agent: pick the agent, its model and effort start from the defaults in settings,
// and Spawn places it. A double click on a tile spawns it as it is.

import { type FormEvent } from 'react';
import { AgentBadge, shortAgentName } from '../agents/AgentBadge';
import { ModelPicker } from '../agents/ModelPicker';
import { Button } from '../components/Button';
import type { Agent } from '../lib/types';
import type { useNewTerminal } from './NewTerminal';

interface Props {
  agents: Agent[];
  nt: ReturnType<typeof useNewTerminal>;
  onSpawn: (agent: Agent) => void;
}

export function SpawnPanel({ agents, nt, onSpawn }: Props) {
  if (!agents.length) return <p className="ring-empty">No agent is installed on this machine yet.</p>;
  const sel = agents.find((a) => a.id === nt.agentId) ?? agents[0];
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSpawn(sel);
  };
  return (
    <form className="spawn-panel" onSubmit={submit}>
      <div className="ring-grid nowheel" role="radiogroup" aria-label="Agents">
        {agents.map((a) => (
          <button
            key={a.id}
            type="button"
            role="radio"
            aria-checked={a.id === sel.id}
            className={`ring-tile ${a.id === sel.id ? 'is-active' : ''}`}
            title={a.name}
            onClick={() => nt.setAgentId(a.id)}
            onDoubleClick={() => onSpawn(a)}
          >
            <span className="ring-tile-mark">
              <AgentBadge agent={a} />
            </span>
            <span className="ring-tile-name truncate">{shortAgentName(a.name)}</span>
          </button>
        ))}
      </div>
      <ModelPicker agent={sel} value={nt.modelChoice} onChange={nt.setModelChoice} profileId={nt.profileId} />
      <Button type="submit" size="sm" variant="primary" icon="play">
        Spawn {shortAgentName(sel.name)}
      </Button>
    </form>
  );
}
