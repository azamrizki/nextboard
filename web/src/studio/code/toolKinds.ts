// What a tool tab of the code view shows (docs/API-studio.md §7.15): Tab.tool.kind, with an
// argument for the kinds that take one (the browser's address). Pure: titles and icons are
// toolsList.ts (toolMeta), the components ToolTab.tsx.
export type ToolKind = 'chats' | 'memory' | 'documents' | 'diagrams' | 'markdown' | 'skills' | 'vault' | 'browser' | 'loops' | 'mission';

/** Every kind, in the activity bar's order. */
export const TOOL_KINDS: readonly ToolKind[] = ['chats', 'memory', 'documents', 'diagrams', 'markdown', 'skills', 'vault', 'browser', 'loops', 'mission'];

export const isToolKind = (v: unknown): v is ToolKind => typeof v === 'string' && (TOOL_KINDS as readonly string[]).includes(v);

/** Kinds open once per argument (a browser per address); every other kind is open once at most. */
export const toolPerArg = (kind: ToolKind): boolean => kind === 'browser';
