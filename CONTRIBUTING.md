# Contributing to NimoOS

Thanks for taking the time to contribute. This document covers the things
that are specific to how NimoOS is built — the multi-repository layout, the
build prerequisites, and the conventions a pull request is expected to
follow. If anything here is out of date, that's itself worth a pull request.

## The repository is not the whole system

NimoOS is not a single codebase. It is a personal-cloud system made of 17+
independent git repositories under the [NimoTech](https://github.com/NimoTech)
organisation — this repository (`NimoOS`) is the core service (file
management, hardware monitoring, Samba, cloud-storage mounts, the web
terminal), and each other backend capability (`NimoOS-Gateway`,
`NimoOS-UserService`, `NimoOS-AppManagement`, `NimoOS-Photos`,
`NimoOS-Search`, `NimoOS-AI`, and so on) is its own Go module in its own
repository, with its own `go.mod`, its own CI, and its own release cadence.
`NimoOS-UI` (the web frontend) and `NimoOS-Common` (the shared Go library
consumed by every service) are separate repositories too.

**This repo is a fork of CasaOS** (Apache-2.0), substantially modified; see
[`NOTICE`](./NOTICE) for the attribution details. That history explains why
some of the layout looks the way it does, but it does not change anything
below.

### Cloning the whole workspace

You don't need every repository to fix a bug that's contained in one
service, but building most of them requires the others to exist as sibling
directories, because Go services `replace` their way to a local checkout of
`NimoOS-Common` (and, for some services, other siblings) instead of a tagged
release:

```
replace github.com/NimoTech/NimoOS-Common => ../NimoOS-Common
```

That means a full build needs a workspace that looks like:

```
nimoos/
├── NimoOS/                # this repository
├── NimoOS-Common/         # shared library — every Go service replaces to it
├── NimoOS-MessageBus/     # other services' `go generate` reads its openapi.yaml
├── NimoOS-Gateway/  NimoOS-UserService/  NimoOS-AppManagement/  ...
└── NimoOS-UI/
```

The easiest way to get there is the public
[NimoOS-Build](https://github.com/NimoTech/NimoOS-Build) repository, which
hosts `clone_all.sh` to clone every service repository as a sibling in one
step; see that repo's README for the up-to-date layout and the rest of its
build/install scripts. If you only need this repository plus `NimoOS-Common`
for a self-contained change, cloning those two by hand is enough — just keep
them siblings.

Before you build, make sure the sibling checkouts you depend on are actually
up to date. A `replace` directive resolves at build time, not at clone time,
so a stale sibling produces confusing "undefined" build errors that look
like they belong to this repo but don't.

### Cross-service features need multiple pull requests

If a feature or fix spans more than one service (say, a new event type that
`NimoOS` publishes and `NimoOS-Photos` consumes), you'll need a pull request
in each affected repository — there is no mechanism to land one PR across
repositories. Coordinate the merge order in the PR descriptions and link
them to each other.

One dependency matters more than the rest: several other services' own
`go generate` steps (this repo included) read
`../NimoOS-MessageBus/api/message_bus/openapi.yaml` directly — that file is
hand-authored and git-tracked in `NimoOS-MessageBus`, not something its own
`go generate` produces. There's no ordering requirement and no need to run
`NimoOS-MessageBus`'s own code generation first; you just need the
`NimoOS-MessageBus` checkout present as a sibling with an up-to-date
`api/message_bus/openapi.yaml`. If you're changing that OpenAPI spec itself
as part of a cross-service feature, land the `NimoOS-MessageBus` PR (or at
least get the spec change reviewed) before relying on it from another
service's generated client.

### Which repository should a bug or idea go to?

If you're not sure, open it here (`NimoOS`) — it'll get moved if it belongs
somewhere else. If you know the affected component, prefer its own
repository so the discussion stays next to the code.

## Building this repository

- **Toolchain:** the Go version and module versions (currently Go 1.23 with
  the `labstack/echo/v4` v4.12 line — see [`go.mod`](./go.mod) for the exact
  pins) are deliberately fixed. **Never run `go mod tidy`.** It will bump
  transitive dependencies beyond what's actually been tested against the
  rest of the NimoOS services and can silently break builds on other
  machines. If a dependency genuinely needs to change, do it as an explicit,
  reviewed `go get <module>@<version>` plus a note in the PR about why.
- **CGO:** this service links SQLite, so it needs `CGO_ENABLED=1` and a C
  toolchain (`gcc`) to build. The same is true for `NimoOS-AI` and
  `NimoOS-Wiki` (SQLite + go-systemd) and `NimoOS-Photos` (SQLite +
  sqlite-vec, which additionally needs the system `sqlite3.h` header). Every
  other service in the system builds as pure Go.
- **Code generation:** several services (this one included) run
  `go generate ./...` before `go build`/`go test` to produce OpenAPI server
  stubs and clients; the generated output is git-ignored. Check the
  repository's own developer docs for the exact command if `go generate
  ./...` isn't enough on its own.
- **Tests:** `go test ./...` from the repository root. Add or update tests
  for any behavioural change — a PR that changes logic without a
  corresponding test change will get asked for one.

## Frontend (NimoOS-UI)

`NimoOS-UI` is Vue 2.7, managed with pnpm:

```bash
pnpm i
pnpm test
pnpm lint
```

Run these before opening a pull request against `NimoOS-UI`.

## Developer Certificate of Origin (DCO)

NimoOS uses the [Developer Certificate of Origin](https://developercertificate.org/)
instead of a Contributor License Agreement. It's a lighter-weight mechanism:
instead of signing a separate legal document, you certify — once per commit,
via a `Signed-off-by` trailer — that you wrote the change or otherwise have
the right to submit it under the project's license. This is the standard
mechanism for Apache-2.0 projects that want that assurance without the
friction of a CLA.

Sign every commit with:

```bash
git commit -s
```

which appends a trailer like:

```
Signed-off-by: Your Name <your.email@example.com>
```

Use your real name and a working email address — anonymous or obviously
fake sign-offs aren't acceptable. If you forgot `-s` on a commit that's
already part of your branch, `git commit --amend -s` (or an interactive
rebase for older commits) fixes it up before you push.

## Opening a pull request

- Keep pull requests focused: one logical change per PR is easier to review
  and easier to revert if something goes wrong.
- Pull requests are merged with **squash merge**, so the individual commits
  on your branch don't need to be pretty, but the PR title and description
  do — write them as you'd want the squashed commit message to read.
- Commit messages and PR descriptions are in English.
- Include test evidence: what you ran, and what passed. If a change can't
  reasonably be tested (a doc fix, for example), say so instead of leaving
  the section blank.
- Fill out the pull request template — in particular, the DCO sign-off
  checkbox and the confirmation that you have not run `go mod tidy`.

## Security-sensitive changes

Don't open a public pull request or issue for a security vulnerability.
See [`SECURITY.md`](./SECURITY.md) for how to report one privately.

Two things worth knowing before you touch related code:

- **Multi-user isolation is incomplete.** Photos and Search are not yet
  scoped per user — see [SECURITY.md's Known limitations](./SECURITY.md#known-limitations).
  If your change touches either subsystem, don't describe or imply that this
  gap is closed; it isn't yet. The current state and the plan to close it
  are tracked in [`ROADMAP.md`](./ROADMAP.md).
- The AI agent's containment is behavioural (command gating, filesystem
  gating, an egress chokepoint, an audit log), not a hard sandbox — see
  [SECURITY.md's AI agent security model](./SECURITY.md#ai-agent-security-model)
  before changing anything in that path.

## Code of Conduct

Participation in this project is covered by our
[Code of Conduct](./CODE_OF_CONDUCT.md). Please read it.
