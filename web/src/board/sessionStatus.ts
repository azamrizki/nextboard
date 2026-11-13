// Maps sessions / machines to a status-dot colour and label (DESIGN.md §3.7).

import type { DotColor } from '../components/StatusDot';
import type { SocketState } from '../lib/socket';
import type { Machine, Session } from '../lib/types';

export interface StatusInfo {
  color: DotColor;
  label: string;
  /** Longer explanation for tooltips. */
  detail?: string;
}

export function sessionStatus(s: Session, socket: SocketState): StatusInfo {
  if (s.status === 'error') return { color: 'danger', label: 'Error', detail: s.error || undefined };
  if (s.status === 'exited') {
    return s.exitCode === 0
      ? { color: 'grey', label: 'Exited', detail: 'Process exited with code 0' }
      : { color: 'danger', label: `Exited (${s.exitCode})`, detail: `Process exited with code ${s.exitCode}` };
  }
  if (s.status === 'reconnecting') return { color: 'warning', label: 'Reconnecting', detail: 'Reconnecting to the machine' };
  if (s.status === 'asleep') return { color: 'grey', label: 'Asleep', detail: 'Stopped to free memory; it resumes when opened or typed into' };
  if (s.status === 'starting') return { color: 'warning', label: 'Starting' };
  if (socket !== 'open') return { color: 'warning', label: 'Connecting', detail: 'Connecting to the hub' };
  return s.busy
    ? { color: 'busy', label: 'Working', detail: 'Output in the last 2 seconds' }
    : { color: 'success', label: 'Idle' };
}

export function machineStatus(m: Machine): StatusInfo {
  switch (m.status) {
    case 'local':
      return { color: 'success', label: 'Local' };
    case 'connected':
      return { color: 'success', label: 'Connected' };
    case 'connecting':
      return { color: 'warning', label: 'Connecting' };
    case 'reconnecting':
      return { color: 'warning', label: 'Reconnecting' };
    case 'hostkey':
      return { color: 'warning', label: 'Verify host key' };
    case 'auth':
      return { color: 'warning', label: m.needsAuth?.kind === 'passphrase' ? 'Needs passphrase' : 'Needs password' };
    case 'disconnected':
      return { color: 'grey', label: 'Disconnected' };
    case 'error':
    default:
      return m.hostKeyMismatch
        ? { color: 'danger', label: 'Host key changed' }
        : { color: 'danger', label: 'Error', detail: m.error || undefined };
  }
}

/** Machine status is usable for spawning terminals. */
export function machineReady(m: Machine): boolean {
  return m.status === 'local' || m.status === 'connected';
}
