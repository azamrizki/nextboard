// The ring's Spawn agent: pick the agent and the login profile it runs under, its model and effort
// start from the defaults in settings, and Spawn places it. A double click on a tile spawns it as
// it is. The profile starts at the default one (NextBoard unless settings say otherwise) every time
// the panel opens or the agent changes, whatever another form picked last.

import { useEffect, useState, type FormEvent } from 'react';
import { AgentBadge, shortAgentName } from '../agents/AgentBadge';
import { ModelPicker } from '../agents/ModelPicker';
import { defaultLoginLabel } from '../agents/settings';
import { Button } from '../components/Button';
import { SelectField } from '../components/form';
import type { Agent } from '../lib/types';
import type { useNewTerminal } from './NewTerminal';

interface Props {
  agents: Agent[];
  nt: ReturnType<typeof useNewTerminal>;
  /** profileId "" is the default profile. */
  onSpawn: (agent: Agent, profileId: string) => void;
}

export function SpawnPanel({ agents, nt, onSpawn }: Props) {
  const sel = agents.find((a) => a.id === nt.agentId) ?? agents[0];
  const [profileId, setProfileId] = useState('');
  useEffect(() => setProfileId(''), [sel?.id]);
  if (!sel) return <p className="ring-empty">No agent is installed on this machine yet.</p>;
  // nt lists the profiles of the agent it has armed, which is the selected tile
  const profiles = sel.id === nt.agentId ? nt.profileOptions : [];
  const chosen = profiles.some((p) => p.id === profileId) ? profileId : '';
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSpawn(sel, chosen);
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
            onDoubleClick={() => onSpawn(a, a.id === sel.id ? chosen : '')}
          >
            <span className="ring-tile-mark">
              <AgentBadge agent={a} />
            </span>
            <span className="ring-tile-name truncate">{shortAgentName(a.name)}</span>
          </button>
        ))}
      </div>
      {profiles.length > 0 && (
        <SelectField label="Login profile" value={chosen} onChange={(e) => setProfileId(e.target.value)} className="control-sm">
          <option value="">{defaultLoginLabel(profiles, nt.defaultProfile)}</option>
          {profiles.filter((p) => p.id !== nt.defaultProfile).map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </SelectField>
      )}
      <ModelPicker agent={sel} value={nt.modelChoice} onChange={nt.setModelChoice} profileId={chosen} />
      <Button type="submit" size="sm" variant="primary" icon="play">
        Spawn {shortAgentName(sel.name)}
      </Button>
    </form>
  );
}
