// "Add machine": pick a Host from the hub's ~/.ssh/config, or enter details manually.
// Choices with 2–4 options use segmented controls / radio cards (DESIGN.md §3.2).

import { useEffect, useState, type FormEvent } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Field, FilePick, FormSection, RadioCards, SegmentedControl, TextField, type RadioCardOption } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import type { AddMachine, AuthMethod, Machine, PersistMode, SSHConfigHost } from '../lib/types';

type Source = 'sshconfig' | 'manual';

interface AddMachineDialogProps {
  onClose: () => void;
  /** Called with the created machine (status may be "hostkey", "connected" or "error"). */
  onAdded: (m: Machine) => void;
}

const AUTH_OPTIONS: RadioCardOption<AuthMethod>[] = [
  { value: 'sshconfig', title: 'Hub’s SSH keys', description: 'Keys in the hub user’s ~/.ssh, then ssh-agent' },
  { value: 'agent', title: 'ssh-agent', description: 'Keys loaded in the hub’s running agent' },
  { value: 'key', title: 'Private key', description: 'Upload a key — stored encrypted on the hub' },
  { value: 'password', title: 'Password', description: 'Stored encrypted; keys are safer' },
];

const PERSIST_OPTIONS = [
  { value: 'auto' as const, label: 'Auto' },
  { value: 'tmux' as const, label: 'tmux' },
  { value: 'direct' as const, label: 'Direct' },
];

const PERSIST_HINT: Record<PersistMode, string> = {
  auto: 'Uses tmux when the machine has it, so agents survive disconnects.',
  tmux: 'Agents keep running in tmux when SSH drops or the hub restarts.',
  direct: 'Agents run on the SSH channel and are relaunched with resume args after a drop.',
};

type Errors = Partial<Record<'host' | 'port' | 'privateKey' | 'password' | 'configHost', string>>;

export function AddMachineDialog({ onClose, onAdded }: AddMachineDialogProps) {
  const [source, setSource] = useState<Source>('sshconfig');
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState('');
  const [errors, setErrors] = useState<Errors>({});

  // shared
  const [name, setName] = useState('');
  const [persistMode, setPersistMode] = useState<PersistMode>('auto');

  // ssh config
  const [hosts, setHosts] = useState<SSHConfigHost[] | null>(null);
  const [hostsError, setHostsError] = useState('');
  const [configHost, setConfigHost] = useState('');

  // manual
  const [host, setHost] = useState('');
  const [port, setPort] = useState('22');
  const [user, setUser] = useState('');
  const [authMethod, setAuthMethod] = useState<AuthMethod>('sshconfig');
  const [privateKey, setPrivateKey] = useState('');
  const [keyFileName, setKeyFileName] = useState('');
  const [passphrase, setPassphrase] = useState('');
  const [password, setPassword] = useState('');

  /** A field's error disappears as soon as the user edits that field. */
  const clearError = (k: keyof Errors) => setErrors((e) => (e[k] ? { ...e, [k]: undefined } : e));

  useEffect(() => {
    api
      .sshConfigHosts()
      .then((list) => {
        setHosts(list);
        if (list.length === 0) setSource('manual');
      })
      .catch((err) => setHostsError(errorMessage(err)));
  }, []);

  const onKeyFile = async (file: File) => {
    if (file.size > 32 * 1024) {
      setErrors((e) => ({ ...e, privateKey: 'That file is too large to be a private key (max 32 KB).' }));
      return;
    }
    setPrivateKey(await file.text());
    setKeyFileName(file.name);
    setErrors((e) => ({ ...e, privateKey: undefined }));
  };

  const build = (): { body?: AddMachine; errors: Errors } => {
    const errs: Errors = {};
    const base = { name: name.trim() || undefined, persistMode };
    if (source === 'sshconfig') {
      if (!configHost) errs.configHost = 'Pick one of the hosts above.';
      return { errors: errs, body: { ...base, sshConfigHost: configHost, authMethod: 'sshconfig' } };
    }
    if (!host.trim()) errs.host = 'Enter a hostname or IP address.';
    const portNum = Number(port);
    if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) errs.port = 'Use a port from 1 to 65535.';
    const body: AddMachine = { ...base, host: host.trim(), port: portNum, user: user.trim() || undefined, authMethod };
    if (authMethod === 'key') {
      if (!privateKey.trim()) errs.privateKey = 'Paste a private key or load it from a file.';
      body.privateKey = privateKey;
      if (passphrase) body.passphrase = passphrase;
    } else if (authMethod === 'password') {
      if (!password) errs.password = 'Enter the SSH password.';
      body.password = password;
    }
    return { errors: errs, body };
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const { body, errors: errs } = build();
    setErrors(errs);
    setFormError('');
    if (Object.values(errs).some(Boolean) || !body) return;
    setBusy(true);
    try {
      onAdded(await api.addMachine(body));
    } catch (err) {
      setFormError(errorMessage(err));
      setBusy(false);
    }
  };

  const hostOptions: RadioCardOption<string>[] = (hosts ?? []).map((h) => ({
    value: h.host,
    title: h.host,
    description: `${h.user ? `${h.user}@` : ''}${h.hostName || h.host}${h.port && h.port !== 22 ? `:${h.port}` : ''}${
      h.proxyJump ? ` via ${h.proxyJump}` : ''
    }`,
  }));

  return (
    <Modal
      title="Add machine"
      icon="server"
      onClose={() => !busy && onClose()}
      size="md"
      dismissable={!busy}
      help="Agentless — nothing is installed on the target."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="add-machine-form" variant="primary" icon="plus" loading={busy}>
            Add machine
          </Button>
        </>
      }
    >
      <form id="add-machine-form" className="form" onSubmit={submit} noValidate>
        <fieldset disabled={busy}>
          <SegmentedControl<Source>
            label="Where to take the connection details from"
            block
            value={source}
            onChange={(v) => {
              setSource(v);
              setErrors({});
            }}
            options={[
              { value: 'sshconfig', label: 'From ~/.ssh/config', icon: 'file' },
              { value: 'manual', label: 'Manual', icon: 'pencil' },
            ]}
          />

          {source === 'sshconfig' ? (
            <FormSection title="Host">
              {hostsError && (
                <div className="alert" role="alert">
                  <Icon name="alertCircle" />
                  <span>{hostsError}</span>
                </div>
              )}
              {!hosts && !hostsError && (
                <p className="status">
                  <span className="spinner" aria-hidden="true" /> Reading ~/.ssh/config…
                </p>
              )}
              {hosts && hosts.length === 0 && (
                <p className="text-2">No hosts in the hub user’s ~/.ssh/config yet. Switch to Manual to enter one.</p>
              )}
              {hosts && hosts.length > 0 && (
                <Field
                  as="group"
                  label="Host from ~/.ssh/config"
                  hint="HostName, User, Port, IdentityFile and ProxyJump come from the config."
                  error={errors.configHost}
                >
                  {({ labelId }) => (
                    <div className="host-list">
                      <RadioCards
                        labelledBy={labelId}
                        columns={1}
                        value={configHost}
                        onChange={(v) => {
                          setConfigHost(v);
                          clearError('configHost');
                        }}
                        options={hostOptions}
                      />
                    </div>
                  )}
                </Field>
              )}
            </FormSection>
          ) : (
            <>
              <FormSection title="Connection">
                <div className="field-row">
                  <TextField
                    fieldClassName="span-9"
                    label="Host"
                    mono
                    value={host}
                    onChange={(e) => {
                      setHost(e.target.value);
                      clearError('host');
                    }}
                    placeholder="10.0.0.5"
                    spellCheck={false}
                    autoComplete="off"
                    error={errors.host}
                  />
                  <TextField
                    fieldClassName="span-3"
                    label="Port"
                    mono
                    className="tabular"
                    value={port}
                    onChange={(e) => {
                      setPort(e.target.value);
                      clearError('port');
                    }}
                    inputMode="numeric"
                    error={errors.port}
                  />
                </div>
                <TextField
                  label="User"
                  optional
                  mono
                  value={user}
                  onChange={(e) => setUser(e.target.value)}
                  placeholder="dev"
                  hint="Defaults to the user running the hub."
                  spellCheck={false}
                  autoComplete="off"
                />
              </FormSection>

              <FormSection title="Authentication">
                <Field as="group" label="Method">
                  {({ labelId }) => (
                    <RadioCards labelledBy={labelId} value={authMethod} onChange={setAuthMethod} options={AUTH_OPTIONS} />
                  )}
                </Field>

                {authMethod === 'key' && (
                  <>
                    <Field label="Private key" error={errors.privateKey} hint="Never sent back to any client.">
                      {({ labelId: _l, ...a11y }) => (
                        <textarea
                          className="control"
                          value={privateKey}
                          onChange={(e) => {
                            setPrivateKey(e.target.value);
                            setKeyFileName('');
                            clearError('privateKey');
                          }}
                          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                          spellCheck={false}
                          rows={6}
                          {...a11y}
                        />
                      )}
                    </Field>
                    <FilePick ariaLabel="Load private key from a file" fileName={keyFileName} onFile={(f) => void onKeyFile(f)} disabled={busy} />
                    <TextField
                      label="Passphrase"
                      optional
                      type="password"
                      value={passphrase}
                      onChange={(e) => setPassphrase(e.target.value)}
                      autoComplete="off"
                    />
                  </>
                )}
                {authMethod === 'password' && (
                  <TextField
                    label="Password"
                    type="password"
                    value={password}
                    onChange={(e) => {
                      setPassword(e.target.value);
                      clearError('password');
                    }}
                    autoComplete="off"
                    error={errors.password}
                  />
                )}
              </FormSection>
            </>
          )}

          <FormSection title="Options">
            <TextField
              label="Display name"
              optional
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={source === 'sshconfig' ? configHost || 'devbox' : host || 'devbox'}
              hint="Shown on the board as “Claude Code @ name”. Defaults to the host."
              maxLength={64}
            />
            <Field as="group" label="Persistence" hint={PERSIST_HINT[persistMode]}>
              {({ labelId }) => (
                <SegmentedControl<PersistMode> labelledBy={labelId} value={persistMode} onChange={setPersistMode} options={PERSIST_OPTIONS} />
              )}
            </Field>
          </FormSection>

          {formError && (
            <div className="alert" role="alert">
              <Icon name="alertCircle" />
              <span>{formError}</span>
            </div>
          )}
        </fieldset>
      </form>
    </Modal>
  );
}
