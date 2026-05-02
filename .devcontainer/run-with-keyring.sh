#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -eq 0 ]; then
  set -- mise run test:ci
fi

if [ -z "${TRACE_DEVCONTAINER_KEYRING_PASSWORD:-}" ]; then
  TRACE_DEVCONTAINER_KEYRING_PASSWORD="$(openssl rand -hex 16)"
fi
export TRACE_DEVCONTAINER_KEYRING_PASSWORD

exec dbus-run-session -- bash -lc '
  set -euo pipefail
  printf "%s" "$TRACE_DEVCONTAINER_KEYRING_PASSWORD" | gnome-keyring-daemon --unlock >/dev/null
  exec "$@"
' bash "$@"
