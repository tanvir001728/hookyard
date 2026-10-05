import type { HookyardRequest } from "./api";
import { redactHeaders } from "./redact";

const sdkMethods: Record<string, string> = { GET: "get", POST: "post", PUT: "put", PATCH: "patch", DELETE: "delete" };

function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

/** The enqueue body that reproduces a request (sensitive headers redacted). */
export function enqueueBody(r: HookyardRequest): Record<string, unknown> {
  const body: Record<string, unknown> = { upstream: r.upstream, method: r.method, path: r.path };
  const headers = redactHeaders(r.headers);
  if (Object.keys(headers).length) body.headers = headers;
  if (r.body !== null && r.body !== undefined) body.body = r.body;
  if (Object.keys(r.tags).length) body.tags = r.tags;
  if (r.callback_url) body.callback_url = r.callback_url;
  if (r.on_result) body.on_result = r.on_result;
  return body;
}

/** A curl command that enqueues the same request again. */
export function toCurl(r: HookyardRequest, origin: string): string {
  return [
    `curl -X POST ${origin}/v1/requests`,
    `  -H "Authorization: Bearer $HOOKYARD_TOKEN"`,
    `  -d ${shellQuote(JSON.stringify(enqueueBody(r)))}`,
  ].join(" \\\n");
}

/** An @hookyard/sdk call that enqueues the same request again. */
export function toSdk(r: HookyardRequest): string {
  const method = sdkMethods[r.method] ?? "post";
  const headers = redactHeaders(r.headers);
  const options: Record<string, unknown> = {};
  if (Object.keys(headers).length) options.headers = headers;
  if (Object.keys(r.tags).length) options.tags = r.tags;
  if (r.callback_url) options.callbackUrl = r.callback_url;
  if (r.on_result) options.onResult = r.on_result;
  const opts = Object.keys(options).length ? JSON.stringify(options, null, 2) : "";

  const args = [JSON.stringify(r.path)];
  if (method === "get" || method === "delete") {
    if (opts) args.push(opts);
  } else {
    args.push(r.body === null || r.body === undefined ? "undefined" : JSON.stringify(r.body, null, 2));
    if (opts) args.push(opts);
  }
  return `await hy.to(${JSON.stringify(r.upstream)}).${method}(${args.join(", ")});`;
}
