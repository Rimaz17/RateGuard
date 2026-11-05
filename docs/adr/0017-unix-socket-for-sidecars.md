# ADR 0017: A Unix socket for sidecar deployment

- **Status:** Accepted
- **Date:** 2026-11-05

## Context

ADR 0004 asks for a Unix-socket listener so that RateGuard can run as a sidecar next to
a latency-sensitive application, with no TCP hop. Three questions follow:

- what the socket serves;
- how its file is created and cleaned up;
- how a caller without an API key is identified, since a socket peer has no IP address.
  Today such a request is refused with 401, "an API key is required on this
  connection".

## Options

For keyless callers over the socket:

1. **Keep refusing them:** a sidecar then cannot limit anonymous traffic by IP at all.
2. **Trust `X-Forwarded-For` from socket peers.** Only a process that the socket file's
   permissions let in can connect, which in a sidecar means the application in the same
   pod or host, the same party that would be listed in `RG_TRUSTED_PROXIES` over TCP.

## Decision

Option 2, with the rest as follows.

- **`RG_UNIX_SOCKET`** names an absolute path, at most 104 bytes long (the shortest
  limit among the platforms that matter). Empty, the default, means no socket. The TCP
  listener on `RG_HTTP_ADDR` keeps running either way.
- **The socket serves the same router** with the same timeouts as TCP. The admin API
  still requires its token.
- **Permissions.** The file is set to `RG_UNIX_SOCKET_MODE`, an octal mode (default
  `0660`), right after it is created. The default lets the owner and group connect, and
  nobody else.
- **A leftover file** is handled at startup:
  - A socket nobody is listening on, left behind by a crash, is removed.
  - A socket that answers belongs to a live process, and startup fails rather than
    stealing it.
  - Anything that is not a socket is never deleted, and startup fails.

  The file is removed again on shutdown.
- **Identity over the socket.** A request with an API key is identified by it, as over
  TCP. Without one, `X-Forwarded-For` is read from the right. Its last entry, added by
  the application, is the caller, unless that address is itself in
  `RG_TRUSTED_PROXIES`; then the walk continues left exactly as over TCP. A request
  with neither a key nor a usable `X-Forwarded-For` is still refused with 401.

## Consequences

- A sidecar is configured with one path and a mode. The application connects to the
  file, and the reference middleware accepts a `unix://` address.
- Anyone who can write to the socket can choose the IP identity a keyless request
  counts against, which is why the mode must stay tight. That is the same trust a
  proxy in `RG_TRUSTED_PROXIES` already has over TCP.
- Phase 7 measures p50 and p99 latency over the socket and over TCP, as ADR 0004
  requires.
