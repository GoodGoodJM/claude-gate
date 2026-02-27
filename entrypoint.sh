#!/bin/sh
set -e

if [ -n "$LITESTREAM_S3_BUCKET" ]; then
  CLAUDE_GATE_DB_PATH="${CLAUDE_GATE_DB_PATH:-/data/claude-gate.db}"
  export CLAUDE_GATE_DB_PATH
  litestream restore -if-db-not-exists -if-replica-exists "$CLAUDE_GATE_DB_PATH"
  # 첫 실행 시 S3에 백업이 없으면 빈 DB 파일을 생성하여 Litestream이 시작할 수 있도록 함
  if [ ! -f "$CLAUDE_GATE_DB_PATH" ]; then
    touch "$CLAUDE_GATE_DB_PATH"
  fi
  exec litestream replicate -exec "claude-gate" -config /etc/litestream.yml
else
  exec claude-gate
fi
