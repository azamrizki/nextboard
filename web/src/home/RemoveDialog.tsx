// Removing one project or many (DESIGN.md §3.11, API-board §1): Archive, the default, keeps
// everything and Restore brings it back; Delete permanently removes NextBoard's data of them
// after the word "delete" is typed. Folders and files on disk stay either way.

import { useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { RadioCards, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import type { Project } from '../lib/types';
import { store, useSessions } from '../store';
import { bulkOutcome, CONFIRM_WORD, deleteList, removeSummary, stopLine, typedOk, type BulkOutcome, type RemoveChoice } from './remove';

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

interface RemoveDialogProps {
  projects: Project[];
  /** They are archived already: only Delete permanently is left. */
  archived?: boolean;
  /** The choice it opens with; Archive unless said otherwise. */
  initial?: RemoveChoice;
  onClose: () => void;
  /** The ids that went through. */
  onDone?: (ids: string[]) => void;
}

/** Brings archived projects back, for the Undo of a toast and the Archived list. */
export async function restoreProjects(list: Project[]): Promise<BulkOutcome> {
  const ids = list.map((p) => p.id);
  const nameOf = (id: string) => list.find((p) => p.id === id)?.name ?? id;
  if (ids.length === 1) {
    try {
      store.upsertProject(await api.restoreProject(ids[0]));
      return { done: ids, failed: [] };
    } catch (err) {
      return { done: [], failed: [{ id: ids[0], name: nameOf(ids[0]), error: errorMessage(err) }] };
    }
  }
  const out = bulkOutcome(await api.bulkProjects(ids, 'restore'), ids, nameOf);
  for (const id of out.done) {
    const p = list.find((x) => x.id === id);
    if (p) store.upsertProject({ ...p, archivedAt: undefined });
  }
  void store.refreshProjects();
  return out;
}

export function RemoveDialog({ projects, archived = false, initial = 'archive', onClose, onDone }: RemoveDialogProps) {
  const sessions = useSessions();
  const [choice, setChoice] = useState<RemoveChoice>(archived ? 'delete' : initial);
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [failed, setFailed] = useState<BulkOutcome['failed']>([]);
  // what is left to do after a run where some went through
  const [left, setLeft] = useState(projects);

  const one = left.length === 1 ? left[0] : null;
  const sum = removeSummary(left, sessions);
  const permanent = choice === 'delete';
  const stops = stopLine(sum);
  const what = one ? `“${one.name}”` : plural(left.length, 'project');

  const run = async () => {
    if (permanent && !typedOk(typed)) return;
    setBusy(true);
    setError('');
    setFailed([]);
    const ids = left.map((p) => p.id);
    const nameOf = (id: string) => left.find((p) => p.id === id)?.name ?? id;
    let out: BulkOutcome;
    try {
      if (ids.length === 1) {
        if (permanent) {
          await api.deleteProject(ids[0], true);
          store.removeProject(ids[0]);
        } else store.upsertProject(await api.archiveProject(ids[0]));
        out = { done: ids, failed: [] };
      } else {
        out = bulkOutcome(await api.bulkProjects(ids, permanent ? 'delete' : 'archive'), ids, nameOf);
        const stamp = new Date().toISOString();
        for (const id of out.done) {
          const p = left.find((x) => x.id === id);
          if (permanent) store.removeProject(id);
          else if (p) store.upsertProject({ ...p, archivedAt: stamp });
        }
        void store.refreshProjects();
      }
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
      return;
    }
    const gone = left.filter((p) => out.done.includes(p.id));
    if (out.done.length) onDone?.(out.done);
    if (gone.length) {
      const label = gone.length === 1 ? gone[0].name : plural(gone.length, 'project');
      if (permanent) toast.success(`${label} deleted. ${gone.length === 1 ? 'Its folder is' : 'Their folders are'} still on disk.`);
      else
        toast.notify('success', `${label} archived`, 'Find it under Archived on the Projects page. Folders on disk are untouched.', {
          label: 'Undo',
          onClick: () => {
            void restoreProjects(gone).then((r) => {
              if (r.failed.length) toast.error(`Could not restore ${r.failed.map((f) => f.name).join(', ')}.`);
            });
          },
        });
    }
    if (!out.failed.length) {
      onClose();
      return;
    }
    setLeft(left.filter((p) => !out.done.includes(p.id)));
    setFailed(out.failed);
    setBusy(false);
  };

  const title = archived ? `Delete ${what} permanently?` : `Remove ${what}?`;
  const action = permanent ? 'Delete permanently' : left.length === 1 ? 'Archive project' : `Archive ${plural(left.length, 'project')}`;

  return (
    <Modal
      title={title}
      icon={permanent ? 'trash' : 'archive'}
      tone={permanent ? 'danger' : 'default'}
      size="md"
      onClose={() => !busy && onClose()}
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant={permanent ? 'danger' : 'primary'} icon={permanent ? 'trash' : 'archive'} loading={busy} disabled={permanent && !typedOk(typed)} onClick={() => void run()}>
            {action}
          </Button>
        </>
      }
    >
      <div className="stack remove-dialog">
        {!archived && (
          <RadioCards<RemoveChoice>
            label="What to do"
            columns={1}
            className="remove-choice"
            value={choice}
            disabled={busy}
            onChange={setChoice}
            options={[
              { value: 'archive', title: 'Archive (restore any time)', description: 'Consoles stop; agents keep their conversation. Boards, memory and history stay, and Restore brings it all back.' },
              { value: 'delete', title: 'Delete permanently', description: 'Removes NextBoard’s data of it. This cannot be undone.' },
            ]}
          />
        )}

        {stops && (
          <p className="remove-line">
            <Icon name="power" />
            <span>{stops}</span>
          </p>
        )}

        {permanent && (
          <div className="remove-goes">
            <p className="remove-goes-title">What is deleted</p>
            <ul>
              {deleteList(sum).map((l) => (
                <li key={l}>{l}</li>
              ))}
            </ul>
          </div>
        )}

        <p className="remove-line remove-kept">
          <Icon name="folder" />
          <span>
            {sum.folders.length === 1 ? (
              <>
                The folder <code className="remove-path">{sum.folders[0]}</code> and its files stay on disk.
              </>
            ) : (
              <>Their {plural(sum.folders.length, 'folder')} and files stay on disk.</>
            )}
          </span>
        </p>

        {permanent && (
          <TextField
            label={
              <>
                Type <strong>{CONFIRM_WORD}</strong> to confirm
              </>
            }
            value={typed}
            autoComplete="off"
            spellCheck={false}
            disabled={busy}
            onChange={(e) => setTyped(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && typedOk(typed)) void run();
            }}
          />
        )}

        {failed.length > 0 && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>
              {failed.length === 1 ? 'One did not go through: ' : `${failed.length} did not go through: `}
              {failed.map((f) => `${f.name} (${f.error})`).join(', ')}.
            </span>
          </div>
        )}
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
      </div>
    </Modal>
  );
}
