import { useState } from 'react';
import { Icon } from '../components/Icon';
import { guiIconUrl } from './api';

/** An app's own icon, or a generic one when the hub has no file for it. */
export function AppIcon({ appId, className }: { appId: string; className?: string }) {
  const [failed, setFailed] = useState(false);
  if (failed || !appId) return <Icon name="monitor" className={className} />;
  return <img className={className} src={guiIconUrl(appId)} alt="" loading="lazy" draggable={false} onError={() => setFailed(true)} />;
}
