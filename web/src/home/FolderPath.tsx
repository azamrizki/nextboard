/** A folder with its last part always visible: long paths lose their middle. */
export function FolderPath({ path, lead }: { path: string; lead?: string }) {
  const cut = path.replace(/\/+$/, '').lastIndexOf('/') + 1;
  return (
    <span className="project-path mono" title={lead ? `${lead}${path}` : undefined}>
      {lead && <span className="path-lead">{lead}</span>}
      <span className="path-head">{path.slice(0, cut)}</span>
      <span className="path-tail">{path.slice(cut)}</span>
    </span>
  );
}
