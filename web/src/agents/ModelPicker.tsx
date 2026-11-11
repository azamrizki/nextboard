// Model and effort of one agent, as selects: what its CLI lists, "Default" for what it does when
// told nothing, and a typed name for a model it does not list.

import { useState } from 'react';
import type { Agent, AgentModel, ModelChoice, ModelList } from '../lib/types';
import { isProviderId } from '../providers/AccountOptions';
import { providerModels, useProvider } from '../providers/providers';
import { modelLabel, useAgentModels } from './models';

const OTHER = '\0other';

interface Props {
  agent: Agent | undefined;
  value: ModelChoice;
  onChange: (c: ModelChoice) => void;
  /** The login whose list this is ('' = the agent's default one), or an API provider (ap_…). */
  profileId?: string;
  /** Settings only: what "Default" stands for when it is not the CLI's own choice. */
  fallback?: ModelChoice;
  disabled?: boolean;
}

export function ModelPicker({ agent, value, onChange, profileId, fallback, disabled }: Props) {
  const provider = useProvider(isProviderId(profileId) ? profileId : undefined);
  const cliList = useAgentModels(agent, provider ? undefined : profileId);
  const [typing, setTyping] = useState(false);
  if (!agent?.models && !provider) return null;
  if (!agent) return null;
  // on a provider: its models, its default
  const list: ModelList | null | undefined = provider
    ? { efforts: cliList?.efforts ?? [], source: 'provider', models: providerModels(provider).map((id): AgentModel => ({ id, name: id })), current: provider.model }
    : cliList;
  const model = value.model ?? '';
  const effort = value.effort ?? '';
  const models = list?.models ?? [];
  const row = models.find((m) => m.id === model);
  const efforts = (row?.efforts?.length ? row.efforts : list?.efforts) ?? [];
  const listed = !model || !!row;

  const shownDefault = (provider ? '' : fallback?.model) || list?.current || '';
  const defaultModel = shownDefault ? `Default (${modelLabel(shownDefault, models)})` : 'Default';
  const defaultEffort = fallback?.effort || row?.defaultEffort || list?.currentEffort || '';

  const pickModel = (id: string) => {
    if (id === OTHER) {
      setTyping(true);
      return;
    }
    setTyping(false);
    const next = models.find((m) => m.id === id);
    // an effort this model does not have would be refused when it starts
    const keep = !effort || !next?.efforts?.length || next.efforts.includes(effort);
    onChange({ model: id, effort: keep ? effort : '' });
  };

  return (
    <span className="model-pick">
      <select
        className="control control-sm"
        aria-label={`Model for ${agent.name}`}
        title="Model"
        disabled={disabled}
        value={typing ? OTHER : model}
        onChange={(e) => pickModel(e.target.value)}
      >
        <option value="">{defaultModel}</option>
        {models.map((m) => (
          <option key={m.id} value={m.id} title={m.description}>
            {m.name}
          </option>
        ))}
        {!listed && <option value={model}>{modelLabel(model, models)}</option>}
        <option value={OTHER}>Other…</option>
      </select>
      {typing && (
        <input
          className="control control-sm mono"
          autoFocus
          aria-label={`Model name for ${agent.name}`}
          placeholder="Model name"
          maxLength={128}
          disabled={disabled}
          value={model}
          onChange={(e) => onChange({ model: e.target.value.trim(), effort })}
        />
      )}
      {agent.efforts && (
        <select
          className="control control-sm model-pick-effort"
          aria-label={`Effort for ${agent.name}`}
          title="Effort"
          disabled={disabled}
          value={effort}
          onChange={(e) => onChange({ model, effort: e.target.value })}
        >
          <option value="">{defaultEffort ? `Default (${defaultEffort})` : 'Default'}</option>
          {efforts.map((x) => (
            <option key={x} value={x}>
              {x}
            </option>
          ))}
          {effort && !efforts.includes(effort) && <option value={effort}>{effort}</option>}
        </select>
      )}
    </span>
  );
}
