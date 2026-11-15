// The open files of the browser tab (v0.9.0-beta.2, docs/API-studio.md §7): one Doc and one
// Monaco model per FileKey, whatever editor card shows it. Cards hold the files their tabs show;
// two cards on one file share its model, its unsaved edits and its dirty state. A model goes once
// no card holds it and no language server borrowed it lately (LENT_MS).
//
// Fs calls take splitKey(key), never a key. Drafts stay in IndexedDB by (machine, path), as before.

import { api, ApiError, errorMessage, fsRawUrl, isCode, type FsStat } from '../../api';
import { toast } from '../../components/Toast';
import { prefs } from '../../lib/format';
import { store } from '../../store';
import { filesApi, fsApi } from '../api';
import { drafts, type Draft } from '../drafts';
import type { DocHolder, DocSaveOptions, DocStoreApi, PartServiceApi } from './contracts';
import { hubOf } from './lsp';
import { languageFor, monaco } from './monaco';
import { cleanAbs, fileKey, keyOfUri, splitKey, uriPartsOf, type FileKey } from './paths';
import type { MdView } from './persist';

/** Files up to here are editable; larger ones are read-only up to MAX_VIEW. */
export const MAX_EDIT = 1 << 20;
export const MAX_VIEW = 10 << 20;
/**
 * A model made for a language server (peek, go to definition, rename) on a file no card holds is
 * let go once nothing asked for it this long and no editor shows it.
 */
const LENT_MS = 3 * 60_000;

const IMAGE_EXT = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'avif', 'bmp', 'svg', 'ico']);
const MEDIA_EXT = new Set(['mp4', 'webm', 'mov', 'mp3', 'wav', 'ogg', 'flac']);

/** Saving one of these can change what the Laravel index holds. */
const changesLaravelIndex = (rel: string) =>
  /(^|\/)(routes|config|lang)\/[^/]+\.php$/.test(rel) || /(^|\/)resources\/views\//.test(rel) || /(^|\/)app\/View\/Components\//.test(rel) || /(^|\/)\.env\.example$/.test(rel);

const extOf = (p: string) => {
  const base = p.slice(p.lastIndexOf('/') + 1);
  const i = base.lastIndexOf('.');
  return i > 0 ? base.slice(i + 1).toLowerCase() : '';
};

export interface Doc {
  /** The file's key (paths.ts): its machine and its absolute path. */
  path: FileKey;
  kind: 'text' | 'image' | 'media' | 'binary' | 'toolarge';
  status: 'loading' | 'ready' | 'error';
  error: string;
  model: monaco.editor.ITextModel | null;
  savedAlt: number;
  dirty: boolean;
  mtime: string;
  size: number;
  bom: boolean;
  /** Not UTF-8: decoded with this, read-only so that the bytes stay as they are. */
  encoding: string;
  /** Both LF and CRLF in the file: saving makes them all the same. */
  mixedEol: boolean;
  /**
   * Why the file itself cannot be edited ("" = it can): a preview, too large, binary, not UTF-8,
   * no SFTP. "Outside the project" is a card's reason: Workspace.readOnlyOf(doc).
   */
  readOnly: string;
  /** Changed on disk while there are unsaved edits, or a save was refused for it. */
  conflict: boolean;
  deleted: boolean;
  outside: boolean;
  saving: boolean;
  saveError: string;
  recovered: Draft | null;
  md: MdView;
}

const newDoc = (path: FileKey, md: MdView): Doc => ({
  path,
  kind: 'text',
  status: 'loading',
  error: '',
  model: null,
  savedAlt: 0,
  dirty: false,
  mtime: '',
  size: 0,
  bom: false,
  encoding: '',
  mixedEol: false,
  readOnly: '',
  conflict: false,
  deleted: false,
  outside: false,
  saving: false,
  saveError: '',
  recovered: null,
  md,
});

// ---- editor settings (one set per browser, every card) -----------------------------------------

export interface EditorPrefs {
  fontSize: number;
  tabSize: number;
  minimap: boolean;
  wordWrap: boolean;
  formatOnSave: boolean;
  autoSave: 'off' | 'afterDelay' | 'onFocusChange';
  stickyScroll: boolean;
  bracketColors: boolean;
  renderWhitespace: boolean;
  /** Ghost text from the runner: never, on Alt+\\, or while typing. */
  aiInline: 'off' | 'manual' | 'auto';
  /** Claude consoles linked to this editor see the selected text. */
  shareSelection: boolean;
}

export const DEFAULT_EDITOR_PREFS: EditorPrefs = {
  fontSize: 13,
  tabSize: 2,
  minimap: true,
  wordWrap: false,
  formatOnSave: false,
  autoSave: 'off',
  stickyScroll: true,
  bracketColors: true,
  renderWhitespace: false,
  aiInline: 'off',
  shareSelection: true,
};

function readPrefs(): EditorPrefs {
  try {
    const raw = prefs.get('wb.editor');
    return raw ? { ...DEFAULT_EDITOR_PREFS, ...(JSON.parse(raw) as Partial<EditorPrefs>) } : DEFAULT_EDITOR_PREFS;
  } catch {
    return DEFAULT_EDITOR_PREFS;
  }
}

let prefsNow: EditorPrefs | null = null;
const prefSubs = new Set<() => void>();

/** The editor settings, shared by every card: a change in one shows in all. */
export const editorPrefs = {
  get(): EditorPrefs {
    prefsNow ??= readPrefs();
    return prefsNow;
  },
  set(patch: Partial<EditorPrefs>) {
    prefsNow = { ...editorPrefs.get(), ...patch };
    prefs.set('wb.editor', JSON.stringify(prefsNow));
    for (const fn of [...prefSubs]) fn();
  },
  subscribe(fn: () => void): () => void {
    prefSubs.add(fn);
    return () => prefSubs.delete(fn);
  },
};

// ---- the parts a doc belongs to (partsvc.ts binds these) ----------------------------------------

/** What the store needs of a part (partsvc.ts PartService). */
export interface DocPart extends PartServiceApi {
  /** A card mounts it now: its language servers run. */
  readonly mounted: boolean;
  /** The path of a key inside the part ("" = its folder); null outside it. */
  relOf(key: FileKey): string | null;
  /** A line for the Output panel of every card that mounts it. */
  log(line: string): void;
  /** The cards that mount it, which see the files its language servers borrowed. */
  holders(): Iterable<Holder>;
}

interface PartLookup {
  byRef(ref: string): DocPart | null;
  /** The part holding a key among every part known to this tab (deepest, mounted first). */
  holding(key: FileKey): DocPart | null;
}

let parts: PartLookup = { byRef: () => null, holding: () => null };

/** partsvc.ts tells the store how to find parts; tests give their own. */
export function bindParts(p: PartLookup) {
  parts = p;
}

// ---- holders --------------------------------------------------------------------------------

/** A card's view as the store sees it. */
export interface Holder extends DocHolder {
  /** Its own save (auto save after a delay saves through the first holder). */
  saveDoc?(key: FileKey): Promise<boolean>;
  /** A doc it holds finished loading (watch it). */
  ready?(key: FileKey): void;
  /** A file it holds moved: its tabs follow, onto the new doc. */
  renamed?(from: FileKey, to: FileKey, doc: Doc): void;
  /** A language server of one of its parts borrowed a file: it shows in its docs until let go. */
  lent?(key: FileKey, doc: Doc): void;
  /** A doc it held or borrowed is gone. */
  gone?(key: FileKey): void;
}

/** One file the store reads and writes: its machine and its real path. */
const where = (k: FileKey) => splitKey(k);

/** The raw bytes of a file, for previews (images, media, downloads). */
export const rawUrlOf = (k: FileKey) => {
  const { machineId, abs } = where(k);
  return fsRawUrl(machineId, abs);
};

/** api.fsRead on a key. */
export const readKey = (k: FileKey, max?: number) => {
  const { machineId, abs } = where(k);
  return api.fsRead(machineId, abs, max);
};

/** api.fsStat on a key. */
export const statKey = (k: FileKey) => {
  const { machineId, abs } = where(k);
  return api.fsStat(machineId, abs);
};

/** fsApi.statBatch on keys: one call per machine. The stats by key; a machine that fails gives none. */
export async function statKeys(keys: readonly FileKey[]): Promise<Map<FileKey, FsStat>> {
  const by = new Map<string, string[]>();
  for (const k of keys) {
    const { machineId, abs } = where(k);
    by.set(machineId, [...(by.get(machineId) ?? []), abs]);
  }
  const out = new Map<FileKey, FsStat>();
  await Promise.all(
    [...by].map(async ([machineId, paths]) => {
      const stats = await fsApi.statBatch(machineId, paths).catch(() => [] as FsStat[]);
      for (const st of stats) if (st.path.startsWith('/')) out.set(fileKey(machineId, st.path), st);
    }),
  );
  return out;
}

/**
 * What a new watch's first look (its starting point, the stats of POST fs/watch) says about open
 * files: the ones that changed or went while no watch looked at them, such as between one card's
 * watch and the next card's. Folders and files no ready doc holds are not changes; a look older
 * than the text held is from before that text's own save. Pure.
 */
export function missedChanges(
  machineId: string,
  stats: readonly FsStat[],
  held: (key: FileKey) => Pick<Doc, 'status' | 'mtime' | 'deleted'> | undefined,
): { key: FileKey; exists: boolean; mtime: string }[] {
  const out: { key: FileKey; exists: boolean; mtime: string }[] = [];
  for (const st of stats) {
    const abs = st.dir ? null : cleanAbs(st.path);
    if (!abs) continue;
    const key = fileKey(machineId, abs);
    const d = held(key);
    if (!d || d.status !== 'ready') continue;
    if (!st.exists) {
      if (!d.deleted) out.push({ key, exists: false, mtime: '' });
      continue;
    }
    if (!st.mtime || st.mtime === d.mtime) continue;
    const at = Date.parse(st.mtime);
    const had = Date.parse(d.mtime);
    if (!Number.isNaN(at) && !Number.isNaN(had) && at < had) continue;
    out.push({ key, exists: true, mtime: st.mtime });
  }
  return out;
}

export class DocStore implements DocStoreApi {
  private docs = new Map<FileKey, Doc>();
  private holds = new Map<FileKey, Set<Holder>>();
  /** Files ensureModel gave out, and when last; let go after LENT_MS unless a card holds them. */
  private lent = new Map<FileKey, number>();
  /** The cards that see a borrowed file (the views of the part that borrowed it). */
  private lentTo = new Map<FileKey, Set<Holder>>();
  private lentTimer = 0;
  private loading = new Map<FileKey, Promise<void>>();
  private draftTimers = new Map<FileKey, number>();
  private autoSaveTimers = new Map<FileKey, number>();
  /** The part a card reached a file through: its language server serves the file when the holding part's does not run. */
  private via = new Map<FileKey, PartServiceApi | null>();
  /** The mtime a reload is under way for: two cards' watches report one change once. */
  private reloadingAt = new Map<FileKey, string>();

  get(key: FileKey): Doc | undefined {
    return this.docs.get(key);
  }

  /** Every doc of the tab. */
  all(): IterableIterator<Doc> {
    return this.docs.values();
  }

  uriOf(key: FileKey): monaco.Uri {
    return monaco.Uri.from(uriPartsOf(key));
  }

  keyOfModel(m: monaco.editor.ITextModel): FileKey | null {
    return keyOfUri(m.uri);
  }

  holders(key: FileKey): ReadonlySet<Holder> {
    return this.holds.get(key) ?? new Set();
  }

  /** The part a card reached the file through (doc.via). */
  viaOf(key: FileKey): PartServiceApi | null {
    return this.via.get(key) ?? null;
  }

  acquire(key: FileKey, holder: Holder, via: PartServiceApi | null, md: MdView = 'reader'): Doc {
    let set = this.holds.get(key);
    if (!set) this.holds.set(key, (set = new Set()));
    set.add(holder);
    this.lentTo.get(key)?.delete(holder);
    if (via && !this.via.get(key)) this.via.set(key, via);
    return this.ensureDoc(key, md);
  }

  release(key: FileKey, holder: DocHolder) {
    const set = this.holds.get(key);
    set?.delete(holder as Holder);
    this.lentTo.get(key)?.delete(holder as Holder);
    if (set?.size) return;
    this.holds.delete(key);
    this.dispose(key);
  }

  /** A card that only saw a borrowed file lets go of it (the file stays for the part's other cards). */
  unlend(key: FileKey, holder: DocHolder) {
    this.lentTo.get(key)?.delete(holder as Holder);
  }

  private ensureDoc(key: FileKey, md: MdView = 'reader'): Doc {
    let d = this.docs.get(key);
    if (!d) {
      d = newDoc(key, md);
      this.docs.set(key, d);
      void this.load(d);
    }
    return d;
  }

  /** For language servers: the model of a file, without a tab. */
  async ensureModel(key: FileKey, via: PartServiceApi | null): Promise<monaco.editor.ITextModel | null> {
    const existing = monaco.editor.getModel(this.uriOf(key));
    if (existing) {
      if (this.docs.get(key)?.model === existing) this.lend(key, via);
      return existing;
    }
    if (!this.via.get(key)) this.via.set(key, via);
    const d = this.ensureDoc(key);
    this.lend(key, via);
    await this.loaded(d);
    return d.model;
  }

  private lend(key: FileKey, via: PartServiceApi | null) {
    this.lent.set(key, Date.now());
    const d = this.docs.get(key);
    const views = via ? (via as DocPart).holders?.() : undefined;
    if (d && views) {
      let set = this.lentTo.get(key);
      if (!set) this.lentTo.set(key, (set = new Set()));
      for (const h of views) {
        if (this.holds.get(key)?.has(h) || set.has(h)) continue;
        set.add(h);
        h.lent?.(key, d);
      }
    }
    if (!this.lentTimer) this.lentTimer = window.setTimeout(() => this.sweepLent(), LENT_MS);
  }

  /** Let go of lent models no card took up; unsaved edits (a rename) and shown ones stay. */
  private sweepLent() {
    this.lentTimer = 0;
    const now = Date.now();
    let next = Infinity;
    const shown = new Set(monaco.editor.getEditors().map((e) => e.getModel()));
    for (const [key, at] of [...this.lent]) {
      const d = this.docs.get(key);
      if (!d || this.holds.get(key)?.size) {
        this.lent.delete(key); // gone, or a card holds it and lets it go when its tab closes
        continue;
      }
      const busy = d.status === 'loading' || this.loading.has(key) || d.dirty || d.saving || !!d.recovered || (!!d.model && shown.has(d.model));
      if (busy || now - at < LENT_MS) {
        next = Math.min(next, busy ? LENT_MS : LENT_MS - (now - at));
        continue;
      }
      this.lent.delete(key);
      this.dispose(key);
    }
    if (next < Infinity) this.lentTimer = window.setTimeout(() => this.sweepLent(), Math.max(next, 10_000));
  }

  /** The doc goes: its model, its timers, and its place in every card. */
  private dispose(key: FileKey) {
    if (this.holds.get(key)?.size) return;
    const d = this.docs.get(key);
    this.lent.delete(key);
    window.clearTimeout(this.draftTimers.get(key));
    window.clearTimeout(this.autoSaveTimers.get(key));
    this.draftTimers.delete(key);
    this.autoSaveTimers.delete(key);
    const saw = this.lentTo.get(key);
    this.lentTo.delete(key);
    this.via.delete(key);
    this.reloadingAt.delete(key);
    if (!d) return;
    if (d.model) {
      hubOf(d.model)?.detach(d.model);
      if (d.dirty) void drafts.remove(where(key).machineId, where(key).abs);
      d.model.dispose();
      d.model = null;
    }
    this.docs.delete(key);
    for (const h of saw ?? []) h.gone?.(key);
  }

  private loaded(d: Doc): Promise<void> {
    return this.loading.get(d.path) ?? Promise.resolve();
  }

  private load(d: Doc): Promise<void> {
    const p = this.loadInner(d).finally(() => this.loading.delete(d.path));
    this.loading.set(d.path, p);
    return p;
  }

  /** Something about a doc changed: every card that holds or borrows it renders again. */
  private changed(key: FileKey) {
    for (const h of [...(this.holds.get(key) ?? []), ...(this.lentTo.get(key) ?? [])]) h.changed();
  }

  private async loadInner(d: Doc) {
    const { machineId, abs } = where(d.path);
    const ext = extOf(abs);
    try {
      const st = await api.fsStat(machineId, abs);
      d.size = st.size;
      d.mtime = st.mtime;
      if (st.exists === false) throw new Error('This file no longer exists.');
      if (IMAGE_EXT.has(ext) || MEDIA_EXT.has(ext)) {
        d.kind = IMAGE_EXT.has(ext) ? 'image' : 'media';
        d.status = 'ready';
        d.readOnly = 'Preview';
        this.changed(d.path);
        this.ready(d.path);
        return;
      }
      if (st.size > MAX_VIEW) {
        d.kind = 'toolarge';
        d.status = 'ready';
        d.readOnly = `This file is ${Math.round(st.size / (1 << 20))} MB; files over 10 MB are not opened.`;
        this.changed(d.path);
        this.ready(d.path);
        return;
      }
      const r = await api.fsRead(machineId, abs, MAX_VIEW + 1);
      d.size = r.size;
      d.mtime = r.mtime;
      let content = r.content;
      if (r.binary) {
        const legacy = await this.decodeLegacy(d.path);
        if (legacy === null) {
          d.kind = 'binary';
          d.status = 'ready';
          d.readOnly = 'Binary file: not opened.';
          this.changed(d.path);
          this.ready(d.path);
          return;
        }
        content = legacy.text;
        d.encoding = legacy.encoding;
      }
      if (this.docs.get(d.path) !== d) return; // let go while it loaded
      d.bom = !d.encoding && content.startsWith('﻿');
      const text = d.bom ? content.slice(1) : content;
      d.mixedEol = /\r\n/.test(text) && /(^|[^\r])\n/.test(text);
      const uri = this.uriOf(d.path);
      const model = monaco.editor.getModel(uri) ?? monaco.editor.createModel(text, languageFor(abs), uri);
      if (model.getValue() !== text.replace(/\r\n|\r|\n/g, model.getEOL())) model.setValue(text);
      d.model = model;
      d.savedAlt = model.getAlternativeVersionId();
      d.kind = 'text';
      d.status = 'ready';
      d.readOnly = d.encoding
        ? `Not UTF-8 (shown as ${d.encoding}): read-only, so that its bytes stay as they are.`
        : st.size > MAX_EDIT
          ? `Files over 1 MB open read-only (this one is ${Math.round(st.size / 1024)} KB).`
          : '';
      model.onDidChangeContent(() => this.onModelChange(d));
      this.attachLsp(d.path, model);
      const draft = await drafts.get(machineId, abs);
      if (this.docs.get(d.path) !== d || model.isDisposed?.()) return; // let go meanwhile
      if (draft && draft.content !== model.getValue()) {
        // The same base as on disk: the unsaved edits simply come back, like a hot exit.
        // Otherwise the file moved on and the user has to choose.
        if (draft.baseMtime === d.mtime) {
          model.setValue(draft.content);
        } else {
          d.recovered = draft;
        }
      } else if (draft) {
        void drafts.remove(machineId, abs);
      }
      this.updateDirty(d);
      this.changed(d.path);
      this.ready(d.path);
    } catch (err) {
      d.status = 'error';
      d.error = errorMessage(err);
      this.changed(d.path);
    }
  }

  private ready(key: FileKey) {
    for (const h of this.holds.get(key) ?? []) h.ready?.(key);
  }

  /**
   * The language server for a model: the holding part's when a card mounts it, else the part the
   * file was reached through (a definition in another part), else whichever is known. A hub
   * takes a model from another when it attaches it (lsp.ts), so one server speaks for it.
   */
  private attachLsp(key: FileKey, model: monaco.editor.ITextModel) {
    const holder = parts.holding(key);
    const via = this.via.get(key) as DocPart | null | undefined;
    const part = holder?.mounted ? holder : via?.mounted ? via : (holder ?? via ?? null);
    part?.lsp.attach(model);
  }

  /** The bytes of a file the hub would not hand over as UTF-8; null for real binaries. */
  private async decodeLegacy(key: FileKey, encoding?: string): Promise<{ text: string; encoding: string } | null> {
    const res = await fetch(rawUrlOf(key));
    if (!res.ok) throw new Error(`Could not read the file (${res.status}).`);
    const bytes = new Uint8Array(await res.arrayBuffer());
    let enc = encoding;
    if (!enc) {
      if (bytes[0] === 0xff && bytes[1] === 0xfe) enc = 'utf-16le';
      else if (bytes[0] === 0xfe && bytes[1] === 0xff) enc = 'utf-16be';
      else if (bytes.subarray(0, 8192).includes(0)) return null;
      else enc = 'windows-1252';
    }
    return { text: new TextDecoder(enc).decode(bytes), encoding: enc };
  }

  /** "Reopen with Encoding": the file stays read-only, only the view changes. */
  async reopenWithEncoding(key: FileKey, encoding: string) {
    const d = this.docs.get(key);
    if (!d?.model || !d.encoding) return;
    try {
      const r = await this.decodeLegacy(key, encoding);
      if (!r) return;
      d.encoding = r.encoding;
      d.readOnly = `Not UTF-8 (shown as ${r.encoding}): read-only, so that its bytes stay as they are.`;
      d.model.setValue(r.text);
      d.savedAlt = d.model.getAlternativeVersionId();
      d.dirty = false;
      this.changed(key);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  }

  private onModelChange(d: Doc) {
    const was = d.dirty;
    this.updateDirty(d);
    if (d.dirty) {
      // typing keeps the tab, in every card that shows it
      for (const h of this.holds.get(d.path) ?? []) h.edited(d.path);
      this.scheduleDraft(d);
      if (editorPrefs.get().autoSave === 'afterDelay' && !d.readOnly) this.scheduleAutoSave(d);
    }
    if (d.dirty !== was || d.dirty) this.changed(d.path);
  }

  private updateDirty(d: Doc) {
    if (d.model) d.dirty = d.model.getAlternativeVersionId() !== d.savedAlt;
  }

  private scheduleDraft(d: Doc) {
    window.clearTimeout(this.draftTimers.get(d.path));
    this.draftTimers.set(
      d.path,
      window.setTimeout(() => this.writeDraft(d), 500),
    );
  }

  private writeDraft(d: Doc) {
    if (!d.model || !d.dirty) return;
    const { machineId, abs } = where(d.path);
    void drafts.put(machineId, abs, { content: d.model.getValue(), baseMtime: d.mtime, at: Date.now() });
  }

  /** Write the pending drafts now (a card is closing): of one holder's docs, or all. */
  flushDrafts(holder?: DocHolder) {
    for (const d of this.docs.values()) {
      if (!d.dirty) continue;
      if (holder && !this.holds.get(d.path)?.has(holder as Holder)) continue;
      this.writeDraft(d);
    }
  }

  private scheduleAutoSave(d: Doc) {
    window.clearTimeout(this.autoSaveTimers.get(d.path));
    this.autoSaveTimers.set(
      d.path,
      window.setTimeout(() => {
        if (!d.dirty || d.conflict) return;
        const h = [...(this.holds.get(d.path) ?? [])].find((x) => x.saveDoc);
        void h?.saveDoc?.(d.path);
      }, 1000),
    );
  }

  async reload(key: FileKey) {
    const d = this.docs.get(key);
    if (!d?.model) {
      if (d) {
        d.status = 'loading';
        this.changed(key);
        await this.load(d);
      }
      return;
    }
    const { machineId, abs } = where(key);
    try {
      const r = await api.fsRead(machineId, abs, MAX_VIEW + 1);
      const legacy = r.binary ? await this.decodeLegacy(key, d.encoding || undefined) : null;
      if (r.binary && !legacy) return;
      const text = legacy ? legacy.text : r.content.startsWith('﻿') ? r.content.slice(1) : r.content;
      if (!d.model) return;
      this.replaceAll(d.model, text);
      d.mtime = r.mtime;
      d.size = r.size;
      d.savedAlt = d.model.getAlternativeVersionId();
      d.conflict = false;
      d.deleted = false;
      d.dirty = false;
      void drafts.remove(machineId, abs);
      this.changed(key);
    } catch (err) {
      toast.error(`Could not reload: ${errorMessage(err)}`);
    }
  }

  /** Replace the text as one undoable step, so Undo brings back what was there. */
  private replaceAll(model: monaco.editor.ITextModel, text: string) {
    if (model.getValue() === text.replace(/\r\n|\r|\n/g, model.getEOL())) return;
    model.pushStackElement();
    model.pushEditOperations([], [{ range: model.getFullModelRange(), text }], () => null);
    model.pushStackElement();
  }

  /** Writes the file; then tells its part: language server, git, Laravel, commands on save. */
  async save(key: FileKey, o: DocSaveOptions): Promise<boolean> {
    const d = this.docs.get(key);
    if (!d?.model || d.readOnly || d.status !== 'ready') return false;
    window.clearTimeout(this.draftTimers.get(key));
    window.clearTimeout(this.autoSaveTimers.get(key));
    const { machineId, abs } = where(key);
    d.saving = true;
    d.saveError = '';
    this.changed(key);
    try {
      if (o.format) await o.format(d);
      let ifMtime = d.mtime || undefined;
      if (o.force) ifMtime = (await api.fsStat(machineId, abs)).mtime || undefined;
      const body = { path: abs, content: (d.bom ? '﻿' : '') + d.model.getValue(), ifMtime, projectId: o.ref, allowOutside: o.outside || undefined };
      const st = await o.run((c) => fsApi.write(machineId, { ...body, ...c }));
      if (!st) {
        d.saving = false;
        this.changed(key);
        return false;
      }
      const part = parts.byRef(o.ref) ?? parts.holding(key);
      const rel = part?.relOf(key) ?? null;
      d.mtime = st.mtime;
      if (part?.laravel?.detected && rel !== null && changesLaravelIndex(rel)) void part.refreshLaravel();
      d.size = st.size;
      d.savedAlt = d.model.getAlternativeVersionId();
      d.dirty = false;
      d.conflict = false;
      d.deleted = false;
      d.outside = false;
      d.saving = false;
      d.recovered = null;
      void drafts.remove(machineId, abs);
      hubOf(d.model)?.saved(d.model);
      part?.gitSoon();
      this.changed(key);
      if (part && rel !== null) void this.runOnSave(d, part, rel);
      return true;
    } catch (err) {
      d.saving = false;
      if (isCode(err, 'mtime_mismatch') || (err instanceof ApiError && err.status === 409)) d.conflict = true;
      else if (isCode(err, 'outside_project')) d.outside = true;
      else if (isCode(err, 'no_sftp')) d.readOnly = 'This machine has no SFTP, so files can only be read.';
      else d.saveError = errorMessage(err);
      this.changed(key);
      return false;
    }
  }

  private async runOnSave(d: Doc, part: DocPart, rel: string) {
    if (!store.getState().settings?.editor?.onSave?.length) return;
    try {
      const r = await filesApi.onSave(part.ref, rel);
      for (const run of r.runs ?? []) {
        part.log(`$ ${run.command}${run.output ? `\n${run.output.trimEnd()}` : ''}`);
        if (run.exitCode !== 0 || run.timedOut) {
          toast.notify('error', 'A command on save failed', `${run.command}: ${run.timedOut ? 'stopped after 60 s' : run.output.trim().slice(-300) || `exit ${run.exitCode}`}`);
        }
      }
      if (!r.runs?.length || !d.model) return;
      // A formatter may have changed the file: take it in as an undoable edit.
      const { machineId, abs } = where(d.path);
      const fresh = await api.fsRead(machineId, abs, MAX_VIEW + 1);
      if (fresh.mtime === d.mtime || d.dirty || !d.model) return;
      this.replaceAll(d.model, fresh.content.startsWith('﻿') ? fresh.content.slice(1) : fresh.content);
      d.mtime = fresh.mtime;
      d.size = fresh.size;
      d.savedAlt = d.model.getAlternativeVersionId();
      d.dirty = false;
      this.changed(d.path);
    } catch (err) {
      part.log(`Commands on save: ${errorMessage(err)}`);
    }
  }

  restoreDraft(key: FileKey) {
    const d = this.docs.get(key);
    if (!d?.recovered || !d.model) return;
    d.model.setValue(d.recovered.content);
    d.recovered = null;
    this.updateDirty(d);
    this.changed(key);
  }

  dropDraft(key: FileKey) {
    const d = this.docs.get(key);
    if (!d) return;
    const { machineId, abs } = where(key);
    void drafts.remove(machineId, abs);
    d.recovered = null;
    this.changed(key);
  }

  /** "Don't save": the edits go, with their draft. */
  discard(key: FileKey) {
    const d = this.docs.get(key);
    const { machineId, abs } = where(key);
    void drafts.remove(machineId, abs);
    if (d) d.dirty = false;
  }

  /** A watch saw the file change. Idempotent per mtime: every card's watch may feed it. */
  /** A new watch's first look on a machine: the open files that changed while no watch looked (missedChanges). */
  catchUp(machineId: string, stats: readonly FsStat[]) {
    for (const c of missedChanges(machineId, stats, (k) => this.docs.get(k))) this.onDiskChange(c.key, c.exists, c.mtime);
  }

  onDiskChange(key: FileKey, exists: boolean, mtime: string | undefined) {
    const d = this.docs.get(key);
    if (!d || d.status !== 'ready' || d.saving) return;
    if (!exists) {
      if (d.deleted) return;
      d.deleted = true;
      this.changed(key);
      return;
    }
    if (!mtime || mtime === d.mtime || this.reloadingAt.get(key) === mtime) return;
    d.deleted = false;
    parts.holding(key)?.gitSoon();
    if (d.dirty) {
      if (d.conflict) return;
      d.conflict = true;
      this.changed(key);
    } else {
      this.reloadingAt.set(key, mtime);
      void this.reload(key).finally(() => {
        if (this.reloadingAt.get(key) === mtime) this.reloadingAt.delete(key);
      });
    }
  }

  /**
   * A file or folder moved on its machine: the docs under it take the new keys, keeping unsaved
   * edits, and every card that holds one moves its tabs along.
   */
  async remap(from: FileKey, to: FileKey) {
    const moved = [...this.docs.values()].filter((d) => d.path === from || d.path.startsWith(`${from}/`));
    for (const old of moved) {
      const next = (to + old.path.slice(from.length)) as FileKey;
      const unsaved = old.dirty && old.model ? old.model.getValue() : null;
      const holders = [...(this.holds.get(old.path) ?? [])];
      const via = this.via.get(old.path) ?? null;
      const { machineId, abs } = where(old.path);
      void drafts.remove(machineId, abs);
      this.holds.delete(old.path);
      this.dispose(old.path);
      if (!holders.length) continue;
      this.holds.set(next, new Set(holders));
      if (via) this.via.set(next, via);
      const d = this.ensureDoc(next, old.md);
      for (const h of holders) h.renamed?.(old.path, next, d);
      await this.loaded(d);
      if (unsaved !== null && d.model) d.model.setValue(unsaved);
    }
  }
}

/** The tab's one store. */
export const docStore = new DocStore();
