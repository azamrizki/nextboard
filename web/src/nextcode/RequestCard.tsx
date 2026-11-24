// What the agent waits on the owner for (docs/API-agents.md §30): a permission (Allow once,
// Always allow when the CLI offers rules, Deny with an optional note, Deny and stop), a question
// (AskUserQuestion's options, single or several, or a free answer), or a plan to approve
// (ExitPlanMode). The request comes from the CLI as data and the answer goes back the same way.

import { useContext, useId, useMemo, useState } from 'react';
import { Button } from '../components/Button';
import { Icon, type IconName } from '../components/Icon';
import { toolDiff } from './diff';
import { ChatAccount, DiffBody, Markdown } from './Items';
import { baseName, inputOf, inputText, planOf, questionsOf, toolArg, toolLabel, toolPath, type Question } from './tools';
import type { Answer, Request } from './types';

export interface RequestActions {
  answer: (requestId: string, a: Answer) => Promise<boolean>;
}

interface CardProps {
  req: Request;
  act: RequestActions;
  compact: boolean;
  /** A phone: the buttons one under the other, the width of the card. */
  stacked: boolean;
}

export function RequestCard(props: CardProps) {
  switch (props.req.kind) {
    case 'question':
      return <QuestionCard {...props} />;
    case 'plan':
      return <PlanCard {...props} />;
    default:
      return <PermissionCard {...props} />;
  }
}

/** Sends one answer at a time: the buttons wait while it goes. */
function useSend(act: RequestActions, id: string) {
  const [busy, setBusy] = useState<string>('');
  const send = async (key: string, a: Answer) => {
    if (busy) return;
    setBusy(key);
    const ok = await act.answer(id, a);
    // a request that was answered goes from the list; this card is gone then
    if (!ok) setBusy('');
  };
  return { busy, send };
}

function Head({ icon, title, why }: { icon: IconName; title: string; why?: string }) {
  return (
    <div className="nc-ask-head">
      <Icon name={icon} />
      <span className="nc-ask-title">{title}</span>
      {why && (
        <span className="nc-ask-why truncate" title={why}>
          {why}
        </span>
      )}
    </div>
  );
}

/** A note to the agent: why it is denied, or what to plan instead. */
function NoteBox({ label, placeholder, value, onChange, onSubmit }: { label: string; placeholder: string; value: string; onChange: (v: string) => void; onSubmit: () => void }) {
  return (
    <textarea
      className="control nc-note"
      aria-label={label}
      placeholder={placeholder}
      value={value}
      rows={2}
      autoFocus
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
          e.preventDefault();
          onSubmit();
        }
      }}
    />
  );
}

/** The heading of a permission request, by its tool. */
function permissionTitle(req: Request, compact: boolean): { icon: IconName; title: string } {
  const path = toolPath(req.tool, req.input);
  const file = path ? baseName(path) : '';
  switch (req.tool) {
    case 'Bash':
      return { icon: 'terminal', title: compact ? 'Run a command?' : 'NextCode wants to run a command' };
    case 'Edit':
    case 'MultiEdit':
    case 'NotebookEdit':
      return { icon: 'pencil', title: `Edit ${file || 'a file'}?` };
    case 'Write':
      return { icon: 'file', title: `Write ${file || 'a file'}?` };
    case 'Read':
      return { icon: 'fileText', title: `Read ${file || 'a file'}?` };
    case 'WebFetch': {
      const url = String(inputOf(req.input).url ?? '');
      let host = '';
      try {
        host = new URL(url).host;
      } catch {
        host = '';
      }
      return { icon: 'globe', title: host ? `Fetch ${host}?` : 'Fetch a page?' };
    }
    case 'WebSearch':
      return { icon: 'globe', title: 'Search the web?' };
    default:
      return { icon: 'alert', title: req.title || `Use ${req.displayName || toolLabel(req.tool)}?` };
  }
}

/** Who suggests the rule an Always allow keeps: the engine's CLI, or NextCode itself on the API engine. */
const RULE_FROM: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', api: 'NextCode' };

function PermissionCard({ req, act, compact, stacked }: CardProps) {
  const { engine } = useContext(ChatAccount);
  const { busy, send } = useSend(act, req.id);
  const [denying, setDenying] = useState(false);
  const [note, setNote] = useState('');
  const { icon, title } = permissionTitle(req, compact);
  const diff = useMemo(() => toolDiff(req.tool, req.input), [req.tool, req.input]);
  const command = req.tool === 'Bash' ? String(inputOf(req.input).command ?? '') : '';
  const arg = !command && !diff ? toolArg(req.tool, req.input) : '';
  const details = !command && !diff ? inputText(req.input) : '';
  // what the CLI says about it, when it adds to the heading
  const about = [req.description, req.title !== title ? req.title : ''].find((t) => t && t !== title) ?? '';
  const why = req.reason || (compact ? '' : req.parent ? 'asked by a sub-agent' : '');

  const deny = (stop: boolean) => send(stop ? 'stop' : 'deny', { decision: stop ? 'denyStop' : 'deny', message: note.trim() || undefined });

  return (
    <div className="nc-ask" role="group" aria-label={title}>
      <Head icon={icon} title={title} why={why} />
      {about && <p className="nc-ask-about">{about}</p>}
      {command && <pre className="nc-cmd">{command}</pre>}
      {diff && (
        <div className="nc-ask-diff">
          <DiffBody diff={diff} />
        </div>
      )}
      {arg && <code className="nc-cmd is-arg">{arg}</code>}
      {!arg && details && details !== '{}' && !command && !diff && <pre className="nc-cmd">{details}</pre>}
      {req.blockedPath && (
        <p className="nc-ask-about">
          Path: <code>{req.blockedPath}</code>
        </p>
      )}
      {denying ? (
        <>
          <NoteBox label="Tell the agent why (optional)" placeholder="Tell it why, or what to do instead (optional)" value={note} onChange={setNote} onSubmit={() => void deny(false)} />
          <div className={`nc-ask-opts ${stacked ? 'is-stacked' : ''}`}>
            <Button size="sm" variant="primary" loading={busy === 'deny'} disabled={!!busy} onClick={() => void deny(false)}>
              {note.trim() ? 'Deny and tell it' : 'Deny'}
            </Button>
            <Button size="sm" variant="danger-ghost" loading={busy === 'stop'} disabled={!!busy} title="Deny, and stop the agent's turn" onClick={() => void deny(true)}>
              Deny and stop
            </Button>
            <span className="spacer" />
            <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => setDenying(false)}>
              Back
            </Button>
          </div>
        </>
      ) : (
        <div className={`nc-ask-opts ${stacked ? 'is-stacked' : ''}`}>
          <Button size="sm" variant="primary" loading={busy === 'allow'} disabled={!!busy} onClick={() => void send('allow', { decision: 'allow' })}>
            Allow once
          </Button>
          {req.canRemember && (
            <Button size="sm" loading={busy === 'always'} disabled={!!busy} title={`Allow, and let calls like this one go ahead next time (the rule ${RULE_FROM[engine] ?? 'Claude Code'} suggests)`} onClick={() => void send('always', { decision: 'allowAlways' })}>
              Always allow
            </Button>
          )}
          <span className="spacer" />
          <Button size="sm" variant="danger-ghost" disabled={!!busy} onClick={() => setDenying(true)}>
            Deny…
          </Button>
        </div>
      )}
    </div>
  );
}

/** The labels picked for one question, and a free answer. */
interface Pick {
  labels: string[];
  other: boolean;
  text: string;
}

const NO_PICK: Pick = { labels: [], other: false, text: '' };

function answersOf(questions: Question[], picks: Record<string, Pick>): Record<string, string[]> | null {
  const out: Record<string, string[]> = {};
  for (const q of questions) {
    const p = picks[q.question] ?? NO_PICK;
    const list = [...p.labels, ...(p.other && p.text.trim() ? [p.text.trim()] : [])];
    if (!list.length) return null;
    out[q.question] = list;
  }
  return out;
}

function QuestionBlock({ q, pick, onPick, compact }: { q: Question; pick: Pick; onPick: (p: Pick) => void; compact: boolean }) {
  const id = useId();
  const choose = (label: string) => {
    if (q.multiSelect) {
      const on = pick.labels.includes(label);
      onPick({ ...pick, labels: on ? pick.labels.filter((l) => l !== label) : [...pick.labels, label] });
    } else onPick({ labels: [label], other: false, text: pick.text });
  };
  const chooseOther = () => onPick(q.multiSelect ? { ...pick, other: !pick.other } : { labels: [], other: true, text: pick.text });
  const role = q.multiSelect ? 'checkbox' : 'radio';
  return (
    <div className="nc-question">
      <p className="nc-q-text" id={id}>
        {q.question}
        {q.multiSelect && <span className="nc-quiet"> · choose any</span>}
      </p>
      <div className="nc-opts" role={q.multiSelect ? 'group' : 'radiogroup'} aria-labelledby={id}>
        {q.options.map((o) => {
          const on = pick.labels.includes(o.label);
          return (
            <button key={o.label} type="button" role={role} aria-checked={on} className={`nc-opt ${on ? 'is-on' : ''} ${q.multiSelect ? 'is-multi' : ''}`} onClick={() => choose(o.label)}>
              <span className="nc-opt-mark" aria-hidden="true" />
              <span className="nc-opt-body">
                <span className="nc-opt-label">{o.label}</span>
                {o.description && !compact && <span className="nc-opt-desc">{o.description}</span>}
                {o.description && compact && <span className="nc-opt-desc nc-clamp-2">{o.description}</span>}
              </span>
            </button>
          );
        })}
        <button type="button" role={role} aria-checked={pick.other} className={`nc-opt ${pick.other ? 'is-on' : ''} ${q.multiSelect ? 'is-multi' : ''}`} onClick={chooseOther}>
          <span className="nc-opt-mark" aria-hidden="true" />
          <span className="nc-opt-body">
            <span className="nc-opt-label is-other">Other…</span>
          </span>
        </button>
      </div>
      {pick.other && (
        <input
          className="control control-sm nc-other"
          aria-label={`Your own answer to: ${q.question}`}
          placeholder="Your answer"
          value={pick.text}
          autoFocus
          onChange={(e) => onPick({ ...pick, text: e.target.value })}
          onKeyDown={(e) => e.stopPropagation()}
        />
      )}
    </div>
  );
}

function QuestionCard({ req, act, compact, stacked }: CardProps) {
  const { busy, send } = useSend(act, req.id);
  const questions = useMemo(() => questionsOf(req.input), [req.input]);
  const [picks, setPicks] = useState<Record<string, Pick>>({});
  const answers = answersOf(questions, picks);
  const why = questions.length === 1 ? questions[0].header : '';
  return (
    <div className="nc-ask" role="group" aria-label="NextCode asks">
      <Head icon="info" title={questions.length > 1 ? `NextCode asks ${questions.length} questions` : 'NextCode asks'} why={why} />
      {!questions.length && <p className="nc-ask-about">{req.description || 'The agent asks something this view cannot show.'}</p>}
      {questions.map((q) => (
        <div key={q.question}>
          {questions.length > 1 && q.header && <span className="nc-label">{q.header}</span>}
          <QuestionBlock q={q} pick={picks[q.question] ?? NO_PICK} compact={compact} onPick={(p) => setPicks((all) => ({ ...all, [q.question]: p }))} />
        </div>
      ))}
      <div className={`nc-ask-opts ${stacked ? 'is-stacked' : ''}`}>
        <Button size="sm" variant="primary" loading={busy === 'answer'} disabled={!!busy || !answers} onClick={() => answers && void send('answer', { decision: 'allow', answers })}>
          Answer
        </Button>
        <span className="spacer" />
        <Button size="sm" variant="ghost" loading={busy === 'skip'} disabled={!!busy} title="Answer nothing: the agent goes on without it" onClick={() => void send('skip', { decision: 'deny', message: 'The owner chose not to answer.' })}>
          Skip
        </Button>
      </div>
    </div>
  );
}

function PlanCard({ req, act, compact, stacked }: CardProps) {
  const { busy, send } = useSend(act, req.id);
  const [keep, setKeep] = useState(false);
  const [note, setNote] = useState('');
  const plan = planOf(req.input);
  // without a note the hub tells the agent to keep planning, and the chat shows no note
  const keepPlanning = () => send('keep', { decision: 'deny', message: note.trim() || undefined });
  return (
    <div className="nc-ask" role="group" aria-label="Plan ready">
      <Head icon="checklist" title="Plan ready" why={compact ? '' : 'plan mode'} />
      <div className={`nc-plan ${compact ? 'is-compact' : ''}`}>{plan ? <Markdown text={plan} /> : <p className="nc-quiet">The plan is empty.</p>}</div>
      {keep ? (
        <>
          <NoteBox label="What to change in the plan" placeholder="What should it change or look at first?" value={note} onChange={setNote} onSubmit={() => void keepPlanning()} />
          <div className={`nc-ask-opts ${stacked ? 'is-stacked' : ''}`}>
            <Button size="sm" variant="primary" loading={busy === 'keep'} disabled={!!busy} onClick={() => void keepPlanning()}>
              Keep planning
            </Button>
            <span className="spacer" />
            <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => setKeep(false)}>
              Back
            </Button>
          </div>
        </>
      ) : (
        <div className={`nc-ask-opts ${stacked ? 'is-stacked' : ''}`}>
          <Button size="sm" variant="primary" loading={busy === 'approve'} disabled={!!busy} onClick={() => void send('approve', { decision: 'allow' })}>
            Approve
          </Button>
          <Button size="sm" loading={busy === 'edits'} disabled={!!busy} title="Approve, and let it edit files without asking" onClick={() => void send('edits', { decision: 'allow', mode: 'acceptEdits' })}>
            Approve, accept edits
          </Button>
          <span className="spacer" />
          <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => setKeep(true)}>
            Keep planning…
          </Button>
        </div>
      )}
    </div>
  );
}
