#!/usr/bin/env bash
# dyna installer: curl -fsSL https://raw.githubusercontent.com/arismoko/dyna-agent/main/install.sh | bash
#
# Env overrides:
#   DYNA_INSTALL_DIR   install location            (default: ~/.local/bin)
#   DYNA_REPO          github owner/repo           (default: arismoko/dyna-agent)
#   DYNA_VERSION       release tag, e.g. v0.2.0    (default: latest)
#   DYNA_NO_SKILLS=1   skip `dyna skill install`
set -euo pipefail

REPO="${DYNA_REPO:-arismoko/dyna-agent}"
VERSION="${DYNA_VERSION:-latest}"
INSTALL_DIR="${DYNA_INSTALL_DIR:-$HOME/.local/bin}"
BIN="$INSTALL_DIR/dyna"
TMP_BIN=""
TMP_SUM=""
TMP_SRC=""

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
info()  { printf '  \033[36m•\033[0m %s\n' "$*"; }
ok()    { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail()  { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

cleanup() {
  [ -z "$TMP_BIN" ] || rm -f "$TMP_BIN"
  [ -z "$TMP_SUM" ] || rm -f "$TMP_SUM"
  [ -z "$TMP_SRC" ] || rm -rf "$TMP_SRC"
}
trap cleanup EXIT

new_tmp_binary() {
  TMP_BIN="$(mktemp "$INSTALL_DIR/.dyna.XXXXXX")" || fail "could not create a staged binary in $INSTALL_DIR"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "sha256sum or shasum is required to verify release downloads"
  fi
}

verify_checksum() {
  local file="$1"
  local sums="$2"
  local asset="$3"
  local expected actual
  expected="$(awk -v asset="$asset" '{ name=$2; sub(/^\*/, "", name); if (name == asset) { print $1; exit } }' "$sums")"
  [ -n "$expected" ] || fail "checksums.txt has no entry for $asset"
  actual="$(sha256_file "$file")"
  [ "$actual" = "$expected" ] || fail "checksum mismatch for $asset"
  ok "verified sha256 for $asset"
}

activate_binary() {
  chmod 0755 "$TMP_BIN"
  "$TMP_BIN" --help >/dev/null 2>&1 || fail "downloaded binary failed to run"
  mv -f "$TMP_BIN" "$BIN"
  TMP_BIN=""
}

bold "dyna installer"
mkdir -p "$INSTALL_DIR"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch" ;;
esac

# 1) Local checkout (script run from inside the repo): build from source.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-.}")" 2>/dev/null && pwd)" || script_dir="$(pwd)"
if [ -f "$script_dir/go.mod" ] && grep -q '^module dyna-agent$' "$script_dir/go.mod" 2>/dev/null; then
  command -v go >/dev/null 2>&1 || fail "building from source requires Go (https://go.dev/dl)"
  info "building from local checkout ($script_dir)"
  new_tmp_binary
  (cd "$script_dir" && go build -o "$TMP_BIN" .)
  activate_binary
  ok "built $BIN"
else
  # 2) Try a prebuilt release binary.
  asset="dyna_${os}_${arch}"
  if [ "$VERSION" = "latest" ]; then
    release_base="https://github.com/$REPO/releases/latest/download"
  else
    release_base="https://github.com/$REPO/releases/download/$VERSION"
  fi
  url="$release_base/$asset"
  info "downloading $url"
  new_tmp_binary
  if curl -fsSL "$url" -o "$TMP_BIN" 2>/dev/null; then
    TMP_SUM="$(mktemp "$INSTALL_DIR/.dyna-checksums.XXXXXX")" || fail "could not stage checksums.txt"
    curl -fsSL "$release_base/checksums.txt" -o "$TMP_SUM" 2>/dev/null \
      || fail "release is missing checksums.txt"
    verify_checksum "$TMP_BIN" "$TMP_SUM" "$asset"
    activate_binary
    ok "installed prebuilt binary at $BIN"
  else
    rm -f "$TMP_BIN"
    TMP_BIN=""
    # 3) Fall back to `go install` style source build from the repo.
    command -v go >/dev/null 2>&1 || fail "no prebuilt binary for ${os}/${arch} and Go is not installed"
    info "no prebuilt binary; building from source"
    TMP_SRC="$(mktemp -d)"
    if [ "$VERSION" = "latest" ]; then
      git clone --depth 1 "https://github.com/$REPO" "$TMP_SRC/src" >/dev/null 2>&1 \
        || fail "could not clone https://github.com/$REPO"
    else
      git clone --depth 1 --branch "$VERSION" "https://github.com/$REPO" "$TMP_SRC/src" >/dev/null 2>&1 \
        || fail "could not clone $VERSION from https://github.com/$REPO"
    fi
    new_tmp_binary
    (cd "$TMP_SRC/src" && go build -o "$TMP_BIN" .)
    activate_binary
    ok "built from source at $BIN"
  fi
fi

"$BIN" --help >/dev/null 2>&1 || fail "installed binary failed to run"
ok "$("$BIN" --help 2>/dev/null | head -1)"

# PATH check
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ok "$INSTALL_DIR is on your PATH" ;;
  *)
    info "$INSTALL_DIR is not on your PATH; add this to your shell rc:"
    # The printed $PATH is intentionally literal for the user's shell.
    # shellcheck disable=SC2016
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
