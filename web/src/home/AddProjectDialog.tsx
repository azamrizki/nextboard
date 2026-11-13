// Add a project: a folder on one of the machines (browsed with the folder picker) and an
// optional display name (≤ 24 characters). Adding a folder that is already a project
// just opens it. "Add a part" makes it a project in several parts at once (ROADMAP #49):
// more folders of the same product, on this machine or others, each named for its role.

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

type MorePart = { key: number; name: string; machineId: string; path: string };
type PartError = { name?: string; folder?: string };

// the own folder's key in errors and browsing; added parts count up from 1
const OWN = 0;

export function AddProjectDialog({ onClose, onAdded }: { onClose: () => void; onAdded: (p: Project, existed: boolean) => void }) {
  const machines = useMachines();
  const [machineId, setMachineId] = useState(machines[0]?.id ?? 'local');
  const [path, setPath] = useState('');
  const [name, setName] = useState('');
  const [partName, setPartName] = useState('');
  const [more, setMore] = useState<MorePart[]>([]);
  const [browsing, setBrowsing] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<Record<number, PartError>>({});
  const [nameError, setNameError] = useState('');
  const nextKey = useRef(1);
  const [added, setAdded] = useState(0);
  const multi = more.length > 0;

  const clearError = (key: number) => setErrors((e) => (e[key] ? { ...e, [key]: {} } : e));
  const change = (key: number, patch: Partial<MorePart>) => {
    setMore((list) => list.map((pt) => (pt.key === key ? { ...pt, ...patch } : pt)));
    clearError(key);
  };
  const addPart = () => {
    // a part elsewhere is the usual case: start on a machine the others are not on
    const used = new Set([machineId, ...more.map((pt) => pt.machineId)]);
    const start = machines.find((m) => !used.has(m.id))?.id ?? machines[0]?.id ?? 'local';
    const key = nextKey.current++;
    setMore((list) => [...list, { key, name: '', machineId: start, path: '' }]);
    setAdded(key);
  };
  const removePart = (key: number) => {
    setMore((list) => list.filter((pt) => pt.key !== key));
    clearError(key);
  };

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    const found: Record<number, PartError> = {};
    if (!path.trim()) found[OWN] = { folder: 'Choose a folder.' };
    for (const pt of more) {
      const pe: PartError = {};
      if (!pt.name.trim()) pe.name = 'Name the part, such as Frontend or Api.';
      if (!pt.path.trim()) pe.folder = 'Choose a folder.';
      if (pe.name || pe.folder) found[pt.key] = pe;
    }
    setErrors(found);
    setNameError('');
    if (Object.keys(found).length) return;
    setBusy(true);
    try {
      const before = new Set(Object.keys(store.getState().projects));
      const p = multi
        ? await api.createProject({
            machineId,
            path: path.trim(),
            name: name.trim() || undefined,
            partName: partName.trim() || undefined,
            parts: more.map((pt) => ({ name: pt.name.trim(), machineId: pt.machineId, path: pt.path.trim() })),
          })
        : await api.createProject({ machineId, path: path.trim(), name: name.trim() || undefined });
      store.upsertProject(p);
      const existed = before.has(p.id);
      if (existed) toast.info(`${p.name} is already a project — opening it.`);
      onAdded(p, existed);
    } catch (err) {
      const msg = errorMessage(err);
      // an error about one folder says which: 0 is the own folder, then the added parts in order
      const at = err instanceof ApiError && typeof err.body.part === 'number' ? err.body.part : -1;
      const onName = err instanceof ApiError && (err.code === 'name_conflict' || (err.status === 400 && /\bname\b/i.test(msg)));
      if (at > 0 && at <= more.length) setErrors({ [more[at - 1].key]: onName ? { name: msg } : { folder: msg } });
      else if (at === 0 && multi) setErrors({ [OWN]: onName ? { name: msg } : { folder: msg } });
      else if (at < 0 && onName) setNameError(msg);
      else setErrors({ [OWN]: { folder: msg } });
      setBusy(false);
    }
  };

  const browseFor = browsing === null ? undefined : browsing === OWN ? { machineId, path } : more.find((pt) => pt.key === browsing);
  const browseMachine = browseFor ? machines.find((m) => m.id === browseFor.machineId) : undefined;
  const ownFolder = (hint?: string) => (
    <FolderFields
      machines={machines}
      machineId={machineId}
      path={path}
      error={errors[OWN]?.folder}
      hint={hint}
      onMachine={(id) => {
        setMachineId(id);
        setPath('');
        clearError(OWN);
      }}
      onPath={(v) => {
        setPath(v);
        clearError(OWN);
      }}
      onBrowse={() => setBrowsing(OWN)}
    />
  );

  return (
    <>
      <Modal
        title="Add project"
        icon="folder"
        size="md"
        onClose={() => !busy && onClose()}
        dismissable={!busy}
        help={`Nothing is installed or changed in the ${multi ? 'folders' : 'folder'}.`}
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
            {multi ? (
              <section className="part-block" aria-label="Part 1">
                <h3 className="part-block-title">Part 1 · the project's own folder</h3>
                <TextField
                  label="Part name"
                  optional
                  value={partName}
                  maxLength={NAME_MAX}
                  placeholder="Main"
                  error={errors[OWN]?.name}
                  onChange={(e) => {
                    setPartName(e.target.value);
                    clearError(OWN);
                  }}
                />
                {ownFolder()}
              </section>
            ) : (
              ownFolder('Usually the root of a git repository.')
            )}
            {more.map((pt, i) => (
              <section key={pt.key} className="part-block" aria-label={`Part ${i + 2}`}>
                <div className="part-block-head">
                  <h3 className="part-block-title">Part {i + 2}</h3>
                  <Button variant="ghost" size="sm" icon="close" iconOnly aria-label={`Remove part ${i + 2}`} onClick={() => removePart(pt.key)} />
                </div>
                <TextField
                  label="Part name"
                  value={pt.name}
                  maxLength={NAME_MAX}
                  placeholder={i === 0 ? 'Frontend' : 'Api'}
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
            {more.length + 1 < MAX_PARTS && (
              <div className="part-more">
                <Button variant="ghost" size="sm" icon="plus" onClick={addPart}>
                  Add a part
                </Button>
                {!multi && <span className="part-more-hint">Another folder of the same product, such as a frontend on another server.</span>}
              </div>
            )}
            <TextField
              label={multi ? 'Project name' : 'Name'}
              optional
              hint={`Shown on Home and in the header, up to ${NAME_MAX} characters. Defaults to the ${multi ? "first part's " : ''}folder name.`}
              value={name}
              maxLength={NAME_MAX}
              placeholder={path.trim().split('/').filter(Boolean).pop() ?? 'my-app'}
              error={nameError || undefined}
              onChange={(e) => {
                setName(e.target.value);
                setNameError('');
              }}
            />
          </fieldset>
        </form>
      </Modal>
      {browseFor && browsing !== null && (
        <FolderBrowser
          machineId={browseFor.machineId}
          machineName={browseMachine?.name ?? browseFor.machineId}
          initialPath={browseFor.path.trim()}
          onClose={() => setBrowsing(null)}
          onChoose={(p) => {
            if (browsing === OWN) {
              setPath(p);
              clearError(OWN);
            } else change(browsing, { path: p });
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
  hint,
  onMachine,
  onPath,
  onBrowse,
}: {
  machines: Machine[];
  machineId: string;
  path: string;
  error?: string;
  hint?: string;
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
      <Field label="Folder" error={error || undefined} hint={hint}>
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
