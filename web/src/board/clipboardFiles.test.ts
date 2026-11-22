import { describe, expect, it, vi } from 'vitest';

vi.mock('../components/Toast', () => ({ toast: { error: vi.fn(), info: vi.fn(), success: vi.fn() } }));

import { toast } from '../components/Toast';
import { clipboardFiles } from './files';

const png = (name = 'image.png') => new File([new Uint8Array([137, 80, 78, 71])], name, { type: 'image/png' });

/** A paste's clipboard as a browser builds it: file items (and files), and text formats. */
function clip({ files = [] as File[], data = {} as Record<string, string>, inItems = true, inFiles = true, folder = false } = {}): DataTransfer {
  const items = inItems
    ? files.map((f) => ({ kind: 'file', type: f.type, getAsFile: () => f, webkitGetAsEntry: () => (folder ? { isDirectory: true } : { isDirectory: false }) }))
    : [];
  return {
    items: [...items, ...Object.keys(data).map((t) => ({ kind: 'string', type: t, getAsFile: () => null }))],
    files: inFiles ? files : [],
    getData: (t: string) => data[t] ?? '',
    types: [...(files.length ? ['Files'] : []), ...Object.keys(data)],
  } as unknown as DataTransfer;
}

describe('what a paste carries', () => {
  it('a picture copied in Telegram, with its caption or link beside it, is the picture', () => {
    const f = png();
    expect(clipboardFiles(clip({ files: [f], data: { 'text/plain': 'Look at the new login page' } }))).toEqual([f]);
    expect(clipboardFiles(clip({ files: [f], data: { 'text/plain': 'https://t.me/c/123/456' } }))).toEqual([f]);
    expect(clipboardFiles(clip({ files: [f] }))).toEqual([f]); // the picture alone
  });

  it("a browser's Copy image, a screenshot tool, and a picture WebKit puts only in items", () => {
    const f = png();
    expect(clipboardFiles(clip({ files: [f], data: { 'text/html': '<img src="blob:https://web.telegram.org/x">' } }))).toEqual([f]);
    expect(clipboardFiles(clip({ files: [f], inFiles: false }))).toEqual([f]);
    expect(clipboardFiles(clip({ files: [f], inItems: false }))).toEqual([f]);
  });

  it("a file manager's files are the files", () => {
    const a = png('shot 1.png');
    expect(clipboardFiles(clip({ files: [a], data: { 'text/plain': '/home/alice/Pictures/shot 1.png' } }))).toEqual([a]);
    const pdf = new File(['%PDF'], 'spec.pdf', { type: 'application/pdf' });
    expect(clipboardFiles(clip({ files: [pdf], data: { 'text/plain': 'spec.pdf' } }))).toEqual([pdf]);
  });

  it("a spreadsheet's cells or an office document's text, with a picture of them, are text", () => {
    const f = png();
    expect(clipboardFiles(clip({ files: [f], data: { 'text/plain': 'Name\tTotal\nShop\t12' } }))).toEqual([]);
    expect(clipboardFiles(clip({ files: [f], data: { 'text/plain': 'Total 12', 'text/html': '<html><body><table><tr><td>Total</td></tr></table>' } }))).toEqual([]);
    expect(clipboardFiles(clip({ files: [f], data: { 'text/plain': 'A paragraph.', 'text/html': '<html xmlns:o="urn:schemas-microsoft-com:office:office"><p>A paragraph.</p>' } }))).toEqual([]);
    expect(clipboardFiles(clip({ files: [f], data: { 'text/plain': 'A paragraph.', 'text/html': '<meta name="generator" content="LibreOffice 7.6"><p>A paragraph.</p>' } }))).toEqual([]);
  });

  it('text alone is text, and a folder is refused', () => {
    expect(clipboardFiles(clip({ data: { 'text/plain': 'hello' } }))).toEqual([]);
    expect(clipboardFiles(null)).toEqual([]);
    expect(clipboardFiles(clip({ files: [new File([], 'photos')], folder: true }))).toEqual([]);
    expect(toast.error).toHaveBeenCalledWith('photos is a folder. Copy the files inside it instead.');
  });
});
