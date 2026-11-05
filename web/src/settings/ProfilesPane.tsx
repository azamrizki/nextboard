// Settings › Login profiles (docs/API-agents.md §10, owner decision #4): a profile is a
// config folder handed to Claude Code (CLAUDE_CONFIG_DIR) or Codex (CODEX_HOME).
// On This PC NextBoard may create the folder; on remote machines it only references a
// folder that already exists.

import { useMemo, useState, type FormEvent } from 'react';
import { api, ApiError, errorMessage } from '../api';
import { supportsProfiles, useAgentCatalog } from '../agents/catalog';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { FolderBrowser } from '../components/FolderBrowser';
import { Checkbox, Field, RadioCards, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { formatDateTime } from '../lib/format';
import type { Agent, Machine, Profile } from '../lib/types';
import { store, useMachines, useProfiles } from '../store';

export function ProfilesPane() {
  const machines = useMachines();
  const profiles = useProfiles();
  const catalog = useAgentCatalog();
  const settings = useEffectiveSettings();
  const agents = catalog.agents.filter((a) => supportsProfiles(a) && !settings.agents.disabled?.[a.id]);
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<Profile | null>(null);
  const [renaming, setRenaming] = useState<Profile | null>(null);
  const agentName = (id: string) => catalog.agents.find((a) => a.id === id)?.name ?? id;

  const setDefault = async (p: Profile, on: boolean) => {
    try {
      await patchSettings({ agents: { profiles: { [p.machineId]: { [p.agent]: on ? p.id : null } } } });
      toast.success(on ? `New ${agentName(p.agent)} consoles on ${machineName(machines, p.machineId)} use “${p.name}”.` : `New consoles use the default login again.`);
    } catch (err) {
      toast.error(`Could not change the default profile: ${errorMessage(err)}`);
    }
  };

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="pf-title">
        <div className="settings-card-head">
          <h3 id="pf-title" className="section-title">
            Profiles
          </h3>
          <Button size="sm" variant="primary" icon="plus" onClick={() => setAdding(true)} disabled={agents.length === 0}>
            Add profile
          </Button>
        </div>
        <p className="setting-hint">
          Agents keep their login, memory and settings in NextBoard’s own folder, apart from your CLI setup. Each profile is its own config folder; the CLI signs in there the first time
          you start a console with it. On remote machines NextBoard only uses folders that already exist.
        </p>
        {profiles.length === 0 && <div className="list-msg">No profiles yet. Consoles use each CLI’s default login.</div>}
        {machines
          .filter((m) => profiles.some((p) => p.machineId === m.id))
          .map((m) => (
            <div key={m.id} className="profile-group">
              <h4 className="profile-machine">
                <Icon name={m.kind === 'ssh' ? 'server' : 'monitor'} />
                {m.name}
              </h4>
              <ul className="plain profile-list">
                {profiles
                  .filter((p) => p.machineId === m.id)
                  .map((p) => {
                    const isDefault = settings.agents.profiles?.[p.machineId]?.[p.agent] === p.id;
                    return (
                      <li key={p.id} className="profile-row">
                        <span className="profile-initials" aria-hidden="true">
                          {p.name.slice(0, 2).toUpperCase()}
                        </span>
                        <div className="grow">
                          <div className="row profile-title">
                            <strong className="truncate">{p.name}</strong>
                            <span className="cap-chip">{agentName(p.agent)}</span>
                            {isDefault && <span className="badge badge-info">Default</span>}
                            {!p.exists && <span className="badge badge-danger">Folder missing</span>}
                          </div>
                          <div className="mono fs-xs text-3 truncate" title={p.dir}>
                            {p.dir}
                          </div>
                          <div className="fs-xs text-3">
                            {p.home ? 'Login, memory and settings of agents in NextBoard; your own CLI setup stays apart' : p.createdByHub ? 'Created by NextBoard' : 'Existing folder'} · {formatDateTime(p.createdAt)}
                          </div>
                        </div>
                        <div className="profile-actions">
                          <Button size="sm" variant="ghost" onClick={() => void setDefault(p, !isDefault)}>
                            {isDefault ? 'Unset default' : 'Use by default'}
                          </Button>
                          <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label={`Rename ${p.name}`} onClick={() => setRenaming(p)} />
                          {!p.home && (
                            <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label={`Remove ${p.name}`} onClick={() => setDeleting(p)} />
                          )}
                        </div>
                      </li>
                    );
                  })}
              </ul>
            </div>
          ))}
      </section>
      {adding && <ProfileDialog machines={machines} agents={agents} onClose={() => setAdding(false)} />}
      {renaming && <RenameProfileDialog profile={renaming} onClose={() => setRenaming(null)} />}
      {deleting && <DeleteProfileDialog profile={deleting} machine={machines.find((m) => m.id === deleting.machineId)} onClose={() => setDeleting(null)} />}
    </div>
  );
}

function machineName(machines: Machine[], id: string) {
  return machines.find((m) => m.id === id)?.name ?? id;
}

function ProfileDialog({ machines, agents, onClose }: { machines: Machine[]; agents: Agent[]; onClose: () => void }) {
  const [machineId, setMachineId] = useState(machines[0]?.id ?? 'local');
  const [agent, setAgent] = useState(agents[0]?.id ?? 'claude');
  const [name, setName] = useState('');
  const machine = machines.find((m) => m.id === machineId);
  const local = machine?.kind !== 'ssh';
  const [mode, setMode] = useState<'create' | 'existing'>('create');
  const [dir, setDir] = useState('');
  const [browsing, setBrowsing] = useState(false);
  const [errors, setErrors] = useState<{ name?: string; dir?: string; form?: string }>({});
  const [busy, setBusy] = useState(false);
  const effectiveMode = local ? mode : 'existing';
  const agentObj = agents.find((a) => a.id === agent);
  const envHint = useMemo(() => (agentObj ? `Passed to ${agentObj.name} as ${agentObj.profileEnv}.` : ''), [agentObj]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const errs: typeof errors = {};
    if (!name.trim()) errs.name = 'Name the profile, for example Work.';
    if (effectiveMode === 'existing' && !dir.trim()) errs.dir = 'Choose the folder that holds this login.';
    setErrors(errs);
    if (Object.keys(errs).length) return;
    setBusy(true);
    try {
      const p = await api.createProfile({
        machineId,
        agent,
        name: name.trim(),
        ...(effectiveMode === 'create' ? { create: true } : { dir: dir.trim() }),
      });
      store.upsertProfile(p);
      toast.success(
        effectiveMode === 'create'
          ? `Profile “${p.name}” created. Start a ${agentObj?.name ?? 'console'} with it to sign in.`
          : `Profile “${p.name}” added.`,
      );
      onClose();
    } catch (err) {
      if (err instanceof ApiError && err.code === 'profile_dir_missing') setErrors({ dir: `That folder doesn’t exist on ${machine?.name}. NextBoard never creates folders on remote machines.` });
      else setErrors({ form: errorMessage(err) });
      setBusy(false);
    }
  };

  return (
    <>
      <Modal
        title="Add a login profile"
        icon="user"
        size="md"
        onClose={onClose}
        dismissable={!busy}
        footer={
          <>
            <Button variant="secondary" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" form="profile-form" variant="primary" loading={busy}>
              Add profile
            </Button>
          </>
        }
      >
        <form id="profile-form" className="form" onSubmit={(e) => void submit(e)} noValidate>
          <fieldset disabled={busy}>
            <div className="field-row">
              <SelectField fieldClassName="span-6" label="Machine" value={machineId} onChange={(e) => setMachineId(e.target.value)}>
                {machines.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </SelectField>
              <SelectField fieldClassName="span-6" label="Agent" value={agent} onChange={(e) => setAgent(e.target.value)} hint={envHint}>
                {agents.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </SelectField>
            </div>
            <TextField label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Work" maxLength={40} error={errors.name} autoFocus />
            {local ? (
              <Field label="Folder" as="group">
                {({ labelId }) => (
                  <RadioCards
                    labelledBy={labelId}
                    value={mode}
                    onChange={setMode}
                    options={[
                      { value: 'create', title: 'New folder', description: 'NextBoard creates a private folder in its data directory.' },
                      { value: 'existing', title: 'Existing folder', description: 'A config folder you already use, e.g. ~/.claude-work.' },
                    ]}
                  />
                )}
              </Field>
            ) : (
              <div className="alert warning" role="note">
                <Icon name="info" />
                <span>On {machine?.name} NextBoard only uses a folder that already exists. Create it there first if needed.</span>
              </div>
            )}
            {effectiveMode === 'existing' && (
              <Field label="Folder path" error={errors.dir} hint={`Must already exist on ${machine?.name ?? 'the machine'}.`}>
                {({ labelId: _l, ...a11y }) => (
                  <div className="input-group has-lead has-addon">
                    <span className="ig-lead">
                      <Icon name="folder" />
                    </span>
                    <input className="control mono" value={dir} onChange={(e) => setDir(e.target.value)} placeholder="~/.claude-work" spellCheck={false} {...a11y} />
                    <Button className="ig-addon" onClick={() => setBrowsing(true)} disabled={!machine}>
                      Browse
                    </Button>
                  </div>
                )}
              </Field>
            )}
            {errors.form && (
              <div className="alert" role="alert">
                <Icon name="alertCircle" />
                <span>{errors.form}</span>
              </div>
            )}
          </fieldset>
        </form>
      </Modal>
      {browsing && machine && (
        <FolderBrowser
          machineId={machine.id}
          machineName={machine.name}
          initialPath={dir || machine.probe?.home || ''}
          title={`Choose a profile folder on ${machine.name}`}
          onClose={() => setBrowsing(false)}
          onChoose={(p) => {
            setDir(p);
            setBrowsing(false);
          }}
        />
      )}
    </>
  );
}

function RenameProfileDialog({ profile, onClose }: { profile: Profile; onClose: () => void }) {
  const [name, setName] = useState(profile.name);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError('Name the profile.');
      return;
    }
    setBusy(true);
    try {
      store.upsertProfile(await api.updateProfile(profile.id, { name: name.trim() }));
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Rename profile"
      icon="pencil"
      size="sm"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="rename-profile" variant="primary" loading={busy}>
            Save name
          </Button>
        </>
      }
    >
      <form id="rename-profile" className="form" onSubmit={(e) => void submit(e)}>
        <TextField label="Name" value={name} onChange={(e) => setName(e.target.value)} maxLength={40} error={error} autoFocus />
      </form>
    </Modal>
  );
}

function DeleteProfileDialog({ profile, machine, onClose }: { profile: Profile; machine: Machine | undefined; onClose: () => void }) {
  const [purge, setPurge] = useState(false);
  const canPurge = profile.createdByHub && machine?.kind !== 'ssh';
  return (
    <ConfirmDialog
      title={`Remove “${profile.name}”?`}
      confirmLabel="Remove profile"
      danger
      onClose={onClose}
      onConfirm={async () => {
        await api.deleteProfile(profile.id, canPurge && purge);
        store.removeProfile(profile.id);
        toast.success(`Profile “${profile.name}” removed.`);
      }}
    >
      <p>
        Consoles stop offering it. {canPurge ? 'The folder stays unless you delete it below.' : 'Its folder on the machine is not touched.'}
      </p>
      {canPurge && <Checkbox checked={purge} onChange={setPurge} label="Also delete the folder NextBoard created (signs this account out)" />}
    </ConfirmDialog>
  );
}
