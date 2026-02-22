# AGENTS.md

This file provides guidance to AI coding agents when working with code in this repository.

## What is this?

Claude Gate는 Claude API reverse proxy로, 개인 Claude 구독의 OAuth 토큰 풀을 여러 Agent/Claude Code 인스턴스에서 안전하게 공유할 수 있게 해준다. 클라이언트에게는 `gate-` 접두사의 프록시 토큰을 발행하고, 내부적으로 실제 OAuth 토큰으로 교체하여 upstream에 전달한다.

## Commands

```bash
make build          # bin/claude-gate 바이너리 빌드 (CGO_ENABLED=0)
make test           # go test -race ./...
make lint           # golangci-lint run
make all            # fmt + vet + lint + test + build

# 단일 패키지 테스트
go test -race ./internal/store/...
go test -race -run TestPoolRoundRobin ./internal/token/...

# E2E 테스트 (실제 Claude API 토큰 필요)
CLAUDE_GATE_REAL_TOKEN="sk-ant-..." ./scripts/e2e-test.sh

# 로컬 실행
CLAUDE_GATE_ADMIN_SECRET="my-secret" ./bin/claude-gate
```

## Architecture

```
Client (Claude Code) → gate-token으로 요청
    ↓
ProxyHandler (proxy.go)
    ├── gate-token 검증 (store.GetGateTokenByToken)
    ├── Manager.ResolveToken → sticky session 확인 → 없으면 round-robin
    ├── httputil.ReverseProxy Director → Authorization 헤더를 real token으로 교체
    ├── ModifyResponse → SSE면 tappingReader로 wrap, JSON이면 직접 파싱
    └── usageCh → UsageWriter (비동기 배치) → store에 기록
    ↓
Claude API (api.anthropic.com)
```

### 핵심 흐름

1. **토큰 라우팅**: `token.Manager`가 sticky session(10분 TTL) 우선 확인 → miss시 `TokenPool.Select()`로 round-robin → 새 sticky session bind
2. **SSE tapping**: `tappingReader`가 response body를 stream-through하면서 `message_start`(input_tokens), `message_delta`(output_tokens) SSE 이벤트에서 usage 추출. bytes를 그대로 전달하며 substring 매치 시에만 JSON 파싱 (성능 최적화)
3. **비동기 usage 기록**: `UsageWriter`가 channel에서 entry를 읽어 배치(100개 또는 1초)로 flush. usage_logs INSERT + gate/real token 누적 카운터 UPDATE

### Store 구조

SQLite WAL 모드, read/write 커넥션 분리. `writeDB`는 `MaxOpenConns=1`, `readDB`는 `MaxOpenConns=4`. 마이그레이션은 `internal/store/migrations/` 에 `{번호}_{이름}.sql` 형식으로 추가하면 자동 실행.

### Admin API 인증

`Authorization: Bearer <CLAUDE_GATE_ADMIN_SECRET>`. 각 route에 개별적으로 auth middleware 적용 (Go 1.22+ method-based mux 패턴 사용으로 sub-mux wrapping 불가).

## 코드 검증 (반드시 커밋 전 실행)

코드를 수정한 후 커밋하기 전에 **반드시** 아래를 로컬에서 실행하여 통과를 확인해야 한다:

```bash
go fix ./...        # Go 1.26+ 코드 현대화 (range-over-int, strings.Builder 등)
make lint           # golangci-lint (errcheck 등 포함)
make test           # go test -race ./...
```

`go fix`는 구식 패턴을 현대 Go 관용구로 자동 변환한다. 코드 수정 후 lint/test 전에 먼저 실행할 것.

lint에서 자주 걸리는 항목:
- **errcheck**: 에러 반환값을 무시하면 안 됨. 의도적으로 무시할 경우 `_ =` 사용
- **type assertion**: `v.(*Type)` 대신 `v, _ := value.(*Type)` comma-ok 패턴 사용

## 개발 시 주의사항

### SQLite 관련
- `:memory:` DB는 read/write 분리 아키텍처에서 동작하지 않음 (각 커넥션이 별도 DB를 갖게 됨). 테스트에서는 반드시 temp file 사용 → `testutil.NewTestStore()` 사용
- sticky session의 expires_at은 반드시 **UTC** 사용. SQLite `datetime('now')`가 UTC 기준이므로 Go에서 `time.Now().UTC()` 필수

### Mux 라우팅
- Go 1.22+ `http.ServeMux`는 method-based 패턴(`GET /path`)과 method-less 패턴(`/path/`)이 충돌할 수 있음. admin API는 method-based 패턴으로 개별 등록
- proxy catch-all `mux.Handle("/", ...)` 은 반드시 마지막에 등록

### 토큰 풀 갱신
- admin API에서 real token CRUD 후 반드시 `onPoolChanged` 콜백 호출하여 `token.Manager`의 풀을 갱신해야 함. 안 하면 새로 추가한 토큰이 프록시에 반영되지 않음

### SSE 프록시
- `WriteTimeout: 0` 필수 (SSE 스트리밍이 timeout에 걸리지 않도록)
- `FlushInterval: -1` 설정으로 즉시 flush

### 테스트 작성
- FK constraint 있음: sticky_sessions 테스트 시 실제 gate_tokens, real_tokens 레코드를 먼저 생성해야 함
- `testutil.NewMockUpstream()`: SSE mock, `testutil.NewMockUpstreamJSON()`: JSON mock

## 환경변수

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `CLAUDE_GATE_ADMIN_SECRET` | (필수) | Admin API/Web UI 인증 시크릿 |
| `CLAUDE_GATE_ADDR` | `:8080` | Listen 주소 |
| `CLAUDE_GATE_DB_PATH` | `./claude-gate.db` | SQLite 파일 경로 |
| `CLAUDE_GATE_UPSTREAM_URL` | `https://api.anthropic.com` | Upstream Claude API |
| `CLAUDE_GATE_STICKY_TTL` | `10m` | Sticky session TTL |
| `CLAUDE_GATE_MAX_FAILURES` | `5` | Real token 자동 비활성화 threshold |

## 의존성 정책

외부 의존성은 `modernc.org/sqlite` 하나만 사용. HTTP, JSON, template 등 모두 stdlib. 새 의존성 추가 전 stdlib로 해결 가능한지 먼저 확인.
