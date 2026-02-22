# Refactor List

## Critical

### 1. Upstream 실패 시 토큰 failure 미추적

`proxy.go`의 `ErrorHandler`에서 upstream 에러(연결 실패, 타임아웃 등) 발생 시 `token.Manager.RecordFailure()`가 호출되지 않는다. `RecordFailure()`는 구현되어 있지만 프록시에서 사용하지 않아 고장난 real token이 계속 라운드로빈에 포함된다.

- `internal/proxy/proxy.go`: `ErrorHandler` (line 101-103) — realToken 참조가 없어 호출 불가
- `internal/token/manager.go`: `RecordFailure()` (line 78-95) — 사용처 없음

**해결**: request context에 realToken을 저장하고, ErrorHandler + ModifyResponse에서 4xx/5xx 응답 시 `RecordFailure()` 호출.

### 2. Web UI ↔ Admin API 비즈니스 로직 중복

`web/handler.go`와 `internal/admin/handler.go`가 동일한 CRUD를 각각 구현. 사이드이펙트(`onPoolChanged`)를 양쪽에 넣어야 하고, 한쪽 누락 시 버그 발생 (실제로 Web UI에서 풀 갱신 누락 버그 있었음).

**해결**: Web UI form을 HTMX로 Admin API를 직접 호출하도록 변경. Web handler는 페이지 렌더링만 담당.

- `web/handler.go`: CRUD 핸들러 8개 제거, `onPoolChanged` 콜백 제거
- `web/templates/*.html`: form action → Admin API 엔드포인트, HTMX 속성 추가
- `internal/admin/handler.go`: redirect 응답 추가 (Accept 헤더 분기)

### 3. `real_tokens.access_token` 인덱스 누락

`GetRealTokenByAccessToken()`이 인덱스 없이 풀스캔. `gate_tokens.token`에는 인덱스가 있지만 `real_tokens.access_token`에는 없음.

- `internal/store/migrations/001_initial.sql`: `CREATE UNIQUE INDEX idx_real_tokens_access_token ON real_tokens(access_token)` 추가 필요

### 4. 구조화된 로거 도입 + 로그 레벨 지원

현재 모든 로그가 stdlib `log.Printf`로만 출력되며 레벨 구분이 없다. `config.go`에서 `CLAUDE_GATE_LOG_LEVEL`을 읽어 `Config.LogLevel`에 저장하지만 **아무 곳에서도 사용하지 않는다**. 디버깅이 불가능한 수준.

**현재 로그 사각지대**:
- 프록시 요청 플로우: gate-token → real-token 매핑 과정 로그 없음
- 토큰 해석: sticky hit/miss, round-robin 선택 결과 로그 없음
- Pool refresh: 언제 갱신됐는지, 활성 토큰 수 로그 없음
- Failure 기록: RecordFailure 호출 시 어떤 토큰인지 로그 없음
- Usage writer: 배치 크기, flush 타이밍 로그 없음
- Admin API / Web UI: 요청 로깅 없음 (누가 뭘 변경했는지 추적 불가)

**영향 범위**:
- `internal/proxy/proxy.go`: `ServeHTTP` — 요청별 gate→real 매핑
- `internal/token/manager.go`: `ResolveToken`, `RecordFailure`, `RefreshPool`
- `internal/token/pool.go`: `Select`, `Refresh`
- `internal/token/sticky.go`: `Resolve`, `Bind`
- `internal/proxy/usage_writer.go`: `flush`
- `internal/admin/handler.go`: 모든 핸들러
- `internal/config/config.go`: `LogLevel` 필드 미사용
- `cmd/claude-gate/main.go`: 시작/종료 로그

**해결**:
1. `log/slog` (Go 1.21+ stdlib) 기반 로거 도입 — 외부 의존성 없이 레벨별 로깅 지원
2. `CLAUDE_GATE_LOG_LEVEL` 실제 적용 (`debug`, `info`, `warn`, `error`)
3. 핵심 경로에 debug 레벨 로그 추가:
   - `[proxy] gate=gate-xxx → real=sk-ant-xxx (sticky hit)` 또는 `(round-robin)`
   - `[pool] refreshed: 3 active tokens`
   - `[sticky] bind gate=xxx → real=yyy ttl=10m`
   - `[usage] flushed 15 entries`
   - `[admin] POST /admin/api/real-tokens by <ip>`
4. 기본은 `info`, `CLAUDE_GATE_LOG_LEVEL=debug`로 상세 로그 활성화

---

## High

### 5. Usage 채널 오버플로 시 데이터 유실

`proxy.go`의 `sendUsage()`가 non-blocking send로 채널이 가득 차면 usage entry를 드롭한다. 채널 크기가 1024(`setup.go`)로, 버스트 시 사용량 데이터가 유실될 수 있다.

- `internal/proxy/proxy.go`: `sendUsage()` (line 109-121)
- `internal/proxy/setup.go`: `usageChannelSize = 1024` (line 10)

**해결**: 드롭 카운터 메트릭 추가, 채널 크기 설정 가능하게, 또는 backpressure(타임아웃 있는 blocking send) 도입.

### 6. Sticky Session을 `sync.Map` + 수동 TTL로 구현

`internal/token/sticky.go`가 `sync.Map`에 `stickyEntry{realTokenID, expiresAt}`를 저장하고 5분 주기 `Range` 순회로 만료 엔트리를 삭제하는 방식. 문제:

- 만료 처리가 lazy — `Resolve` 시 체크 + 5분 cleanup이라 만료 엔트리가 최대 5분간 메모리 잔존
- `sync.Map.Range`는 O(n) — 엔트리 수 증가 시 cleanup 비용 증가
- TTL 갱신 로직이 수동 — `Bind` 시 직접 `time.Now().UTC().Add(ttl)` 계산

**영향 범위**:
- `internal/token/sticky.go`: `StickyManager` 전체 (cache, Resolve, Bind, cleanupLoop, cleanup)

**해결**: `jellydator/ttlcache/v3` 도입.
- TTL 만료 시 자동 eviction → `cleanupLoop` 제거
- `OnEviction` 콜백으로 DB 정리 연동
- `Get`/`Set`에 TTL 내장 → 수동 만료 체크 불필요
- Web UI 세션(`web/handler.go`의 `sessions map[string]time.Time`)도 동일 라이브러리로 대체 가능 → race condition 해결 + 만료 자동 정리

### 7. Web UI 세션 메모리 누수

`web/handler.go`의 `sessions map[string]time.Time`이 만료 세션을 자동 정리하지 않음. 로그아웃 시에만 삭제되고, 브라우저를 그냥 닫으면 영원히 남음. 동시 접근 시 race condition도 있음.

- `web/handler.go`: sessions 필드 (line 23), requireAuth (line 72-76)

**해결**: `#6`의 `ttlcache` 도입 시 함께 교체. `ttlcache.New[string, bool]()` + TTL 24h로 세션 관리 단순화.

### 8. Store 메서드에 context.Context 미지원

모든 DB 쿼리가 `Query()`, `Exec()` 사용. `QueryContext()`, `ExecContext()`를 사용하지 않아 요청 취소 시에도 쿼리가 계속 실행됨.

- `internal/store/` 전체: 모든 CRUD 메서드

**해결**: 모든 public 메서드에 `ctx context.Context` 파라미터 추가, `*Context()` 변형 사용.

### 9. UsageWriter.flush() 에러 무시

DB 실패 시 `log.Printf`만 하고 재시도/알림 없음. DB가 일시적으로 불가하면 usage 데이터가 영구 유실.

- `internal/proxy/usage_writer.go`: `flush()` (line 78-113)

**해결**: 지수 백오프 재시도, 또는 실패한 entry를 버퍼에 유지하고 다음 flush에서 재시도.

---

## Medium

### 10. SELECT 쿼리 문자열 중복

`real_token.go`에서 같은 SELECT 컬럼 목록이 4곳에서 반복, `gate_token.go`에서 3곳 반복.

- `internal/store/real_token.go`: GetRealToken, ListRealTokens, ListActiveRealTokens, GetRealTokenByAccessToken
- `internal/store/gate_token.go`: GetGateToken, GetGateTokenByToken, ListGateTokens

**해결**: 컬럼 목록을 패키지 상수로 추출.

### 11. JSON usage 파싱 로직 중복

SSE 경로(`tap.go`)와 JSON 경로(`proxy.go`)에서 usage 추출 로직이 별도 구현. API 변경 시 두 곳을 수정해야 함.

- `internal/proxy/tap.go`: `messageStartEnvelope`, `messageDeltaEnvelope`
- `internal/proxy/proxy.go`: `jsonUsageEnvelope`, `extractJSONUsage()`

**해결**: 공통 usage 추출 유틸리티 함수/구조체로 통합.

### 12. Config 검증 부족

`CLAUDE_GATE_UPSTREAM_URL`의 URL 유효성, `CLAUDE_GATE_ADDR`의 형식, `CLAUDE_GATE_DB_PATH` 디렉토리 존재 여부 등을 검증하지 않음. duration/int 파싱 실패 시 경고 없이 기본값으로 대체.

- `internal/config/config.go`: `Load()`, `envDuration()`, `envInt()`

**해결**: URL 파싱 검증, 디렉토리 접근성 체크, 파싱 실패 시 로그 경고 추가.

### 13. Manager.Pool() 캡슐화 위반

`token.Manager`가 `Pool()` 메서드로 내부 `TokenPool`을 직접 노출.

- `internal/token/manager.go`: `Pool()` (line 98-100)

**해결**: `PoolLen()` 같은 구체적 메서드로 대체.

### 14. FK cascade delete 미설정

`real_tokens`/`gate_tokens` 삭제 시 `usage_logs`, `sticky_sessions`에 고아 레코드 발생 가능.

- `internal/store/migrations/001_initial.sql`: FK에 `ON DELETE CASCADE` 없음

**해결**: 마이그레이션에서 `ON DELETE CASCADE` 추가 (또는 애플리케이션 레벨에서 순서대로 삭제).

### 15. sticky_sessions.expires_at 인덱스 누락

`DeleteExpiredStickySessions()`가 `expires_at` 컬럼으로 DELETE하지만 인덱스 없음.

- `internal/store/migrations/001_initial.sql`
- `internal/store/sticky.go`: `DeleteExpiredStickySessions()`

### 16. 보안 헤더 누락

Web UI에 `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options` 등 보안 헤더가 없음. 세션 쿠키에 `Secure` 플래그도 HTTPS 배포 시 필요.

- `web/handler.go`: 전체 응답

### 17. 템플릿 매 요청마다 재파싱

`renderLayout()`이 매 요청마다 `template.ParseFS()`를 호출하여 layout + page 조합을 재파싱.

- `web/handler.go`: `renderLayout()` (line 283-294)

**해결**: 초기화 시 모든 조합을 미리 파싱해서 캐시.

---

## Low

### 18. RowsAffected 에러 무시

`real_token.go`, `gate_token.go`의 Update/Delete/SetActive에서 `res.RowsAffected()` 에러를 `_`로 무시.

### 19. Store.Close()에서 errors.Join 미사용

Go 1.20+의 `errors.Join()` 대신 `fmt.Errorf("close store: %v", errs)` 사용으로 개별 에러 컨텍스트 손실.

### 20. 풀 갱신 동시 호출 시 debounce 없음

여러 핸들러가 동시에 `onPoolChanged`를 호출하면 풀이 중복 갱신됨. 짧은 시간 내 여러 토큰 변경 시 불필요한 DB 조회 발생.

**해결**: debounce 또는 coalesce 패턴 적용.
