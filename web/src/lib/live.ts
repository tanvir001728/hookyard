import { useEffect, useRef, useState } from "react";
import type { Schemas } from "./api";

export type LiveEvent = Schemas["Event"] & { id: string };
export type ConnectionState = "connecting" | "open" | "reconnecting";

const TYPES = ["attempt", "request", "upstream"] as const;

/**
 * Subscribes to GET /v1/events with EventSource (which sends the session cookie and reconnects on
 * its own) and calls onEvent for each event. Changing `query` opens a new stream.
 */
export function useEventStream(query: string, onEvent: (e: LiveEvent) => void): { state: ConnectionState; droppedAt: Date | null } {
  const [state, setState] = useState<ConnectionState>("connecting");
  const [droppedAt, setDroppedAt] = useState<Date | null>(null);
  // Keep the latest callback without reopening the stream.
  const handler = useRef(onEvent);
  handler.current = onEvent;

  useEffect(() => {
    setState("connecting");
    const source = new EventSource(`/v1/events${query ? `?${query}` : ""}`);
    source.onopen = () => setState("open");
    // EventSource retries by itself; CONNECTING means a retry is pending.
    source.onerror = () => setState("reconnecting");
    const listener = (msg: MessageEvent<string>) => {
      try {
        handler.current({ ...(JSON.parse(msg.data) as Schemas["Event"]), id: msg.lastEventId });
      } catch {
        // Ignore a malformed message rather than breaking the stream.
      }
    };
    for (const t of TYPES) source.addEventListener(t, listener as EventListener);
    // The server ends the stream of a client that fell behind; EventSource reconnects.
    source.addEventListener("dropped", () => setDroppedAt(new Date()));
    return () => source.close();
  }, [query]);

  return { state, droppedAt };
}
