// Project badges (docs/API-board.md, project badges; DESIGN.md §3.11): one short coloured label with
// an icon per project, from a list shared by all projects. On a card it sits in the cover's top right
// corner, in a list row and the project switcher after the name. Changing a badge changes it on
// every project that carries it.

import { useId, useMemo, useState, type CSSProperties, type FormEvent } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Field, TextField } from '../components/form';
import { Icon, type IconName } from '../components/Icon';
import { Modal } from '../components/Modal';
import type { BadgeColor, Project, ProjectBadge } from '../lib/types';
import { store, useProjectBadges, useProjects } from '../store';

/** The same colour on every theme: a badge sits over a picture as often as on the page. */
export const BADGE_COLORS: Record<BadgeColor, string> = {
  red: '#d63a3a',
  orange: '#e0661f',
  amber: '#c98a0a',
  green: '#1f9d55',
  teal: '#0f8f86',
  blue: '#2f6fe0',
  violet: '#7a4ee0',
  pink: '#d0408a',
  gray: '#5b5b66',
};

const COLOR_NAMES = Object.keys(BADGE_COLORS) as BadgeColor[];

/** The icons a badge can have: things a project's state or audience is said with. */
export const BADGE_ICONS: IconName[] = [
  'shield',
  'code',
  'bolt',
  'activity',
  'monitor',
  'server',
  'cloud',
  'globe',
  'flag',
  'star',
  'bug',
  'lock',
  'alertCircle',
  'clock',
  'sparkle',
  'target',
  'pin',
  'branch',
  'user',
  'creditCard',
];

/** As the server allows it (12 characters). */
export const BADGE_NAME_MAX = 12;

const isIcon = (v: string | undefined): v is IconName => !!v && (BADGE_ICONS as string[]).includes(v);

/** The badge as it shows: on a cover ("cover") or after a name ("sm"). */
export function BadgeMark({ badge, size = 'sm', className = '' }: { badge: Pick<ProjectBadge, 'name' | 'color' | 'icon'>; size?: 'cover' | 'sm'; className?: string }) {
  const color = BADGE_COLORS[badge.color] ?? BADGE_COLORS.gray;
  return (
    <span className={`project-badge is-${size} ${className}`} style={{ '--badge': color } as CSSProperties} title={badge.name}>
      {isIcon(badge.icon) && <Icon name={badge.icon} />}
      <span className="project-badge-name">{badge.name}</span>
    </span>
  );
}

const nameOrder = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' });

/**
 * Projects by their badge: those with one first, A to Z by the badge's name, then those without;
 * the order they were given stays within one badge.
 */
export function sortByBadge(list: Project[], badges: Record<string, ProjectBadge>): Project[] {
  const name = (p: Project) => (p.badgeId ? badges[p.badgeId]?.name : undefined);
  return [...list].sort((a, b) => {
    const x = name(a);
    const y = name(b);
    if (x === y) return 0;
    if (x === undefined) return 1;
    if (y === undefined) return -1;
    return nameOrder(x, y);
  });
}

/** The projects in a section per badge, A to Z, and those without one after them. */
export function badgeSections(list: Project[], badges: ProjectBadge[]): { badge: ProjectBadge | null; projects: Project[] }[] {
  const byId = new Map(badges.map((b) => [b.id, b]));
  const out = new Map<string, { badge: ProjectBadge | null; projects: Project[] }>();
  for (const b of [...badges].sort((a, b) => nameOrder(a.name, b.name))) out.set(b.id, { badge: b, projects: [] });
  const none: Project[] = [];
  for (const p of list) {
    const b = p.badgeId ? byId.get(p.badgeId) : undefined;
    if (b) out.get(b.id)!.projects.push(p);
    else none.push(p);
  }
  const sections = [...out.values()].filter((s) => s.projects.length > 0);
  if (none.length) sections.push({ badge: null, projects: none });
  return sections;
}

/** Badge of a project: pick one of the list, change it, make a new one, or take it off. */
export function BadgeDialog({ project, onClose }: { project: Project; onClose: () => void }) {
  const badges = useProjectBadges();
  const projects = useProjects();
  const current = badges.find((b) => b.id === project.badgeId);
  // "" none, "new" a badge made here, else the id of one in the list
  const [pick, setPick] = useState<string>(current?.id ?? (badges.length ? '' : 'new'));
  const picked = badges.find((b) => b.id === pick);
  const [name, setName] = useState(picked?.name ?? '');
  const [color, setColor] = useState<BadgeColor>(picked?.color ?? 'red');
  const [icon, setIcon] = useState<string>(picked ? (picked.icon ?? '') : 'shield');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [deleting, setDeleting] = useState<ProjectBadge | null>(null);
  const formId = useId();

  const choose = (id: string) => {
    setPick(id);
    setError('');
    const b = badges.find((x) => x.id === id);
    setName(b?.name ?? '');
    setColor(b?.color ?? 'red');
    setIcon(b ? (b.icon ?? '') : 'shield');
  };

  const users = useMemo(() => (picked ? projects.filter((p) => p.badgeId === picked.id).length : 0), [projects, picked]);
  const others = users - (project.badgeId === picked?.id ? 1 : 0);
  const editing = pick !== '';
  const changed = !!picked && (name.trim() !== picked.name || color !== picked.color || icon !== (picked.icon ?? ''));

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    const clean = name.trim();
    if (editing && !clean) {
      setError('A badge needs a name.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      let id = pick;
      if (pick === 'new') {
        const b = await api.createProjectBadge({ name: clean, color, icon: icon || undefined });
        store.upsertProjectBadge(b);
        id = b.id;
        // a retry after a refused save must not make the badge again
        setPick(b.id);
      } else if (picked && changed) {
        store.upsertProjectBadge(await api.updateProjectBadge(picked.id, { name: clean, color, icon }));
      }
      if (id !== (project.badgeId ?? '')) store.upsertProject(await api.updateProject(project.id, { badgeId: id }));
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  const preview = { name: name.trim() || 'Badge', color, icon };

  return (
    <>
      <Modal
        title={`Badge of ${project.name}`}
        icon="bookmark"
        size="sm"
        onClose={() => !busy && onClose()}
        dismissable={!busy}
        help="One badge per project. A badge is shared: changing it changes it on every project that has it."
        footer={
          <>
            {picked && (
              <Button variant="danger-ghost" onClick={() => setDeleting(picked)} disabled={busy} className="badge-delete">
                Delete badge
              </Button>
            )}
            <Button variant="secondary" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" form={formId} variant="primary" loading={busy}>
              Save
            </Button>
          </>
        }
      >
        <form id={formId} className="form badge-form" onSubmit={submit}>
          <fieldset disabled={busy}>
            <div className="field">
              <span className="field-label" id={`${formId}-pick`}>
                Badge
              </span>
              <div className="badge-picks" role="radiogroup" aria-labelledby={`${formId}-pick`}>
                <button type="button" role="radio" aria-checked={pick === ''} className="badge-pick is-none" onClick={() => choose('')}>
                  None
                </button>
                {badges.map((b) => (
                  <button key={b.id} type="button" role="radio" aria-checked={pick === b.id} className="badge-pick" onClick={() => choose(b.id)}>
                    <BadgeMark badge={b} />
                  </button>
                ))}
                <button type="button" role="radio" aria-checked={pick === 'new'} className="badge-pick is-new" onClick={() => choose('new')}>
                  <Icon name="plus" />
                  New badge
                </button>
              </div>
            </div>

            {editing && (
              <>
                <TextField
                  label="Name"
                  value={name}
                  maxLength={BADGE_NAME_MAX}
                  autoFocus={pick === 'new'}
                  hint={picked && others > 0 ? `Also on ${others} other project${others === 1 ? '' : 's'}: a change shows there too.` : 'Shown in capitals, at most 12 characters.'}
                  error={error}
                  onChange={(e) => setName(e.target.value)}
                />
                <Field label="Colour">
                  {() => (
                    <div className="badge-swatches" role="radiogroup" aria-label="Colour">
                      {COLOR_NAMES.map((c) => (
                        <button
                          key={c}
                          type="button"
                          role="radio"
                          aria-checked={color === c}
                          aria-label={c}
                          title={c}
                          className="badge-swatch"
                          style={{ '--badge': BADGE_COLORS[c] } as CSSProperties}
                          onClick={() => setColor(c)}
                        />
                      ))}
                    </div>
                  )}
                </Field>
                <Field label="Icon" optional>
                  {() => (
                    <div className="badge-icons" role="radiogroup" aria-label="Icon">
                      <button type="button" role="radio" aria-checked={!icon} aria-label="No icon" title="No icon" className="badge-icon is-none" onClick={() => setIcon('')}>
                        <Icon name="minus" />
                      </button>
                      {BADGE_ICONS.map((k) => (
                        <button key={k} type="button" role="radio" aria-checked={icon === k} aria-label={k} title={k} className="badge-icon" onClick={() => setIcon(k)}>
                          <Icon name={k} />
                        </button>
                      ))}
                    </div>
                  )}
                </Field>
                <div className="field">
                  <span className="field-label">Preview</span>
                  <div className="badge-preview">
                    <BadgeMark badge={preview} size="cover" />
                    <BadgeMark badge={preview} />
                  </div>
                </div>
              </>
            )}
            {!editing && error && (
              <p className="field-error" role="alert">
                {error}
              </p>
            )}
          </fieldset>
        </form>
      </Modal>
      {deleting && (
        <ConfirmDialog
          title={`Delete the badge “${deleting.name}”?`}
          confirmLabel="Delete badge"
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await api.deleteProjectBadge(deleting.id);
            store.removeProjectBadge(deleting.id);
            setDeleting(null);
            onClose();
          }}
        >
          <p>
            {users === 0 ? 'No project has it.' : users === 1 ? 'It comes off the one project that has it.' : `It comes off the ${users} projects that have it.`} The projects themselves
            do not change.
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}
