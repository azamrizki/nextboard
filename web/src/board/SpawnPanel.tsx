// The ring's Spawn agent: pick the agent and the login profile it runs under, its model and effort
// start from the defaults in settings, and Spawn places it. A double click on a tile spawns it as
// it is. The profile starts at the default one (NextBoard unless settings say otherwise) every time
// the panel opens or the agent changes, whatever another form picked last. In a project of several
// parts it asks where: Root (all parts) or a part, the focused tile's by default. NextCode is a
// tile of its own (a chat console), shown once with an Engine control for its Claude, Codex and
// API agents; its logins are only its own engine's (nt.profileOptions), and its API engine runs on
// an API provider only (Settings › NextCode's default first), so it cannot spawn without one.

import { useEffect, useState, type FormEvent } from 'react';
import { AgentBadge, shortAgentName } from '../agents/AgentBadge';
import { agentTagline } from '../agents/catalog';
import { ModelPicker } from '../agents/ModelPicker';
import { Button } from '../components/Button';
import { SelectField } from '../components/form';
import type { Agent } from '../lib/types';
import { apiProviderChoice, foldEngines, isApiEngine } from '../nextcode/engines';
import { EngineSwitch } from '../nextcode/EngineSwitch';
import { AccountOptions, accountFor, NeedsProviderHint, needsProvider } from '../providers/AccountOptions';
import { useProviders } from '../providers/providers';
import type { useNewTerminal } from './NewTerminal';
import { OTHER_VALUE, PlacePicker } from './PlacePicker';

interface Props {
  agents: Agent[];
  nt: ReturnType<typeof useNewTerminal>;
  /** account "" is the default profile; pf_… another profile; ap_… an API provider. */
  onSpawn: (agent: Agent, account: string) => void;
}

export function SpawnPanel({ agents, nt, onSpawn }: Props) {
  const sel = agents.find((a) => a.id === nt.agentId) ?? agents[0];
  const [profileId, setProfileId] = useState('');
  // every provider can run the API engine: a tile that is not armed spawns on its default
  const allProviders = useProviders();
  useEffect(() => setProfileId(''), [sel?.id]);
  if (!sel) return <p className="ring-empty">No agent is installed on this machine yet.</p>;
  // nt lists the profiles of the agent it has armed, which is the selected tile
  const profiles = sel.id === nt.agentId ? nt.profileOptions : [];
  const providers = sel.id === nt.agentId ? nt.providerOptions : isApiEngine(sel) ? allProviders : [];
  const chosen = accountFor(sel, profileId, { profiles, providers }, nt.defaultProvider);
  const blocked = needsProvider(sel, providers);
  // NextCode once: the engine chosen stands for all of them
  const tiles = foldEngines(agents, sel.id);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!blocked) onSpawn(sel, chosen);
  };
  /** A double click spawns a tile as it is: the account picked for the armed one, else the default. */
  const quick = (a: Agent) => {
    const account = a.id === sel.id ? (blocked ? null : chosen) : isApiEngine(a) ? apiProviderChoice('', allProviders, nt.defaultProvider) || null : '';
    if (account !== null) onSpawn(a, account);
  };
  return (
    <form className="spawn-panel" onSubmit={submit}>
      <div className="ring-grid nowheel" role="radiogroup" aria-label="Agents">
        {tiles.map((a) => (
          <button
            key={a.id}
            type="button"
            role="radio"
            aria-checked={a.id === sel.id}
            className={`ring-tile ${a.id === sel.id ? 'is-active' : ''}`}
            title={agentTagline(a) ? `${a.name} · ${agentTagline(a)}` : a.name}
            onClick={() => nt.setAgentId(a.id)}
            onDoubleClick={() => quick(a)}
          >
            <span className="ring-tile-mark">
              <AgentBadge agent={a} />
            </span>
            <span className="ring-tile-name truncate">{shortAgentName(a.name)}</span>
          </button>
        ))}
      </div>
      {agentTagline(sel) && (
        <span className="fs-xs text-3">
          {sel.name}: {agentTagline(sel)}
        </span>
      )}
      <EngineSwitch agents={agents} value={sel.id} onChange={nt.setAgentId} where={nt.machine?.name} field />
      {nt.multi && <PlacePicker project={nt.project} value={nt.placeValue} onChange={nt.setPlace} other={nt.placeValue === OTHER_VALUE} />}
      {blocked ? (
        <NeedsProviderHint />
      ) : (
        profiles.length + providers.length > 0 && (
          <SelectField label={isApiEngine(sel) ? 'API provider' : 'Login profile'} value={chosen} onChange={(e) => setProfileId(e.target.value)} className="control-sm">
            <AccountOptions profiles={profiles} defaultProfile={nt.defaultProfile} providers={providers} providersOnly={isApiEngine(sel)} defaultProvider={nt.defaultProvider} />
          </SelectField>
        )
      )}
      <ModelPicker agent={sel} value={nt.modelChoice} onChange={nt.setModelChoice} profileId={chosen} />
      <Button type="submit" size="sm" variant="primary" icon="play" disabled={blocked} title={blocked ? 'Add an API provider in Settings › API providers first' : undefined}>
        Spawn {shortAgentName(sel.name)}
      </Button>
    </form>
  );
}
