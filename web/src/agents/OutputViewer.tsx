// Output viewer (docs/API-agents.md §8, C5): a full reply as a document, with Copy, Save .md
// (outputs/ in the project) and "Reply to @handle", which goes through the Phase 6 delivery
// engine (typed only when the agent is at its prompt). From a console it shows its last
// reply; from a report in a thread, the reply of that report, whatever the console did since.

import { useEffect, useState, type KeyboardEvent } from 'react';
import { api, errorMessage, isCode, isStatus } from '../api';
import { orchApi } from '../orch/api';
import type { ThreadMessage } from '../orch/types';
import { is404, orchStore, useOrch } from '../orch/store';
import { deliveryProblem } from '../orch/DeliveryQueue';
import { useMarkdown } from '../board/lazy';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgo } from '../lib/format';
import type { Session } from '../lib/types';
import { useSession } from '../store';
import { handleOf } from './state';

interface ShownReply {
  source?: 'transcript' | 'screen';
  text: string;
  at: string;
  chars: number;
  truncated: boolean;
  partial?: boolean;
}

type OutputViewerProps = { onClose: () => void } & ({ sessionId: string; message?: undefined } | { message: ThreadMessage; sessionId?: undefined });

export function OutputViewer({ sessionId, message, onClose }: OutputViewerProps) {
  const session = useSession(sessionId ?? message?.sessionId ?? '');
  const [reply, setReply] = useState<ShownReply | null>(null);
  const [state, setState] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const md = useMarkdown();
  // a report's reply never changes; a console's follows its last turn
  const replyAt = message ? '' : (session?.reply?.at ?? '');
  const threadId = message?.threadId;
  const messageId = message?.id;
  const messageText = message?.text;
  const messageAt = message?.at;
  const messageChars = message?.report?.chars;

  useEffect(() => {
    if (!threadId || !messageId) return;
    let stop = false;
    setError('');
    orchApi
      .messageOutput(threadId, messageId)
      .then((r) => {
        if (stop) return;
        setReply(r);
        setState('ready');
      })
      .catch((err) => {
        if (stop) return;
        // the thread was cut back, or an older hub: the message itself still says this much
        if (is404(err) && messageText) {
          const chars = messageChars ?? [...messageText].length;
          setReply({ text: messageText, at: messageAt ?? '', chars, truncated: false, partial: [...messageText].length < chars });
          setState('ready');
        } else if (is404(err)) setState('empty');
        else {
          setError(errorMessage(err));
          setState('error');
        }
      });
    return () => {
      stop = true;
    };
  }, [threadId, messageId, messageText, messageAt, messageChars]);

  useEffect(() => {
    if (!sessionId) return;
    const ac = new AbortController();
    setError('');
    api
      .reply(sessionId, ac.signal)
      .then((r) => {
        setReply(r);
        setState('ready');
      })
      .catch((err) => {
        if (ac.signal.aborted) return;
        if (isCode(err, 'no_reply') || isStatus(err, 404)) setState('empty');
        else {
          setError(errorMessage(err));
          setState('error');
        }
      });
    return () => ac.abort();
  }, [sessionId, replyAt]);

  const name = session ? handleOf(session) : message?.handle ? `@${message.handle}` : 'this console';

  const copy = async () => {
    if (!reply) return;
    try {
      await navigator.clipboard.writeText(reply.text);
      toast.success('Reply copied to the clipboard.');
    } catch {
      toast.error('The browser blocked clipboard access. Select the text and copy it instead.');
    }
  };

  const save = async () => {
    if (!session && !message) return;
    setSaving(true);
    try {
      const st = message ? await orchApi.saveMessageOutput(message.threadId, message.id) : await api.saveReply(session!.id);
      const base = (session?.cwd ?? '').replace(/\/+$/, '') + '/';
      toast.success(`Saved to ${session && st.path.startsWith(base) ? st.path.slice(base.length) : st.path}.`);
    } catch (err) {
      toast.error(`Could not save the reply: ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={`Output · ${name}`}
      icon="fileText"
      size="lg"
      className="output-modal"
      onClose={onClose}
      help={reply ? <ReplyMeta reply={reply} /> : undefined}
      footer={
        <>
          <Button variant="secondary" icon="download" onClick={() => void save()} loading={saving} disabled={!reply}>
            Save .md
          </Button>
          <Button variant="primary" icon="copy" onClick={() => void copy()} disabled={!reply}>
            Copy reply
          </Button>
        </>
      }
    >
      <div className="output-doc" tabIndex={0} aria-label={message ? `Reply from ${name}` : `Last reply from ${name}`}>
        {state === 'loading' && (
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> {message ? 'Reading the reply…' : 'Reading the last reply…'}
          </div>
        )}
        {state === 'empty' && (
          <div className="empty-state">
            <span className="empty-icon">
              <Icon name="fileText" />
            </span>
            <h3>{message ? 'This reply was not kept' : 'No reply yet'}</h3>
            <p>{message ? 'Its message is no longer in this thread.' : `When ${name} finishes a turn, its answer shows up here.`}</p>
          </div>
        )}
        {state === 'error' && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>Could not read the reply: {error}</span>
          </div>
        )}
        {state === 'ready' && reply && (
          md ? (
            <div className="md-body output-md" dangerouslySetInnerHTML={{ __html: md.renderMarkdown(reply.text) }} />
          ) : (
            <pre className="output-plain">{reply.text}</pre>
          )
        )}
        {reply?.truncated && <p className="output-note">The reply was longer than 200 KB and is cut off here.</p>}
        {reply?.partial && (
          <p className="output-note">
            This reply is from before whole replies were kept: {[...reply.text].length.toLocaleString()} of its {reply.chars.toLocaleString()} characters are left.
          </p>
        )}
      </div>
      {session && <ReplyBox session={session} />}
    </Modal>
  );
}

function ReplyMeta({ reply }: { reply: ShownReply }) {
  return (
    <span title={formatDateTime(reply.at)}>
      {reply.source ? `${reply.source === 'screen' ? 'Read from the screen' : 'From the transcript'} · ` : ''}
      {reply.chars.toLocaleString()} characters · {timeAgo(reply.at)}
    </span>
  );
}

const DELIVERY_TEXT: Record<string, string> = {
  queued: 'Queued: it is typed when @ is at its prompt.',
  waiting: 'Waiting: it is typed when @ is at its prompt.',
  typing: 'Typing…',
  sent: 'Sent, not confirmed yet.',
  confirmed: 'Delivered: @ is working on it.',
  failed: 'Not delivered',
  expired: 'Expired before @ was free.',
  cancelled: 'Cancelled.',
};

/** "Reply to @handle": a follow-up through the delivery engine (source "reply"). */
function ReplyBox({ session }: { session: Session }) {
  const id = `reply-${session.id}`;
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [deliveryId, setDeliveryId] = useState('');
  const d = useOrch((st) => (deliveryId ? st.deliveries[deliveryId] : undefined));
  const handle = handleOf(session);
  const notAgent = session.agentKind === 'shell' || session.agentState === 'shell' || session.status !== 'running';

  const send = async () => {
    const t = text.trim();
    if (!t || busy) return;
    setBusy(true);
    setError('');
    try {
      const res = await orchApi.deliver(session.id, { text: t, submit: true, source: 'reply' });
      orchStore.upsertDelivery(res);
      setDeliveryId(res.id);
      setText('');
    } catch (err) {
      if (is404(err)) setError('This hub cannot send follow-ups yet. Type into the console instead.');
      else if (isCode(err, 'not_agent')) setError(`${handle} is not running an agent, so nothing was typed. Start the agent first.`);
      else if (isCode(err, 'queue_full')) setError(`${handle} already has 20 messages waiting. Cancel some from its queue badge.`);
      else setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      void send();
    }
  };
  const status = d ? (DELIVERY_TEXT[d.state] ?? d.state).replace(/@/g, handle) + (d.state === 'failed' && (d.error || d.code) ? `: ${deliveryProblem(d)}.` : '') : '';
  return (
    <div className="field output-reply">
      <label className="field-label" htmlFor={id}>
        Reply to {handle}
      </label>
      <div className="output-reply-row">
        <textarea
          id={id}
          className="control"
          rows={2}
          value={text}
          disabled={notAgent}
          placeholder={notAgent ? `${handle} is not running an agent` : 'Type a follow-up… (Ctrl+Enter sends)'}
          aria-describedby={`${id}-hint`}
          aria-invalid={error ? true : undefined}
          maxLength={32000}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKey}
        />
        <Button variant="primary" icon="send" loading={busy} disabled={!text.trim() || notAgent} onClick={() => void send()}>
          Send reply
        </Button>
      </div>
      {error ? (
        <span className="field-error" id={`${id}-hint`}>
          <Icon name="alertCircle" />
          {error}
        </span>
      ) : (
        <span className="field-hint" id={`${id}-hint`} role="status">
          {status || `Typed into ${handle} when it is at its prompt — never into a shell or a permission question.`}
        </span>
      )}
    </div>
  );
}
