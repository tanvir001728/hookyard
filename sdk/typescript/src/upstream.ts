import type { Job } from "./job.js";
import type { HttpMethod, SendInput, SendOptions, UpstreamClient } from "./types.js";

/** Builds the `to(upstream)` helper on top of a `send` function. */
export function createUpstreamClient(send: (input: SendInput) => Promise<Job>, upstream: string): UpstreamClient {
  const withoutBody = (method: HttpMethod) => (path: string, options?: SendOptions) =>
    send({ ...options, upstream, method, path });
  const withBody = (method: HttpMethod) => (path: string, body?: unknown, options?: SendOptions) =>
    send({ ...options, upstream, method, path, ...(body !== undefined && { body }) });

  return {
    upstream,
    get: withoutBody("GET"),
    delete: withoutBody("DELETE"),
    post: withBody("POST"),
    put: withBody("PUT"),
    patch: withBody("PATCH"),
  };
}
