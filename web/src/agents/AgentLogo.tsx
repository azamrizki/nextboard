// One agent logo at icon size. See logos.ts.

import type { CSSProperties } from 'react';
import type { AgentLogoFile } from './logos';

export function AgentLogo({ logo, className = '' }: { logo: AgentLogoFile; className?: string }) {
  if (logo.mono) return <span className={`agent-logo is-mono ${className}`} style={{ '--agent-logo': `url("${logo.src}")` } as CSSProperties} aria-hidden="true" />;
  return <img className={`agent-logo ${className}`} src={logo.src} alt="" draggable={false} />;
}
