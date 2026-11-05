// Adding or changing a monitor. What the form asks follows the kind of check.

import { useState, type FormEvent } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Checkbox, Field, FormSection, SegmentedControl, SelectField, TextField } from '../components/form';
import { Modal } from '../components/Modal';
import { useMachines } from '../store';
import { uptimeApi, type Monitor, type MonitorConfig, type MonitorType } from './api';
import { putMonitor } from './store';

const TYPES: { value: MonitorType; label: string }[] = [
  { value: 'http', label: 'HTTP(s)' },
  { value: 'tcp', label: 'TCP port' },
  { value: 'ping', label: 'Ping' },
  { value: 'dns', label: 'DNS' },
  { value: 'push', label: 'Push' },
];

const headersToText = (list?: { name: string; value: string }[]) => (list ?? []).map((h) => `${h.name}: ${h.value}`).join('\n');

function textToHeaders(text: string): { name: string; value: string }[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
    .map((l) => {
      const i = l.indexOf(':');
      return i < 0 ? { name: l, value: '' } : { name: l.slice(0, i).trim(), value: l.slice(i + 1).trim() };
    });
}

/** A name when none was typed: the host is what people call it anyway. */
function nameFrom(f: { type: MonitorType; url: string; host: string; port: string }): string {
  if (f.type === 'http') {
    try {
      return new URL(f.url).hostname;
    } catch {
      return '';
    }
  }
  if (f.type === 'tcp' && f.host) return `${f.host}:${f.port}`;
  return f.type === 'push' ? '' : f.host;
}

interface Props {
  projectId: string;
  monitor?: Monitor;
  onClose: () => void;
  onSaved: (m: Monitor) => void;
}

export function MonitorForm({ projectId, monitor: m, onClose, onSaved }: Props) {
  const machines = useMachines();
  const [type, setType] = useState<MonitorType>(m?.type ?? 'http');
  const [name, setName] = useState(m?.name ?? '');
  const [machineId, setMachineId] = useState(m?.machineId ?? '');
  const [url, setUrl] = useState(m?.url ?? '');
  const [method, setMethod] = useState(m?.method ?? 'GET');
  const [accepted, setAccepted] = useState(m?.accepted ?? '200-299');
  const [keyword, setKeyword] = useState(m?.keyword ?? '');
  const [invert, setInvert] = useState(!!m?.keywordInvert);
  const [ignoreTls, setIgnoreTls] = useState(!!m?.ignoreTls);
  const [headers, setHeaders] = useState(headersToText(m?.headers));
  const [host, setHost] = useState(m?.host ?? '');
  const [port, setPort] = useState(m?.port ? String(m.port) : '');
  const [dnsType, setDnsType] = useState(m?.dnsType ?? 'A');
  const [dnsServer, setDnsServer] = useState(m?.dnsServer ?? '');
  const [dnsExpect, setDnsExpect] = useState(m?.dnsExpect ?? '');
  const [interval, setIntervalSec] = useState(String(m?.intervalSec ?? 60));
  const [retries, setRetries] = useState(String(m?.retries ?? 1));
  const [retryInterval, setRetryInterval] = useState(m?.retryIntervalSec ? String(m.retryIntervalSec) : '');
  const [timeout, setTimeoutSec] = useState(m?.timeoutSec ? String(m.timeoutSec) : '');
  const [notify, setNotify] = useState(m?.notify ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    const body: MonitorConfig = {
      name: name.trim() || nameFrom({ type, url, host, port }),
      type,
      machineId,
      notify,
      url,
      method,
      accepted,
      keyword,
      keywordInvert: invert,
      ignoreTls,
      headers: textToHeaders(headers),
      host,
      port: Number(port) || 0,
      dnsType,
      dnsServer,
      dnsExpect,
      intervalSec: Number(interval) || 0,
      retries: Number(retries) || 0,
      // left empty: as often as the interval, and what fits it
      retryIntervalSec: Number(retryInterval) || 0,
      timeoutSec: Number(timeout) || 0,
    };
    try {
      const saved = m ? await uptimeApi.update(m.id, body) : await uptimeApi.create(projectId, body);
      putMonitor(saved);
      onSaved(saved);
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  const fromMachine = machines.length > 1 && (type === 'http' || type === 'tcp' || type === 'ping');

  return (
    <Modal
      title={m ? `Edit ${m.name}` : 'Add monitor'}
      icon="activity"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="up-monitor-form" loading={busy}>
            {m ? 'Save' : 'Add monitor'}
          </Button>
        </>
      }
    >
      <form id="up-monitor-form" className="up-form" onSubmit={(e) => void submit(e)}>
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
        <Field label="Check" as="group">
          {(a) => <SegmentedControl<MonitorType> block labelledBy={a.labelId} value={type} onChange={setType} options={TYPES} />}
        </Field>
        {type === 'http' && (
          <TextField label="URL" mono required type="url" placeholder="https://example.com/health" value={url} onChange={(e) => setUrl(e.target.value)} autoFocus />
        )}
        {(type === 'tcp' || type === 'ping' || type === 'dns') && (
          <div className="up-form-row">
            <TextField
              label={type === 'dns' ? 'Name to look up' : 'Host'}
              mono
              required
              placeholder={type === 'dns' ? 'example.com' : '10.0.0.5'}
              value={host}
              onChange={(e) => setHost(e.target.value)}
              autoFocus
              fieldClassName="up-grow"
            />
            {type === 'tcp' && <TextField label="Port" mono required inputMode="numeric" placeholder="5432" value={port} onChange={(e) => setPort(e.target.value)} fieldClassName="up-narrow" />}
            {type === 'dns' && (
              <SelectField label="Record" value={dnsType} onChange={(e) => setDnsType(e.target.value)} fieldClassName="up-narrow">
                {['A', 'AAAA', 'CNAME', 'MX', 'NS', 'TXT'].map((t) => (
                  <option key={t}>{t}</option>
                ))}
              </SelectField>
            )}
          </div>
        )}
        {type === 'push' && <p className="up-form-note">Something else reports in: a cron job calls this monitor’s URL, and it goes down when the calls stop. The URL appears once the monitor exists.</p>}
        <TextField label="Name" optional placeholder={nameFrom({ type, url, host, port }) || 'Nightly backup'} maxLength={80} required={type === 'push'} value={name} onChange={(e) => setName(e.target.value)} />
        {type === 'http' && (
          <>
            <TextField label="Text the page must contain" optional hint="Looked for in the first 1 MB." value={keyword} onChange={(e) => setKeyword(e.target.value)} />
            {keyword && <Checkbox checked={invert} onChange={setInvert} label="Down when this text appears, instead of when it is missing" />}
          </>
        )}
        {type === 'dns' && (
          <div className="up-form-row">
            <TextField label="Answer must contain" optional mono value={dnsExpect} onChange={(e) => setDnsExpect(e.target.value)} fieldClassName="up-grow" />
            <TextField label="Resolver" optional mono placeholder="1.1.1.1" value={dnsServer} onChange={(e) => setDnsServer(e.target.value)} fieldClassName="up-grow" />
          </div>
        )}
        {fromMachine && (
          <SelectField label="Check from" hint="From a server, this reaches what only that server can see, such as its own localhost." value={machineId} onChange={(e) => setMachineId(e.target.value)}>
            {machines.map((x) => (
              <option key={x.id} value={x.id === 'local' ? '' : x.id}>
                {x.name}
              </option>
            ))}
          </SelectField>
        )}
        <FormSection title="Timing">
          <div className="up-form-row">
            <TextField label={type === 'push' ? 'Expected every (s)' : 'Check every (s)'} inputMode="numeric" value={interval} onChange={(e) => setIntervalSec(e.target.value)} fieldClassName="up-grow" />
            <TextField label="Retries" inputMode="numeric" value={retries} onChange={(e) => setRetries(e.target.value)} fieldClassName="up-grow" />
            <TextField label="Retry every (s)" inputMode="numeric" placeholder={interval} value={retryInterval} onChange={(e) => setRetryInterval(e.target.value)} fieldClassName="up-grow" />
            {type !== 'push' && <TextField label="Timeout (s)" inputMode="numeric" placeholder={String(Math.min(Math.floor((Number(interval) || 60) * 0.8), 30))} value={timeout} onChange={(e) => setTimeoutSec(e.target.value)} fieldClassName="up-grow" />}
          </div>
          <p className="up-form-note">Every 20 s at the most. Retries are failures in a row that stay pending before it counts as down.</p>
        </FormSection>
        {type === 'http' && (
          <FormSection title="Request">
            <div className="up-form-row">
              <SelectField label="Method" value={method} onChange={(e) => setMethod(e.target.value)} fieldClassName="up-narrow">
                <option>GET</option>
                <option>HEAD</option>
              </SelectField>
              <TextField label="Accepted status codes" mono value={accepted} onChange={(e) => setAccepted(e.target.value)} fieldClassName="up-grow" />
            </div>
            <Field label="Headers" optional hint="One per line, such as Authorization: Bearer abc. Kept on the hub as written.">
              {({ labelId: _l, ...a }) => <textarea className="control mono" rows={2} spellCheck={false} {...a} value={headers} onChange={(e) => setHeaders(e.target.value)} />}
            </Field>
            <Checkbox checked={ignoreTls} onChange={setIgnoreTls} label="Accept an invalid certificate" />
          </FormSection>
        )}
        <Checkbox checked={notify} onChange={setNotify} label="Tell me when it goes down or comes back" />
      </form>
    </Modal>
  );
}
