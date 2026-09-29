import { formatMilliseconds, toMilliseconds } from "./duration.js";
import { TimeoutError } from "./errors.js";
import { sleep } from "./transport.js";
import { isFinalStatus } from "./types.js";
import type { Duration, HookyardRequest, RequestStatus } from "./types.js";

export interface ResultOptions {
  /** How long to wait for a final status before throwing a `TimeoutError`. Default `"30s"`. */
  timeout?: Duration | undefined;
  /**
   * Fixed delay between status checks. By default the SDK checks after 250ms and backs off to one
   * check every 2s.
   */
  pollInterval?: Duration | undefined;
}

const DEFAULT_RESULT_TIMEOUT_MS = 30_000;
const FIRST_POLL_MS = 250;
const MAX_POLL_MS = 2_000;

/**
 * A request that was handed to Hookyard. Enqueueing returns as soon as the request is stored;
 * delivery happens in the background. Use {@link Job.result} to wait for the outcome.
 */
export class Job {
  /** The request ID, prefixed with `req_`. */
  readonly id: string;
  /**
   * `true` if a request with the same `dedupeKey` already existed, so this call returned it instead
   * of creating a new one.
   */
  readonly deduplicated: boolean;

  #request: HookyardRequest;
  readonly #load: (id: string) => Promise<HookyardRequest>;

  /** @internal Jobs are created by `send()` and friends. */
  constructor(request: HookyardRequest, deduplicated: boolean, load: (id: string) => Promise<HookyardRequest>) {
    this.id = request.id;
    this.deduplicated = deduplicated;
    this.#request = request;
    this.#load = load;
  }

  /** The request as last seen: when it was enqueued, or at the last `refresh()` or `result()`. */
  get request(): HookyardRequest {
    return this.#request;
  }

  /** The status as last seen. Call `refresh()` for the current one. */
  get status(): RequestStatus {
    return this.#request.status;
  }

  /** Fetches the current state of the request, updates `job.request` and returns it. */
  async refresh(): Promise<HookyardRequest> {
    this.#request = await this.#load(this.id);
    return this.#request;
  }

  /**
   * Waits until the request reaches a final status (`succeeded`, `dead`, `canceled` or `unknown`)
   * and returns it. A request that ends up `dead` is returned, not thrown: check `status`.
   *
   * @throws {TimeoutError} if the request is not final within `timeout` (default 30s). The error's
   *   `request` property holds the last known state; the request keeps being delivered.
   */
  async result(options: ResultOptions = {}): Promise<HookyardRequest> {
    const timeoutMs = toMilliseconds(options.timeout ?? DEFAULT_RESULT_TIMEOUT_MS, "timeout");
    const fixedInterval =
      options.pollInterval === undefined ? undefined : toMilliseconds(options.pollInterval, "pollInterval");
    const deadline = Date.now() + timeoutMs;
    let interval = fixedInterval ?? FIRST_POLL_MS;

    while (!isFinalStatus(this.#request.status)) {
      const remaining = deadline - Date.now();
      if (remaining <= 0) {
        throw new TimeoutError(
          `Request ${this.id} did not reach a final status within ${formatMilliseconds(timeoutMs)} ` +
            `(last status: ${this.#request.status}). It is still being delivered: call result() again ` +
            `to keep waiting, or check it later with requests.get("${this.id}").`,
          { request: this.#request },
        );
      }
      await sleep(Math.min(interval, remaining));
      await this.refresh();
      if (fixedInterval === undefined) interval = Math.min(interval * 1.5, MAX_POLL_MS);
    }
    return this.#request;
  }

  toJSON(): { id: string; status: RequestStatus; deduplicated: boolean; request: HookyardRequest } {
    return { id: this.id, status: this.status, deduplicated: this.deduplicated, request: this.#request };
  }
}
