// Files dropped from the browser have no path on any machine, so we ask: copy them into
// the project folder (never overwrites: name-2.ext, …) or keep them on the board only
// (images become board assets, text files a snapshot card). Dismissing drops nothing. A project of
// several parts asks which part's folder (v0.9.0-beta.2): the focused tile's by default.

import { useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { RadioCards } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { packUpload } from '../lib/pack';
import type { BoardNode, Project } from '../lib/types';
import { projectParts } from '../lib/parts';
import type { BoardController } from './controller';
import { PlacePicker } from './PlacePicker';
import { ASSET_TYPES, imageNodeFromBlob, looksLikeText, MAX_UPLOAD, nodeForPath, snapshotNodeFromFile } from './files';
import { joinPath } from './kinds';
import { formatBytes } from './tiles/FileTile';

type Mode = 'copy' | 'board';

export function DropDialog({
  files,
  at,
  ctl,
  project,
  onClose,
  partId,
}: {
  files: File[];
  at: { x: number; y: number };
  ctl: BoardController;
  project: Project;
  onClose: () => void;
  /** Several parts: the part to copy into unless another is picked (the focused tile's, else the last used). */
  partId?: string;
}) {
  const parts = projectParts(project);
  // a visible choice: the part asked for, else one on This PC, else the first shown
  const [partPick, setPartPick] = useState(() => (parts.find((p) => p.id === partId) ?? parts.find((p) => (p.machineId || 'local') === 'local') ?? parts[0]).id);
  const part = parts.find((p) => p.id === partPick) ?? parts[0];
  const where = parts.length > 1 ? `${project.name} · ${part.name}` : project.name;
  const keepable = files.filter((f) => ASSET_TYPES.has(f.type) || looksLikeText(f));
  const [mode, setMode] = useState<Mode>('copy');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const tooBig = files.filter((f) => f.size > MAX_UPLOAD);

  const run = async () => {
    setBusy(true);
    setError('');
    const created: BoardNode[] = [];
    let x = at.x;
    try {
      for (const f of files) {
        if (f.size > MAX_UPLOAD) continue;
        const pos = { x, y: at.y };
        let node: BoardNode | null = null;
        if (mode === 'copy') {
          const mid = part.machineId || 'local';
          const stat = await api.fsUpload(mid, await packUpload(f), { path: joinPath(part.path, f.name), unique: true });
          node = await nodeForPath(ctl, mid, stat.path, pos, { centre: false });
        } else if (ASSET_TYPES.has(f.type)) {
          node = await imageNodeFromBlob(ctl, f, pos, f.name, false);
        } else if (looksLikeText(f)) {
          node = await snapshotNodeFromFile(ctl, f, { x: pos.x + 280, y: pos.y + 210 });
        }
        if (node) {
          created.push(node);
          x = node.x + node.w + 32;
        }
      }
      if (created.length) ctl.addNodes(created);
      if (mode === 'copy' && created.length) {
        toast.success(`${created.length === 1 ? '1 file' : `${created.length} files`} copied into ${where}.`);
      }
      onClose();
    } catch (err) {
      if (created.length) ctl.addNodes(created);
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  const options = [
    {
      value: 'copy' as const,
      title: `Copy into ${where}`,
      description: `Writes to ${part.path} on ${parts.length > 1 ? 'that part’s' : 'the project’s'} machine and adds a card that points at the copy.`,
    },
    {
      value: 'board' as const,
      title: 'Keep on the board only',
      description:
        keepable.length === files.length
          ? 'Images are stored on the hub; text files become a read-only snapshot card.'
          : 'Only images and text files can be kept on the board; the others are skipped.',
      disabled: keepable.length === 0,
    },
  ];

  return (
    <Modal
      title={files.length === 1 ? `Add “${files[0].name}”` : `Add ${files.length} files`}
      icon="upload"
      size="md"
      onClose={() => !busy && onClose()}
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" icon={mode === 'copy' ? 'folder' : 'plus'} loading={busy} onClick={() => void run()}>
            {mode === 'copy' ? 'Copy into project' : 'Add to board'}
          </Button>
        </>
      }
    >
      <div className="stack">
        <ul className="drop-files">
          {files.map((f) => (
            <li key={f.name + f.size}>
              <Icon name={f.type.startsWith('image/') ? 'image' : 'file'} />
              <span className="truncate">{f.name}</span>
              <span className="text-3 tabular">{formatBytes(f.size)}</span>
            </li>
          ))}
        </ul>
        <RadioCards<Mode> label="Where should the files go?" value={mode} onChange={setMode} options={options} columns={1} disabled={busy} />
        {mode === 'copy' && <PlacePicker project={project} value={part.id} onChange={setPartPick} label="Into the part" noRoot hint={false} disabled={busy} />}
        {tooBig.length > 0 && (
          <div className="warning-panel">
            <Icon name="alert" />
            <span>{tooBig.map((f) => f.name).join(', ')} is larger than 20 MB and will be skipped.</span>
          </div>
        )}
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
      </div>
    </Modal>
  );
}
