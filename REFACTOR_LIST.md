# Refactor List

## High — 코드 중복

### 1. Admin Handler: activate/deactivate 4개 함수 거의 동일

`activateRealToken`, `deactivateRealToken`, `activateGateToken`, `deactivateGateToken`이 토큰 타입과 active 값만 다르고 구조가 완전히 동일하다.

- `internal/admin/handler.go:215-245` — activateRealToken / deactivateRealToken
- `internal/admin/handler.go:326-354` — activateGateToken / deactivateGateToken

```go
// 4개 함수의 공통 패턴:
id := r.PathValue("id")
err := h.store.SetXxxTokenActive(r.Context(), id, true/false)
if err == sql.ErrNoRows { respondError(...) }
if err != nil { respondError(...) }
h.logger.Info("xxx token activated/deactivated", "id", id)
h.onPoolChanged()  // real token만
respondSuccess(...)
```

**해결**: 단일 헬퍼 함수로 통합.

```go
func (h *AdminHandler) setTokenActive(w http.ResponseWriter, r *http.Request, tokenType string, active bool) {
    id := r.PathValue("id")
    var err error
    if tokenType == "real" {
        err = h.store.SetRealTokenActive(r.Context(), id, active)
    } else {
        err = h.store.SetGateTokenActive(r.Context(), id, active)
    }
    // ... 공통 에러 처리 + respondSuccess
}
```

### 2. Admin Handler: delete 2개 함수 거의 동일

`deleteRealToken`과 `deleteGateToken`이 store 메서드와 redirect URL만 다르다.

- `internal/admin/handler.go:199-213` — deleteRealToken
- `internal/admin/handler.go:311-324` — deleteGateToken

**해결**: `#1`과 유사하게 `tokenType` 파라미터로 분기하는 공통 함수.

### 3. Admin Handler: usage 핸들러 3개 거의 동일

`getUsage`, `getUsageByRealToken`, `getUsageByGateToken`이 `since` 파싱 + store 호출 + writeJSON의 동일 구조.

- `internal/admin/handler.go:370-400` — 3개 함수

### 4. Store: GetUsageStats 쿼리 3개 거의 동일

SQL의 SELECT/SUM 부분이 완전히 동일하고 WHERE 조건만 다르다. Scan 코드도 동일.

- `internal/store/usage.go:78-95` — GetUsageStats (WHERE 없음)
- `internal/store/usage.go:97-114` — GetUsageStatsByRealToken (WHERE real_token_id = ?)
- `internal/store/usage.go:116-133` — GetUsageStatsByGateToken (WHERE gate_token_id = ?)

**해결**: 내부 헬퍼로 추출.

```go
func (s *Store) queryUsageStats(ctx context.Context, where string, args ...any) (*UsageStats, error) {
    query := `SELECT COALESCE(SUM(input_tokens), 0), ... FROM usage_logs`
    if where != "" { query += " WHERE " + where }
    // ...
}
```

### 5. Store: DeleteRealToken / DeleteGateToken 트랜잭션 구조 동일

두 함수 모두 `BeginTx → DELETE sticky_sessions → DELETE usage_logs → DELETE 본 테이블 → RowsAffected 체크 → Commit` 동일 패턴. Rollback 호출이 각각 5군데씩 반복.

- `internal/store/real_token.go:150-183` — DeleteRealToken
- `internal/store/gate_token.go:134-167` — DeleteGateToken

**해결**: 트랜잭션 헬퍼 도입.

```go
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
    tx, err := s.writeDB.BeginTx(ctx, nil)
    if err != nil { return err }
    if err := fn(tx); err != nil {
        _ = tx.Rollback()
        return err
    }
    return tx.Commit()
}
```

그리고 cascade delete 로직을 `deleteTokenCascade(tx, table, idColumn, id)` 같은 공통 함수로 추출.

### 6. Store: SetRealTokenActive / SetGateTokenActive bool→int 변환 중복

두 함수 모두 `val := 0; if active { val = 1 }` 변환 + `ExecContext` + `RowsAffected` 체크 동일 패턴.

- `internal/store/real_token.go:128-148`
- `internal/store/gate_token.go:112-132`

**해결**: `boolToInt()` 헬퍼 추출. 또는 SQL 레벨에서 `CAST(? AS INTEGER)`.

### 7. Store: UpdateRealToken / UpdateGateToken 구조 동일

`ExecContext → RowsAffected → ErrNoRows 체크` 패턴이 동일.

- `internal/store/real_token.go:110-126`
- `internal/store/gate_token.go:94-110`

### 8. writeError / writeJSONError 구현 중복

두 패키지에서 완전히 동일한 JSON 에러 응답 함수를 각각 정의.

- `internal/admin/middleware.go:36-40` — `writeError()`
- `internal/proxy/proxy.go:164-168` — `writeJSONError()`

```go
// admin/middleware.go
func writeError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// proxy/proxy.go — 위와 100% 동일한 코드
func writeJSONError(w http.ResponseWriter, code int, msg string) { ... }
```

**해결**: `internal/httputil` 같은 공유 패키지로 추출하거나, 한쪽에서 다른 쪽을 import.

### 9. SSE vs JSON usage envelope 필드 중복

SSE 경로와 JSON 경로에서 usage 필드를 각각 별도 struct로 정의.

- `internal/proxy/tap.go:102-129` — `messageStartEnvelope`, `messageDeltaEnvelope`
- `internal/proxy/proxy.go:140-148` — `jsonUsageEnvelope`

동일 필드: `InputTokens`, `OutputTokens`, `CacheCreationInputTokens`, `CacheReadInputTokens`, `Model`. API 스키마 변경 시 두 곳 수정 필요.

**해결**: 공통 `usageFields` struct 추출 후 SSE/JSON envelope에서 embed.

---

## Medium — 구조 개선

### 10. `scanRealTokenRows` 래퍼 함수 불필요

`real_token.go:228-230`의 `scanRealTokenRows`는 `scanRealToken(rows)`를 호출할 뿐. `scanRealToken`이 이미 `rowScanner` 인터페이스를 받으므로 `*sql.Rows`를 직접 전달 가능.

- `internal/store/real_token.go:228-230`

```go
func scanRealTokenRows(rows *sql.Rows) (*RealToken, error) {
    return scanRealToken(rows) // 그냥 scanRealToken 직접 호출하면 됨
}
```

**해결**: `scanRealTokenRows` 제거, 호출부에서 `scanRealToken(rows)` 직접 사용.

### 11. `droppedCount` 필드 증가만 하고 읽지 않음

`ProxyHandler.droppedCount`가 `sendUsage()`에서 증가되지만 외부로 노출하거나 읽는 코드가 없다.

- `internal/proxy/proxy.go:35` — 필드 정의
- `internal/proxy/proxy.go:134` — `h.droppedCount.Add(1)` (유일한 사용)

**해결**: 모니터링 엔드포인트에 노출하거나, 주기적으로 로그로 출력. 아니면 제거.

### 12. 매직 넘버 상수화

| 값 | 위치 | 설명 |
|---|---|---|
| `100*time.Millisecond` | `token/manager.go:63` | pool refresh debounce |
| `5 * time.Minute` | `token/sticky.go:90` | DB cleanup interval |
| `24 * time.Hour` | `web/handler.go:36` | 세션 TTL |
| `86400` | `web/handler.go:136` | 쿠키 MaxAge (위의 24h와 동일 의미인데 별도 하드코딩) |
| `3` | `proxy/usage_writer.go:112` | max retry count |
| `30 * time.Second` | `cmd/claude-gate/main.go:75` | server read timeout |
| `120 * time.Second` | `cmd/claude-gate/main.go:77` | server idle timeout |
| `10 * time.Second` | `cmd/claude-gate/main.go:90` | shutdown timeout |

**해결**: 각 파일 또는 패키지의 `const` 블록으로 추출.

### 13. `newTestStore` 헬퍼 2곳에서 중복 정의

- `internal/store/store_test.go:13-22`
- `internal/admin/handler_test.go:18-27`

동일한 `t.TempDir()` + `store.New()` + `t.Cleanup()` 패턴.

**해결**: `internal/testutil` 패키지에 `NewTestStore(t)` 추출. 또는 `store` 패키지에 `NewTestStore` export.

### 14. Admin Handler `respondError`의 URL 하드코딩

flash 메시지가 URL-encoded string으로 하드코딩되어 관리가 번거로움.

- `internal/admin/handler.go` — `respondError` / `respondSuccess` 호출 시 `"/admin/real-tokens?flash=Token+not+found"` 같은 문자열 약 15곳

**해결**: `net/url.URL` 사용하여 안전하게 빌드.

```go
func adminRedirect(base, flash string) string {
    u, _ := url.Parse(base)
    q := u.Query()
    q.Set("flash", flash)
    u.RawQuery = q.Encode()
    return u.String()
}
```

### 15. `StickyManager.done` 채널 불필요한 복잡도

`done` 채널은 `dbCleanupLoop`의 종료를 기다리기 위해 존재하지만, context 취소만으로 충분히 처리 가능. `Stop()`에서 `<-sm.done` 대기하는 패턴은 `sync.WaitGroup`이 더 명확.

- `internal/token/sticky.go:20` — `done chan struct{}`
- `internal/token/sticky.go:44-49` — `Stop()`에서 `<-sm.done`

**해결**: `sync.WaitGroup`으로 교체하거나, context 취소 후 `cache.Stop()`만으로 정리.

---

## Low — 일관성/스타일

### 16. SSE 변수명 네이밍 불일치

`dataPrefix` vs `messageStartKey` — 접미사가 `Prefix`와 `Key`로 혼용.

- `internal/proxy/tap.go:73-77`

```go
var (
    dataPrefix        = []byte("data: ")     // Prefix
    messageStartKey   = []byte("message_start") // Key
    messageDeltaKey   = []byte("message_delta") // Key
)
```

**해결**: `sseDataPrefix` / `sseEventMessageStart` / `sseEventMessageDelta` 등으로 통일.

### 17. 쿠키 MaxAge와 ttlcache TTL 불일치 가능성

쿠키 `MaxAge: 86400`(초)와 `ttlcache TTL: 24 * time.Hour`는 같은 값이지만 별도 리터럴로 관리. 한쪽만 변경하면 세션 불일치 발생.

- `web/handler.go:36` — `ttlcache.WithTTL[string, bool](24 * time.Hour)`
- `web/handler.go:136` — `MaxAge: 86400`

**해결**: `const sessionTTL = 24 * time.Hour` 정의 후 `MaxAge: int(sessionTTL.Seconds())` 사용.

### 18. `InsertUsageLogs` nil/empty 슬라이스 시 불필요한 트랜잭션

빈 슬라이스가 들어와도 `BeginTx` → `Prepare` → `Commit` 실행.

- `internal/store/usage.go:46-76`

**해결**: 함수 시작에 early return 추가.

```go
func (s *Store) InsertUsageLogs(ctx context.Context, logs []UsageLog) error {
    if len(logs) == 0 { return nil }
    // ...
}
```

### 19. UsageWriter에서 토큰별 사용량 업데이트가 개별 쿼리

flush 시 buf의 각 entry마다 `UpdateGateTokenUsage` + `UpdateRealTokenUsage`를 개별 호출. entry가 100개면 쿼리 200개.

- `internal/proxy/usage_writer.go:125-132`

**해결**: 토큰 ID별로 aggregate 후 한 번씩만 호출.

```go
gateUsage := map[string][2]int64{} // id → {input, output}
for _, e := range uw.buf {
    v := gateUsage[e.GateTokenID]
    v[0] += e.Usage.InputTokens
    v[1] += e.Usage.OutputTokens
    gateUsage[e.GateTokenID] = v
}
for id, v := range gateUsage {
    uw.store.UpdateGateTokenUsage(ctx, id, v[0], v[1])
}
```
