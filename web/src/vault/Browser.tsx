// The open vault: sections, list and the chosen item. All three fit on a wide card; a narrower one
// turns the sections into a select, and a narrow one shows the list or the item.

import { useEffect, useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon, type IconName } from '../components/Icon';
import { Menu, type MenuEntry } from '../components/Menu';
import { toast } from '../components/Toast';
import { formatDateTime } from '../lib/format';
import { FolderDialog } from './Dialogs';
import { LeakCheck } from './Leaks';
import { copyValue, CopyButton } from './fields';
import { byName, hostOf, inNav, isSecretField, itemSubtitle, ITEM_TYPES, matchesQuery, safeHref, TRASH_DAYS, TYPE_SCHEMA, type FieldDef, type NavKey, type VaultFolder, type VaultItem } from './model';
import { deleteFolder, deleteRecord, emptyTrash, restoreItem, toggleFavorite, trashItem, type VaultState } from './store';
import { formatCode, parseTotp, totpCode, type TotpSpec } from './totp';

const MASK = '••••••••••••';

function run(fn: () => Promise<unknown>, done?: string) {
  void fn().then(
    () => done && toast.success(done),
    (err: unknown) => toast.error(errorMessage(err)),
  );
}

// ---- detail ----

function Value({ label, value, mono, secret, multiline }: { label: string; value: string; mono?: boolean; secret?: boolean; multiline?: boolean }) {
  const [shown, setShown] = useState(false);
  return (
    <div className="vault-value">
      <span className="vault-value-label">{label}</span>
      <div className="vault-value-row">
        {/* A masked value is not in the page at all, and the mask does not give away its length. */}
        <span className={`vault-value-text ${mono ? 'mono' : ''} ${multiline ? 'multiline' : ''}`}>{secret && !shown ? MASK : value}</span>
        {secret && <Button size="sm" variant="ghost" iconOnly icon={shown ? 'eyeOff' : 'eye'} aria-label={`${shown ? 'Hide' : 'Show'} ${label.toLowerCase()}`} aria-pressed={shown} title={shown ? 'Hide' : 'Show'} onClick={() => setShown(!shown)} />}
        <CopyButton value={value} label={label} secret={!!secret} />
      </div>
    </div>
  );
}

function TotpValue({ spec }: { spec: TotpSpec }) {
  const [code, setCode] = useState('');
  const [left, setLeft] = useState(spec.period);
  useEffect(() => {
    let stop = false;
    let step = -1;
    const tick = () => {
      const now = Date.now();
      setLeft(spec.period - (Math.floor(now / 1000) % spec.period));
      const s = Math.floor(now / 1000 / spec.period);
      if (s === step) return;
      step = s;
      void totpCode(spec, now).then((c) => !stop && setCode(c));
    };
    tick();
    const t = window.setInterval(tick, 1000);
    return () => {
      stop = true;
      window.clearInterval(t);
    };
  }, [spec]);
  return (
    <div className="vault-value">
      <span className="vault-value-label">Authenticator code</span>
      <div className="vault-value-row">
        <span className="vault-value-text mono vault-totp" data-testid="vault-totp">
          {formatCode(code)}
        </span>
        <span className="vault-totp-left tabular" title="Seconds until the next code">
          {left}s
        </span>
        <CopyButton value={code} label="Code" secret />
      </div>
    </div>
  );
}

function Uri({ uri }: { uri: string }) {
  const href = safeHref(uri);
  return (
    <div className="vault-value">
      <span className="vault-value-label">Address</span>
      <div className="vault-value-row">
        <span className="vault-value-text mono" title={uri}>
          {hostOf(uri)}
        </span>
        {href && (
          <a className="btn btn-ghost btn-sm btn-icon" href={href} target="_blank" rel="noreferrer noopener" aria-label={`Open ${hostOf(uri)}`} title="Open">
            <Icon name="external" />
          </a>
        )}
        <CopyButton value={uri} label="Address" secret={false} />
      </div>
    </div>
  );
}

interface DetailProps {
  item: VaultItem;
  folders: VaultFolder[];
  onBack: () => void;
  onEdit: () => void;
  onDeleted: () => void;
}

function Detail({ item, folders, onBack, onEdit, onDeleted }: DetailProps) {
  const schema = TYPE_SCHEMA[item.type];
  const [confirmDelete, setConfirmDelete] = useState(false);
  const folder = folders.find((f) => f.id === item.folderId);
  const totpRaw = item.values.totp ?? '';
  const totp = useMemo(() => parseTotp(totpRaw), [totpRaw]);

  const field = (f: FieldDef) => {
    const value = item.values[f.key];
    if (!value) return null;
    if (f.kind === 'totp') return totp ? <TotpValue key={`${f.key}:${totpRaw}`} spec={totp} /> : <Value key={f.key} label={f.label} value={value} mono secret />;
    return <Value key={f.key} label={f.label} value={value} mono={f.mono} secret={isSecretField(item.type, f)} multiline={f.kind === 'multiline'} />;
  };

  return (
    <div className="vault-detail nowheel">
      <header className="vault-detail-head">
        <Button size="sm" variant="ghost" iconOnly icon="arrowLeft" className="vault-back" aria-label="Back to the list" onClick={onBack} />
        <span className="vault-avatar" aria-hidden="true">
          <Icon name={schema.icon} />
        </span>
        <div className="vault-detail-title">
          <h3 className="truncate" title={item.name}>
            {item.name}
          </h3>
          <span className="truncate">{[schema.label, folder?.name].filter(Boolean).join(' · ')}</span>
        </div>
        <span className="spacer" />
        {item.deletedAt ? (
          <>
            <Button size="sm" icon="undo" onClick={() => run(() => restoreItem(item), `${item.name} restored.`)}>
              Restore
            </Button>
            <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label="Delete for good" title="Delete for good" onClick={() => setConfirmDelete(true)} />
          </>
        ) : (
          <>
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              icon="star"
              className={item.favorite ? 'vault-star on' : 'vault-star'}
              aria-label={item.favorite ? 'Remove from favorites' : 'Add to favorites'}
              aria-pressed={item.favorite}
              title={item.favorite ? 'Remove from favorites' : 'Add to favorites'}
              onClick={() => run(() => toggleFavorite(item))}
            />
            <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label="Edit" title="Edit" onClick={onEdit} />
            <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label="Move to trash" title="Move to trash" onClick={() => run(() => trashItem(item), `${item.name} moved to trash.`)} />
          </>
        )}
      </header>

      <div className="vault-values">
        {schema.fields.map(field)}
        {item.uris.map((u) => (
          <Uri key={u} uri={u} />
        ))}
        {item.fields.map((f) => (
          <Value key={`${f.name}:${f.value}`} label={f.name || 'Field'} value={f.value} secret={f.hidden} mono={f.hidden} />
        ))}
        {item.notes && <Value label="Notes" value={item.notes} multiline />}
      </div>
      {item.type === 'login' && !item.deletedAt && item.values.password && <LeakCheck key={item.values.password} password={item.values.password} />}

      {!!item.passkeys?.length && (
        <div className="vault-passkeys" data-testid="vault-passkeys">
          <h3 className="vault-note">Passkeys</h3>
          {item.passkeys.map((k) => (
            <p key={k.credentialId} className="vault-note">
              {k.userName || 'No account name'} on {k.rpId}
              {k.createdAt ? `, made ${formatDateTime(k.createdAt)}` : ''}
            </p>
          ))}
        </div>
      )}
      {item.history.length > 0 && (
        <details className="vault-history">
          <summary>Password history ({item.history.length})</summary>
          {item.history.map((h) => (
            <Value key={h.at + h.password} label={formatDateTime(h.at)} value={h.password} mono secret />
          ))}
        </details>
      )}

      <p className="vault-note vault-stamps">
        {item.deletedAt ? `In the trash since ${formatDateTime(item.deletedAt)}. Deleted for good after ${TRASH_DAYS} days.` : `Changed ${formatDateTime(item.updatedAt)}. Created ${formatDateTime(item.createdAt)}.`}
      </p>

      {confirmDelete && (
        <ConfirmDialog
          title={`Delete ${item.name} for good?`}
          confirmLabel="Delete for good"
          danger
          onClose={() => setConfirmDelete(false)}
          onConfirm={async () => {
            await deleteRecord(item);
            onDeleted();
          }}
        >
          <p>It cannot be brought back.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

// ---- list ----

function Row({ item, active, onPick }: { item: VaultItem; active: boolean; onPick: () => void }) {
  const schema = TYPE_SCHEMA[item.type];
  const sub = itemSubtitle(item);
  const { username, password } = item.values;
  return (
    <li className={`vault-row ${active ? 'is-selected' : ''}`}>
      <button type="button" className="vault-row-main" aria-current={active || undefined} onClick={onPick}>
        <span className="vault-avatar" aria-hidden="true">
          <Icon name={schema.icon} />
        </span>
        <span className="vault-row-text">
          <span className="vault-row-name truncate">
            {item.name}
            {item.favorite && <Icon name="star" className="vault-row-star" label="Favorite" />}
          </span>
          {sub && <span className="vault-row-sub truncate">{sub}</span>}
        </span>
      </button>
      {item.type === 'login' && !item.deletedAt && (username || password) && (
        <span className="vault-row-actions">
          {username && <Button size="sm" variant="ghost" iconOnly icon="user" aria-label={`Copy username of ${item.name}`} title="Copy username" onClick={() => copyValue(username, 'Username', false)} />}
          {password && <Button size="sm" variant="ghost" iconOnly icon="key" aria-label={`Copy password of ${item.name}`} title="Copy password" onClick={() => copyValue(password, 'Password', true)} />}
        </span>
      )}
    </li>
  );
}

function NavButton({ nav, current, icon, label, count, onPick }: { nav: NavKey; current: NavKey; icon: IconName; label: string; count?: number; onPick: (n: NavKey) => void }) {
  return (
    <button type="button" className={`vault-nav-btn ${nav === current ? 'is-selected' : ''}`} aria-current={nav === current || undefined} onClick={() => onPick(nav)}>
      <Icon name={icon} />
      <span className="truncate">{label}</span>
      {count !== undefined && count > 0 && <span className="vault-nav-count tabular">{count}</span>}
    </button>
  );
}

// ---- everything ----

interface BrowserProps {
  v: VaultState;
  onNew: () => void;
  onEdit: (item: VaultItem) => void;
  onImport: () => void;
  /** The section new items go to. */
  onFolderChange: (folderId: string) => void;
  /** A new item was just saved: show it. */
  showId: string;
}

export function VaultBrowser({ v, onNew, onEdit, onImport, onFolderChange, showId }: BrowserProps) {
  const [nav, setNav] = useState<NavKey>('all');
  const [query, setQuery] = useState('');
  const [picked, setPicked] = useState('');
  const [narrowDetail, setNarrowDetail] = useState(false);
  const [folderDialog, setFolderDialog] = useState<'new' | VaultFolder | null>(null);
  const [removeFolder, setRemoveFolder] = useState<VaultFolder | null>(null);
  const [confirmEmpty, setConfirmEmpty] = useState(false);

  const live = useMemo(() => v.items.filter((i) => !i.deletedAt), [v.items]);
  const trash = v.items.length - live.length;
  const folders = useMemo(() => [...v.folders].sort(byName), [v.folders]);

  const shown = useMemo(() => v.items.filter((i) => inNav(i, nav) && matchesQuery(i, query)).sort(byName), [v.items, nav, query]);
  const current = v.items.find((i) => i.id === picked && inNav(i, nav)) ?? (narrowDetail ? undefined : shown[0]);

  useEffect(() => {
    if (!showId) return;
    setPicked(showId);
    setNarrowDetail(true);
  }, [showId]);

  // A section that is gone (its folder deleted elsewhere) must not leave an empty card.
  useEffect(() => {
    if (nav.startsWith('folder:') && nav !== 'folder:' && !v.folders.some((f) => `folder:${f.id}` === nav)) setNav('all');
  }, [nav, v.folders]);

  const pick = (n: NavKey) => {
    setNav(n);
    setPicked('');
    setNarrowDetail(false);
    onFolderChange(n.startsWith('folder:') ? n.slice(7) : '');
  };

  const countOf = (n: NavKey) => v.items.filter((i) => inNav(i, n)).length;
  const openFolder = nav.startsWith('folder:') && nav !== 'folder:' ? v.folders.find((f) => `folder:${f.id}` === nav) : undefined;
  const folderMenu: MenuEntry[] = openFolder
    ? [
        { label: 'Rename folder…', icon: 'pencil', onSelect: () => setFolderDialog(openFolder) },
        { label: 'Delete folder…', icon: 'trash', danger: true, onSelect: () => setRemoveFolder(openFolder) },
      ]
    : [];

  return (
    <div className={`vault-grid ${narrowDetail ? 'is-picked' : ''}`}>
      <nav className="vault-nav nowheel" aria-label="Vault sections">
        <NavButton nav="all" current={nav} icon="layers" label="All items" count={live.length} onPick={pick} />
        <NavButton nav="favorites" current={nav} icon="star" label="Favorites" count={countOf('favorites')} onPick={pick} />
        <span className="vault-nav-title">Types</span>
        {ITEM_TYPES.map((t) => (
          <NavButton key={t} nav={`type:${t}`} current={nav} icon={TYPE_SCHEMA[t].icon} label={TYPE_SCHEMA[t].plural} count={countOf(`type:${t}`)} onPick={pick} />
        ))}
        <span className="vault-nav-title vault-nav-title-row">
          Folders
          <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="New folder" title="New folder" onClick={() => setFolderDialog('new')} />
        </span>
        {folders.map((f) => (
          <NavButton key={f.id} nav={`folder:${f.id}`} current={nav} icon="folder" label={f.name} count={countOf(`folder:${f.id}`)} onPick={pick} />
        ))}
        {folders.length > 0 && <NavButton nav="folder:" current={nav} icon="folderOpen" label="No folder" count={countOf('folder:')} onPick={pick} />}
        <span className="vault-nav-gap" />
        <NavButton nav="trash" current={nav} icon="trash" label="Trash" count={trash} onPick={pick} />
      </nav>

      <div className="vault-side">
        <div className="vault-side-head">
          <select className="control control-sm vault-nav-select" aria-label="Vault section" value={nav} onChange={(e) => pick(e.target.value)}>
            <option value="all">All items</option>
            <option value="favorites">Favorites</option>
            {ITEM_TYPES.map((t) => (
              <option key={t} value={`type:${t}`}>
                {TYPE_SCHEMA[t].plural}
              </option>
            ))}
            {folders.map((f) => (
              <option key={f.id} value={`folder:${f.id}`}>
                {f.name}
              </option>
            ))}
            {folders.length > 0 && <option value="folder:">No folder</option>}
            <option value="trash">Trash</option>
          </select>
          <input className="control control-sm vault-search" type="search" placeholder="Search" aria-label="Search credentials" value={query} onChange={(e) => setQuery(e.target.value)} autoComplete="off" data-lpignore="true" data-1p-ignore="true" data-bwignore="true" />
          {openFolder && <Menu className="nodrag" label={`Actions for ${openFolder.name}`} items={folderMenu} />}
          {nav === 'trash' && trash > 0 && (
            <Button size="sm" variant="danger-ghost" iconOnly icon="broom" aria-label="Empty trash" title="Empty trash" onClick={() => setConfirmEmpty(true)} />
          )}
        </div>
        {v.broken > 0 && (
          <div className="alert vault-broken" role="alert">
            {v.broken === 1 ? '1 item' : `${v.broken} items`} could not be opened: damaged, or from another key. They stay on the hub untouched.
          </div>
        )}
        {shown.length ? (
          <ul className="plain vault-list nowheel" aria-label="Credentials">
            {shown.map((i) => (
              <Row
                key={i.id}
                item={i}
                active={i.id === current?.id}
                onPick={() => {
                  setPicked(i.id);
                  setNarrowDetail(true);
                }}
              />
            ))}
          </ul>
        ) : (
          <div className="vault-empty">
            {query ? (
              <p>Nothing matches.</p>
            ) : nav === 'trash' ? (
              <p>The trash is empty.</p>
            ) : v.items.length ? (
              <p>Nothing here.</p>
            ) : (
              <>
                <Icon name="key" />
                <h3>No credentials yet</h3>
                <p>Add your first one, or bring them over from Bitwarden.</p>
                <div className="vault-empty-actions">
                  <Button variant="primary" size="sm" icon="plus" onClick={onNew}>
                    New item
                  </Button>
                  <Button size="sm" icon="upload" onClick={onImport}>
                    Import
                  </Button>
                </div>
              </>
            )}
          </div>
        )}
      </div>

      <div className="vault-main">
        {current ? (
          <Detail
            key={current.id}
            item={current}
            folders={v.folders}
            onBack={() => setNarrowDetail(false)}
            onEdit={() => onEdit(current)}
            onDeleted={() => {
              setPicked('');
              setNarrowDetail(false);
            }}
          />
        ) : (
          v.items.length > 0 && (
            <div className="vault-empty">
              <p>Pick an item.</p>
            </div>
          )
        )}
      </div>

      {folderDialog && <FolderDialog folder={folderDialog === 'new' ? undefined : folderDialog} onSaved={(f) => folderDialog === 'new' && pick(`folder:${f.id}`)} onClose={() => setFolderDialog(null)} />}
      {removeFolder && (
        <ConfirmDialog title={`Delete folder ${removeFolder.name}?`} confirmLabel="Delete folder" danger onClose={() => setRemoveFolder(null)} onConfirm={() => deleteFolder(removeFolder)}>
          <p>Only the folder goes. The items in it stay, without a folder.</p>
        </ConfirmDialog>
      )}
      {confirmEmpty && (
        <ConfirmDialog title="Empty the trash?" confirmLabel={`Delete ${trash} for good`} danger onClose={() => setConfirmEmpty(false)} onConfirm={() => emptyTrash()}>
          <p>They cannot be brought back.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
