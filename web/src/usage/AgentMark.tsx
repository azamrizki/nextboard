// A small mark per CLI: its logo, or a drawn one in that CLI's accent colour (--usage-accent, set
// by data-agent).

import { AgentLogo } from '../agents/AgentLogo';
import { agentLogo } from '../agents/logos';

export function AgentMark({ agent, className = '' }: { agent: string; className?: string }) {
  const logo = agentLogo(agent);
  if (logo) return <AgentLogo logo={logo} className={`agent-mark ${className}`} />;
  return (
    <svg className={`agent-mark ${className}`} data-agent={agent} viewBox="0 0 24 24" aria-hidden="true">
      {agent === 'claude' ? (
        // a burst of eight rays
        <path d="M12 2.5v19M2.5 12h19M5.3 5.3l13.4 13.4M18.7 5.3 5.3 18.7" />
      ) : (
        // a prompt in a box
        <path d="M4 5h16v14H4zM8 9.5l3 2.5-3 2.5M13 15h3" />
      )}
    </svg>
  );
}
