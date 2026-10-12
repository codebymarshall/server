"""Unit coverage for .github/scripts/check-image.py and files/prune/prune.sh."""

import importlib.util
import shutil
import subprocess
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / ".github" / "scripts" / "check-image.py"
PRUNE = ROOT / "files" / "prune" / "prune.sh"

_spec = importlib.util.spec_from_file_location("check_image", SCRIPT)
check_image = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(check_image)


def write(path: Path, text: str = "") -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    return path


def test_lookup_resolves_symlinks_inside_the_image(tmp_path):
    write(tmp_path / "usr/bin/real")
    (tmp_path / "usr/bin/abs").symlink_to("/usr/bin/real")
    (tmp_path / "usr/bin/rel").symlink_to("real")
    (tmp_path / "usr/bin/host").symlink_to("/etc/hostname")

    assert check_image.lookup([tmp_path], "/usr/bin/abs") == tmp_path / "usr/bin/real"
    assert check_image.lookup([tmp_path], "/usr/bin/rel") == tmp_path / "usr/bin/real"
    assert check_image.lookup([tmp_path], "/sbin/real") == tmp_path / "usr/bin/real"
    assert check_image.lookup([tmp_path], "/usr/bin/host") is None


def test_lookup_prefers_the_upper_layer(tmp_path):
    upper, lower = tmp_path / "sysext", tmp_path / "usr-root"
    write(lower / "usr/bin/tool", "lower")
    write(lower / "usr/bin/base-only")
    write(upper / "usr/bin/tool", "upper")

    assert check_image.lookup([upper, lower], "/usr/bin/tool").read_text() == "upper"
    assert check_image.lookup([upper, lower], "/usr/bin/base-only") is not None


@pytest.mark.parametrize(
    ("command", "program"),
    [
        ("/usr/bin/foo --flag", "/usr/bin/foo"),
        ("@/usr/bin/foo foo", "/usr/bin/foo"),
        ("+!bar", "bar"),
        ("-plymouth quit", None),
        ("@-/usr/bin/foo", None),
        ("${CMD} x", None),
        ("%h/bin/x", None),
        ("", None),
    ],
)
def test_first_program(command, program):
    assert check_image.first_program(command) == program


def test_check_programs_flags_missing_unit_and_udev_programs(tmp_path):
    write(tmp_path / "usr/bin/present")
    write(tmp_path / "usr/lib/udev/helper_id")
    write(
        tmp_path / "usr/lib/systemd/system/a.service",
        "[Service]\nExecStart=/usr/bin/present\nExecStop=/usr/bin/gone\nExecStartPre=-/usr/bin/optional\n",
    )
    write(
        tmp_path / "usr/lib/systemd/system/b.service",
        "[Unit]\nConditionPathExists=/usr/bin/guarded\n[Service]\nExecStart=/usr/bin/guarded\n",
    )
    write(
        tmp_path / "usr/lib/udev/rules.d/60-x.rules",
        'IMPORT{program}="helper_id x"\nRUN+="missing_id"\nRUN{builtin}+="kmod load"\n',
    )

    errors = check_image.check_programs("usr", [tmp_path], tmp_path)

    assert errors == [
        "usr: /usr/lib/systemd/system/a.service runs missing /usr/bin/gone",
        "usr: /usr/lib/udev/rules.d/60-x.rules runs missing missing_id",
    ]


@pytest.mark.skipif(shutil.which("readelf") is None, reason="needs binutils")
def test_check_libraries_requires_every_needed_soname(tmp_path):
    binary = tmp_path / "usr/bin/true"
    binary.parent.mkdir(parents=True)
    shutil.copy(shutil.which("true"), binary)

    errors = check_image.check_libraries("usr", [tmp_path], tmp_path)
    assert any("needs missing libc.so.6" in e for e in errors)

    needed = check_image.elf_needed([binary])[0][binary]
    for soname in needed:
        write(tmp_path / "usr/lib/x86_64-linux-gnu" / soname)
    assert check_image.check_libraries("usr", [tmp_path], tmp_path) == []


def test_waivers_are_arch_neutral_and_shrink_only(monkeypatch):
    monkeypatch.setattr(
        check_image,
        "KNOWN_BROKEN",
        {"/usr/lib/*-linux-gnu/x.so needs missing liby.so.1", "/usr/bin/fixed runs missing z"},
    )
    errors = [
        "usr: /usr/lib/aarch64-linux-gnu/x.so needs missing liby.so.1",
        "initrd: /usr/lib/x86_64-linux-gnu/x.so needs missing liby.so.1",
        "usr: /usr/bin/new runs missing q",
    ]

    assert check_image.apply_waivers(errors) == [
        "usr: /usr/bin/new runs missing q",
        "stale KNOWN_BROKEN entry, delete it: /usr/bin/fixed runs missing z",
    ]


def test_waivers_ignore_library_version_numbers(monkeypatch):
    monkeypatch.setattr(
        check_image, "KNOWN_BROKEN", {"/usr/lib/*-linux-gnu/libv.so.* needs missing libz.so.1"}
    )
    errors = ["nv: /usr/lib/x86_64-linux-gnu/libv.so.595.104.02 needs missing libz.so.1"]

    assert check_image.apply_waivers(errors) == []


def test_diff_report_lists_removed_and_added_paths():
    report = check_image.diff_report({"/bin", "/bin/a", "/bin/b"}, {"/bin", "/bin/b", "/bin/c"})

    assert "1 removed, 1 added" in report
    assert "/bin/a" in report.split("Removed")[1].split("</details>")[0]
    assert "/bin/c" in report.split("Added")[1]


def run_prune(root: Path, *lists: Path):
    return subprocess.run(
        ["sh", str(PRUNE), str(root), *map(str, lists)], capture_output=True, text=True
    )


def test_prune_removes_globs_and_ignores_comments(tmp_path):
    root = tmp_path / "root"
    write(root / "usr/lib/x86_64-linux-gnu/libicuuc.so.78")
    write(root / "usr/lib/x86_64-linux-gnu/libicudata.so.78.3")
    write(root / "usr/lib/x86_64-linux-gnu/libc.so.6")
    write(root / "usr/share/icu/78.3/data")
    lst = write(
        tmp_path / "a.list",
        "# comment\n\nusr/lib/*-linux-gnu/libicu*.so*   # trailing\nusr/share/icu\n",
    )

    result = run_prune(root, lst)

    assert result.returncode == 0, result.stderr
    assert sorted(p.name for p in (root / "usr/lib/x86_64-linux-gnu").iterdir()) == ["libc.so.6"]
    assert not (root / "usr/share/icu").exists()


def test_prune_fails_on_a_stale_pattern(tmp_path):
    root = tmp_path / "root"
    write(root / "usr/bin/keep")
    lst = write(tmp_path / "a.list", "usr/bin/gone\n")

    result = run_prune(root, lst)

    assert result.returncode != 0
    assert "matches nothing: usr/bin/gone" in result.stderr
    assert (root / "usr/bin/keep").exists()


@pytest.mark.parametrize("name", ["common.list", "usr.list", "initrd.list"])
def test_prune_lists_never_touch_load_bearing_paths(name):
    patterns = [
        line.split("#", 1)[0].strip()
        for line in (PRUNE.parent / name).read_text().splitlines()
    ]
    patterns = [p for p in patterns if p]
    assert patterns
    for pattern in patterns:
        assert pattern.startswith("usr/"), pattern
        assert ".." not in pattern, pattern
        for protected in ("usr/bin/gpg", "usr/bin/gpgv", "usr/lib/locale", "usr/lib/systemd"):
            assert not Path(protected).match(pattern), (pattern, protected)


def test_installer_image_rejects_oversized_sparse_file_before_extraction(tmp_path):
    image = tmp_path / "bluefin-server-installer_26.10.1.raw"
    with image.open("wb") as handle:
        handle.truncate(4 * 2**30)
    with pytest.raises(SystemExit, match="installer image.*4 GiB"):
        check_image.main([str(tmp_path)])
