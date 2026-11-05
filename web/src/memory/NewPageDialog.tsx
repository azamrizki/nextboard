// A new sub-memory: a page on one topic that agents look up when the work needs it.

import { useState, type FormEvent } from 'react';
import { errorMessage, isStatus } from '../api';
import { Button } from '../components/Button';
import { TextField } from '../components/form';
import { Modal } from '../components/Modal';
import { memoryApi } from './api';

const TEMPLATES: { title: string; description: string; content: string }[] = [
  { title: 'Design system', description: 'Tokens, components and layout rules. Read before UI work.', content: '## Tokens\n\n- \n\n## Components\n\n- \n\n## Layout\n\n- \n' },
  { title: "Do and don't", description: 'What this codebase wants and what it rejects. Read before writing code.', content: "## Do\n\n- \n\n## Don't\n\n- \n" },
  { title: 'Testing', description: 'How tests run and what they touch. Read before running or writing tests.', content: '## Running\n\n- \n\n## What they touch\n\n- \n' },
  { title: 'Deployment', description: 'How releases reach production. Read before anything about deploys.', content: '## Steps\n\n- \n\n## Rollback\n\n- \n' },
  { title: 'Data model', description: 'Tables, their meaning and how they relate. Read before changing data.', content: '## Tables\n\n- \n\n## Rules\n\n- \n' },
];

const slugOf = (s: string) =>
  s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48)
    .replace(/-+$/, '');

export function NewPageDialog({ owner, ownerName, taken, onClose, onCreated }: { owner: string; ownerName: string; taken: string[]; onClose: () => void; onCreated: (name: string) => void }) {
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [content, setContent] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  // A title in another script has no letters a name can use.
  const slug = slugOf(title) || (title.trim() ? `page-${Date.now().toString(36)}` : '');
  const clash = taken.includes(slug) || slug === 'main' || slug === 'critical';

  const pick = (t: (typeof TEMPLATES)[number]) => {
    setTitle(t.title);
    setDescription(t.description);
    setContent(t.content);
  };

  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (!slug || clash) return;
    setBusy(true);
    setError('');
    try {
      await memoryApi.save(owner, slug, { title: title.trim(), description: description.trim(), content });
      onCreated(slug);
    } catch (err) {
      setError(isStatus(err, 409) ? 'This memory has too many pages already.' : errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`New sub-memory in ${ownerName}`}
      icon="brain"
      onClose={onClose}
      size="sm"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" icon="plus" onClick={create} loading={busy} disabled={!slug || clash}>
            Create
          </Button>
        </>
      }
    >
      <form className="mem-form" onSubmit={create}>
        <div className="chips" role="group" aria-label="Start from">
          {TEMPLATES.map((t) => (
            <button key={t.title} type="button" className={`mem-chip ${title === t.title ? 'is-on' : ''}`} onClick={() => pick(t)}>
              {t.title}
            </button>
          ))}
        </div>
        <TextField label="Title" value={title} autoFocus maxLength={80} onChange={(e) => setTitle(e.target.value)} error={clash ? 'A page with this name exists.' : undefined} hint={slug && !clash ? `Agents ask for it as ${slug}` : undefined} />
        <TextField label="When to read it" optional value={description} maxLength={300} onChange={(e) => setDescription(e.target.value)} hint="One sentence. Agents choose what to look up by this." />
        {error && (
          <p className="field-error" role="alert">
            {error}
          </p>
        )}
      </form>
    </Modal>
  );
}
