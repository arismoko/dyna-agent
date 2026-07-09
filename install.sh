#!/usr/bin/env bash
# dyna installer — curl -fsSL https://raw.githubusercontent.com/Aria-Figueredo/dyna-agent/main/install.sh | bash
#
# Env overrides:
#   DYNA_INSTALL_DIR   install location            (default: ~/.local/bin)
#   DYNA_REPO          github owner/repo           (default: Aria-Figueredo/dyna-agent)
#   DYNA_VERSION       release tag, e.g. v0.2.0    (default: latest)
#   DYNA_NO_SKILLS=1   skip `dyna skill install`
set -euo pipefail

REPO="${DYNA_REPO:-Aria-Figueredo/dyna-agent}"
VERSION="${DYNA_VERSION:-latest}"
INSTALL_DIR="${DYNA_INSTALL_DIR:-$HOME/.local/bin}"
BIN="$INSTALL_DIR/dyna"

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
info()  { printf '  \033[36m•\033[0m %s\n' "$*"; }
ok()    { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail()  { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

bold "⬡ dyna installer"
mkdir -p "$INSTALL_DIR"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch" ;;
esac

# 1) Local checkout? (script run from inside the repo) — build from source.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-.}")" 2>/dev/null && pwd || pwd)"
if [ -f "$script_dir/go.mod" ] && grep -q '^module dyna-agent$' "$script_dir/go.mod" 2>/dev/null; then
  command -v go >/dev/null 2>&1 || fail "building from source requires Go (https://go.dev/dl)"
  info "building from local checkout ($script_dir)"
  (cd "$script_dir" && go build -o "$BIN" .)
  ok "built $BIN"
else
  # 2) Try a prebuilt release binary.
  if [ "$VERSION" = "latest" ]; then
    url="https://github.com/$REPO/releases/latest/download/dyna_${os}_${arch}"
  else
    url="https://github.com/$REPO/releases/download/$VERSION/dyna_${os}_${arch}"
  fi
  info "downloading $url"
  if curl -fsSL "$url" -o "$BIN.tmp" 2>/dev/null; then
    mv "$BIN.tmp" "$BIN"
    chmod +x "$BIN"
    ok "installed prebuilt binary → $BIN"
  else
    rm -f "$BIN.tmp"
    # 3) Fall back to `go install` style source build from the repo.
    command -v go >/dev/null 2>&1 || fail "no prebuilt binary for ${os}/${arch} and Go is not installed"
    info "no prebuilt binary — building from source"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    git clone --depth 1 "https://github.com/$REPO" "$tmp/src" >/dev/null 2>&1 \
      || fail "could not clone https://github.com/$REPO"
    (cd "$tmp/src" && go build -o "$BIN" .)
    ok "built from source → $BIN"
  fi
fi

"$BIN" --help >/dev/null 2>&1 || fail "installed binary failed to run"
ok "$("$BIN" --help 2>/dev/null | head -1)"

# PATH check
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ok "$INSTALL_DIR is on your PATH" ;;
  *)
    info "$INSTALL_DIR is NOT on your PATH — add this to your shell rc:"
    printf '      export PATH="%s:$PATH"\n' "$INSTALL_DIR"
    ;;
esac

# Teach the installed agent harnesses about dyna.
if [ "${DYNA_NO_SKILLS:-0}" != "1" ]; then
  bold "installing agent skills (detected harnesses)"
  "$BIN" skill install || true
fi

bold "next steps"
info "dyna profiles init   # register the curated default fleet (fable/sol/terra/luna)"
info "dyna demo            # mock workers + sample workflow"
info "dyna tui             # the dashboard"
info "dyna guide           # scripting guide for agents"
