// Kanban: columns (rename, colour, sort by priority, move, clear done, delete) with
// cards (title, notes, due date, priority, done). Cards drag between columns with the
// pointer (works under any zoom and on touch); every change is one undo step.

import { memo, useRef, useState, type CSSProperties } from 'react';
import { createPortal } from 'react-dom';
import type { NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Checkbox, Field, SegmentedControl, TextField } from '../../components/form';
import { Icon } from '../../components/Icon';
import { Menu, type MenuEntry } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { itemId } from '../../lib/ids';
import type { FlowNode } from '../flow';
import { PRIORITIES, PRIORITY_LABEL, readKanban, type KanbanCard, type KanbanColumn, type Priority } from '../kinds';
import { TAG_COLORS, TAG_LABELS, type TagColor } from '../palette';
import { useBoardEnv } from '../ui';
import { InlineTitle, TileFrame, TileHead, usePointerDrag } from './common';
import { taskStateLabel, useTaskLink } from './taskLink';

const PRIORITY_RANK: Record<string, number> = { high: 0, med: 1, low: 2 };

export const tagSwatches = TAG_COLORS.map((c) => ({ value: c, label: TAG_LABELS[c], color: `var(--tag-${c})` }));

export function isoDate(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}
export function addDays(d: Date, n: number): Date {
  const x = new Date(d);
  x.setDate(x.getDate() + n);
  return x;
}
function dueLabel(due: string): { text: string; overdue: boolean; soon: boolean } {
  const today = isoDate(new Date());
  const tomorrow = isoDate(addDays(new Date(), 1));
  const [y, m, d] = due.split('-').map(Number);
  const date = new Date(y, m - 1, d);
  const text =
    due === today
      ? 'Today'
      : due === tomorrow
        ? 'Tomorrow'
        : date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  return { text, overdue: due < today, soon: due === today || due === tomorrow };
}

interface DropTarget {
  col: number;
  index: number;
}

function KanbanTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const d = readKanban(data.node);
  const { ctl } = useBoardEnv();
  const autoRun = data.node.data.autoRun === true;
  const link = useTaskLink(id, autoRun);
  const [editCard, setEditCard] = useState<{ col: string; card: KanbanCard | null } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<KanbanColumn | null>(null);
  const [adding, setAdding] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [drop, setDrop] = useState<DropTarget | null>(null);
  const [ghost, setGhost] = useState<{ x: number; y: number; title: string } | null>(null);
  const colsRef = useRef<HTMLDivElement>(null);

  const save = (columns: KanbanColumn[]) => ctl.updateData(id, { columns });
  const mapCol = (colId: string, fn: (c: KanbanColumn) => KanbanColumn) => save(d.columns.map((c) => (c.id === colId ? fn(c) : c)));
  const total = d.columns.reduce((n, c) => n + c.cards.length, 0);

  // ---- drag & drop of cards ----
  const hitTest = (x: number, y: number): DropTarget | null => {
    const cols = Array.from(colsRef.current?.querySelectorAll<HTMLElement>('.kb-col') ?? []);
    for (let ci = 0; ci < cols.length; ci++) {
      const r = cols[ci].getBoundingClientRect();
      if (x < r.left || x > r.right || y < r.top - 20 || y > r.bottom + 20) continue;
      const cards = Array.from(cols[ci].querySelectorAll<HTMLElement>('.kb-card'));
      let index = cards.length;
      for (let k = 0; k < cards.length; k++) {
        const cr = cards[k].getBoundingClientRect();
        if (y < cr.top + cr.height / 2) {
          index = k;
          break;
        }
      }
      return { col: ci, index };
    }
    return null;
  };
  const drag = usePointerDrag<{ col: number; card: KanbanCard }>({
    onMove: (it, x, y) => {
      setDrop(hitTest(x, y));
      setGhost({ x, y, title: it.card.title || 'Card' });
    },
    onDrop: (it, x, y) => {
      setGhost(null);
      setDrop(null);
      const t = hitTest(x, y);
      if (!t) return;
      const cols = d.columns.map((c) => ({ ...c, cards: c.cards.slice() }));
      const from = cols[it.col];
      const fromIndex = from.cards.findIndex((c) => c.id === it.card.id);
      if (fromIndex < 0) return;
      let index = t.index;
      if (t.col === it.col && index > fromIndex) index--;
      if (t.col === it.col && index === fromIndex) return;
      from.cards.splice(fromIndex, 1);
      cols[t.col].cards.splice(index, 0, it.card);
      save(cols);
    },
    onCancel: () => {
      setGhost(null);
      setDrop(null);
    },
  });

  // ---- column actions ----
  const moveCol = (ci: number, to: number) => {
    if (to < 0 || to >= d.columns.length || to === ci) return;
    const cols = d.columns.slice();
    const [c] = cols.splice(ci, 1);
    cols.splice(to, 0, c);
    save(cols);
  };
  const addColumn = () => {
    const color = TAG_COLORS[d.columns.length % TAG_COLORS.length];
    const col = { id: itemId(), title: 'New column', color, cards: [] };
    save([...d.columns, col]);
    setRenaming(col.id);
  };
  const deleteColumn = (col: KanbanColumn) => save(d.columns.filter((c) => c.id !== col.id));

  const columnMenu = (col: KanbanColumn, ci: number): MenuEntry[] => [
    { label: 'Rename', icon: 'pencil', onSelect: () => setRenaming(col.id) },
    { label: 'Add card', icon: 'plus', onSelect: () => setAdding(col.id) },
    {
      type: 'swatches',
      label: 'Colour',
      value: col.color,
      options: tagSwatches,
      onSelect: (v) => mapCol(col.id, (c) => ({ ...c, color: v as TagColor })),
    },
    {
      label: 'Sort by priority',
      icon: 'sort',
      disabled: col.cards.length < 2,
      onSelect: () =>
        mapCol(col.id, (c) => ({
          ...c,
          cards: c.cards
            .map((card, i) => ({ card, i }))
            .sort((a, b) => (PRIORITY_RANK[a.card.priority ?? ''] ?? 3) - (PRIORITY_RANK[b.card.priority ?? ''] ?? 3) || a.i - b.i)
            .map((x) => x.card),
        })),
    },
    'sep',
    { label: 'Move left', icon: 'arrowLeft', disabled: ci === 0, onSelect: () => moveCol(ci, ci - 1) },
    { label: 'Move right', icon: 'arrowRight', disabled: ci === d.columns.length - 1, onSelect: () => moveCol(ci, ci + 1) },
    { label: 'Move to start', icon: 'chevronLeft', disabled: ci === 0, onSelect: () => moveCol(ci, 0) },
    { label: 'Move to end', icon: 'chevronRight', disabled: ci === d.columns.length - 1, onSelect: () => moveCol(ci, d.columns.length - 1) },
    'sep',
    {
      label: 'Clear done cards',
      icon: 'broom',
      disabled: !col.cards.some((c) => c.done),
      onSelect: () => mapCol(col.id, (c) => ({ ...c, cards: c.cards.filter((x) => !x.done) })),
    },
    {
      label: 'Delete column…',
      icon: 'trash',
      danger: true,
      onSelect: () => (col.cards.length ? setConfirmDelete(col) : deleteColumn(col)),
    },
  ];

  // ---- card actions ----
  const putCard = (colId: string, card: KanbanCard) =>
    mapCol(colId, (c) => {
      const i = c.cards.findIndex((x) => x.id === card.id);
      if (i < 0) return { ...c, cards: [...c.cards, card] };
      const cards = c.cards.slice();
      cards[i] = card;
      return { ...c, cards };
    });
  const removeCard = (colId: string, cardId: string) => mapCol(colId, (c) => ({ ...c, cards: c.cards.filter((x) => x.id !== cardId) }));
  const moveCardTo = (fromCol: string, card: KanbanCard, toCol: string) =>
    save(
      d.columns.map((c) =>
        c.id === fromCol && c.id !== toCol
          ? { ...c, cards: c.cards.filter((x) => x.id !== card.id) }
          : c.id === toCol && c.id !== fromCol
            ? { ...c, cards: [...c.cards, card] }
            : c,
      ),
    );

  const cardMenu = (col: KanbanColumn, card: KanbanCard): MenuEntry[] => [
    {
      label: card.done ? 'Mark as not done' : 'Mark as done',
      icon: 'check',
      onSelect: () => putCard(col.id, { ...card, done: !card.done || undefined }),
    },
    { label: 'Edit…', icon: 'pencil', onSelect: () => setEditCard({ col: col.id, card }) },
    {
      label: 'Duplicate',
      icon: 'duplicate',
      onSelect: () =>
        mapCol(col.id, (c) => {
          const i = c.cards.findIndex((x) => x.id === card.id);
          const cards = c.cards.slice();
          cards.splice(i + 1, 0, { ...card, id: itemId() });
          return { ...c, cards };
        }),
    },
    ...(link.team.length
      ? ([
          { type: 'heading', label: 'Assign to' },
          ...link.team.map((s): MenuEntry => ({ label: `@${s.handle ?? s.name}`, icon: 'user', checked: card.assignee === s.handle, onSelect: () => putCard(col.id, { ...card, assignee: s.handle }) })),
          ...(card.assignee ? [{ label: 'Nobody yet', icon: 'close', onSelect: () => putCard(col.id, { ...card, assignee: undefined }) } as MenuEntry] : []),
        ] as MenuEntry[])
      : []),
    { type: 'heading', label: 'Move to' },
    ...d.columns
      .filter((c) => c.id !== col.id)
      .map((c) => ({ label: c.title, icon: 'arrowRight' as const, onSelect: () => moveCardTo(col.id, card, c.id) })),
    'sep',
    { label: 'Delete card', icon: 'trash', danger: true, onSelect: () => removeCard(col.id, card.id) },
  ];

  return (
    <TileFrame id={id} kind="kanban" selected={!!selected} label={`Kanban ${d.title}`} minW={420} minH={260}>
      <TileHead
        id={id}
        icon="kanban"
        title={<InlineTitle value={d.title} label="Board title" onCommit={(title) => ctl.updateData(id, { title })} />}
        meta={
          <span className="tile-meta tabular">
            {total === 1 ? '1 card' : `${total} cards`}
            {link.linked && <span className="task-lead"> · {autoRun ? `${link.lead} runs this` : `${link.lead} follows this`}</span>}
          </span>
        }
        menu={link.menu}
        actions={
          <Button size="sm" variant="ghost" icon="plus" className="nodrag" onClick={addColumn}>
            Add column
          </Button>
        }
        menuLabel={`Actions for ${d.title}`}
      />
      <div className="kb-cols nodrag nowheel" ref={colsRef}>
        {d.columns.map((col, ci) => (
          <section
            key={col.id}
            className={`kb-col ${drop?.col === ci ? 'is-target' : ''}`}
            style={{ '--col-color': `var(--tag-${col.color ?? 'sky'})` } as CSSProperties}
            aria-label={`${col.title}, ${col.cards.length} cards`}
          >
            <header className="kb-col-head">
              <span className="kb-col-dot" aria-hidden="true" />
              {renaming === col.id ? (
                <input
                  className="control control-sm tile-title-input"
                  defaultValue={col.title}
                  autoFocus
                  maxLength={60}
                  aria-label="Column name"
                  onFocus={(e) => e.currentTarget.select()}
                  onBlur={(e) => {
                    setRenaming(null);
                    const title = e.target.value.trim();
                    if (title && title !== col.title) mapCol(col.id, (c) => ({ ...c, title }));
                  }}
                  onKeyDown={(e) => {
                    e.stopPropagation();
                    if (e.key === 'Enter') e.currentTarget.blur();
                    if (e.key === 'Escape') {
                      e.currentTarget.value = col.title;
                      e.currentTarget.blur();
                    }
                  }}
                />
              ) : (
                <span className="kb-col-title" onDoubleClick={() => setRenaming(col.id)} title={col.title}>
                  {col.title}
                </span>
              )}
              <span className="kb-count tabular">{col.cards.length}</span>
              <span className="spacer" />
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                icon="plus"
                aria-label={`Add card to ${col.title}`}
                title="Add card"
                onClick={() => setAdding(col.id)}
              />
              <Menu label={`${col.title} column actions`} items={columnMenu(col, ci)} />
            </header>
            <ol className="kb-cards">
              {col.cards.map((card, k) => {
                const due = card.due ? dueLabel(card.due) : null;
                return (
                  <li
                    key={card.id}
                    className={`kb-card ${card.done ? 'done' : ''} ${drag.dragging?.card.id === card.id ? 'is-dragging' : ''} ${
                      drop?.col === ci && drop.index === k ? 'drop-before' : ''
                    }`}
                    onPointerDown={(e) => {
                      if ((e.target as HTMLElement).closest('.menu')) return;
                      drag.begin(e, { col: ci, card });
                    }}
                  >
                    <button
                      type="button"
                      className="kb-card-main"
                      onClick={() => setEditCard({ col: col.id, card })}
                      aria-label={`Edit card ${card.title || 'Untitled'}`}
                    >
                      <span className="kb-card-title">
                        {card.done && <Icon name="check" className="kb-done-icon" />}
                        {card.title || 'Untitled'}
                      </span>
                      {card.notes && <span className="kb-card-notes">{card.notes}</span>}
                      {(due || card.priority || card.assignee || card.state) && (
                        <span className="kb-chips">
                          {card.assignee && <span className="kb-chip task-who">@{card.assignee}</span>}
                          {card.state && (
                            <span className={`kb-chip task-state is-${card.state}`} title={card.error}>
                              {taskStateLabel(card.state)}
                            </span>
                          )}
                          {card.priority && <span className={`kb-chip prio-${card.priority}`}>{PRIORITY_LABEL[card.priority]}</span>}
                          {due && (
                            <span className={`kb-chip ${due.overdue && !card.done ? 'overdue' : due.soon ? 'soon' : ''}`}>
                              <Icon name="clock" />
                              {due.text}
                            </span>
                          )}
                        </span>
                      )}
                    </button>
                    <Menu className="kb-card-menu" label={`Actions for ${card.title || 'card'}`} items={cardMenu(col, card)} />
                  </li>
                );
              })}
              {drop?.col === ci && drop.index === col.cards.length && <li className="kb-drop-end" aria-hidden="true" />}
            </ol>
            {adding === col.id ? (
              <input
                className="control control-sm kb-add-input"
                autoFocus
                placeholder="Card title, Enter to add"
                aria-label={`New card in ${col.title}`}
                maxLength={200}
                onBlur={() => setAdding(null)}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  const v = e.currentTarget.value.trim();
                  if (e.key === 'Enter' && v) {
                    putCard(col.id, { id: itemId(), title: v });
                    e.currentTarget.value = '';
                  } else if (e.key === 'Escape') setAdding(null);
                }}
              />
            ) : (
              <button type="button" className="kb-add" onClick={() => setAdding(col.id)}>
                <Icon name="plus" />
                Add card
              </button>
            )}
          </section>
        ))}
        {d.columns.length === 0 && (
          <div className="tile-empty">
            No columns. <button type="button" className="link-btn" onClick={addColumn}>Add one</button>
          </div>
        )}
      </div>

      {ghost &&
        createPortal(
          <div className="drag-ghost" style={{ left: ghost.x, top: ghost.y }} aria-hidden="true">
            {ghost.title}
          </div>,
          document.body,
        )}

      {editCard && (
        <CardDialog
          card={editCard.card}
          onClose={() => setEditCard(null)}
          onSave={(card) => {
            putCard(editCard.col, card);
            setEditCard(null);
          }}
          onDelete={
            editCard.card
              ? () => {
                  removeCard(editCard.col, editCard.card!.id);
                  setEditCard(null);
                }
              : undefined
          }
        />
      )}
      {confirmDelete && (
        <ConfirmDialog
          title={`Delete “${confirmDelete.title}”?`}
          confirmLabel="Delete column"
          danger
          onClose={() => setConfirmDelete(null)}
          onConfirm={() => deleteColumn(confirmDelete)}
        >
          <p>
            The column and its {confirmDelete.cards.length === 1 ? 'card' : `${confirmDelete.cards.length} cards`} are removed. Undo
            (Ctrl+Z) brings them back.
          </p>
        </ConfirmDialog>
      )}
    </TileFrame>
  );
}

export const KanbanTile = memo(KanbanTileImpl);

// ---- card editor ----

function CardDialog({
  card,
  onSave,
  onClose,
  onDelete,
}: {
  card: KanbanCard | null;
  onSave: (c: KanbanCard) => void;
  onClose: () => void;
  onDelete?: () => void;
}) {
  const [title, setTitle] = useState(card?.title ?? '');
  const [notes, setNotes] = useState(card?.notes ?? '');
  const [due, setDue] = useState(card?.due ?? '');
  const [priority, setPriority] = useState<Priority | 'none'>(card?.priority ?? 'none');
  const [done, setDone] = useState(!!card?.done);
  const [error, setError] = useState('');

  const submit = () => {
    if (!title.trim()) {
      setError('Give the card a title.');
      return;
    }
    const next: KanbanCard = { id: card?.id ?? itemId(), title: title.trim() };
    if (notes.trim()) next.notes = notes.trim();
    if (due) next.due = due;
    if (priority !== 'none') next.priority = priority;
    if (done) next.done = true;
    onSave(next);
  };
  const today = new Date();
  const quick = [
    { label: 'Today', value: isoDate(today) },
    { label: 'Tomorrow', value: isoDate(addDays(today, 1)) },
    { label: 'Next week', value: isoDate(addDays(today, 7)) },
  ];

  return (
    <Modal
      title={card ? 'Edit card' : 'New card'}
      icon="kanban"
      size="sm"
      onClose={onClose}
      help={
        onDelete ? (
          <Button variant="danger-ghost" size="sm" icon="trash" onClick={onDelete}>
            Delete card
          </Button>
        ) : undefined
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" icon="check" onClick={submit}>
            Save card
          </Button>
        </>
      }
    >
      <form
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <TextField label="Title" value={title} autoFocus maxLength={200} error={error || undefined} onChange={(e) => (setTitle(e.target.value), setError(''))} />
        <Field label="Notes" optional>
          {({ labelId: _l, ...a11y }) => (
            <textarea className="control prose" rows={4} value={notes} maxLength={4000} onChange={(e) => setNotes(e.target.value)} {...a11y} />
          )}
        </Field>
        <Field label="Due date" optional>
          {({ labelId: _l, ...a11y }) => (
            <div className="stack-sm">
              <input type="date" className="control" value={due} onChange={(e) => setDue(e.target.value)} {...a11y} />
              <div className="chip-row">
                {quick.map((q) => (
                  <button key={q.label} type="button" className={`chip-btn ${due === q.value ? 'on' : ''}`} onClick={() => setDue(q.value)}>
                    {q.label}
                  </button>
                ))}
                {due && (
                  <button type="button" className="chip-btn" onClick={() => setDue('')}>
                    No date
                  </button>
                )}
              </div>
            </div>
          )}
        </Field>
        <Field label="Priority" as="group">
          {({ labelId }) => (
            <SegmentedControl<Priority | 'none'>
              labelledBy={labelId}
              value={priority}
              onChange={setPriority}
              block
              options={[{ value: 'none', label: 'None' }, ...PRIORITIES.map((p) => ({ value: p, label: PRIORITY_LABEL[p] }))]}
            />
          )}
        </Field>
        <Checkbox checked={done} onChange={setDone} label="Done" />
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}
