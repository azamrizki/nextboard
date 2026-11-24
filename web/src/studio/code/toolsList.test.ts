import { describe, expect, it } from 'vitest';
import type { ToolKind } from './toolKinds';
import { browserArg, browserTitle, CODE_VIEW_TOOLS, parseBrowserArg, STUDIO_TOOLS, TOOL_ITEMS, toolMeta } from './toolsList';

const ALL: ToolKind[] = ['chats', 'memory', 'documents', 'diagrams', 'markdown', 'skills', 'vault', 'browser', 'loops', 'mission'];

describe('CODE_VIEW_TOOLS', () => {
  it('lists the tools in the activity bar order, Studios as one item', () => {
    expect(CODE_VIEW_TOOLS.map((t) => t.label)).toEqual(['Chats', 'Memory', 'Studios', 'Vault', 'Browser', 'Loops', 'Mission Map']);
    expect(CODE_VIEW_TOOLS.map((t) => t.kind)).toEqual(['chats', 'memory', 'documents', 'vault', 'browser', 'loops', 'mission']);
  });

  it('gives only Studios a menu, of its four studios', () => {
    const withMenu = CODE_VIEW_TOOLS.filter((t) => t.menu);
    expect(withMenu.map((t) => t.label)).toEqual(['Studios']);
    expect(withMenu[0].menu).toBe(STUDIO_TOOLS);
    expect(STUDIO_TOOLS.map((t) => [t.kind, t.label])).toEqual([
      ['documents', 'Documents'],
      ['diagrams', 'Diagrams'],
      ['markdown', 'Markdown'],
      ['skills', 'Skills'],
    ]);
  });

  it('opens every kind from somewhere, each once', () => {
    expect(TOOL_ITEMS.map((t) => t.kind)).toEqual(ALL);
    expect(new Set(TOOL_ITEMS.map((t) => t.icon)).size).toBe(TOOL_ITEMS.length);
  });
});

describe('toolMeta', () => {
  it('names each tool and gives its icon', () => {
    expect(ALL.map((k) => toolMeta(k).title)).toEqual(['Chats', 'Memory', 'Documents', 'Diagrams', 'Markdown', 'Skills', 'Vault', 'Browser', 'Loops', 'Mission Map']);
    expect(toolMeta('vault')).toEqual({ title: 'Vault', icon: 'key', tip: 'Vault' });
    expect(toolMeta('mission').icon).toBe('map');
    expect(toolMeta('chats').icon).toBe('message');
  });

  it('ignores the argument of a tool that takes none', () => {
    expect(toolMeta('memory', 'anything')).toEqual(toolMeta('memory'));
  });

  it('names a browser tab after its site, and keeps the address as its tip', () => {
    expect(toolMeta('browser', 'http://localhost:5173/cart')).toEqual({ title: 'localhost:5173', icon: 'globe', tip: 'http://localhost:5173/cart' });
    expect(toolMeta('browser', '')).toEqual({ title: 'Browser', icon: 'globe', tip: 'Browser' });
    expect(toolMeta('browser').title).toBe('Browser');
  });

  it('gives a kind it does not know (a tab from a newer build) a title still', () => {
    expect(toolMeta('later' as ToolKind)).toEqual({ title: 'Tool', icon: 'grid', tip: 'Tool' });
  });
});

describe('browserTitle', () => {
  it('reads localhost in every spelling as the port on localhost', () => {
    expect(browserTitle('localhost:3000')).toBe('localhost:3000');
    expect(browserTitle('http://127.0.0.1:8080/x?y=1')).toBe('localhost:8080');
    expect(browserTitle('5173')).toBe('localhost:5173');
    expect(browserTitle('https://localhost/')).toBe('localhost:443');
  });

  it('reads a site as its host, with a port only when it has one', () => {
    expect(browserTitle('https://shop.example.com/cart')).toBe('shop.example.com');
    expect(browserTitle('example.org')).toBe('example.org');
    expect(browserTitle('http://harbor.example.net:8443/a')).toBe('harbor.example.net:8443');
  });

  it('falls back to the text, cut short, and to Browser for nothing', () => {
    expect(browserTitle('   ')).toBe('Browser');
    expect(browserTitle(undefined)).toBe('Browser');
    expect(browserTitle('not a url at all')).toBe('not a url at all');
    expect(browserTitle(`x${'y'.repeat(60)} z`)).toHaveLength(40);
  });
});

describe('browserArg', () => {
  it('keeps the machine a tab was opened for with its address, and reads plain addresses as before', () => {
    expect(browserArg('http://localhost:5173/', 'm_shop1')).toBe('@m_shop1 http://localhost:5173/');
    expect(browserArg('http://localhost:5173/')).toBe('http://localhost:5173/');
    expect(browserArg('', 'local')).toBe('@local');
    expect(browserArg('localhost:3000', 'bad id')).toBe('localhost:3000');
    expect(parseBrowserArg('@m_shop1 http://localhost:5173/cart')).toEqual({ url: 'http://localhost:5173/cart', machineId: 'm_shop1' });
    expect(parseBrowserArg('@local')).toEqual({ url: '', machineId: 'local' });
    expect(parseBrowserArg('http://localhost:5173/')).toEqual({ url: 'http://localhost:5173/' });
    expect(parseBrowserArg(undefined)).toEqual({ url: '' });
    // the title and the tip are the address's
    expect(toolMeta('browser', '@m_shop1 http://localhost:5173/cart')).toEqual({ title: 'localhost:5173', icon: 'globe', tip: 'http://localhost:5173/cart' });
  });
});
