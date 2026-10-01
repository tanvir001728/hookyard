/** The value shown in place of a sensitive header value. */
export const REDACTED = "••••••";

const sensitive = /^(authorization|proxy-authorization|cookie|set-cookie)$|token|secret|password|api[-_]?key|signature|credential/i;

/** Whether a header name usually carries a secret. */
export function isSensitiveHeader(name: string): boolean {
  return sensitive.test(name);
}

/** Returns headers with sensitive values replaced, for display and copying. */
export function redactHeaders(headers: Record<string, string> | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(headers ?? {})) out[k] = isSensitiveHeader(k) ? REDACTED : v;
  return out;
}
