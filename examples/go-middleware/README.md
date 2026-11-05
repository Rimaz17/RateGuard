# RateGuard Go middleware

A reference integration for [RateGuard](../../README.md): a `net/http` middleware that
asks RateGuard about every request and refuses the ones it refuses. It is a module of
its own and uses only the standard library, so importing it adds no dependencies.

```go
rg, err := rateguard.New(os.Getenv("RATEGUARD_URL")) // http://rateguard:8080, or unix:///run/rateguard.sock
if err != nil {
	log.Fatal(err)
}
handler := rg.Middleware(mux)
```

That is the whole integration; [sampleapp](sampleapp/main.go) is a runnable service built
around it.

## What it does

| RateGuard answers | The middleware |
|---|---|
| 200, allowed | Calls your handler, with `X-RateLimit-Limit`, `-Remaining` and `-Reset` set |
| 429, refused | Answers 429 with the same headers and `Retry-After` |
| 401 (bad key) or 403 (no policy) | Answers with that status and RateGuard's message |
| Unreachable, too slow, or any other error | Answers 503, or calls your handler with `WithFailOpen()` |

- The caller's `Authorization` header is forwarded, so RateGuard identifies it by API
  key.
- The caller's address is appended to `X-Forwarded-For`. RateGuard only believes it if
  your service's network is in `RG_TRUSTED_PROXIES`, or if it talks to a sidecar over
  the Unix socket.
- The path sent is the decoded path your router routes on, so `/api/%6Cogin` cannot slip
  past a rule on `/api/login`. The query string is never sent.
- Connections to RateGuard are kept alive, up to 256 idle ones, because opening a
  connection is most of the cost of the extra hop.
- Each decision has a 250ms timeout by default (`WithTimeout`).
- `WithErrorHandler` is told about every failure, for your logs or metrics.

## Options

| Option | Default | Meaning |
|---|---|---|
| `WithTimeout(d)` | 250ms | Longest a decision may take |
| `WithFailOpen()` | off (fail closed with 503) | Serve requests when RateGuard cannot answer |
| `WithErrorHandler(f)` | none | Called with every failed decision |
| `WithHTTPClient(c)` | pooled client | Replace the HTTP client, for tests or a custom transport |

## Tests

```bash
go test -race ./...
```

`TestE2E` runs against a real RateGuard when `RATEGUARD_URL`, `RATEGUARD_API_KEY` and
`RATEGUARD_LIMIT` (that key's capacity) are set, as CI does against the compose stack.
