"""Profile defaults must never enable engines on unrelated base installations."""
import os
import subprocess
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[2] / "files/runtimes/bluefin-oci-features"


def selection(tmp_path, profile, override=None):
    credentials = tmp_path / "credentials"
    credentials.mkdir()
    (credentials / "bluefin.homebrew.profile").write_text(profile)
    if override is not None:
        (credentials / "bluefin.homebrew.podman").write_text(override)
    result = subprocess.run(
        ["bash", SCRIPT, "--print-selection"],
        env=os.environ | {
            "CREDENTIALS_DIRECTORY": str(credentials),
            "BLUEFIN_OCI_STATE_FILE": str(tmp_path / "state"),
        },
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout


def test_home_server_defaults_to_only_rootless_podman(tmp_path):
    assert selection(tmp_path, "home-server") == "true:false\n"


def test_non_home_profile_does_not_enable_any_engine(tmp_path):
    assert selection(tmp_path, "other") == "false:false\n"


def test_explicit_false_overrides_home_server_default(tmp_path):
    assert selection(tmp_path, "home-server", "false") == "false:false\n"
