// Add / edit a custom agent (docs/API-agents.md §1, C9). Field anatomy per DESIGN.md
// §3.2. Arguments are one per line (no shell quoting needed). An agent can borrow the
// capabilities of Claude Code or Codex ("base") — e.g. a wrapper script around them.

import { useState, type CSSProperties, type FormEvent } from 'react';
import { api, ApiError, errorMessage } from '../api';
import { refreshAgents } from '../agents/catalog';
import { Button } from '../components/Button';
import { Field, FormSection, RadioCards, SegmentedControl, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import type { Agent, AgentInput, AgentKind } from '../lib/types';

/** Agent colours (stored as #rrggbb by the hub, so not tokens). */
const COLORS = ['#a371f7', '#6e95ff', '#4fd6e8', '#2fdc8a', '#f2b27a', '#ff8a9c', '#d97757', '#8b949e'];

const lines = (s: string) =>
  s
    .split('\n')
    .map((x) => x.trim())
    .filter(Boolean);

const ID_RE = /^[a-z][a-z0-9-]{1,23}$/;
const ENV_RE = /^[A-Za-z_][A-Za-z0-9_]*=/;

export function slugOf(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .replace(/^[^a-z]+/, '')
    .slice(0, 24);
}

interface Errors {
  name?: string;
  id?: string;
  command?: string;
  env?: string;
  form?: string;
}

export function CustomAgentDialog({ agent, onClose }: { agent: Agent | null; onClose: () => void }) {
  const editing = !!agent;
  const [name, setName] = useState(agent?.name ?? '');
  const [id, setId] = useState(agent?.id ?? '');
  const [command, setCommand] = useState(agent?.command ?? '');
  const [args, setArgs] = useState((agent?.args ?? []).join('\n'));
  const [env, setEnv] = useState((agent?.env ?? []).join('\n'));
  const [kind, setKind] = useState<AgentKind>(agent?.kind ?? 'agent');
  const [base, setBase] = useState<'' | 'claude' | 'codex'>((agent?.base as '' | 'claude' | 'codex') ?? '');
  const [resumeKind, setResumeKind] = useState<'none' | 'continue'>(agent?.resume?.kind === 'continue' ? 'continue' : 'none');
  const [continueArgs, setContinueArgs] = useState((agent?.resume?.continueArgs ?? []).join('\n'));
  const [yoloArgs, setYoloArgs] = useState(agent && !agent.base ? (agent.yoloArgs ?? []).join('\n') : '');
  const [color, setColor] = useState(agent?.color && COLORS.includes(agent.color.toLowerCase()) ? agent.color.toLowerCase() : agent?.color || COLORS[0]);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);

  const validate = (): Errors => {
    const e: Errors = {};
    if (!name.trim()) e.name = 'Give the agent a name.';
    const effectiveId = id.trim() || slugOf(name);
    if (!editing && effectiveId && !ID_RE.test(effectiveId)) e.id = 'Use 2–24 lowercase letters, digits or dashes, starting with a letter.';
    if (!command.trim()) e.command = 'Enter the program to run, for example my-agent or /opt/bin/my-agent.';
    else if (/\s/.test(command.trim()) && !command.trim().startsWith('/')) e.command = 'Put only the program here and each argument on its own line below.';
    const badEnv = lines(env).find((l) => !ENV_RE.test(l));
    if (badEnv) e.env = `“${badEnv}” is not KEY=value.`;
    return e;
  };

  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    const e = validate();
    setErrors(e);
    if (Object.keys(e).length) return;
    const body: AgentInput = {
      name: name.trim(),
      command: command.trim(),
      args: lines(args),
      env: lines(env),
      color,
      kind,
      base: kind === 'agent' ? base : '',
    };
    if (!editing && id.trim()) body.id = id.trim();
    if (kind === 'agent' && !base) {
      body.yoloArgs = lines(yoloArgs);
      body.resume =
        resumeKind === 'continue' && lines(continueArgs).length
          ? { kind: 'continue', continueArgs: lines(continueArgs), launchArgs: [], resumeArgs: [], pickerArgs: [] }
          : { kind: 'none', continueArgs: [], launchArgs: [], resumeArgs: [], pickerArgs: [] };
    }
    setBusy(true);
    try {
      if (agent) {
        const { id: _drop, ...patch } = body;
        await api.updateAgent(agent.id, patch);
        toast.success(`${body.name} saved.`);
      } else {
        const a = await api.createAgent(body);
        toast.success(`${a.name} added. Pick it from the Spawn tool or the dock.`);
      }
      refreshAgents();
      onClose();
    } catch (err) {
      if (err instanceof ApiError && err.code === 'agent_exists') setErrors({ id: 'An agent with this ID already exists. Choose another ID.' });
      else setErrors({ form: errorMessage(err) });
      setBusy(false);
    }
  };

  return (
    <Modal
      title={editing ? `Edit ${agent?.name}` : 'Add an agent'}
      icon="bot"
      size="md"
      onClose={onClose}
      dismissable={!busy}
      help="Running consoles keep their command until restarted."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="custom-agent-form" variant="primary" loading={busy}>
            {editing ? 'Save agent' : 'Add agent'}
          </Button>
        </>
      }
    >
      <form id="custom-agent-form" className="form" onSubmit={(e) => void submit(e)} noValidate>
        <fieldset disabled={busy}>
          <FormSection title="Agent">
            <div className="field-row">
              <TextField
                fieldClassName="span-7"
                label="Name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="My agent"
                maxLength={80}
                error={errors.name}
                autoFocus
              />
              <TextField
                fieldClassName="span-5"
                label="ID"
                optional
                mono
                value={editing ? agent?.id : id}
                disabled={editing}
                onChange={(e) => setId(e.target.value)}
                placeholder={slugOf(name) || 'my-agent'}
                maxLength={24}
                error={errors.id}
                hint={editing ? 'IDs can’t change.' : 'Used in commands; from the name if empty.'}
              />
            </div>
            <TextField
              label="Command"
              mono
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              placeholder="my-agent"
              spellCheck={false}
              error={errors.command}
              hint="A program on the login PATH, or an absolute path."
            />
            <Field label="Arguments" optional hint="One argument per line — no quoting needed.">
              {({ labelId: _l, ...a11y }) => (
                <textarea className="control mono textarea-sm" rows={3} value={args} onChange={(e) => setArgs(e.target.value)} placeholder={'--model\nfast'} spellCheck={false} {...a11y} />
              )}
            </Field>
            <Field label="Type" as="group">
              {({ labelId }) => (
                <SegmentedControl
                  labelledBy={labelId}
                  value={kind}
                  onChange={setKind}
                  options={[
                    { value: 'agent', label: 'Agent', icon: 'bot' },
                    { value: 'shell', label: 'Shell', icon: 'terminal' },
                  ]}
                />
              )}
            </Field>
          </FormSection>

          {kind === 'agent' && (
            <FormSection title="Capabilities">
              <Field label="Works like" as="group" hint="A wrapper around Claude Code or Codex can use their live status, resume, transcript and profiles.">
                {({ labelId }) => (
                  <RadioCards
                    labelledBy={labelId}
                    columns={1}
                    value={base}
                    onChange={setBase}
                    options={[
                      { value: '', title: 'Its own CLI', description: 'Status from the screen; resume only via a “continue” flag.' },
                      { value: 'claude', title: 'Claude Code', description: 'Hooks, exact resume, transcript, profiles.' },
                      { value: 'codex', title: 'Codex', description: 'Notify, exact resume, transcript, profiles.' },
                    ]}
                  />
                )}
              </Field>
              {!base && (
                <>
                  <Field label="Resume" as="group">
                    {({ labelId }) => (
                      <SegmentedControl
                        labelledBy={labelId}
                        value={resumeKind}
                        onChange={setResumeKind}
                        options={[
                          { value: 'none', label: 'Not supported' },
                          { value: 'continue', label: 'Continue latest' },
                        ]}
                      />
                    )}
                  </Field>
                  {resumeKind === 'continue' && (
                    <Field label="Continue arguments" hint="Added when resuming the latest conversation in the folder, one per line.">
                      {({ labelId: _l, ...a11y }) => (
                        <textarea className="control mono textarea-sm" rows={2} value={continueArgs} onChange={(e) => setContinueArgs(e.target.value)} placeholder="--continue" spellCheck={false} {...a11y} />
                      )}
                    </Field>
                  )}
                  <Field label="Yolo arguments" optional hint="Its skip-permissions flag, one per line. Leave empty if it has none.">
                    {({ labelId: _l, ...a11y }) => (
                      <textarea className="control mono textarea-sm" rows={2} value={yoloArgs} onChange={(e) => setYoloArgs(e.target.value)} placeholder="--yolo" spellCheck={false} {...a11y} />
                    )}
                  </Field>
                </>
              )}
            </FormSection>
          )}

          <FormSection title="More">
            <Field label="Environment" optional hint="KEY=value, one per line." error={errors.env}>
              {({ labelId: _l, ...a11y }) => (
                <textarea className="control mono textarea-sm" rows={2} value={env} onChange={(e) => setEnv(e.target.value)} placeholder="MY_AGENT_MODE=fast" spellCheck={false} {...a11y} />
              )}
            </Field>
            <Field label="Colour" as="group" hint="The accent line on its consoles.">
              {({ labelId }) => (
                <div className="swatch-row" role="radiogroup" aria-labelledby={labelId}>
                  {COLORS.map((c) => (
                    <button
                      key={c}
                      type="button"
                      role="radio"
                      aria-checked={color === c}
                      aria-label={`Colour ${c}`}
                      className="swatch-btn"
                      style={{ '--swatch': c } as CSSProperties}
                      onClick={() => setColor(c)}
                    />
                  ))}
                </div>
              )}
            </Field>
          </FormSection>
          {errors.form && (
            <div className="alert" role="alert">
              <Icon name="alertCircle" />
              <span>{errors.form}</span>
            </div>
          )}
        </fieldset>
      </form>
    </Modal>
  );
}
