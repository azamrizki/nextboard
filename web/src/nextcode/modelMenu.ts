// The chat's model menu (docs/API-agents.md §25, §30): what it lists from the CLI's model list, what
// a pick sends to PUT …/nextcode/model, and what the chip says. Pure, so tests can read it.

import { errorMessage, isCode, isStatus } from '../api';
import { modelLabel } from '../agents/models';
import type { AgentModel, ModelChoice, ModelList } from '../lib/types';
import type { ModelChange } from './api';

/** The names the hub takes (docs/API-agents.md §25); anything else is 400. */
export const MODEL_NAME = /^[A-Za-z0-9][A-Za-z0-9._:/@[\]-]{0,127}$/;

/** Why a typed model name would be refused, or ''. */
export function modelNameProblem(name: string): string {
  const n = name.trim();
  if (!n) return 'Type a model name.';
  if (!MODEL_NAME.test(n)) return 'A model name has letters, digits and . _ : / @ [ ] -, up to 128 characters.';
  return '';
}

export interface MenuModels {
  /** "Default (Sonnet 5.5)": what the CLI runs when told nothing. */
  defaultLabel: string;
  /** The CLI's list, and the console's model when the list does not have it. */
  models: AgentModel[];
  /** The efforts of the chosen model (else the CLI's). */
  efforts: string[];
  /** "Default (high)". */
  defaultEffortLabel: string;
}

/** The rows of the menu for the console's model and effort. */
export function menuModels(list: ModelList | null | undefined, model: string, effort: string): MenuModels {
  const known = list?.models ?? [];
  const row = known.find((m) => m.id === model);
  const models = model && !row ? [...known, { id: model, name: modelLabel(model, known) }] : known;
  const efforts = [...((row?.efforts?.length ? row.efforts : list?.efforts) ?? [])];
  if (effort && !efforts.includes(effort)) efforts.push(effort);
  const cur = list?.current ?? '';
  const curEffort = row?.defaultEffort || list?.currentEffort || '';
  return {
    defaultLabel: cur ? `Default (${modelLabel(cur, known)})` : 'Default',
    models,
    efforts,
    defaultEffortLabel: curEffort ? `Default (${curEffort})` : 'Default',
  };
}

/**
 * What picking a model or an effort sends: only what changes ("" = the CLI's own choice). A model
 * that does not have the console's effort takes the default effort with it, as the spawn form does.
 * null when nothing changes.
 */
export function modelChange(now: ModelChoice, pick: ModelChange, list: ModelList | null | undefined): ModelChange | null {
  const model = now.model ?? '';
  const effort = now.effort ?? '';
  const out: ModelChange = {};
  if (pick.model !== undefined && pick.model !== model) {
    out.model = pick.model;
    const next = list?.models.find((m) => m.id === pick.model);
    if (pick.effort === undefined && effort && next?.efforts?.length && !next.efforts.includes(effort)) out.effort = '';
  }
  if (pick.effort !== undefined && pick.effort !== effort) out.effort = pick.effort;
  return out.model === undefined && out.effort === undefined ? null : out;
}

/** The effort alone changes: it waits for the CLI's next start, so the chat says when it applies. */
export const onlyEffort = (c: ModelChange) => c.model === undefined && c.effort !== undefined;

/**
 * The chip: the console's model (what it chose, else what the CLI reports it runs), then its
 * effort. "Default model" before either is known.
 */
export function modelChipText(chosen: ModelChoice, running: string | undefined, known?: AgentModel[]): string {
  const model = chosen.model || running || '';
  return [model ? modelLabel(model, known) : 'Default model', chosen.effort ?? ''].filter(Boolean).join(' · ');
}

/** Why the hub did not take a model or effort, in words. */
export function modelErrorText(err: unknown): string {
  if (isCode(err, 'bad_model')) return `That model or effort was refused: ${errorMessage(err)}`;
  if (isCode(err, 'not_nextcode')) return 'This console is not a NextCode chat: change its model with Agent and model… in its menu.';
  // a hub from before the route: Go's mux answers 405, the API's catch-all 404 "endpoint not found"
  if (isStatus(err, 405) || (isStatus(err, 404) && /endpoint not found/i.test(errorMessage(err))))
    return 'This hub cannot switch a chat’s model yet. Update NextBoard on the hub, or use Agent and model… in the console menu.';
  return `Could not change the model: ${errorMessage(err)}`;
}
