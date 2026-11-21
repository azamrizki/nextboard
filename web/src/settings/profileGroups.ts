// Login profiles by the agent they belong to (Settings › Login profiles): each agent has a group of
// its own, so NextCode's logins never show among Claude Code's, though both are Claude Code folders.

import { byPriority } from '../agents/catalog';
import type { Agent, Profile } from '../lib/types';
import { agentChoiceName } from '../nextcode/engines';

export interface ProfileGroup<P> {
  agent: string;
  /** The agent's name, or its id when the catalog does not know it. */
  name: string;
  profiles: P[];
}

/**
 * One group per agent, in the pickers' order (Claude Code, Codex, NextCode, …); profiles keep their
 * order. NextCode's two engines are two groups, named "NextCode · Claude" and "NextCode · Codex".
 */
export function groupByAgent<P extends Pick<Profile, 'agent'>>(profiles: P[], agents: (Pick<Agent, 'id' | 'name'> & { engine?: string })[]): ProfileGroup<P>[] {
  const groups = new Map<string, ProfileGroup<P>>();
  for (const p of profiles) {
    let g = groups.get(p.agent);
    if (!g) {
      const a = agents.find((x) => x.id === p.agent);
      g = { agent: p.agent, name: a ? agentChoiceName(a, agents) : p.agent, profiles: [] };
      groups.set(p.agent, g);
    }
    g.profiles.push(p);
  }
  return [...groups.values()].sort((a, b) => byPriority({ id: a.agent, name: a.name }, { id: b.agent, name: b.name }));
}
