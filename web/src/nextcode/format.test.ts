import { describe, expect, it } from 'vitest';
import { costText, elapsed, isLoginError, liveLine, loginHelp, matchCommands, MODES, modeOf, modesFor, rateLimitText } from './format';
import { planOf, questionsOf, shortPath, tail, toolArg, toolLabel, toolMeta } from './tools';
import type { Status } from './types';

const status = (over: Partial<Status> = {}): Status => ({ engine: 'claude', process: 'running', working: false, ...over });

describe('the live line', () => {
  it('follows the session’s state and activity', () => {
    expect(liveLine('working', 'Bash(npm test)', status({ working: true }), true)).toMatchObject({ dot: 'busy', text: 'Working · Bash(npm test)', timed: true });
    expect(liveLine('working', '', status({ working: true }), true).text).toBe('Working · Thinking');
    expect(liveLine('needsYou', 'Permission: Bash(rm -rf dist)', status({ working: true }), true)).toMatchObject({ dot: 'needs', text: 'Needs you · Permission: Bash(rm -rf dist)' });
    expect(liveLine('ready', '', status(), true)).toMatchObject({ dot: 'grey', text: 'Ready' });
  });

  it('says what only the chat knows', () => {
    expect(liveLine('working', 'x', status({ working: true, stopping: true }), true).text).toBe('Stopping…');
    expect(liveLine('working', 'Thinking', status({ working: true, note: 'Retrying (attempt 2 of 10)…' }), true).text).toBe('Working · Thinking · Retrying (attempt 2 of 10)…');
    expect(liveLine('ready', '', status({ process: 'stopped', conversation: 'c1' }), true).text).toMatch(/^Ready · resting/);
    expect(liveLine('ready', '', status(), false)).toMatchObject({ dot: 'warning', text: 'Connecting to the chat…' });
  });
});

describe('numbers', () => {
  it('formats cost, time and the rate limit', () => {
    expect(costText(undefined)).toBe('');
    expect(costText(0.004)).toBe('< $0.01');
    expect(costText(0.4219)).toBe('$0.42');
    expect(elapsed(41_000)).toBe('0:41');
    expect(elapsed(3_729_000)).toBe('1:02:09');
    expect(rateLimitText({ status: 'allowed' })).toBe('');
    expect(rateLimitText({ status: 'allowed', utilization: 0.23 })).toBe('limit 23% used');
    expect(rateLimitText({ status: 'allowed_warning' })).toBe('near the limit');
    expect(rateLimitText({ status: 'rejected' })).toBe('limit reached');
    // Codex: the fuller of its windows
    expect(rateLimitText({ rateLimits: { primary: { usedPercent: 23.4 }, secondary: { usedPercent: 61 } } })).toBe('limit 61% used');
    expect(rateLimitText({ rateLimits: { primary: { usedPercent: 100 } } })).toBe('limit reached');
    expect(rateLimitText({ rateLimits: { credits: { balance: '0' } } })).toBe('');
    expect(rateLimitText(null)).toBe('');
  });

  it('has no rate limit on the API engine: a provider has no subscription window', () => {
    expect(rateLimitText({ status: 'rejected' }, 'api')).toBe('');
    expect(rateLimitText({ rateLimits: { primary: { usedPercent: 100 } } }, 'api')).toBe('');
    expect(rateLimitText({ status: 'rejected' }, 'claude')).toBe('limit reached');
  });

  it('reads the permission mode', () => {
    expect(modeOf(null)).toBe('default');
    expect(modeOf(status({ permissionMode: 'acceptEdits' }))).toBe('acceptEdits');
    expect(modeOf(status({ permissionMode: 'weird' }))).toBe('default');
  });
});

describe('slash commands', () => {
  it('lists those that start with what is typed first', () => {
    const cmds = ['review', 'compact', 'model', 'pr-review', 'clear'];
    expect(matchCommands(cmds, '')).toEqual(cmds);
    expect(matchCommands(cmds, 're')).toEqual(['review', 'pr-review']);
    expect(matchCommands(cmds, 'C')).toEqual(['compact', 'clear']);
    expect(matchCommands(cmds, 'zzz')).toEqual([]);
  });
});

describe('the permission modes per engine', () => {
  it('are the same five on every engine, Auto saying what it does on the API engine', () => {
    expect(modesFor('claude')).toBe(MODES);
    expect(modesFor('codex')).toBe(MODES);
    const api = modesFor('api');
    expect(api.map((m) => m.value)).toEqual(MODES.map((m) => m.value));
    expect(api.find((m) => m.value === 'auto')?.title).toBe('Edits in the project go ahead, read-only commands run, and other commands ask');
    expect(api.find((m) => m.value === 'plan')?.title).toBe(MODES.find((m) => m.value === 'plan')?.title);
  });
});

describe('what a failed sign-in offers', () => {
  it('signs NextCode in on a login engine, through that engine’s CLI', () => {
    expect(loginHelp('claude', false)).toMatchObject({ title: 'Not logged in', action: 'Sign in NextCode', to: '/settings/nextcode' });
    expect(loginHelp('claude', false).hint).toMatch(/apart from Claude Code's/);
    expect(loginHelp('codex', false).hint).toMatch(/runs Codex's own login/);
    expect(loginHelp('', false).action).toBe('Sign in NextCode');
  });

  it('never offers a sign-in on the API engine, or on a provider: the provider’s key was refused', () => {
    for (const h of [loginHelp('api', false), loginHelp('api', true), loginHelp('claude', true)]) {
      expect(h).toMatchObject({ title: 'The API provider refused the key', action: 'Open API providers', to: '/settings/providers' });
      expect(h.hint).toMatch(/Settings › API providers/);
    }
  });
});

describe('a failed sign-in', () => {
  it('is told apart from other errors', () => {
    expect(isLoginError('Invalid API key · Please run /login')).toBe(true);
    expect(isLoginError('Not logged in')).toBe(true);
    expect(isLoginError('The CLI ended (exit 1)')).toBe(false);
  });
});

describe('tools', () => {
  it('names a call by its main argument', () => {
    expect(toolArg('Bash', { command: 'npm test -- signup\nnpm run lint' })).toBe('npm test -- signup …');
    expect(toolArg('Read', { file_path: '/home/me/shop/src/api/signup.ts' }, true)).toBe('signup.ts');
    expect(toolArg('Read', { file_path: '/home/me/shop/src/api/signup.ts' })).toBe('…/shop/src/api/signup.ts');
    expect(toolArg('Read', { file_path: '/a/b.ts', offset: 10, limit: 20 }, true)).toBe('b.ts  lines 10–29');
    expect(toolArg('Grep', { pattern: 'signup|rateLimit', path: 'src/' })).toBe('signup|rateLimit  src/');
    expect(toolArg('Task', { description: 'Explore how sign-up works', prompt: 'long' })).toBe('Explore how sign-up works');
    expect(toolArg('mcp__github__create_issue', { title: 'Bug' })).toBe('Bug');
    expect(toolArg('Unknown', null)).toBe('');
    expect(toolLabel('mcp__github__create_issue')).toBe('github · create_issue');
    expect(shortPath('a/b')).toBe('a/b');
  });

  it('notes what a finished call gave', () => {
    expect(toolMeta({ name: 'Read', status: 'done', output: 'a\nb\nc\n' })).toBe('3 lines');
    expect(toolMeta({ name: 'Grep', status: 'done', output: 'Found 4 files\na\nb' })).toBe('4 files');
    expect(toolMeta({ name: 'Read', status: 'running' })).toBe('');
    expect(tail('1\n2\n3\n4\n', 2)).toEqual({ text: '3\n4', cut: 2 });
  });

  it('reads a question’s options and a plan', () => {
    const qs = questionsOf({
      questions: [
        { question: 'Where should the counts live?', header: 'Storage', multiSelect: false, options: [{ label: 'Redis', description: 'Shared' }, { label: 'In memory' }] },
        { question: '', options: [] },
      ],
    });
    expect(qs).toEqual([
      {
        question: 'Where should the counts live?',
        header: 'Storage',
        multiSelect: false,
        options: [
          { label: 'Redis', description: 'Shared' },
          { label: 'In memory', description: '' },
        ],
      },
    ]);
    expect(questionsOf(undefined)).toEqual([]);
    expect(planOf({ plan: '1. Do it' })).toBe('1. Do it');
  });
});
