// Models and efforts of an agent's CLI (docs/API-agents.md §25): what it says it can run, what
// settings give a new console, and how to name a model on screen.

import { useEffect, useState } from 'react';
import { api } from '../api';
import type { Agent, AgentModel, ModelChoice, ModelList, Settings } from '../lib/types';

export const NO_CHOICE: ModelChoice = { model: '', effort: '' };

const EMPTY: ModelList = { models: [], efforts: [], source: 'none' };
const KEEP_MS = 60_000;
const lists = new Map<string, { at: number; list: Promise<ModelList> }>();

function loadModels(agentId: string, profileId: string): Promise<ModelList> {
  const key = `${agentId}|${profileId}`;
  const had = lists.get(key);
  if (had && Date.now() - had.at < KEEP_MS) return had.list;
  const list = api.agentModels(agentId, profileId).catch(() => EMPTY);
  lists.set(key, { at: Date.now(), list });
  return list;
}

/** What the CLI can run, once known. */
export function useAgentModels(agent: Agent | undefined, profileId = ''): ModelList | null {
  const id = agent?.models ? agent.id : '';
  const [state, setState] = useState<{ key: string; list: ModelList } | null>(null);
  const key = `${id}|${profileId}`;
  useEffect(() => {
    if (!id) return;
    let stop = false;
    void loadModels(id, profileId).then((list) => {
      if (!stop) setState({ key, list });
    });
    return () => {
      stop = true;
    };
  }, [id, profileId, key]);
  return state?.key === key ? state.list : null;
}

/** What a new console of this agent starts with: as the hub's ModelFor, field by field. */
export function defaultChoice(settings: Settings, role: string | undefined, agentId: string): ModelChoice {
  const ag = settings.agents;
  const own = ag.models?.[agentId];
  const forRole = role && ag.roleAgents?.[role] === agentId ? ag.roleModels?.[role] : undefined;
  return { model: forRole?.model || own?.model || '', effort: forRole?.effort || own?.effort || '' };
}

const WORDS: Record<string, string> = { gpt: 'GPT' };

/** A model's name where only its id is known: claude-sonnet-5-5 → Sonnet 5.5. */
export function modelLabel(id: string, known?: AgentModel[]): string {
  const row = known?.find((m) => m.id === id);
  if (row) return row.name;
  const claude = /^claude-([a-z]+)-(\d+)(?:-(\d+))?(\[1m\])?$/.exec(id);
  if (claude) return `${claude[1][0].toUpperCase()}${claude[1].slice(1)} ${claude[2]}${claude[3] ? `.${claude[3]}` : ''}${claude[4] ? ' (1M)' : ''}`;
  if (id.includes('/')) return id;
  return id
    .split('-')
    .map((w) => WORDS[w] ?? (/^[a-z]/.test(w) ? w[0].toUpperCase() + w.slice(1) : w))
    .join('-');
}

/** Model and effort of a console, in a few words; '' when it left both to the CLI. */
export function modelSummary(model?: string, effort?: string): string {
  return [model ? modelLabel(model) : '', effort ?? ''].filter(Boolean).join(' · ');
}
