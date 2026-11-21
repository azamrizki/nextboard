import { describe, expect, it } from 'vitest';
import type { Agent } from '../lib/types';
import { agentProfiles, agentTagline, byPriority, hasYolo, isChatAgent, supportsProfiles, switchTargets } from './catalog';

function agent(id: string, over: Partial<Agent> & { engine?: string } = {}): Agent {
  return {
    id,
    name: id,
    kind: 'agent',
    source: 'builtin',
    base: '',
    command: id,
    args: [],
    env: [],
    color: '',
    detect: '',
    yoloArgs: [],
    yoloEnv: [],
    resume: { kind: 'none', launchArgs: [], resumeArgs: [], continueArgs: [], pickerArgs: [] },
    liveStatus: 'none',
    transcript: 'none',
    profileEnv: '',
    screenRules: '',
    resumeArgs: [],
    ...over,
  } as Agent;
}

const claude = agent('claude', { name: 'Claude Code', profileEnv: 'CLAUDE_CONFIG_DIR', yoloArgs: ['--dangerously-skip-permissions'] });
const codex = agent('codex', { name: 'Codex', profileEnv: 'CODEX_HOME', yoloArgs: ['--dangerously-bypass-approvals-and-sandbox'] });
const nextcode = agent('nextcode', { name: 'NextCode', base: 'claude', engine: 'claude', command: 'claude', profileEnv: 'CLAUDE_CONFIG_DIR', yoloArgs: ['--dangerously-skip-permissions'] });
// NextCode's own engine: no CLI, no login folder; even a profileEnv the catalog gave it would not make one
const nextcodeApi = agent('nextcode-api', { name: 'NextCode', engine: 'api', command: '', profileEnv: 'NEXTCODE_HOME' });
const gemini = agent('gemini', { name: 'Gemini CLI', yoloArgs: ['--yolo'] });
const shell = agent('shell', { name: 'Shell', kind: 'shell', command: '$SHELL' });
const aider = agent('aider', { name: 'Aider', source: 'catalog' });

const profiles = [
  { id: 'pf_claude', machineId: 'local', agent: 'claude' },
  { id: 'pf_claudework', machineId: 'local', agent: 'claude' },
  { id: 'pf_nextcode', machineId: 'local', agent: 'nextcode' },
  { id: 'pf_codex', machineId: 'local', agent: 'codex' },
  { id: 'pf_remote', machineId: 'm_server', agent: 'claude' },
  { id: 'pf_odd', machineId: 'local', agent: 'nextcode-api' },
];

describe('NextCode in the catalog', () => {
  it('is a chat agent, and only it', () => {
    expect(isChatAgent(nextcode)).toBe(true);
    expect([claude, codex, gemini, shell, undefined].some(isChatAgent)).toBe(false);
    expect(agentTagline(nextcode)).toBe('Chat · built for NextBoard');
    expect(agentTagline(claude)).toBe('');
  });

  it('comes right after Codex in the pickers, its Codex engine right after it', () => {
    const nextcodeCodex = agent('nextcode-codex', { name: 'NextCode', base: 'codex', engine: 'codex', command: 'codex', profileEnv: 'CODEX_HOME' });
    const sorted = [gemini, nextcodeCodex, aider, nextcode, codex, claude].sort(byPriority).map((a) => a.id);
    expect(sorted).toEqual(['claude', 'codex', 'nextcode', 'nextcode-codex', 'gemini', 'aider']);
    expect(isChatAgent(nextcodeCodex)).toBe(true);
  });

  it('has its API engine after its other two, and a chat too', () => {
    const nextcodeCodex = agent('nextcode-codex', { name: 'NextCode', engine: 'codex' });
    const sorted = [gemini, nextcodeApi, nextcodeCodex, nextcode, claude].sort(byPriority).map((a) => a.id);
    expect(sorted).toEqual(['claude', 'nextcode', 'nextcode-codex', 'nextcode-api', 'gemini']);
    expect(isChatAgent(nextcodeApi)).toBe(true);
    expect(hasYolo(nextcodeApi)).toBe(false);
  });

  it('has no Yolo, though its catalog entry has a skip-permissions flag: it has permission modes', () => {
    expect(hasYolo(nextcode)).toBe(false);
    expect(hasYolo(claude)).toBe(true);
    expect(hasYolo(shell)).toBe(false);
  });
});

describe('agentProfiles', () => {
  it('offers NextCode only its own profiles, never Claude Code’s', () => {
    expect(agentProfiles(profiles, 'local', nextcode).map((p) => p.id)).toEqual(['pf_nextcode']);
  });

  it('offers Claude Code only its own profiles, never NextCode’s', () => {
    expect(agentProfiles(profiles, 'local', claude).map((p) => p.id)).toEqual(['pf_claude', 'pf_claudework']);
    expect(agentProfiles(profiles, 'm_server', claude).map((p) => p.id)).toEqual(['pf_remote']);
  });

  it('offers nothing to an agent without login profiles, or none', () => {
    expect(agentProfiles(profiles, 'local', gemini)).toEqual([]);
    expect(agentProfiles(profiles, 'local', undefined)).toEqual([]);
  });

  it('offers NextCode’s API engine no login profile at all: it runs on an API provider', () => {
    expect(supportsProfiles(nextcodeApi)).toBe(false);
    expect(agentProfiles(profiles, 'local', nextcodeApi)).toEqual([]);
    expect(supportsProfiles(nextcode)).toBe(true);
  });
});

describe('switchTargets', () => {
  const all = [claude, codex, nextcode, gemini, shell];

  it('never offers NextCode to a terminal console', () => {
    const t = switchTargets(all, claude);
    expect(t.list.map((a) => a.id)).toEqual(['claude', 'codex', 'gemini', 'shell']);
    expect(t.hidden.map((a) => a.id)).toEqual(['nextcode']);
    expect(switchTargets(all, shell).list).not.toContain(nextcode);
  });

  it('never offers a terminal agent to a NextCode console', () => {
    const t = switchTargets(all, nextcode);
    expect(t.list.map((a) => a.id)).toEqual(['nextcode']);
    expect(t.hidden.map((a) => a.id)).toEqual(['claude', 'codex', 'gemini', 'shell']);
  });

  it('lets a NextCode console move between its engines, the API engine too', () => {
    const t = switchTargets([...all, nextcodeApi], nextcodeApi);
    expect(t.list.map((a) => a.id)).toEqual(['nextcode', 'nextcode-api']);
  });

  it('treats a console whose agent is not known yet as a terminal', () => {
    expect(switchTargets(all, undefined).list).not.toContain(nextcode);
  });
});
