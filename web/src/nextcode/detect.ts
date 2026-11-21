// Which consoles are NextCode's (docs/API-agents.md §30): those whose agent has an `engine` in the
// catalog, whichever engine runs it (Claude Code, Codex, or its own on an API provider). Their console is a chat, so the views that draw a terminal draw
// NextCodeChat instead. Until the catalog has loaded, the built-ins' ids decide, so a NextCode
// console never flashes a terminal first; a terminal console is never taken for one.

import { useSyncExternalStore } from 'react';
import { isChatAgent, useAgent } from '../agents/catalog';
import type { Agent } from '../lib/types';
import { store } from '../store';
import { isNextCodeBuiltin, NEXTCODE_AGENTS } from './engines';

/** The built-in NextCode agent (its Claude engine). */
export const NEXTCODE_AGENT = NEXTCODE_AGENTS.claude;

export function isNextCodeAgent(agentId: string, agent: Agent | undefined): boolean {
  if (!agentId) return false;
  return agent ? isChatAgent(agent) : isNextCodeBuiltin(agentId);
}

/** The agent a session runs ("" when the session is unknown): a string, so session events do not re-render. */
function useSessionAgentId(sessionId: string): string {
  return useSyncExternalStore(store.subscribe, () => {
    const s = store.getState().sessions[sessionId];
    return s ? s.agent || s.preset || '' : '';
  });
}

/** True when this console is a NextCode chat, not a terminal. */
export function useIsNextCode(sessionId: string): boolean {
  const agentId = useSessionAgentId(sessionId);
  const agent = useAgent(agentId || undefined);
  return isNextCodeAgent(agentId, agent);
}
