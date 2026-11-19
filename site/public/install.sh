#!/bin/sh
# Installs closedvps from its GitHub release:  curl -fsSL https://closedvps.com/install.sh | sh
# CLOSEDVPS_VERSION=v1.2.3 picks a release (default: latest); CLOSEDVPS_INSTALL_DIR picks the folder
# (default: ~/.local/bin). The download is checked against the release's checksums.txt.
set -eu

main() {
  repo="tulayha/closedvps"
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "this script is for macOS and Linux; on Windows run: irm https://closedvps.com/install.ps1 | iex" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "no build for $(uname -m)" ;;
  esac

  if [ -n "${CLOSEDVPS_DOWNLOAD_BASE:-}" ]; then # CI tests the script against a local build
    base="$CLOSEDVPS_DOWNLOAD_BASE"
  elif [ -n "${CLOSEDVPS_VERSION:-}" ]; then
    base="https://github.com/$repo/releases/download/$CLOSEDVPS_VERSION"
  else
    base="https://github.com/$repo/releases/latest/download"
  fi
  dir="${CLOSEDVPS_INSTALL_DIR:-$HOME/.local/bin}"
  file="closedvps_${os}_${arch}.tar.gz"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  echo "Downloading $file"
  curl -fsSL -o "$tmp/$file" "$base/$file" || fail "download failed: $base/$file"
  curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "download failed: $base/checksums.txt"

  want="$(grep " $file\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
  [ -n "$want" ] || fail "$file is not listed in checksums.txt"
  if command -v sha256sum >/dev/null 2>&1; then
    got="$(sha256sum "$tmp/$file" | cut -d' ' -f1)"
  else
    got="$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)"
  fi
  [ "$got" = "$want" ] || fail "checksum mismatch for $file; not installing"

  tar -xzf "$tmp/$file" -C "$tmp" closedvps
  mkdir -p "$dir"
  mv "$tmp/closedvps" "$dir/closedvps"
  chmod 755 "$dir/closedvps"
  echo "Installed $("$dir/closedvps" version) to $dir/closedvps"

  case ":$PATH:" in
    *":$dir:"*) echo "Next: closedvps create" ;;
    *)
      echo
      echo "$dir is not on your PATH yet. Add it in your shell's startup file, or run it directly:"
      echo "  $dir/closedvps create"
      ;;
  esac
}

fail() {
  echo "closedvps install: $1" >&2
  exit 1
}

main "$@"
