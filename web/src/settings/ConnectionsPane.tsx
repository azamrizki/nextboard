// Settings › Connections (B21): the GitHub CLI (gh) on every machine that has a project.
// Connect opens a console in that project running `gh auth login`; the token stays in
// gh's own keyring on that machine (the hub never reads it).

import { useEffect, useState } from 'react';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { boardPath, navigate } from '../lib/router';
import { useMachines, useProjects } from '../store';
import { gitError } from '../git/common';
import { gitApi } from '../studio/api';
import type { GitHubStatus } from '../studio/types';

export function ConnectionsPane() {
  const machines = useMachines();
  const projects = useProjects();
  const withProject = machines.map((m) => ({ m, p: projects.find((p) => p.machineId === m.id) })).filter((x) => x.p);
  const [status, setStatus] = useState<Record<string, GitHubStatus | { error: string } | undefined>>({});

  useEffect(() => {
    for (const { m, p } of withProject) {
      if (status[m.id] || !p) continue;
      gitApi
        .github({ pid: p.id, repo: '' })
        .then((g) => setStatus((s) => ({ ...s, [m.id]: g })))
        .catch((err) => setStatus((s) => ({ ...s, [m.id]: { error: gitError(err) } })));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [withProject.length]);

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="cn-gh">
        <h3 id="cn-gh" className="section-title">
          GitHub (gh CLI)
        </h3>
        <p className="setting-hint">Publishing a repository uses the GitHub CLI of the project’s machine. Each machine signs in on its own.</p>
        {!withProject.length && <p className="list-msg">Add a project first; the check runs in a project folder.</p>}
        <ul className="plain conn-list">
          {withProject.map(({ m, p }) => {
            const st = status[m.id];
            const g = st && 'ghInstalled' in st ? st : null;
            return (
              <li key={m.id} className="conn-row">
                <Icon name={m.id === 'local' ? 'monitor' : 'server'} />
                <div className="grow">
                  <div className="fs-sm">{m.name}</div>
                  <div className="fs-xs text-3">
                    {!st ? 'Checking…' : 'error' in st ? st.error : !g?.ghInstalled ? `gh isn’t installed${g?.installHint ? ` — ${g.installHint}` : ''}` : g.authenticated ? `Signed in${g.user ? ` as ${g.user}` : ''}` : 'Not signed in'}
                  </div>
                </div>
                {g?.ghInstalled && !g.authenticated && p && (
                  <Button
                    size="sm"
                    variant="primary"
                    onClick={() =>
                      void gitApi
                        .githubLogin(p.id)
                        .then(() => {
                          toast.info(`A console in ${p.name} runs gh auth login. Finish the sign-in there.`);
                          navigate(boardPath(p.id));
                        })
                        .catch((err) => toast.error(gitError(err)))
                    }
                  >
                    Connect
                  </Button>
                )}
                {g?.authenticated && <span className="badge badge-success">Connected</span>}
              </li>
            );
          })}
        </ul>
      </section>
    </div>
  );
}
