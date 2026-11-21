import { describe, expect, it } from 'vitest';
import type { Agent } from '../lib/types';
import { isNextCodeAgent } from './detect';

const agent = (id: string, engine?: string) => ({ id, name: id, kind: 'agent', engine }) as Agent;

describe('isNextCodeAgent', () => {
  it('follows the catalog: an agent with an engine has a chat', () => {
    expect(isNextCodeAgent('nextcode', agent('nextcode', 'claude'))).toBe(true);
    expect(isNextCodeAgent('nextcode-codex', agent('nextcode-codex', 'codex'))).toBe(true);
    expect(isNextCodeAgent('nextcode-api', agent('nextcode-api', 'api'))).toBe(true);
    expect(isNextCodeAgent('mine', agent('mine', 'claude'))).toBe(true);
  });

  it('never takes a terminal agent for one', () => {
    expect(isNextCodeAgent('claude', agent('claude'))).toBe(false);
    expect(isNextCodeAgent('codex', agent('codex', ''))).toBe(false);
    expect(isNextCodeAgent('shell', undefined)).toBe(false);
    expect(isNextCodeAgent('', undefined)).toBe(false);
  });

  it('knows the built-ins, every engine, before the catalog has loaded', () => {
    expect(isNextCodeAgent('nextcode', undefined)).toBe(true);
    expect(isNextCodeAgent('nextcode-codex', undefined)).toBe(true);
    expect(isNextCodeAgent('nextcode-api', undefined)).toBe(true);
    expect(isNextCodeAgent('mine', undefined)).toBe(false);
  });
});
