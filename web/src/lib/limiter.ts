/**
 * Runs at most max calls at a time. A browser opens about six connections to the hub, and calls
 * that wait on a slow SSH machine would otherwise fill them all, leaving whatever the user just
 * clicked waiting in line.
 */
export function limiter(max: number) {
  let running = 0;
  const waiting: (() => void)[] = [];
  return async <T>(fn: () => Promise<T>): Promise<T> => {
    if (running >= max) await new Promise<void>((resolve) => waiting.push(resolve));
    else running++;
    try {
      return await fn();
    } finally {
      const next = waiting.shift();
      if (next) next(); // the slot passes on directly
      else running--;
    }
  };
}
