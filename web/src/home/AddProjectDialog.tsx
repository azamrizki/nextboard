// Add a project: a folder on one of the machines (browsed with the folder picker) and an
// optional display name (≤ 24 characters). Adding a folder that is already a project
// just opens it.

import { useState, type FormEvent } from 'react';
import { api, errorMessage } from '../api';
import { machineReady, machineStatus } from '../board/sessionStatus';
import { Button } from '../components/Button';
import { FolderBrowser } from '../components/FolderBrowser';
import { Field, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { NAME_MAX } from '../components/ProjectBar';
import { toast } from '../components/Toast';
import type { Project } from '../lib/types';
import { store, useMachines } from '../store';

export function AddProjectDialog({ onClose, onAdded }: { onClose: () => void; onAdded: (p: Project, existed: boolean) => void }) {
  const machines = useMachines();
  const [machineId, setMachineId] = useState(machines[0]?.id ?? 'local');
  const [path, setPath] = useState('');
  const [name, setName] = useState('');
  const [browsing, setBrowsing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const machine = machines.find((m) => m.id === machineId);

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!path.trim()) {
      setError('Choose a folder.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const before = new Set(Object.keys(store.getState().projects));
      const p = await api.createProject({ machineId, path: path.trim(), name: name.trim() || undefined });
      store.upsertProject(p);
      const existed = before.has(p.id);
      if (existed) toast.info(`${p.name} is already a project — opening it.`);
      onAdded(p, existed);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <>
      <Modal
        title="Add project"
        icon="folder"
        size="md"
        onClose={() => !busy && onClose()}
        dismissable={!busy}
        help="Nothing is installed or changed in the folder."
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
        <form id="add-project-form" className="form" onSubmit={submit}>
          <fieldset disabled={busy}>
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
            <Field label="Folder" error={error || undefined} hint="Usually the root of a git repository.">
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
            <TextField
              label="Name"
              optional
              hint={`Shown on Home and in the header, up to ${NAME_MAX} characters. Defaults to the folder name.`}
              value={name}
              maxLength={NAME_MAX}
              placeholder={path.trim().split('/').filter(Boolean).pop() ?? 'my-app'}
              onChange={(e) => setName(e.target.value)}
            />
          </fieldset>
        </form>
      </Modal>
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
