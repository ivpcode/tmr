#!/bin/sh
# build-deb.sh — costruisce il pacchetto Debian di ivt.
#
# Uso: scripts/build-deb.sh <versione> [arch]
#   versione  es. 0.1.0 (senza prefisso v)
#   arch      amd64 (default) | arm64
#
# Produce dist/ivt_<versione>_<arch>.deb. Richiede Go e dpkg-deb.
set -eu

VERSION="${1:?versione mancante (es. 0.1.0)}"
ARCH="${2:-amd64}"

case "$ARCH" in
	amd64) GOARCH=amd64 ;;
	arm64) GOARCH=arm64 ;;
	*) echo "arch non supportata: $ARCH (amd64|arm64)" >&2; exit 1 ;;
esac

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
STAGE="$(mktemp -d)"
chmod 755 "$STAGE" # mktemp crea 0700, ma la radice del pacchetto deve essere 0755
trap 'rm -rf "$STAGE"' EXIT

# Binario statico: niente cgo, niente dipendenze runtime.
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
	go build -C "$ROOT" -trimpath \
	-ldflags "-s -w -X main.version=$VERSION" \
	-o "$STAGE/usr/bin/ivt" ./cmd/ivt

install -d -m 755 "$STAGE/DEBIAN" "$STAGE/usr/share/doc/ivt"
install -m 644 "$ROOT/README.md" "$STAGE/usr/share/doc/ivt/README.md"

# Il file copyright deve citare anche xterm.js, incorporato nel binario.
{
	echo "ivt - session-only terminal multiplexer for long-running agents"
	echo "Copyright (C) 2026 Brainyware"
	echo ""
	echo "Incorpora xterm.js e @xterm/addon-fit (licenza MIT):"
	echo ""
	cat "$ROOT/internal/web/assets/LICENSE.xterm"
} > "$STAGE/usr/share/doc/ivt/copyright"

SIZE_KB=$(du -sk "$STAGE/usr" | cut -f1)
cat > "$STAGE/DEBIAN/control" <<EOF
Package: ivt
Version: $VERSION
Section: utils
Priority: optional
Architecture: $ARCH
Installed-Size: $SIZE_KB
Maintainer: Brainyware <prez@brainyware.ai>
Homepage: https://github.com/ivpcode/tmr
Description: session-only terminal multiplexer for long-running agents
 ivt runs commands (AI agents, builds, shells) in named sessions that
 survive detaching from the terminal, tmux-style but reduced to session
 management only. Sessions can be resumed from the CLI or from a built-in
 HTTPS web UI with a full xterm.js terminal in the browser.
 .
 Single static binary with no runtime dependencies.
EOF

mkdir -p "$ROOT/dist"
OUT="$ROOT/dist/ivt_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "$STAGE" "$OUT" >/dev/null
echo "$OUT"
