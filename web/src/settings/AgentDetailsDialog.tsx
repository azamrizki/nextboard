// Agent details: what NextBoard runs, its capabilities, and detection + path override
// per machine (settings.agents.paths, §1.2).

import { useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { canContinue, canPickInCli, canResumeExact, hasLiveStatus, hasTranscript, hasYolo, SOURCE_LABEL, supportsProfiles, useMachinesAgents } from '../agents/catalog';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import type { Agent, Machine } from '../lib/types';
import { useMachines } from '../store';

const LIVE: Record<string, string> = {
  'claude-hooks': 'Hooks passed per launch (--settings)',
  'codex-notify': 'Turn notifications passed per launch (-c notify)',
  screen: 'Recognised from the screen',
  none: 'Output activity only',
};

export function AgentDetailsDialog({ agent, onClose }: { agent: Agent; onClose: () => void }) {
  const machines = useMachines();
  const ids = useMemo(() => machines.map((m) => m.id), [machines]);
  const per = useMachinesAgents(ids);
  const cmd = [agent.command, ...(agent.args ?? [])].join(' ');
  const resume = canResumeExact(agent) ? 'This console’s own conversation' : canContinue(agent) ? 'Latest conversation in the folder' : canPickInCli(agent) ? 'The CLI’s own picker' : 'Not supported';

  return (
    <Modal title={agent.name} icon="bot" size="md" onClose={onClose} footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
      <div className="stack">
        <dl className="kv">
          <dt>Runs</dt>
          <dd className="mono">{cmd}</dd>
          <dt>Source</dt>
          <dd>{SOURCE_LABEL[agent.source] ?? agent.source}</dd>
          <dt>Live status</dt>
          <dd>{hasLiveStatus(agent) ? LIVE[agent.liveStatus] : (LIVE[agent.liveStatus] ?? agent.liveStatus)}</dd>
          <dt>Resume</dt>
          <dd>{resume}</dd>
          <dt>Output viewer</dt>
          <dd>{hasTranscript(agent) ? 'Read from its transcript' : 'Read from the screen'}</dd>
          <dt>Yolo</dt>
          <dd className={hasYolo(agent) ? 'mono' : undefined}>{hasYolo(agent) ? [...(agent.yoloArgs ?? []), ...(agent.yoloEnv ?? [])].join(' ') : 'No skip-permissions flag'}</dd>
          <dt>Login profiles</dt>
          <dd>{supportsProfiles(agent) ? <span className="mono">{agent.profileEnv}</span> : 'Not supported'}</dd>
        </dl>
        {agent.kind === 'agent' && agent.command !== '$SHELL' && (
          <section className="form-section" aria-labelledby="paths-title">
            <h3 className="section-title" id="paths-title">
              Detection and path per machine
            </h3>
            <p className="setting-hint">
              NextBoard looks for <span className="mono">{agent.detect || agent.command}</span> on each machine’s login PATH. Set a path when it lives
              somewhere else.
            </p>
            {machines.map((m) => (
              <PathRow key={m.id} machine={m} agent={agent} available={per[m.id]?.agents.find((a) => a.id === agent.id)?.available} />
            ))}
          </section>
        )}
      </div>
    </Modal>
  );
}

function PathRow({ machine, agent, available }: { machine: Machine; agent: Agent; available: boolean | undefined }) {
  const settings = useEffectiveSettings();
  const saved = settings.agents.paths?.[machine.id]?.[agent.id] ?? '';
  const [value, setValue] = useState(saved);
  const [busy, setBusy] = useState(false);
  const found = machine.probe?.commands?.[agent.detect || agent.command] ?? '';
  const id = `path-${machine.id}-${agent.id}`;
  const dirty = value.trim() !== saved;

  const store = async (v: string) => {
    setBusy(true);
    try {
      await patchSettings({ agents: { paths: { [machine.id]: { [agent.id]: v || null } } } });
      toast.success(v ? `Path for ${agent.name} on ${machine.name} saved.` : `${agent.name} on ${machine.name} uses PATH again.`);
    } catch (err) {
      toast.error(`Could not save the path: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  const status = available === undefined ? 'Unknown until the machine is connected' : available ? (found ? `Found: ${found}` : saved ? 'Using the path below' : 'Found') : 'Not found';
  return (
    <div className="field path-row">
      <label className="field-label row" htmlFor={id}>
        <span className={`dot dot-${available ? 'success' : available === false ? 'danger' : 'grey'}`} aria-hidden="true" />
        {machine.name}
        <span className="text-3 fs-xs truncate">· {status}</span>
      </label>
      <div className="input-group has-addon">
        <input
          id={id}
          className="control control-sm mono"
          placeholder={found || `/usr/local/bin/${agent.command}`}
          value={value}
          spellCheck={false}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && dirty && void store(value.trim())}
        />
        <Button size="sm" className="ig-addon" loading={busy} disabled={!dirty} onClick={() => void store(value.trim())}>
          Save path
        </Button>
      </div>
      {saved && (
        <span className="field-hint">
          Overrides PATH.{' '}
          <button type="button" className="link-btn" onClick={() => (setValue(''), void store(''))}>
            Use PATH again
          </button>
        </span>
      )}
    </div>
  );
}
