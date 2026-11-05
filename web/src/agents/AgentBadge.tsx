// The small square that stands for an agent where a whole name does not fit, in the agent's colour
// from the catalog: its logo, a terminal icon for a shell, or its first letters on a gradient for
// an agent without a logo.

import type { CSSProperties } from 'react';
import { Icon } from '../components/Icon';
import type { Agent } from '../lib/types';
import { AgentLogo } from './AgentLogo';
import { agentLogo } from './logos';
import '../styles/ring.css';

export function AgentBadge({ agent, small }: { agent: Agent | undefined; small?: boolean }) {
  const logo = agent ? agentLogo(agent.id, agent.base) : null;
  const shell = agent?.kind === 'shell';
  const kind = shell || logo ? 'has-mark' : 'has-letters';
  return (
    <span className={`agent-badge ${kind} ${small ? 'is-small' : ''}`} style={{ '--agent-c': agent?.color } as CSSProperties} aria-hidden="true">
      {shell ? <Icon name="terminal" /> : logo ? <AgentLogo logo={logo} /> : (agent?.name ?? '?').replace(/[^\p{L}\p{N}]/gu, '').slice(0, 2)}
    </span>
  );
}

/** "Claude Code" → "Claude": what fits under a badge. The whole name stays in the tooltip. */
export const shortAgentName = (name: string) => name.replace(/^GitHub /, '').split(' ')[0] || name;
