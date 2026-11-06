// An agent asks to run a command with a credential (agents.ts): a pill while a request waits, and
// the dialog that approves it. Approving opens the item here, in this tab, checks that its blob
// still shares it with that project, and sends only the values the command asked for.

import { useEffect, useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { store, useProject } from '../store';
import { agentApi, agentValues, onOpenCredentialRequest, type CredRequest, type CredUse } from './agents';
import { ResetDialog } from './Dialogs';
import { VaultGate } from './Gate';
import type { VaultItem } from './model';
import { touchVault, useVault } from './store';

/** The shortest value the hub can hide from the output (vault.MinCensored). */
const MIN_VALUE = 4;

const who = (r: CredRequest) => (r.handle ? `@${r.handle.replace(/^@/, '')}` : r.agent || 'An agent');

const ENDED: Record<string, string> = {
  running: 'You approved it. The command is running.',
  done: 'It ran.',
  denied: 'It was denied.',
  expired: 'Nobody approved it in time.',
  failed: 'It could not run.',
};

function minutes(ms: number) {
  const m = Math.round(ms / 60000);
  return m >= 1 ? `${m} min` : `${Math.round(ms / 1000)} s`;
}

interface Resolved {
  use: CredUse;
  item?: VaultItem;
  value?: string;
  problem?: string;
}

/** Each value the command asked for, as the vault says it is now, not as the hub's catalog says. */
function resolve(r: CredRequest, items: VaultItem[], projectName: string): Resolved[] {
  return r.uses.map((use) => {
    const item = items.find((x) => x.id === use.item);
    if (!item || item.deletedAt) return { use, problem: 'Not in the vault any more.' };
    if (!item.agents?.projects.includes(r.projectId)) return { use, item, problem: `Not shared with agents of ${projectName}.` };
    const v = agentValues(item).find((x) => x.name.toLowerCase() === use.value.toLowerCase());
    if (!v) return { use, item, problem: `Has no ${use.value} any more.` };
    if (v.value.length < MIN_VALUE) return { use, item, problem: `Too short to hide from the output (under ${MIN_VALUE} characters).` };
    return { use, item, value: v.value };
  });
}

function ApproveDialog({ request: r, onClose }: { request: CredRequest; onClose: () => void }) {
  const v = useVault();
  const project = useProject(r.projectId);
  const projectName = project?.name ?? 'this project';
  const [busy, setBusy] = useState<'' | 'allow' | 'deny'>('');
  const [error, setError] = useState('');
  const [note, setNote] = useState('');
  const [reset, setReset] = useState(false);
  const unlocked = v.status === 'unlocked';
  const resolved = useMemo(() => (unlocked ? resolve(r, v.items, projectName) : []), [unlocked, r, v.items, projectName]);
  const blocked = resolved.some((x) => x.problem);
  const waiting = r.state === 'waiting';

  const allow = async () => {
    setBusy('allow');
    setError('');
    try {
      await agentApi.approve(
        r.id,
        resolved.map((x) => x.value ?? ''),
      );
      toast.success('Approved once. The agent gets the output with the values hidden.');
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy('');
    }
  };
  const deny = async () => {
    setBusy('deny');
    setError('');
    try {
      await agentApi.deny(r.id, note.trim());
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy('');
    }
  };

  return (
    <Modal
      title={`${who(r)} asks to use a credential`}
      icon="key"
      size="md"
      onClose={onClose}
      footer={
        waiting ? (
          <>
            <Button onClick={() => void deny()} loading={busy === 'deny'} disabled={!!busy}>
              Deny
            </Button>
            <Button variant="primary" onClick={() => void allow()} loading={busy === 'allow'} disabled={!!busy || !unlocked || blocked}>
              Allow once
            </Button>
          </>
        ) : (
          <Button onClick={onClose}>Close</Button>
        )
      }
    >
      <div className="vault-approve" onPointerDownCapture={touchVault} onKeyDownCapture={touchVault}>
        {!waiting && (
          <div className="alert" role="status">
            {ENDED[r.state] ?? r.state}
            {r.state === 'done' && r.exitCode !== undefined ? ` Exit code ${r.exitCode}.` : ''}
          </div>
        )}
        <dl className="vault-approve-facts">
          <dt>Agent</dt>
          <dd>
            {who(r)}
            {r.agent ? ` · ${r.agent}` : ''}
          </dd>
          <dt>Project</dt>
          <dd>{project?.name ?? r.projectId}</dd>
          <dt>Runs on</dt>
          <dd>
            {r.machine}, in <code>{r.cwd}</code>
          </dd>
          <dt>Why</dt>
          <dd>{r.why}</dd>
          <dt>Time limit</dt>
          <dd>{minutes(r.timeoutMs)}</dd>
        </dl>
        <div className="vault-approve-block">
          <span className="field-label">Command</span>
          <pre className="vault-approve-command">{r.command}</pre>
        </div>
        <div className="vault-approve-block">
          <span className="field-label">It gets</span>
          <ul className="plain vault-approve-uses">
            {(unlocked ? resolved : r.uses.map((use) => ({ use }) as Resolved)).map((x, i) => (
              <li key={i} className={x.problem ? 'is-problem' : ''}>
                <span>
                  <strong>{x.item?.name ?? x.use.name}</strong> · {x.use.value}{' '}
                  {x.use.env ? (
                    <>
                      as <code>{x.use.env}</code>
                    </>
                  ) : (
                    'on its standard input'
                  )}
                </span>
                {x.problem && <span className="vault-approve-problem">{x.problem}</span>}
              </li>
            ))}
          </ul>
        </div>
        {waiting && !unlocked && <VaultGate v={v} onReset={() => setReset(true)} />}
        {waiting && unlocked && (
          <>
            <div className="alert warning" role="note">
              <Icon name="alert" />
              <span>
                Allow only a command you understand. The agent gets its output with these values hidden, but a script it runs, like one in package.json, runs as it is now, and agents can change scripts.
              </span>
            </div>
            <TextField label="Tell the agent why, if you deny it" value={note} maxLength={300} onChange={(e) => setNote(e.target.value)} />
          </>
        )}
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
      </div>
      {reset && <ResetDialog count={null} onClose={() => setReset(false)} />}
    </Modal>
  );
}

/** Mounted once while logged in: the pill and, when asked for, the dialog. */
export function CredentialRequests() {
  const [items, setItems] = useState<CredRequest[]>([]);
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = () =>
      agentApi
        .requests()
        .then((res) => alive && setItems(res.items ?? []))
        .catch(() => undefined);
    void load();
    const off = store.onEvent((ev) => {
      if (ev.type === 'credentialRequest' || ev.type === 'snapshot') void load();
    });
    const offOpen = onOpenCredentialRequest((id) => setOpen(id ?? ''));
    return () => {
      alive = false;
      off();
      offOpen();
    };
  }, []);

  const waiting = items.filter((r) => r.state === 'waiting');
  // '' is "the oldest one waiting"; a request that ended stays open with what became of it.
  const current = open === null ? undefined : open === '' ? waiting[0] : items.find((r) => r.id === open);

  return (
    <>
      {waiting.length > 0 && !current && (
        <button type="button" className="vault-request-pill" onClick={() => setOpen(waiting[0].id)}>
          <Icon name="key" />
          <span>
            {who(waiting[0])} asks to use a credential
            {waiting.length > 1 ? ` (+${waiting.length - 1})` : ''}
          </span>
        </button>
      )}
      {current && <ApproveDialog key={current.id} request={current} onClose={() => setOpen(null)} />}
    </>
  );
}
