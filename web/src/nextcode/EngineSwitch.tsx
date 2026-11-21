// The Engine control of the spawn forms and the switch dialogs: NextCode is listed once, and this
// picks the agent that runs it, Claude Code ("nextcode"), Codex ("nextcode-codex") or its own
// engine on an API provider ("nextcode-api"). It shows only while NextCode is chosen and the hub
// has two engines or more; the API engine is disabled until there is an API provider.

import { Field, SegmentedControl } from '../components/form';
import type { Agent } from '../lib/types';
import { providersStore, useProviders } from '../providers/providers';
import { engineChoices, engineOf, isNextCodeBuiltin, rememberEngine, type Engine } from './engines';

interface Props {
  /** The agents the form offers: two NextCode agents or more must be among them. */
  agents: Agent[];
  /** The agent chosen now. */
  value: string;
  onChange: (agentId: string) => void;
  /** The machine's name, for why an engine is not offered. */
  where?: string;
  /** With a field label ("Engine") above it, as in the forms. */
  field?: boolean;
  className?: string;
}

export function EngineSwitch({ agents, value, onChange, where, field = false, className }: Props) {
  const providers = useProviders();
  // until the list has come, the API engine is not held back
  const choices = engineChoices(agents, { where, providers: providersStore.isLoaded() ? providers.length : undefined });
  if (!choices.length || !isNextCodeBuiltin(value)) return null;
  const engine = engineOf(agents.find((a) => a.id === value) ?? { id: value }) as Engine;
  const pick = (e: Engine) => {
    const to = choices.find((c) => c.value === e);
    if (!to || to.disabled || to.agentId === value) return;
    rememberEngine(e);
    onChange(to.agentId);
  };
  const control = (labelledBy?: string) => (
    <SegmentedControl<Engine>
      label={labelledBy ? undefined : 'NextCode engine'}
      labelledBy={labelledBy}
      size="sm"
      className={`nc-engines ${className ?? ''}`}
      value={engine}
      onChange={pick}
      options={choices.map((c) => ({ value: c.value, label: c.label, disabled: c.disabled, title: c.title }))}
    />
  );
  if (!field) return control();
  return (
    <Field label="Engine" as="group">
      {({ labelId }) => control(labelId)}
    </Field>
  );
}
