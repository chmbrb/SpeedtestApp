#!/bin/sh
# HTTPS_HOSTS is what people type to reach the app: one or more addresses or names, separated by commas or spaces
# (for example "192.168.0.7, speedtest.lan"). The first one is the default when a browser does not say which it wants.
set -eu
: "${HTTPS_HOSTS:?set HTTPS_HOSTS in .env (the address people use to reach this machine)}"
HTTPS_PRIMARY="$(printf '%s' "$HTTPS_HOSTS" | tr ',' ' ' | awk '{print $1}')"
export HTTPS_PRIMARY
exec "$@"
