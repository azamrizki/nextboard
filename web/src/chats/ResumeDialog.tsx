// Resume chat (mockups 2 and 5): where the chat started (the place and the folder, which is the
// only place it can resume, so paths in it stay right), its agent and account, its last reply and
// size; then a new console there (under its old handle, or the next free name when another
// console holds it now) or a swap into an idle console of the same agent in the same folder, and
// whether to tell it what changed since. The hub does the work (POST /api/chats/{id}/resume).

import { useEffect, useMemo, useState } from 'react';
import { ApiError, errorMessage, isCode } from '../api';
import { AgentBadge } from '../agents/AgentBadge';
import { jumpToSession } from '../agents/jump';
import { useAgent } from '../agents/catalog';
import { agentPill } from '../agents/state';
import { useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { Checkbox } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgo } from '../lib/format';
import { placementOfSession } from '../lib/parts';
import { placeShort } from '../lib/placement';
import type { Project, Session } from '../lib/types';
import { useLeadName } from '../orch/lead';
import { useStoreSessions } from '../orch/hooks';
import { useProviders } from '../providers/providers';
import { store, useMachineNameOf, useMachines, useProfile } from '../store';
import { chatsApi } from './api';
import { changesLine, cleanHandle, clashOf, sessionOfResult, idleWhere, placeChip, readHandleTaken, refusedNow, swapCandidates, swapReasonShort, swapReasonText, swapStamp, type Refusal, type SwapReason } from './helpers';
import { chatsStore, useProjectChats } from './store';
import type { ChatDetail, ChatView, HandleTaken } from './types';
import { TranscriptDialog } from './ChatRows';
import '../styles/chats.css';

export interface ResumeDialogProps {
  chat: ChatView;
  project: Project | undefined;
  workspaceId: string;
  /** Where a new console's tile goes (board coordinates); near: by the tiles of the chat's place. */
  at?: { x: number; y: number; near?: boolean };
  /** From an editor card's agent pane: a console of that card, without a tile. */
  pane?: { card: string };
  onClose: () => void;
  /** The console that holds the chat now. */
  onResumed: (s: Session) => void;
  /** It is live after all: show its console. */
  onShow?: (sessionId: string) => void;
}

/** "/home/alice/code/shop-web" → "~/code/shop-web" on a machine whose home is /home/alice. */
export function tildePath(path: string, home: string | undefined): string {
  if (!home || home === '/' || !path.startsWith(home)) return path;
  const rest = path.slice(home.length);
  return rest === '' ? '~' : rest.startsWith('/') ? `~${rest}` : path;
}

type Mode = 'new' | 'swap';

/** A console the chat could move into. */
interface Candidate {
  id: string;
  handle: string;
  /** Unknown to this client when only the hub named it (409 handle_taken). */
  session?: Session;
  reason: SwapReason;
}

export function ResumeDialog({ chat: initial, project, workspaceId, at, pane, onClose, onResumed, onShow }: ResumeDialogProps) {
  const settings = useEffectiveSettings();
  const sessions = useStoreSessions();
  const machines = useMachines();
  const nameOf = useMachineNameOf();
  const lead = useLeadName();
  const providers = useProviders();
  const { items: projectChats } = useProjectChats(initial.projectId);
  const [detail, setDetail] = useState<ChatDetail | null>(null);
  // the row the events keep fresh (status, holder, handle, title) wins over the detail fetched
  // once; the detail adds what only it has (last reply, what changed, the handle clash)
  const chat: ChatView = projectChats.find((c) => c.id === initial.id) ?? detail ?? initial;
  const profile = useProfile(chat.profileId);
  const [changesState, setChangesState] = useState<'loading' | 'ready' | 'none'>('loading');
  const [hubTaken, setHubTaken] = useState<HandleTaken | null>(null);
  const [mode, setMode] = useState<Mode>('new');
  const [into, setInto] = useState('');
  const [name, setName] = useState('');
  const [nameTouched, setNameTouched] = useState(false);
  const [tell, setTell] = useState(settings.agents.resumeTellChanges ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  // the hub's 409 not_swappable, by console, until that console changes
  const [notSwappable, setNotSwappable] = useState<Record<string, Refusal>>({});
  const [live, setLive] = useState<string>('');
  const [transcript, setTranscript] = useState(false);

  // the view first, then what changed since (that may take a moment: git logs, memory)
  useEffect(() => {
    const ac = new AbortController();
    chatsApi
      .get(initial.id, false, ac.signal)
      .then((d) => {
        setDetail((cur) => ({ ...d, changes: cur?.changes ?? d.changes }));
        if (d.handleTaken) setHubTaken(d.handleTaken);
      })
      .catch(() => undefined);
    chatsApi
      .get(initial.id, true, ac.signal)
      .then((d) => {
        setDetail((cur) => ({ ...(cur ?? d), changes: d.changes ?? null, lastReply: cur?.lastReply ?? d.lastReply }));
        if (d.handleTaken) setHubTaken(d.handleTaken);
        setChangesState(changesLine(d.changes) ? 'ready' : 'none');
      })
      .catch(() => {
        if (!ac.signal.aborted) setChangesState('none');
      });
    return () => ac.abort();
  }, [initial.id]);

  // the hub's word on the old handle once it has answered; before that, what this client sees
  const hubSaid = !!detail && 'handleTaken' in detail;
  const clash = useMemo(() => (hubSaid && !hubTaken ? null : clashOf(chat, sessions, hubTaken)), [chat, sessions, hubTaken, hubSaid]);
  // the next free name, once the hub has said it; the user may type another
  useEffect(() => {
    if (clash?.next && !nameTouched) setName(`@${clash.next}`);
  }, [clash?.next, nameTouched]);

  // the consoles a swap could go into: the clash's holder (as the hub named it, when this client
  // does not know that console), else the others in the same folder
  const candidates = useMemo((): Candidate[] => {
    if (clash) {
      if (!clash.by) return [];
      const s = sessions[clash.by];
      return [{ id: clash.by, handle: s?.handle ?? hubTaken?.byHandle ?? clash.taken, session: s, reason: (refusedNow(notSwappable[clash.by], s) as SwapReason) || (clash.reason as SwapReason) }];
    }
    return swapCandidates(chat, sessions).map((c) => ({ id: c.session.id, handle: c.session.handle ?? c.session.name, session: c.session, reason: (refusedNow(notSwappable[c.session.id], c.session) as SwapReason) || c.reason }));
  }, [clash, sessions, chat, notSwappable, hubTaken]);
  useEffect(() => {
    if (!into && candidates.length) setInto(candidates[0].id);
  }, [candidates, into]);
  const target = candidates.find((c) => c.id === into) ?? candidates[0];
  const swapOk = !!target && !target.reason;
  useEffect(() => {
    if (mode === 'swap' && !swapOk) setMode('new');
  }, [mode, swapOk]);

  const agent = useAgent(chat.agent);
  const agentName = agent?.name ?? chat.agent;
  const provider = chat.providerId ? providers.find((p) => p.id === chat.providerId) : undefined;
  const account = provider ? `API provider ${provider.name}` : chat.profileId ? `login profile ${profile?.name ?? chat.profileId}` : 'default login';
  const machineLabel = (id: string) => (id === 'local' || !id ? 'This PC' : nameOf({ machineId: id, machineName: '' }) || id);
  const chip = placeChip(chat, project, machineLabel);
  const folder = chat.remoteCwd || chat.cwd;
  const home = machines.find((m) => m.id === (chat.remoteMachineId || chat.machineId))?.probe?.home;
  const shortFolder = tildePath(folder, home);
  const chipPlace = chip.kind === 'part' ? chip.label.split(' · ')[0] : chip.kind === 'root' ? 'Root' : '';
  const newName = clash ? cleanHandle(name) : chat.handle;
  const placeWords = chipPlace === 'Root' ? 'the root' : `the ${chipPlace} part`;
  const newWhere = pane ? 'in this editor' : !chipPlace ? 'on this board' : at?.near ? `next to ${placeWords}’s tiles` : chipPlace === 'Root' ? 'at the project root' : `in ${placeWords}`;
  const changes = changesLine(detail?.changes);
  const holder = clash ? sessions[clash.by] : undefined;
  const holderChat = clash ? clash.byChat || projectChats.find((c) => c.sessionId === clash.by && c.status !== 'closed')?.title || '' : '';
  const holderPlace = holder && project ? placementOfSession(holder, project) : null;
  const holderState = holder ? agentPill(holder).label.toLowerCase() : '';

  const resume = async () => {
    if (busy) return;
    setBusy(true);
    setError('');
    try {
      const body = {
        workspaceId,
        tellChanges: tell,
        ...(mode === 'swap' && target ? { into: target.id } : {}),
        ...(mode === 'new' && clash && newName ? { handle: newName } : {}),
        ...(mode === 'new' && at && !pane ? { x: Math.round(at.x), y: Math.round(at.y) } : {}),
        ...(pane ? { panel: true, card: pane.card } : {}),
      };
      const r = await chatsApi.resume(chat.id, body);
      const s = sessionOfResult(r);
      if (s) store.upsertSession(s);
      // its mission still runs, and a resumed chat does not rejoin it by itself
      if (r?.missionId) toast.info(`This chat was part of a mission that still runs. Ask ${lead} to add @${s?.handle ?? chat.handle} to the team.`);
      void chatsStore.load(chat.projectId, true);
      if (s) onResumed(s);
      onClose();
    } catch (err) {
      if (isCode(err, 'handle_taken') && err instanceof ApiError) {
        const t = readHandleTaken(err.body);
        if (t) {
          const again = !!clash && !!newName;
          setHubTaken(t);
          setName(`@${t.next}`);
          setNameTouched(false);
          setMode('new');
          if (again) setError(`@${newName} is taken too. The next free name is @${t.next}.`);
        } else setError(errorMessage(err));
      } else if (isCode(err, 'not_swappable') && err instanceof ApiError && target) {
        const reason = typeof err.body.reason === 'string' ? err.body.reason : 'busy';
        setNotSwappable((m) => ({ ...m, [target.id]: { reason, stamp: swapStamp(sessions[target.id]) } }));
        setMode('new');
        setError(swapReasonText(reason, target.handle));
      } else if (isCode(err, 'chat_live') && err instanceof ApiError) {
        const by = typeof err.body.sessionId === 'string' ? err.body.sessionId : typeof err.body.by === 'string' ? err.body.by : chat.sessionId ?? '';
        setLive(by);
        setError('This chat is open in a console already: one chat, one console.');
      } else if (isCode(err, 'chat_orphan')) {
        setError('Its place is gone or its folder moved, so it cannot resume there. Read its transcript, or continue it elsewhere from the Chats panel.');
      } else setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  // one chat, one console: held by one now (the hub said so, or it was resumed elsewhere meanwhile)
  const heldBy = live || (!busy && (chat.status === 'live' || chat.status === 'asleep') && chat.sessionId ? chat.sessionId : '');
  const was = chat.handle ? (
    <>
      {' '}
      · was <strong>@{chat.handle}</strong>
    </>
  ) : null;

  return (
    <Modal
      title="Resume chat"
      size="md"
      className="resume-chat-modal"
      onClose={onClose}
      dismissable={!busy}
      help={
        <Button variant="ghost" icon="fileText" onClick={() => setTranscript(true)} disabled={!chat.agentSessionId && !chat.transcriptPath}>
          View transcript
        </Button>
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          {heldBy ? (
            <Button
              variant="primary"
              icon="target"
              onClick={() => {
                onClose();
                if (onShow) onShow(heldBy);
                else jumpToSession(heldBy);
              }}
            >
              Show the console
            </Button>
          ) : (
            <Button variant="primary" icon="resume" loading={busy} disabled={(mode === 'new' && !!clash && !newName) || (mode === 'swap' && !swapOk)} onClick={() => void resume()}>
              Resume chat
            </Button>
          )}
        </>
      }
    >
      <div className="stack resume-chat">
        <p className="resume-sub">
          <AgentBadge agent={agent} small />
          <span className="truncate" title={chat.title}>
            {chat.title || 'Untitled chat'}
            {clash ? was : null}
          </span>
        </p>
        <dl className="resume-facts">
          {!clash && (
            <>
              <dt>Agent</dt>
              <dd>
                {agentName} · {account}
              </dd>
            </>
          )}
          <dt>Where</dt>
          <dd>
            <span className="resume-where">
              <span className={`chat-chip is-${chip.kind}`} title={chip.title}>
                {chip.kind === 'orphan' && <Icon name="alert" />}
                {chip.label}
              </span>
              <span className="mono truncate" title={folder}>
                {shortFolder}
              </span>
            </span>
            {!clash && <span className="resume-hint">The folder it was started in — paths in the chat stay right.</span>}
          </dd>
          {clash && (
            <>
              <dt>Agent</dt>
              <dd>
                {agentName} · {account}
              </dd>
            </>
          )}
          {!clash && detail?.lastReply && (
            <>
              <dt>Last reply</dt>
              <dd>
                <blockquote className="resume-reply">{detail.lastReply}</blockquote>
              </dd>
            </>
          )}
          {!clash && (
            <>
              <dt>Size</dt>
              <dd className="tabular">
                {chat.messages} {chat.messages === 1 ? 'message' : 'messages'} · started {formatDateTime(chat.startedAt)} · last {timeAgo(chat.lastAt || chat.startedAt)}
              </dd>
            </>
          )}
        </dl>

        {clash && (
          <div className="resume-taken" role="status">
            <Icon name="alert" />
            <span>
              <strong>@{clash.taken}</strong> is already on the board
              {holderChat ? (
                <>
                  , in another chat: “{holderChat}”
                </>
              ) : null}
              {holderPlace && project ? ` · ${placeShort(project, holderPlace)}` : ''}
              {holderState ? ` · ${holderState}` : ''}
            </span>
          </div>
        )}

        {chat.precise === false && (
          <p className="fs-xs text-3">{agentName} keeps no conversation ids: it continues the last conversation in that folder.</p>
        )}
        {chat.missionId && (
          <p className="fs-xs text-3">This chat was part of a mission. Resuming does not put it back on the team: ask {lead} to add it.</p>
        )}

        <div className="resume-opts" role="radiogroup" aria-label="Where it resumes">
          <div className={`resume-opt ${mode === 'new' ? 'is-on' : ''}`}>
            <button type="button" role="radio" aria-checked={mode === 'new'} className="radio-card" onClick={() => setMode('new')}>
              <span className="radio-dot" aria-hidden="true" />
              <span className="radio-card-body">
                <span className="radio-card-title">
                  <strong>New console</strong> {clash ? 'under another name' : newWhere}
                </span>
                {!clash && (
                  <span className="radio-card-desc">
                    {chat.handle ? `@${chat.handle} · ` : ''}
                    {agentName} · in {shortFolder}
                  </span>
                )}
              </span>
            </button>
            {clash && mode === 'new' && (
              <div className="resume-name">
                <div className="resume-name-row">
                  <label htmlFor="resume-name">Name</label>
                  <input
                    id="resume-name"
                    className="control control-sm"
                    value={name}
                    placeholder={clash.next ? `@${clash.next}` : 'The next free name'}
                    onChange={(e) => {
                      setName(e.target.value);
                      setNameTouched(true);
                    }}
                    maxLength={40}
                    spellCheck={false}
                    autoComplete="off"
                  />
                  <span className="text-3 fs-xs">{clash.next && cleanHandle(name) === clash.next ? 'the next free name' : clash.next ? '' : 'the hub picks the next free name'}</span>
                </div>
                <p className="fs-xs text-3">
                  The thread notes that this chat, once @{clash.taken}’s, is now in {newName ? `@${newName}` : 'the new console'}.
                </p>
              </div>
            )}
          </div>

          {candidates.length > 0 && !pane && (
            <div className={`resume-opt ${mode === 'swap' ? 'is-on' : ''}`}>
              <button
                type="button"
                role="radio"
                aria-checked={mode === 'swap'}
                className="radio-card"
                disabled={!swapOk}
                title={target?.reason ? swapReasonText(target.reason, target.handle) : undefined}
                onClick={() => setMode('swap')}
              >
                <span className="radio-dot" aria-hidden="true" />
                <span className="radio-card-body">
                  {clash && target ? (
                    <>
                      <span className="radio-card-title">
                        <strong>Swap into @{target.handle}</strong> — keeps the name
                      </span>
                      <span className="radio-card-desc">
                        {target.reason
                          ? swapReasonText(target.reason, target.handle)
                          : `@${target.handle}’s current chat goes to history and can be resumed later. Only while @${target.handle} is idle and in the same folder.`}
                      </span>
                    </>
                  ) : target ? (
                    <>
                      <span className="radio-card-title">
                        Into <strong>@{target.handle}</strong> ({agentName}, {target.reason ? swapReasonShort(target.reason) : target.session ? idleWhere(target.session, chipPlace) : 'idle'})
                      </span>
                      <span className="radio-card-desc">Only consoles in the same folder are offered.</span>
                    </>
                  ) : null}
                </span>
              </button>
              {!clash && mode === 'swap' && candidates.length > 1 && (
                <div className="resume-name">
                  <div className="resume-name-row">
                    <label htmlFor="resume-into">Console</label>
                    <select id="resume-into" className="control control-sm" value={into} onChange={(e) => setInto(e.target.value)}>
                      {candidates.map((c) => (
                        <option key={c.id} value={c.id} disabled={!!c.reason}>
                          @{c.handle}
                          {c.reason ? ` (${swapReasonShort(c.reason)})` : ''}
                        </option>
                      ))}
                    </select>
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        <Checkbox
          checked={tell}
          onChange={setTell}
          label={
            <span className="resume-tell">
              Tell it what changed since
              {changesState === 'loading' ? (
                <span className="text-3">: looking…</span>
              ) : changes ? (
                <>
                  : <strong>{changes}</strong>
                </>
              ) : (
                <span className="text-3"> (nothing so far)</span>
              )}
            </span>
          }
        />

        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
      </div>
      {transcript && <TranscriptDialog chat={chat} onClose={() => setTranscript(false)} />}
    </Modal>
  );
}
