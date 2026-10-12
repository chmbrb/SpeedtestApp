#!/bin/sh
# Starts the backend.
#
# Ookla's command-line tool is NOT part of this image. Its terms of use have to be accepted by the person running the
# server, so nothing here accepts them for anyone: the tool is downloaded only when OOKLA_ACCEPT_LICENSE=yes has been set,
# straight from Ookla, and checked against the checksum pinned below before it is used. Without that setting the backend
# starts normally and simply offers the browser (NDT7) test only.
set -u

OOKLA_DIR="${OOKLA_DIR:-/var/lib/ookla}"
OOKLA_VERSION="1.2.0"
# OOKLA_DOWNLOAD_BASE exists so the download and checksum logic can be tested without contacting Ookla.
BASE="${OOKLA_DOWNLOAD_BASE:-https://install.speedtest.net/app/cli}"

install_ookla() {
  case "$(uname -m)" in
    x86_64)         arch=x86_64;  sha=5690596c54ff9bed63fa3732f818a05dbc2db19ad36ed68f21ca5f64d5cfeeb7 ;;
    aarch64|arm64)  arch=aarch64; sha=3953d231da3783e2bf8904b6dd72767c5c6e533e163d3742fd0437affa431bd3 ;;
    armv7l|armv6l)  arch=armhf;   sha=e45fcdebbd8a185553535533dd032d6b10bc8c64eee4139b1147b9c09835d08d ;;
    *) echo "ookla: no download is pinned for this processor ($(uname -m))."; return 1 ;;
  esac
  tmp="$(mktemp -d)"
  if ! wget -q -O "$tmp/ookla.tgz" "$BASE/ookla-speedtest-$OOKLA_VERSION-linux-$arch.tgz"; then
    echo "ookla: the download failed (is this container online?)."; rm -rf "$tmp"; return 1
  fi
  if ! echo "$sha  $tmp/ookla.tgz" | sha256sum -c - >/dev/null 2>&1; then
    echo "ookla: the downloaded file does not match the pinned checksum, so it was NOT installed."; rm -rf "$tmp"; return 1
  fi
  if ! tar -xzf "$tmp/ookla.tgz" -C "$OOKLA_DIR" speedtest || ! chmod 755 "$OOKLA_DIR/speedtest"; then
    echo "ookla: could not unpack the tool."; rm -rf "$tmp"; return 1
  fi
  rm -rf "$tmp"
  echo "ookla: installed version $OOKLA_VERSION ($arch)."
}

if [ "${OOKLA_ACCEPT_LICENSE:-}" = "yes" ] && [ "${OOKLA_PATH:-}" = "$OOKLA_DIR/speedtest" ] && [ ! -x "$OOKLA_PATH" ]; then
  echo "ookla: OOKLA_ACCEPT_LICENSE=yes is set, so the tool will be downloaded from Ookla (terms: https://www.speedtest.net/about/eula)."
  install_ookla || echo "ookla: Ookla tests will stay off until this is fixed."
fi

exec /app/server "$@"
