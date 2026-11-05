// Calendar: month grid (Monday first) with Today / previous / next, events with an
// optional time, notes and colour. Click a day to add, drag an event to move it,
// right-click an event to edit, duplicate, recolour or delete.

import { memo, useMemo, useState, type CSSProperties } from 'react';
import { createPortal } from 'react-dom';
import type { NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import { Checkbox, Field, TextField } from '../../components/form';
import { ContextMenu, type MenuEntry } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { itemId } from '../../lib/ids';
import type { FlowNode } from '../flow';
import { monthOf, readCalendar, type CalEvent } from '../kinds';
import { TAG_COLORS, TAG_LABELS, type TagColor } from '../palette';
import { useBoardEnv } from '../ui';
import { InlineTitle, TileFrame, TileHead, usePointerDrag } from './common';
import { isoDate, tagSwatches } from './KanbanTile';

const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

function monthGrid(month: string): Date[] {
  const [y, m] = month.split('-').map(Number);
  const first = new Date(y, m - 1, 1);
  const offset = (first.getDay() + 6) % 7; // Monday = 0
  const start = new Date(y, m - 1, 1 - offset);
  return Array.from({ length: 42 }, (_, i) => new Date(start.getFullYear(), start.getMonth(), start.getDate() + i));
}

function shiftMonth(month: string, delta: number): string {
  const [y, m] = month.split('-').map(Number);
  return monthOf(new Date(y, m - 1 + delta, 1));
}

const byTime = (a: CalEvent, b: CalEvent) => (a.time ?? '').localeCompare(b.time ?? '') || a.title.localeCompare(b.title);

function CalendarTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const d = readCalendar(data.node);
  const { ctl } = useBoardEnv();
  const [edit, setEdit] = useState<{ ev: CalEvent | null; date: string } | null>(null);
  const [menu, setMenu] = useState<{ x: number; y: number; ev: CalEvent } | null>(null);
  const [overDate, setOverDate] = useState<string | null>(null);
  const [ghost, setGhost] = useState<{ x: number; y: number; title: string } | null>(null);

  const days = useMemo(() => monthGrid(d.month), [d.month]);
  const today = isoDate(new Date());
  const byDate = useMemo(() => {
    const map = new Map<string, CalEvent[]>();
    for (const e of d.events) {
      const list = map.get(e.date);
      if (list) list.push(e);
      else map.set(e.date, [e]);
    }
    for (const list of map.values()) list.sort(byTime);
    return map;
  }, [d.events]);

  const saveEvents = (events: CalEvent[]) => ctl.updateData(id, { events });
  const putEvent = (ev: CalEvent) => {
    const i = d.events.findIndex((e) => e.id === ev.id);
    saveEvents(i < 0 ? [...d.events, ev] : d.events.map((e) => (e.id === ev.id ? ev : e)));
  };
  const removeEvent = (evId: string) => saveEvents(d.events.filter((e) => e.id !== evId));
  const setMonth = (month: string) => ctl.updateData(id, { month }, { history: false });

  const [y, m] = d.month.split('-').map(Number);
  const monthLabel = new Date(y, m - 1, 1).toLocaleDateString(undefined, { month: 'long', year: 'numeric' });

  // rows that fit the tile: 6 weeks + weekday header
  const rowsH = Math.max(40, (data.node.h - 104) / 6);
  const perCell = Math.max(0, Math.floor((rowsH - 24) / 20));

  const dateAt = (x: number, y2: number): string | null => {
    const el = document.elementFromPoint(x, y2) as HTMLElement | null;
    return el?.closest<HTMLElement>('[data-date]')?.dataset.date ?? null;
  };
  const drag = usePointerDrag<CalEvent>({
    onMove: (ev, x, y2) => {
      setOverDate(dateAt(x, y2));
      setGhost({ x, y: y2, title: ev.title || 'Event' });
    },
    onDrop: (ev, x, y2) => {
      setOverDate(null);
      setGhost(null);
      const date = dateAt(x, y2);
      if (date && date !== ev.date) putEvent({ ...ev, date });
    },
    onCancel: () => {
      setOverDate(null);
      setGhost(null);
    },
  });

  const eventMenu = (ev: CalEvent): MenuEntry[] => [
    { label: 'Edit…', icon: 'pencil', onSelect: () => setEdit({ ev, date: ev.date }) },
    { label: 'Duplicate', icon: 'duplicate', onSelect: () => putEvent({ ...ev, id: itemId() }) },
    { type: 'swatches', label: 'Colour', value: ev.color ?? 'sky', options: tagSwatches, onSelect: (v) => putEvent({ ...ev, color: v as TagColor }) },
    'sep',
    { label: 'Delete event', icon: 'trash', danger: true, onSelect: () => removeEvent(ev.id) },
  ];

  return (
    <TileFrame id={id} kind="calendar" selected={!!selected} label={`Calendar ${d.title}`} minW={420} minH={340}>
      <TileHead
        id={id}
        icon="calendar"
        title={<InlineTitle value={d.title} label="Calendar title" onCommit={(title) => ctl.updateData(id, { title })} />}
        actions={
          <div className="cal-nav nodrag">
            <Button size="sm" variant="ghost" iconOnly icon="chevronLeft" aria-label="Previous month" onClick={() => setMonth(shiftMonth(d.month, -1))} />
            <span className="cal-month tabular" aria-live="polite">
              {monthLabel}
            </span>
            <Button size="sm" variant="ghost" iconOnly icon="chevronRight" aria-label="Next month" onClick={() => setMonth(shiftMonth(d.month, 1))} />
            <Button size="sm" variant="ghost" onClick={() => setMonth(monthOf(new Date()))} disabled={d.month === monthOf(new Date())}>
              Today
            </Button>
            <Button size="sm" variant="ghost" icon="plus" onClick={() => setEdit({ ev: null, date: d.month === monthOf(new Date()) ? today : `${d.month}-01` })}>
              New
            </Button>
          </div>
        }
        menuLabel={`Actions for ${d.title}`}
      />
      <div className="cal-grid nodrag nowheel" role="grid" aria-label={monthLabel}>
        {WEEKDAYS.map((w) => (
          <div key={w} className="cal-wd" role="columnheader">
            {w}
          </div>
        ))}
        {days.map((day) => {
          const iso = isoDate(day);
          const evs = byDate.get(iso) ?? [];
          const inMonth = day.getMonth() === m - 1;
          const shown = evs.length > perCell ? evs.slice(0, Math.max(0, perCell - 1)) : evs;
          const more = evs.length - shown.length;
          return (
            <div
              key={iso}
              role="gridcell"
              data-date={iso}
              className={`cal-day ${inMonth ? '' : 'other'} ${iso === today ? 'today' : ''} ${overDate === iso ? 'is-target' : ''}`}
              onClick={(e) => {
                if (e.target === e.currentTarget || (e.target as HTMLElement).classList.contains('cal-num')) setEdit({ ev: null, date: iso });
              }}
              aria-label={`${day.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' })}${
                evs.length ? `, ${evs.length} event${evs.length > 1 ? 's' : ''}` : ''
              }`}
            >
              <span className="cal-num tabular">{day.getDate()}</span>
              {shown.map((ev) => (
                <button
                  key={ev.id}
                  type="button"
                  className={`cal-ev ${drag.dragging?.id === ev.id ? 'is-dragging' : ''}`}
                  style={{ '--ev-color': `var(--tag-${ev.color ?? 'sky'})` } as CSSProperties}
                  title={`${ev.time ? `${ev.time} ` : ''}${ev.title}${ev.notes ? `\n${ev.notes}` : ''}`}
                  onPointerDown={(e) => drag.begin(e, ev)}
                  onClick={() => setEdit({ ev, date: ev.date })}
                  onContextMenu={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setMenu({ x: e.clientX, y: e.clientY, ev });
                  }}
                >
                  {ev.time && <span className="cal-ev-time tabular">{ev.time}</span>}
                  <span className="truncate">{ev.title || 'Untitled'}</span>
                </button>
              ))}
              {more > 0 && (
                <button type="button" className="cal-more" onClick={() => setEdit({ ev: null, date: iso })} title={evs.slice(shown.length).map((e) => e.title).join('\n')}>
                  +{more} more
                </button>
              )}
            </div>
          );
        })}
      </div>

      {ghost &&
        createPortal(
          <div className="drag-ghost" style={{ left: ghost.x, top: ghost.y }} aria-hidden="true">
            {ghost.title}
          </div>,
          document.body,
        )}
      {menu && <ContextMenu x={menu.x} y={menu.y} label={`Actions for ${menu.ev.title}`} items={eventMenu(menu.ev)} onClose={() => setMenu(null)} />}
      {edit && (
        <EventDialog
          ev={edit.ev}
          date={edit.date}
          onClose={() => setEdit(null)}
          onSave={(ev) => {
            putEvent(ev);
            setEdit(null);
          }}
          onDelete={
            edit.ev
              ? () => {
                  removeEvent(edit.ev!.id);
                  setEdit(null);
                }
              : undefined
          }
        />
      )}
    </TileFrame>
  );
}

export const CalendarTile = memo(CalendarTileImpl);

function EventDialog({
  ev,
  date,
  onSave,
  onClose,
  onDelete,
}: {
  ev: CalEvent | null;
  date: string;
  onSave: (e: CalEvent) => void;
  onClose: () => void;
  onDelete?: () => void;
}) {
  const [title, setTitle] = useState(ev?.title ?? '');
  const [day, setDay] = useState(ev?.date ?? date);
  const [allDay, setAllDay] = useState(!ev?.time);
  const [time, setTime] = useState(ev?.time ?? '09:00');
  const [notes, setNotes] = useState(ev?.notes ?? '');
  const [color, setColor] = useState<TagColor>(ev?.color ?? 'sky');
  const [error, setError] = useState('');

  const submit = () => {
    if (!title.trim()) {
      setError('Give the event a title.');
      return;
    }
    if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) {
      setError('Pick a date.');
      return;
    }
    const next: CalEvent = { id: ev?.id ?? itemId(), title: title.trim(), date: day, color };
    if (!allDay && /^\d{2}:\d{2}$/.test(time)) next.time = time;
    if (notes.trim()) next.notes = notes.trim();
    onSave(next);
  };

  return (
    <Modal
      title={ev ? 'Edit event' : 'New event'}
      icon="calendar"
      size="sm"
      onClose={onClose}
      help={
        onDelete ? (
          <Button variant="danger-ghost" size="sm" icon="trash" onClick={onDelete}>
            Delete event
          </Button>
        ) : undefined
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" icon="check" onClick={submit}>
            Save event
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
        <div className="field-row">
          <TextField fieldClassName="span-7" label="Date" type="date" value={day} onChange={(e) => setDay(e.target.value)} />
          <TextField fieldClassName="span-5" label="Time" type="time" value={time} disabled={allDay} onChange={(e) => setTime(e.target.value)} />
        </div>
        <Checkbox checked={allDay} onChange={setAllDay} label="All day" />
        <Field label="Notes" optional>
          {({ labelId: _l, ...a11y }) => (
            <textarea className="control prose" rows={3} value={notes} maxLength={4000} onChange={(e) => setNotes(e.target.value)} {...a11y} />
          )}
        </Field>
        <Field label="Colour" as="group">
          {({ labelId }) => (
            <div className="swatch-row" role="radiogroup" aria-labelledby={labelId}>
              {TAG_COLORS.map((c) => (
                <button
                  key={c}
                  type="button"
                  role="radio"
                  aria-checked={color === c}
                  aria-label={TAG_LABELS[c]}
                  title={TAG_LABELS[c]}
                  className="swatch-btn"
                  style={{ '--swatch': `var(--tag-${c})` } as CSSProperties}
                  onClick={() => setColor(c)}
                />
              ))}
            </div>
          )}
        </Field>
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}
