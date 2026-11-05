// Logos of the agent CLIs, from @lobehub/icons-static-svg 1.102.0 (MIT); each mark belongs to its
// owner. They load as images: an SVG in <img> or in a CSS mask can't run anything. Mono files are
// one shape, painted in the agent's colour through a mask; the rest bring their own colours.

import amp from './logos/amp.svg';
import claude from './logos/claude.svg';
import cline from './logos/cline.svg';
import codex from './logos/codex.svg';
import copilot from './logos/copilot.svg';
import cursor from './logos/cursor-agent.svg';
import gemini from './logos/gemini.svg';
import goose from './logos/goose.svg';
import kimi from './logos/kimi.svg';
import kiro from './logos/kiro.svg';
import opencode from './logos/opencode.svg';
import pi from './logos/pi.svg';
import qwen from './logos/qwen.svg';
import vibe from './logos/vibe.svg';

export interface AgentLogoFile {
  src: string;
  mono: boolean;
}

const COLOR: Record<string, string> = { amp, claude, codex, gemini, kimi, kiro, qwen, vibe };
const MONO: Record<string, string> = { cline, copilot, 'cursor-agent': cursor, goose, opencode, pi };

/** The logo of a catalog agent; a custom agent shows the one of the CLI it is based on. */
export function agentLogo(id: string, base?: string): AgentLogoFile | null {
  for (const key of [id, base ?? '']) {
    if (COLOR[key]) return { src: COLOR[key], mono: false };
    if (MONO[key]) return { src: MONO[key], mono: true };
  }
  return null;
}
