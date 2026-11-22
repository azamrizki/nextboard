// Live state fed by /api/events (sessions, machines, devices, projects) plus helpers so
// REST responses can be applied immediately. Plain external store + useSyncExternalStore.
// Board ops ("boardOps") are not stored here: boards subscribe via onEvent().

import { useMemo, useSyncExternalStore } from 'react';
import { api, openEventsSocket, type ReconnectingSocket } from './api';
import type { Device, HubEvent, Machine, Profile, Project, ProjectBadge, ProjectGroup, Session, SessionStatusFrame, Settings } from './lib/types';

export interface LiveState {
  sessions: Record<string, Session>;
  machines: Record<string, Machine>;
  devices: Record<string, Device>;
  projects: Record<string, Project>;
  projectGroups: Record<string, ProjectGroup>;
  projectBadges: Record<string, ProjectBadge>;
  /** Projects have been received at least once (snapshot or REST). */
  projectsReady: boolean;
  /** Archived projects (API-board §1): kept apart, so that no list or picker of the active ones shows them. */
  archivedProjects: Record<string, Project>;
  archivedProjectsReady: boolean;
  /** Archived machines (docs/API.md Machines), apart from the active ones for the same reason. */
  archivedMachines: Record<string, Machine>;
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
  projectBadges: {},
  projectsReady: false,
  archivedProjects: {},
  archivedProjectsReady: false,
  archivedMachines: {},
  ready: false,
  connected: false,
  settings: null,
  agentsRev: 0,
  profiles: {},
  profilesReady: false,
};



/** Whether two records from the hub hold the same values (nested ones compared as JSON), but for the key skip. */
export function sameRecord(a: object, b: object, skip?: string): boolean {
  const ka = Object.keys(a).filter((k) => k !== skip);
  if (ka.length !== Object.keys(b).filter((k) => k !== skip).length) return false;
  for (const k of ka) {
    const x = (a as Record<string, unknown>)[k];
    const y = (b as Record<string, unknown>)[k];
    if (x === y) continue;
    if (typeof x !== 'object' || typeof y !== 'object' || x === null || y === null) return false;
    if (JSON.stringify(x) !== JSON.stringify(y)) return false;
  }
  return true;
}

function revsOf(list: Session[] | null | undefined): Map<string, number> {
  const out = new Map<string, number>();
  for (const s of list ?? []) if (s.rev !== undefined) out.set(s.id, s.rev);
  return out;
}

function byId<T extends { id: string }>(list: T[] | null | undefined): Record<string, T> {
  const out: Record<string, T> = {};
  for (const item of list ?? []) out[item.id] = item;
  return out;
}

/** The active ones of a list from the hub, by id: an archived one is never among them. */
function activeById<T extends { id: string; archivedAt?: string }>(list: T[] | null | undefined): Record<string, T> {
  return byId((list ?? []).filter((x) => !x.archivedAt));
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
  /** The newest Session.rev the hub gave of each console. */
  private revs = new Map<string, number>();

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
        this.revs = revsOf(ev.sessions);
        this.set({
          sessions: byId(ev.sessions),
          machines: activeById(ev.machines),
          devices: byId(ev.devices),
          ...(ev.projects !== undefined ? { projects: activeById(ev.projects), projectsReady: true } : {}),
          ...(ev.projectGroups !== undefined ? { projectGroups: byId(ev.projectGroups) } : {}),
          ...(ev.projectBadges !== undefined ? { projectBadges: byId(ev.projectBadges) } : {}),
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
      case 'projectBadges':
        this.set({ projectBadges: byId(ev.badges) });
        break;
      case 'projects':
        void this.refreshProjects();
        break;
      case 'machines':
        void this.refreshMachines();
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
    this.revs = revsOf(list);
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
    this.set({ machines: activeById(list) });
  }
  seedDevices(list: Device[]) {
    this.set({ devices: byId(list) });
  }

  upsertSession(s: Session) {
    if (s.rev !== undefined) {
      // a reply can come after an event about a later change (a NextCode console is ready
      // before the reply that made it goes out): the older record would undo it
      if (s.rev < (this.revs.get(s.id) ?? 0)) return;
      this.revs.set(s.id, s.rev);
    }
    // A change comes twice, as the console socket's status frame and as the hub's session event:
    // the second one changes nothing and re-renders nothing.
    const cur = this.state.sessions[s.id];
    if (cur && sameRecord(cur, s, 'rev')) return;
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
    // live, so newer than any record: not held to Session.rev as a reply is
    this.set({ sessions: { ...this.state.sessions, [id]: next } });
  }
  removeSession(id: string) {
    const next = without(this.state.sessions, id);
    if (next !== this.state.sessions) this.set({ sessions: next });
  }
  /** An archived one moves to the archived list, a restored one back. */
  upsertMachine(m: Machine) {
    if (m.archivedAt) {
      this.set({ machines: without(this.state.machines, m.id), archivedMachines: { ...this.state.archivedMachines, [m.id]: m } });
      return;
    }
    this.set({ machines: { ...this.state.machines, [m.id]: m }, archivedMachines: without(this.state.archivedMachines, m.id) });
  }
  removeMachine(id: string) {
    const machines = without(this.state.machines, id);
    const archivedMachines = without(this.state.archivedMachines, id);
    if (machines !== this.state.machines || archivedMachines !== this.state.archivedMachines) this.set({ machines, archivedMachines });
  }
  seedArchivedMachines(list: Machine[]) {
    this.set({ archivedMachines: byId(list.filter((m) => m.archivedAt)) });
  }
  seedProjects(list: Project[]) {
    this.set({ projects: activeById(list), projectsReady: true });
  }
  seedArchivedProjects(list: Project[]) {
    this.set({ archivedProjects: byId(list.filter((p) => p.archivedAt)), archivedProjectsReady: true });
  }
  /** An archived one moves to the archived list, a restored one back. */
  upsertProject(p: Project) {
    // an event that repeats the project as it is would re-render every board tile showing it
    const [mine, other] = p.archivedAt ? [this.state.archivedProjects, this.state.projects] : [this.state.projects, this.state.archivedProjects];
    const cur = mine[p.id];
    if (cur && !(p.id in other) && sameRecord(cur, p)) return;
    if (p.archivedAt) {
      this.set({ projects: without(this.state.projects, p.id), archivedProjects: { ...this.state.archivedProjects, [p.id]: p } });
      return;
    }
    this.set({ projects: { ...this.state.projects, [p.id]: p }, archivedProjects: without(this.state.archivedProjects, p.id) });
  }
  /**
   * A change of the project as it is in the store at the time of writing, not as it was when a
   * click started a request: an event may have brought a newer one meanwhile.
   */
  patchProject(id: string, change: (p: Project) => Project) {
    const p = this.state.projects[id];
    if (p) this.upsertProject(change(p));
  }
  /** Gone for good (a permanent delete, a merge): from both lists. */
  removeProject(id: string) {
    const projects = without(this.state.projects, id);
    const archivedProjects = without(this.state.archivedProjects, id);
    if (projects !== this.state.projects || archivedProjects !== this.state.archivedProjects) this.set({ projects, archivedProjects });
  }

  private refreshing: Partial<Record<'projects' | 'machines', Promise<void>>> = {};

  /** Both project lists again, after a bulk change. Calls that come while one runs share it. */
  refreshProjects(): Promise<void> {
    this.refreshing.projects ??= Promise.all([api.projects(), api.archivedProjects()])
      .then(([active, archived]) => {
        this.seedProjects(active);
        this.seedArchivedProjects(archived);
      })
      .catch(() => undefined)
      .finally(() => {
        delete this.refreshing.projects;
      });
    return this.refreshing.projects;
  }

  /** Both machine lists again, after a bulk change. */
  refreshMachines(): Promise<void> {
    this.refreshing.machines ??= Promise.all([api.machines(), api.archivedMachines()])
      .then(([active, archived]) => {
        this.seedMachines(active);
        this.seedArchivedMachines(archived);
      })
      .catch(() => undefined)
      .finally(() => {
        delete this.refreshing.machines;
      });
    return this.refreshing.machines;
  }
  upsertProjectGroup(g: ProjectGroup) {
    this.set({ projectGroups: { ...this.state.projectGroups, [g.id]: g } });
  }
  upsertProjectBadge(b: ProjectBadge) {
    this.set({ projectBadges: { ...this.state.projectBadges, [b.id]: b } });
  }
  removeProjectBadge(id: string) {
    const next = without(this.state.projectBadges, id);
    if (next !== this.state.projectBadges) this.set({ projectBadges: next });
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

/** A session's machine as it is named now: Session.machineName is a copy a rename never refreshes. */
export function machineNameOf(s: Pick<Session, 'machineId' | 'machineName'>, st: Pick<LiveState, 'machines' | 'archivedMachines'> = store.getState()): string {
  const m = st.machines[s.machineId] ?? st.archivedMachines[s.machineId];
  // This PC under its default name: the hub names its consoles' machine after the hub instead
  if (!m || (m.id === 'local' && m.name === 'This PC')) return s.machineName;
  return m.name;
}

export function useMachineName(s: Pick<Session, 'machineId' | 'machineName'>): string {
  return useSlice((st) => machineNameOf(s, st));
}

/** machineNameOf for a list of sessions, drawn again when a machine is renamed. */
export function useMachineNameOf(): (s: Pick<Session, 'machineId' | 'machineName'>) => string {
  const machines = useSlice((st) => st.machines);
  const archivedMachines = useSlice((st) => st.archivedMachines);
  return useMemo(() => (s) => machineNameOf(s, { machines, archivedMachines }), [machines, archivedMachines]);
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
  const key = useSlice((s) => sessionIdsKey(s.sessions));
  return useMemo(() => (key ? key.split('\n') : []), [key]);
}

// one sort per sessions record, not one per subscriber on every notify
let idsKeyOf: Record<string, Session> | null = null;
let idsKey = '';
function sessionIdsKey(rec: Record<string, Session>): string {
  if (rec !== idsKeyOf) {
    idsKey = Object.values(rec)
      .sort(byCreation)
      .map((x) => x.id)
      .join('\n');
    idsKeyOf = rec;
  }
  return idsKey;
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

/** Archived projects, archived last first. */
export function useArchivedProjects(): Project[] {
  const rec = useSlice((s) => s.archivedProjects);
  return useMemo(() => Object.values(rec).sort((a, b) => (b.archivedAt || '').localeCompare(a.archivedAt || '') || a.name.localeCompare(b.name)), [rec]);
}

export function useArchivedProjectsReady(): boolean {
  return useSlice((s) => s.archivedProjectsReady);
}

/** Archived machines, by name. */
export function useArchivedMachines(): Machine[] {
  const rec = useSlice((s) => s.archivedMachines);
  return useMemo(() => Object.values(rec).sort((a, b) => a.name.localeCompare(b.name)), [rec]);
}

/** The badges, A to Z. */
export function useProjectBadges(): ProjectBadge[] {
  const rec = useSlice((s) => s.projectBadges);
  return useMemo(() => Object.values(rec).sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })), [rec]);
}

/** The badge a project carries, or undefined. */
export function useBadgeOf(p: { badgeId?: string }): ProjectBadge | undefined {
  // the cover also renders to static markup (its tests), which needs a server snapshot
  const get = () => (p.badgeId ? store.getState().projectBadges[p.badgeId] : undefined);
  return useSyncExternalStore(store.subscribe, get, get);
}

export function useProjectGroups(): ProjectGroup[] {
  const rec = useSlice((s) => s.projectGroups);
  return useMemo(() => Object.values(rec), [rec]);
}

// the editor's own terminals are the user's shells; an agent there counts
const isRunning = (s: Session) => (!s.panel || s.agentKind === 'agent') && (s.status === 'running' || s.status === 'starting');

/** Sessions that are currently running (live chip). */
export function useRunningSessions(): Session[] {
  const rec = useSlice((s) => s.sessions);
  return useMemo(() => Object.values(rec).filter(isRunning).sort(byCreation), [rec]);
}

/**
 * How many running sessions `keep` takes. A number, so that a busy agent's ~1/s session events
 * re-render a count only when it changes.
 */
export function useRunningCount(keep: (s: Session) => boolean = () => true): number {
  return useSlice((s) => {
    let n = 0;
    for (const x of Object.values(s.sessions)) if (isRunning(x) && keep(x)) n++;
    return n;
  });
}

/** Agents whose process the hub ended to free memory (ROADMAP #50). */
export function useAsleepCount(): number {
  const rec = useSlice((s) => s.sessions);
  return useMemo(() => Object.values(rec).filter((s) => s.status === 'asleep').length, [rec]);
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

/**
 * How many sessions of a workspace wait for the user. A number, so that a status change
 * elsewhere does not re-render the whole board that shows it.
 */
export function useNeedsYouIn(workspaceId: string): number {
  return useSlice((s) => {
    let n = 0;
    for (const x of Object.values(s.sessions)) if (x.workspaceId === workspaceId && x.agentState === 'needsYou') n++;
    return n;
  });
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
