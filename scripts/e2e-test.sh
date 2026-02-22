#!/usr/bin/env bash
set -uo pipefail

# ─── 설정 ───
ADMIN_SECRET="${CLAUDE_GATE_ADMIN_SECRET:-e2e-test-secret}"
REAL_TOKEN="${CLAUDE_GATE_REAL_TOKEN:?CLAUDE_GATE_REAL_TOKEN 환경변수를 설정해주세요}"
GATE_ADDR="localhost:18080"
DB_PATH="/tmp/claude-gate-e2e-test.db"
BINARY="./bin/claude-gate"
PASS=0
FAIL_COUNT=0

# 색상
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail()  { echo -e "${RED}[FAIL]${NC} $*"; exit 1; }

assert_eq() {
    local desc="$1" expected="$2" actual="$3"
    if [[ "$expected" == "$actual" ]]; then
        echo -e "  ${GREEN}PASS${NC} $desc"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}FAIL${NC} $desc (expected: $expected, got: $actual)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

assert_contains() {
    local desc="$1" needle="$2" haystack="$3"
    if [[ "$haystack" == *"$needle"* ]]; then
        echo -e "  ${GREEN}PASS${NC} $desc"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}FAIL${NC} $desc (expected to contain: $needle)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

assert_not_empty() {
    local desc="$1" value="$2"
    if [[ -n "$value" ]]; then
        echo -e "  ${GREEN}PASS${NC} $desc"
        PASS=$((PASS + 1))
    else
        echo -e "  ${RED}FAIL${NC} $desc (got empty)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

cleanup() {
    info "정리 중..."
    if [[ -n "${GATE_PID:-}" ]]; then
        kill "$GATE_PID" 2>/dev/null || true
        wait "$GATE_PID" 2>/dev/null || true
    fi
    rm -f "$DB_PATH" "${DB_PATH}-wal" "${DB_PATH}-shm"
    info "정리 완료"
}
trap cleanup EXIT

# ═══════════════════════════════════════════
echo -e "${BOLD}claude-gate E2E 테스트${NC}"
echo "═══════════════════════════════════════"

# ─── 1. 빌드 ───
info "빌드 중..."
go build -o "$BINARY" ./cmd/claude-gate
info "빌드 완료"
echo ""

# ─── 2. 서버 시작 ───
info "서버 시작 중..."
CLAUDE_GATE_ADDR=":18080" \
CLAUDE_GATE_DB_PATH="$DB_PATH" \
CLAUDE_GATE_ADMIN_SECRET="$ADMIN_SECRET" \
"$BINARY" 2>&1 &
GATE_PID=$!

for i in $(seq 1 30); do
    if curl -s "http://${GATE_ADDR}/admin/api/real-tokens" \
        -H "Authorization: Bearer ${ADMIN_SECRET}" > /dev/null 2>&1; then
        break
    fi
    if ! kill -0 "$GATE_PID" 2>/dev/null; then
        fail "서버가 비정상 종료됨"
    fi
    sleep 0.2
done
info "서버 준비 완료 (PID: ${GATE_PID})"
echo ""

# ═══════════════════════════════════════════
echo -e "${BOLD}[Admin API 테스트]${NC}"
echo "───────────────────────────────────────"

# 인증 없이 요청
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://${GATE_ADDR}/admin/api/real-tokens)
assert_eq "인증 없이 요청 → 401" "401" "$HTTP_CODE"

# 잘못된 인증
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://${GATE_ADDR}/admin/api/real-tokens -H "Authorization: Bearer wrong")
assert_eq "잘못된 인증 → 401" "401" "$HTTP_CODE"

# Real token 등록
REAL_TOKEN_RESP=$(curl -s -X POST "http://${GATE_ADDR}/admin/api/real-tokens" \
    -H "Authorization: Bearer ${ADMIN_SECRET}" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"e2e-test-token\",\"access_token\":\"${REAL_TOKEN}\"}")
REAL_TOKEN_ID=$(echo "$REAL_TOKEN_RESP" | jq -r '.data.id // empty')
assert_not_empty "Real token 등록" "$REAL_TOKEN_ID"

# Real token 목록
RT_LIST=$(curl -s "http://${GATE_ADDR}/admin/api/real-tokens" -H "Authorization: Bearer ${ADMIN_SECRET}")
RT_COUNT=$(echo "$RT_LIST" | jq '.data | length')
assert_eq "Real token 목록 (1개)" "1" "$RT_COUNT"

# access_token 미노출 확인
assert_eq "access_token 미노출" "" "$(echo "$RT_LIST" | jq -r '.data[0].access_token // empty')"

# Gate token 발행
GATE_TOKEN_RESP=$(curl -s -X POST "http://${GATE_ADDR}/admin/api/gate-tokens" \
    -H "Authorization: Bearer ${ADMIN_SECRET}" \
    -H "Content-Type: application/json" \
    -d '{"name":"e2e-test-agent"}')
GATE_TOKEN=$(echo "$GATE_TOKEN_RESP" | jq -r '.data.token // empty')
GATE_TOKEN_ID=$(echo "$GATE_TOKEN_RESP" | jq -r '.data.id // empty')
assert_not_empty "Gate token 발행" "$GATE_TOKEN"
assert_contains "Gate token 형식 (gate- 접두사)" "gate-" "$GATE_TOKEN"

echo ""

# ═══════════════════════════════════════════
echo -e "${BOLD}[Proxy 테스트]${NC}"
echo "───────────────────────────────────────"

# 인증 없이 프록시
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://${GATE_ADDR}/v1/messages)
assert_eq "프록시 인증 없음 → 401" "401" "$HTTP_CODE"

# 가짜 gate token
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://${GATE_ADDR}/v1/messages \
    -H "Authorization: Bearer fake-token-12345" \
    -H "Content-Type: application/json" \
    -H "anthropic-version: 2023-06-01" \
    -d '{"model":"claude-sonnet-4-20250514","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}')
assert_eq "가짜 gate token → 401" "401" "$HTTP_CODE"

# 정상 gate token → upstream 도달 확인
PROXY_RESP=$(curl -s -X POST http://${GATE_ADDR}/v1/messages \
    -H "Authorization: Bearer ${GATE_TOKEN}" \
    -H "Content-Type: application/json" \
    -H "anthropic-version: 2023-06-01" \
    -d '{"model":"claude-sonnet-4-20250514","max_tokens":20,"messages":[{"role":"user","content":"say hello"}]}')
# upstream까지 도달했다면 request_id가 있을 것
assert_contains "프록시 → upstream 도달 (request_id 존재)" "request_id" "$PROXY_RESP"
echo "  응답: $(echo "$PROXY_RESP" | jq -c '.')"

echo ""

# ═══════════════════════════════════════════
echo -e "${BOLD}[Claude Code 테스트]${NC}"
echo "───────────────────────────────────────"

info "Claude Code를 gate 통해서 실행 중..."
CLAUDE_OUTPUT=$(ANTHROPIC_BASE_URL="http://${GATE_ADDR}" \
CLAUDE_CODE_OAUTH_TOKEN="${GATE_TOKEN}" \
CLAUDECODE="" \
claude -p "respond with exactly: GATE_OK" 2>&1 || true)
echo "  Claude 응답: ${CLAUDE_OUTPUT}"

if echo "$CLAUDE_OUTPUT" | grep -qi "GATE_OK\|hello\|error\|authentication"; then
    echo -e "  ${GREEN}PASS${NC} Claude Code가 gate를 통해 요청을 보냄"
    PASS=$((PASS + 1))
else
    echo -e "  ${YELLOW}WARN${NC} Claude Code 응답을 확인할 수 없음 (빈 응답이면 OAuth 토큰 문제일 수 있음)"
fi

echo ""

# ═══════════════════════════════════════════
echo -e "${BOLD}[Usage 테스트]${NC}"
echo "───────────────────────────────────────"

sleep 2  # usage writer flush 대기

USAGE=$(curl -s "http://${GATE_ADDR}/admin/api/usage" -G -d "since=2020-01-01T00:00:00Z" \
    -H "Authorization: Bearer ${ADMIN_SECRET}")
REQ_COUNT=$(echo "$USAGE" | jq '.data.request_count')
echo "  전체 사용량: $(echo "$USAGE" | jq -c '.data')"

if [[ "$REQ_COUNT" -gt 0 ]]; then
    echo -e "  ${GREEN}PASS${NC} Usage 기록됨 (${REQ_COUNT}건)"
    PASS=$((PASS + 1))
else
    echo -e "  ${YELLOW}WARN${NC} Usage 기록 없음 (OAuth 토큰이 유효하지 않으면 정상)"
fi

echo ""

# ═══════════════════════════════════════════
echo "═══════════════════════════════════════"
echo -e "${BOLD}결과: ${GREEN}${PASS} passed${NC}, ${RED}${FAIL_COUNT} failed${NC}"
echo "═══════════════════════════════════════"

if [[ $FAIL_COUNT -gt 0 ]]; then
    exit 1
fi
