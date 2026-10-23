---
name: system-containers
description: Add or document system containers that should behave like first-class systemd machines rather than podman/OCI workloads. Use when managing container lifecycle with machinectl and systemd-run.
metadata:
  type: how-to
  status: stable
  last_updated: "2026-10-22"
  context7-sources:
    - /systemd/systemd
---
# system containers

Use this skill when adding or documenting system containers that should behave
like first-class systemd machines rather than podman/OCI workloads.

A system container is a plain rootfs image imported with
`machinectl import-tar`. It is managed with `machinectl` and the thin
`/usr/bin/system-container` helper shipped in the OS image (backed by
`machinectl start`/`poweroff`/`shell`/`remove`).

The host experience should be a transparent machine experience, not a container
runtime UI.

## When to Use

- Shipping a development or debugging toolbox as a `systemd-nspawn` rootfs.
- Documenting how operators import, start, enter, and reset a machine image.
- Changing `files/bin/system-container` or the default toolbox justfile in
  `files/os/justfile`.

## When NOT to Use

- Adding a distroless OCI runtime image — the server repo does not build those.
- Kubernetes workload or cluster questions — use `k0s-sysext.md`.

## Lifecycle

The OS image ships a small helper at `/usr/bin/system-container`:

```bash
system-container start <name>
system-container stop  <name>    # machinectl poweroff
system-container enter <name>    # machinectl shell
system-container reset <name>    # machinectl remove
```

Under the hood these map directly to `machinectl`.

## First-time setup

Replace `<image-url>` with the published `.tar.zst` rootfs URL for the toolbox.

```bash
sudo machinectl import-tar <image-url> homebrew
sudo system-container start homebrew
sudo system-container enter homebrew
```

When done inside the container:

```bash
exit
sudo system-container stop homebrew
```

## Reset a container

Resetting removes the machine and starts over from a fresh import:

```bash
sudo system-container reset homebrew
sudo machinectl import-tar <image-url> homebrew
sudo system-container start homebrew
```

## Inspecting machines on a host

```bash
machinectl list
machinectl list-images
```

## Homebrew conventions

If the toolbox is Homebrew-based, use `/home/linuxbrew` as the writable prefix.
Set `HOMEBREW_NO_AUTO_UPDATE=1` and `HOMEBREW_NO_INSTALL_CLEANUP=1` in its
environment so updates are explicit and the environment behaves like a standard
Linux Homebrew installation.

## Transparent Homebrew profile

The optional `homebrew` sysupdate feature delivers two signed, host-version-locked
assets: `homebrew_<ver>.raw.zst` in `/var/lib/machines` and
`homebrew-integration_<ver>.raw.zst` in `/var/lib/extensions`. The latter contains
the helper, restricted all-users sudo rule, lifecycle units, shell/SSH hooks and
Python closure. None of that bridge ships in the base OS. Ship this consumer only
with a merged producer main SHA; the producer owns its independent FSDK machine.

Apply the published, self-contained `home-server.bu` through the existing
Ignition provisioning flow. Its canonical selector runs before signed feature
fetching; a profile-only gate starts the newly merged Homebrew service before
console/SSH login. Explicit engine credentials, persisted selections and effective
sysupdate drop-ins preserve operator policy and data. Podman defaults on only in
this selected profile; Docker and the cluster stay off. Runtime activation and
private endpoints are documented in [systemd-sysext-extensions.md](systemd-sysext-extensions.md).
Provision a nonroot owner and private account/key credentials for headless use;
otherwise owner setup uses the console before getty. See
[tpm2-credential-sealing.md](tpm2-credential-sealing.md) for credential transport.

Installed packages persist at `/var/lib/bluefin-homebrew/prefix`, bound into the
machine at `/home/linuxbrew/.linuxbrew`. The root-owned owner record prevents a
silent UID/GID change. Only the owner runs `brew`; other provisioned users execute
installed tools with a read-only prefix. User scopes keep detached tmux sessions
alive after SSH disconnects, without nesting host engine workloads in the machine.
After login readiness, `bluefin-homebrew-default-formulae.service` (in
`home-server.bu` only) runs `brew install tmux neovim` as the owner through the
same dispatch. Login never waits for it; a failed run (no network) retries next
boot. The `/var/lib/bluefin-homebrew/default-formulae` stamp keeps it from running
again, so formulae the owner removes stay removed.

Dispatch preserves sudo's authenticated active primary GID, NSS supplementary
groups and inherited descriptors. Shared cwd identity is pinned and compared in
host and guest, then reopened under caller permissions before execution; `/`
deliberately maps to the guest root. Environment/package argv travel in a sealed,
caller-owned request, never sudo argv. Native allowed/denied sudo audit events
remain enabled, and user scopes have a fixed description. Foreground PTYs relay
input; background jobs retain their streams. Application output and independent
system auditing remain outside this argv privacy guarantee.

## Interacting with a machine service

To run a command inside a running machine:

```bash
systemd-run --machine=homebrew --pty /usr/bin/env
```

## Verification

- [ ] `files/bin/system-container` exists and maps to valid `machinectl` commands.
- [ ] The rootfs image format is documented (`.tar.zst` for `import-tar`).
- [ ] Toolbox images are not confused with OCI images or sysexts in docs.

## See also

- [CONTEXT.md](../../CONTEXT.md) — canonical project domain glossary.
- `machinectl(1)`, `systemd-nspawn(1)`
