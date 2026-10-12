"""Behavioral input-validation regressions, before any host mutation."""
import os
import subprocess
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[2] / "files/runtimes/bluefin-oci-owner-prepare"


def test_rootless_choices_reject_unknown_values_before_mutation(tmp_path):
    credentials = tmp_path / "credentials"
    credentials.mkdir()
    (credentials / "bluefin.homebrew.docker").write_text("yes\n")
    result = subprocess.run(
        ["bash", SCRIPT, "--print-selection"],
        env=os.environ | {
            "CREDENTIALS_DIRECTORY": str(credentials),
            "BLUEFIN_OCI_STATE_FILE": str(tmp_path / "state"),
        },
        text=True,
        capture_output=True,
    )
    assert result.returncode != 0
    assert "true or false" in result.stderr


def test_rootless_choices_are_opt_in_and_explicit_false_is_preserved(tmp_path):
    credentials = tmp_path / "credentials"
    credentials.mkdir()
    (credentials / "bluefin.homebrew.podman").write_text("false\n")
    result = subprocess.run(
        ["bash", SCRIPT, "--print-selection"],
        env=os.environ | {
            "CREDENTIALS_DIRECTORY": str(credentials),
            "BLUEFIN_OCI_STATE_FILE": str(tmp_path / "state"),
        },
        text=True,
        capture_output=True,
        check=True,
    )
    assert result.stdout == "false:false\n"


def test_rootless_choices_require_separate_explicit_engine_selection(tmp_path):
    credentials = tmp_path / "credentials"
    credentials.mkdir()
    (credentials / "bluefin.homebrew.docker").write_text("true\n")
    result = subprocess.run(
        ["bash", SCRIPT, "--print-selection"],
        env=os.environ | {
            "CREDENTIALS_DIRECTORY": str(credentials),
            "BLUEFIN_OCI_STATE_FILE": str(tmp_path / "state"),
        },
        text=True,
        capture_output=True,
        check=True,
    )
    assert result.stdout == "false:true\n"
