# pacchain

> **Work in progress.** pacchain is a fork of
> [williambailey/pacproxy](https://github.com/williambailey/pacproxy) being
> adapted for pentest workflows. The full README arrives with v0.1.0.

pacchain is a local chain proxy that sits behind Burp Suite and routes each
connection to the upstream proxy chosen by a PAC file:

```
Browser → Burp (127.0.0.1:8080) → pacchain → PAC-selected proxy / DIRECT → target
```

Browser PAC extensions decide routing inside the browser, so traffic skips
Burp entirely. pacchain moves the PAC evaluation behind Burp instead.

## Development setup

```bash
git clone https://github.com/0xnashi/pacchain.git
cd pacchain
make hooks   # enable git hooks — run once per clone
make test
make build   # → bin/pacchain
```

`make hooks` creates a local `.denylist` (gitignored). Add your internal
domains and hostnames to it; the pre-commit and commit-msg hooks refuse any
commit containing them. Real PAC files are gitignored — only `examples/`
may hold PAC files, and those must use reserved domains (`*.test`,
`example.com`).

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
Original work Copyright William Bailey.
