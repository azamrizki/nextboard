// What Copy or Cut in a file tree put aside. One per browser tab, shared by the trees of its
// tiles, so a file can go from one project to another on the same machine.

export interface FileClip {
  machineId: string;
  path: string;
  cut: boolean;
}

let clip: FileClip | null = null;
const listeners = new Set<() => void>();

export const fileClip = {
  get: () => clip,
  set(next: FileClip | null) {
    clip = next;
    listeners.forEach((fn) => fn());
  },
  subscribe(fn: () => void) {
    listeners.add(fn);
    return () => {
      listeners.delete(fn);
    };
  },
};
