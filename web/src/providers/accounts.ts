// What a new console starts with, as the forms offer it (docs/API-agents.md §10.2, §30): the
// default login (""), another login profile (pf_…) or an API provider (ap_…). NextCode's API
// engine has no login at all: it runs on an API provider, picked or the default of Settings ›
// NextCode. Pure, so tests can read it.

import { apiProviderChoice, isApiEngine } from '../nextcode/engines';

export function isProviderId(v: string | undefined): boolean {
  return !!v && v.startsWith('ap_');
}

/** A select value as the API takes it. */
export function splitAccount(v: string): { profileId?: string; providerId?: string } {
  if (!v) return {};
  return isProviderId(v) ? { providerId: v } : { profileId: v };
}

type AgentLike = { id: string; engine?: string };

/**
 * The account a console of `agent` starts on: the one chosen when it is among what is offered,
 * else the default login (""). For the API engine: the provider chosen, else Settings › NextCode's
 * default, else the first one; '' only when there is no provider at all.
 */
export function accountFor(agent: AgentLike | undefined, chosen: string, offered: { profiles: { id: string }[]; providers: { id: string }[] }, defaultProvider?: string): string {
  if (isApiEngine(agent)) return apiProviderChoice(chosen, offered.providers, defaultProvider);
  return offered.profiles.some((p) => p.id === chosen) || offered.providers.some((p) => p.id === chosen) ? chosen : '';
}

/** A console of the API engine with no API provider to run on: its Start waits for one. */
export const needsProvider = (agent: AgentLike | undefined, providers: { id: string }[]) => isApiEngine(agent) && providers.length === 0;
