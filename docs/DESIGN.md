# rish-mcp 설계 문서

이 문서는 `plan.md`(의사결정 로그)에서 확정된 목표·비목표·아키텍처 결정을 실제 구현
가능한 수준으로 구체화한다. "왜 이렇게 정했는지"는 `plan.md`를, "무엇을 어떻게
만드는지"는 이 문서를 보면 된다.

---
## 1. 개요

AI(Claude 등)가 가상머신이 아니라 사용자의 실제 Android 기기를 조작할 수 있게
해주는 MCP 서버 + 에이전트다. 사용자가 소유한 기기 하나(또는 소수)를 대상으로 하는
개인용 도구이며, 관리자를 두는 멀티테넌트 서비스가 아니다.

```
┌─────────┐   MCP   ┌──────────────────┐ ◀── outbound WS ── ┌──────────────────┐
│   AI    │──HTTPS─▶│  Go relay + MCP  │                    │   Android 앱     │
│(Claude) │ ◀───────│     서버         │── exec / result ──▶│ Shizuku → ADB fb │
└─────────┘         └──────────────────┘                    └──────────────────┘
                             │
                             │ 버전/체크섬 조회, APK 배포
                             ▼
                   ┌──────────────────┐
                   │  공식 버전 서버   │  (별도 바이너리/컨테이너, 무토큰)
                   └──────────────────┘
```

- 기기는 항상 **아웃바운드**로만 연결한다 (CGNAT 뒤에서도 동작, 인바운드 노출 없음).
- 현재 모든 폼팩터가 상시 WebSocket을 사용한다. 구현되지 않은 FCM 수신 스텁과 SDK는
  APK에서 제거했으며, 실제 relay 발신 경로와 Firebase 프로젝트가 준비되기 전에는
  push-wake 기능이 있다고 표시하지 않는다.
- relay는 개인이 셀프호스팅한다. 공식 서버는 버전 정보와 APK 배포만 담당하고
  relay 기능은 포함하지 않는다 (`plan.md` 비목표: multi-tenant 아님).

---
## 2. 컴포넌트

### 2.1 Android 앱

| 모듈 | 상태 | 역할 |
|---|---|---|
| `ShizukuShellClient.kt` / `ShellUserService.kt` | **1.0** | 명시적 권한 승인 후 Shizuku UserService를 uid 2000으로 바인딩하는 우선 백엔드 |
| `AdbShellClient.kt` | **1.0 폴백** | 온디바이스 ADB 프로토콜 클라이언트. Android 11+ 무선 페어링, 11 미만 USB-tcpip 브리지 |
| `ShellBackendManager.kt` | **1.0** | Shizuku 우선/ADB 폴백 선택, 중복 ADB 연결 방지, 실행 중인 명령의 백엔드 재시도 금지 |
| `ConnectionManager.kt` | **신규** | 상시 WS, 단일 재연결 게이트, 네트워크 전환, 최대 4개 동시 명령 및 입력 제한 |
| `AgentService.kt` | 유지 | 포그라운드 서비스로 연결과 두 셸 백엔드를 유지·감독 |
| `BootReceiver.kt` | 유지 | 부팅 시 자동 시작 |
| `DeviceProfile.kt` | 유지 | 기기 종류(`android`/`watch`)·SDK·앱 버전 리포팅 |
| `Prefs.kt` | 유지 | relay URL/토큰 등 로컬 설정 저장 |
| `MainActivity.kt` | 유지 | Shizuku 권한 + ADB 페어링 UI, 검증된 `am start` extras(`relay`/`token`/`adbPort`/`autostart`) 처리 |

### 2.2 Go relay + MCP 서버

기존 `server/src/*.ts` 구조를 Go로 옮긴다:

| 기존 (Node/TS) | 신규 (Go) | 역할 |
|---|---|---|
| `index.ts` | `cmd/relay` | 프로세스 진입점, HTTP/WS 라우팅 |
| (McpServer 등록부) | `internal/mcp` | MCP JSON-RPC 처리 + 툴 구현 (공식 SDK 없이 직접 구현) |
| `relay.ts` | `internal/relay` | 기기 레지스트리, 명령 큐, WS 핸들링 |
| `oauth.ts` | `internal/oauth` | claude.ai 커넥터용 최소 OAuth 레이어 |
| `release.ts`, `apkinfo.ts` | `internal/release` | GitHub 릴리즈 폴링/캐싱, APK 메타데이터 파싱 |

MCP Go SDK가 비공식/미성숙이므로 `internal/mcp`는 JSON-RPC 메시지를 직접 파싱·응답하는
얇은 레이어로 구현한다 (`plan.md` "수용하는 리스크" 참고).

### 2.3 공식 버전 서버

기존 `public.ts`와 동일한 역할을 Go로 재구현한다. relay와 별도 바이너리/컨테이너로
띄워서, 토큰이나 기기 정보를 전혀 다루지 않는 무신뢰(trust-free) 엔드포인트로 유지한다
(§4의 트러스트 경계 원칙 참고).

---
## 3. 핵심 플로우

### 3.1 셸 백엔드 선택

1. Shizuku binder가 실행 중이고 앱 권한이 승인되었으면 UserService를 바인딩해 우선 사용한다.
   단, Shizuku server uid가 정확히 2000일 때만 허용하며 root(uid 0)는 거부한다.
2. Shizuku가 없거나 중지/미승인 상태이면 이미 페어링된 온디바이스 ADB를 사용한다.
3. 명령을 전달한 뒤 binder/ADB가 끊겨도 다른 백엔드에서 같은 명령을 자동 재실행하지
   않는다. `pm`, `settings put`, 파일 쓰기 같은 비멱등 명령의 중복 실행을 막기 위해서다.

**ADB: Android 11 이상**

1. 사용자가 설정 > 개발자 옵션 > 무선 디버깅을 켜고 페어링 코드를 확인
2. rish-mcp 앱에 그 코드를 1회 입력 → `AdbShellClient`가 페어링 완료
3. 이후 앱이 자동으로 재연결·재프로비저닝 (재부팅 후에도 페어링 정보는 유지됨)

**ADB: Android 11 미만**
1. 무선 페어링 API 자체가 없으므로, PC + `adb`로 최초 1회 `adb tcpip`를 실행해
   기기의 adbd를 TCP 리스닝 모드로 전환 (단순 충전 케이블 연결로는 불가)
2. 이후 앱이 `127.0.0.1:<port>`로 자체 접속을 유지
3. **알려진 한계**: ROM에 따라 재부팅 후 이 설정이 풀릴 수 있음. 이 경우 매번 PC+adb로
   재연결해야 한다 — 별도 우회는 시도하지 않고 문서화된 제약으로 받아들인다

### 3.2 연결 모델

- 모든 기기는 foreground service에서 상시 outbound WebSocket을 유지한다.
- 핸드헬드는 20초, watch는 60초 ping을 사용하며 heartbeat는 각각 30초/90초다.
- epoch gate와 단일 지연 재연결 플래그가 죽은 소켓 callback, heartbeat, 네트워크 전환이
  동시에 새 소켓을 만드는 것을 막는다.
- 앱은 한 번에 최대 4개 명령만 실행하고, 64 KiB 명령/256자 request id/600초 timeout
  제한을 relay와 독립적으로 다시 적용한다.

### 3.3 명령 실행 (`run_shell` / `list_devices`)

MCP 툴 계약은 이번 재구성 대상이 아니므로 기존 스키마를 그대로 재사용한다
(`before/server/src/index.ts:63-131` 기준):

```
run_shell({ cmd: string, deviceId?: string, timeoutMs?: number })
  → "exit=<code> (<durationMs>ms)\n--- stdout ---\n...\n--- stderr ---\n..."
    (isError = exit code !== 0)

list_devices()
  → [{ id, name, kind, sdk, agentVersion, agentVersionCode, shellBackend,
       connectedForMs, pending }]
```

`shellBackend`는 현재 `shizuku`, `adb`, 또는 `unknown`이며 `status` 프레임으로
연결 중에도 갱신된다.

---
## 4. API/프로토콜 명세

### WS relay 프레임

기존 프레임 포맷을 그대로 재사용한다 (`before/server/src/index.ts:244-344`,
`before/docs/USAGE.md` Appendix A):

```json
// relay → 기기
{ "type": "exec", "reqId": "<uuid>", "cmd": "...", "timeoutMs": 60000 }

// 기기 → relay
{ "type": "result", "reqId": "<uuid>", "code": 0,
  "stdout": "...", "stderr": "", "truncated": false, "durationMs": 127 }

// 활성 셸 백엔드가 바뀔 때 기기 → relay
{ "type": "status", "backend": "shizuku" }
```

연결 쿼리 파라미터는 `token`, `deviceId`, `name`, `sdk`, `kind`, `ver`, `vc`, `backend`다.
앱은 OkHttp `HttpUrl`로 값을 인코딩하고 기존 동명 쿼리를 교체해 토큰/기기명에 `&`, 공백
등이 있어도 파라미터 경계가 깨지지 않게 한다.

### OAuth

기존과 동일한 단일 사용자 모델을 Go로 재구현한다: DB 없이 `AI_TOKEN`에서 유도한 HMAC로
모든 토큰을 서명, PKCE(S256) 필수, 인가 코드 단발성/5분 TTL, `AI_TOKEN` 회전 시 모든
토큰 즉시 무효화. 상세 사양은 `before/docs/USAGE.md` §6을 원본으로 삼는다.

### 공식 버전 서버 API

```
GET /api/version/release  → { versionName, versionCode, tag, sizeBytes, sha256, modifiedAt, download }
GET /agent.apk             → APK 바이너리 (무토큰, IP당 rate limit)
```

기존과 동일한 응답 shape을 유지한다.

---
## 5. 보안 모델

- 트러스트 경계는 기존과 동일하게 둘로 나눈다: `MCP_HOST`(private, `/mcp`+`/agent`+OAuth,
  토큰 필요)와 `PUBLIC_MCP_HOST`(public, 버전/APK만, 무토큰)
- `AI_TOKEN`은 AI 클라이언트용 마스터 키, `DEVICE_TOKEN`은 기기가 relay에 등록할 때
  쓰는 공유 비밀 — 역할과 회전 방식 모두 기존과 동일하게 유지
- root 권한은 요구하지 않는다 (`plan.md` 비목표) — 셸 권한은 여전히 uid 2000 수준
- root 모드 Shizuku도 사용하지 않는다. `Shizuku.getUid()`가 2000이 아니면 bind하지 않고
  ADB 폴백으로 전환한다.

---
## 6. 리소스·성능 목표

| 항목 | 목표 | 비고 |
|---|---|---|
| 저사양 기기 유휴 배터리 소모 | 실기기 측정 전 목표 미확정 | watch ping 60s / heartbeat 90s 적용 |
| 저사양 기기 유휴 메모리 | <50MB 목표 | Firebase SDK 제거, 실기기 계측 필요 |
| 셸 실행 동시성 | 최대 4 | 무제한 coroutine/process 생성 방지 |
| 서버 동시 접속·명령 처리 지연 | 기존 Node/TS 대비 확실한 개선 | 정량 벤치마크는 구현 후 별도 측정 |

---
## 7. 알려진 제약/리스크

- **Go MCP 생태계 미성숙**: 공식 SDK가 없어 MCP JSON-RPC, relay 프로토콜, OAuth를
  전부 자체 구현해야 함 — 개인 프로젝트 규모 대비 투자가 큰 편임을 인지하고 진행
  (구현 완료: `internal/mcp`, `internal/oauth`)
- **Android 11 미만 USB 페어링**: PC + adb가 실제로 필요하고, 재부팅 후 유지 여부는
  ROM에 따라 다름 — 실기기 검증 전까지는 가정으로 취급
- **Push wake 미제공**: relay sender/Firebase 프로젝트 없이 수신 클래스만 두는 것은
  기능이 아니므로 SDK와 스텁을 제거했다. 다시 도입할 때는 등록 API, 토큰 회전/폐기,
  발신 인증정보 보관, 전달 실패 폴백, 실제 watch 배터리 계측을 한 변경으로 구현해야 한다.
- **Android 앱은 실기기 미검증**: Shizuku bind/권한, ADB 무선 페어링, fallback 전환,
  실제 명령 실행은 Docker 컴파일과 순수 로직 유닛 테스트만으로 증명되지 않는다.
- **Go 서버 배포 구성 완료**: `server/Dockerfile`로 두 바이너리 이미지 빌드,
  `docker-compose.yml`(repo 루트)로 Traefik/Dokploy 배포 구성 완료. 컨테이너는
  `read_only: true` + `tmpfs` + non-root `USER appuser`(uid 10001)로 하드닝됨

---
## 8. 구현 로드맵 (제안 순서)

1. ✅ Go relay 골격 + MCP 툴 2개(`run_shell`, `list_devices`) + 정적 bearer 인증
2. ✅ Shizuku 우선 + `AdbShellClient` 폴백 — 권한 UI, AIDL UserService,
   중복 실행 없는 router, 포트/URL/명령 검증까지 배선. 실기기 검증은 아직
3. ✅ OAuth 레이어 이식 — `internal/oauth`, `/mcp`이 정적 bearer와 OAuth access
   token을 병행 수용
4. ⏸ push-wake 연결 — 불완전 FCM SDK/스텁 제거. relay 발신 경로와 실기기 계측을
   포함할 수 있을 때만 재개
5. ✅ 공식 버전 서버 — `cmd/publicserver` (`/healthz`, `/api/version/release`,
   `/agent.apk`), GitHub 릴리즈 폴링/캐싱(`internal/release`)
6. ✅ (코드 기준) Android 11 미만 USB 경로 — `MainActivity`/`Prefs.adbPort`가 이미
   host-agnostic이라 pre-11 사용자는 PC+adb로 얻은 포트만 입력하면 됨. PC+adb
   tcpip 브리지 자체는 사용자가 수행하는 수동 단계라 앱 코드로 검증할 대상이 아님
8. ✅ CLI 설정 도구 — `server/cmd/setup` (Go 바이너리) + `cli/` (Node.js,
   `npx rish-mcp-setup`), APK 다운로드/로컬 빌드/릴레이 실행
9. ✅ 로그 인젝션 수정 — `ws.go`의 deviceId 로깅에 `sanitizeLogField` 적용,
   회귀 테스트(`TestRegisterAgentLogInjection`) 추가
10. ✅ 테스트 커버리지 86.6% — `internal/mcp`/`internal/oauth`/`internal/relay` 100%,
    `internal/release` 99.3%, `cmd/publicserver` 80.8%, `cmd/relay` 81.8%, `cmd/setup` 66.5%
11. ✅ Docker 컨테이너 하드닝 — non-root `USER appuser`, `read_only: true`,
    `tmpfs: /tmp:size=64M,noexec,nosuid,nodev`
