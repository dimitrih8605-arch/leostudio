// Lightweight replay channel between Library and Generate pages.
// We piggyback on localStorage + a custom event so consumers don't need
// React Router state. The channel is intentionally narrow: a target page,
// the prompt, and the optional aspect ratio.

const KEY = "leostudio-replay";
const EVENT = "leostudio:replay";

export type ReplayTarget = "image" | "video";

export type ReplayPayload = {
  target: ReplayTarget;
  prompt: string;
  aspectRatio?: string;
  modelId?: string;
};

export function setReplay(payload: ReplayPayload) {
  localStorage.setItem(KEY, JSON.stringify(payload));
  window.dispatchEvent(new CustomEvent(EVENT));
}

export function consumeReplay(target: ReplayTarget): ReplayPayload | null {
  const raw = localStorage.getItem(KEY);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as ReplayPayload;
    if (parsed.target !== target) return null;
    localStorage.removeItem(KEY);
    return parsed;
  } catch {
    localStorage.removeItem(KEY);
    return null;
  }
}

// Subscribe on a generate page so it picks up replays even when target
// switches without a remount.
export function onReplay(handler: () => void) {
  const cb = () => handler();
  window.addEventListener(EVENT, cb);
  return () => window.removeEventListener(EVENT, cb);
}
