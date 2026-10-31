<div align="center">

# rish-mcp

**루트, Shizuku, 상시 연결된 PC 없이 AI가 내 Android 기기의 `adb shell`을 MCP로 사용할 수 있게 해주는 셀프호스팅 도구입니다.**

[![CI](https://github.com/turin-dev/rish-mcp/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/turin-dev/rish-mcp/actions/workflows/ci.yml)
[![CodeQL](https://github.com/turin-dev/rish-mcp/actions/workflows/codeql.yml/badge.svg?branch=master)](https://github.com/turin-dev/rish-mcp/actions/workflows/codeql.yml)
[![npm](https://img.shields.io/npm/v/rish-mcp-setup?logo=npm)](https://www.npmjs.com/package/rish-mcp-setup)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[English](README.md) · **한국어**

</div>

> [!WARNING]
> **현재 리라이트 버전은 프리뷰 단계입니다.** Go 릴레이와 Android 에이전트는 제한된 테스트에 사용할 수 있지만, Android 리라이트는 아직 실제 기기 기준의 안정 릴리스 검증을 모두 통과하지 않았습니다. 운영 환경에 사용하기 전에 [릴리스 채널](docs/RELEASES.md)을 확인하세요.

> [!CAUTION]
> GitHub 릴리스 `v0.2.0`~`v0.5.0`은 예전 Shizuku 기반 구현입니다. 현재 리라이트는 별도의 `agent-v*` 릴리스 채널을 사용하며, 현재 서명된 프리뷰는 `agent-v0.1.0`입니다.

## rish-mcp란?

rish-mcp는 Android 기기 자체의 셸(`uid 2000`, `adb shell`과 같은 권한 수준)을 AI 클라이언트가 사용할 수 있는 MCP 도구로 노출합니다.

Android 에이전트는 사용자가 관리하는 릴레이 서버로 **아웃바운드 WebSocket** 연결을 생성합니다. AI 클라이언트는 **Streamable HTTP 기반 MCP**로 릴레이에 접속하고 Bearer 토큰으로 인증합니다. 기기가 인터넷에서 들어오는 연결을 직접 받을 필요가 없어 NAT/CGNAT 환경에서도 사용할 수 있습니다.

```text
                               아웃바운드 WebSocket
┌──────────────┐   MCP/HTTPS   ┌──────────────────────┐ ◀────────────────── ┌──────────────┐
│  AI 클라이언트 │ ────────────▶ │   Go 릴레이 + MCP     │                     │ Android 에이전트│
│ Claude 등     │ ◀──────────── │  server/cmd/relay    │ ─── 셸 명령 실행 ──▶ │  adb shell   │
└──────────────┘                └──────────────────────┘ ◀── 실행 결과 반환 ── └──────────────┘
                                         │
                                         │ 버전 정보 / APK
                                         ▼
                               ┌──────────────────────┐
                               │ 공개 버전 서버         │
                               │ server/cmd/publicserver
                               └──────────────────────┘
```

이전 Node/TypeScript + Shizuku 구현은 참고용으로 [`before/`](before/)에 남아 있습니다. 리라이트 이유는 [`plan.md`](plan.md), 현재 아키텍처는 [`docs/DESIGN.md`](docs/DESIGN.md)를 참고하세요.

## 주요 특징

- **루트와 Shizuku가 필요 없음** — Android 앱이 기기 자신의 `adbd`와 직접 통신합니다.
- **MCP 네이티브** — 원격 Streamable HTTP MCP 엔드포인트에서 `list_devices`, `run_shell`을 제공합니다.
- **기기 측 아웃바운드 연결만 사용** — 휴대폰에 외부 공개 포트를 열 필요가 없습니다.
- **셀프호스팅 릴레이** — 셸 접근 자격 증명과 명령 트래픽을 직접 관리하는 인프라에 둘 수 있습니다.
- **Go 릴레이** — 배포가 간단하고 서버 바이너리가 작으며 동시성 처리가 명확합니다.
- **Kotlin Android 에이전트** — Android 11+ 무선 디버깅 페어링을 지원하고, 구형 Android는 `adb tcpip` 방식으로 연결할 수 있습니다.
- **공개 업데이트 서비스 분리** — 버전 정보/APK 제공 기능을 셸 접근 권한이 있는 릴레이와 분리합니다.

## 프로젝트 상태

| 구성 요소 | 상태 |
| --- | --- |
| Go 릴레이 (`server/cmd/relay`) — MCP, WebSocket 릴레이, Bearer 인증 + OAuth | ✅ 빌드 및 테스트 완료 |
| 공개 버전 서버 (`server/cmd/publicserver`) | ✅ 빌드 및 테스트 완료 |
| Android `AdbShellClient` — 페어링 및 셸 실행 | ✅ 빌드 완료, 단위 테스트 가능한 부분 테스트 완료 |
| Android UI/서비스 — `MainActivity`, `AgentService`, `ConnectionManager` | 🧪 빌드 성공, 실제 기기 검증 진행 중 |
| 서명된 리라이트 APK | 🧪 `agent-v0.1.0` 프리뷰 제공 중 |
| Docker 패키징 / Compose 배포 | ✅ 제공됨 |
| 저사양 하이브리드 모드 + FCM 웨이크업 | ⛔ 계획 단계, Firebase 설정 필요 |

## 빠른 시작

### 1. 릴레이 설정

Docker가 설치된 머신에서 설치 스크립트를 실행하세요. 다시 실행하면 릴레이가 업데이트됩니다.

```bash
curl -fsSL https://rish-mcp.turin.my | sh
```

실행 전에 스크립트 내용을 확인하려면 [`rish-mcp.turin.my/relay`](https://rish-mcp.turin.my/relay)를 열어 보세요. npm 도구로도 같은 작업을 할 수 있습니다: `npx rish-mcp-setup --yes --action server`.

설치 도구는 `AI_TOKEN`과 `DEVICE_TOKEN`을 생성하거나 기존 값을 재사용하고, `~/.config/rish-mcp/relay.env`에 저장한 뒤 `rish-mcp-relay` 컨테이너를 실행합니다.

리버스 프록시, Docker Compose, 수동 배포 방법은 [`docs/USAGE.md`](docs/USAGE.md)를 참고하세요.

### 2. Android 에이전트 설치 및 페어링

[GitHub Releases](https://github.com/turin-dev/rish-mcp/releases)의 서명된 `agent-v*` 아티팩트를 사용하고 해당 릴리스 노트를 따라 설치하세요.

**Android 11+**에서는 **개발자 옵션 → 무선 디버깅 → 페어링 코드로 기기 페어링**을 켠 뒤 rish-mcp 앱에 페어링 정보를 입력합니다. 구형 Android는 문서에 설명된 PC 보조 `adb tcpip` 방식을 사용합니다.

상세 절차는 [`docs/USAGE.md`](docs/USAGE.md#3-pair-the-android-agent)를 참고하세요.

### 3. MCP 클라이언트 연결

클라이언트 설정을 자동 생성할 수 있습니다.

```bash
npx rish-mcp-setup --yes --action client \
  --url https://mcp.example.com/mcp \
  --token "$AI_TOKEN"
```

또는 호환되는 MCP 클라이언트에 직접 설정할 수 있습니다.

```json
{
  "mcpServers": {
    "phone": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {
        "Authorization": "Bearer <AI_TOKEN>"
      }
    }
  }
}
```

Claude CLI에서는 다음과 같이 추가할 수 있습니다.

```bash
claude mcp add --transport http phone https://mcp.example.com/mcp \
  --header "Authorization: Bearer <AI_TOKEN>"
```

> [!IMPORTANT]
> Bearer 토큰은 `Authorization` 헤더에 넣으세요. URL이나 쿼리 문자열에 접근 토큰을 넣지 마세요.

## MCP 도구

### `list_devices()`

연결된 Android 기기 목록을 반환합니다. 에이전트 버전, 연결 시간, 대기 중인 명령 수 등의 연결 정보도 포함합니다.

### `run_shell({ cmd, deviceId?, timeoutMs? })`

Android shell uid(`2000`) 권한으로 명령을 실행하고 stdout, stderr, 종료 코드, 실행 시간 정보를 반환합니다. 여러 기기가 동시에 연결된 경우에만 `deviceId`가 필요합니다.

```text
run_shell({ "cmd": "getprop ro.product.model" })
```

전체 도구 계약은 [`docs/USAGE.md`](docs/USAGE.md#5-tool-reference)를 참고하세요.

## 저장소 구조

```text
app/                      Android Kotlin 에이전트
server/cmd/relay/         MCP + WebSocket 릴레이
server/cmd/publicserver/  공개 버전/APK 서버
cli/                      rish-mcp-setup npm 패키지
docs/                     설계, 사용법, 릴리스 문서
before/                   레거시 Shizuku + Node/TypeScript 구현
```

## 소스에서 빌드

### Go 서버

```bash
cd server
go build ./...
go test ./...
```

### 서버 컨테이너

```bash
docker build --target relay -t rishmcp-relay server
docker build --target publicserver -t rishmcp-public server
```

### Android 디버그 빌드

저장소 루트에서 실행합니다.

```bash
docker build -t rishmcp-android-build -f app/Dockerfile.build app

docker run --rm -v "$PWD/app:/work" -w /work rishmcp-android-build \
  gradle --no-daemon testDebugUnitTest assembleDebug
```

디버그 APK 출력 경로:

```text
app/app/build/outputs/apk/debug/app-debug.apk
```

공식 서명 빌드는 엄격한 `agent-vMAJOR.MINOR.PATCH` 태그를 사용하는 [Android 릴리스 워크플로](.github/workflows/release.yml)에서 생성됩니다.

## 배포

Traefik/Dokploy용 Compose 설정이 포함되어 있습니다.

```bash
cp .env.example .env
# MCP_HOST / PUBLIC_MCP_HOST를 수정하고 두 비밀값을 교체하세요.
openssl rand -hex 32

docker network create dokploy-network  # 필요한 경우 한 번만
docker compose up -d --build
curl -fsS "https://${MCP_HOST}/healthz"
```

릴레이는 셸 접근 비밀값과 기기 트래픽을 처리합니다. 별도의 공개 서버는 릴레이 토큰을 받지 않으며 릴리스 메타데이터와 APK만 제공합니다.

환경 변수, OAuth, 리버스 프록시 설정, 문제 해결은 [`docs/USAGE.md`](docs/USAGE.md)를 참고하세요.

## 보안 모델

> [!WARNING]
> `AI_TOKEN`은 연결된 기기에 원격 `adb shell` 접근 권한을 제공하는 것과 사실상 같습니다. SSH 개인 키와 비슷한 수준의 고가치 자격 증명으로 취급하세요.

- 명령은 root가 아니라 **shell uid 2000**으로 실행됩니다.
- Android 에이전트가 릴레이에 먼저 연결하며, 기기에 인바운드 셸 서비스를 열지 않습니다.
- 실제 배포에서는 **HTTPS/WSS**를 사용하세요.
- `AI_TOKEN`, `DEVICE_TOKEN`은 외부에 노출하지 말고 유출 가능성이 있으면 교체하세요.
- 범위는 의도적으로 **단일 사용자 / 기기 소유자 운영**이며 멀티테넌트 원격 기기 플랫폼을 목표로 하지 않습니다.

취약점은 [`SECURITY.md`](SECURITY.md)의 안내에 따라 비공개로 제보해 주세요.

## 릴리스 채널

| 채널 | 의미 |
| --- | --- |
| `agent-v*` | 현재 Android 리라이트 아티팩트 |
| `v0.2.0`–`v0.5.0` | 레거시 Shizuku 앱, 현재 리라이트와 호환되지 않음 |
| npm `rish-mcp-setup` | 릴레이/클라이언트 설정 전용. Android APK는 설치하지 않음 |

승격 조건과 서명 세부 정보는 [`docs/RELEASES.md`](docs/RELEASES.md)에 있습니다.

## 문서

| 문서 | 내용 |
| --- | --- |
| [`docs/USAGE.md`](docs/USAGE.md) | 배포, 페어링, MCP 도구, OAuth, 프로토콜, 문제 해결 |
| [`docs/DESIGN.md`](docs/DESIGN.md) | 현재 아키텍처와 구현 범위 |
| [`docs/RELEASES.md`](docs/RELEASES.md) | 릴리스 채널, 서명, 승격 조건 |
| [`plan.md`](plan.md) | 리라이트 이유와 프로젝트 방향 |
| [`cli/README.md`](cli/README.md) | `rish-mcp-setup` CLI 레퍼런스 |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | 기여 가이드 |

## 기여

이슈와 Pull Request를 환영합니다. 기여 전 [`CONTRIBUTING.md`](CONTRIBUTING.md)를 읽어 주세요.

## 라이선스

rish-mcp는 [MIT License](LICENSE)로 배포됩니다.
