#!/bin/sh
set -eu
if [ "${RAILWAY_PUBLIC_DOMAIN:-}" = "generous-clarity-production-f87cb8e4.up.railway.app" ]; then
  ./yueying sync-client-origins https://rain-ziyu.github.io
fi
exec "$@"
