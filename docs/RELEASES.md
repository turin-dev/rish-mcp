# Release channels

The repository contains two generations of the product. Their artifacts must
not share an implicit "latest APK" channel.

## Legacy releases

GitHub tags `v0.2.0`, `v0.3.0`, and `v0.5.0` contain the previous
legacy Android agent and Node/TypeScript server. They are retained for
existing users and historical reference, but they are not compatible release
artifacts for the current Go rewrite and its isolated `agent-v` channel.

## Rewrite agent releases

The rewritten Android agent uses tags in the strict form
`agent-vMAJOR.MINOR.PATCH` (three decimal components, with no omitted or extra
component and no leading zero except the value `0`). Its APK `versionName` must
be exactly `MAJOR.MINOR.PATCH`. The prefix is configurable with
`RELEASE_TAG_PREFIX`, but defaults to `agent-v`, preventing an old or unrelated
APK from becoming the current agent by accident.

The public version server ignores drafts, prereleases, malformed channel tags,
and releases without an `.apk` asset. It selects the highest compatible tag by
semantic version rather than GitHub release creation order. Once a rewrite APK
is cached, a replacement must have both a greater tag version and a strictly
greater Android `versionCode`; lower or equal versions are never downloaded as
an update. Both downloaded and cached APKs are rejected when their embedded
`versionName` differs from the tag suffix. Downloaded, cached, and `APK_PATH`
override files are capped at 128 MiB; inflated `AndroidManifest.xml` data is
bounded separately while parsing.

The paginated release scan is capped at 1,000 entries. Reaching that bound
before a final short page rejects the entire refresh, so the server never
publishes a lower tag selected from a known-partial creation-ordered list.

Validated APKs are stored at immutable SHA-256-based filenames. A small
`release.json` pointer uses a rollback backup, so an ordinary failed update or
process interruption keeps the last-good metadata/artifact pair recoverable.
This is not a power-loss durability guarantee. Startup reconciles an interrupted
pointer update and, only after a valid current pointer is loaded with no
unresolved backup, removes non-current immutable APK artifacts. The cache is
single-writer: run only one public-server process per `RELEASE_CACHE_DIR`.
`APK_PATH` remains an explicit local override and bypasses GitHub polling.

The npm package `rish-mcp-setup` has its own independent semantic version. A
CLI package version is not an Android agent version and must not be used to
select an APK.

The current rewrite source identifies itself as `1.0.0`: the Android agent uses
`versionName 1.0.0` with monotonic `versionCode 10000`, the MCP server reports
`1.0.0`, and the npm setup CLI package is `1.0.0`. This source-version bump does
not publish an artifact. No signed rewrite APK has been published yet, and the
`agent-v1.0.0` tag remains reserved until every publication gate below passes.

## Publication gates

An `agent-vX.Y.Z` release is ready only after all of the following are recorded:

1. Go, CLI, web, and Android CI checks pass for the exact commit.
2. Android unit tests and a release APK build pass.
3. The APK is signed with the official key and its signature is verified.
4. The tag suffix and APK `versionName` agree, and the APK `versionCode` is
   strictly greater than the most recently published rewrite agent.
   Source releases use `MAJOR*10000 + MINOR*100 + PATCH`; 1.0.0 is therefore
   10000, safely above the legacy v0.5.0 package's code 5.
5. The `.apk` asset is no larger than 128 MiB, and its filename, SHA-256
   checksum, and release metadata agree.
6. Pairing, relay connection, `list_devices`, and `run_shell` are exercised on
   a real supported Android device.
7. Upgrade behavior from any supported prior rewrite release is verified.

Build success alone is not release acceptance. Until these gates are met, use
a locally built debug APK and do not advertise it as an official release.
