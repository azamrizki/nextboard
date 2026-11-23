// The database browser's shapes (docs/API-db.md), as the hub sends them.

export type Driver = 'mysql' | 'mariadb' | 'postgres' | 'redis' | 'valkey';
export type Env = 'local' | 'development' | 'staging' | 'production';
export type ReachKind = 'direct' | 'machine' | 'ssh';
export type HopAuth = 'password' | 'key' | 'agent';
export type AgentAccess = 'none' | 'read' | 'ask' | 'write';
export type TLSMode = 'off' | 'preferred' | 'required' | 'verify-ca';

export interface Hop {
  host: string;
  port: number;
  user: string;
  auth: HopAuth;
  keyPath?: string;
  hasKey?: boolean;
  hasSecret?: boolean;
  hostKey?: string;
}

export interface Reach {
  kind: ReachKind;
  machineId?: string;
  configHost?: string;
  hops?: Hop[];
  remoteHost?: string;
  remotePort?: number;
  openLocal?: boolean;
  localPort?: number;
  keepAliveSec?: number;
  timeoutSec?: number;
  reconnect?: boolean;
}

export interface Security {
  readOnly: boolean;
  confirmWrites: boolean;
  confirmEdits: boolean;
  autoCommit: boolean;
}

export interface Agents {
  access: AgentAccess;
  scope: 'project' | 'wired';
  masked: string[];
  askBar: boolean;
  editorAi: boolean;
}

export interface Connection {
  id: string;
  projectId: string;
  name: string;
  description?: string;
  group?: string;
  driver: Driver;
  env: Env;
  host: string;
  port: number;
  database?: string;
  user?: string;
  showAll?: boolean;
  serverTz?: boolean;
  hasPassword: boolean;
  savePassword: boolean;
  tls: TLSMode;
  props?: Record<string, string>;
  redisDb?: number;
  reach: Reach;
  security: Security;
  agents: Agents;
  limits: { timeoutSec: number; pageSize: number };
  createdAt?: string;
  updatedAt?: string;
}

export interface Status {
  connected: boolean;
  version?: string;
  since?: string;
  tunnel: boolean;
  localPort?: number;
  passwordNeeded?: boolean;
  error?: string;
}

export interface Secrets {
  password?: string;
  hopSecrets?: (string | null)[];
  hopKeys?: (string | null)[];
}

export interface HopResult {
  host: string;
  ms: number;
  error?: string;
}

export interface HostKeyInfo {
  hop: number;
  host: string;
  fingerprint: string;
  keyType: string;
  changed: boolean;
}

export interface TestResult {
  ok: boolean;
  version?: string;
  ms: number;
  hops?: HopResult[];
  tables: number;
  keys: number;
  error?: string;
  hostKey?: HostKeyInfo;
}

export interface Candidate {
  source: string;
  part?: string;
  connection: Connection;
  hasPassword: boolean;
  ref: string;
}

export interface DbObject {
  name: string;
  type: string;
  rows: number;
  bytes?: number;
}

export interface Schema {
  databases: string[] | null;
  current: string;
  tables: DbObject[] | null;
  views: DbObject[] | null;
  routines: DbObject[] | null;
}

export interface KVInfo {
  version: string;
  db: number;
  dbs: Record<string, number>;
  memory: string;
  clients: number;
  mode?: string;
  slowLog: number;
}

export interface Column {
  name: string;
  type: string;
  nullable: boolean;
  default?: string;
  key?: string;
  extra?: string;
  comment?: string;
  values?: string[];
}

export interface Index {
  name: string;
  columns: string[];
  unique: boolean;
  primary: boolean;
  kind?: string;
}

export interface ForeignKey {
  name: string;
  columns: string[];
  refSchema: string;
  refTable: string;
  refColumns: string[];
}

export interface TableInfo {
  schema: string;
  name: string;
  columns: Column[];
  indexes: Index[] | null;
  foreignKeys: ForeignKey[] | null;
  referencedBy: ForeignKey[] | null;
  primaryKey: string[] | null;
  ddl: string;
  rows: number;
}

/** A cell as the hub sends it: a plain value, or a cut text / binary. */
export type Cell = null | string | number | boolean | { $cut: string; len: number } | { $bin: string; len: number };

export interface ResultColumn {
  name: string;
  type: string;
}

export interface Result {
  sql: string;
  kind: 'read' | 'write' | 'ddl' | 'tx' | 'session';
  columns?: ResultColumn[];
  rows?: Cell[][];
  truncated?: boolean;
  affected: number;
  ms: number;
  error?: string;
  pending?: boolean;
  notice?: string;
}

export interface Filter {
  column: string;
  op: string;
  value?: string;
  values?: string[];
}

export interface Sort {
  column: string;
  desc?: boolean;
}

export interface RowsPage {
  columns: ResultColumn[];
  rows: Cell[][];
  total: number;
  ms: number;
  sql: string;
}

export interface Change {
  op: 'update' | 'insert' | 'delete';
  key?: Record<string, unknown>;
  set?: Record<string, unknown>;
}

export interface Stmt {
  sql: string;
  kind: string;
  verb: string;
  danger?: string;
  table?: string;
}

export interface KeyItem {
  name: string;
  type: string;
  ttl: number;
}

export interface KeyPage {
  keys: KeyItem[];
  cursor: number;
  total: number;
}

export interface KeyValue {
  name: string;
  type: string;
  ttl: number;
  size: number;
  encoding: string;
  length: number;
  value?: Cell;
  items?: Cell[][];
  entries?: { id: string; fields: Record<string, Cell> }[];
  next?: string;
}

export interface SavedQuery {
  id: string;
  projectId: string;
  connectionId?: string;
  name: string;
  sql: string;
  updatedAt: string;
}

export interface HistoryItem {
  at: string;
  sql: string;
  kind: string;
  rows: number;
  ms: number;
  error?: string;
  who?: string;
}

export interface Activity {
  at: string;
  who: string;
  sql: string;
  rows: number;
  what: string;
  note?: string;
  error?: string;
}

export interface DbRequest {
  id: string;
  state: 'waiting' | 'running' | 'done' | 'denied' | 'expired' | 'failed';
  connectionId: string;
  connection: string;
  driver: Driver;
  env: Env;
  projectId: string;
  workspaceId?: string;
  sessionId?: string;
  who: string;
  sql: string;
  why: string;
  danger?: string;
  rows: number;
  redis?: boolean;
  createdAt: string;
  expiresAt: string;
  result?: string;
  isError?: boolean;
}

export const isSQL = (d: Driver) => d === 'mysql' || d === 'mariadb' || d === 'postgres';
export const isKV = (d: Driver) => d === 'redis' || d === 'valkey';

export const DRIVERS: { id: Driver; label: string; mark: string; sub: string; port: number }[] = [
  { id: 'mysql', label: 'MySQL', mark: 'My', sub: '5.7, 8.x, 9.x', port: 3306 },
  { id: 'mariadb', label: 'MariaDB', mark: 'Ma', sub: '10.x, 11.x', port: 3306 },
  { id: 'postgres', label: 'PostgreSQL', mark: 'Pg', sub: '9.6 and newer', port: 5432 },
  { id: 'redis', label: 'Redis', mark: 'Rd', sub: '6, 7, 8', port: 6379 },
  { id: 'valkey', label: 'Valkey', mark: 'Vk', sub: '7, 8', port: 6379 },
];

export const LATER_DRIVERS = [
  { mark: 'Sq', label: 'SQLite', sub: 'a file on a machine' },
  { mark: 'Ms', label: 'SQL Server', sub: '2017 and newer' },
  { mark: 'Mo', label: 'MongoDB', sub: 'documents' },
  { mark: 'Ch', label: 'ClickHouse', sub: 'analytics' },
  { mark: 'Or', label: 'Oracle', sub: '' },
];

export const ENVS: { id: Env; label: string }[] = [
  { id: 'local', label: 'Local' },
  { id: 'development', label: 'Development' },
  { id: 'staging', label: 'Staging' },
  { id: 'production', label: 'Production' },
];

export const driverOf = (d: Driver) => DRIVERS.find((x) => x.id === d) ?? DRIVERS[0];

/** A connection type's safety defaults (as the hub's). */
export function defaultSecurity(e: Env): Security {
  if (e === 'production') return { readOnly: true, confirmWrites: true, confirmEdits: true, autoCommit: false };
  if (e === 'staging') return { readOnly: false, confirmWrites: true, confirmEdits: true, autoCommit: false };
  return { readOnly: false, confirmWrites: false, confirmEdits: true, autoCommit: true };
}

export function blankConnection(projectId: string, driver: Driver = 'mysql'): Connection {
  return {
    id: '',
    projectId,
    name: '',
    driver,
    env: 'local',
    host: '127.0.0.1',
    port: driverOf(driver).port,
    database: '',
    user: '',
    hasPassword: false,
    savePassword: true,
    tls: 'off',
    reach: { kind: 'direct', keepAliveSec: 30, timeoutSec: 15, reconnect: true },
    security: defaultSecurity('local'),
    agents: { access: 'ask', scope: 'project', masked: ['*password*', '*token*', '*secret*', '*api_key*'], askBar: true, editorAi: true },
    limits: { timeoutSec: 60, pageSize: 100 },
  };
}
