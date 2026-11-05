// Settings › Agents: what a new console starts with, per agent and per group role. Any spawn form
// shows these as its first value and can still choose otherwise.

import { useState } from 'react';
import { errorMessage } from '../api';
import { byPriority, useMachineAgents } from '../agents/catalog';
import { ModelPicker } from '../agents/ModelPicker';
import { NO_CHOICE } from '../agents/models';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { SettingRow } from '../components/Switch';
import { toast } from '../components/Toast';
import type { Agent, ModelChoice } from '../lib/types';
import { ROLE_LABEL, ROLES } from '../orch/types';

async function save(agents: Record<string, unknown>) {
  try {
    await patchSettings({ agents });
  } catch (err) {
    toast.error(`Could not save the default: ${errorMessage(err)}`);
  }
}

/** Merge patch: an empty choice removes the entry. */
const patchOf = (c: ModelChoice) => (c.model || c.effort ? { model: c.model || null, effort: c.effort || null } : null);

export function ModelDefaults() {
  const settings = useEffectiveSettings();
  const ma = useMachineAgents('local');
  const ag = settings.agents;
  const installed = ma.agents.filter((a) => a.kind === 'agent' && a.available !== false).sort(byPriority);
  const withModels = installed.filter((a) => a.models);
  return (
    <section className="card settings-card" aria-labelledby="ag-models">
      <h3 id="ag-models" className="section-title">
        Default models
      </h3>
      <p className="setting-hint">
        What a new console starts with. Spawn forms show these first, and you can still choose otherwise there. A console then keeps its model and effort when it restarts, also what you chose inside the CLI with /model.
      </p>
      {withModels.map((a) => (
        <SettingRow key={a.id} title={a.name}>
          <ModelPicker agent={a} value={ag.models?.[a.id] ?? NO_CHOICE} onChange={(c) => void save({ models: { [a.id]: patchOf(c) } })} />
        </SettingRow>
      ))}
      {ma.ready && withModels.length === 0 && <p className="setting-hint">No installed agent can be told a model at launch.</p>}

      <h4 className="settings-subtitle">Group roles</h4>
      <p className="setting-hint">A group with this role starts with this agent, and with this model when its slot runs that agent.</p>
      {ROLES.map((role) => (
        <RoleRow key={role} role={role} installed={installed} agentId={ag.roleAgents?.[role] ?? ''} value={ag.roleModels?.[role] ?? NO_CHOICE} own={ag.models} />
      ))}
    </section>
  );
}

interface RoleRowProps {
  role: (typeof ROLES)[number];
  installed: Agent[];
  agentId: string;
  value: ModelChoice;
  own: Record<string, ModelChoice> | null | undefined;
}

function RoleRow({ role, installed, agentId, value, own }: RoleRowProps) {
  const [busy, setBusy] = useState(false);
  const agent = installed.find((a) => a.id === agentId);
  const label = ROLE_LABEL[role];
  const change = async (next: string) => {
    setBusy(true);
    // a model belongs to one agent: another agent starts from its own default
    await save({ roleAgents: { [role]: next }, roleModels: { [role]: null } });
    setBusy(false);
  };
  return (
    <SettingRow title={label} hint={agentId && !agent ? `${agentId} is not installed here: the machine's default runs instead.` : undefined}>
      <span className="model-role">
        <select className="control control-sm" aria-label={`Agent for ${label}`} disabled={busy} value={agent ? agentId : ''} onChange={(e) => void change(e.target.value)}>
          <option value="">Machine default</option>
          {installed.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
        <ModelPicker agent={agent} value={value} fallback={agent ? own?.[agent.id] : undefined} onChange={(c) => void save({ roleModels: { [role]: patchOf(c) } })} />
      </span>
    </SettingRow>
  );
}
