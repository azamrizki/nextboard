import { describe, expect, it } from 'vitest';
import {
  asFileKey,
  authorityOf,
  cleanAbs,
  fileKey,
  joinKey,
  keyOfUri,
  keyUnder,
  machineOfAuthority,
  parentKey,
  ROOT_KEY,
  splitKey,
  storedPaths,
  under,
  uriPartsOf,
  viewKey,
  ViewPaths,
  type FileKey,
  type Mount,
} from './paths';

const mount = (partId: string, name: string, machineId: string, root: string): Mount => ({ partId, name, machineId, root, rootKey: fileKey(machineId, root), ref: `p_shop~${partId}` });

const admin = mount('pt_admin', 'admin', 'srv-a', '/srv/shop/admin');
const front = mount('pt_front', 'frontend', 'local', '/home/me/shop-front');
const root = new ViewPaths([admin, front], true);

describe('file keys', () => {
  it('round-trips machine and path, This PC being the path itself', () => {
    for (const [machineId, abs] of [
      ['local', '/home/me/x.ts'],
      ['srv-a', '/srv/app/x.ts'],
      ['Srv_A', '/'],
      ['sérveur-東京', '/data/a b/ü.txt'],
    ]) {
      const k = fileKey(machineId, abs);
      expect(splitKey(k)).toEqual({ machineId, abs });
      expect(asFileKey(k)).toBe(k);
    }
    expect(fileKey('local', '/a/b')).toBe('/a/b');
    expect(fileKey('', '/a/b')).toBe('/a/b');
    expect(fileKey('srv-a', '/a')).toBe('//m-7372762d61/a');
  });

  it('gives the same path on two machines two keys', () => {
    expect(fileKey('srv-a', '/srv/x')).not.toBe(fileKey('srv-b', '/srv/x'));
    expect(fileKey('srv-a', '/srv/x')).not.toBe(fileKey('local', '/srv/x'));
  });

  it('cleans the path and refuses a relative one', () => {
    expect(fileKey('local', '/a//b/./c/../d/')).toBe('/a/b/d');
    expect(fileKey('srv-a', '//x/../..')).toBe('//m-7372762d61/');
    expect(() => fileKey('local', 'a/b')).toThrow();
    expect(cleanAbs('x')).toBeNull();
    expect(cleanAbs('/')).toBe('/');
  });

  it('keeps authorities lowercase hex and reads only its own', () => {
    expect(authorityOf('local')).toBe('');
    expect(authorityOf('AB')).toBe('m-4142');
    expect(machineOfAuthority('m-4142')).toBe('AB');
    expect(machineOfAuthority('M-4142'.toLowerCase())).toBe('AB');
    expect(machineOfAuthority('m-41X2')).toBeNull();
    expect(machineOfAuthority('m-')).toBeNull();
    expect(machineOfAuthority('m-ff')).toBeNull(); // not UTF-8
    expect(machineOfAuthority('m-6c6f63616c')).toBeNull(); // "local" is never encoded
    expect(machineOfAuthority('server')).toBeNull();
    expect(machineOfAuthority('')).toBe('local');
  });

  it('maps keys to Monaco URI parts and back', () => {
    for (const k of [fileKey('local', '/home/me/x.ts'), fileKey('srv-a', '/srv/x.ts'), fileKey('東京', '/')]) {
      expect(keyOfUri(uriPartsOf(k))).toBe(k);
    }
    expect(uriPartsOf(fileKey('local', '/a'))).toEqual({ scheme: 'file', authority: '', path: '/a' });
    expect(keyOfUri({ scheme: 'file', authority: 'M-7372762D61', path: '/a' })).toBe(fileKey('srv-a', '/a'));
    expect(keyOfUri({ scheme: 'inmemory', authority: '', path: '/a' })).toBeNull();
    expect(keyOfUri({ scheme: 'file', authority: 'host', path: '/a' })).toBeNull();
  });

  it('refuses what is not a key', () => {
    for (const raw of ['', 'a/b', '//m-zz/a', '//m-7372762d61', 'nb:other']) expect(asFileKey(raw)).toBeNull();
    expect(asFileKey('/a//b/')).toBe('/a/b');
    expect(asFileKey(ROOT_KEY)).toBe(ROOT_KEY);
    expect(() => splitKey(ROOT_KEY)).toThrow();
    expect(() => splitKey('//m-zz/a' as FileKey)).toThrow();
  });

  it('joins and walks up keys', () => {
    const r = fileKey('srv-a', '/');
    expect(joinKey(r, 'etc/x')).toBe(fileKey('srv-a', '/etc/x'));
    expect(joinKey(fileKey('local', '/a'), 'b')).toBe('/a/b');
    expect(parentKey(fileKey('srv-a', '/etc/x'))).toBe(fileKey('srv-a', '/etc'));
    expect(parentKey(fileKey('srv-a', '/etc'))).toBe(r);
    expect(parentKey(r)).toBeNull();
    expect(parentKey(ROOT_KEY)).toBeNull();
  });
});

describe('under: the "/" boundary', () => {
  it('holds a folder and what is inside it only', () => {
    expect(under('/srv/app', '/srv/app')).toBe(true);
    expect(under('/srv/app/x', '/srv/app')).toBe(true);
    expect(under('/srv/app2/x', '/srv/app')).toBe(false);
    expect(under('/srv/ap', '/srv/app')).toBe(false);
    expect(under('/srv/app/x', '/srv/app/')).toBe(true);
    expect(under('/anything', '/')).toBe(true);
    expect(under('/', '/')).toBe(true);
  });

  it('keys: the same machine too', () => {
    expect(keyUnder(fileKey('srv-a', '/srv/app/x'), fileKey('srv-a', '/srv/app'))).toBe(true);
    expect(keyUnder(fileKey('srv-a', '/srv/app2/x'), fileKey('srv-a', '/srv/app'))).toBe(false);
    expect(keyUnder(fileKey('srv-b', '/srv/app/x'), fileKey('srv-a', '/srv/app'))).toBe(false);
    expect(keyUnder(fileKey('srv-a', '/x'), fileKey('srv-a', '/'))).toBe(true);
    expect(keyUnder(fileKey('local', '/x'), ROOT_KEY)).toBe(false);
    expect(keyUnder(ROOT_KEY, ROOT_KEY)).toBe(true);
  });
});

describe('ViewPaths, identity mode', () => {
  const one = new ViewPaths([front], false);
  const remote = new ViewPaths([admin], false);

  it('keeps today labels: absolute path and rel', () => {
    const k = fileKey('local', '/home/me/shop-front/src/x.ts');
    expect(one.rootKey).toBe('/home/me/shop-front');
    expect(one.label(k)).toBe('/home/me/shop-front/src/x.ts');
    expect(one.relLabel(k)).toBe('src/x.ts');
    expect(one.relLabel(front.rootKey)).toBe('');
    expect(one.relLabel(fileKey('local', '/usr/lib/x.d.ts'))).toBe('/usr/lib/x.d.ts');
    expect(one.partRel(k)).toEqual({ mount: front, rel: 'src/x.ts' });
    expect(one.partRel(fileKey('local', '/home/me/shop-front2/x'))).toBeNull();
  });

  it('resolves rel and absolute paths on the part machine', () => {
    expect(one.resolve('src/x.ts')).toBe('/home/me/shop-front/src/x.ts');
    expect(one.resolve('./src/x.ts')).toBe('/home/me/shop-front/src/x.ts');
    expect(one.resolve('.')).toBe(front.rootKey);
    expect(one.resolve('/etc/hosts')).toBe('/etc/hosts');
    expect(remote.resolve('/etc/hosts')).toBe(fileKey('srv-a', '/etc/hosts'));
    expect(remote.resolve('/etc/hosts', 'local')).toBe('/etc/hosts');
    expect(one.resolve('../x')).toBeNull();
    expect(one.resolve('  ')).toBeNull();
  });

  it('routes every glob to all', () => {
    expect([...one.routeGlobs('frontend/**, *.ts')]).toEqual([['*', 'frontend/**,*.ts']]);
  });
});

describe('ViewPaths, the Root view', () => {
  const ka = fileKey('srv-a', '/srv/shop/admin/src/x.ts');

  it('labels "/<part>/…"', () => {
    expect(root.rootKey).toBe(ROOT_KEY);
    expect(root.label(ROOT_KEY)).toBe('/');
    expect(root.label(ka)).toBe('/admin/src/x.ts');
    expect(root.relLabel(ka)).toBe('admin/src/x.ts');
    expect(root.label(admin.rootKey)).toBe('/admin');
    expect(root.relLabel(admin.rootKey)).toBe('admin');
    expect(root.label(fileKey('srv-a', '/usr/lib/x.d.ts'))).toBe('admin: /usr/lib/x.d.ts');
    expect(root.label(fileKey('srv-z', '/x'))).toBe('srv-z: /x');
    expect(root.relLabel(fileKey('srv-a', '/usr/lib/x.d.ts'))).toBe('/usr/lib/x.d.ts');
  });

  it('round-trips "/<part>/rel" and the key', () => {
    for (const k of [ka, fileKey('local', '/home/me/shop-front/a/b.md'), admin.rootKey, front.rootKey]) {
      expect(root.resolve(root.label(k))).toBe(k);
      expect(root.resolve(root.relLabel(k))).toBe(k);
    }
    expect(root.resolve('./admin/src/x.ts')).toBe(ka);
    expect(root.resolve('ADMIN/src/x.ts')).toBe(ka);
    expect(root.resolve('/')).toBe(ROOT_KEY);
  });

  it('refuses unknown parts and ".."', () => {
    expect(root.resolve('api/x')).toBeNull();
    expect(root.resolve('/admin/../frontend/x')).toBeNull();
    expect(root.resolve('src/x')).toBeNull();
  });

  it('reads an absolute path as the part holding it, or on the machine given', () => {
    expect(root.resolve('/home/me/shop-front/x')).toBe(fileKey('local', '/home/me/shop-front/x'));
    expect(root.resolve('/srv/shop/admin/src/x.ts')).toBe(ka);
    expect(root.resolve('/opt/elsewhere')).toBeNull();
    expect(root.resolve('/admin/x', 'local')).toBe('/admin/x');
  });

  it('finds the mount on the right machine, the longest root', () => {
    const outer = mount('pt_o', 'outer', 'srv-a', '/srv');
    const inner = mount('pt_i', 'inner', 'srv-a', '/srv/shop');
    const other = mount('pt_b', 'other', 'srv-b', '/srv/shop/admin');
    const v = new ViewPaths([outer, inner, other], true);
    expect(v.mountOf(fileKey('srv-a', '/srv/shop/admin/x'))).toBe(inner);
    expect(v.mountOf(fileKey('srv-a', '/srv/x'))).toBe(outer);
    expect(v.mountOf(fileKey('srv-b', '/srv/shop/admin/x'))).toBe(other);
    expect(v.mountOf(fileKey('srv-b', '/srv/x'))).toBeNull();
    expect(v.mountOf(ROOT_KEY)).toBeNull();
  });

  it('routes globs by their leading part name', () => {
    expect(Object.fromEntries(root.routeGlobs('admin/**, *.ts, Frontend/src/*.vue, frontend, ./admin/x/'))).toEqual({
      pt_admin: '**,x/',
      '*': '*.ts',
      pt_front: 'src/*.vue,**',
    });
    expect(root.routeGlobs('').size).toBe(0);
  });

  it('needs a mount', () => {
    expect(() => new ViewPaths([], true)).toThrow();
  });
});

describe('viewKey and saved paths', () => {
  it('keys a single-part project by its id, a part card by ~part, the Root card by @root', () => {
    expect(viewKey('p_shop', 1, { root: true })).toBe('p_shop');
    expect(viewKey('p_shop', 1, { partId: 'pt_a' })).toBe('p_shop');
    expect(viewKey('p_shop', 2, { partId: 'pt_a' })).toBe('p_shop~pt_a');
    expect(viewKey('p_shop', 3, { root: true })).toBe('p_shop@root');
  });

  it('reads v1 paths on the part machine and v2 keys as they are', () => {
    const v1local = storedPaths({ groups: [] }, 'local');
    expect(v1local('/home/me/x.ts')).toBe('/home/me/x.ts');
    const v1remote = storedPaths({}, 'srv-a');
    expect(v1remote('/srv/x.ts')).toBe(fileKey('srv-a', '/srv/x.ts'));
    expect(v1remote('x.ts')).toBeNull();
    const v2 = storedPaths({ v: 2 }, 'local');
    const k = fileKey('srv-a', '/srv/x.ts');
    expect(v2(k)).toBe(k);
    expect(v2('/home/me/x.ts')).toBe('/home/me/x.ts');
    expect(v2('garbage')).toBeNull();
    expect(storedPaths(null, 'local')('/a')).toBe('/a');
  });
});
