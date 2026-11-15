// The Groups view of Home (DESIGN.md §3.11): every group as a row, indented under its parent,
// with how many projects it holds, and the select mode of §3.18 to delete several at once.
// Deleting a group removes no project: what was inside moves up a level.

import type { MouseEvent as ReactMouseEvent } from 'react';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Menu } from '../components/Menu';
import { SelectBox, type SelectMode } from '../components/SelectBar';
import type { ProjectGroup } from '../lib/types';
import type { GroupNode } from './groups';

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

interface GroupListProps {
  /** Top to bottom, a group before what is inside it. */
  rows: GroupNode[];
  select: SelectMode;
  onOpen: (g: ProjectGroup) => void;
  onSubgroup: (g: ProjectGroup) => void;
  onEdit: (g: ProjectGroup) => void;
  onDelete: (g: ProjectGroup) => void;
  onNew: () => void;
  /** A filter hides the others. */
  filtering?: boolean;
}

export function GroupList({ rows, select, onOpen, onSubgroup, onEdit, onDelete, onNew, filtering }: GroupListProps) {
  if (!rows.length && filtering) {
    return (
      <div className="center-msg" role="status">
        <p>No group matches the filter.</p>
      </div>
    );
  }
  if (!rows.length) {
    return (
      <div className="card home-empty">
        <div className="empty-state">
          <span className="empty-icon">
            <Icon name="layers" />
          </span>
          <h2>No groups yet</h2>
          <p>Groups sort projects into a tree: clients, products, whatever fits.</p>
          <Button icon="layers" onClick={onNew}>
            New group
          </Button>
        </div>
      </div>
    );
  }
  return (
    <div className="card group-list" role="list" aria-label="Project groups">
      {rows.map((n) => {
        const g = n.group;
        const selected = select.has(g.id);
        const own = n.projects.length;
        const inside = n.total - own;
        const pick = (e: ReactMouseEvent) => {
          if ((e.target as HTMLElement).closest('.menu')) return;
          if (select.on || e.shiftKey || e.ctrlKey || e.metaKey) {
            e.preventDefault();
            select.click(g.id, e);
          }
        };
        return (
          <div
            role="listitem"
            key={g.id}
            className={`group-row ${select.on ? 'is-selecting' : ''} ${selected ? 'is-selected' : ''}`}
            style={{ ['--depth' as string]: n.depth }}
            onClick={pick}
            onMouseDown={(e) => {
              if (e.shiftKey) e.preventDefault();
            }}
            {...select.pressProps(g.id)}
          >
            {select.on ? (
              <button type="button" className="group-open" role="checkbox" aria-checked={selected} aria-label={`Select group ${g.name}`}>
                <SelectBox checked={selected} />
                <Icon name="layers" />
                <span className="truncate">{g.name}</span>
              </button>
            ) : (
              <button type="button" className="group-open" title={`Show the projects in ${g.name}`} onClick={(e) => !(e.shiftKey || e.ctrlKey || e.metaKey) && onOpen(g)}>
                <Icon name="layers" />
                <span className="truncate">{g.name}</span>
              </button>
            )}
            <span className="group-count tabular" title={inside ? `${plural(own, 'project')} in it, ${plural(inside, 'project')} in its sub-groups` : undefined}>
              {plural(n.total, 'project')}
              {n.children.length > 0 && <span className="group-sub"> · {plural(n.children.length, 'sub-group')}</span>}
            </span>
            <Menu
              label={`Actions for group ${g.name}`}
              className={select.on ? 'is-hidden' : ''}
              items={[
                { label: 'New sub-group…', icon: 'plus', onSelect: () => onSubgroup(g) },
                { label: 'Edit group…', icon: 'pencil', onSelect: () => onEdit(g) },
                { label: 'Select', icon: 'selectAll', onSelect: () => select.start(g.id) },
                'sep',
                { label: 'Delete group…', icon: 'trash', danger: true, onSelect: () => onDelete(g) },
              ]}
            />
          </div>
        );
      })}
    </div>
  );
}
