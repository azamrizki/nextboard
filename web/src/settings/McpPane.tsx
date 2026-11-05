// Settings › MCP servers (docs/API-agents.md §23): tools set once in NextBoard that
// reach every agent at launch, whichever CLI it runs in, so switching agents keeps
// them. Values are write-only: the hub never shows them again.

import { useEffect, useState, type FormEvent } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Checkbox, SegmentedControl, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { SwitchRow } from '../components/Switch';
import { toast } from '../components/Toast';
import type { McpProbe, McpServer, McpVar } from '../lib/types';
import { useProjects } from '../store';

const NEW: McpServer = { id: '', transport: 'http', url: '', headers: [], command: '', args: [], env: [], projects: [], onServers: true, enabled: true };

function probeText(p: McpProbe): string {
  if (p.ok) return p.tools.length ? `Works: ${p.tools.length} tool${p.tools.length === 1 ? '' : 's'} (${p.tools.slice(0, 6).join(', ')}${p.tools.length > 6 ? ', …' : ''})` : 'Works, but offers no tools';
  if (p.needsLogin) return 'It wants a login. If it takes a token, add it as a header; otherwise sign in once inside each CLI with its /mcp command.';
  return `Does not answer: ${p.error ?? 'unknown error'}`;
}

function VarRows({ label, nameHint, vars, onChange }: { label: string; nameHint: string; vars: McpVar[]; onChange: (v: McpVar[]) => void }) {
  const set = (i: number, patch: Partial<McpVar>) => onChange(vars.map((v, j) => (j === i ? { ...v, ...patch } : v)));
  return (
    <div className="field">
      <span className="field-label">{label}</span>
      {vars.map((v, i) => (
        <div key={i} className="mcp-var">
          <input className="control mono" value={v.name} placeholder={nameHint} aria-label={`${label} name`} spellCheck={false} onChange={(e) => set(i, { name: e.target.value })} />
          <input
            className="control mono"
            type="password"
            autoComplete="new-password"
            value={v.value ?? ''}
            placeholder={v.set ? 'Saved; type to replace' : 'Value'}
            aria-label={`${v.name || label} value`}
            onChange={(e) => set(i, { value: e.target.value })}
          />
          <Button size="sm" variant="ghost" iconOnly icon="close" aria-label={`Remove ${v.name || label}`} onClick={() => onChange(vars.filter((_, j) => j !== i))} />
        </div>
      ))}
      <div>
        <Button size="sm" icon="plus" onClick={() => onChange([...vars, { name: '', value: '' }])}>
          Add
        </Button>
      </div>
    </div>
  );
}

function ServerDialog({ start, onClose, onSaved }: { start: McpServer | null; onClose: () => void; onSaved: (s: McpServer) => void }) {
  const projects = useProjects();
  const editing = !!start;
  const [sv, setSv] = useState<McpServer>(start ?? NEW);
  const [argsText, setArgsText] = useState((start?.args ?? []).join('\n'));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const patch = (p: Partial<McpServer>) => setSv((cur) => ({ ...cur, ...p }));

  const save = async (e?: FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    setError('');
    const body: McpServer = { ...sv, id: sv.id.trim(), args: argsText.split('\n').map((a) => a.trim()).filter(Boolean) };
    try {
      onSaved(await (editing ? api.updateMcpServer(sv.id, body) : api.createMcpServer(body)));
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  const http = sv.transport === 'http';
  return (
    <Modal
      title={editing ? `Edit ${sv.id}` : 'Add MCP server'}
      icon="cpu"
      size="md"
      onClose={onClose}
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" icon="check" loading={busy} onClick={() => void save()}>
            Save and try it
          </Button>
        </>
      }
    >
      <form className="stack" onSubmit={save}>
        <TextField label="Name" value={sv.id} mono disabled={editing} autoFocus={!editing} maxLength={32} spellCheck={false} hint="Agents see its tools under this name." onChange={(e) => patch({ id: e.target.value })} />
        <SegmentedControl<McpServer['transport']>
          value={sv.transport}
          label="Kind"
          block
          onChange={(transport) => patch({ transport, onServers: transport === 'http' })}
          options={[
            { value: 'http', label: 'URL' },
            { value: 'stdio', label: 'Command' },
          ]}
        />
        {http ? (
          <>
            <TextField label="URL" value={sv.url ?? ''} mono spellCheck={false} placeholder="https://mcp.example.com/mcp" onChange={(e) => patch({ url: e.target.value })} />
            <VarRows label="Headers" nameHint="Authorization" vars={sv.headers} onChange={(headers) => patch({ headers })} />
          </>
        ) : (
          <>
            <TextField label="Command" value={sv.command ?? ''} mono spellCheck={false} placeholder="npx" onChange={(e) => patch({ command: e.target.value })} />
            <div className="field">
              <label className="field-label" htmlFor="mcp-args">
                Arguments, one per line
              </label>
              <textarea id="mcp-args" className="control mono" rows={3} value={argsText} spellCheck={false} placeholder={'-y\n@example/mcp-server'} onChange={(e) => setArgsText(e.target.value)} />
              <span className="setting-hint">Keep keys below, not here: arguments can be read in the process list.</span>
            </div>
            <VarRows label="Environment" nameHint="API_KEY" vars={sv.env} onChange={(env) => patch({ env })} />
          </>
        )}
        <div className="field">
          <span className="field-label">Which projects</span>
          <SegmentedControl<'all' | 'some'>
            value={sv.projects.length ? 'some' : 'all'}
            label="Which projects"
            size="sm"
            onChange={(v) => patch({ projects: v === 'all' ? [] : projects.slice(0, 1).map((p) => p.id) })}
            options={[
              { value: 'all', label: 'All' },
              { value: 'some', label: 'Only these' },
            ]}
          />
          {sv.projects.length > 0 && (
            <div className="mcp-projects">
              {projects.map((p) => (
                <Checkbox
                  key={p.id}
                  checked={sv.projects.includes(p.id)}
                  label={p.name}
                  onChange={(on) => {
                    const next = on ? [...sv.projects, p.id] : sv.projects.filter((x) => x !== p.id);
                    if (next.length) patch({ projects: next });
                  }}
                />
              ))}
            </div>
          )}
        </div>
        <SwitchRow
          checked={sv.onServers}
          onChange={(onServers) => patch({ onServers })}
          title="Also for agents working on a server"
          hint={http ? undefined : 'The command runs on This PC, not on the server. Leave off for anything that touches files.'}
        />
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
      </form>
    </Modal>
  );
}

export function McpPane() {
  const projects = useProjects();
  const [list, setList] = useState<McpServer[] | null>(null);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState<McpServer | 'new' | null>(null);
  const [deleting, setDeleting] = useState<McpServer | null>(null);
  const [probes, setProbes] = useState<Record<string, McpProbe | 'running'>>({});

  const load = () =>
    api
      .mcpServers()
      .then(setList)
      .catch((err) => setError(errorMessage(err)));
  useEffect(() => {
    void load();
  }, []);

  const test = async (id: string) => {
    setProbes((p) => ({ ...p, [id]: 'running' }));
    try {
      const r = await api.testMcpServer(id);
      setProbes((p) => ({ ...p, [id]: r }));
    } catch (err) {
      setProbes((p) => ({ ...p, [id]: { ok: false, tools: [], error: errorMessage(err), tookMs: 0 } }));
    }
  };

  const toggle = async (s: McpServer, enabled: boolean) => {
    try {
      const saved = await api.updateMcpServer(s.id, { ...s, enabled });
      setList((l) => l?.map((x) => (x.id === s.id ? saved : x)) ?? l);
    } catch (err) {
      toast.error(`Could not change ${s.id}: ${errorMessage(err)}`);
    }
  };

  const scope = (s: McpServer) => {
    if (!s.projects.length) return 'All projects';
    const names = s.projects.map((id) => projects.find((p) => p.id === id)?.name ?? id);
    return names.length > 2 ? `${names.length} projects` : names.join(', ');
  };

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="mcp-title">
        <div className="mcp-head">
          <div>
            <h3 id="mcp-title" className="section-title">
              Servers
            </h3>
            <p className="setting-hint">Set once here and every agent gets them at launch, whichever CLI it runs in: Claude Code, Codex and OpenCode. Switching agents keeps them. Nothing is written to a CLI’s own config. Agents already running get changes when they restart.</p>
          </div>
          <Button size="sm" variant="primary" icon="plus" onClick={() => setEditing('new')}>
            Add server
          </Button>
        </div>
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
        {list?.length === 0 && <p className="list-msg">No MCP servers yet.</p>}
        <ul className="plain conn-list">
          {list?.map((s) => {
            const p = probes[s.id];
            return (
              <li key={s.id} className="mcp-row">
                <div className="conn-row">
                  <Icon name={s.transport === 'http' ? 'globe' : 'terminal'} />
                  <div className="grow mcp-text">
                    <div className="fs-sm mono">{s.id}</div>
                    <div className="fs-xs text-3 truncate" title={s.url || s.command}>
                      {s.transport === 'http' ? s.url : [s.command, ...s.args].join(' ')}
                    </div>
                    <div className="fs-xs text-3">
                      {scope(s)}
                      {s.onServers ? ' · also on servers' : ' · This PC only'}
                    </div>
                  </div>
                  <Button size="sm" onClick={() => void test(s.id)} loading={p === 'running'}>
                    Try
                  </Button>
                  <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label={`Edit ${s.id}`} onClick={() => setEditing(s)} />
                  <Button size="sm" variant="ghost" iconOnly icon="trash" aria-label={`Delete ${s.id}`} onClick={() => setDeleting(s)} />
                </div>
                <SwitchRow checked={s.enabled} onChange={(on) => void toggle(s, on)} title="Give it to agents" />
                {p && p !== 'running' && (
                  <p className={`fs-xs mcp-probe ${p.ok ? 'is-ok' : 'is-bad'}`} role="status">
                    <Icon name={p.ok ? 'check' : 'alertCircle'} />
                    <span>{probeText(p)}</span>
                  </p>
                )}
              </li>
            );
          })}
        </ul>
      </section>

      {editing && (
        <ServerDialog
          start={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(s) => {
            setEditing(null);
            void load().then(() => test(s.id));
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.id}?`}
          confirmLabel="Delete server"
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await api.deleteMcpServer(deleting.id);
            setList((l) => l?.filter((x) => x.id !== deleting.id) ?? l);
          }}
        >
          <p>Its saved keys are removed from the hub. Agents already running keep it until they restart.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
