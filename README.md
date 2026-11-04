# pacchain

A PAC-aware chain proxy for Burp Suite.

```
Browser ──► Burp (127.0.0.1:8080) ──► pacchain (127.0.0.1:8888) ──► PAC-selected proxy / DIRECT ──► target
```

## Description

When a test environment is reached through a PAC file, a browser PAC
extension (e.g. SwitchyOmega) sends traffic straight to the PAC's proxies
and **Burp never sees it**. pacchain sits behind Burp instead: Burp sends
everything to pacchain, and pacchain runs your PAC for each request and
forwards it to the proxy the PAC chooses.

Based on [williambailey/pacproxy](https://github.com/williambailey/pacproxy).

## Features

- Runs the real PAC JavaScript, with the same `url` / `host` a browser passes
- Reloads the PAC automatically when the file (or a symlink to it) changes
- Fails over to the next proxy in `PROXY a; PROXY b`
- Blocks falling back to `DIRECT` after a proxy failed (staging hostnames
  reached directly are usually production), unless `--allow-direct-fallback`
- One log line per request, with no headers, cookies or query strings
- Single binary, listens on localhost only

## Installation

Download a binary from [Releases](https://github.com/0xnashi/pacchain/releases)
(tested on macOS; Linux and Windows builds are untested), or build from
source with Go 1.25+:

```bash
go install github.com/0xnashi/pacchain@latest
```

On macOS, extract it and clear the quarantine flag:

```bash
tar -xzf pacchain_1.0.0_darwin_arm64.tar.gz
xattr -d com.apple.quarantine ./pacchain
```

## Burp Suite setup

Two different settings, two different ports:

**1. Proxy listener** (where the browser connects to Burp)
Proxy → Proxy settings → Proxy listeners: `127.0.0.1:8080`

Do not use 8888 here: that port is pacchain's.

**2. Upstream proxy** (where Burp sends traffic next)
Settings → Network → Connections → Upstream proxy servers → Add:

| Destination host | Proxy host | Proxy port |
|---|---|---|
| `*` | `127.0.0.1` | `8888` |

Use `127.0.0.1`, not `localhost`. Make sure the rule is enabled and no other
rule above it matches first.

**3. Browser**
Use Burp's built-in browser (Proxy → Open browser), or point your browser
at `127.0.0.1:8080`. Turn off any PAC in the browser: pacchain applies it now.

**Check:** open a page that the PAC routes to a proxy. It should appear in
Burp's HTTP history *and* as a `[+]` line in pacchain's output. If pacchain
prints nothing, Burp is not using the upstream rule (if your Burp project
overrides settings, try a Temporary project with Burp defaults).

## Usage

```bash
pacchain -c env-a.pac
```

```
10:02:11 [*] pacchain 1.0.0 listening on 127.0.0.1:8888 (PAC: env-a.pac)
10:02:11 [*] watching env-a.pac for changes: edit it or switch its symlink, no restart needed
10:02:15 [+] CONNECT app.stg.test:443 → PROXY proxy-b.example.test:8080 (env-a.pac, 0.2ms)
10:02:16 [+] GET http://web.stg.test/page → DIRECT (env-a.pac, 0.1ms) 200
```

`[+]` routed · `[*]` info · `[!]` warning · `[-]` failed

## Examples

Check where a URL will go:

```bash
pacchain -c env-a.pac -r https://app.stg.test/
```

Switch PAC files without restarting pacchain or Burp:

```bash
ln -s env-a.pac current.pac
pacchain -c current.pac
ln -sfn env-b.pac current.pac   # takes effect within a second
```

## Options

| Option | Default | Description |
|---|---|---|
| `-c <pac>` | (required) | PAC file, `http(s)://` URL, inline script, or inline result like `PROXY host:port; DIRECT` |
| `-l <addr>` | `127.0.0.1:8888` | Listen address |
| `-r <url>` | | Print the PAC result for `<url>` and exit |
| `-v` | off | Verbose log |
| `--allow-direct-fallback` | off | Allow `DIRECT` after a proxy option failed |
| `--version` | | Print the version |

## Limitations

- HTTP (not HTTPS) requests are re-sent by Go's HTTP client: header names
  are canonicalized (`x-foo` → `X-Foo`), their order may change, and a
  request without `User-Agent` gets `Go-http-client/1.1`. HTTPS is relayed
  byte for byte.
- No credentials for upstream proxies. An upstream's own error (e.g. 407)
  is passed to Burp, marked with an `X-Pacchain-Upstream` header.
- `kill -HUP` reload is not available on Windows; edit the file instead.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
Based on pacproxy, Copyright William Bailey.
