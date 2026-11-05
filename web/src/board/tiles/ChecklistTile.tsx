// Checklist: title (double-click renames), items with checkboxes, a progress ring,
// "Clear done" (undo brings the items back).

import { memo, useRef, useState, type KeyboardEvent } from 'react';
import type { NodeProps } from '@xyflow/react';
import { Icon } from '../../components/Icon';
import { itemId } from '../../lib/ids';
import type { FlowNode } from '../flow';
import { readChecklist, type CheckItem } from '../kinds';
import { useBoardEnv } from '../ui';
import { InlineTitle, TileFrame, TileHead } from './common';
import { taskStateLabel, useTaskLink } from './taskLink';

export function ProgressRing({ done, total, size = 28 }: { done: number; total: number; size?: number }) {
  const r = size / 2 - 3;
  const c = 2 * Math.PI * r;
  const frac = total ? done / total : 0;
  return (
    <svg className="progress-ring" width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label={`${done} of ${total} done`}>
      <circle className="ring-track" cx={size / 2} cy={size / 2} r={r} />
      <circle
        className="ring-fill"
        cx={size / 2}
        cy={size / 2}
        r={r}
        strokeDasharray={`${c * frac} ${c}`}
        transform={`rotate(-90 ${size / 2} ${size / 2})`}
      />
    </svg>
  );
}

function ChecklistTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const d = readChecklist(data.node);
  const autoRun = data.node.data.autoRun === true;
  const link = useTaskLink(id, autoRun);
  const { ctl } = useBoardEnv();
  const [adding, setAdding] = useState('');
  const [editingItem, setEditingItem] = useState<string | null>(null);
  const addRef = useRef<HTMLInputElement>(null);
  const done = d.items.filter((i) => i.done).length;

  const setItems = (items: CheckItem[], merge?: string) => ctl.updateData(id, { items }, merge ? { merge } : undefined);

  const add = () => {
    const text = adding.trim();
    if (!text) return;
    setItems([...d.items, { id: itemId(), text, done: false }]);
    setAdding('');
  };

  const onAddKey = (e: KeyboardEvent<HTMLInputElement>) => {
    e.stopPropagation();
    if (e.key === 'Enter') add();
    else if (e.key === 'Escape') e.currentTarget.blur();
  };

  return (
    <TileFrame id={id} kind="checklist" selected={!!selected} label={`Checklist ${d.title}`} minW={220} minH={160}>
      <TileHead
        id={id}
        icon={<ProgressRing done={done} total={d.items.length} size={20} />}
        title={<InlineTitle value={d.title} label="Checklist title" onCommit={(title) => ctl.updateData(id, { title })} />}
        meta={
          <span className="tile-meta tabular">
            {done}/{d.items.length}
            {link.linked && <span className="task-lead"> · {autoRun ? `${link.lead} runs this` : `${link.lead} follows this`}</span>}
          </span>
        }
        menu={link.menu}
        menuLabel={`Actions for ${d.title}`}
      />
      <ul className="check-list nowheel" aria-label="Items">
        {d.items.length === 0 && <li className="tile-empty">No items yet. Add the first one below.</li>}
        {d.items.map((it) => (
          <li key={it.id} className={`check-item ${it.done ? 'done' : ''}`}>
            <button
              type="button"
              role="checkbox"
              aria-checked={it.done}
              aria-label={it.text || 'Item'}
              className="check-box nodrag"
              onClick={() => setItems(d.items.map((x) => (x.id === it.id ? { ...x, done: !x.done } : x)))}
            >
              {it.done && <Icon name="check" />}
            </button>
            {editingItem === it.id ? (
              <input
                className="control control-sm check-input nodrag"
                defaultValue={it.text}
                autoFocus
                aria-label="Item text"
                maxLength={500}
                onBlur={(e) => {
                  setEditingItem(null);
                  const text = e.target.value.trim();
                  if (text && text !== it.text) setItems(d.items.map((x) => (x.id === it.id ? { ...x, text } : x)));
                }}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  if (e.key === 'Enter' || e.key === 'Escape') {
                    if (e.key === 'Escape') e.currentTarget.value = it.text;
                    e.currentTarget.blur();
                  }
                }}
              />
            ) : (
              <span className="check-text" onDoubleClick={(e) => (e.stopPropagation(), setEditingItem(it.id))}>
                {it.text}
              </span>
            )}
            {it.assignee && <span className="kb-chip task-who">@{it.assignee}</span>}
            {it.state && (
              <span className={`kb-chip task-state is-${it.state}`} title={it.error}>
                {taskStateLabel(it.state)}
              </span>
            )}
            <button
              type="button"
              className="btn btn-ghost btn-sm btn-icon check-del nodrag"
              aria-label={`Remove ${it.text || 'item'}`}
              title="Remove item"
              onClick={() => setItems(d.items.filter((x) => x.id !== it.id))}
            >
              <Icon name="close" />
            </button>
          </li>
        ))}
      </ul>
      <div className="tile-foot">
        <div className="input-group has-lead grow">
          <span className="ig-lead">
            <Icon name="plus" />
          </span>
          <input
            ref={addRef}
            className="control control-sm nodrag"
            placeholder="Add an item"
            aria-label="New item"
            value={adding}
            maxLength={500}
            onChange={(e) => setAdding(e.target.value)}
            onKeyDown={onAddKey}
          />
        </div>
        {done > 0 && (
          <button type="button" className="btn btn-ghost btn-sm nodrag" onClick={() => setItems(d.items.filter((x) => !x.done))}>
            Clear done
          </button>
        )}
      </div>
    </TileFrame>
  );
}

export const ChecklistTile = memo(ChecklistTileImpl);
