import { beforeEach, describe, expect, it } from 'vitest';
import type { HostSnapshot } from '../lib/types';
import { bytes, hostState, levelOf, memPercent, resetHost, takeSnapshot, upFor } from './store';

const snap = (at: string, cpu: number, extra: Partial<HostSnapshot> = {}): HostSnapshot => ({
  at,
  supported: true,
  os: 'linux',
  hostname: 'devbox',
  uptimeSec: 100,
  cpu: { model: 'AMD Ryzen 5 3600XT 6-Core Processor', threads: 12, percent: cpu, cores: [], load: [1, 1, 1] },
  memory: { total: 16e9, used: 6.4e9, available: 9.6e9, cached: 5e9, swapTotal: 0, swapUsed: 0 },
  gpus: [{ name: 'Intel Arc A380', vendor: 'intel', driver: 'i915', busy: 8, tempC: 55, powerW: 17, vramUsed: null, vramTotal: null }],
  temps: [],
  disks: [],
  hottest: { kind: 'cpu', label: 'CPU', c: 62, sensor: 'k10temp' },
  level: 'ok',
  why: '',
  ...extra,
});

describe("This PC's readings", () => {
  beforeEach(() => resetHost());

  it('keeps a point of history every 15 s, not one per reading', () => {
    for (const [at, cpu] of [
      ['10:00:00', 10],
      ['10:00:05', 20],
      ['10:00:10', 30],
      ['10:00:15', 40],
      ['10:00:20', 50],
      ['10:00:30', 60],
    ] as const)
      takeSnapshot(snap(`2026-11-22T${at}Z`, cpu));
    const h = hostState().history;
    expect(h.map((p) => p.cpu)).toEqual([10, 40, 60]);
    expect(h[0]).toMatchObject({ mem: 40, gpu: 8, temp: 62 });
    expect(hostState().snap?.cpu.percent).toBe(60);
  });

  it('an event keeps the consoles the details named while they look, and drops them after', () => {
    const consoles = [{ sessionId: 's_a', cpu: 5, rss: 1, procs: 1, handle: 'Raka', name: 'Claude Code', agent: 'claude', projectId: 'p', projectName: 'Shop', workspaceId: 'w' }];
    takeSnapshot(snap('2026-11-22T10:00:00Z', 10, { consoles }));
    takeSnapshot(snap('2026-11-22T10:00:05Z', 20), true);
    expect(hostState().snap?.consoles).toEqual(consoles);
    expect(hostState().snap?.cpu.percent).toBe(20);
    takeSnapshot(snap('2026-11-22T10:00:10Z', 30), false);
    expect(hostState().snap?.consoles).toBeUndefined();
  });

  it('reads memory, levels, sizes and uptime as people say them', () => {
    const s = snap('2026-11-22T10:00:00Z', 10);
    expect(memPercent(s)).toBe(40);
    expect(levelOf(null)).toBe('ok');
    expect(levelOf({ ...s, level: '' })).toBe('ok');
    expect(levelOf({ ...s, level: 'hot' })).toBe('hot');
    expect(bytes(512)).toBe('512 B');
    expect(bytes(430 * 2 ** 20)).toBe('430 MB');
    expect(bytes(6.4 * 2 ** 30)).toBe('6.4 GB');
    expect(upFor(13 * 86400 + 5)).toBe('up 13 days');
    expect(upFor(5 * 3600)).toBe('up 5 h');
    expect(upFor(30)).toBe('up 1 min');
  });
});
