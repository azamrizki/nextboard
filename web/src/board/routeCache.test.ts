import { describe, expect, it } from 'vitest';
import { routeIn, sameBoxes, snapshotOf, type TileBox } from './routeCache';
import { routeWire } from './wireRoute';

const box = (id: string, x: number, y: number, solid = true): TileBox => ({ id, x, y, w: 100, h: 60, solid });
const fresh = (boxes: TileBox[], s: string, t: string) =>
  routeWire(boxes.find((b) => b.id === s)!, boxes.find((b) => b.id === t)!, boxes.filter((b) => b.solid && b.id !== s && b.id !== t));

describe('wire route cache', () => {
  const a = box('a', 0, 0);
  const b = box('b', 400, 0);
  const c = box('c', 0, 300);
  const d = box('d', 400, 300);

  it('routes like routeWire at rest', () => {
    const boxes = [a, b, c, d];
    const r = routeIn(undefined, snapshotOf(boxes, false), 'a', 'b');
    expect(r.route).toEqual(fresh(boxes, 'a', 'b'));
  });

  it('returns the same answer for the same snapshot', () => {
    const snap = snapshotOf([a, b], false);
    const r = routeIn(undefined, snap, 'a', 'b');
    expect(routeIn(r, snap, 'a', 'b')).toBe(r);
  });

  it('keeps the path of a wire whose ends did not move during a drag', () => {
    const r0 = routeIn(undefined, snapshotOf([a, b, c, d], false), 'a', 'b');
    // c is dragged right between a and b
    const moved = [a, b, { ...c, x: 200, y: 0 }, d];
    const r1 = routeIn(r0, snapshotOf(moved, true), 'a', 'b');
    expect(r1.route).toBe(r0.route);
  });

  it('re-routes a wire with a moving end during a drag', () => {
    const r0 = routeIn(undefined, snapshotOf([a, b, c, d], false), 'a', 'b');
    const moved = [a, { ...b, y: 200 }, c, d];
    const r1 = routeIn(r0, snapshotOf(moved, true), 'a', 'b');
    expect(r1.route).not.toBe(r0.route);
    expect(r1.route).toEqual(fresh(moved, 'a', 'b'));
  });

  it('routes everything afresh once the drag ends', () => {
    const r0 = routeIn(undefined, snapshotOf([a, b, c, d], false), 'a', 'b');
    const moved = [a, b, { ...c, x: 200, y: 0 }, d];
    const during = routeIn(r0, snapshotOf(moved, true), 'a', 'b');
    const after = routeIn(during, snapshotOf(moved, false), 'a', 'b');
    expect(after.route).toEqual(fresh(moved, 'a', 'b'));
  });

  it('ignores a cached answer for other ends', () => {
    const snap = snapshotOf([a, b, c, d], true);
    const ab = routeIn(undefined, snap, 'a', 'b');
    const cd = routeIn(ab, snap, 'c', 'd');
    expect(cd.route).toEqual(fresh([a, b, c, d], 'c', 'd'));
  });

  it('has no route when an end is missing', () => {
    expect(routeIn(undefined, snapshotOf([a], false), 'a', 'b').route).toBeNull();
  });

  it('compares boxes by value', () => {
    expect(sameBoxes([a, b], [{ ...a }, { ...b }])).toBe(true);
    expect(sameBoxes([a, b], [a, { ...b, w: 101 }])).toBe(false);
    expect(sameBoxes([a, b], [a, { ...b, solid: false }])).toBe(false);
  });
});
