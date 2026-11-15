// Picking several things in a list to act on them at once (DESIGN.md §3.18): a click or a tap
// toggles one, Shift picks the run from the last one clicked, Ctrl/⌘ toggles one, and "Select
// all" takes what is shown. Pure, so the rules are tested apart from any page (selection.test.ts).

export interface Selection {
  ids: ReadonlySet<string>;
  /** Where a Shift range starts: the last one clicked. */
  anchor: string | null;
}

export const NO_SELECTION: Selection = { ids: new Set(), anchor: null };

export interface ClickMods {
  shift?: boolean;
  /** Ctrl, or ⌘ on a Mac. */
  toggle?: boolean;
}

/** Whether an id may be picked (a machine in use may not). */
export type Selectable = (id: string) => boolean;

const always: Selectable = () => true;

/**
 * One click on `id`, with `order` what is on screen top to bottom. Shift adds every pickable one
 * from the anchor to it; anything else toggles it and makes it the anchor.
 */
export function clickSelect(sel: Selection, id: string, order: readonly string[], mods: ClickMods = {}, selectable: Selectable = always): Selection {
  if (!selectable(id)) return sel;
  if (mods.shift && sel.anchor && sel.anchor !== id) {
    const from = order.indexOf(sel.anchor);
    const to = order.indexOf(id);
    if (from >= 0 && to >= 0) {
      const ids = new Set(sel.ids);
      const [lo, hi] = from < to ? [from, to] : [to, from];
      for (const x of order.slice(lo, hi + 1)) if (selectable(x)) ids.add(x);
      // the anchor stays, so a second Shift-click changes the run's other end
      return { ids, anchor: sel.anchor };
    }
  }
  const ids = new Set(sel.ids);
  if (!ids.delete(id)) ids.add(id);
  return { ids, anchor: id };
}

/** How many of what is shown are picked: none, some, or every one that may be. */
export function visibleState(sel: Selection, visible: readonly string[], selectable: Selectable = always): 'none' | 'some' | 'all' {
  const open = visible.filter(selectable);
  const picked = open.filter((id) => sel.ids.has(id)).length;
  if (!picked) return 'none';
  return picked === open.length ? 'all' : 'some';
}

/** "Select all": every pickable one of what is shown; when they all are already, none of them. */
export function toggleAllVisible(sel: Selection, visible: readonly string[], selectable: Selectable = always): Selection {
  const ids = new Set(sel.ids);
  const open = visible.filter(selectable);
  if (visibleState(sel, visible, selectable) === 'all') for (const id of open) ids.delete(id);
  else for (const id of open) ids.add(id);
  return { ids, anchor: sel.anchor };
}

/**
 * Only what is still there and still pickable: a project archived elsewhere, or a filter that hides
 * one, drops it from the selection, so an action never reaches something the user cannot see.
 */
export function pruneSelection(sel: Selection, present: readonly string[], selectable: Selectable = always): Selection {
  const keep = new Set(present);
  let changed = false;
  const ids = new Set<string>();
  for (const id of sel.ids) {
    if (keep.has(id) && selectable(id)) ids.add(id);
    else changed = true;
  }
  const anchor = sel.anchor && keep.has(sel.anchor) ? sel.anchor : null;
  if (!changed && anchor === sel.anchor) return sel;
  return { ids, anchor };
}

/** The picked ones in the order they are shown. */
export function pickedInOrder(sel: Selection, order: readonly string[]): string[] {
  return order.filter((id) => sel.ids.has(id));
}
