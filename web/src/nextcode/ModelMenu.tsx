// The chat's model chip (docs/API-agents.md §30): the console's model and effort, and a menu of the
// models of every account NextCode has (One NextCode: its Claude and ChatGPT logins, its API
// providers; accounts.ts), each account's under its name (§25: "Default" for its own choice,
// "Other…" for a typed name, on the account the console runs on). A pick goes to PUT
// …/nextcode/model: on the same account the model switches at once and the effort with the CLI's
// next start, so an effort alone says "Applies to the next message"; a model of another account
// moves the chat there, and the next message brings the chat so far. The chip follows the session
// as the hub's session events bring it; a refused pick leaves it as it was.

import { useEffect, useId, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react';
import { useAgentCatalog, useMachineAgents } from '../agents/catalog';
import { useModelList } from '../agents/ModelPicker';
import { useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Popover } from '../components/Popover';
import { toast } from '../components/Toast';
import type { Agent, ModelChoice, Session } from '../lib/types';
import { useProviders } from '../providers/providers';
import { useProfiles } from '../store';
import { menuAccounts, sessionAccount, type AccountSources, type NcAccount } from './accounts';
import type { ModelChange } from './api';
import { engineLabel, NEXTCODE_EFFORTS } from './engines';
import { menuModels, modelChange, modelChipText, modelNameProblem, onlyEffort } from './modelMenu';

/** Where NextCode's accounts come from: the settings, its logins, the API providers and the catalog. */
export function useAccountSources(): AccountSources {
  const settings = useEffectiveSettings();
  const profiles = useProfiles();
  const providers = useProviders();
  const { agents } = useMachineAgents('local');
  return useMemo(() => ({ settings, profiles, providers, agents }), [settings, profiles, providers, agents]);
}

/** How long "Applies to the next message" stays by the chip. */
const HINT_MS = 6000;

interface Props {
  session: Session | undefined;
  agent: Agent | undefined;
  /** The model the CLI reports it runs (Status.model). */
  running?: string;
  /** "claude" | "codex" | "api": shown next to the chip. */
  engine?: string;
  /** The composer's bar (card, phone): a smaller chip, the menu opens upward. */
  compact?: boolean;
  disabled?: boolean;
  onChange: (c: ModelChange) => Promise<boolean>;
}

export function ModelMenu({ session, agent, running, engine, compact = false, disabled = false, onChange }: Props) {
  const src = useAccountSources();
  const account = sessionAccount(session, src);
  const accounts = useMemo(() => menuAccounts(account, src), [account, src]);
  const own = accounts.find((a) => a.id === account);
  const { list, onProvider, pickable: ownPickable } = useModelList(agent, account);
  // another account's models can be picked too, when it has any
  const pickable = ownPickable || accounts.length > 1;
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [hint, setHint] = useState(false);
  // what was just picked, until the session event brings it
  const [picked, setPicked] = useState<ModelChoice | null>(null);
  const ref = useRef<HTMLButtonElement>(null);
  const now: ModelChoice = { model: session?.model ?? '', effort: session?.effort ?? '' };
  const shown = picked ?? now;

  useEffect(() => {
    if (!picked) return;
    if ((picked.model ?? '') === now.model && (picked.effort ?? '') === now.effort) {
      setPicked(null);
      return;
    }
    const t = window.setTimeout(() => setPicked(null), 5000);
    return () => window.clearTimeout(t);
  }, [picked, now.model, now.effort]);

  useEffect(() => {
    if (!hint) return;
    const t = window.setTimeout(() => setHint(false), HINT_MS);
    return () => window.clearTimeout(t);
  }, [hint]);

  const text = modelChipText(shown, running, list?.models);
  const eng = engineLabel(engine);
  // the account it runs on: "Claude", "Codex", the provider's name
  const where = own?.name || eng;
  const engineTag = where && (
    <span className="nc-engine" title={engine === 'api' ? `NextCode runs on ${where}, an API provider, with its own engine` : `NextCode runs on ${where}, through ${eng === 'Codex' ? 'Codex' : 'Claude Code'}`}>
      {where}
    </span>
  );

  const apply = async (pick: ModelChange) => {
    setOpen(false);
    ref.current?.focus();
    if (pick.account && pick.account !== account) {
      // another account: the chat moves there with the model picked (its own effort)
      setBusy(true);
      const ok = await onChange({ account: pick.account, model: pick.model ?? '' });
      setBusy(false);
      // the chat's effort is NextCode's: it goes along
      if (ok) setPicked({ model: pick.model ?? '', effort: shown.effort ?? '' });
      return;
    }
    // NextCode's effort stays whatever the model (the hub fits it to the model): no list to check it against
    const change = modelChange(shown, { model: pick.model, effort: pick.effort }, null);
    if (!change) return;
    setBusy(true);
    const ok = await onChange(change);
    setBusy(false);
    if (!ok) return;
    setPicked({ model: change.model ?? shown.model, effort: change.effort ?? shown.effort });
    if (!onlyEffort(change)) setHint(false);
    // the card's bar has no room for the hint next to the chip
    else if (compact) toast.info('Applies to the next message');
    else setHint(true);
  };

  if (!pickable) {
    return (
      <span className={`nc-model ${compact ? 'is-compact' : ''}`}>
        <span className="nc-chip is-static" title="The model of this console">
          <Icon name="cpu" />
          <span className="truncate">{text}</span>
        </span>
        {engineTag}
      </span>
    );
  }

  return (
    <span className={`nc-model ${compact ? 'is-compact' : ''}`}>
      <button
        ref={ref}
        type="button"
        className="nc-chip"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-busy={busy || undefined}
        aria-label={`Model: ${text}. Change it`}
        title={`Model and effort of this console${eng ? ` (${eng} engine)` : ''}`}
        disabled={disabled}
        onClick={() => !busy && setOpen((o) => !o)}
      >
        {busy ? <span className="spinner nc-chip-spin" aria-hidden="true" /> : <Icon name="cpu" />}
        <span className="truncate">{text}</span>
        <Icon name="chevronDown" />
      </button>
      {engineTag}
      {hint && (
        <span className="nc-model-hint" role="status">
          Applies to the next message
        </span>
      )}
      {open && (
        <Popover anchor={ref} onClose={() => setOpen(false)} label="Model and effort" placement={compact ? 'above' : 'below'} align="start" className="nc-model-pop">
          <ModelMenuBody
            agent={agent}
            list={list}
            onProvider={onProvider}
            now={shown}
            engine={where}
            account={account}
            accounts={accounts}
            nextTurn={engine === 'codex' || engine === 'api'}
            onPick={(p) => void apply(p)}
          />
        </Popover>
      )}
    </span>
  );
}

interface BodyProps {
  agent: Agent | undefined;
  list: ReturnType<typeof useModelList>['list'];
  onProvider: boolean;
  now: ModelChoice;
  /** The account's name (else the engine's), in the heading. */
  engine: string;
  /** The account the console runs on, and every account NextCode has. */
  account: string;
  accounts: NcAccount[];
  /** A new model, too, applies from the next message (Codex, the API engine), not at once. */
  nextTurn: boolean;
  onPick: (c: ModelChange) => void;
}

function ModelMenuBody({ agent, list, onProvider, now, engine, account, accounts, nextTurn, onPick }: BodyProps) {
  const model = now.model ?? '';
  const effort = now.effort ?? '';
  const view = menuModels(list, model, effort);
  const [typing, setTyping] = useState(false);
  const [typed, setTyped] = useState('');
  const [problem, setProblem] = useState('');
  const boxRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const uid = useId();

  // the choice the console has takes the keyboard (after the popover places itself)
  useEffect(() => {
    const id = window.requestAnimationFrame(() => boxRef.current?.querySelector<HTMLElement>('.nc-mm-model[aria-checked="true"]')?.focus());
    return () => window.cancelAnimationFrame(id);
  }, []);

  useEffect(() => {
    if (typing) inputRef.current?.focus();
  }, [typing]);

  // ↑/↓ move through every choice, ←/→ along the efforts
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).tagName === 'INPUT') return;
    const items = Array.from(boxRef.current?.querySelectorAll<HTMLButtonElement>('button.menu-item:not(:disabled)') ?? []);
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    const inRow = (document.activeElement as HTMLElement | null)?.closest('.nc-mm-efforts');
    let next = -1;
    if (e.key === 'ArrowDown') next = inRow ? items.findIndex((b, k) => k > i && !b.closest('.nc-mm-efforts')) : i + 1;
    else if (e.key === 'ArrowUp') {
      next = i - 1;
      while (inRow && next >= 0 && items[next].closest('.nc-mm-efforts')) next--;
    } else if (inRow && (e.key === 'ArrowRight' || e.key === 'ArrowLeft')) next = e.key === 'ArrowRight' ? i + 1 : i - 1;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = items.length - 1;
    else return;
    e.preventDefault();
    if (!items.length) return;
    items[(next + items.length) % items.length]?.focus();
  };

  const submitTyped = (e: FormEvent) => {
    e.preventDefault();
    const p = modelNameProblem(typed);
    setProblem(p);
    if (!p) onPick({ model: typed.trim() });
  };

  const row = (id: string, label: string, title?: string) => (
    <button
      key={id || '\0default'}
      type="button"
      role="radio"
      aria-checked={!typing && id === model}
      className="menu-item nc-mm-model"
      title={title}
      onClick={() => onPick({ model: id })}
    >
      <span className="menu-label">{label}</span>
      {!typing && id === model && <Icon name="check" className="menu-check" />}
    </button>
  );

  // several accounts: each one's models under its name, the console's own first among them as listed
  const several = accounts.length > 1;
  return (
    <div className="nc-mm" ref={boxRef} onKeyDown={onKey}>
      <div className="menu-heading" id={`${uid}-model`}>
        Model{engine && !several ? ` · ${engine}` : ''}
      </div>
      <div role="radiogroup" aria-labelledby={`${uid}-model`} className="nc-mm-list">
        {accounts.map((a) =>
          a.id === account ? (
            <div key={a.id} role="group" aria-label={a.name} className="nc-mm-account">
              {several && <AccountHeading account={a} current />}
              {!list && !onProvider && agent?.models && (
                <div className="nc-mm-note" role="status">
                  <span className="spinner" aria-hidden="true" /> Reading what the CLI can run…
                </div>
              )}
              {row('', view.defaultLabel, onProvider ? 'The API provider’s own model' : 'What the CLI runs when told nothing')}
              {view.models.map((m) => row(m.id, m.name, m.description || m.id))}
            </div>
          ) : (
            <OtherAccount key={a.id} account={a} onPick={(model) => onPick({ account: a.id, model })} />
          ),
        )}
        {!accounts.some((a) => a.id === account) && (
          <>
            {row('', view.defaultLabel, onProvider ? 'The API provider’s own model' : 'What the CLI runs when told nothing')}
            {view.models.map((m) => row(m.id, m.name, m.description || m.id))}
          </>
        )}
        <button type="button" role="radio" aria-checked={typing} className="menu-item nc-mm-model" onClick={() => setTyping(true)}>
          <span className="menu-label">Other…</span>
          {typing && <Icon name="check" className="menu-check" />}
        </button>
      </div>
      {typing && (
        <form className="nc-mm-other" onSubmit={submitTyped}>
          <input
            ref={inputRef}
            className="control control-sm mono"
            aria-label="Model name"
            aria-invalid={problem ? true : undefined}
            placeholder="Model name"
            maxLength={128}
            spellCheck={false}
            value={typed}
            onChange={(e) => {
              setTyped(e.target.value);
              setProblem('');
            }}
          />
          <Button type="submit" size="sm" variant="primary" disabled={!typed.trim()}>
            Use
          </Button>
          {problem && (
            <span className="nc-mm-problem" role="alert">
              {problem}
            </span>
          )}
        </form>
      )}
      {agent?.efforts && (
        <>
          <div className="menu-sep" role="separator" />
          <div className="menu-heading" id={`${uid}-effort`}>
            Effort
          </div>
          <div role="radiogroup" aria-labelledby={`${uid}-effort`} className="nc-mm-efforts">
            {/* NextCode's efforts on every account; one this chat has from before stays listed */}
            {['', ...NEXTCODE_EFFORTS, ...(effort && !NEXTCODE_EFFORTS.includes(effort) ? [effort] : [])].map((x) => (
              <button
                key={x || '\0default'}
                type="button"
                role="radio"
                aria-checked={x === effort}
                className="menu-item choice-item"
                title={x ? `NextCode's ${x}, on every model of this chat: a model without it runs at its nearest` : 'The model’s own effort'}
                onClick={() => onPick({ effort: x })}
              >
                {x || 'Default'}
              </button>
            ))}
          </div>
          {/* Claude Code is told a model at once; Codex and the API engine take it with their next turn */}
          <p className="nc-mm-note">{nextTurn ? 'A new model or effort applies from the next message.' : 'A new model applies at once, a new effort from the next message.'}</p>
        </>
      )}
      {several && <p className="nc-mm-note">A model of another account starts a new conversation there; your next message brings this chat along.</p>}
    </div>
  );
}

function AccountHeading({ account, current }: { account: NcAccount; current?: boolean }) {
  return (
    <div className="nc-mm-acc" title={account.detail}>
      <span className="truncate">{account.name}</span>
      {current && <span className="nc-mm-acc-tag">this chat</span>}
      {account.missing && <span className="nc-mm-acc-tag is-bad">cannot run</span>}
    </div>
  );
}

/** Another account's models: picking one moves the chat there. */
function OtherAccount({ account, onPick }: { account: NcAccount; onPick: (model: string) => void }) {
  const catalog = useAgentCatalog();
  const agent = catalog.agents.find((a) => a.id === account.agentId);
  const { list, onProvider } = useModelList(agent, account.id);
  const view = menuModels(list, '', '');
  const item = (id: string, label: string, title: string) => (
    <button key={id || '\0default'} type="button" role="radio" aria-checked={false} className="menu-item nc-mm-model" title={title} disabled={account.missing} onClick={() => onPick(id)}>
      <span className="menu-label">{label}</span>
    </button>
  );
  return (
    <div role="group" aria-label={account.name} className="nc-mm-account">
      <AccountHeading account={account} />
      {!list && !onProvider && agent?.models && (
        <div className="nc-mm-note" role="status">
          <span className="spinner" aria-hidden="true" /> Reading its models…
        </div>
      )}
      {item('', view.defaultLabel, `${account.name}: ${onProvider ? 'the API provider’s own model' : 'what its CLI runs when told nothing'}`)}
      {view.models.map((m) => item(m.id, m.name, `${account.name} · ${m.description || m.id}`))}
    </div>
  );
}
