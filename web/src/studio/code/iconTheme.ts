// File icon themes from extensions: a theme is a table from names to pictures, so all of it is
// data. The pictures are SVG shown through <img>, where a script inside one cannot run.

import { useSyncExternalStore } from 'react';
import { prefs } from '../../lib/format';
import { extApi, type InstalledExtension } from '../api';
import { parseJsonc } from './jsonc';
import { languageFor, monaco } from './monaco';

interface ThemeFile {
  iconDefinitions?: Record<string, { iconPath?: string }>;
  file?: string;
  folder?: string;
  folderExpanded?: string;
  rootFolder?: string;
  rootFolderExpanded?: string;
  fileExtensions?: Record<string, string>;
  fileNames?: Record<string, string>;
  folderNames?: Record<string, string>;
  folderNamesExpanded?: Record<string, string>;
  languageIds?: Record<string, string>;
}

interface Theme extends ThemeFile {
  ext: string;
  path: string;
}

const PREF = 'wb.iconTheme';

let theme: Theme | null = null;
let version = 0;
const subs = new Set<() => void>();
const bump = () => {
  version++;
  for (const fn of subs) fn();
};
const subscribe = (fn: () => void) => {
  subs.add(fn);
  return () => subs.delete(fn);
};

/** Re-render when the theme changes or a picture has arrived. */
export const useIconTheme = () => useSyncExternalStore(subscribe, () => version);

const joinPath = (from: string, rel: string) => new URL(rel, `file:///${from}`).pathname.slice(1);

const lower = (m: Record<string, string> | undefined) => Object.fromEntries(Object.entries(m ?? {}).map(([k, v]) => [k.toLowerCase(), v]));

let extLanguages: Map<string, string> | null = null;
function languageByExtension(name: string): string | undefined {
  if (!extLanguages) {
    extLanguages = new Map();
    for (const l of monaco.languages.getLanguages()) for (const e of l.extensions ?? []) if (!extLanguages.has(e.toLowerCase())) extLanguages.set(e.toLowerCase(), l.id);
  }
  const i = name.lastIndexOf('.');
  return i >= 0 ? extLanguages.get(name.slice(i).toLowerCase()) : undefined;
}

/** The icon id of a file, in the order VS Code looks: name, extension, language, default. */
export function fileIconId(t: ThemeFile, rawName: string): string | undefined {
  const name = rawName.toLowerCase();
  const byName = t.fileNames?.[name];
  if (byName) return byName;
  for (let i = name.indexOf('.'); i >= 0; i = name.indexOf('.', i + 1)) {
    const hit = t.fileExtensions?.[name.slice(i + 1)];
    if (hit) return hit;
  }
  const lang = languageFor(name) ?? languageByExtension(name);
  return (lang && t.languageIds?.[lang]) || t.file;
}

export function folderIconId(t: ThemeFile, rawName: string, open: boolean): string | undefined {
  const name = rawName.toLowerCase();
  return (open ? t.folderNamesExpanded?.[name] : t.folderNames?.[name]) ?? (open ? t.folderExpanded : t.folder);
}

// ---- pictures -----------------------------------------------------------------------

const pictures = new Map<string, string | null>();
const LOADS = 4;
let loading = 0;
const queue: (() => void)[] = [];

function loadPicture(id: string, t: Theme) {
  const rel = t.iconDefinitions?.[id]?.iconPath;
  pictures.set(id, null);
  if (!rel) return;
  const run = async () => {
    loading++;
    try {
      const svg = await extApi.file(t.ext, joinPath(t.path, rel));
      if (theme === t) {
        pictures.set(id, `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`);
        bump();
      }
    } catch {
      /* this file keeps the plain icon */
    } finally {
      loading--;
      queue.shift()?.();
    }
  };
  if (loading < LOADS) void run();
  else queue.push(() => void run());
}

const pictureOf = (id: string | undefined): string | null => {
  if (!theme || !id) return null;
  if (!pictures.has(id)) loadPicture(id, theme);
  return pictures.get(id) ?? null;
};

/** The picture for a file or folder, or null for the plain icon. */
export function iconSrc(name: string, dir: boolean, open: boolean): string | null {
  if (!theme) return null;
  return pictureOf(dir ? folderIconId(theme, name, open) : fileIconId(theme, name));
}

// ---- choosing -----------------------------------------------------------------------

export async function applyIconTheme(ext: InstalledExtension, path: string) {
  const data = parseJsonc(await extApi.file(ext.id, path)) as ThemeFile | null;
  // themes built on an icon font or PNG files have nothing this editor can show
  if (!data || !Object.values(data.iconDefinitions ?? {}).some((d) => d.iconPath?.toLowerCase().endsWith('.svg'))) throw new Error('This icon theme has no SVG icons, the only kind this editor can show.');
  pictures.clear();
  queue.length = 0;
  extLanguages = null;
  theme = {
    ...data,
    ext: ext.id,
    path,
    fileNames: lower(data.fileNames),
    fileExtensions: lower(data.fileExtensions),
    folderNames: lower(data.folderNames),
    folderNamesExpanded: lower(data.folderNamesExpanded),
  };
  prefs.set(PREF, JSON.stringify({ ext: ext.id, path }));
  bump();
}

export function useDefaultIcons() {
  theme = null;
  pictures.clear();
  queue.length = 0;
  prefs.set(PREF, '');
  bump();
}

export const activeIconTheme = () => (theme ? { ext: theme.ext, path: theme.path } : null);

/** The saved choice, once the installed list is known; gone with its extension. */
export async function restoreIconTheme(list: InstalledExtension[]) {
  const saved = prefs.get(PREF);
  if (!saved) return;
  try {
    const v = JSON.parse(saved) as { ext?: string; path?: string };
    const ext = list.find((x) => x.id === v.ext);
    if (ext && v.path && ext.contributes.iconThemes?.some((t) => t.path === v.path)) await applyIconTheme(ext, v.path);
    else if (!ext) useDefaultIcons();
  } catch {
    /* the plain icons stay */
  }
}
