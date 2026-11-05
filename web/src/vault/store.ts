// The open vault of this tab. One per hub, shared by every Credentials card, and it never leaves
// memory: no localStorage, no sessionStorage, nothing a reload would find. A reload locks it.

import { useEffect, useSyncExternalStore } from 'react';
import { ApiError, errorMessage } from '../api';
import { store as hubStore } from '../store';
import { vaultApi, type VaultBlob, type VaultHeader } from './api';
import { copyText } from '../lib/clipboard';
import { vaultSupported, createKey, keyFromPinSecret, open, openKey, proofForPin, randomId, rewrapKey, seal, wrapKeyForPin, type AeadKey, type PinRecord } from './crypto';
import type { Imported } from './importExport';
import { readRecord, TRASH_DAYS, type VaultFolder, type VaultItem, type VaultRecord } from './model';

export type VaultStatus = 'loading' | 'unsupported' | 'absent' | 'locked' | 'unlocked' | 'error';

export interface VaultState {
  status: VaultStatus;
  error: string;
  items: VaultItem[];
  folders: VaultFolder[];
  /** Blobs that would not open: damaged, or written with another key. Never hidden, never overwritten. */
  broken: number;
}

const CLOSED = { error: '', items: [], folders: [], broken: 0 };

let state: VaultState = { status: 'loading', ...CLOSED };
const listeners = new Set<() => void>();

function set(patch: Partial<VaultState>) {
  state = { ...state, ...patch };
  listeners.forEach((fn) => fn());
}

const subscribe = (fn: () => void) => {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
};

// Secrets live in these two and nowhere else.
let key: AeadKey | null = null;
let records = new Map<string, VaultRecord>();
const brokenIds = new Set<string>();

let header: VaultHeader | null = null;
/** Bumped when the vault closes: an answer that started before that is dropped. */
let generation = 0;

// ---- preferences (of this browser, and nothing secret) ----

export interface VaultPrefs {
  /** Lock after this many minutes without touching a Credentials card. */
  lockAfterMin: number;
  /** Empty the clipboard this many seconds after copying a secret; 0 = leave it. */
  clipboardSec: number;
}

const PREFS_KEY = 'nb.vault.prefs';
const DEFAULT_PREFS: VaultPrefs = { lockAfterMin: 15, clipboardSec: 30 };

function loadPrefs(): VaultPrefs {
  try {
    const raw = JSON.parse(localStorage.getItem(PREFS_KEY) ?? '{}') as Partial<VaultPrefs>;
    return {
      lockAfterMin: typeof raw.lockAfterMin === 'number' && raw.lockAfterMin > 0 ? raw.lockAfterMin : DEFAULT_PREFS.lockAfterMin,
      clipboardSec: typeof raw.clipboardSec === 'number' && raw.clipboardSec >= 0 ? raw.clipboardSec : DEFAULT_PREFS.clipboardSec,
    };
  } catch {
    return DEFAULT_PREFS;
  }
}

let prefs = loadPrefs();
const prefListeners = new Set<() => void>();

export function setVaultPrefs(patch: Partial<VaultPrefs>) {
  prefs = { ...prefs, ...patch };
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
  } catch {
    /* it still applies until the tab closes */
  }
  prefListeners.forEach((fn) => fn());
}

export function useVaultPrefs(): VaultPrefs {
  return useSyncExternalStore((fn) => {
    prefListeners.add(fn);
    return () => prefListeners.delete(fn);
  }, () => prefs);
}

export const vaultPrefs = () => prefs;

// ---- clipboard ----

let clipboardTimer = 0;
let clipboardArmed = false;
let cancelRetry: (() => void) | null = null;

/** navigator.clipboard exists only on HTTPS and localhost. */
const asyncClipboard = () => window.isSecureContext && !!navigator.clipboard;

/**
 * The old way, for plain HTTP. The browser allows it only within about 5 seconds of a click or key
 * press. A space and not an empty string, so that the secret is overwritten in every browser.
 */
function overwriteByCommand(): boolean {
  let done = false;
  const onCopy = (e: ClipboardEvent) => {
    done = true;
    e.clipboardData?.setData('text/plain', ' ');
    e.preventDefault();
    e.stopImmediatePropagation(); // not the board's Ctrl+C
  };
  window.addEventListener('copy', onCopy, true);
  try {
    document.execCommand('copy');
  } catch {
    done = false;
  }
  window.removeEventListener('copy', onCopy, true);
  return done;
}

/** Too long since the last input: the next click or key press is allowed to do it. */
function overwriteOnNextInput() {
  cancelRetry?.();
  const run = () => {
    cancelRetry?.();
    overwriteByCommand();
  };
  window.addEventListener('pointerup', run, true);
  window.addEventListener('keydown', run, true);
  cancelRetry = () => {
    window.removeEventListener('pointerup', run, true);
    window.removeEventListener('keydown', run, true);
    cancelRetry = null;
  };
}

async function emptyClipboard() {
  clipboardArmed = false;
  window.clearTimeout(clipboardTimer);
  if (!asyncClipboard()) {
    if (!overwriteByCommand()) overwriteOnNextInput();
    return;
  }
  try {
    await navigator.clipboard.writeText('');
  } catch {
    // Only a focused page may write: do it the moment this one has focus again.
    window.addEventListener('focus', () => void navigator.clipboard.writeText('').catch(() => undefined), { once: true });
  }
}

/**
 * Copy a secret, and take it off the clipboard again later. Must be called straight from the click:
 * on plain HTTP that is what lets the browser copy at all.
 */
export async function copySecret(value: string): Promise<{ ok: boolean; clearsIn: number; atNextInput: boolean }> {
  cancelRetry?.();
  window.clearTimeout(clipboardTimer);
  clipboardArmed = false;
  if (!(await copyText(value))) return { ok: false, clearsIn: 0, atNextInput: false };
  clipboardArmed = prefs.clipboardSec > 0;
  if (clipboardArmed) clipboardTimer = window.setTimeout(() => void emptyClipboard(), prefs.clipboardSec * 1000);
  return { ok: true, clearsIn: prefs.clipboardSec, atNextInput: !asyncClipboard() };
}

// ---- the idle lock ----

let lastActive = 0;
let idleTimer = 0;

/** Only work in a Credentials card counts: hours in a console must not keep the vault open. */
export function touchVault() {
  lastActive = Date.now();
}

function checkIdle() {
  if (key && Date.now() - lastActive > prefs.lockAfterMin * 60_000) lockVault();
}

function startIdleTimer() {
  touchVault();
  window.clearInterval(idleTimer);
  idleTimer = window.setInterval(checkIdle, 5000);
}

// ---- closing ----

function closeSession() {
  generation++;
  key?.wipe();
  key = null;
  records = new Map();
  brokenIds.clear();
  window.clearInterval(idleTimer);
  if (clipboardArmed) void emptyClipboard();
}

const lockChannel = typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel('nb.vault') : null;

/** Lock this tab; a lock asked for by hand locks the others too. */
export function lockVault(everywhere = false) {
  closeSession();
  set({ status: header ? 'locked' : 'loading', ...CLOSED });
  if (everywhere) lockChannel?.postMessage('lock');
}

/** Logged out, or the session ended. */
export function resetVaultStore() {
  closeSession();
  header = null;
  set({ status: 'loading', ...CLOSED });
}

// ---- loading ----

function publish() {
  const items: VaultItem[] = [];
  const folders: VaultFolder[] = [];
  for (const r of records.values()) {
    if (r.k === 'item') items.push(r);
    else folders.push(r);
  }
  set({ status: 'unlocked', error: '', items, folders, broken: brokenIds.size });
}

/** Open what changed since last time. */
async function merge(blobs: VaultBlob[]) {
  const k = key;
  const g = generation;
  if (!k) return;
  const next = new Map<string, VaultRecord>();
  const nextBroken = new Set<string>();
  for (const b of blobs) {
    const had = records.get(b.id);
    if (had?.rev === b.rev) {
      next.set(b.id, had);
      continue;
    }
    try {
      const rec = readRecord(await open<unknown>(k, b.id, b.blob), b.id, b.rev);
      if (rec) next.set(b.id, rec);
      else nextBroken.add(b.id);
    } catch {
      nextBroken.add(b.id);
    }
    if (g !== generation) return;
  }
  records = next;
  brokenIds.clear();
  nextBroken.forEach((id) => brokenIds.add(id));
  publish();
}

async function refreshOnce() {
  const g = generation;
  let view;
  try {
    view = await vaultApi.get();
  } catch (err) {
    if (g !== generation) return;
    if (state.status === 'unlocked') return; // a hiccup: what is open stays open
    closeSession();
    set({ status: 'error', ...CLOSED, error: errorMessage(err) });
    return;
  }
  if (g !== generation) return;
  if (!view.exists || !view.header) {
    closeSession();
    header = null;
    set({ status: 'absent', ...CLOSED });
    return;
  }
  // Reset somewhere else: this key belongs to a vault that is gone.
  if (key && header && header.id !== view.header.id) closeSession();
  header = view.header;
  if (!key) {
    set({ status: 'locked', ...CLOSED });
    return;
  }
  await merge(view.items ?? []);
}

let running: Promise<void> | null = null;
let again = false;

/** One at a time, and one more if something changed meanwhile. */
function refresh(): Promise<void> {
  if (running) {
    again = true;
    return running;
  }
  running = (async () => {
    do {
      again = false;
      await refreshOnce();
    } while (again);
  })().finally(() => {
    running = null;
  });
  return running;
}

let started = false;

function start() {
  if (!vaultSupported()) {
    set({ status: 'unsupported', ...CLOSED });
    return;
  }
  if (started) {
    if (state.status === 'loading') void refresh();
    return;
  }
  started = true;
  hubStore.onEvent((ev) => {
    if (ev.type === 'vaultChanged' || ev.type === 'snapshot') void refresh();
  });
  lockChannel?.addEventListener('message', () => lockVault());
  // Not even a page restored from the back/forward cache should come back open.
  window.addEventListener('pagehide', () => {
    if (key) lockVault();
  });
  document.addEventListener('visibilitychange', checkIdle);
  void refresh();
}

/** Try again after an error. */
export const retryVault = () => refresh();

export function useVault(): VaultState {
  useEffect(start, []);
  return useSyncExternalStore(subscribe, () => state);
}

// ---- opening ----

export async function createVault(password: string) {
  const made = await createKey(password);
  header = await vaultApi.init(made.material);
  generation++;
  key = made.key;
  records = new Map();
  brokenIds.clear();
  startIdleTimer();
  publish();
}

async function adoptKey(opened: AeadKey) {
  generation++;
  key?.wipe();
  key = opened;
  startIdleTimer();
  await refresh();
  if (state.status === 'unlocked') void purgeTrash();
}

export async function unlockVault(password: string) {
  if (!header) await refresh();
  if (!header) throw new Error('There is no vault to open.');
  await adoptKey(await openKey(password, header));
}

// ---- PIN ----

export const PIN_LENGTH = 6;
/** Told to the person; the hub is what counts them (internal/vault/pin.go). */
export const MAX_PIN_TRIES = 5;
const PIN_KEY = 'nb.vault.pin';

export class WrongPin extends Error {
  readonly left: number;
  constructor(left: number) {
    super('Wrong PIN.');
    this.left = left;
  }
}

export class PinGone extends Error {
  constructor() {
    super('The PIN no longer works.');
  }
}

/** This browser's half of its PIN. Holds no PIN, and is useless without the hub. */
function readPin(): PinRecord | null {
  try {
    const rec = JSON.parse(localStorage.getItem(PIN_KEY) ?? 'null') as PinRecord | null;
    return rec && header && rec.vaultId === header.id && rec.pinId && rec.salt && rec.wrappedKey ? rec : null;
  } catch {
    return null;
  }
}

function forgetPin() {
  try {
    localStorage.removeItem(PIN_KEY);
  } catch {
    /* nothing to forget then */
  }
}

/** This browser can open the vault with a PIN. */
export const pinEnabled = () => !!readPin();

/** Too simple to survive five guesses. */
export function pinProblem(pin: string): string {
  if (!new RegExp(`^\\d{${PIN_LENGTH}}$`).test(pin)) return `The PIN is ${PIN_LENGTH} digits.`;
  const steps = [...pin].slice(1).map((c, i) => Number(c) - Number(pin[i]));
  const simple = steps.every((s) => s === 0) || steps.every((s) => s === 1) || steps.every((s) => s === -1) || pin.slice(0, 2).repeat(3) === pin || pin.slice(0, 3).repeat(2) === pin;
  return simple ? 'Too easy to guess. Avoid repeats and runs like 123456.' : '';
}

export async function setupPin(master: string, pin: string) {
  if (!header) throw new Error('There is no vault.');
  const old = readPin();
  const rec = await wrapKeyForPin(master, pin, header, (proof) => vaultApi.pinCreate(proof));
  localStorage.setItem(PIN_KEY, JSON.stringify({ ...rec, vaultId: header.id }));
  if (old) void vaultApi.pinRemove(old.pinId).catch(() => undefined);
}

export async function removePin() {
  const rec = readPin();
  forgetPin();
  if (rec) await vaultApi.pinRemove(rec.pinId);
}

/** For a browser that was lost: its half alone opens nothing once the hub's half is gone. */
export async function removeAllPins() {
  forgetPin();
  await vaultApi.pinsClear();
}

export async function unlockWithPin(pin: string) {
  const rec = readPin();
  if (!rec) throw new PinGone();
  const proof = await proofForPin(pin, rec);
  let secret: string;
  try {
    ({ secret } = await vaultApi.pinOpen(rec.pinId, proof));
  } catch (err) {
    if (err instanceof ApiError && err.code === 'wrong_pin') throw new WrongPin(Number(err.body.left) || 0);
    if (err instanceof ApiError && err.code === 'pin_gone') {
      forgetPin();
      throw new PinGone();
    }
    throw err;
  }
  let opened: AeadKey;
  try {
    opened = await keyFromPinSecret(secret, rec);
  } catch {
    // The right PIN and a record that does not open: nothing can be done with this one any more.
    forgetPin();
    void vaultApi.pinRemove(rec.pinId).catch(() => undefined);
    throw new PinGone();
  }
  await adoptKey(opened);
}

/** The password is right, without changing anything. For what deserves asking again. */
export async function checkPassword(password: string) {
  if (!header) throw new Error('There is no vault.');
  (await openKey(password, header)).wipe();
}

export async function changeMasterPassword(current: string, next: string) {
  if (!header) throw new Error('There is no vault.');
  const material = await rewrapKey(current, next, header);
  header = await vaultApi.setKey(material, header.keyRev);
  forgetPin(); // the hub turned every PIN off with it
}

export async function resetVault(): Promise<string> {
  const { kept } = await vaultApi.reset();
  forgetPin();
  closeSession();
  header = null;
  await refresh();
  return kept;
}

// ---- changing ----

const needKey = (): AeadKey => {
  if (!key) throw new Error('The vault is locked.');
  return key;
};

/** What goes in the blob: the revision belongs to the hub. */
function sealRecord(k: AeadKey, rec: VaultRecord) {
  const { rev: _rev, id: _id, ...plain } = rec;
  return seal(k, rec.id, plain);
}

async function writeRecord<T extends VaultRecord>(rec: T, baseRev: number): Promise<T> {
  const g = generation;
  const blob = await sealRecord(needKey(), rec);
  const saved = await vaultApi.put(rec.id, blob, baseRev);
  const out = { ...rec, rev: saved.rev };
  // Locked while it was on its way: it is saved, and nothing comes back on screen.
  if (g === generation) {
    records.set(rec.id, out);
    publish();
  }
  return out;
}

export const isConflict = (err: unknown) => err instanceof ApiError && err.code === 'conflict';

/**
 * Save an item. A conflict means it changed elsewhere: the caller's edit is still in its form, and
 * force writes it over the newer one once the person has said so.
 */
export async function saveItem(item: VaultItem, opts: { force?: boolean } = {}): Promise<VaultItem> {
  const id = item.id || randomId();
  const stamped: VaultItem = { ...item, id, updatedAt: new Date().toISOString() };
  try {
    return await writeRecord(stamped, opts.force ? (records.get(id)?.rev ?? 0) : item.rev);
  } catch (err) {
    if (isConflict(err)) await refresh();
    throw err;
  }
}

export const toggleFavorite = (item: VaultItem) => saveItem({ ...item, favorite: !item.favorite });
export const trashItem = (item: VaultItem) => saveItem({ ...item, deletedAt: new Date().toISOString() });
export const restoreItem = (item: VaultItem) => saveItem({ ...item, deletedAt: undefined });

export async function deleteRecord(rec: VaultRecord) {
  needKey();
  const g = generation;
  await vaultApi.remove(rec.id, rec.rev);
  if (g === generation) {
    records.delete(rec.id);
    publish();
  }
}

export async function emptyTrash() {
  for (const r of [...records.values()]) if (r.k === 'item' && r.deletedAt) await deleteRecord(r);
}

/** Past the 30 days, as in Bitwarden. Failing is fine: it is tried at every unlock. */
async function purgeTrash() {
  const limit = Date.now() - TRASH_DAYS * 86_400_000;
  try {
    for (const r of [...records.values()]) {
      if (r.k === 'item' && r.deletedAt && new Date(r.deletedAt).getTime() < limit) await deleteRecord(r);
    }
  } catch (err) {
    console.warn('vault: could not empty old trash', errorMessage(err));
  }
}

export async function saveFolder(name: string, folder?: VaultFolder): Promise<VaultFolder> {
  const rec: VaultFolder = folder ? { ...folder, name } : { k: 'folder', id: randomId(), rev: 0, name };
  return writeRecord(rec, rec.rev);
}

/** The items in it stay, without a folder. */
export async function deleteFolder(folder: VaultFolder) {
  for (const r of [...records.values()]) {
    if (r.k === 'item' && r.folderId === folder.id) await saveItem({ ...r, folderId: '' });
  }
  await deleteRecord(folder);
}

const BATCH_ITEMS = 500;
const BATCH_CHARS = 8 << 20;

/** Encrypt an import here and send it in pieces. The new folders get ids of their own. */
export async function importRecords(data: Imported): Promise<number> {
  const k = needKey();
  const folderIds = new Map<string, string>();
  const recs: VaultRecord[] = [];
  const existing = [...records.values()].filter((r): r is VaultFolder => r.k === 'folder');
  for (const f of data.folders) {
    const same = existing.find((x) => x.name === f.name);
    if (same) {
      folderIds.set(f.ref, same.id);
      continue;
    }
    const id = randomId();
    folderIds.set(f.ref, id);
    recs.push({ k: 'folder', id, rev: 0, name: f.name });
  }
  const count = data.items.length;
  for (const { item, folderRef } of data.items) recs.push({ ...item, id: randomId(), rev: 0, folderId: folderIds.get(folderRef) ?? '' });

  let batch: { id: string; blob: string }[] = [];
  let chars = 0;
  const flush = async () => {
    if (batch.length) await vaultApi.batch(batch);
    batch = [];
    chars = 0;
  };
  try {
    for (const rec of recs) {
      const blob = await sealRecord(k, rec);
      if (batch.length >= BATCH_ITEMS || chars + blob.length > BATCH_CHARS) await flush();
      batch.push({ id: rec.id, blob });
      chars += blob.length;
    }
    await flush();
  } finally {
    await refresh();
  }
  return count;
}
