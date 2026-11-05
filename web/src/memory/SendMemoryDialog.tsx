// Send memory to agents, right now: which memories and which agents are both a choice, and
// the dialog shows what it will cost before anything is typed. An agent gets only what it
// does not have yet, so a send with nothing new uses no turn.

import { useEffect, useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Checkbox, SegmentedControl, SelectField } from '../components/form';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { orchApi } from '../orch/api';
import type { MemoryBlock, MemoryPlan, MemoryPlanTarget } from '../orch/types';
import { agentsLabel, blockKey, sentMessage, summarizeSend, tokensLabel } from './blocks';
import { MemoryPicker } from './MemoryPicker';

type Who = 'all' | 'some';

function targetNote(t: MemoryPlanTarget) {
  if (!t.canSend) return 'Cannot take it now';
  if (t.chars === 0) return 'Has it already';
  const size = `${tokensLabel(t.chars)} tokens`;
  return t.state === 'ready' ? size : `${size}, when free`;
}

export function SendMemoryDialog({ projectId, projects, initial, onClose }: { projectId: string; projects?: { id: string; name: string }[]; initial: MemoryBlock[]; onClose: () => void }) {
  const [project, setProject] = useState(projectId);
  const [blocks, setBlocks] = useState<MemoryBlock[]>(initial);
  const [who, setWho] = useState<Who>('all');
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [force, setForce] = useState(false);
  const [plan, setPlan] = useState<MemoryPlan | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const picked = useMemo(() => new Set(blocks.map(blockKey)), [blocks]);
  const key = [...picked].sort().join(',');

  useEffect(() => {
    let alive = true;
    const t = window.setTimeout(() => {
      orchApi
        .memoryPlan(project, { blocks, force })
        .then((p) => {
          if (!alive) return;
          setPlan(p);
          setError('');
        })
        .catch((err) => alive && setError(errorMessage(err)));
    }, 150);
    return () => {
      alive = false;
      window.clearTimeout(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [project, key, force]);

  const targets = plan?.targets ?? [];
  const selected = who === 'all' ? targets : targets.filter((t) => chosen.has(t.sessionId));
  const sum = summarizeSend(selected);
  const size = selected.length ? sum.chars : 0;

  const send = async () => {
    setBusy(true);
    try {
      const r = await orchApi.pushMemory(project, { blocks, targets: sum.send.map((t) => t.sessionId), force });
      toast.success(sentMessage({ ...r, upToDate: sum.upToDate }));
      onClose();
    } catch (err) {
      toast.error(`Could not send the memory: ${errorMessage(err)}`);
      setBusy(false);
    }
  };

  return (
    <Modal
      title="Send memory to agents"
      icon="send"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" icon="send" onClick={() => void send()} loading={busy} disabled={!sum.send.length}>
            Send to {agentsLabel(sum.send.length)}
          </Button>
        </>
      }
    >
      <div className="memory-send">
        {projects && projects.length > 0 && (
          <SelectField label="Agents of" value={project} onChange={(e) => setProject(e.target.value)}>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </SelectField>
        )}
        <section aria-label="Which memory">
          <h3 className="section-title">Which memory</h3>
          <MemoryPicker
            projectId={project}
            picked={picked}
            onToggle={(b, on) => setBlocks((cur) => (on ? [...cur, b] : cur.filter((x) => blockKey(x) !== blockKey(b))))}
          />
        </section>
        <section aria-label="Which agents">
          <h3 className="section-title">Which agents</h3>
          <SegmentedControl<Who>
            value={who}
            onChange={setWho}
            size="sm"
            block
            label="Which agents"
            options={[
              { value: 'all', label: 'All in this project' },
              { value: 'some', label: 'Choose' },
            ]}
          />
          {error && (
            <p className="field-error" role="alert">
              {error}
            </p>
          )}
          {plan && targets.length === 0 && <p className="tile-hint">No agent is running in this project. They get the project memory at launch.</p>}
          <div className="memory-targets">
            {targets.map((t) => (
              <label key={t.sessionId} className={`memory-pick-row ${t.canSend ? '' : 'is-off'}`}>
                {who === 'some' && (
                  <input
                    type="checkbox"
                    checked={chosen.has(t.sessionId)}
                    onChange={() =>
                      setChosen((cur) => {
                        const n = new Set(cur);
                        if (n.has(t.sessionId)) n.delete(t.sessionId);
                        else n.add(t.sessionId);
                        return n;
                      })
                    }
                  />
                )}
                <span className="memory-pick-text">
                  <span className="truncate">@{t.handle}</span>
                </span>
                <span className="memory-size tabular" title={t.lookup.length ? `${t.lookup.length} left out, it reads those by name when needed` : undefined}>
                  {targetNote(t)}
                </span>
              </label>
            ))}
          </div>
          <Checkbox checked={force} onChange={setForce} label="Also send what they already have, for an agent that lost it after /clear" />
          <p className="tile-hint tabular" role="status">
            {sum.send.length > 0 ? `${tokensLabel(size)} tokens in total` : selected.length ? 'Nothing new to send: no turn is used.' : 'Choose at least one agent.'}
          </p>
        </section>
      </div>
    </Modal>
  );
}
