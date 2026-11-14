import { describe, expect, it } from 'vitest';
import type { ThreadMessage } from './types';
import { mergeMessages, RECORDS_KEEP, THREAD_KEEP, withRecord } from './store';

const msg = (i: number, at = `2026-11-14T10:${String(Math.floor(i / 60) % 60).padStart(2, '0')}:${String(i % 60).padStart(2, '0')}.000Z`): ThreadMessage =>
  ({ id: `m${String(i).padStart(5, '0')}`, at, role: 'agent', kind: 'reply', text: `#${i}` }) as unknown as ThreadMessage;

describe('what a tab keeps of the orchestration', () => {
  it('keeps the newest records, the one just written among them', () => {
    let rec: Record<string, { id: string; n: number }> = {};
    for (let i = 0; i < RECORDS_KEEP + 30; i++) rec = withRecord(rec, { id: `r${i}`, n: i });
    expect(Object.keys(rec)).toHaveLength(RECORDS_KEEP);
    expect(rec.r0).toBeUndefined();
    expect(rec[`r${RECORDS_KEEP + 29}`].n).toBe(RECORDS_KEEP + 29);
    const oldest = Object.keys(rec)[0];
    rec = withRecord(rec, { id: oldest, n: -1 }); // an update of the oldest keeps it
    expect(rec[oldest].n).toBe(-1);
  });

  it('keeps a thread to the hub’s length, in order, with updates in place', () => {
    let list: ThreadMessage[] = [];
    for (let i = 0; i < THREAD_KEEP + 10; i++) list = mergeMessages(list, [msg(i)]);
    expect(list).toHaveLength(THREAD_KEEP);
    expect(list[0].id).toBe('m00010');
    expect(list[list.length - 1].id).toBe(`m${String(THREAD_KEEP + 9).padStart(5, '0')}`);
    const upd = { ...list[3], text: 'changed' } as ThreadMessage;
    list = mergeMessages(list, [upd]);
    expect(list).toHaveLength(THREAD_KEEP);
    expect((list[3] as unknown as { text: string }).text).toBe('changed');
    // one that comes late goes where its time puts it
    const late = msg(20);
    list = mergeMessages(list.filter((m) => m.id !== late.id), [late]);
    expect(list.map((m) => m.id)).toEqual([...list.map((m) => m.id)].sort());
  });
});
