// Live state fed by /api/events (sessions, machines, devices, projects) plus helpers so
// REST responses can be applied immediately. Plain external store + useSyncExternalStore.
// Board ops ("boardOps") are not stored here: boards subscribe via onEvent().

import { useMemo, useSyncExternalStore } from 'react';
import { openEventsSocket, type ReconnectingSocket } from './api';
import type { Device, HubEvent, Machine, Profile, Project, ProjectGroup, Session, SessionStatusFrame, Settings } from './lib/types';

export interface LiveState {
  sessions: Record<string, Session>;
  machines: Record<string, Machine>;
  devices: Record<string, Device>;
  projects: Record<string, Project>;
  projectGroups: Record<string, ProjectGroup>;
  /** Projects have been received at least once (snapshot or REST). */
  projectsReady: boolean;
  /** A snapshot (or REST seed) has been received. */
  ready: boolean;
  /** The /api/events socket is currently open. */
  connected: boolean;
  /** Phase 5 settings document (null until loaded / on hubs without it). */
  settings: Settings | null;
  /** Bumped on every "agents" event (catalog changed → refetch). */
  agentsRev: number;
  profiles: Record<string, Profile>;
  profilesReady: boolean;
}

const EMPTY: LiveState = {
  sessions: {},
  machines: {},
  devices: {},
  projects: {},
  projectGroups: {},
  projectsReady: false,
  ready: false,
  connected: false,
  settings: null,
  agentsRev: 0,
  profiles: {},
  profilesReady: false,
};


function byId<T extends { id: string }>(list: T[] | null | undefined): Record<string, T> {
  const out: Record<string, T> = {};
  for (const item of list ?? []) out[item.id] = item;
  return out;
}

function without<T>(rec: Record<string, T>, id: string): Record<string, T> {
  if (!(id in rec)) return rec;
  const next = { ...rec };
  delete next[id];
  return next;
}

type Listener = () => void;
type EventListener = (ev: HubEvent) => void;

class LiveStore {
  private state: LiveState = EMPTY;
  private listeners = new Set<Listener>();
  private eventListeners = new Set<EventListener>();
  private socket: ReconnectingSocket | null = null;

  getState = (): LiveState => this.state;

  subscribe = (fn: Listener): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  /** Listen to raw hub events (after they have been applied to the state). */
  onEvent(fn: EventListener): () => void {
    this.eventListeners.add(fn);
    return () => this.eventListeners.delete(fn);
  }

  private set(patch: Partial<LiveState>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((l) => l());
  }

  // ---- connection lifecycle ----

  /** Open /api/events (idempotent). */
  connect() {
    if (this.socket) return;
    this.socket = openEventsSocket({
      onStateChange: (s) => this.set({ connected: s === 'open' }),
      onMessage: (ev) => {
        if (typeof ev.data !== 'string') return;
        let msg: HubEvent;
        try {
          msg = JSON.parse(ev.data) as HubEvent;
        } catch {
          return;
        }
        this.apply(msg);
      },
    });
  }

  /** Close the socket and forget everything (logout). */
  reset() {
    this.socket?.close();
    this.socket = null;
    this.state = EMPTY;
    this.listeners.forEach((l) => l());
  }

  apply(ev: HubEvent) {
    switch (ev.type) {
      case 'snapshot':
        this.set({
          sessions: byId(ev.sessions),
          machines: byId(ev.machines),
          devices: byId(ev.devices),
          ...(ev.projects !== undefined ? { projects: byId(ev.projects), projectsReady: true } : {}),
          ...(ev.projectGroups !== undefined ? { projectGroups: byId(ev.projectGroups) } : {}),
          ready: true,
        });
        break;
      case 'project':
        this.upsertProject(ev.project);
        break;
      case 'projectRemoved':
        this.removeProject(ev.id);
        break;
      case 'projectGroups':
        this.set({ projectGroups: byId(ev.groups) });
        break;
      case 'session':
        this.upsertSession(ev.session);
        break;
      case 'removed':
        this.removeSession(ev.id);
        break;
      case 'machine':
        this.upsertMachine(ev.machine);
        break;
      case 'machineRemoved':
        this.removeMachine(ev.id);
        break;
      case 'device':
        this.upsertDevice(ev.device);
        break;
      case 'deviceRemoved':
        this.removeDevice(ev.id);
        break;
      case 'settings':
        this.set({ settings: ev.settings });
        break;
      case 'agents':
        this.set({ agentsRev: this.state.agentsRev + 1 });
        break;
      case 'profile':
        this.set({ profiles: { ...this.state.profiles, [ev.profile.id]: ev.profile } });
        break;
      case 'profileRemoved': {
        const next = without(this.state.profiles, ev.id);
        if (next !== this.state.profiles) this.set({ profiles: next });
        break;
      }
      default:
        break;
    }
    this.eventListeners.forEach((l) => l(ev));
  }

  // ---- mutations (also used to apply REST responses optimistically) ----

  /** Seed from REST before the first snapshot arrives. */
  seedSessions(list: Session[]) {
    if (this.state.ready) return;
    this.set({ sessions: byId(list), ready: true });
  }
  setSettings(settings: Settings) {
    this.set({ settings });
  }
  bumpAgents() {
    this.set({ agentsRev: this.state.agentsRev + 1 });
  }
  seedProfiles(list: Profile[]) {
    this.set({ profiles: byId(list), profilesReady: true });
  }
  upsertProfile(p: Profile) {
    this.set({ profiles: { ...this.state.profiles, [p.id]: p } });
  }
  removeProfile(id: string) {
    const next = without(this.state.profiles, id);
    if (next !== this.state.profiles) this.set({ profiles: next });
  }
  seedMachines(list: Machine[]) {
    this.set({ machines: byId(list) });
  }
  seedDevices(list: Device[]) {
    this.set({ devices: byId(list) });
  }

  upsertSession(s: Session) {
    this.set({ sessions: { ...this.state.sessions, [s.id]: s } });
  }
  /** Apply a status frame from a session socket; no-op if nothing changed. */
  patchSessionStatus(id: string, f: SessionStatusFrame) {
    const cur = this.state.sessions[id];
    if (!cur) return;
    const next: Session = { ...cur, status: f.status, exitCode: f.exitCode, busy: f.busy };
    // Phase 5 fields only when the frame carries them (Phase-4 hubs don't)
    if (f.agentState !== undefined) next.agentState = f.agentState;
    if (f.activity !== undefined) next.activity = f.activity;
    if (f.needsYouSince !== undefined) next.needsYouSince = f.needsYouSince;
    if (f.foreground !== undefined) next.foreground = f.foreground;
    if (f.job !== undefined) next.job = f.job;
    if (f.active !== undefined) next.active = f.active;
    if (
      cur.status === next.status &&
      cur.exitCode === next.exitCode &&
      cur.busy === next.busy &&
      cur.agentState === next.agentState &&
      cur.activity === next.activity &&
      cur.needsYouSince === next.needsYouSince &&
      cur.foreground === next.foreground &&
      cur.job === next.job &&
      cur.active === next.active
    )
      return;
    this.upsertSession(next);
  }
  removeSession(id: string) {
    const next = without(this.state.sessions, id);
    if (next !== this.state.sessions) this.set({ sessions: next });
  }
  upsertMachine(m: Machine) {
    this.set({ machines: { ...this.state.machines, [m.id]: m } });
  }
  removeMachine(id: string) {
    const next = without(this.state.machines, id);
    if (next !== this.state.machines) this.set({ machines: next });
  }
  seedProjects(list: Project[]) {
    this.set({ projects: byId(list), projectsReady: true });
  }
  upsertProject(p: Project) {
    this.set({ projects: { ...this.state.projects, [p.id]: p } });
  }
  removeProject(id: string) {
    const next = without(this.state.projects, id);
    if (next !== this.state.projects) this.set({ projects: next });
  }
  upsertProjectGroup(g: ProjectGroup) {
    this.set({ projectGroups: { ...this.state.projectGroups, [g.id]: g } });
  }
  removeProjectGroup(id: string) {
    const next = without(this.state.projectGroups, id);
    if (next !== this.state.projectGroups) this.set({ projectGroups: next });
  }
  upsertDevice(d: Device) {
    this.set({ devices: { ...this.state.devices, [d.id]: d } });
  }
  removeDevice(id: string) {
    const next = without(this.state.devices, id);
    if (next !== this.state.devices) this.set({ devices: next });
  }
}

export const store = new LiveStore();

// ---- hooks ----------------------------------------------------------------------

function useSlice<T>(select: (s: LiveState) => T): T {
  return useSyncExternalStore(store.subscribe, () => select(store.getState()));
}

export function useEventsConnected(): boolean {
  return useSlice((s) => s.connected);
}

export function useStoreReady(): boolean {
  return useSlice((s) => s.ready);
}

export function useSession(id: string): Session | undefined {
  return useSlice((s) => s.sessions[id]);
}

export function useMachine(id: string): Machine | undefined {
  return useSlice((s) => s.machines[id]);
}

const byCreation = (a: Session, b: Session) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id);

/** Sessions sorted by creation time (stable order for auto-layout). */
export function useSessions(): Session[] {
  const rec = useSlice((s) => s.sessions);
  return useMemo(() => Object.values(rec).sort(byCreation), [rec]);
}

/**
 * Session ids in creation order. Unlike useSessions() this only changes when sessions
 * are added or removed, not on every status/busy update.
 */
export function useSessionIds(): string[] {
  const key = useSlice((s) =>
    Object.values(s.sessions)
      .sort(byCreation)
      .map((x) => x.id)
      .join('\n'),
  );
  return useMemo(() => (key ? key.split('\n') : []), [key]);
}

/** Machines with "local" first, then by name. */
export function useMachines(): Machine[] {
  const rec = useSlice((s) => s.machines);
  return useMemo(
    () =>
      Object.values(rec).sort((a, b) => {
        if (a.id === 'local') return -1;
        if (b.id === 'local') return 1;
        return a.name.localeCompare(b.name);
      }),
    [rec],
  );
}

export function useProject(id: string | undefined): Project | undefined {
  return useSlice((s) => (id ? s.projects[id] : undefined));
}

export function useProjectsReady(): boolean {
  return useSlice((s) => s.projectsReady);
}

/** Projects, most recently opened first (API order). */
export function useProjects(): Project[] {
  const rec = useSlice((s) => s.projects);
  return useMemo(
    () =>
      Object.values(rec).sort(
        (a, b) => (b.lastOpenedAt || '').localeCompare(a.lastOpenedAt || '') || a.name.localeCompare(b.name),
      ),
    [rec],
  );
}

export function useProjectGroups(): ProjectGroup[] {
  const rec = useSlice((s) => s.projectGroups);
  return useMemo(() => Object.values(rec), [rec]);
}

/** Sessions that are currently running (live chip). */
export function useRunningSessions(): Session[] {
  const rec = useSlice((s) => s.sessions);
  return useMemo(
    // the editor's own terminals are the user's shells; an agent there counts
    () => Object.values(rec).filter((s) => (!s.panel || s.agentKind === 'agent') && (s.status === 'running' || s.status === 'starting')).sort(byCreation),
    [rec],
  );
}

export function useSettings(): Settings | null {
  return useSlice((s) => s.settings);
}

export function useAgentsRev(): number {
  return useSlice((s) => s.agentsRev);
}

export function useProfile(id: string | undefined): Profile | undefined {
  return useSlice((s) => (id ? s.profiles[id] : undefined));
}

export function useProfiles(): Profile[] {
  const rec = useSlice((s) => s.profiles);
  return useMemo(() => Object.values(rec).sort((a, b) => a.name.localeCompare(b.name)), [rec]);
}

/** Sessions waiting for the user, oldest needsYouSince first (chip + N). */
export function useNeedsYou(): Session[] {
  const rec = useSlice((s) => s.sessions);
  return useMemo(() => needsYouList(Object.values(rec)), [rec]);
}

export function needsYouList(list: Session[]): Session[] {
  return list
    .filter((s) => s.agentState === 'needsYou')
    .sort((a, b) => (a.needsYouSince ?? '').localeCompare(b.needsYouSince ?? '') || byCreation(a, b));
}

export function useDevices(): Device[] {
  const rec = useSlice((s) => s.devices);
  return useMemo(
    () => Object.values(rec).sort((a, b) => b.createdAt.localeCompare(a.createdAt)),
    [rec],
  );
}
