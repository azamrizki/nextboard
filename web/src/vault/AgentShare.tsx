// Which projects' agents may ask to use an item (agents.ts). Part of the item form, and saved in
// the item's blob like everything else in it.

import { useMemo } from 'react';
import { Checkbox } from '../components/form';
import { useProjects } from '../store';
import { agentValues } from './agents';
import type { AgentShare, VaultItem } from './model';

export function AgentShareField({ item, onChange, error }: { item: VaultItem; onChange: (share: AgentShare | undefined) => void; error?: string }) {
  const projects = useProjects();
  const values = useMemo(() => agentValues(item).map((v) => v.name), [item]);
  const on = !!item.agents;
  // Cards and identities give nothing of their own: offer it once there is a field to give.
  if (!values.length && !on) return null;
  const picked = new Set(item.agents?.projects ?? []);
  const toggle = (id: string, yes: boolean) => onChange({ projects: yes ? [...picked, id] : [...picked].filter((p) => p !== id) });

  return (
    <div className="vault-span vault-agents">
      <Checkbox checked={on} onChange={(yes) => onChange(yes ? { projects: [] } : undefined)} label="Agents may ask to use it" />
      {on && (
        <>
          <p className="vault-note">
            Agents of the projects you pick see its name and the names of its values{values.length ? `: ${values.join(', ')}` : ''}. They never see a value: every command that would get one waits for you to approve it.
          </p>
          <div className="vault-agents-projects" role="group" aria-label="Projects whose agents may ask">
            {projects.map((p) => (
              <Checkbox key={p.id} checked={picked.has(p.id)} onChange={(yes) => toggle(p.id, yes)} label={p.name} />
            ))}
          </div>
          {!values.length && <p className="vault-note">Nothing in it can go to a command yet: give it a password or a custom field.</p>}
          {error && (
            <span className="field-error" role="alert">
              {error}
            </span>
          )}
        </>
      )}
    </div>
  );
}
