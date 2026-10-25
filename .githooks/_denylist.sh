# Shared helper for pacchain git hooks (sourced, not executed).
#
# Loads non-comment, non-blank lines from .denylist into a temp file and
# sets $DENYLIST_PATTERNS to its path. Sets it to "" when there is nothing
# to check. The temp file is removed on exit.

DENYLIST_FILE="$(git rev-parse --show-toplevel)/.denylist"
DENYLIST_PATTERNS=""

if [ -f "$DENYLIST_FILE" ]; then
	DENYLIST_PATTERNS=$(mktemp "${TMPDIR:-/tmp}/pacchain-denylist.XXXXXX")
	trap 'rm -f "$DENYLIST_PATTERNS"' EXIT
	grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$DENYLIST_FILE" >"$DENYLIST_PATTERNS" || true
	if [ ! -s "$DENYLIST_PATTERNS" ]; then
		DENYLIST_PATTERNS=""
	fi
fi

if [ -z "$DENYLIST_PATTERNS" ]; then
	echo "[!] $(basename "$0"): .denylist missing or empty — internal keyword check SKIPPED (run: make hooks)" >&2
fi
