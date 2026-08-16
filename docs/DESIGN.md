# rish-mcp 설계 문서 (revive)

이 문서는 `plan.md`(의사결정 로그)에서 확정된 목표·비목표·아키텍처 결정을 실제 구현
가능한 수준으로 구체화한다. "왜 이렇게 정했는지"는 `plan.md`를, "무엇을 어떻게
만드는지"는 이 문서를 보면 된다.

---
## 1. 개요

AI(Claude 등)가 가상머신이 아니라 사용자의 실제 Android 기기를 조작할 수 있게
해주는 MCP 서버 + 에이전트다. 사용자가 소유한 기기 하나(또는 소수)를 대상으로 하는
개인용 도구이며, 관리자를 두는 멀티테넌트 서비스가 아니다.

```
                 상시 WS (일반 기기)
┌─────────┐   MCP   ┌──────────────────┐ ◀───────────────── ┌──────────────┐
│   AI    │──HTTPS─▶│  Go relay + MCP  │                    │ Android 앱   │
│(Claude) │ ◀───────│     서버         │──FCM 웨이크업──────▶│ (저사양 기기) │
└─────────┘         └──────────────────┘   (Google FCM 경유) └──────────────┘
                             │
                             │ 버전/체크섬 조회, APK 배포
                             ▼
                   ┌──────────────────┐
                   │  공식 버전 서버   │  (별도 바이너리/컨테이너, 무토큰)
                   └──────────────────┘
```

- 기기는 항상 **아웃바운드**로만 연결한다 (CGNAT 뒤에서도 동작, 인바운드 노출 없음).
- 일반 폰/태블릿은 상시 WebSocket 연결, WearOS 등 저사양 기기는 FCM으로 깨워서
  짧게 연결하는 하이브리드 모델을 쓴다 (§3.2).
- relay는 개인이 셀프호스팅한다. 공식 서버는 버전 정보와 APK 배포만 담당하고
  relay 기능은 포함하지 않는다 (`plan.md` 비목표: multi-tenant 아님).

---
## 2. 컴포넌트

### 2.1 Android 앱

| 모듈 | 상태 | 역할 |
|---|---|---|
| `AdbShellClient.kt` | **신규** | 온디바이스 ADB 프로토콜 클라이언트. `ShellUserService.kt`(Shizuku AIDL 바인딩)를 대체. Android 11+는 무선 디버깅 페어링, 11 미만은 USB-tcpip 브리지로 셸(uid 2000) 권한 확보 |
| `ConnectionManager.kt` | **신규** | 기기 종류에 따라 상시 WS 유지 / FCM 웨이크업+폴백 폴링 중 라우팅 |
| `FcmWakeReceiver.kt` | **신규** | 저사양 기기에서 FCM 푸시 수신 → 짧은 WS 세션 시작 |
| `AgentService.kt` | 유지 | 포그라운드 서비스로 연결을 유지·감독 (역할 동일, `AdbShellClient`/`ConnectionManager` 사용하도록 내부 배선만 교체) |
| `BootReceiver.kt` | 유지 | 부팅 시 자동 시작 |
| `DeviceProfile.kt` | 유지 | 기기 종류(`android`/`watch`)·SDK·앱 버전 리포팅 |
| `Prefs.kt` | 유지 | relay URL/토큰 등 로컬 설정 저장 |
| `MainActivity.kt` | 유지 | 프로비저닝 UI + `am start` extras(`relay`/`token`/`autostart`) 처리. 완전 무탭은 더 이상 목표가 아니므로(`plan.md` 비목표), 최초 1회 페어링 확인 화면이 추가됨 |

`ShellUserService.kt`, Shizuku 관련 AIDL(`IUserService.aidl`)은 제거 대상이다.

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

### 3.1 셸 접근 페어링 (Shizuku 대체)

**Android 11 이상**
1. 사용자가 설정 > 개발자 옵션 > 무선 디버깅을 켜고 페어링 코드를 확인
2. rish-mcp 앱에 그 코드를 1회 입력 → `AdbShellClient`가 페어링 완료
3. 이후 앱이 자동으로 재연결·재프로비저닝 (재부팅 후에도 페어링 정보는 유지됨)

**Android 11 미만**
1. 무선 페어링 API 자체가 없으므로, PC + `adb`로 최초 1회 `adb tcpip`를 실행해
   기기의 adbd를 TCP 리스닝 모드로 전환 (단순 충전 케이블 연결로는 불가)
2. 이후 앱이 `127.0.0.1:<port>`로 자체 접속을 유지
3. **알려진 한계**: ROM에 따라 재부팅 후 이 설정이 풀릴 수 있음. 이 경우 매번 PC+adb로
   재연결해야 한다 — 별도 우회는 시도하지 않고 문서화된 제약으로 받아들인다

### 3.2 연결 모델

- **일반 폰/태블릿**: 상시 WebSocket 연결 유지, ping 25초 주기 (기존과 동일)
- **저사양 기기(WearOS 등)**: 평소엔 연결을 끊어 두고, relay가 명령을 받으면 FCM으로
  기기를 깨움 → 기기가 짧게 WS 연결해서 명령 실행·결과 반환 후 즉시 종료
  - FCM 전달 실패에 대비해 주기적 폴링을 폴백으로 유지 (주기는 **TBD**, 구현 시 확정)
  - Wear OS 3+ 는 대부분 GMS를 탑재하므로 FCM 적용 가능을 전제로 함

### 3.3 명령 실행 (`run_shell` / `list_devices`)

MCP 툴 계약은 이번 재구성 대상이 아니므로 기존 스키마를 그대로 재사용한다
(`before/server/src/index.ts:63-131` 기준):

```
run_shell({ cmd: string, deviceId?: string, timeoutMs?: number })
  → "exit=<code> (<durationMs>ms)\n--- stdout ---\n...\n--- stderr ---\n..."
    (isError = exit code !== 0)

list_devices()
  → [{ id, name, kind, sdk, agentVersion, agentVersionCode,
       latestAgentVersion, updateAvailable, connectedForMs, pending }]
```

저사양 기기 경로에서도 응답 shape은 동일하다 — 다만 FCM 웨이크업 때문에 첫 명령의
지연 시간이 상시 연결 기기보다 클 수 있다.

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
```

연결 쿼리 파라미터(`token`, `deviceId`, `name`, `sdk`, `kind`, `ver`, `vc`)와 keepalive
정책(일반 25초 / 기존 watch 60초 방식)은 일반 상시 연결 기기에 그대로 적용한다. 저사양
기기는 FCM 웨이크업 이후 같은 프레임으로 짧은 세션만 수행하고 ping 루프 자체를 돌리지
않는다.

### OAuth

기존과 동일한 단일 사용자 모델을 Go로 재구현한다: DB 없이 `AI_TOKEN`에서 유도한 HMAC로
모든 토큰을 서명, PKCE(S256) 필수, 인가 코드 단발성/5분 TTL, `AI_TOKEN` 회전 시 모든
토큰 즉시 무효화. 상세 사양은 `before/docs/USAGE.md` §6을 원본으로 삼는다.

### 공식 버전 서버 API

```
GET /api/version/release  → { versionName, versionCode, source, sizeBytes, sha256, modifiedAt, download }
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

---
## 6. 리소스·성능 목표

| 항목 | 목표 | 비고 |
|---|---|---|
| 저사양 기기 유휴 배터리 소모 | 시간당 2~3% 이내 | 기존 Shizuku 방식(핑 25s/60s) 대비 개선 |
| 저사양 기기 유휴 메모리 | <50MB | |
| 저사양 기기 유휴 CPU | 웨이크업/폴링 순간에만 짧게 사용, 그 외 0% | |
| 서버 동시 접속·명령 처리 지연 | 기존 Node/TS 대비 확실한 개선 | 정량 벤치마크는 구현 후 별도 측정 |

---
## 7. 알려진 제약/리스크

- **Go MCP 생태계 미성숙**: 공식 SDK가 없어 MCP JSON-RPC, relay 프로토콜, OAuth를
  전부 자체 구현해야 함 — 개인 프로젝트 규모 대비 투자가 큰 편임을 인지하고 진행
  (구현 완료: `internal/mcp`, `internal/oauth`)
- **Android 11 미만 USB 페어링**: PC + adb가 실제로 필요하고, 재부팅 후 유지 여부는
  ROM에 따라 다름 — 실기기 검증 전까지는 가정으로 취급
- **FCM 하이브리드 연결이 통째로 보류 상태**: Firebase 프로젝트가 없어 §3.2/§8의
  저사양 기기 웨이크업 경로를 구현할 수 없음. 재개하려면: (a) Firebase 프로젝트
  생성, (b) `google-services.json`을 앱에 추가 + FCM SDK 의존성, (c) relay가 FCM
  발신 크리덴셜(서비스 계정 키)로 기기를 깨우는 서버측 로직, (d)
  `ConnectionManager`에 이미 표시해둔 자리에 `FcmWakeReceiver.kt` 구현. 그 전까지
  모든 기기가 상시 WS를 씀
- **Android 앱은 실기기 미검증**: `AdbShellClient`/`ConnectionManager`/
  `MainActivity`는 컴파일·유닛 테스트(순수 로직 부분만)는 통과했지만, 실제 무선
  페어링·연결·명령 실행은 이 개발 환경에 연결된 Android 기기가 없어 검증하지 못함
- **Go 서버 배포 구성 미완성**: `server/Dockerfile`로 두 바이너리 이미지는 빌드
  되지만, 리버스 프록시/docker-compose 설정은 기존 `before/docker-compose.yml`
  (Traefik 기반)을 그대로 재현하지 않았음

---
## 8. 구현 로드맵 (제안 순서)

1. ✅ Go relay 골격 + MCP 툴 2개(`run_shell`, `list_devices`) + 정적 bearer 인증
2. ✅ 앱 `AdbShellClient` (Android 11+ 무선 페어링 경로 우선) — libadb-android 기반,
   `ConnectionManager`/`AgentService`/`MainActivity` 페어링 UI까지 배선 완료.
   실기기 검증은 아직
3. ✅ OAuth 레이어 이식 — `internal/oauth`, `/mcp`이 정적 bearer와 OAuth access
   token을 병행 수용
4. ⛔ 저사양 기기 하이브리드 연결(`ConnectionManager` + FCM) — **보류** (§7 참고,
   Firebase 프로젝트 필요)
5. ✅ 공식 버전 서버 — `cmd/publicserver` (`/healthz`, `/api/version/release`,
   `/agent.apk`), GitHub 릴리즈 폴링/캐싱(`internal/release`)
6. ✅ (코드 기준) Android 11 미만 USB 경로 — `MainActivity`/`Prefs.adbPort`가 이미
   host-agnostic이라 pre-11 사용자는 PC+adb로 얻은 포트만 입력하면 됨. PC+adb
   tcpip 브리지 자체는 사용자가 수행하는 수동 단계라 앱 코드로 검증할 대상이 아님
7. ✅ 통합 문서화 — `README.md`, `docs/USAGE.md` 재작성, 이 로드맵 갱신
