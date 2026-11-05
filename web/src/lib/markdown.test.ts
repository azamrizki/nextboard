// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import { renderMarkdown } from './markdown';

function dom(text: string, resolve?: (src: string) => string | null, anchors = false) {
  const div = document.createElement('div');
  div.innerHTML = renderMarkdown(text, resolve, { anchors });
  return div;
}

// Agents and repositories write what this renders: none of it may run in the page.
describe('renderMarkdown stops script', () => {
  it.each([
    ['a script tag', '<script>alert(1)</script>'],
    ['an event handler', '<img src=x onerror=alert(1)>'],
    ['an svg', '<svg onload=alert(1)></svg>'],
    ['an iframe', '<iframe src="javascript:alert(1)"></iframe>'],
    ['a form', '<form action="https://evil.example"><input name=password></form>'],
    ['a style tag', '<style>body{display:none}</style>'],
    ['a javascript link', '[x](javascript:alert(1))'],
    ['a javascript link in capitals', '[x](JAVASCRIPT:alert(1))'],
    ['an encoded javascript link', '[x](java&#115;cript:alert(1))'],
    ['a vbscript link', '[x](vbscript:msgbox(1))'],
    ['a data link', '[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)'],
    ['an image that is script', '![x](javascript:alert(1))'],
    ['a reference link', '[x][1]\n\n[1]: javascript:alert(1)'],
    ['an autolink', '<javascript:alert(1)>'],
  ])('%s', (_name, text) => {
    const div = dom(text);
    expect(div.querySelector('script, iframe, svg, form, input, style, object, embed')).toBeNull();
    for (const el of div.querySelectorAll('*')) {
      for (const a of el.attributes) {
        expect(a.name).not.toMatch(/^on/);
        expect(a.value.toLowerCase().replace(/\s/g, '')).not.toMatch(/^(javascript|vbscript|data:text)/);
      }
    }
  });
});

describe('renderMarkdown links', () => {
  it('open apart from the page and tell it nothing', () => {
    const a = dom('[x](https://example.com/a)').querySelector('a')!;
    expect(a.getAttribute('href')).toBe('https://example.com/a');
    expect(a.getAttribute('target')).toBe('_blank');
    expect(a.getAttribute('rel')).toBe('noopener noreferrer nofollow');
  });

  it('within the page are kept only when the page handles them', () => {
    expect(dom('[x](#part)').querySelector('a')!.hasAttribute('href')).toBe(false);
    expect(dom('[x](#part)', undefined, true).querySelector('a')!.getAttribute('href')).toBe('#part');
  });

  it('are found in plain text', () => {
    expect(dom('see https://example.com now').querySelector('a')?.getAttribute('href')).toBe('https://example.com');
  });
});

describe('renderMarkdown images', () => {
  it('load nothing from outside by themselves', () => {
    const img = dom('![x](https://evil.example/track.png)').querySelector('img')!;
    expect(img.hasAttribute('src')).toBe(false);
    expect(img.getAttribute('loading')).toBe('lazy');
  });

  it('go where the caller maps them', () => {
    const img = dom('![x](a.png)', (src) => (src === 'a.png' ? '/api/files/a.png' : null)).querySelector('img')!;
    expect(img.getAttribute('src')).toBe('/api/files/a.png');
    expect(dom('![x](b.png)', () => null).querySelector('img')!.hasAttribute('src')).toBe(false);
  });

  it('do not keep the mapping for the next render', () => {
    dom('![x](a.png)', () => '/mapped');
    expect(dom('![x](a.png)').querySelector('img')!.hasAttribute('src')).toBe(false);
  });
});

describe('renderMarkdown text', () => {
  it('draws task lists without inputs', () => {
    const div = dom('- [x] done\n- [ ] open');
    expect(div.querySelectorAll('li.task-item')).toHaveLength(2);
    expect(div.querySelector('input')).toBeNull();
  });

  it('draws alerts', () => {
    expect(dom('> [!WARNING]\n> careful').textContent).toContain('careful');
    expect(dom('> [!WARNING]\n> careful').innerHTML).not.toContain('[!WARNING]');
  });

  it('shows raw HTML as text', () => {
    expect(dom('a <b>bold</b>').querySelector('b')).toBeNull();
    expect(dom('a <b>bold</b>').textContent).toContain('<b>bold</b>');
  });
});
