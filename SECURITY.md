# Security policy

rish-mcp grants an AI client shell-level access to an owner's Android device.
Treat vulnerabilities that bypass authentication, cross device boundaries,
expose tokens, alter release artifacts, or broaden shell privileges as
security-sensitive.

## Supported code

Security fixes target the current `master` branch while the no-Shizuku rewrite
is under development. GitHub releases `v0.2.0` through `v0.5.0` belong to the
legacy Shizuku implementation and are not supported as releases of the current
architecture.

## Reporting a vulnerability

Use GitHub's private **Report a vulnerability** form in this repository's
Security tab. Do not open a public issue, discussion, or pull request for an
unpatched vulnerability, and do not include live tokens, device identifiers,
or private relay URLs in a report.

Please include:

- the affected commit, component, and deployment shape;
- reproducible steps or a minimal proof of concept;
- the security boundary that was crossed and the expected behavior;
- any known mitigations; and
- whether the issue has been disclosed elsewhere.

The maintainer will acknowledge the report as soon as practical, validate its
scope, and coordinate remediation and disclosure through the private advisory.
The architecture and trust boundaries are documented in
[`docs/DESIGN.md`](docs/DESIGN.md).
