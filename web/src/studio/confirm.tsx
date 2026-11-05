// Remote-write confirmation (docs/API-studio.md §9.2, owner decision #3): writes into a
// Claude config folder on an SSH machine answer 428 with the exact paths they would
// touch. We show the machine and the paths; "Write to <machine>" repeats the request
// with confirm.paths set to that list (the hub compares it as a set).

import { useCallback, useRef, useState, type ReactNode } from 'react';
import { ApiError } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import type { Confirm } from './api';

interface Pending {
  machine: string;
  paths: string[];
  resolve: (ok: boolean) => void;
}

export function isConfirmRequired(err: unknown): err is ApiError {
  return err instanceof ApiError && err.status === 428 && err.code === 'confirm_required';
}

/**
 * run(fn): calls fn({}); on a 428 asks the user and, when confirmed, calls
 * fn({confirm:{paths}}) once more. Resolves to null when the user cancels.
 */
export function useRemoteConfirm(): [ReactNode, <T>(fn: (c: Confirm) => Promise<T>) => Promise<T | null>] {
  const [pending, setPending] = useState<Pending | null>(null);
  const ref = useRef(pending);
  ref.current = pending;

  const run = useCallback(async <T,>(fn: (c: Confirm) => Promise<T>): Promise<T | null> => {
    try {
      return await fn({});
    } catch (err) {
      if (!isConfirmRequired(err)) throw err;
      const paths = Array.isArray(err.body.paths) ? (err.body.paths as string[]) : [];
      const machine = typeof err.body.machine === 'string' ? err.body.machine : 'the remote machine';
      const ok = await new Promise<boolean>((resolve) => setPending({ machine, paths, resolve }));
      setPending(null);
      if (!ok) return null;
      return fn({ confirm: { paths } });
    }
  }, []);

  const dialog = pending ? (
    <Modal
      title={`Write to ${pending.machine}?`}
      icon="shieldAlert"
      size="md"
      onClose={() => pending.resolve(false)}
      footer={
        <>
          <Button onClick={() => pending.resolve(false)}>Cancel</Button>
          <Button variant="primary" icon="check" onClick={() => pending.resolve(true)} autoFocus>
            Write to {pending.machine}
          </Button>
        </>
      }
    >
      <div className="stack">
        <p className="text-2">
          This changes the Claude Code configuration folder on <strong>{pending.machine}</strong>. NextBoard keeps a copy of anything it overwrites or deletes on the hub. These
          paths will be written:
        </p>
        <ul className="plain confirm-paths mono fs-xs" aria-label="Paths to write">
          {pending.paths.map((p) => (
            <li key={p}>
              <Icon name="file" /> <span className="truncate">{p}</span>
            </li>
          ))}
        </ul>
      </div>
    </Modal>
  ) : null;
  return [dialog, run];
}
