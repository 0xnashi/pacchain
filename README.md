# pacchain

A PAC-aware chain proxy for Burp Suite.

pacchain runs behind Burp and sends each request to the upstream proxy that
your PAC file chooses, so traffic that must go through environment-specific
proxies (staging, QA, internal) still passes through Burp.

```
Browser ──► Burp (127.0.0.1:8080) ──► pacchain (127.0.0.1:8888) ──► PAC-selected proxy / DIRECT ──► target
```

## Description

Testing a staging environment often means using a PAC file: some hosts must
go through one proxy, some through another, everything else directly. A
browser PAC extension (e.g. SwitchyOmega) applies the PAC inside the
browser, so the browser connects straight to those proxies and **Burp never
sees the traffic**. Recreating the PAC as Burp upstream rules by hand is
tedious and breaks every time the PAC changes.

pacchain moves the PAC decision behind Burp. Burp gets a single upstream
rule (`*` → pacchain); pacchain runs your PAC's `FindProxyForURL` for every
connection, exactly like a browser, and forwards to the proxy it returns.

pacchain is a fork of [williambailey/pacproxy](https://github.com/williambailey/pacproxy).

## Features

- **Runs the real PAC JavaScript**: `dnsDomainIs`, `shExpMatch`, `isInNet` and the
  other standard PAC functions behave as in a browser. No hand-maintained rules.
- **Browser-identical PAC input**: `FindProxyForURL` gets the same `url` and `host`
  a browser would pass.
- **Live PAC reload**: edit the PAC file or switch a symlink (`env-a.pac` →
  `env-b.pac`) and it takes effect within a second. Open tunnels are closed so
  nothing keeps using the old route. A broken PAC is rejected and the previous
  one stays in use.
- **Safe failover**: `PROXY a; PROXY b` tries `b` only if `a` cannot be used.
  Falling back to `DIRECT` after a proxy failed is **blocked by default**,
  because for staging hostnames DIRECT usually means production.
- **Clear failures**: an upstream's own error (e.g. 407) is relayed to Burp,
  marked with `X-Pacchain-Upstream`; an unreachable proxy gets a "is the VPN
  connected?" hint.
- **Faithful relaying**: HTTPS is relayed as an untouched encrypted tunnel.
  HTTP responses reach Burp exactly as the server sent them (no
  decompression), and request encodings are not altered. See
  [Relaying](#relaying) for the few HTTP details that do change.
- **Readable, secret-free log**: one line per connection. Headers, cookies,
  bodies, query strings and the PAC source are never logged.
- **Defensive**: listens on localhost by default, stops a PAC that runs longer
  than 3 seconds, and warns when Burp is not sending it any traffic.
- **Single binary**: no runtime dependencies.

## Installation

### Pre-built binary

Download the archive for your platform from the
[Releases](https://github.com/0xnashi/pacchain/releases) page.

| Platform | Status |
|---|---|
| macOS (Apple Silicon, Intel) | tested |
| Linux (amd64, arm64) | built, not tested |
| Windows (amd64) | built, not tested |

On macOS, a downloaded binary is quarantined by Gatekeeper. Clear it with:

```bash
xattr -d com.apple.quarantine ./pacchain
```

### From source

Requires Go 1.25 or newer.

```bash
go install github.com/0xnashi/pacchain@latest
```

or

```bash
git clone https://github.com/0xnashi/pacchain.git
cd pacchain
make build        # → bin/pacchain
```

## Usage

```
pacchain -c <pac> [options]
```

### 1. Start pacchain

```bash
pacchain -c env-a.pac
```

```
21:25:59 [*] pacchain 0.1.0 listening on 127.0.0.1:8888 (PAC: env-a.pac)
21:25:59 [*] watching env-a.pac for changes: edit it or switch its symlink, no restart needed
```

Connect your VPN first if your proxies are behind one.

### 2. Point Burp at pacchain

**Burp → Settings → Network → Connections → Upstream proxy servers → Add**

| Field | Value |
|---|---|
| Destination host | `*` |
| Proxy host | `127.0.0.1` |
| Proxy port | `8888` |

Burp's own **Proxy listener stays on `127.0.0.1:8080`**. These are two
different settings: the listener is where the browser connects to Burp, the
upstream is where Burp sends traffic next.

### 3. Point the browser at Burp

Use Burp's built-in browser, or set your browser's proxy to `127.0.0.1:8080`.
Do **not** apply the PAC in the browser any more; pacchain applies it.

Each connection now shows up in Burp and gets one line in pacchain's log:

```
[+] CONNECT app.stg.test:443 → PROXY proxy-b.example.test:8080 (env-a.pac, 0.2ms)
[+] GET http://web.stg.test/page → DIRECT (env-a.pac, 0.1ms) 200
```

### Reading the log

| Prefix | Meaning |
|---|---|
| `[+]` | connection routed |
| `[*]` | information (startup, PAC reloaded) |
| `[!]` | warning: something failed but pacchain carried on (e.g. tried the next proxy) |
| `[-]` | the request failed |

A line reads: `METHOD target → route (PAC file, PAC evaluation time) [HTTP status]`.
HTTPS targets show only `host:port`: the rest is inside the encrypted tunnel.
When the PAC returns several options, the full PAC answer is shown so you
can see what was skipped.

## Examples

**Check where a URL will go before testing**

```bash
pacchain -c env-a.pac -r https://app.stg.test/login
```
```
PROXY proxy-b.example.test:8080
```

For HTTPS URLs the path is ignored, as in real traffic: pacchain only ever
sees `host:port` of an HTTPS connection.

**Switch between PAC files without restarting**

```bash
ln -s env-a.pac current.pac
pacchain -c current.pac

# later, in another terminal
ln -sfn env-b.pac current.pac     # reloads within a second; Burp needs no change
```

Editing the PAC file in place works too. `kill -HUP <pid>` forces a reload
(also for a PAC loaded from a URL, which is not watched).

**Use another port**

```bash
pacchain -c env-a.pac -l 127.0.0.1:8899
```

**More detail while debugging**

```bash
pacchain -c env-a.pac -v
```

Adds the raw PAC answer to every line, the remote address to HTTPS lines,
and a line when each HTTPS tunnel closes (duration, bytes sent and received).

**Other ways to give the PAC**

```bash
pacchain -c https://pac.example.test/proxy.pac                        # URL (not watched; reload with kill -HUP)
pacchain -c 'function FindProxyForURL(url, host) { return "DIRECT"; }' # inline script
pacchain -c 'PROXY proxy.example.test:8080; DIRECT'                    # inline result
```

## Options

| Option | Default | Description |
|---|---|---|
| `-c <pac>` | (required) | PAC file path, `http(s)://` URL, inline JavaScript, or an inline result such as `PROXY host:port; DIRECT` |
| `-l <addr>` | `127.0.0.1:8888` | Address to listen on. A non-localhost address prints a warning |
| `-r <url>` | | Print the proxies the PAC returns for `<url>`, one per line, and exit |
| `-v` | off | Verbose log |
| `--allow-direct-fallback` | off | Allow `DIRECT` after a proxy option failed. See [Failover](#failover) |
| `--version` | | Print the version and exit |
| `-h` | | Help |

Exit codes: `0` success, `1` runtime error, `2` invalid usage.

## Troubleshooting

**Nothing appears in pacchain's log**

After 30 seconds without any request pacchain says so:

```
[!] no requests received in 30s. If you are browsing through Burp, its upstream proxy is probably not in effect ...
```

- Check the upstream rule in Burp: `*` → `127.0.0.1:8888`, enabled, and at
  the top of the list. Burp uses the first matching rule.
- Upstream rules exist at both user and project level, and a project-level
  override hides the user-level rules.
- Still nothing: start Burp with a **Temporary project** and **Burp defaults**,
  add only the one upstream rule, and test again. If that works, a setting
  in your saved Burp configuration is overriding it.

**`port already in use`**

Another program (Burp, Charles, Fiddler, or a pacchain you forgot to stop)
holds the port. pacchain never moves to another port silently, because Burp
would then send traffic to whatever owns the old one.

```bash
lsof -nP -iTCP:8888 -sTCP:LISTEN     # who is it?
pacchain -c env-a.pac -l 127.0.0.1:8899  # or use another port (and update Burp)
```

**`proxy unreachable: is the VPN connected?`**

pacchain could not connect to (or resolve) the proxy the PAC chose. With
proxies behind a VPN this almost always means the VPN is down. Reconnect;
no restart is needed.

**Burp shows a 407, 403 or 502 with an `X-Pacchain-Upstream` header**

That response comes from the upstream proxy named in the header, not from
pacchain: the proxy refused the request (407: it wants authentication; 502:
it cannot reach the target). pacchain relays it unchanged and does not try
another proxy, because a different proxy might lead to a different
environment. pacchain has no setting for upstream proxy credentials.

**`DIRECT fallback blocked`**

The PAC said `PROXY ...; DIRECT`, the proxy failed, and pacchain refused to
connect directly. Fix the proxy (usually: the VPN). Only if a direct
connection is really what you want, start pacchain with
`--allow-direct-fallback`.

**`PAC evaluation timed out after 3s`**

The PAC did not return within 3 seconds for that host, usually because of a
loop in the PAC or a slow `dnsResolve`. Only that request fails.

**`PAC reload ... failed, still using the previous PAC`**

The changed PAC file has a syntax error, does not define `FindProxyForURL`,
or timed out while loading. Fix and save it again; pacchain keeps routing
with the previous version meanwhile.

**Windows**

`kill -HUP` does not exist on Windows; reload by editing or replacing the PAC
file instead.

## Technical Details

### How a request is routed

1. Burp sends `CONNECT host:443` (HTTPS) or `GET http://host/path` (HTTP)
   to pacchain.
2. pacchain calls the PAC's `FindProxyForURL(url, host)` with the values a
   browser (Chrome) would use: `https://host/` for HTTPS, with the port if it
   is not 443; the full URL for HTTP, without `#fragment` or credentials.
3. It tries the returned options in order (see below).
4. **HTTPS**: pacchain sends the `CONNECT` to the chosen proxy, waits for its
   `200`, only then answers Burp `200` and relays the encrypted bytes in both
   directions. It never decrypts anything.
   **HTTP**: pacchain forwards the request through the chosen proxy and
   relays the response.

The PAC runs in [otto](https://github.com/robertkrimen/otto), an ES5
JavaScript interpreter written in Go, with the standard PAC helper
functions. `dnsResolve`, `isResolvable` and `isInNet` resolve names on the
machine running pacchain.

### Failover

| What happened with an option | What pacchain does |
|---|---|
| proxy unreachable, unresolvable, hung up, or no answer to `CONNECT` within 10s | try the next option |
| proxy answered with an error (407, 403, 502, …) | stop; relay that response with `X-Pacchain-Upstream` (HTTP: see below) |
| `DIRECT` comes after a proxy option that failed | blocked, unless `--allow-direct-fallback` (then logged as `[!]`) |
| `DIRECT` is the first option | normal direct connection |
| every option failed without an answer | pacchain's own `502`, listing each attempt |

A browser would silently fall back to `DIRECT`. pacchain does not, because
with staging proxies the same hostname reached directly is usually the
production system, and the page would look perfectly normal.

For HTTP (not HTTPS) requests, a proxy's error response cannot be told
apart from the target's own response, except for `407`, which only a proxy
sends. So over HTTP only a `407` is treated as the proxy's answer and marked
with `X-Pacchain-Upstream`; any other status is relayed as the response.

HTTP request bodies up to 1 MB are buffered when the PAC offers more than
one option, so a retried request is sent intact.

### Relaying

**HTTPS** is an opaque tunnel: the bytes are relayed unchanged.

**HTTP** requests are re-sent through Go's HTTP client:

- responses are never decompressed, and the client's `Accept-Encoding` is
  forwarded as is, so Burp sees what the server sent
- hop-by-hop headers (`Connection`, `Proxy-Connection`) are removed, as a
  proxy must
- header names are sent in canonical form (`x-foo` → `X-Foo`) and their
  order may change
- a request **without** `User-Agent` gets `User-Agent: Go-http-client/1.1`

If an exact HTTP request matters (header case, order, or a missing
`User-Agent`), send it over HTTPS or check it at the target.

### PAC reload

The PAC file is checked once a second by content hash, which also catches
editors that save by renaming and symlinks switched to another file. On a
change, or on `SIGHUP`, a new JavaScript engine is built and checked first
(it must load within 3s and define `FindProxyForURL`); only then does it
replace the running one. After a successful reload every open tunnel and
idle upstream connection is closed, so Burp reconnects and the new PAC
decides the route.

### Security notes

- Anyone who can reach pacchain's port can use your VPN and upstream
  proxies through it. It listens on `127.0.0.1` by default and warns when
  told to listen elsewhere.
- A PAC file is code from someone else. Each evaluation is limited to 3
  seconds, and a PAC that fails to load never replaces a working one.
- The log never contains headers, cookies, bodies, query strings, URL
  credentials or the PAC source, so it can be shared or pasted into a
  report. (A `console.log` you add to a PAC yourself is printed as is.)

## Development

```bash
make hooks     # once per clone: git hooks that block PAC files and internal names
make test      # go test -race ./...
make devenv    # local fake upstream proxies, for use with examples/dev.pac
make release   # cross-compile into build/
```

`make devenv` starts fake proxies that behave like staging proxies (each
answers from its own backend and names itself in `X-Fake-Upstream`) plus
ones that reply 407, 502 or hang up. With `pacchain -c examples/dev.pac`
every behaviour above can be tried without a VPN; `examples/dev.pac` lists
which hostname triggers which case.

`make hooks` creates a local, gitignored `.denylist`: put your internal
domains and hostnames in it, and the hooks refuse any commit containing them.
Never commit real PAC files; only `examples/` may hold PAC files, using
reserved domains such as `*.test`.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
pacchain is based on [pacproxy](https://github.com/williambailey/pacproxy),
Copyright William Bailey.
