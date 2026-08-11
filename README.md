# NimoOS - Your Personal Cloud

> ### About
>
> NimoOS is a fork of [CasaOS](https://github.com/IceWhaleTech/CasaOS)
> (Apache-2.0), originally developed by IceWhale Technology Co., Ltd.
> Building on that foundation, NimoOS adds an AI agent, RAG-based
> retrieval, a knowledge layer, and a built-in web terminal.
>
> See [`NOTICE`](./NOTICE) for attribution details. CasaOS and IceWhale
> are trademarks of IceWhale Technology Co., Ltd. NimoOS is an independent
> project and is not affiliated with, endorsed by, or sponsored by
> IceWhale Technology Co., Ltd.

> ⚠️ Multi-user isolation is incomplete — Photos and Search are not yet
> per-user scoped. Read [SECURITY.md](./SECURITY.md#known-limitations)
> before deploying NimoOS for more than one person.

<p align="center">
    <!-- NimoOS Badges -->
    <a href="https://github.com/NimoTech/NimoOS" target="_blank">
        <img alt="NimoOS Version" src="https://img.shields.io/github/v/release/NimoTech/NimoOS?color=162453&style=flat-square&label=NimoOS" />
    </a>
    <a href="https://github.com/NimoTech/NimoOS/blob/main/LICENSE" target="_blank">
        <img alt="NimoOS License" src="https://img.shields.io/github/license/NimoTech/NimoOS?color=162453&style=flat-square&label=License" />
    </a>
    <a href="https://github.com/NimoTech/NimoOS/pulls" target="_blank">
        <img alt="NimoOS Pull Requests" src="https://img.shields.io/github/issues-pr/NimoTech/NimoOS?color=162453&style=flat-square&label=PRs" />
    </a>
    <a href="https://github.com/NimoTech/NimoOS/issues" target="_blank">
        <img alt="NimoOS Issues" src="https://img.shields.io/github/issues/NimoTech/NimoOS?color=162453&style=flat-square&label=Issues" />
    </a>
    <a href="https://github.com/NimoTech/NimoOS/stargazers" target="_blank">
        <img alt="NimoOS Stargazers" src="https://img.shields.io/github/stars/NimoTech/NimoOS?color=162453&style=flat-square&label=Stars" />
    </a>
    <br/>
    <!-- NimoOS Community -->
    <a href="https://discord.com/invite/8NStGMweZh" target="_blank">
        <img alt="NimoOS Discord" src="https://img.shields.io/badge/Discord-162453?style=flat-square&logo=discord&logoColor=fff&label=Discord" />
    </a>
    <a href="https://www.reddit.com/r/Nimo" target="_blank">
        <img alt="NimoOS Reddit" src="https://img.shields.io/badge/r%2FNimo-162453?style=flat-square&logo=reddit&logoColor=fff&label=Reddit" />
    </a>
    <a href="https://x.com/Nimo_PC" target="_blank">
        <img alt="NimoOS on X" src="https://img.shields.io/badge/%40Nimo__PC-162453?style=flat-square&logo=x&logoColor=fff&label=X" />
    </a>
    <a href="https://github.com/NimoTech/NimoOS/discussions" target="_blank">
        <img alt="NimoOS GitHub Discussions" src="https://img.shields.io/github/discussions/NimoTech/NimoOS?color=162453&style=flat-square&label=Discussions&logo=github" />
    </a>
    <br/>
    <!-- NimoOS Links -->
    <a href="https://nimopc.com" target="_blank">Website</a> |
    <a href="https://github.com/NimoTech/NimoOS" target="_blank">GitHub</a>
</p>

## What is NimoOS

NimoOS turns a spare Linux box into a personal cloud server: file storage and
sharing, disk/RAID management, and one-click Docker app installs, plus an AI
agent with retrieval over your own files, a knowledge layer, and a built-in
web terminal (see the About block above). It runs as a set of Go
microservices behind a single gateway, with a web UI on top.

## Features

- File management — browse, upload, download, share; snapshots and a Time
  Machine view for recovery; Samba shares; cloud storage mounts via rclone
- Disk management, USB auto-mount, and RAID management
- One-click Docker Compose app installs from the built-in app store, plus
  third-party app stores that use the `x-casaos` compose extension
- AI agent — local and cloud inference routing, cross-session memory, an MCP
  client and server, and Telegram/Discord bridges
- Retrieval-augmented search — a document parsing/embedding pipeline with
  vector recall, aggregated with filename search
- Wiki knowledge layer — an auto-maintained navigation map alongside your
  files, plus manual and auto-distilled knowledge notes
- Local photo library with vector search and image captioning
- Built-in web terminal with persistent sessions and a step-up password
  prompt
- One-line installer, an offline dependency mirror, LAN device discovery,
  and a UI localised into 33 languages

See [`ROADMAP.md`](./ROADMAP.md) for the full, maintained capability list,
including what's still in progress or planned.

## Getting Started

### Hardware and OS support

- **Architecture: linux/amd64 only.** arm64 and armv7 binaries build and pass
  CI, but no arm release artifacts are published yet, so the installer stops
  with an explanation on those machines rather than half-installing.
- OS: Debian-family Linux (Debian, Ubuntu, Raspberry Pi OS)

### Quick Setup NimoOS

Install a system from the list above, then run:

```sh
curl -fsSL https://get.nimotech.ai/get/nimoos-install.sh | sudo bash
```

or, with wget:

```sh
wget -qO- https://get.nimotech.ai/get/nimoos-install.sh | sudo bash
```

### Update NimoOS

From the web UI: `Settings → Update`.

From a terminal — over SSH or on a directly attached keyboard, **not** from
the terminal built into the NimoOS UI, since updating restarts the services
that terminal runs through:

```sh
curl -fsSL https://get.nimotech.ai/get/nimoos-update.sh | sudo bash
```

To check which version is installed:

```sh
nimoos -v
```

### Uninstall NimoOS

```sh
nimoos-uninstall
```

## Community

NimoOS is a fork of [CasaOS](https://github.com/IceWhaleTech/CasaOS), originally
developed by IceWhale Technology Co., Ltd. NimoOS is an independent project and is
not affiliated with, endorsed by, or sponsored by IceWhale.

NimoOS extends that foundation with an AI agent, RAG-based retrieval, a knowledge
layer, and a built-in web terminal.

Bug reports and technical discussion belong in
[Issues](https://github.com/NimoTech/NimoOS/issues) and
[GitHub Discussions](https://github.com/NimoTech/NimoOS/discussions) — that keeps
them searchable and linkable to the code.

For everything else:

- [Discord](https://discord.com/invite/8NStGMweZh)
- [Reddit — r/Nimo](https://www.reddit.com/r/Nimo)
- [X — @Nimo_PC](https://x.com/Nimo_PC)
- [nimopc.com](https://nimopc.com)

## Contributing

Contributions are welcome.

- Open an issue or a pull request on <https://github.com/NimoTech/NimoOS>
- See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for how to build the project and what a good change looks like

## Credits

NimoOS is a fork of CasaOS — see [`NOTICE`](./NOTICE) for the upstream project
and its attribution. Everything NimoOS itself has changed is recorded in this
repository's git history.

## Changelog

Detailed changes for each release are documented in the [release notes](https://github.com/NimoTech/NimoOS/releases) and [`CHANGELOG.md`](./CHANGELOG.md).
