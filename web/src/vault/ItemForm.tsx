// Adding or changing a credential. The fields follow its type (model.ts).

import { useState, type FormEvent } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Checkbox, Field, SegmentedControl, SelectField, TextField } from '../components/form';
import { Modal } from '../components/Modal';
import { GeneratorDialog } from './Dialogs';
import { NO_AUTOFILL, SecretInput } from './fields';
import { byName, emptyItem, HISTORY_MAX, isSecretField, ITEM_TYPES, TYPE_SCHEMA, type FieldDef, type ItemType, type VaultFolder, type VaultItem } from './model';
import { isConflict, saveItem } from './store';
import { parseTotp } from './totp';

const NOTES_MAX = 20_000;
const SHORT_LABEL: Record<ItemType, string> = { login: 'Login', card: 'Card', identity: 'Identity', note: 'Note', ssh: 'SSH key' };

/** Trim what a person leaves behind, and remember the password this one replaces. */
function cleaned(draft: VaultItem, original: VaultItem | undefined): VaultItem {
  const uris = draft.uris.map((u) => u.trim()).filter(Boolean);
  const fields = draft.fields.filter((f) => f.name.trim() || f.value);
  const values: Record<string, string> = {};
  for (const [k, v] of Object.entries(draft.values)) if (v) values[k] = v;
  let history = draft.history;
  const was = original?.values.password;
  if (was && was !== values.password) history = [{ password: was, at: new Date().toISOString() }, ...history].slice(0, HISTORY_MAX);
  return { ...draft, name: draft.name.trim(), uris, fields, values, history };
}

interface ItemFormProps {
  /** An item to change; otherwise a new one. */
  item?: VaultItem;
  folders: VaultFolder[];
  startFolder?: string;
  startType?: ItemType;
  onSaved: (item: VaultItem) => void;
  onClose: () => void;
}

export function ItemForm({ item, folders, startFolder, startType, onSaved, onClose }: ItemFormProps) {
  const [draft, setDraft] = useState<VaultItem>(() => item ?? emptyItem(startType ?? 'login', '', startFolder ?? ''));
  const [generate, setGenerate] = useState(false);
  const [revealed, setRevealed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [conflict, setConflict] = useState(false);
  const [nameError, setNameError] = useState('');
  const [totpError, setTotpError] = useState('');

  const patch = (p: Partial<VaultItem>) => setDraft((d) => ({ ...d, ...p }));
  const setValue = (key: string, value: string) => setDraft((d) => ({ ...d, values: { ...d.values, [key]: value } }));
  const schema = TYPE_SCHEMA[draft.type];

  const save = async (force: boolean) => {
    setError('');
    setConflict(false);
    setNameError('');
    setTotpError('');
    if (!draft.name.trim()) {
      setNameError('Give it a name.');
      return;
    }
    const totp = draft.values.totp?.trim() ?? '';
    // A kind of key this cannot make codes from may come in with an import: it may stay as it was.
    if (totp && !parseTotp(totp) && totp !== item?.values.totp) {
      setTotpError('Not an authenticator key. Paste the otpauth:// link or its Base32 key.');
      return;
    }
    setBusy(true);
    try {
      onSaved(await saveItem(cleaned(draft, item), { force }));
      onClose();
    } catch (err) {
      setBusy(false);
      if (isConflict(err)) {
        setConflict(true);
        return;
      }
      setError(errorMessage(err));
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void save(false);
  };

  const fieldFor = (f: FieldDef) => {
    const value = draft.values[f.key] ?? '';
    const set = (v: string) => setValue(f.key, v);
    const secret = isSecretField(draft.type, f);
    if (f.kind === 'multiline') {
      return (
        <Field key={f.key} label={f.label} className="vault-span">
          {({ labelId: _l, ...a11y }) => <textarea {...NO_AUTOFILL} {...a11y} className={`control mono vault-textarea ${secret ? 'vault-blur-until-focus' : ''}`} rows={4} value={value} onChange={(e) => set(e.target.value)} />}
        </Field>
      );
    }
    if (f.kind === 'password') {
      return (
        <SecretInput
          key={f.key}
          label={f.label}
          value={value}
          onChange={set}
          revealed={revealed}
          onReveal={setRevealed}
          fieldClassName="vault-span"
          actions={<Button size="sm" variant="ghost" iconOnly icon="sparkle" aria-label="Generate a password" title="Generate a password" onClick={() => setGenerate(true)} />}
        />
      );
    }
    if (secret) {
      return <SecretInput key={f.key} label={f.label} value={value} onChange={set} error={f.kind === 'totp' ? totpError : undefined} hint={f.kind === 'totp' ? 'The otpauth:// link or Base32 key a site gives you for its authenticator.' : undefined} fieldClassName={f.kind === 'totp' ? 'vault-span' : undefined} />;
    }
    return <TextField key={f.key} label={f.label} mono={f.mono} value={value} fieldClassName={f.wide ? 'vault-span' : undefined} onChange={(e) => set(e.target.value)} {...NO_AUTOFILL} />;
  };

  return (
    <Modal
      title={item ? `Edit ${item.name}` : `New ${TYPE_SCHEMA[draft.type].label.toLowerCase()}`}
      icon={schema.icon}
      size="md"
      onClose={onClose}
      dismissable={false}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          {conflict ? (
            <Button variant="danger" loading={busy} onClick={() => void save(true)}>
              Save over it
            </Button>
          ) : (
            <Button variant="primary" type="submit" form="vault-item-form" loading={busy}>
              Save
            </Button>
          )}
        </>
      }
    >
      <form id="vault-item-form" className="vault-form" onSubmit={submit}>
        {!item && (
          <SegmentedControl
            label="Type"
            block
            size="sm"
            value={draft.type}
            className="vault-span"
            onChange={(type) => patch({ type, uris: type === 'login' ? (draft.uris.length ? draft.uris : ['']) : draft.uris })}
            options={ITEM_TYPES.map((t) => ({ value: t, label: SHORT_LABEL[t] }))}
          />
        )}
        <TextField label="Name" autoFocus value={draft.name} maxLength={200} error={nameError} onChange={(e) => patch({ name: e.target.value })} {...NO_AUTOFILL} />
        <SelectField label="Folder" value={draft.folderId} onChange={(e) => patch({ folderId: e.target.value })}>
          <option value="">No folder</option>
          {[...folders].sort(byName).map((f) => (
            <option key={f.id} value={f.id}>
              {f.name}
            </option>
          ))}
        </SelectField>
        {schema.fields.map(fieldFor)}

        {draft.type === 'login' && (
          <div className="vault-span vault-list-edit">
            <span className="field-label">Addresses</span>
            {draft.uris.map((uri, i) => (
              <div className="vault-input-row" key={i}>
                <input
                  className="control mono"
                  {...NO_AUTOFILL}
                  aria-label={`Address ${i + 1}`}
                  placeholder="https://example.com"
                  value={uri}
                  onChange={(e) => patch({ uris: draft.uris.map((u, j) => (j === i ? e.target.value : u)) })}
                />
                <Button size="sm" variant="ghost" iconOnly icon="minus" aria-label={`Remove address ${i + 1}`} title="Remove" onClick={() => patch({ uris: draft.uris.filter((_, j) => j !== i) })} />
              </div>
            ))}
            <Button size="sm" variant="ghost" icon="plus" onClick={() => patch({ uris: [...draft.uris, ''] })}>
              Add address
            </Button>
          </div>
        )}

        <Field label="Notes" className="vault-span">
          {({ labelId: _l, ...a11y }) => <textarea {...NO_AUTOFILL} {...a11y} className="control vault-textarea" rows={4} maxLength={NOTES_MAX} value={draft.notes} onChange={(e) => patch({ notes: e.target.value })} />}
        </Field>

        <div className="vault-span vault-list-edit">
          <span className="field-label">Custom fields</span>
          {draft.fields.map((f, i) => {
            const change = (p: Partial<typeof f>) => patch({ fields: draft.fields.map((x, j) => (j === i ? { ...x, ...p } : x)) });
            return (
              <div className="vault-custom" key={i}>
                <input className="control" {...NO_AUTOFILL} aria-label={`Field ${i + 1} name`} placeholder="Name" value={f.name} onChange={(e) => change({ name: e.target.value })} />
                <input className="control mono" {...NO_AUTOFILL} autoComplete="new-password" aria-label={`Field ${i + 1} value`} placeholder="Value" type={f.hidden ? 'password' : 'text'} value={f.value} onChange={(e) => change({ value: e.target.value })} />
                <Checkbox checked={f.hidden} onChange={(hidden) => change({ hidden })} label="Hidden" />
                <Button size="sm" variant="ghost" iconOnly icon="minus" aria-label={`Remove field ${i + 1}`} title="Remove" onClick={() => patch({ fields: draft.fields.filter((_, j) => j !== i) })} />
              </div>
            );
          })}
          <Button size="sm" variant="ghost" icon="plus" onClick={() => patch({ fields: [...draft.fields, { name: '', value: '', hidden: true }] })}>
            Add field
          </Button>
        </div>

        <div className="vault-span">
          <Checkbox checked={draft.favorite} onChange={(favorite) => patch({ favorite })} label="Favorite" />
        </div>

        {conflict && (
          <div className="alert warning vault-span" role="alert">
            This item changed in another tab or on another device. "Save over it" replaces that version with yours; Cancel keeps it and drops your edit.
          </div>
        )}
        {error && (
          <div className="alert vault-span" role="alert">
            {error}
          </div>
        )}
      </form>
      {generate && (
        <GeneratorDialog
          onClose={() => setGenerate(false)}
          onUse={(v) => {
            setValue('password', v);
            setRevealed(true);
            setGenerate(false);
          }}
        />
      )}
    </Modal>
  );
}
