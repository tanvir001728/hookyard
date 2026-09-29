# flakyvendor

A deliberately unreliable HTTP API for testing and demonstrating Hookyard. Real partner APIs time
out, rate limit, return `503` during deploys and sometimes report errors inside a `200 OK`.
`flakyvendor` does all of that on request.

```sh
make flakyvendor               # listens on :9090
./bin/flakyvendor -addr :9999
```

## Controlling behavior

Behavior is chosen with **query parameters on any path**. Because Hookyard forwards the request path
unchanged, an application picks the behavior through the `path` of the Hookyard request:

```json
{ "upstream": "flaky", "method": "POST", "path": "/orders?fail_first=2" }
```

| Parameter | Effect |
| --- | --- |
| `status=N` | Always respond with status `N` |
| `fail_rate=F` | Fail a fraction `F` (0 to 1) of requests at random |
| `fail_first=N` | Fail the first `N` requests for the same key, then succeed |
| `key=K` | Counter key for `fail_first` (default: method and path) |
| `fail_status=N` | Status used for failures (default `503`) |
| `retry_after=S` | Add `Retry-After: S` to failures (seconds or an HTTP date) |
| `fake_error=1` | Respond `200 OK` with `"status": "FAILED"` in the body |
| `latency=D` | Wait `D` (for example `200ms`) before responding |
| `hang=1` | Never respond, so the client times out |

Every response echoes the received request as JSON (method, path, headers, body and the attempt
number for its key).

## Inspecting traffic

| Endpoint | Description |
| --- | --- |
| `GET /_requests` | Requests received so far (the last 1000), oldest first |
| `DELETE /_requests` | Clear recorded requests and `fail_first` counters |
| `GET /_health` | Liveness check |

## Use in Go tests

```go
vendor := flakyvendor.New()
srv := httptest.NewServer(vendor)
defer srv.Close()
// ... point an upstream at srv.URL, deliver requests ...
got := vendor.Requests()
```
