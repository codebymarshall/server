"""Home defaults and cluster access use the published templates and real helpers."""
import os
import shutil
import subprocess
from pathlib import Path

import pytest
import yaml

ROOT = Path(__file__).resolve().parents[2]
TEMPLATE = ROOT / "files/homebrew/templates/home-server.bu"
RUNTIME = ROOT / "files/runtimes/bluefin-oci-owner-prepare"


def template_credentials(tmp_path: Path) -> Path:
    """Use the default delivered by each real unit's template drop-in."""
    home = yaml.safe_load(TEMPLATE.read_text())
    units = {unit["name"]: unit for unit in home["systemd"]["units"]}
    defaults = []
    for name in ("bluefin-oci-features.service", "bluefin-oci-owner-prepare.service"):
        settings = [line.removeprefix("SetCredential=")
                    for dropin in units[name]["dropins"]
                    for line in dropin["contents"].splitlines()
                    if line.startswith("SetCredential=")]
        assert settings == ["bluefin.homebrew.profile:home-server"]
        defaults.append(settings)
    assert defaults[0] == defaults[1]
    credentials = tmp_path / "credentials"
    credentials.mkdir()
    for setting in defaults[0]:
        name, value = setting.split(":", 1)
        (credentials / name).write_text(value)
    return credentials


def runtime(tmp_path: Path, credentials: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", RUNTIME, *args],
        env=os.environ | {
            "CREDENTIALS_DIRECTORY": str(credentials),
            "BLUEFIN_OCI_STATE_FILE": str(tmp_path / "runtimes.state"),
            "BLUEFIN_SYSUPDATE_ROOT": str(tmp_path / "host"),
        },
        text=True, capture_output=True, check=False,
    )


def test_template_only_home_enables_podman_without_usb_profile_credential(tmp_path: Path) -> None:
    result = runtime(tmp_path, template_credentials(tmp_path), "--print-selection")
    assert result.returncode == 0, result.stderr
    assert result.stdout == "true:false\n"


def test_explicit_podman_disable_wins_template_default(tmp_path: Path) -> None:
    credentials = template_credentials(tmp_path)
    (credentials / "bluefin.homebrew.podman").write_text("false")
    result = runtime(tmp_path, credentials, "--print-selection")
    assert result.returncode == 0, result.stderr
    assert result.stdout == "false:false\n"


@pytest.mark.skipif(os.geteuid() != 0, reason="selection state requires root ownership")
def test_persisted_operator_disable_wins_template_default(tmp_path: Path) -> None:
    credentials = template_credentials(tmp_path)
    (tmp_path / "runtimes.state").write_text("false:false\n")
    result = runtime(tmp_path, credentials, "--print-selection")
    assert result.returncode == 0, result.stderr
    assert result.stdout == "false:false\n"


@pytest.mark.skipif(os.geteuid() != 0 or not shutil.which("systemd-analyze"),
                    reason="requires root ownership and real systemd configuration merging")
@pytest.mark.parametrize("operator_name", ["90-operator.conf", "50-bluefin-home-server.conf"])
def test_operator_feature_disable_is_not_reenabled_or_recorded_as_running(
    tmp_path: Path, operator_name: str,
) -> None:
    credentials = template_credentials(tmp_path)
    dropins = tmp_path / "host/etc/sysupdate.d/podman.feature.d"
    dropins.mkdir(parents=True)
    operator = dropins / operator_name
    contents = "# Operator policy\n[Feature]\nEnabled=false\n"
    operator.write_text(contents)
    result = runtime(tmp_path, credentials, "--configure-features")
    assert result.returncode == 0, result.stderr
    assert operator.read_text() == contents
    assert (tmp_path / "runtimes.state").read_text() == "false:false\n"
    assert not (tmp_path / "host/etc/sysupdate.d/docker.feature.d/90-operator.conf").exists()


