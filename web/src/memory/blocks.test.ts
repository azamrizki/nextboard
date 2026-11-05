import { describe, expect, it } from 'vitest';
import type { MemoryPlanTarget } from '../orch/types';
import { addBlocks, agentsLabel, blockKey, sentMessage, summarizeSend, tokensLabel } from './blocks';

const target = (over: Partial<MemoryPlanTarget>): MemoryPlanTarget => ({
  sessionId: 's',
  handle: 'ash',
  agent: 'claude',
  workspaceId: 'w',
  state: 'ready',
  canSend: true,
  chars: 0,
  lookup: [],
  ...over,
});

describe('memory blocks', () => {
  it('names a memory the way the hub does', () => {
    expect(blockKey({ name: 'main' })).toBe('global/main');
    expect(blockKey({ projectId: '', name: 'design' })).toBe('global/design');
    expect(blockKey({ projectId: 'p_a', name: 'design' })).toBe('p_a/design');
  });

  it('rounds tokens up, in thousands from 1000', () => {
    expect(tokensLabel(0)).toBe('≈0');
    expect(tokensLabel(1)).toBe('≈1');
    expect(tokensLabel(3996)).toBe('≈999');
    expect(tokensLabel(4000)).toBe('≈1.0k');
    expect(tokensLabel(5200)).toBe('≈1.3k');
  });

  it('never adds a block twice, or the project memory that is always on', () => {
    const got = addBlocks([{ name: 'main' }], [{ name: 'main' }, { projectId: 'p_a', name: 'main' }, { projectId: 'p_b', name: 'x' }, { projectId: 'p_b', name: 'x', lookup: true }], 'p_a');
    expect(got).toEqual([{ name: 'main' }, { projectId: 'p_b', name: 'x' }]);
  });

  it('counts only what a send would really type', () => {
    const s = summarizeSend([
      target({ sessionId: 'a', chars: 400 }),
      target({ sessionId: 'b', chars: 0 }),
      target({ sessionId: 'c', chars: 100 }),
      target({ sessionId: 'd', chars: 900, canSend: false }),
      target({ sessionId: 'e', chars: 0, canSend: false }),
    ]);
    expect(s.send.map((t) => t.sessionId)).toEqual(['a', 'c']);
    expect(s.chars).toBe(500);
    expect(s.upToDate).toBe(1);
    expect(s.unavailable).toBe(2);
  });

  it('says what a send did', () => {
    expect(sentMessage({ delivered: 2, busy: 0 })).toBe('Memory sent to 2 agents.');
    expect(sentMessage({ delivered: 1, busy: 1, upToDate: 3 })).toBe('Memory sent to 1 agent; 1 agent gets it when free; 3 had it already.');
    expect(sentMessage({ delivered: 0, busy: 2 })).toBe('2 agents get it when free.');
    expect(sentMessage({ delivered: 0, busy: 0 })).toBe('Nothing was sent.');
  });

  it('says one agent or many', () => {
    expect(agentsLabel(1)).toBe('1 agent');
    expect(agentsLabel(0)).toBe('0 agents');
    expect(agentsLabel(3)).toBe('3 agents');
  });
});
