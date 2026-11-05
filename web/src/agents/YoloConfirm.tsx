// Confirmation before any Yolo launch (docs/API-agents.md §9): per console, per spawn
// and for the hub-wide default. Wording follows the contract.

import type { ReactNode } from 'react';
import { ConfirmDialog } from '../components/ConfirmDialog';
import type { Machine } from '../lib/types';

/** "on devbox (alice@10.0.0.5)" for SSH machines, "on This PC" locally. */
export function onMachine(m: Machine | undefined, fallbackName = 'This PC'): string {
  if (!m) return `on ${fallbackName}`;
  if (m.kind !== 'ssh') return `on ${m.name}`;
  const host = m.sshConfigHost || (m.user ? `${m.user}@${m.host}` : m.host);
  return host && host !== m.name ? `on ${m.name} (${host})` : `on ${m.name}`;
}

interface YoloConfirmProps {
  /** e.g. "@Ash (Claude Code) on devbox" or "Every new console of an agent that supports it". */
  subject: ReactNode;
  /** Extra sentence (what happens right after confirming). */
  extra?: ReactNode;
  onConfirm: () => Promise<void> | void;
  onClose: () => void;
}

export function YoloConfirm({ subject, extra, onConfirm, onClose }: YoloConfirmProps) {
  return (
    <ConfirmDialog title="Run without permission prompts?" confirmLabel="Run with Yolo" danger icon="bolt" onConfirm={onConfirm} onClose={onClose}>
      <p>
        {subject} will edit and delete files and run any command without asking you first. This applies from the next launch.
      </p>
      {extra && <p className="confirm-extra">{extra}</p>}
    </ConfirmDialog>
  );
}
