// Handoff (docs/API-orchestration.md §9, D18): continue a console's task in a fresh console
// of another agent — on any machine (a NextBoard extension). The hub collects the
// transcript, writes a brief, starts the new console next to the source and delivers it.

import { orchError } from '../actions';
import { useMemo, useState, type FormEvent } from 'react';
import { byPriority, useMachineAgents } from '../../agents/catalog';
import { defaultProfileOf, useEffectiveSettings } from '../../agents/settings';
import { jumpToSession } from '../../agents/jump';
import { ApiError } from '../../api';
import { machineReady } from '../../board/sessionStatus';
import { Button } from '../../components/Button';
import { Field, SelectField } from '../../components/form';
import { Icon } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { useMachines, useProfiles, useSession } from '../../store';
import { AccountOptions, splitAccount } from '../../providers/AccountOptions';
import { compatible, useProviders } from '../../providers/providers';
import { orchApi } from '../api';
import { is404, orchStore, useOrch } from '../store';

const STEPS: { id: string; label: string }[] = [
  { id: 'collecting', label: 'Reading the conversation' },
  { id: 'summarizing', label: 'Writing the brief' },
  { id: 'spawning', label: 'Starting the new console' },
  { id: 'waitingReady', label: 'Waiting for it to be ready' },
  { id: 'delivering', label: 'Handing over the brief' },
];

export default function HandoffDialog({ sessionId, workspaceId, projectId, onClose }: { sessionId: string; workspaceId: string; projectId: string; onClose: () => void }) {
  const source = useSession(sessionId);
  const machines = useMachines();
  const profiles = useProfiles();
  const providers = useProviders();
  const settings = useEffectiveSettings();
  const [machineId, setMachineId] = useState(source?.machineId ?? 'local');
  const ma = useMachineAgents(machineId);
  const options = useMemo(() => ma.agents.filter((a) => a.kind === 'agent' && a.available !== false).sort(byPriority), [ma.agents]);
  const [agent, setAgent] = useState('');
  const [profileId, setProfileId] = useState('');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [hid, setHid] = useState<string | null>(null);
  const h = useOrch((s) => (hid ? s.handoffs[hid] : undefined));
  const target = useSession(h?.targetSessionId ?? '');
  const chosen = agent || options.find((a) => a.id !== (source?.agent || source?.preset))?.id || options[0]?.id || '';
  const profs = profiles.filter((p) => p.machineId === machineId && p.agent === chosen);
  const provs = providers.filter((p) => compatible(options.find((a) => a.id === chosen), p));
  const account = profs.some((p) => p.id === profileId) || provs.some((p) => p.id === profileId) ? profileId : '';

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!chosen) return;
    setBusy(true);
    setError('');
    try {
      const r = await orchApi.handoff(sessionId, {
        agent: chosen,
        machineId: machineId !== source?.machineId ? machineId : undefined,
        ...splitAccount(account),
        workspaceId,
        note: note.trim() || undefined,
        threadId: projectId,
      });
      orchStore.upsertHandoff(r);
      setHid(r.id);
    } catch (err) {
      if (is404(err)) setError('This hub cannot hand off tasks yet. Update NextBoard on the hub.');
      else if (err instanceof ApiError && err.code === 'handoff_running') setError('A handoff from this console is already running. Wait for it to finish.');
      else if (err instanceof ApiError && err.code === 'not_agent') setError('The source console is not running an agent, so there is nothing to hand off.');
      else setError(orchError(err));
    } finally {
      setBusy(false);
    }
  };

  const who = source?.handle ? `@${source.handle}` : (source?.name ?? 'this console');
  const stepIndex = h ? STEPS.findIndex((s) => s.id === h.state) : -1;
  const finished = h && (h.state === 'done' || h.state === 'failed' || h.state === 'manual');

  return (
    <Modal
      title={`Hand off ${who}’s task`}
      icon="handoff"
      size="md"
      onClose={onClose}
      dismissable={!busy}
      help={!h ? <span>{who} keeps running. No confirmation is needed.</span> : undefined}
      footer={
        h ? (
          <>
            {h.targetSessionId && (
              <Button variant="secondary" icon="target" onClick={() => (onClose(), jumpToSession(h.targetSessionId!))}>
                Show @{target?.handle ?? 'new console'}
              </Button>
            )}
            <Button variant="primary" onClick={onClose}>
              {finished ? 'Done' : 'Close (keeps running)'}
            </Button>
          </>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" form="handoff-form" variant="primary" icon="handoff" loading={busy} disabled={!chosen}>
              Hand off
            </Button>
          </>
        )
      }
    >
      {!h ? (
        <form id="handoff-form" className="form" onSubmit={submit}>
          <SelectField label="Machine" value={machineId} onChange={(e) => setMachineId(e.target.value)} hint="Another machine works too: the brief travels with the task.">
            {machines.map((m) => (
              <option key={m.id} value={m.id} disabled={!machineReady(m)}>
                {m.name}
                {machineReady(m) ? '' : ' (offline)'}
              </option>
            ))}
          </SelectField>
          <SelectField label="Continue with" value={chosen} onChange={(e) => setAgent(e.target.value)} error={!options.length && ma.ready ? 'No agent is installed on that machine.' : undefined}>
            {options.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </SelectField>
          {profs.length + provs.length > 0 && (
            <SelectField label="Login profile" optional value={account} onChange={(e) => setProfileId(e.target.value)}>
              <AccountOptions profiles={profs} defaultProfile={defaultProfileOf(settings, machineId, chosen)} providers={provs} />
            </SelectField>
          )}
          <Field label="Note for the next agent" optional hint="Added to the brief, for example what to focus on.">
            {({ labelId: _l, ...a11y }) => <textarea className="control" rows={3} maxLength={2000} value={note} onChange={(e) => setNote(e.target.value)} {...a11y} />}
          </Field>
          {error && (
            <div className="alert" role="alert">
              <Icon name="alertCircle" />
              <span>{error}</span>
            </div>
          )}
        </form>
      ) : (
        <div className="stack">
          <ol className="handoff-steps" aria-label="Handoff progress">
            {STEPS.map((s, i) => {
              const state = h.state === 'done' || i < stepIndex ? 'done' : i === stepIndex ? 'active' : h.state === 'failed' || h.state === 'manual' ? 'skipped' : 'todo';
              return (
                <li key={s.id} className={`hstep is-${state}`}>
                  <span className="hstep-icon" aria-hidden="true">
                    {state === 'done' ? <Icon name="check" /> : state === 'active' ? <span className="spinner" /> : <span className="hstep-dot" />}
                  </span>
                  <span>{s.label}</span>
                  <span className="sr-only">{state === 'done' ? 'done' : state === 'active' ? 'in progress' : ''}</span>
                </li>
              );
            })}
          </ol>
          {h.state === 'done' && (
            <p className="handoff-msg ok">
              <Icon name="check" /> @{target?.handle ?? 'the new console'} has the brief and continues from here.
            </p>
          )}
          {h.state === 'failed' && (
            <div className="alert" role="alert">
              <Icon name="alertCircle" />
              <span>The handoff failed: {h.error || 'unknown error'}.</span>
            </div>
          )}
          {h.state === 'manual' && (
            <>
              <p className="handoff-msg">The new console did not get ready in time. Paste the brief into it yourself:</p>
              <textarea className="control mono handoff-brief" readOnly value={h.brief ?? ''} aria-label="Brief" />
              <Button
                size="sm"
                icon="copy"
                onClick={() =>
                  void navigator.clipboard
                    ?.writeText(h.brief ?? '')
                    .then(() => toast.success('Brief copied.'))
                    .catch(() => toast.error('The browser blocked the clipboard. Select the text and copy it.'))
                }
              >
                Copy brief
              </Button>
            </>
          )}
        </div>
      )}
    </Modal>
  );
}
