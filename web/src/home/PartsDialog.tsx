// The parts of a project (docs/API-board.md §1, ROADMAP #49): the folders one project spans on
// one machine or several, such as a backend on one server and a frontend on another. Each change
// is saved as it is made; folders on disk are never touched.

import { useEffect, useId, useState, type FormEvent } from 'react';
import { api, errorMessage } from '../api';
import { machineReady, machineStatus } from '../board/sessionStatus';
import { Button } from '../components/Button';
import { FolderBrowser } from '../components/FolderBrowser';
import { Checkbox, Field, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { NAME_MAX } from '../components/ProjectBar';
import { toast } from '../components/Toast';
import { projectParts } from '../lib/parts';
import type { Project, ProjectPart } from '../lib/types';
import { store, useMachines } from '../store';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { FolderPath } from './FolderPath';
import { MergeDialog } from './MergeDialog';

export function PartsDialog({ project, onClose }: { project: Project; onClose: () => void }) {
  const machines = useMachines();
  const parts = projectParts(project);
  const machineName = (id: string) => machines.find((m) => m.id === id)?.name ?? id;
  const [busy, setBusy] = useState('');
  const [merging, setMerging] = useState(false);
  const [splitting, setSplitting] = useState<ProjectPart | null>(null);

  /** Runs one change and takes the project the hub answers with. */
  const change = async (what: string, run: () => Promise<Project>) => {
    setBusy(what);
    try {
      store.upsertProject(await run());
      return true;
    } catch (err) {
      toast.error(errorMessage(err));
      return false;
    } finally {
      setBusy('');
    }
  };

  return (
    <Modal
      title={`Parts of ${project.name}`}
      icon="layers"
      size="md"
      onClose={onClose}
      help="Folders and their files stay on disk. Memory, the lead, boards and tasks stay one for the whole project."
      footer={
        <Button variant="primary" onClick={onClose}>
          Done
        </Button>
      }
    >
      <p className="parts-intro">
        One project can span folders on several machines: a backend on one server, a frontend on another. A terminal opens in the part on the machine you pick. In a read-only part agents only read and search.
      </p>
      {parts.length > 1 && (
        <SelectField
          label="Agents in parts on other machines"
          value={project.partAccess ?? ''}
          disabled={!!busy}
          hint="An agent works fully in its own part and in the other parts on its machine. In parts on other machines it reads and searches, or changes and runs commands too. A read-only part stays read only."
          onChange={(e) => void change('access', () => api.updateProject(project.id, { partAccess: e.target.value }))}
        >
          <option value="">Read only</option>
          <option value="full">Full access</option>
        </SelectField>
      )}
      <ul className="part-list" aria-label={`Parts of ${project.name}`}>
        {parts.map((pt, i) => (
          <PartRow
            key={pt.id}
            part={pt}
            first={i === 0}
            last={i === parts.length - 1}
            busy={!!busy}
            machine={machineName(pt.machineId)}
            onRename={(name) => change('rename', () => api.updatePart(project.id, pt.id, { name }))}
            onReadOnly={(readOnly) => change('readOnly', () => api.updatePart(project.id, pt.id, { readOnly }))}
            onMove={(index) => void change('move', () => api.updatePart(project.id, pt.id, { index }))}
            onRemove={() => void change('remove', () => api.deletePart(project.id, pt.id))}
            onBlocked={(blocked) => change('blocked', () => api.updatePart(project.id, pt.id, { blocked }))}
            onSplit={() => setSplitting(pt)}
            index={i}
          />
        ))}
      </ul>
      <div className="parts-merge">
        <Button variant="ghost" size="sm" icon="layers" onClick={() => setMerging(true)}>
          Merge another project into this one…
        </Button>
      </div>
      <AddPart project={project} onAdded={(p) => store.upsertProject(p)} />
      {merging && <MergeDialog project={project} onClose={() => setMerging(false)} />}
      {splitting && (
        <ConfirmDialog
          title={`Make ${splitting.name} a project of its own?`}
          confirmLabel="Split it off"
          onClose={() => setSplitting(null)}
          onConfirm={async () => {
            const r = await api.splitPart(project.id, splitting.id);
            store.upsertProject(r.from);
            store.upsertProject(r.project);
            toast.success(`${r.project.name} is a project of its own now.`);
          }}
        >
          <p>
            <strong>{splitting.name}</strong> becomes a project with one empty board, in the same group and with the same tags. Its review and local history go with it. Boards, memory and monitors stay here. Close the terminals in it first.
          </p>
        </ConfirmDialog>
      )}
    </Modal>
  );
}

function PartRow({
  part,
  first,
  last,
  busy,
  machine,
  index,
  onRename,
  onReadOnly,
  onMove,
  onRemove,
  onBlocked,
  onSplit,
}: {
  part: ProjectPart;
  first: boolean;
  last: boolean;
  busy: boolean;
  machine: string;
  index: number;
  onRename: (name: string) => Promise<boolean>;
  onReadOnly: (on: boolean) => Promise<boolean>;
  onMove: (index: number) => void;
  onRemove: () => void;
  onBlocked: (list: string[]) => Promise<boolean>;
  onSplit: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const blocked = part.blocked ?? [];
  const saveBlocked = async () => {
    if (await onBlocked(draft.split('\n').map((l) => l.trim()).filter(Boolean))) setEditing(false);
  };
  const [name, setName] = useState(part.name);
  // shown at once, and back if the hub refuses
  const [readOnly, setReadOnly] = useState(!!part.readOnly);
  useEffect(() => setReadOnly(!!part.readOnly), [part.readOnly]);
  const toggleReadOnly = async (on: boolean) => {
    setReadOnly(on);
    if (!(await onReadOnly(on))) setReadOnly(!on);
  };
  const save = async () => {
    const v = name.trim().slice(0, NAME_MAX);
    if (!v || v === part.name) return setName(part.name);
    if (!(await onRename(v))) setName(part.name);
  };
  return (
    <li className={`part-row${readOnly ? ' is-read-only' : ''}`}>
      <div className="part-main">
        <input
          className="control control-sm part-name"
          value={name}
          maxLength={NAME_MAX}
          aria-label={`Name of part ${part.name}`}
          onChange={(e) => setName(e.target.value)}
          onBlur={() => void save()}
          onKeyDown={(e) => {
            if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
            if (e.key === 'Escape') {
              e.stopPropagation();
              setName(part.name);
            }
          }}
        />
        <FolderPath lead={`${machine} · `} path={part.path} />
        {first && <span className="part-first">The project's own folder</span>}
      </div>
      <div className="part-actions">
        <span title="Agents only read and search here. Your own terminals can do anything.">
          <Checkbox label="Read-only" checked={readOnly} disabled={busy} onChange={(on) => void toggleReadOnly(on)} />
        </span>
        <Button variant="ghost" size="sm" icon="arrowUp" iconOnly aria-label={`Move ${part.name} up`} title={index === 1 ? `Make ${part.name} the project's own folder` : `Move ${part.name} up`} disabled={busy || first} onClick={() => onMove(index - 1)} />
        <Button variant="ghost" size="sm" icon="chevronDown" iconOnly aria-label={`Move ${part.name} down`} disabled={busy || last} onClick={() => onMove(index + 1)} />
        <Button
          variant={blocked.length ? 'secondary' : 'ghost'}
          size="sm"
          icon="lock"
          iconOnly
          aria-label={`Blocked commands of ${part.name}`}
          aria-expanded={editing}
          title={blocked.length ? `Blocked here: ${blocked.join(', ')}` : 'Commands that need your approval in this part'}
          disabled={busy}
          onClick={() => {
            setDraft(blocked.join('\n'));
            setEditing(!editing);
          }}
        />
        <Button variant="ghost" size="sm" icon="arrowUpRight" iconOnly aria-label={`Split ${part.name} off`} title={first ? "The project's own folder stays" : `Make ${part.name} a project of its own`} disabled={busy || first} onClick={onSplit} />
        <Button
          variant="danger-ghost"
          size="sm"
          icon="trash"
          iconOnly
          aria-label={`Remove part ${part.name}`}
          title={first ? "The project's own folder stays: move another part up first" : `Remove ${part.name} from the project. Its folder stays on disk.`}
          disabled={busy || first}
          onClick={onRemove}
        />
      </div>
      {editing && (
        <div className="part-blocked">
          <label className="part-add-title" htmlFor={`blocked-${part.id}`}>
            Blocked commands in {part.name}
          </label>
          <textarea
            id={`blocked-${part.id}`}
            className="control mono"
            rows={3}
            placeholder={'git push\nphp artisan migrate'}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
          <span className="field-hint">One per line. An agent that runs one there stops and asks you, on top of the commands blocked for the whole hub and project.</span>
          <div className="part-add-foot">
            <Button size="sm" variant="secondary" onClick={() => setEditing(false)}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" disabled={busy} onClick={() => void saveBlocked()}>
              Save
            </Button>
          </div>
        </div>
      )}
    </li>
  );
}

function AddPart({ project, onAdded }: { project: Project; onAdded: (p: Project) => void }) {
  const machines = useMachines();
  const [machineId, setMachineId] = useState(machines[0]?.id ?? 'local');
  const [path, setPath] = useState('');
  const [name, setName] = useState('');
  const [browsing, setBrowsing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const formId = useId();
  const machine = machines.find((m) => m.id === machineId);

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!name.trim()) return setError('Name the part, such as Backend or Frontend.');
    if (!path.trim()) return setError('Choose a folder.');
    setBusy(true);
    setError('');
    try {
      onAdded(await api.createPart(project.id, { name: name.trim(), machineId, path: path.trim() }));
      toast.success(`${name.trim()} is now a part of ${project.name}.`);
      setName('');
      setPath('');
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <form id={formId} className="form part-add" onSubmit={submit} aria-label="Add a part">
        <h3 className="part-add-title">Add a part</h3>
        <fieldset disabled={busy}>
          <TextField label="Part name" value={name} maxLength={NAME_MAX} placeholder="Backend" onChange={(e) => setName(e.target.value)} />
          <SelectField
            label="Machine"
            value={machineId}
            onChange={(e) => {
              setMachineId(e.target.value);
              setPath('');
            }}
            hint={machine && !machineReady(machine) ? `${machine.name} is ${machineStatus(machine).label.toLowerCase()}.` : undefined}
          >
            {machines.length === 0 && <option value="local">This PC</option>}
            {machines.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
                {machineReady(m) ? '' : ` (${machineStatus(m).label.toLowerCase()})`}
              </option>
            ))}
          </SelectField>
          <Field label="Folder" error={error || undefined}>
            {({ labelId: _l, ...a11y }) => (
              <div className="input-group has-lead has-addon">
                <span className="ig-lead">
                  <Icon name="folder" />
                </span>
                <input
                  className="control mono"
                  placeholder={machine?.probe?.home ? `${machine.probe.home}/projects/app` : '/home/me/projects/app'}
                  value={path}
                  onChange={(e) => {
                    setPath(e.target.value);
                    setError('');
                  }}
                  spellCheck={false}
                  {...a11y}
                />
                <Button className="ig-addon" onClick={() => setBrowsing(true)}>
                  Browse
                </Button>
              </div>
            )}
          </Field>
          <div className="part-add-foot">
            <Button type="submit" variant="secondary" icon="plus" loading={busy}>
              Add part
            </Button>
          </div>
        </fieldset>
      </form>
      {browsing && (
        <FolderBrowser
          machineId={machineId}
          machineName={machine?.name ?? machineId}
          initialPath={path.trim()}
          onClose={() => setBrowsing(false)}
          onChoose={(p) => {
            setPath(p);
            setBrowsing(false);
          }}
        />
      )}
    </>
  );
}
