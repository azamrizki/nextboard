// New instance… (multi-instance design §4.2): another hub on This PC with its own data folder
// (~/.nextboard-<name>), ports and service, made by this hub (POST /api/instances) with this
// instance's admin password again. It ends on the new hub's single-use setup link, an ordinary
// link so no popup blocker stands in the way, over plain HTTP as well.

import { useEffect, useId, useState } from 'react';
import { api, ApiError, errorMessage } from '../api';
import { copyText } from '../lib/clipboard';
import { formatDateTime } from '../lib/format';
import { hostLabel, instanceNameError, portError, setupLink, tunnelCommand } from '../lib/instance';
import type { CreatedInstance, InstancesList } from '../lib/types';
import { Button } from './Button';
import { Checkbox, TextField } from './form';
import { Icon } from './Icon';
import { Modal } from './Modal';

type Address = 'loopback' | 'same';
type Errors = Partial<Record<'name' | 'port' | 'password', string>>;

export function NewInstanceDialog({ onClose }: { onClose: () => void }) {
  const [list, setList] = useState<InstancesList | null>(null);
  const [loadError, setLoadError] = useState('');
  const [name, setName] = useState('');
  const [hubName, setHubName] = useState('');
  const [touchedHubName, setTouchedHubName] = useState(false);
  const [address, setAddress] = useState<Address>('loopback');
  const [port, setPort] = useState('');
  const [fromThis, setFromThis] = useState(false);
  const [password, setPassword] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [serverErrors, setServerErrors] = useState<Errors>({});
  const [formError, setFormError] = useState('');
  const [created, setCreated] = useState<CreatedInstance | null>(null);
  const formId = useId();

  useEffect(() => {
    let live = true;
    api
      .instances()
      .then((l) => {
        if (!live) return;
        setList(l);
        if (l.suggest.port) setPort(String(l.suggest.port));
        // the same host as this hub, so it is reached the same way (loopback is the other choice)
        setAddress(l.suggest.loopback ? 'loopback' : 'same');
      })
      .catch((err) => live && setLoadError(errorMessage(err)));
    return () => {
      live = false;
    };
  }, []);

  const self = list?.self ?? '';
  const suggest = list?.suggest;
  const taken = [self, ...(list?.items ?? []).map((i) => i.name)].filter(Boolean);
  const hostname = suggest?.hostname || 'nextboard';
  useEffect(() => {
    if (!touchedHubName) setHubName(name ? `${hostname} · ${name}` : '');
  }, [name, hostname, touchedHubName]);

  const checks: Errors = {
    name: instanceNameError(name, taken) || undefined,
    port: portError(port) || undefined,
    password: password ? undefined : 'This instance’s admin password, to confirm it is you.',
  };
  const shown: Errors = {
    name: serverErrors.name ?? (submitted || name ? checks.name : undefined),
    port: serverErrors.port ?? (submitted ? checks.port : undefined),
    password: serverErrors.password ?? (submitted ? checks.password : undefined),
  };
  const host = address === 'loopback' ? '127.0.0.1' : (suggest?.host ?? '127.0.0.1');
  const beyond = address === 'same' && !suggest?.loopback;
  const blockPort = suggest?.port && port.trim() === String(suggest.port) ? suggest.previewRange : undefined;

  const submit = async () => {
    setSubmitted(true);
    setFormError('');
    setServerErrors({});
    if (!list || checks.name || checks.port || checks.password) return;
    setBusy(true);
    try {
      const c = await api.createInstance({
        name,
        hubName: hubName.trim() || undefined,
        listenHost: host,
        port: Number(port.trim()),
        allowNetwork: beyond,
        password,
      });
      setPassword('');
      setCreated(c);
    } catch (err) {
      const code = err instanceof ApiError ? err.code : '';
      const msg = errorMessage(err);
      if (code === 'bad_instance_name' || code === 'instance_exists') setServerErrors({ name: msg });
      else if (code === 'port_in_use' || code === 'no_free_block') setServerErrors({ port: msg });
      else if (code === 'bad_password') setFormError('Wrong admin password: nothing was made.');
      else setFormError(msg);
    } finally {
      setBusy(false);
    }
  };

  if (created) return <NewInstanceDone created={created} from={fromThis ? self : ''} onClose={onClose} />;

  return (
    <Modal
      title="New instance"
      icon="server"
      size="md"
      onClose={() => !busy && onClose()}
      dismissable={!busy}
      help={name && !checks.name ? `Its data goes in ~/.nextboard-${name}.` : 'Another hub on This PC, with its own data, settings and logins.'}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form={formId} variant="primary" icon="plus" loading={busy} disabled={!list}>
            Create instance
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="form new-instance-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {loadError && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{loadError}</span>
          </div>
        )}
        {list?.noFreeBlock && !suggest?.port && (
          <div className="alert warning" role="note">
            <Icon name="alertCircle" />
            <span>{list.noFreeBlock} Type a free port.</span>
          </div>
        )}
        <TextField
          label="Name"
          mono
          autoFocus
          autoComplete="off"
          spellCheck={false}
          value={name}
          maxLength={24}
          onChange={(e) => {
            setName(e.target.value.trim().toLowerCase());
            setServerErrors((x) => ({ ...x, name: undefined }));
          }}
          error={shown.name}
          hint="Lowercase letters, digits and dashes, for example work or personal."
        />
        <TextField
          label="Hub name"
          value={hubName}
          maxLength={64}
          onChange={(e) => {
            setTouchedHubName(true);
            setHubName(e.target.value);
          }}
          hint="What its header and the app call it. Its setup can change it."
        />
        <fieldset className="field new-instance-address">
          <legend className="field-label">Address</legend>
          <label className="check">
            <input type="radio" name={`${formId}-address`} checked={address === 'loopback'} onChange={() => setAddress('loopback')} />
            This machine only (127.0.0.1)
          </label>
          {suggest && !suggest.loopback && (
            <label className="check">
              <input type="radio" name={`${formId}-address`} checked={address === 'same'} onChange={() => setAddress('same')} />
              Same address as this hub ({hostLabel(suggest.host)})
            </label>
          )}
          <span className="field-hint">
            {address === 'same' ? 'Reached the way this hub is.' : 'Reached from This PC, or through a tunnel or a reverse proxy.'}
          </span>
        </fieldset>
        {beyond && (
          <div className="alert warning" role="note">
            <Icon name="alertCircle" />
            <span>It listens beyond this machine over plain HTTP, which others on the network path can read. Put HTTPS in front of it, or keep it on this machine only and use a tunnel.</span>
          </div>
        )}
        <TextField
          label="Port"
          mono
          inputMode="numeric"
          value={port}
          onChange={(e) => {
            setPort(e.target.value);
            setServerErrors((x) => ({ ...x, port: undefined }));
          }}
          error={shown.port}
          hint={blockPort ? `The next free block: its previews get ports ${blockPort[0]}–${blockPort[1]}.` : 'Its previews get the next free block of 20 ports.'}
        />
        <div className="field">
          <Checkbox checked={fromThis} onChange={setFromThis} label="Start from this instance's settings" />
          <span className="field-hint">
            Its setup then offers to copy settings from {self || 'this instance'}, with this instance’s admin password; nothing is copied before you choose.
          </span>
        </div>
        <TextField
          label="Your admin password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => {
            setPassword(e.target.value);
            setServerErrors((x) => ({ ...x, password: undefined }));
          }}
          error={shown.password}
          hint="This instance’s admin password, asked again for this."
        />
        {formError && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{formError}</span>
          </div>
        )}
      </form>
    </Modal>
  );
}

function CopyLine({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <span className="copy-line">
      <code>{text}</code>
      <Button
        size="sm"
        variant="ghost"
        iconOnly
        icon={copied ? 'check' : 'clipboard'}
        aria-label={label}
        onClick={() => void copyText(text).then((ok) => ok && setCopied(true))}
      />
    </span>
  );
}

function NewInstanceDone({ created: c, from, onClose }: { created: CreatedInstance; from: string; onClose: () => void }) {
  const here = { protocol: window.location.protocol, hostname: window.location.hostname };
  const urls = c.setupUrls ?? [];
  const link = setupLink(urls, c.listen, here, from || undefined);
  const withFrom = (u: string) => {
    if (!from) return u;
    try {
      const x = new URL(u);
      x.searchParams.set('from', from);
      return x.toString();
    } catch {
      return u;
    }
  };
  const others = urls.map(withFrom).filter((u) => u !== link);
  // through a tunnel the new hub is on this browser's localhost
  const tunneled = link ? null : setupLink(urls, c.listen, { protocol: 'http:', hostname: 'localhost' }, from || undefined);
  return (
    <Modal
      title="New instance"
      icon="server"
      size="md"
      onClose={onClose}
      footer={
        <Button variant="secondary" onClick={onClose}>
          Done
        </Button>
      }
    >
      <div className="form new-instance-done">
        <p>
          Instance <strong className="mono">{c.instance}</strong> is made. Its data is in <span className="mono">{c.dataDir}</span>, its hub listens on{' '}
          <span className="mono">{c.listen}</span>.
        </p>
        {c.started && c.service && (
          <p className="text-2">
            Its service {c.service.name} runs it{c.healthy ? '.' : ', but it has not answered yet.'}
          </p>
        )}
        {c.startError && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>
              Its service did not start: {c.startError}. See <code>nextboard logs --instance {c.instance}</code>.
            </span>
          </div>
        )}
        {!c.started && c.command && (
          <div className="alert warning" role="note">
            <Icon name="alertCircle" />
            <div>
              This hub does not run as a service, so nothing started the new one. Run this on This PC; the setup link works once it runs:
              <CopyLine text={c.command} label="Copy the command" />
            </div>
          </div>
        )}
        {c.warnings?.map((w) => (
          <p key={w} className="fs-xs text-3">
            {w}
          </p>
        ))}
        {link ? (
          <a className="btn btn-primary btn-block" href={link} target="_blank" rel="noopener noreferrer">
            <Icon name="external" />
            Open setup
          </a>
        ) : (
          <div className="alert warning" role="note">
            <Icon name="alertCircle" />
            <div>
              None of its addresses is reachable from this browser: it listens on {c.listen} only. Open a tunnel from the machine this browser runs on:
              <CopyLine text={tunnelCommand(c.listen, window.location.hostname)} label="Copy the tunnel command" />
              {tunneled && (
                <>
                  then open the setup link there:
                  <CopyLine text={tunneled} label="Copy the setup link" />
                </>
              )}
              Or put a reverse proxy in front of it (deploy/nginx.conf.example) and give it that address with <code>--public-url</code>.
            </div>
          </div>
        )}
        {others.length > 0 && (
          <div>
            <span className="fs-xs text-3">Other addresses</span>
            <ul className="plain url-list">
              {others.map((u) => (
                <li key={u} title={u}>
                  {u}
                </li>
              ))}
            </ul>
          </div>
        )}
        <p className="fs-xs text-3">
          The link works once{c.expiresAt ? `, until ${formatDateTime(c.expiresAt)}` : ''}, and is shown only here.
        </p>
      </div>
    </Modal>
  );
}
