// Init memory: a hidden read-only run reads the project and proposes its memory.
// Nothing is saved until the owner has looked at it.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { SegmentedControl, SelectField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { orchApi } from '../orch/api';
import { useNow } from '../orch/hooks';
import { orchStore, useOrch } from '../orch/store';
import { memoryApi } from './api';
import type { InitResult } from './types';

type Effort = 'quick' | 'balanced' | 'thorough';

/** A rule usually names a command: show it as one. */
function withCode(text: string): ReactNode {
  return text.split(/(`[^`\n]+`)/).map((part, i) => (part.startsWith('`') && part.endsWith('`') && part.length > 2 ? <code key={i}>{part.slice(1, -1)}</code> : part));
}

const jobKey = (pid: string) => `nb.memoryInit.${pid}`;

function storedJob(pid: string): string | null {
  try {
    return sessionStorage.getItem(jobKey(pid));
  } catch {
    return null;
  }
}

function storeJob(pid: string, id: string | null) {
  try {
    if (id) sessionStorage.setItem(jobKey(pid), id);
    else sessionStorage.removeItem(jobKey(pid));
  } catch {
    /* only a convenience: the run goes on without it */
  }
}

/** The run of this project still going or waiting to be looked at, across page changes. */
export function useInitJob(projectId: string) {
  const [jobId, setJobId] = useState<string | null>(() => (projectId ? storedJob(projectId) : null));
  const job = useOrch((s) => (jobId ? s.jobs[jobId] : undefined));

  useEffect(() => setJobId(projectId ? storedJob(projectId) : null), [projectId]);

  const active = !job || job.state === 'queued' || job.state === 'running';
  useEffect(() => {
    if (!jobId || !active) return;
    const poll = () =>
      orchApi
        .job(jobId)
        .then((x) => orchStore.upsertJob(x))
        .catch(() => {
          storeJob(projectId, null); // the hub restarted: that run is gone
          setJobId(null);
        });
    void poll();
    const t = window.setInterval(poll, 2000);
    return () => window.clearInterval(t);
  }, [jobId, active, projectId]);

  const set = (id: string | null) => {
    if (projectId) storeJob(projectId, id);
    setJobId(id);
  };
  return { jobId, job, setJobId: set };
}

export function InitDialog({
  projectId,
  projectName,
  init,
  onClose,
  onApplied,
}: {
  projectId: string;
  projectName: string;
  init: ReturnType<typeof useInitJob>;
  onClose: () => void;
  onApplied: () => void;
}) {
  const { job, jobId, setJobId } = init;
  const [effort, setEffort] = useState<Effort>('balanced');
  const [agent, setAgent] = useState('claude');
  const [starting, setStarting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const running = !!jobId && (!job || job.state === 'queued' || job.state === 'running');
  const now = useNow(1000, running);

  const proposal = useMemo<InitResult | null>(() => {
    if (job?.state !== 'done' || !job.result) return null;
    const r = job.result as Partial<InitResult>;
    return { facts: r.facts ?? [], subs: r.subs ?? [], critical: r.critical ?? [] };
  }, [job]);

  const [facts, setFacts] = useState<Set<number>>(new Set());
  const [subs, setSubs] = useState<Set<number>>(new Set());
  const [rules, setRules] = useState<Set<number>>(new Set());
  useEffect(() => {
    if (!proposal) return;
    setFacts(new Set(proposal.facts.map((_, i) => i)));
    setSubs(new Set(proposal.subs.map((_, i) => i)));
    setRules(new Set(proposal.critical.map((_, i) => i)));
  }, [proposal]);

  const start = async () => {
    setStarting(true);
    setError('');
    try {
      const j = await memoryApi.init(projectId, { effort, agent });
      orchStore.upsertJob(j);
      setJobId(j.id);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setStarting(false);
    }
  };

  const total = facts.size + subs.size + rules.size;
  const save = async () => {
    if (!proposal) return;
    setSaving(true);
    setError('');
    try {
      await memoryApi.applyInit(projectId, {
        facts: proposal.facts.filter((_, i) => facts.has(i)),
        subs: proposal.subs.filter((_, i) => subs.has(i)),
        critical: proposal.critical.filter((_, i) => rules.has(i)),
      });
      setJobId(null);
      toast.success(`Saved ${total} item${total === 1 ? '' : 's'} to the memory of ${projectName}.`);
      onApplied();
    } catch (err) {
      setError(errorMessage(err));
      setSaving(false);
    }
  };

  const toggler = (set: (f: (s: Set<number>) => Set<number>) => void, i: number) => () =>
    set((cur) => {
      const n = new Set(cur);
      if (n.has(i)) n.delete(i);
      else n.add(i);
      return n;
    });

  const nothing = proposal && !proposal.facts.length && !proposal.subs.length && !proposal.critical.length;
  const failed = job && (job.state === 'failed' || job.state === 'cancelled');

  return (
    <Modal
      title={`Init memory: ${projectName}`}
      icon="brain"
      size="lg"
      onClose={onClose}
      help={proposal ? 'Only adds. What you already wrote stays as it is.' : running ? 'You can close this and come back: it keeps running.' : undefined}
      footer={
        proposal ? (
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setJobId(null);
                onClose();
              }}
              disabled={saving}
            >
              Discard
            </Button>
            <Button variant="primary" icon="check" onClick={save} loading={saving} disabled={!total}>
              Save {total} item{total === 1 ? '' : 's'}
            </Button>
          </>
        ) : running ? (
          <>
            <Button
              variant="secondary"
              onClick={() => {
                if (jobId) void orchApi.cancelJob(jobId).then((j) => orchStore.upsertJob(j)).catch(() => undefined);
                setJobId(null);
              }}
            >
              Stop
            </Button>
            <Button variant="primary" onClick={onClose}>
              Keep running
            </Button>
          </>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" icon="search" onClick={start} loading={starting}>
              {failed ? 'Try again' : 'Read the project'}
            </Button>
          </>
        )
      }
    >
      {error && (
        <div className="alert mb-4" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      {failed && !running && (
        <div className="alert mb-4" role="alert">
          <Icon name="alertCircle" />
          <span>The run stopped: {job?.error || job?.code || job?.state}</span>
        </div>
      )}

      {!running && !proposal && (
        <div className="mem-form">
          <p className="text-2">
            A hidden run of your CLI reads the project, read-only, and proposes three things: facts for the main memory, sub-memories such as a design system, and critical rules from what could harm this
            project in particular. You choose what to keep. It uses your CLI's plan.
          </p>
          <div className="mem-init-row">
            <SegmentedControl<Effort>
              value={effort}
              onChange={setEffort}
              label="How deep"
              options={[
                { value: 'quick', label: 'Quick' },
                { value: 'balanced', label: 'Balanced' },
                { value: 'thorough', label: 'Thorough' },
              ]}
            />
            <SelectField label="Run with" value={agent} onChange={(e) => setAgent(e.target.value)} className="control-sm">
              <option value="claude">Claude Code</option>
              <option value="codex">Codex</option>
            </SelectField>
          </div>
        </div>
      )}

      {running && (
        <div className="mem-job" role="status">
          <span className="spinner" aria-hidden="true" />
          <span>{job?.progress?.text || 'Reading the project…'}</span>
          <span className="text-3 tabular">{job?.startedAt ? `${Math.round((now - Date.parse(job.startedAt)) / 1000)}s` : ''}</span>
        </div>
      )}

      {nothing && <p className="text-2">The run found nothing new to add.</p>}

      {proposal && !nothing && (
        <div className="mem-review">
          {proposal.critical.length > 0 && (
            <section className="mem-review-group is-critical" aria-label="Critical rules">
              <h3>
                <Icon name="shieldAlert" /> Critical rules
              </h3>
              <p className="fs-xs text-3">What this project in particular must be protected from. They go first into every agent.</p>
              {proposal.critical.map((r, i) => (
                <label key={i} className="mem-check">
                  <input type="checkbox" checked={rules.has(i)} onChange={toggler(setRules, i)} />
                  <span>{withCode(r)}</span>
                </label>
              ))}
            </section>
          )}
          {proposal.subs.length > 0 && (
            <section className="mem-review-group" aria-label="Sub-memories">
              <h3>
                <Icon name="layers" /> Sub-memories
              </h3>
              {proposal.subs.map((s, i) => (
                <div key={s.slug} className="mem-review-sub">
                  <label className="mem-check">
                    <input type="checkbox" checked={subs.has(i)} onChange={toggler(setSubs, i)} />
                    <span>
                      <strong>{s.title}</strong> <span className="text-3">{s.description}</span>
                    </span>
                  </label>
                  <details>
                    <summary className="fs-xs text-3">Read it</summary>
                    <pre className="mono fs-xs">{s.content}</pre>
                  </details>
                </div>
              ))}
            </section>
          )}
          {proposal.facts.length > 0 && (
            <section className="mem-review-group" aria-label="Facts">
              <h3>
                <Icon name="brain" /> Main memory
              </h3>
              {proposal.facts.map((f, i) => (
                <label key={i} className="mem-check">
                  <input type="checkbox" checked={facts.has(i)} onChange={toggler(setFacts, i)} />
                  <span>
                    <span className="mem-kind">{f.kind}</span> {withCode(f.text)}
                  </span>
                </label>
              ))}
            </section>
          )}
        </div>
      )}
    </Modal>
  );
}
