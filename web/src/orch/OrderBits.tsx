// Pieces shared by the orchestrator card on the board and the orchestrator pane in the editor.

import { useState, type MouseEvent, type ReactNode } from 'react';
import { agentPill } from '../agents/state';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { copyText } from '../lib/clipboard';
import { toast } from '../components/Toast';
import type { Session } from '../lib/types';
import type { ReplyDraft } from './lead';

/** Ideas for an empty thread. A click only fills the box; nothing is sent. */
export const GENERAL_IDEAS = ['What is the team working on?', 'Help me plan the next step', 'Run the tests and report what fails'];

/** The lead's face: the first letter of the name. */
export function LeadAvatar({ name, size = 'sm' }: { name: string; size?: 'sm' | 'md' | 'lg' }) {
  return (
    <span className={`lead-avatar size-${size}`} aria-hidden="true">
      {[...name.trim()][0]?.toUpperCase() ?? '?'}
    </span>
  );
}

/** A console as a chip: its state as a dot, a click mentions it in the order. */
export function AgentChip({ session: s, onMention, onMenu }: { session: Session; onMention: () => void; onMenu?: (e: MouseEvent) => void }) {
  const pill = agentPill(s);
  const name = s.handle ? `@${s.handle}` : s.name;
  return (
    <button
      type="button"
      className={`orch-chip ${s.paused ? 'is-paused' : ''} ${pill.state === 'needsYou' ? 'is-needs' : ''}`}
      title={`${name} · ${pill.label}. Click to mention${onMenu ? ', right-click for actions' : ''}.`}
      onClick={onMention}
      onContextMenu={onMenu}
    >
      <span className={`dot dot-${pill.dot}`} aria-hidden="true" />
      <span className="truncate">{name}</span>
    </button>
  );
}

/** What an empty thread shows. */
export function OrderWelcome({ title, lead, ideas, onIdea, children }: { title: string; lead?: string; ideas?: string[]; onIdea?: (idea: string) => void; children: ReactNode }) {
  return (
    <div className="orch-welcome">
      {lead ? (
        <LeadAvatar name={lead} size="lg" />
      ) : (
        <span className="orch-badge" aria-hidden="true">
          <Icon name="orchestrator" />
        </span>
      )}
      <h3>{title}</h3>
      {children}
      {ideas && ideas.length > 0 && (
        <div className="orch-ideas">
          {ideas.map((idea) => (
            <button key={idea} type="button" className="orch-idea" title="Puts this in the box below. Nothing is sent yet." onClick={() => onIdea?.(idea)}>
              <span className="truncate">{idea}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** Above the order box while answering a message. */
export function ReplyChip({ reply, onClear }: { reply: ReplyDraft; onClear: () => void }) {
  return (
    <div className="orch-reply" role="status">
      <Icon name="reply" />
      <span className="orch-reply-text truncate" title={reply.excerpt}>
        <strong>{reply.label}</strong> {reply.excerpt}
      </span>
      <button type="button" className="orch-reply-x" aria-label="Stop replying" title="Stop replying" onClick={onClear}>
        <Icon name="close" />
      </button>
    </div>
  );
}

/** Copies a text and says so on the button itself. */
export function CopyButton({ text, label = 'Copy', className }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    if (await copyText(text)) {
      setDone(true);
      window.setTimeout(() => setDone(false), 1500);
    } else toast.error('Could not copy. Select the text and copy it by hand.');
  };
  return (
    <button type="button" className={`msg-act ${done ? 'is-done' : ''} ${className ?? ''}`} aria-label={done ? 'Copied' : label} title={done ? 'Copied' : label} onClick={() => void copy()}>
      <Icon name={done ? 'check' : 'copy'} />
    </button>
  );
}

/** A lead's name and how it behaves, for one card. */
export function LeadProfileDialog({
  name,
  personality,
  fallback,
  onSave,
  onClose,
}: {
  name: string;
  personality: string;
  /** The hub's lead, used when these stay empty. */
  fallback: string;
  onSave: (name: string, personality: string) => void;
  onClose: () => void;
}) {
  const [n, setN] = useState(name);
  const [p, setP] = useState(personality);
  return (
    <Modal
      title="Lead profile"
      icon="orchestrator"
      onClose={onClose}
      help="This card only. Settings › Lead agent is for every other."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            onClick={() => {
              onSave(n.trim(), p.trim());
              onClose();
            }}
          >
            Save
          </Button>
        </>
      }
    >
      <div className="stack">
        <div className="field">
          <label className="field-label" htmlFor="lead-card-name">
            Name
          </label>
          <input id="lead-card-name" className="control" value={n} maxLength={24} placeholder={fallback} autoFocus onChange={(e) => setN(e.target.value)} />
        </div>
        <div className="field">
          <label className="field-label" htmlFor="lead-card-soul">
            Personality
          </label>
          <textarea
            id="lead-card-soul"
            className="control lead-soul"
            rows={4}
            maxLength={800}
            value={p}
            placeholder="How it talks and leads. Calm and to the point; pushes back when a plan looks risky."
            onChange={(e) => setP(e.target.value)}
          />
        </div>
      </div>
    </Modal>
  );
}
