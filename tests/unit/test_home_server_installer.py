"""Native Home Server installer arguments, private credentials and boot profiles."""

from __future__ import annotations

import os
import shlex
import stat
import subprocess
from pathlib import Path

import pytest
import yaml

from _systemd import SystemdFile

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "files/os/libexec/bluefin-home-server-install"
UNIT = ROOT / "files/os/creds/systemd/system/bluefin-home-server-install.service"
BOOT = ROOT / "elements/oci/bluefin-server-boot.bst"
TEMPLATE = ROOT / "files/homebrew/templates/home-server.bu"


class Installer:
    def __init__(self, tmp: Path) -> None:
        self.creds = tmp / "creds"
        self.out = tmp / "run"
        self.stick = tmp / "stick"
        self.creds.mkdir()
        self.out.mkdir()
        (self.stick / "repart.d/10-esp.conf.d").mkdir(parents=True)
        (self.stick / "home-server.bu").write_text("variant: fcos\n", encoding="utf-8")
        (self.stick / "home-server-cluster.bu").write_text("variant: fcos\n", encoding="utf-8")
        self.credential("bluefin.install-home-server", "1")

    def credential(self, name: str, value: str | bytes) -> None:
        data = value.encode() if isinstance(value, str) else value
        (self.creds / name).write_bytes(data)

    def run(self, stdin: str = "\n\n\n\n") -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["bash", str(SCRIPT)],
            env={
                **os.environ,
                "CREDENTIALS_DIRECTORY": str(self.creds),
                "RUNTIME_DIRECTORY": str(self.out),
                "BLUEFIN_INSTALLER_HOME_SERVER": str(self.stick),
                "BLUEFIN_INSTALL_ARGS": "do-not-overwrite",
            },
            input=stdin, capture_output=True, text=True, check=False,
        )

    def args(self) -> list[str]:
        text = (self.out / "sysinstall.env").read_text(encoding="utf-8")
        assert text.startswith("BLUEFIN_HOME_SERVER_INSTALL_ARGS=")
        assert text.count("\n") == 1
        return shlex.split(text.partition("=")[2])

    def value(self, name: str) -> str:
        return (self.out / name).read_text(encoding="utf-8")


@pytest.fixture
def installer(tmp_path: Path) -> Installer:
    return Installer(tmp_path)


def test_default_home_profile_prepares_rootless_backend_without_cluster(installer: Installer) -> None:
    result = installer.run()
    assert result.returncode == 0, result.stderr
    assert "Owner account" in result.stdout
    assert installer.args() == [
        f"--definitions={installer.stick}/repart.d",
        f"--load-credential=ignition.config:{installer.stick}/home-server.bu",
        *[f"--load-credential=bluefin.homebrew.{name}:{installer.out}/{name}"
          for name in ("profile", "podman", "docker", "cluster")],
    ]
    assert installer.value("profile") == "home-server"
    assert installer.value("podman") == "true"
    assert all(installer.value(name) == "false" for name in ("docker", "cluster"))
    assert not (installer.out / "owner").exists()
    assert "BLUEFIN_INSTALL_ARGS=" not in (installer.out / "sysinstall.env").read_text()


def test_interactive_owner_and_optional_choices_are_forwarded_privately(installer: Installer) -> None:
    result = installer.run("alice\nyes\nno\nyes\n")
    assert result.returncode == 0, result.stderr
    assert installer.value("owner") == "alice"
    assert installer.value("podman") == "true"
    assert installer.value("docker") == "false"
    assert installer.value("cluster") == "true"
    assert f"--load-credential=bluefin.homebrew.owner:{installer.out}/owner" in installer.args()
    for name in ("profile", "owner", "podman", "docker", "cluster", "sysinstall.env"):
        assert stat.S_IMODE((installer.out / name).stat().st_mode) == 0o600


@pytest.mark.parametrize("stdin", ["q\n", "alice\nq\n", "alice\n\nq\n", "alice\n\n\nq\n", ""])
def test_cancel_or_eof_never_emits_install_arguments(installer: Installer, stdin: str) -> None:
    assert installer.run(stdin).returncode != 0
    assert not (installer.out / "sysinstall.env").exists()


def test_invalid_prompt_answer_is_asked_again(installer: Installer) -> None:
    result = installer.run("root\nalice\nmaybe\nyes\n\n\n")
    assert result.returncode == 0, result.stderr
    assert installer.value("owner") == "alice"
    assert installer.value("podman") == "true"
    assert result.stdout.count("Owner account") == 2
    assert result.stdout.count("Enable Podman") == 2


def test_unattended_owner_and_account_credentials_do_not_prompt_or_leak(installer: Installer) -> None:
    installer.credential("systemd.unit-dropin.systemd-sysinstall.service~01", "[Service]\nStandardInput=null\n")
    installer.credential("bluefin.homebrew.owner", "alice")
    installer.credential("bluefin.homebrew.docker", "yes")
    installer.credential("sysusers.extra", 'u alice 1000 "Owner" /home/alice /bin/bash\n')
    installer.credential("passwd.hashed-password.alice", "$6$private-password-hash")
    installer.credential("tmpfiles.extra", "private-authorized-key-provisioning")
    result = installer.run("")
    assert result.returncode == 0, result.stderr
    assert "Owner account" not in result.stdout
    assert "Enable " not in result.stdout
    assert installer.value("docker") == "true"
    assert installer.value("podman") == "true"
    assert installer.value("cluster") == "false"
    assert "private" not in result.stdout + result.stderr


def test_unattended_install_without_owner_fails_instead_of_waiting(installer: Installer) -> None:
    installer.credential("systemd.unit-dropin.systemd-sysinstall.service", "[Service]\n")
    result = installer.run("")
    assert result.returncode != 0
    assert "owner" in result.stderr
    assert "Owner account" not in result.stdout
    assert not (installer.out / "sysinstall.env").exists()


def test_unattended_owner_without_private_password_field_fails(installer: Installer) -> None:
    installer.credential("systemd.unit-dropin.systemd-sysinstall.service", "[Service]\n")
    installer.credential("bluefin.homebrew.owner", "alice")
    result = installer.run("")
    assert result.returncode != 0
    assert "passwd.hashed-password.alice" in result.stderr
    assert not (installer.out / "sysinstall.env").exists()


@pytest.mark.parametrize(("name", "value"), [
    ("bluefin.install-home-server", "0"),
    ("bluefin.homebrew.owner", "root"),
    ("bluefin.homebrew.owner", "alice\n--erase=yes"),
    ("bluefin.homebrew.owner", b"alice\0root"),
    ("bluefin.homebrew.owner", "a" * 32),
    ("bluefin.homebrew.podman", "maybe"),
    ("bluefin.homebrew.docker", b"true\0false"),
    ("bluefin.homebrew.cluster", "true\nfalse"),
])
def test_invalid_credentials_fail_without_publishing_args(installer: Installer, name: str, value: str | bytes) -> None:
    installer.credential(name, value)
    assert installer.run().returncode != 0
    assert not (installer.out / "sysinstall.env").exists()


def test_missing_payload_fails_before_owner_prompt(installer: Installer) -> None:
    (installer.stick / "home-server.bu").unlink()
    result = installer.run()
    assert result.returncode != 0
    assert "Owner account" not in result.stdout
    assert not (installer.out / "sysinstall.env").exists()


def test_unit_is_installer_only_and_retains_runtime_files() -> None:
    unit = SystemdFile(UNIT)
    assert unit.value("Unit", "ConditionCredential") == "bluefin.install-home-server"
    assert "systemd-sysinstall.service" in unit.words("Unit", "Before")
    assert unit.value("Service", "RuntimeDirectory") == "bluefin-home-server-install"
    assert unit.value("Service", "RuntimeDirectoryMode") == "0700"
    assert unit.value("Service", "RemainAfterExit") == "yes"
    assert unit.value("Service", "TTYPath") == "/dev/console"
    assert unit.value("Service", "ExecCondition") == "/usr/bin/systemctl is-active --quiet bluefin-installer-secure-boot.service"
    assert set(unit.values("Service", "ImportCredential")) >= {
        "bluefin.install-home-server", "bluefin.homebrew.*", "sysusers.extra", "tmpfiles.extra",
        "passwd.hashed-password.*", "ssh.authorized_keys.*", "systemd.unit-dropin.systemd-sysinstall.service*",
    }
    assert unit.values("Install", "WantedBy") == []
    disk = SystemdFile(ROOT / "files/os/creds/systemd/system/bluefin-installer-disk.service")
    assert "bluefin-home-server-install.service" in disk.words("Unit", "Requires")
    assert "bluefin-home-server-install.service" in disk.words("Unit", "After")


def test_home_template_bootstraps_brew_without_default_cluster_or_tools() -> None:
    template = yaml.safe_load(TEMPLATE.read_text(encoding="utf-8"))
    units = {unit["name"]: unit for unit in template["systemd"]["units"]}
    assert units["bluefin-root-password-prompt.service"]["mask"] is True
    assert units["bluefin-homebrew.service"]["enabled"] is True
    assert units["bluefin-sysext-fetch.service"]["enabled"] is True
    assert not template.get("passwd")
    files = template["storage"]["files"]
    assert len(files) == 1
    assert files[0]["path"] == "/etc/sysupdate.d/homebrew.feature.d/50-home-server.conf"
    assert files[0]["contents"]["inline"] == "[Feature]\nEnabled=true\n"
    assert files[0]["mode"] == 0o644
    assert not any("kube" in name or "tmux" in name for name in units)


def test_boot_profile_builder_keeps_home_first_and_advanced_entries(tmp_path: Path) -> None:
    commands = yaml.safe_load(BOOT.read_text(encoding="utf-8"))["config"]["commands"]
    command = commands[-1]
    start = command.index("# The USB installer's boot menu:")
    end = command.index("cp /efi-keys/", start)
    fragment = command[start:end].replace("/tmp/", f"{tmp_path}/")
    fragment = fragment.replace("/osrel/usr/lib/os-release", str(tmp_path / "os-release"))
    (tmp_path / "os-release").write_text('ID=bluefin-server\nPRETTY_NAME="Bluefin Server 1"\n', encoding="utf-8")
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    # ukify is the external signing/build boundary: retain and inspect our
    # generated profile sections and signed UKI invocation, not fake PE bytes.
    (bin_dir / "ukify").write_text("#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\n", encoding="utf-8")
    (bin_dir / "ukify").chmod(0o755)
    calls = tmp_path / "calls"
    result = subprocess.run(
        ["bash", "-eu", "-c", 'usrhash=abc; uki() { ukify build "$@"; };\n' + fragment],
        env={**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "CALLS": str(calls)},
        capture_output=True, text=True, check=False,
    )
    assert result.returncode == 0, result.stderr
    home_osrel = (tmp_path / "home-server.osrel").read_text(encoding="utf-8")
    assert 'PRETTY_NAME="Bluefin Home Server"' in home_osrel
    assert (tmp_path / "home-server.profile").read_text().strip() == "ID=home-server"
    assert "bluefin.install-home-server:1" in calls.read_text().splitlines()[-1]
    assert "bluefin.install-home-server" not in (tmp_path / "minimal.cmdline").read_text()
    assert "bluefin.install-home-server" not in (tmp_path / "homelab-node.cmdline").read_text()
    final = calls.read_text().splitlines()[-1]
    joined = [arg for arg in shlex.split(final) if arg.startswith("--join-profile=")]
    assert joined == [f"--join-profile={tmp_path}/{name}.efi" for name in ("minimal", "homelab-control-plane", "homelab-node")]
