import type { Upstream } from "./api";

/** Quotes a YAML scalar when it could be misread (empty, special characters, or looks like another type). */
function scalar(v: string | number | boolean): string {
  if (typeof v !== "string") return String(v);
  if (v === "" || /^[\s>|&*!%@`#'"{}[\],?:-]|[:#]\s|\s$|^(true|false|null|yes|no|on|off|~)$/i.test(v) || /^[\d.+-]/.test(v)) {
    return JSON.stringify(v);
  }
  return v;
}

/** `equals "FAILED"` → `equals: "FAILED"`; `exists` → `exists: true`. */
function condition(c: string): string {
  if (c === "exists") return "exists: true";
  if (c === "is missing") return "exists: false";
  const space = c.indexOf(" ");
  return `${c.slice(0, space)}: ${c.slice(space + 1)}`;
}

/**
 * The upstream's resolved configuration as a `hookyard.yaml` entry, with defaults filled in. Header
 * values are never available to the dashboard, so they appear as placeholders.
 */
export function upstreamYaml(u: Upstream): string {
  const out: string[] = [`${u.name}:`, `  base_url: ${scalar(u.base_url)}`, `  timeout: ${u.timeout}`];
  const r = u.retry;
  out.push("  retry:");
  if (r.preset) out.push(`    preset: ${r.preset}`);
  out.push(
    `    max_attempts: ${r.max_attempts}`,
    `    initial_interval: ${r.initial_interval}`,
    `    max_interval: ${r.max_interval}`,
    `    multiplier: ${r.multiplier}`,
    `    max_age: ${r.max_age}`,
  );
  out.push(`  dedupe_window: ${u.dedupe_window}`);
  if (u.limits.rate_limit) out.push(`  rate_limit: ${u.limits.rate_limit}`, `  burst: ${u.limits.burst}`);
  if (u.limits.max_concurrency) out.push(`  max_concurrency: ${u.limits.max_concurrency}`);
  if (u.breaker) {
    const b = u.breaker;
    out.push(
      "  breaker:",
      `    failure_rate: ${b.failure_rate}`,
      `    min_calls: ${b.min_calls}`,
      `    window: ${b.window}`,
      `    consecutive_failures: ${b.consecutive_failures}`,
      `    cooldown: ${b.cooldown}`,
      `    probes: ${b.probes}`,
    );
  } else {
    out.push("  breaker: off");
  }
  out.push(`  on_timeout: ${u.on_timeout}`);
  if (u.callback_url) out.push(`  callback_url: ${scalar(u.callback_url)}`);
  if (u.classify.length) {
    out.push("  classify:");
    u.classify.forEach((rule, i) => {
      const lines: string[] = [];
      if (rule.name !== `rule ${i + 1}`) lines.push(`name: ${scalar(rule.name)}`);
      if (rule.status) lines.push(`status: ${scalar(rule.status)}`);
      if (rule.body) lines.push(`body: ${scalar(rule.body)}`);
      if (rule.condition) lines.push(condition(rule.condition));
      lines.push(`then: ${rule.then}`);
      lines.forEach((l, j) => out.push(`${j === 0 ? "    - " : "      "}${l}`));
    });
  }
  if (u.header_names.length) {
    out.push("  headers: # values are hidden");
    for (const h of u.header_names) out.push(`    ${h}: "\${${h.toUpperCase().replace(/[^A-Z0-9]+/g, "_")}}"`);
  }
  return out.join("\n");
}
