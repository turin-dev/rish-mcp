# 테스트 커버리지 분석 노트

> 최종 갱신: 2026-08-16, 이후 `master`에 병합됨 (3차 갱신)
> 측정: `go test -count=1 -coverprofile=/tmp/cover_all.out ./...`

## 전체 커버리지 요약

| 패키지 | 커버리지 | 미커버 stmts | 비고 |
|---|---|---|---|
| `cmd/publicserver` | 80.8% | 4 | `main()` 함수뿐 (표준적으로 테스트 불가) |
| `cmd/relay` | 81.8% | 14 | `main()` + `requireEnv` + `listDevicesHandler` Marshal 오류 |
| `cmd/setup` | **66.5%** | ~110 | **runDeviceSetup 89.7%, runBuildAPKOnly 100% 도달** |
| `internal/mcp` | 100.0% | 0 | |
| `internal/oauth` | 100.0% | 0 | |
| `internal/relay` | 100.0% | 0 | |
| `internal/release` | 99.3% | 2 | 문서화된 unreachable 분기 2개 |
| **total** | **86.6%** | ~132 | |

---

## 1. `cmd/setup/main.go` — 234 stmts, **66.5%** (47.1% → 66.5%)

**`runDeviceSetup`/`runBuildAPKOnly` 흐름 테스트 추가 (2026-08-16, 3차).** fake adb/docker shim + httptest 서버 조합으로 대화형 CLI의 핵심 흐름을 비대화형/대화형 모두에서 커버했다.

### 함수별 커버리지

| 함수 | 커버리지 | 비고 |
|---|---|---|
| `colorsEnabled` | 83.3% | NO_COLOR env 분기 미커버 |
| `style`/`heading`/`dim`/`good`/`bad`/`step` | 100.0% | |
| `prompt` | 70.0% | stdin read 오류 분기만 미커버 (EOF 시) |
| `promptDefault` | 75.0% | |
| `promptYesNo` | 100.0% | 대화형(y/n/기본값) + 비대화형 모두 커버 |
| `main` | 0.0% | 표준 main 함수 |
| `runDeviceSetup` | **89.7%** | 비대화형 해피패스/기기 없음/docker 부재 fail-fast + 대화형 pre-11 |
| `runBuildAPKOnly` | **100.0%** | 서버 다운로드 / 다운로드 실패 / 로컬 빌드 3경로 |
| `runStartRelay` | 0.0% | relay 실행 모드 (남은 대표 미커버 — E2E에 적합) |
| `randomToken` | 75.0% | panic 분기 (crypto/rand 불가) 실질적 unreachable |
| `ensureADB` | 91.7% | |
| `adbBinaryName` | 66.7% | windows 분기 (GOOS 의존) |
| `platformToolsCacheDir` | 80.0% | |
| `platformToolsURL` | 40.0% | linux 분기만 (테스트 환경) |
| `downloadPlatformTools` | **93.8%** | 성공/실패/불량 아카이브/missing adb 커버 |
| `downloadFile` | 91.7% | 200 OK / 404 / conn error 커버 |
| `unzip` | 70.6% | 추출 + 경로 탈출 방지(`../`) 커버 |
| `extractZipFile` | 80.0% | |
| `listDevices` | 100.0% | fake adb 스크립트로 empty/online/offline 커버 |
| `acquireAPK` | **86.7%** | 로컬 빌드 fallback + 다운로드 오류 |
| `buildLocally` | **93.1%** | docker build 실패 / gradle 실패 / 성공 / no build output / empty outDir |
| `ensureGoogleServicesJSON` | **100.0%** | 존재/비존재+비대화형 + 대화형 reject + empty path + copy success + copy fail |
| `findRepoRoot` | 90.0% | |

### 테스트 기법

- **`withStdinPipe()`**: 패키지 레벨 `stdin`을 `os.Pipe()`로 교체해 `prompt()` 계열의 대화형 경로를 테스트. 쓰기는 goroutine으로 (ReadString이 블로킹).
- **`nonInteractive` 모드**: `-yes`/`-y`/`RISH_MCP_YES=1` 환경변수 경로를 노출하는 대화형 함수의 분기 테스트에 사용.
- **`writeZip` 헬퍼**: `archive/zip`으로 메모리에 zip 생성 → 임시 파일로 기록 → `unzip` 검증. 경로 탈출 확인은 `../` 엔트리 포함.
- **fake adb**: `t.TempDir()`에 `#!/bin/sh` 스크립트 작성, `exec.Command(adbPath, ...)`가 이를 실행. 기기 ID 목록(`adb devices`), `install`/`tcpip`/`shell` 서브커맨드 처리.
- **PATH isolation**: `PATH`를 fake adb bin 디렉토리 하나만 남도록 교체 → `exec.LookPath("docker")`가 실패. `TestRunDeviceSetupFailFastNoDocker`가 docker 부재 fail-fast 경로를 검증하는 핵심 기법.
- **`fakeDocker`**: `t.TempDir()`에 docker shim 설치. `docker build` / `docker run` 각각의 exit code를 제어하고, `touchApk` 옵션으로 APK 출력 파일을 생성.
- **`fakeRepoTree`**: `t.TempDir()`에 `app/Dockerfile.build` + `app/app/` 디렉토리 구조 생성. `findRepoRoot()`의 앵커 파일 역할.
- **`httptest.NewServer`**: `downloadFile()` 200/404/연결오류 분기 + `runDeviceSetup` 해피패스의 APK 다운로드/설치 흐름.
- **`shrinkPoll`**: `devicePollInterval`(2s)/`devicePollAttempts`(15) 패키지 레벨 변수를 테스트에서 축소 → 폴링 루프가 실제 대기 없이 단시간에 종료.

### 문서화 판단

- `main`과 `runStartRelay`만 대화형 CLI + 실제 relay 기동(포트 바인딩, `os/exec` 자식 프로세스)이라 단위 테스트에서 제외 — `runDeviceSetup`(89.7%)과 `runBuildAPKOnly`(100%)는 fake adb/docker/http 서버로 커버 완료.
- `downloadPlatformTools`는 downloadFile+unzip이 이미 각각 91.7%/70.6% 커버되어 조합 테스트의 추가 가치가 낮음.
- 남은 미커버는 주로 비대화형 분기, OS 의존 분기(windows/darwin), EOF 오류 분기.

---

## 2. `cmd/publicserver/main.go` — 4 stmts, 80.8%

### 미커버 함수 상세

#### `main()` (29-59) — 0.0%

**표준 main 함수.** 환경변수 읽기, signal.Notify, http.ListenAndServe. 테스트 불가.

### 개선 사항 (완료)

| 테스트 | 상태 | 비고 |
|---|---|---|
| `TestEnvOr` | ✅ 100% | env set / unset 2개 케이스 |
| `TestEnvInt` | ✅ 100% | valid / invalid / unset 3개 케이스 |
| `TestHealthzWithRelease` | ✅ 성공 분기 | `src.Get()` non-nil → 200, `ok: true`, `release: "2.0.0"` |
| `TestVersionHandlerWithRelease` | ✅ 성공 분기 | Cache-Control, Content-Type, JSON 응답 전체 |
| `TestAgentApkHandlerNoRelease503` | ✅ 503 분기 | `rel == nil` → 503 |
| `TestAgentApkHandlerRateLimited429` | ✅ 429 분기 | rate limit → 429 |

---

## 3. `cmd/relay/main.go` — 14 stmts, 81.8%

### 미커버 함수 상세

#### `main()` (28-57) — 0.0%

표준 main 함수. 테스트 불가.

#### `runShellHandler` (166-209) — 100.0% ✅

모든 분기 커버 완료:

- `timeout > maxTimeout` 클램프 — `TestRunShellTimeoutClamp` (timeoutMs: 999999999)
- `res.Truncated` → `[output truncated]` — `TestRunShellTruncatedOutput` (`fakeAgentWithResult` 사용)
- `res.Stderr != ""` → stderr 섹션 — `TestRunShellStderr`
- `res.Code != 0` → `IsError: true` — `TestRunShellNonZeroExit`
- cmd 누락/과대/unmarshal 오류 — `TestRunShellRejectsEmptyCmd` / `TestRunShellRejectsOversizedCmd` / `TestRunShellRejectsInvalidJSON`
- 기기 없음 오류 — `TestRunShellNoDeviceConnected`

#### `listDevicesHandler` (211-220) — 83.3% (1 stmt)

```
line 215: json.MarshalIndent(devices, ...) 오류 분기 (미커버)
```

- `[]DeviceInfo`는 NaN/Inf 같은 `UnsupportedValueError`를 만들 수 없는 자료형이라 실질적으로 unreachable.
- **테스트 불가** (의도적 무시). ~0.4% 상승 가능하나 가치 낮음.

#### `mcpHandler` (240-268) — 100.0% ✅

- method not allowed / parse error / 413 / notification 202 — `TestMCPMethodNotAllowed`, `TestMCPParseError`, `TestMCPRejectsOversizedBody`, `TestMCPNotification`

#### `requireEnv` (314-319) — 0.0%

```go
func requireEnv(key string) string {
    v := os.Getenv(key)
    if v == "" { log.Fatalf("...") }
    return v
}
```

- `log.Fatalf` 호출 분기(os.Getenv가 빈 값 반환) — 테스트 시 프로세스 종료 유발. 호출처 없음(과거 코드에서 잔존).
- **테스트 추가 난이도**: 중. `log.Fatalf`는 실제로 호출되면 테스트 종료.

#### `envDurationMs` (322-329) — 100.0% ✅

- 순수 함수 + os.Getenv + ParseInt. `TestEnvDurationMs`가 valid/invalid/unset 3케이스 커버.

---

## 4. `internal/release/source.go` — 1 stmt, 99.3%

```
line 265.16-267.3: if err != nil { return err }
```

```go
fresh, err := ReadApkInfo(s.apkFile())
if err != nil { // unreachable: Rename on the same fs preserves size+mtime, so the cache always hits
    return err
}
```

- **문서화된 미커버 분기.** 주석에 `unreachable`로 명시 — 동일 파일시스템의 `os.Rename`은 inode를 유지하므로 `ReadApkInfo`가 캐시된 파일을 항상 성공적으로 읽음.
- `os.Rename`이 실패하면(tmp→dst 실패), 이전 줄에서 `os.Remove(tmp)` 후 `return err`로 이미 함수 종료됨.
- **테스트 불가** (의도적 unreachable). 무시해도 안전.

---

## 5. `internal/release/apkinfo.go` — 1 stmt, 99.3%

```
line 102.16-104.3: if err != nil { return ApkInfo{}, err }
```

```go
axml, err := io.ReadAll(f)
_ = f.Close()
if err != nil { // unreachable: zip.Reader from bytes.Reader never returns read errors
    return ApkInfo{}, err
}
```

- **문서화된 미커버 분기.** 주석에 `unreachable`로 명시 — `zip.Reader`는 `bytes.Reader`를 통해 읽으므로, `io.ReadAll`이 실패할 수 없음 (메모리 내 버퍼).
- `_ = f.Close()`도 `io.ReadAll` 전에 파일을 닫지 않음에 유의 (defer 사용이 아니므로).
- **테스트 불가** (의도적 unreachable). 무시해도 안전.

---

## 6. 결론 및 권장사항

### 즉시 개선 가능 (낮은 난이도)

| 파일 | 함수 | 예상 커버리지 증가 | 비고 |
|---|---|---|---|
| `cmd/publicserver/main.go` | `envOr` | ~0.8% | 2개 테스트 케이스 |
| `cmd/publicserver/main.go` | `envInt` | ~0.8% | 3개 테스트 케이스 |
| `cmd/relay/main.go` | `envDurationMs` | ~0.6% | 3개 테스트 케이스 |
| `cmd/publicserver/main.go` | `healthzHandler` 성공 분기 | ~0.8% | 1개 테스트 케이스 추가 |
| `cmd/publicserver/main.go` | `versionHandler` 성공 분기 | ~2.0% | 1개 테스트 케이스 추가 |
| `cmd/publicserver/main.go` | `agentApkHandler` 503/429 | ~1.6% | 2개 테스트 케이스 추가 |

### 중간 난이도

| 파일 | 함수 | 예상 커버리지 증가 | 비고 |
|---|---|---|---|
| `cmd/relay/main.go` | `runShellHandler` 오류 분기 | ~2.4% | registry mock 필요 |
| `cmd/relay/main.go` | `listDevicesHandler` 오류 분기 | ~0.4% | registry mock 필요 |
| `cmd/relay/main.go` | `mcpHandler` 오류 분기 | ~3.2% | registry + MCP mock 필요 |

### 개선 불가 / 불필요

| 경로 | 이유 |
|---|---|
| `main()` 함수 (publicserver, relay, setup) | 표준적으로 테스트 불가 |
| `cmd/setup/main.go` — runStartRelay | relay 기동, 실제 포트 바인딩 + os/exec 자식 프로세스 |
| `cmd/setup/main.go` — main | 표준 main 함수 |
| `release/source.go:265` | 문서화된 unreachable 분기 |
| `release/apkinfo.go:102` | 문서화된 unreachable 분기 |
| `cmd/relay/requireEnv` | log.Fatalf 포함, 테스트 시 프로세스 종료 |

### 우선순위 추천

**완료 (publicserver 66.3% → 80.8%):**
- `envOr`, `envInt` — ✅ 100%
- `healthzHandler`/`versionHandler` 성공 분기 — ✅
- `agentApkHandler` 503/429 분기 — ✅

**완료 (relay 69.6% → 81.8%):**
- `envDurationMs` — ✅ 100%
- `mcpHandler` — ✅ 100% (405/400/413/202)
- `runShellHandler` — ✅ 100% (timeout clamp, truncated, stderr, non-zero exit)
- `listDevicesHandler` — 83.3%, 마지막 Marshal 오류 분기는 unreachable로 판단

**남은 작업:**
- `cmd/setup` — `runStartRelay` 0.0%, relay 기동 + 포트 바인딩 (E2E에 적합)
- `listDevicesHandler` Marshal 오류 — unreachable, 가치 낮음 (0.4%)
- `envDurationMs` edge case — `-1` → 0 클램프 등 (선택적, ~0.2%)
