// What changes the project groups: a group's name and place, removing one, and a project's
// group and tags (DESIGN.md §3.11). Removing a group removes no project: what was inside moves up.

import { useId, useMemo, useState, type FormEvent, type KeyboardEvent } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Field, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { NAME_MAX } from '../components/ProjectBar';
import type { Project, ProjectGroup } from '../lib/types';
import { store, useProjectGroups, useProjects } from '../store';
import { allTags, flatGroups, groupPath, groupTree } from './groups';

/** Every group as a path, for a select. `except` leaves out a group and what is inside it. */
function useGroupOptions(except?: string) {
  const groups = useProjectGroups();
  return useMemo(() => {
    const hidden = new Set<string>();
    if (except) {
      hidden.add(except);
      // parents come first in this order, so one pass finds every level
      for (const n of flatGroups(groupTree(groups, []).roots)) {
        if (n.group.parentId && hidden.has(n.group.parentId)) hidden.add(n.group.id);
      }
    }
    return flatGroups(groupTree(groups, []).roots)
      .filter((n) => !hidden.has(n.group.id))
      .map((n) => ({ id: n.group.id, path: groupPath(groups, n.group.id) }));
  }, [groups, except]);
}

interface GroupDialogProps {
  /** The group to edit; none makes a new one. */
  group?: ProjectGroup;
  /** Where a new one goes. */
  parentId?: string;
  onClose: () => void;
  onSaved?: (g: ProjectGroup) => void;
}

export function GroupDialog({ group, parentId, onClose, onSaved }: GroupDialogProps) {
  const options = useGroupOptions(group?.id);
  const [name, setName] = useState(group?.name ?? '');
  const [parent, setParent] = useState(group?.parentId ?? parentId ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const formId = useId();

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!name.trim()) {
      setError('Give the group a name.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const saved = group ? await api.updateProjectGroup(group.id, { name: name.trim(), parentId: parent }) : await api.createProjectGroup({ name: name.trim(), parentId: parent || undefined });
      store.upsertProjectGroup(saved);
      onSaved?.(saved);
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <Modal
      title={group ? 'Edit group' : parentId ? 'New sub-group' : 'New group'}
      icon="layers"
      size="sm"
      onClose={() => !busy && onClose()}
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form={formId} variant="primary" loading={busy}>
            {group ? 'Save' : 'Create group'}
          </Button>
        </>
      }
    >
      <form id={formId} className="form" onSubmit={submit}>
        <fieldset disabled={busy}>
          <TextField label="Name" value={name} maxLength={NAME_MAX} autoFocus error={error} onChange={(e) => setName(e.target.value)} />
          <SelectField label="Inside" value={parent} onChange={(e) => setParent(e.target.value)}>
            <option value="">Top level</option>
            {options.map((o) => (
              <option key={o.id} value={o.id}>
                {o.path}
              </option>
            ))}
          </SelectField>
        </fieldset>
      </form>
    </Modal>
  );
}

export function DeleteGroupDialog({ group, onClose }: { group: ProjectGroup; onClose: () => void }) {
  const groups = useProjectGroups();
  const parent = groupPath(groups, group.parentId);
  return (
    <ConfirmDialog
      title={`Delete group “${group.name}”?`}
      confirmLabel="Delete group"
      danger
      onClose={onClose}
      onConfirm={async () => {
        await api.deleteProjectGroup(group.id);
        store.removeProjectGroup(group.id);
      }}
    >
      <p>
        What is inside moves to <strong>{parent || 'All projects'}</strong>. No project, board or file is removed.
      </p>
    </ConfirmDialog>
  );
}

/** Tags as chips. Enter or a comma ends one; pasting "a, b" gives two. */
function TagInput({ id, tags, draft, onDraft, onTags, known, describedBy }: { id: string; tags: string[]; draft: string; onDraft: (v: string) => void; onTags: (t: string[]) => void; known: string[]; describedBy?: string }) {
  const listId = useId();
  const add = (text: string) => {
    const next = [...tags];
    for (const raw of text.split(',')) {
      const t = raw.trim().slice(0, NAME_MAX);
      if (t && !next.some((x) => x.toLowerCase() === t.toLowerCase())) next.push(t);
    }
    onTags(next);
    onDraft('');
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if ((e.key === 'Enter' && draft.trim()) || e.key === ',') {
      e.preventDefault();
      add(draft);
    } else if (e.key === 'Backspace' && !draft && tags.length) {
      onTags(tags.slice(0, -1));
    }
  };
  return (
    <div className="tag-input control">
      {tags.map((t) => (
        <span key={t} className="project-tag">
          <span className="truncate">{t}</span>
          <button type="button" className="tag-x" aria-label={`Remove tag ${t}`} title="Remove" onClick={() => onTags(tags.filter((x) => x !== t))}>
            <Icon name="close" />
          </button>
        </span>
      ))}
      <input
        id={id}
        value={draft}
        list={listId}
        maxLength={NAME_MAX}
        aria-describedby={describedBy}
        placeholder={tags.length ? '' : 'backend, client, …'}
        onChange={(e) => {
          if (e.target.value.includes(',')) add(e.target.value);
          else onDraft(e.target.value);
        }}
        onKeyDown={onKey}
      />
      <datalist id={listId}>
        {known.map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>
    </div>
  );
}

/** The group a project is in and its tags. Saved together, so Cancel leaves both. */
export function OrganizeDialog({ project, onClose }: { project: Project; onClose: () => void }) {
  const groups = useProjectGroups();
  const projects = useProjects();
  const options = useGroupOptions();
  const [groupId, setGroupId] = useState(groups.some((g) => g.id === project.groupId) ? (project.groupId ?? '') : '');
  const [fresh, setFresh] = useState('');
  const [tags, setTags] = useState(project.tags ?? []);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const formId = useId();
  const known = useMemo(() => allTags(projects).map((t) => t.tag).filter((t) => !tags.some((x) => x.toLowerCase() === t.toLowerCase())), [projects, tags]);

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    setError('');
    try {
      let target = groupId;
      if (fresh.trim()) {
        const g = await api.createProjectGroup({ name: fresh.trim(), parentId: groupId || undefined });
        store.upsertProjectGroup(g);
        target = g.id;
        // a retry after a refused save must not make the group again
        setGroupId(g.id);
        setFresh('');
      }
      // what is still being typed counts too
      const typed = draft.split(',').map((t) => t.trim().slice(0, NAME_MAX)).filter(Boolean);
      const all = [...tags];
      for (const t of typed) if (!all.some((x) => x.toLowerCase() === t.toLowerCase())) all.push(t);
      store.upsertProject(await api.updateProject(project.id, { groupId: target, tags: all }));
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`Group and tags of ${project.name}`}
      icon="tag"
      size="sm"
      onClose={() => !busy && onClose()}
      dismissable={!busy}
      help="Only the list changes. The folder, boards and terminals stay."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form={formId} variant="primary" loading={busy}>
            Save
          </Button>
        </>
      }
    >
      <form id={formId} className="form" onSubmit={submit}>
        <fieldset disabled={busy}>
          <SelectField label="Group" value={groupId} autoFocus onChange={(e) => setGroupId(e.target.value)}>
            <option value="">No group (All projects)</option>
            {options.map((o) => (
              <option key={o.id} value={o.id}>
                {o.path}
              </option>
            ))}
          </SelectField>
          <TextField
            label="New group"
            optional
            value={fresh}
            maxLength={NAME_MAX}
            hint={groupId ? `Goes inside ${groupPath(groups, groupId)}, and the project goes in it.` : 'Goes at the top level, and the project goes in it.'}
            onChange={(e) => setFresh(e.target.value)}
          />
          <Field label="Tags" optional hint="Enter or a comma ends a tag." error={error}>
            {({ id, 'aria-describedby': describedBy }) => <TagInput id={id} tags={tags} draft={draft} onDraft={setDraft} onTags={setTags} known={known} describedBy={describedBy} />}
          </Field>
        </fieldset>
      </form>
    </Modal>
  );
}
