// Files dropped from the browser have no path on any machine, so we ask: copy them into
// the project folder (never overwrites: name-2.ext, …) or keep them on the board only
// (images become board assets, text files a snapshot card). Dismissing drops nothing.

import { useState } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { RadioCards } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import type { BoardNode, Project } from '../lib/types';
import type { BoardController } from './controller';
import { ASSET_TYPES, fileToBase64, imageNodeFromBlob, looksLikeText, MAX_UPLOAD, nodeForPath, snapshotNodeFromFile } from './files';
import { joinPath } from './kinds';
import { formatBytes } from './tiles/FileTile';

type Mode = 'copy' | 'board';

export function DropDialog({
  files,
  at,
  ctl,
  project,
  onClose,
}: {
  files: File[];
  at: { x: number; y: number };
  ctl: BoardController;
  project: Project;
  onClose: () => void;
}) {
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
          const content = await fileToBase64(f);
          const stat = await api.fsWrite(project.machineId, {
            path: joinPath(project.path, f.name),
            content,
            encoding: 'base64',
            unique: true,
          });
          node = await nodeForPath(ctl, project.machineId, stat.path, pos, { centre: false });
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
        toast.success(`${created.length === 1 ? '1 file' : `${created.length} files`} copied into ${project.name}.`);
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
      title: `Copy into ${project.name}`,
      description: `Writes to ${project.path} on the project’s machine and adds a card that points at the copy.`,
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
