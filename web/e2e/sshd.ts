import { spawn, execFileSync, type ChildProcess } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { join } from 'node:path';
import type { Hub } from './hub';

/**
 * A throwaway OpenSSH server on a loopback port, with its own host and user keys, as
 * internal/machine's tests make one. Nothing in ~/.ssh is touched. It is a server as the hub
 * meets one: SFTP subsystem, the default MaxSessions of 10.
 */
export class SSHD {
  private proc: ChildProcess | null = null;
  private constructor(readonly dir: string, readonly port: number, readonly userKey: string) {}

  static available() {
    return existsSync('/usr/sbin/sshd');
  }

  static async start(extra: string[] = []): Promise<SSHD> {
    const dir = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'sshd-'));
    for (const k of ['hostkey', 'user']) execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', join(dir, k)]);
    writeFileSync(join(dir, 'authorized_keys'), readFileSync(join(dir, 'user.pub')), { mode: 0o600 });
    const port = await freePort();
    const config = [
      `Port ${port}`,
      'ListenAddress 127.0.0.1',
      `HostKey ${dir}/hostkey`,
      `PidFile ${dir}/sshd.pid`,
      `AuthorizedKeysFile ${dir}/authorized_keys`,
      'UsePAM no',
      'PasswordAuthentication no',
      'KbdInteractiveAuthentication no',
      'PubkeyAuthentication yes',
      'StrictModes no',
      'Subsystem sftp internal-sftp',
      ...extra,
    ];
    writeFileSync(join(dir, 'sshd_config'), config.join('\n') + '\n', { mode: 0o600 });
    const s = new SSHD(dir, port, readFileSync(join(dir, 'user'), 'utf8'));
    // `timeout` so that a run killed halfway leaves no server behind on a shared machine
    s.proc = spawn('timeout', ['1800', '/usr/sbin/sshd', '-D', '-e', '-f', join(dir, 'sshd_config')], { stdio: 'ignore' });
    for (let i = 0; i < 50; i++) {
      if (await listening(port)) return s;
      await new Promise((r) => setTimeout(r, 100));
    }
    s.stop();
    throw new Error('sshd did not listen');
  }

  /** The machine in the hub, connected, its host key taken. */
  async machine(hub: Hub, name: string): Promise<string> {
    let m = await hub.call('POST', '/api/machines', { name, host: '127.0.0.1', port: this.port, authMethod: 'key', privateKey: this.userKey, persistMode: 'direct' });
    if (m.status === 'hostkey') m = await hub.call('POST', `/api/machines/${m.id}/hostkey`, { accept: true, fingerprint: m.pendingHostKey.fingerprint });
    if (m.status !== 'connected') throw new Error(`machine ${name}: ${m.status} ${m.error ?? ''}`);
    return m.id;
  }

  stop() {
    this.proc?.kill('SIGTERM');
    this.proc = null;
  }
}

function freePort(): Promise<number> {
  return new Promise((done, fail) => {
    const srv = createServer();
    srv.once('error', fail);
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as { port: number }).port;
      srv.close(() => done(port));
    });
  });
}

function listening(port: number): Promise<boolean> {
  return new Promise((done) => {
    const srv = createServer();
    srv.once('error', () => done(true));
    srv.listen(port, '127.0.0.1', () => srv.close(() => done(false)));
  });
}
