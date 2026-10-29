NAME      := pacchain
VERSION   := $(shell awk -F\" '/^const Version/ { print $$2 }' ./pacproxy.go)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: all build test devenv hooks release clean favicon

all: test build

# Local build for this machine.
build:
	go build -trimpath -o bin/$(NAME) .

test:
	go test -race ./...

# Start local fake upstream proxies matching examples/dev.pac (no VPN needed).
devenv:
	go run ./cmd/fakeupstream

# Enable the repo's git hooks. Run once per clone (git never installs hooks
# automatically). Creates .denylist from the example if it does not exist.
hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/pre-commit .githooks/commit-msg
	@if [ ! -f .denylist ]; then \
		cp .denylist.example .denylist; \
		echo "[!] Created .denylist from example — edit it and add your internal keywords"; \
	fi
	@echo "[+] git hooks enabled (.githooks)"

# Cross-compile release binaries into build/.
release: clean
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		out="build/$(NAME)_$(VERSION)_$${os}_$${arch}/$(NAME)$${ext}"; \
		echo "[*] $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "-s -w" -o "$$out" . || exit 1; \
	done

clean:
	rm -rf bin/ build/

favicon:
	echo 'package main' > favicon.go
	xxd -p favicon.ico | tr -d '\n' | sed 's/.\{2\}/\\x&/g' | sed 's/.*/var faviconIco = []byte("&")/g' >> favicon.go
