// Merge another project into this one (docs/API-board.md §1, ROADMAP #49): its folders become
// parts, and its boards, terminals, monitors, loops and memory come along. Nothing on disk changes;
// the preview says what will move before anything does.

import { useEffect, useId, useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { SelectField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { boardPath, navigate } from '../lib/router';
import type { MergePreview, Project } from '../lib/types';
import { store, useMachines, useProjects } from '../store';
import { moveAgentShares, sharedWith, useVault } from '../vault/store';
import { FolderPath } from './FolderPath';

const count = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

export function MergeDialog({ project, onClose }: { project: Project; onClose: () => void }) {
  const projects = useProjects().filter((p) => p.id !== project.id);
  const machines = useMachines();
  const machineName = (id: string) => machines.find((m) => m.id === id)?.name ?? id;
  const [from, setFrom] = useState('');
  const [preview, setPreview] = useState<MergePreview | null>(null);
  const [critical, setCritical] = useState<'append' | 'drop'>('drop');
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const formId = useId();
  const other = projects.find((p) => p.id === from);
  // credentials its agents may ask for: only the open vault can move them, they are in each blob
  const vault = useVault();
  const vaultOpen = vault.status === 'unlocked';
  const shared = vaultOpen && from ? sharedWith(from) : 0;

  useEffect(() => {
    setPreview(null);
    setError('');
    setCritical('drop');
    if (!from) return;
    let live = true;
    setLoading(true);
    api
      .mergePreview(project.id, from)
      .then((v) => live && setPreview(v))
      .catch((err) => live && setError(errorMessage(err)))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [project.id, from]);

  const merge = async () => {
    if (!preview || preview.problem || !other) return;
    setBusy(true);
    setError('');
    try {
      const p = await api.merge(project.id, from, critical);
      store.upsertProject(p);
      store.removeProject(from);
      toast.success(`${other.name} is now part of ${project.name}.`);
      if (vaultOpen && shared > 0) {
        try {
          const n = await moveAgentShares(from, project.id);
          toast.info(`${n === 1 ? '1 credential' : `${n} credentials`} its agents could ask for ${n === 1 ? 'is' : 'are'} now for ${project.name}.`);
        } catch (err) {
          toast.error(`Could not move the credentials its agents could ask for: ${errorMessage(err)}`);
        }
      }
      onClose();
      // someone looking at its board goes on there
      if (window.location.pathname.startsWith(`/p/${from}`)) navigate(boardPath(project.id), true);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`Merge into ${project.name}`}
      icon="layers"
      size="md"
      onClose={() => !busy && onClose()}
      dismissable={!busy}
      help="Folders and their files stay where they are. The other project's link keeps working and opens here."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form={formId} variant="primary" icon="layers" loading={busy} disabled={!preview || !!preview.problem}>
            Merge
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="form merge-form"
        onSubmit={(e) => {
          e.preventDefault();
          void merge();
        }}
      >
        <p className="parts-intro">The other project's folders become parts of this one, and its boards, terminals, monitors, loops and memory come along. It then no longer shows on its own.</p>
        <SelectField label="Project to merge in" value={from} autoFocus onChange={(e) => setFrom(e.target.value)} error={error || undefined}>
          <option value="">Choose a project…</option>
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </SelectField>
        {loading && (
          <p className="status" role="status">
            <span className="spinner" aria-hidden="true" />
            Looking at what would move…
          </p>
        )}
        {preview && other && (
          <div className="merge-preview" aria-label="What moves">
            {preview.problem ? (
              <p className="merge-problem" role="alert">
                <Icon name="alertCircle" />
                {preview.code === 'too_many_parts' ? `Together they would have more than 8 parts.` : preview.code === 'part_overlap' ? `A folder of ${other.name} is inside one of this project's parts, or holds one.` : preview.problem}
              </p>
            ) : (
              <>
                <h3 className="part-add-title">New parts</h3>
                <ul className="merge-parts">
                  {preview.parts.map((pt) => (
                    <li key={pt.id}>
                      <strong>{pt.name}</strong>
                      <FolderPath lead={`${machineName(pt.machineId)} · `} path={pt.path} />
                    </li>
                  ))}
                </ul>
                <ul className="merge-facts">
                  <li>
                    <strong>{count(preview.workspaces.length, 'board')}</strong> become tabs here: {preview.workspaces.map((w) => w.name).join(', ')}.
                  </li>
                  {preview.sessions > 0 && (
                    <li>
                      <strong>{count(preview.sessions, 'terminal')}</strong> come along and keep running{preview.running ? ` (${preview.running} running now)` : ''}.
                    </li>
                  )}
                  {preview.monitors > 0 && (
                    <li>
                      <strong>{count(preview.monitors, 'monitor')}</strong> keep their history{preview.publicPage ? '. Its public status page stops: its link no longer works, and its monitors are not on this project’s page until you add them' : ''}.
                    </li>
                  )}
                  {preview.loops > 0 && (
                    <li>
                      <strong>{count(preview.loops, 'loop')}</strong> come along.
                    </li>
                  )}
                  {(preview.memoryChars > 0 || preview.subMemories > 0) && (
                    <li>
                      Its memory becomes a sub-memory named after it{preview.subMemories ? `, with ${count(preview.subMemories, 'page')} of its own` : ''}.
                    </li>
                  )}
                  {vaultOpen ? (
                    shared > 0 && (
                      <li>
                        <strong>{shared === 1 ? '1 credential' : `${shared} credentials`}</strong> its agents may ask for will be for {project.name} instead, saved in your open vault.
                      </li>
                    )
                  ) : (
                    <li>Credentials its agents may ask for stay limited to it while the vault is locked: unlock Credentials first to move them along, or allow them for {project.name} later.</li>
                  )}
                </ul>
                {preview.criticalChars > 0 && (
                  <SelectField
                    label={`Critical notes of ${other.name}`}
                    value={critical}
                    onChange={(e) => setCritical(e.target.value === 'append' ? 'append' : 'drop')}
                    hint={preview.criticalFits ? 'They would hold for every agent of this project too.' : 'Both together are longer than 16 KB: shorten them first to add them.'}
                  >
                    <option value="drop">Leave them out</option>
                    <option value="append" disabled={!preview.criticalFits}>
                      Add them under a heading “From {other.name}”
                    </option>
                  </SelectField>
                )}
              </>
            )}
          </div>
        )}
      </form>
    </Modal>
  );
}
