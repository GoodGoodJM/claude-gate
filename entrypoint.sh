#!/bin/sh
set -e

if [ -n "$LITESTREAM_S3_BUCKET" ]; then
  CLAUDE_GATE_DB_PATH="${CLAUDE_GATE_DB_PATH:-/data/claude-gate.db}"
  litestream restore -if-db-not-exists -if-replica-exists "$CLAUDE_GATE_DB_PATH"
  exec litestream replicate -exec "claude-gate" -config /etc/litestream.yml
else
  exec claude-gate
fi
