// Add a project (ROADMAP #49): a name and its parts, each a folder on one of the machines (browsed
// with the folder picker) named for its role. One part or several, the form is the same: a project
// in one folder is a list of one. Adding a folder that is already a project just opens it.

import { useRef, useState, type FormEvent } from 'react';
import { ApiError, api, errorMessage } from '../api';
import { machineReady, machineStatus } from '../board/sessionStatus';
import { Button } from '../components/Button';
import { FolderBrowser } from '../components/FolderBrowser';
import { Field, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { NAME_MAX } from '../components/ProjectBar';
import { toast } from '../components/Toast';
import { MAX_PARTS } from '../lib/parts';
import type { Machine, Project } from '../lib/types';
import { store, useMachines } from '../store';

type Draft = { key: number; name: string; machineId: string; path: string };
type DraftError = { name?: string; folder?: string };

const folderName = (path: string) => path.trim().split('/').filter(Boolean).pop() ?? '';
// examples of part names, by place; an unnamed part takes its folder's name (the first: Main)
const EXAMPLES = ['Main', 'Frontend', 'Api', 'Worker', 'Admin', 'Docs', 'Mobile', 'Infra'];

export function AddProjectDialog({ onClose, onAdded }: { onClose: () => void; onAdded: (p: Project, existed: boolean) => void }) {
  const machines = useMachines();
  const nextKey = useRef(1);
  const [parts, setParts] = useState<Draft[]>(() => [{ key: 0, name: '', machineId: machines[0]?.id ?? 'local', path: '' }]);
  const [name, setName] = useState('');
  const [browsing, setBrowsing] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<Record<number, DraftError>>({});
  const [nameError, setNameError] = useState('');
  const [added, setAdded] = useState(-1);

  const change = (key: number, patch: Partial<Draft>) => {
    setParts((list) => list.map((pt) => (pt.key === key ? { ...pt, ...patch } : pt)));
    setErrors((e) => (e[key] ? { ...e, [key]: {} } : e));
  };
  const addPart = () => {
    // a part elsewhere is the usual case: start on a machine the others are not on
    const used = new Set(parts.map((pt) => pt.machineId));
    const start = machines.find((m) => !used.has(m.id))?.id ?? machines[0]?.id ?? 'local';
    const key = nextKey.current++;
    setParts((list) => [...list, { key, name: '', machineId: start, path: '' }]);
    setAdded(key);
  };
  const removePart = (key: number) => setParts((list) => list.filter((pt) => pt.key !== key));

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    const found: Record<number, DraftError> = {};
    for (const pt of parts) if (!pt.path.trim()) found[pt.key] = { folder: 'Choose a folder.' };
    setErrors(found);
    setNameError('');
    if (Object.keys(found).length) return;
    setBusy(true);
    const [own, ...more] = parts;
    try {
      const before = new Set(Object.keys(store.getState().projects));
      const p = await api.createProject({
        machineId: own.machineId,
        path: own.path.trim(),
        name: name.trim() || undefined,
        partName: own.name.trim() || undefined,
        parts: more.length ? more.map((pt) => ({ name: pt.name.trim(), machineId: pt.machineId, path: pt.path.trim() })) : undefined,
      });
      store.upsertProject(p);
      const existed = before.has(p.id);
      if (existed) toast.info(`${p.name} is already a project — opening it.`);
      onAdded(p, existed);
    } catch (err) {
      const msg = errorMessage(err);
      // an error about one folder says which, by its place in the list
      const at = err instanceof ApiError && typeof err.body.part === 'number' ? err.body.part : -1;
      const onName = err instanceof ApiError && (err.code === 'name_conflict' || (err.status === 400 && /\bname\b/i.test(msg)));
      if (at >= 0 && at < parts.length) setErrors({ [parts[at].key]: onName ? { name: msg } : { folder: msg } });
      else if (onName) setNameError(msg);
      else setErrors({ [own.key]: { folder: msg } });
      setBusy(false);
    }
  };

  const browseFor = parts.find((pt) => pt.key === browsing);
  const browseMachine = browseFor ? machines.find((m) => m.id === browseFor.machineId) : undefined;

  return (
    <>
      <Modal
        title="Add project"
        icon="folder"
        size="md"
        onClose={() => !busy && onClose()}
        dismissable={!busy}
        help={`Nothing is installed or changed in the ${parts.length > 1 ? 'folders' : 'folder'}.`}
        footer={
          <>
            <Button variant="secondary" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" form="add-project-form" variant="primary" icon="plus" loading={busy}>
              Add project
            </Button>
          </>
        }
      >
        <form id="add-project-form" className="form" onSubmit={submit} noValidate>
          <fieldset disabled={busy}>
            <TextField
              label="Project name"
              optional
              hint={`Shown on Home and in the header, up to ${NAME_MAX} characters. Defaults to the first part's folder name.`}
              value={name}
              maxLength={NAME_MAX}
              placeholder={folderName(parts[0].path) || 'my-app'}
              error={nameError || undefined}
              onChange={(e) => {
                setName(e.target.value);
                setNameError('');
              }}
            />
            <div className="parts-form" role="group" aria-labelledby="add-project-parts">
              <div className="parts-form-head">
                <h3 className="part-add-title" id="add-project-parts">
                  Parts
                </h3>
                <p className="part-more-hint">Each part is a folder of this project, usually the root of a git repository, on This PC or a server.</p>
              </div>
              {parts.map((pt, i) => (
                <section key={pt.key} className="part-block" aria-label={`Part ${i + 1}`}>
                  <div className="part-block-head">
                    <h4 className="part-block-title">Part {i + 1}</h4>
                    {parts.length > 1 && <Button variant="ghost" size="sm" icon="close" iconOnly aria-label={`Remove part ${i + 1}`} onClick={() => removePart(pt.key)} />}
                  </div>
                  <TextField
                    label="Part name"
                    optional
                    value={pt.name}
                    maxLength={NAME_MAX}
                    placeholder={i === 0 ? 'Main' : folderName(pt.path) || EXAMPLES[i]}
                    autoFocus={pt.key === added}
                    error={errors[pt.key]?.name}
                    onChange={(e) => change(pt.key, { name: e.target.value })}
                  />
                  <FolderFields
                    machines={machines}
                    machineId={pt.machineId}
                    path={pt.path}
                    error={errors[pt.key]?.folder}
                    onMachine={(id) => change(pt.key, { machineId: id, path: '' })}
                    onPath={(v) => change(pt.key, { path: v })}
                    onBrowse={() => setBrowsing(pt.key)}
                  />
                </section>
              ))}
              {parts.length < MAX_PARTS && (
                <div className="part-more">
                  <Button variant="ghost" size="sm" icon="plus" onClick={addPart}>
                    Add a part
                  </Button>
                </div>
              )}
            </div>
          </fieldset>
        </form>
      </Modal>
      {browseFor && (
        <FolderBrowser
          machineId={browseFor.machineId}
          machineName={browseMachine?.name ?? browseFor.machineId}
          initialPath={browseFor.path.trim()}
          onClose={() => setBrowsing(null)}
          onChoose={(p) => {
            change(browseFor.key, { path: p });
            setBrowsing(null);
          }}
        />
      )}
    </>
  );
}

/** A machine and a folder on it, with the folder picker. */
function FolderFields({
  machines,
  machineId,
  path,
  error,
  onMachine,
  onPath,
  onBrowse,
}: {
  machines: Machine[];
  machineId: string;
  path: string;
  error?: string;
  onMachine: (id: string) => void;
  onPath: (path: string) => void;
  onBrowse: () => void;
}) {
  const machine = machines.find((m) => m.id === machineId);
  return (
    <>
      <SelectField
        label="Machine"
        value={machineId}
        onChange={(e) => onMachine(e.target.value)}
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
              onChange={(e) => onPath(e.target.value)}
              spellCheck={false}
              {...a11y}
            />
            <Button className="ig-addon" onClick={onBrowse}>
              Browse
            </Button>
          </div>
        )}
      </Field>
    </>
  );
}
