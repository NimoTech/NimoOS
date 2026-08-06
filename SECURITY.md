# Security Policy

## Reporting a vulnerability

Please report security issues **privately** — do not open a public issue.

- Preferred: GitHub [private vulnerability reporting](https://github.com/NimoTech/NimoOS/security/advisories/new)
- Email: `nimonas@yeaher.com`

Please include: affected version, affected component, reproduction steps, and
the impact you observed.

**Our commitment:**

| | |
|---|---|
| Acknowledge your report | within 5 working days |
| Initial assessment | within 10 working days |
| Fix or documented mitigation | depends on severity; we will keep you updated |

We will credit reporters in the release notes unless you prefer otherwise.

## Supported versions

NimoOS is currently in the `v1.9.x-alpha` line. Only the latest release
receives security fixes. There is no long-term support branch yet.

## Known limitations

NimoOS supports multiple user accounts — each account gets its own role, its
own Linux system user, and its own data directory. However, two subsystems do
**not** currently enforce that boundary:

- **Photos** — the photo library is global. Every user account sees the same
  albums. There is no per-user filtering at the data layer.
- **Search** — the filename index is global and is not filtered by per-user
  permissions.

This means NimoOS is currently suitable for **single-user deployments, or
multi-user deployments where all users are fully trusted with each other's
photos and filenames**. Do not use it for multi-tenant scenarios, and do not
expose an instance to untrusted users or to the public internet.

Closing these two gaps is on the roadmap; the shared prerequisite is
consolidating the service-to-service identity chain. See
[ROADMAP.md](./ROADMAP.md).

## AI agent security model

The built-in AI agent can read files and run shell commands on your server.
Its containment is built from **behavioural guardrails**, not a hard sandbox:

- **Command gating** — a policy layer inspects shell commands before execution
  and blocks destructive patterns.
- **Filesystem gating** — a deny-only gate restricts which paths the agent may
  read, with system-internal directories carved out.
- **Egress chokepoint** — the agent runs in a network namespace with no default
  route; all outbound traffic passes through a proxy that applies allow/deny
  rules and inspects uploads for sensitive content.
- **Audit log** — agent actions are recorded.

**What this means in practice:** these layers reliably stop accidents and
common prompt-injection paths. They should **not** be assumed to stop a
determined, targeted bypass by someone who can read this source code — which,
as an open-source project, is everyone.

Run the agent only with models and integrations you trust, and treat the
machine it runs on as one the agent has broad access to.
