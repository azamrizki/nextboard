import { describe, expect, it } from 'vitest';
import { emptyChat, groupItems, groupStreams, reduce, reduceAll, type ChatState } from './reducer';
import type { Item, Request, Status } from './types';

const at = '2026-11-20T10:00:00Z';
const status = (over: Partial<Status> = {}): Status => ({ engine: 'claude', process: 'running', working: false, ...over });
const text = (id: string, t: string, over: Partial<Item> = {}): Item => ({ id, kind: 'text', at, text: t, ...over });
const tool = (id: string, name: string, over: Partial<Item> = {}): Item => ({ id, kind: 'tool', at, tool: { name, input: {}, status: 'running' }, ...over });
const req = (id: string, over: Partial<Request> = {}): Request => ({ id, kind: 'permission', tool: 'Bash', at, ...over });

function loaded(items: Item[] = [], more = false): ChatState {
  return reduce(emptyChat, { t: 'snapshot', items, more, requests: [], status: status() });
}

describe('snapshot', () => {
  it('replaces everything and marks the chat loaded', () => {
    let s = loaded([text('a', 'old')]);
    s = reduce(s, { t: 'block', id: 'b1', kind: 'text' });
    s = reduce(s, { t: 'request', request: req('r1') });
    s = reduce(s, { t: 'snapshot', items: [text('x', 'new')], more: true, requests: [req('r2')], status: status({ working: true }) });
    expect(s.loaded).toBe(true);
    expect(s.items.map((i) => i.id)).toEqual(['x']);
    expect(s.more).toBe(true);
    expect(s.requests.map((r) => r.id)).toEqual(['r2']);
    expect(s.status?.working).toBe(true);
    // a stream is not in a snapshot: its item comes later, or it was lost
    expect(s.streams).toEqual([]);
  });

  it('takes null lists as empty', () => {
    const s = reduce(emptyChat, { t: 'snapshot', items: null, requests: null, status: status() });
    expect(s.items).toEqual([]);
    expect(s.requests).toEqual([]);
    expect(s.more).toBe(false);
  });

  it('keeps the last status when the snapshot has none', () => {
    const s = reduce(loaded(), { t: 'snapshot', items: [] });
    expect(s.status?.engine).toBe('claude');
  });
});

describe('item', () => {
  it('appends a new item', () => {
    const s = reduce(loaded([text('a', 'one')]), { t: 'item', item: text('b', 'two') });
    expect(s.items.map((i) => i.id)).toEqual(['a', 'b']);
  });

  it('replaces the item with the same id in place', () => {
    const before = loaded([tool('t1', 'Bash'), text('a', 'after')]);
    const done: Item = { ...tool('t1', 'Bash'), tool: { name: 'Bash', input: { command: 'ls' }, output: 'x', status: 'done' } };
    const s = reduce(before, { t: 'item', item: done });
    expect(s.items.map((i) => i.id)).toEqual(['t1', 'a']);
    expect(s.items[0].tool?.status).toBe('done');
    // the other items keep their identity (their views are memoised)
    expect(s.items[1]).toBe(before.items[1]);
  });

  it('ignores an item without an id', () => {
    const s = loaded([text('a', 'one')]);
    expect(reduce(s, { t: 'item', item: { id: '', kind: 'text', at } })).toBe(s);
  });
});

describe('streaming', () => {
  it('grows a block with its deltas', () => {
    const s = reduceAll(loaded(), [
      { t: 'block', id: 'msg_1/0', kind: 'text' },
      { t: 'delta', id: 'msg_1/0', kind: 'text', text: 'Hel' },
      { t: 'delta', id: 'msg_1/0', kind: 'text', text: 'lo' },
    ]);
    expect(s.streams).toEqual([{ id: 'msg_1/0', kind: 'text', name: undefined, parent: undefined, text: 'Hello' }]);
    expect(s.items).toEqual([]);
  });

  it('keeps a tool block’s name and a sub-agent’s parent', () => {
    const s = reduce(loaded(), { t: 'block', id: 'msg_2/1', kind: 'tool', name: 'Bash', parent: 'toolu_9' });
    expect(s.streams[0]).toMatchObject({ kind: 'tool', name: 'Bash', parent: 'toolu_9', text: '' });
  });

  it('replaces the block with the item whose stream is its id', () => {
    const s = reduceAll(loaded([text('u', 'q', { kind: 'user' })]), [
      { t: 'block', id: 'msg_1/0', kind: 'text' },
      { t: 'block', id: 'msg_1/1', kind: 'tool', name: 'Read' },
      { t: 'delta', id: 'msg_1/0', kind: 'text', text: 'Looking' },
      { t: 'item', item: text('msg_1/0', 'Looking at it.', { stream: 'msg_1/0' }) },
    ]);
    expect(s.streams.map((x) => x.id)).toEqual(['msg_1/1']);
    expect(s.items.map((i) => i.id)).toEqual(['u', 'msg_1/0']);
    // a tool item has its tool_use id and the block's id as stream
    const t = reduce(s, { t: 'item', item: tool('toolu_1', 'Read', { stream: 'msg_1/1' }) });
    expect(t.streams).toEqual([]);
    expect(t.items.map((i) => i.id)).toEqual(['u', 'msg_1/0', 'toolu_1']);
  });

  it('ignores deltas of a block it never saw start', () => {
    const s = loaded();
    expect(reduce(s, { t: 'delta', id: 'nope', kind: 'text', text: 'x' })).toBe(s);
  });

  it('does not open a block twice, nor one whose item is already in', () => {
    let s = reduce(loaded(), { t: 'block', id: 'b', kind: 'text' });
    expect(reduce(s, { t: 'block', id: 'b', kind: 'text' })).toBe(s);
    s = reduce(loaded([text('b', 'x', { stream: 'b' })]), { t: 'block', id: 'b', kind: 'text' });
    expect(s.streams).toEqual([]);
  });

  it('ends open blocks when the turn ends', () => {
    let s = reduceAll(loaded(), [
      { t: 'status', status: status({ working: true }) },
      { t: 'block', id: 'b', kind: 'text' },
      { t: 'delta', id: 'b', kind: 'text', text: 'partial' },
    ]);
    expect(s.streams[0].ended).toBeUndefined();
    s = reduce(s, { t: 'status', status: status({ working: false }) });
    expect(s.streams[0]).toMatchObject({ text: 'partial', ended: true });
    // its item may still come and take its place
    s = reduce(s, { t: 'item', item: text('b', 'partial', { stream: 'b' }) });
    expect(s.streams).toEqual([]);
  });
});

describe('requests', () => {
  it('adds, updates and resolves requests', () => {
    let s = reduce(loaded(), { t: 'request', request: req('r1') });
    s = reduce(s, { t: 'request', request: req('r2', { kind: 'question', tool: 'AskUserQuestion' }) });
    s = reduce(s, { t: 'request', request: req('r1', { reason: 'not in your rules' }) });
    expect(s.requests.map((r) => r.id)).toEqual(['r1', 'r2']);
    expect(s.requests[0].reason).toBe('not in your rules');
    s = reduce(s, { t: 'resolved', id: 'r1' });
    expect(s.requests.map((r) => r.id)).toEqual(['r2']);
  });

  it('keeps the state when an unknown request is resolved', () => {
    const s = reduce(loaded(), { t: 'request', request: req('r1') });
    expect(reduce(s, { t: 'resolved', id: 'other' })).toBe(s);
  });
});

describe('status', () => {
  it('takes the latest status', () => {
    const s = reduce(loaded(), { t: 'status', status: status({ working: true, model: 'claude-sonnet', permissionMode: 'plan', commands: ['review'] }) });
    expect(s.status).toMatchObject({ working: true, model: 'claude-sonnet', permissionMode: 'plan', commands: ['review'] });
  });
});

describe('older items', () => {
  it('puts older items in front, without doubles', () => {
    const s = reduce(loaded([text('c', 'c'), text('d', 'd')], true), { t: 'older', items: [text('a', 'a'), text('b', 'b'), text('c', 'c')], more: false });
    expect(s.items.map((i) => i.id)).toEqual(['a', 'b', 'c', 'd']);
    expect(s.more).toBe(false);
  });
});

describe('grouping', () => {
  it('puts a sub-agent’s items under its tool call', () => {
    const items = [
      text('u', 'go', { kind: 'user' }),
      tool('toolu_task', 'Task'),
      { id: 'p1', kind: 'prompt', at, text: 'Explore', parent: 'toolu_task' } as Item,
      tool('toolu_grep', 'Grep', { parent: 'toolu_task' }),
      text('sub/0', 'found it', { parent: 'toolu_task' }),
      // its parent is further back: it shows in the main conversation
      text('orphan', 'x', { parent: 'toolu_gone' }),
      text('main/0', 'done'),
    ];
    const g = groupItems(items);
    expect(g.top.map((i) => i.id)).toEqual(['u', 'toolu_task', 'orphan', 'main/0']);
    expect(g.children.get('toolu_task')?.map((i) => i.id)).toEqual(['p1', 'toolu_grep', 'sub/0']);
    const st = groupStreams(
      [
        { id: 's1', kind: 'text', text: '', parent: 'toolu_task' },
        { id: 's2', kind: 'text', text: '' },
      ],
      g.ids,
    );
    expect(st.top.map((x) => x.id)).toEqual(['s2']);
    expect(st.children.get('toolu_task')?.map((x) => x.id)).toEqual(['s1']);
  });
});
