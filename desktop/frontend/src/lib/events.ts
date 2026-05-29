// Wails injects EventsOn / EventsOff at window.runtime. We expose typed
// helpers here so pages don't reach into that namespace directly.

import { useEffect } from "react";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function runtime(): any {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const w = window as any;
  return w.runtime ?? null;
}

export function onEvent(name: string, handler: (...args: unknown[]) => void) {
  const rt = runtime();
  if (!rt || !rt.EventsOn) return () => undefined;
  rt.EventsOn(name, handler);
  return () => {
    rt.EventsOff(name, handler);
  };
}

// Convenience hook for components that want to refetch when an event fires.
export function useWailsEvent(name: string, handler: () => void) {
  useEffect(() => {
    return onEvent(name, handler);
  }, [name, handler]);
}
