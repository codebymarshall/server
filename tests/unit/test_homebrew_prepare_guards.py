"""Guards in files/homebrew/bluefin-homebrew-prepare that run before any mount.

bluefin-homebrew-prepare runs as root. Before it mounts the Homebrew machine
image it checks that its own runtime paths are root-owned and not writable by
anyone else (trusted), reads /etc/bluefin-homebrew.conf (configuration),
picks which host account owns the Homebrew prefix (login_users,
select_owner, credential) and refuses to run unprivileged (main).
tests/unit/test_homebrew_dispatch.py covers seeding, the command index and
interactive owner creation; these tests cover the rest of that path.
"""

import importlib.machinery
import importlib.util
import os
import pwd
import stat
import subprocess
import sys
from pathlib import Path
from unittest.mock import patch

import pytest

SOURCE = Path(__file__).resolve().parents[2] / "files/homebrew/bluefin-homebrew-prepare"
_loader = importlib.machinery.SourceFileLoader("homebrew_prepare_guards", str(SOURCE))
_spec = importlib.util.spec_from_loader(_loader.name, _loader)
assert _spec is not None
prepare = importlib.util.module_from_spec(_spec)
_loader.exec_module(prepare)


def account(name, uid, shell="/bin/bash"):
    return pwd.struct_passwd((name, "x", uid, uid, name, f"/var/home/{name}", shell))


# --- trusted ---------------------------------------------------------------


def fake_lstat(overrides):
    """Path.lstat that reports root:root 0755 directories unless overridden."""

    def lstat(self):
        mode, uid = overrides.get(str(self), (stat.S_IFDIR | 0o755, 0))
        return os.stat_result((mode, 0, 0, 1, uid, 0, 0, 0, 0, 0))

    return lstat


def test_trusted_accepts_a_root_owned_unwritable_chain():
    with patch.object(prepare.Path, "lstat", fake_lstat({})):
        assert prepare.trusted("/run/bluefin-homebrew") == Path("/run/bluefin-homebrew")


@pytest.mark.parametrize(
    ("bad", "mode", "uid"),
    [
        ("/run/bluefin-homebrew", stat.S_IFLNK | 0o755, 0),
        ("/run/bluefin-homebrew", stat.S_IFDIR | 0o755, 1000),
        ("/run/bluefin-homebrew", stat.S_IFDIR | 0o775, 0),
        ("/run/bluefin-homebrew", stat.S_IFDIR | 0o757, 0),
        ("/run", stat.S_IFLNK | 0o755, 0),
        ("/run", stat.S_IFDIR | 0o1777, 0),
        ("/", stat.S_IFDIR | 0o755, 1000),
    ],
    ids=["leaf-symlink", "leaf-not-root", "leaf-group-writable", "leaf-world-writable",
         "parent-symlink", "parent-world-writable", "root-not-root"],
)
def test_trusted_rejects_any_symlinked_foreign_or_writable_component(bad, mode, uid):
    with patch.object(prepare.Path, "lstat", fake_lstat({bad: (mode, uid)})), \
            pytest.raises(RuntimeError, match=f"Untrusted runtime path: {bad}$"):
        prepare.trusted("/run/bluefin-homebrew")


def test_trusted_rejects_a_real_path_owned_by_the_test_user(tmp_path):
    if os.geteuid() == 0:
        pytest.skip("root owns tmp_path")
    with pytest.raises(RuntimeError, match="Untrusted runtime path"):
        prepare.trusted(tmp_path)


# --- configuration ---------------------------------------------------------


def read_configuration(tmp_path, text):
    conf = tmp_path / "bluefin-homebrew.conf"
    if text is not None:
        conf.write_text(text)

    def redirect(value):
        return conf if value == "/etc/bluefin-homebrew.conf" else Path(value)

    with patch.object(prepare, "Path", side_effect=redirect), \
            patch.object(prepare, "trusted", side_effect=lambda path: path) as trusted:
        return prepare.configuration(), trusted, conf


def test_configuration_is_empty_without_a_file(tmp_path):
    values, trusted, _ = read_configuration(tmp_path, None)
    assert values == {}
    trusted.assert_not_called()


def test_configuration_reads_owner_and_project_roots_after_the_trust_check(tmp_path):
    values, trusted, conf = read_configuration(
        tmp_path,
        "# comment\n\n  OWNER=alice  \nPROJECT_ROOTS=/srv/a /srv/b\n",
    )
    assert values == {"OWNER": "alice", "PROJECT_ROOTS": "/srv/a /srv/b"}
    trusted.assert_called_once_with(conf)


def test_configuration_keeps_the_last_value_of_a_repeated_key(tmp_path):
    values, _, _ = read_configuration(tmp_path, "OWNER=alice\nOWNER=bob\n")
    assert values == {"OWNER": "bob"}


@pytest.mark.parametrize("line", ["OWNER", "SHELL=/bin/sh", "owner=alice", "Bind=/root:/root"])
def test_configuration_rejects_unknown_keys_and_lines_without_a_value(tmp_path, line):
    with pytest.raises(ValueError, match="Expected OWNER=account"):
        read_configuration(tmp_path, f"OWNER=alice\n{line}\n")


def test_configuration_is_not_read_when_the_trust_check_fails(tmp_path):
    conf = tmp_path / "bluefin-homebrew.conf"
    conf.write_text("OWNER=alice\n")

    def redirect(value):
        return conf if value == "/etc/bluefin-homebrew.conf" else Path(value)

    with patch.object(prepare, "Path", side_effect=redirect), \
            patch.object(prepare, "trusted", side_effect=RuntimeError("Untrusted runtime path")), \
            pytest.raises(RuntimeError, match="Untrusted"):
        prepare.configuration()


# --- login_users -----------------------------------------------------------


def test_login_users_keeps_interactive_regular_accounts_in_uid_order():
    accounts = [
        account("root", 0),
        account("systemd", 999),
        account("alice", 1001),
        account("bob", 1000, "/usr/bin/zsh"),
        account("nobody", 65534),
        account("edge", 65533),
        account("svc1", 1002, "/usr/sbin/nologin"),
        account("svc2", 1003, "/sbin/nologin"),
        account("svc3", 1004, "/bin/false"),
        account("svc4", 1005, "/usr/bin/false"),
    ]
    with patch.object(prepare.pwd, "getpwall", return_value=accounts):
        assert [user.pw_name for user in prepare.login_users()] == ["bob", "alice", "edge"]


# --- credential ------------------------------------------------------------


def test_credential_reads_the_service_credentials_directory_and_strips(tmp_path, monkeypatch):
    (tmp_path / "bluefin.test.credential").write_text("  value\n")
    monkeypatch.setenv("CREDENTIALS_DIRECTORY", str(tmp_path))
    assert prepare.credential("bluefin.test.credential") == "value"


def test_credential_is_empty_when_no_directory_has_it(tmp_path, monkeypatch):
    monkeypatch.setenv("CREDENTIALS_DIRECTORY", str(tmp_path))
    assert prepare.credential("bluefin.test.absent-credential-for-unit-test") == ""


# --- select_owner ----------------------------------------------------------


class Host:
    """Stand-in for the account database and the commands select_owner runs."""

    def __init__(self, accounts, credentials=None):
        self.accounts = {user.pw_name: user for user in accounts}
        self.credentials = credentials or {}
        self.commands = []

    def getpwnam(self, name):
        return self.accounts[name]

    def getpwuid(self, uid):
        return next(user for user in self.accounts.values() if user.pw_uid == uid)

    def credential(self, name):
        return self.credentials.get(name, "")

    def run(self, *args, **kwargs):
        self.commands.append((args, kwargs))
        if args[0] == "systemd-ask-password":
            raise AssertionError(f"unexpected console prompt: {args}")
        if args[0] == "useradd":
            name = args[-1]
            self.accounts[name] = account(name, 1100)
        return subprocess.CompletedProcess(args, 0, stdout="")


def select(host, state, config, users=()):
    with patch.object(prepare, "STATE", state), \
            patch.object(prepare, "credential", side_effect=host.credential), \
            patch.object(prepare, "login_users", return_value=list(users)), \
            patch.object(prepare.pwd, "getpwnam", side_effect=host.getpwnam), \
            patch.object(prepare.pwd, "getpwuid", side_effect=host.getpwuid), \
            patch.object(prepare, "run", side_effect=host.run), \
            patch.object(prepare, "trusted"), patch.object(prepare.Path, "mkdir"), \
            patch.object(prepare, "provision_owner_keys") as provision:
        owner = prepare.select_owner(config)
    return owner, provision


def test_owner_credential_wins_over_configuration_and_discovery(tmp_path):
    alice, carol = account("alice", 1000), account("carol", 1001)
    host = Host([alice, carol], {"bluefin.homebrew.owner": "carol"})
    (tmp_path / "owner").write_text("1000:1000\n")
    owner, provision = select(host, tmp_path, {"OWNER": "alice"}, [alice, carol])
    assert owner.pw_name == "carol"
    provision.assert_called_once_with(carol)
    assert host.commands == []


def test_configured_owner_wins_over_the_recorded_owner(tmp_path):
    alice, bob = account("alice", 1000), account("bob", 1001)
    (tmp_path / "owner").write_text("1000:1000\n")
    owner, _ = select(Host([alice, bob]), tmp_path, {"OWNER": "bob"}, [alice, bob])
    assert owner.pw_name == "bob"


def test_recorded_owner_wins_over_the_first_login_user(tmp_path):
    alice, dave = account("alice", 1000), account("dave", 1005)
    (tmp_path / "owner").write_text("1005:1005\n")
    owner, _ = select(Host([alice, dave]), tmp_path, {}, [alice, dave])
    assert owner.pw_name == "dave"


def test_first_login_user_is_the_owner_when_nothing_names_one(tmp_path):
    alice, bob = account("alice", 1000), account("bob", 1001)
    host = Host([alice, bob])
    owner, _ = select(host, tmp_path, {}, [alice, bob])
    assert owner.pw_name == "alice"
    assert host.commands == []


@pytest.mark.parametrize("name", ["root", "Alice", "1alice", "alice smith", "a" * 32, "alice\n"])
def test_invalid_or_root_owner_names_are_rejected_before_any_account_change(tmp_path, name):
    host = Host([])
    with pytest.raises(ValueError, match="valid non-root account name"):
        select(host, tmp_path, {"OWNER": name})
    assert host.commands == []


@pytest.mark.parametrize("uid", [0, 999, 65534])
def test_owner_outside_the_regular_uid_range_is_rejected_before_key_provisioning(tmp_path, uid):
    host = Host([account("svc", uid)])
    with patch.object(prepare, "provision_owner_keys") as provision, \
            pytest.raises(ValueError, match="UID must be between 1000 and 65533"):
        with patch.object(prepare, "STATE", tmp_path), \
                patch.object(prepare, "credential", side_effect=host.credential), \
                patch.object(prepare, "login_users", return_value=[]), \
                patch.object(prepare.pwd, "getpwnam", side_effect=host.getpwnam), \
                patch.object(prepare, "run", side_effect=host.run):
            prepare.select_owner({"OWNER": "svc"})
    provision.assert_not_called()


def test_new_owner_with_a_hashed_password_credential_is_created_without_prompting(tmp_path):
    host = Host([], {"passwd.hashed-password.eve": "$6$salt$hash"})
    owner, provision = select(host, tmp_path, {"OWNER": "eve"})
    assert owner.pw_name == "eve"
    assert host.commands[0][0] == (
        "useradd", "--create-home", "--home-dir", "/var/home/eve", "--shell", "/bin/bash", "eve")
    args, kwargs = host.commands[1]
    assert args == ("chpasswd", "--encrypted")
    assert kwargs["input"] == "eve:$6$salt$hash\n"
    provision.assert_called_once()


# --- main ------------------------------------------------------------------


def run_main(tmp_path, argv, uid=0):
    state = tmp_path / "state"
    calls = {}
    with patch.object(prepare, "STATE", state), \
            patch.object(sys, "argv", ["bluefin-homebrew-prepare", *argv]), \
            patch.object(prepare.os, "getuid", return_value=uid), \
            patch.object(prepare, "trusted", side_effect=lambda path: path), \
            patch.object(prepare, "configuration", return_value={"OWNER": "alice"}), \
            patch.object(prepare, "select_owner", return_value=account("alice", 1000)) as select_owner, \
            patch.object(prepare, "record_owner") as record_owner, \
            patch.object(prepare, "refresh_commands") as refresh, \
            patch.object(prepare, "prepare_machine") as machine:
        prepare.main()
        calls.update(select_owner=select_owner, record_owner=record_owner,
                     refresh=refresh, machine=machine)
    return state, calls


def test_main_refuses_to_run_unprivileged_before_touching_state(tmp_path, capsys):
    with pytest.raises(SystemExit) as exit_info:
        run_main(tmp_path, [], uid=1000)
    assert exit_info.value.code == 2
    assert "runtime preparation is root-only" in capsys.readouterr().err
    assert not (tmp_path / "state").exists()


def test_main_refresh_only_rebuilds_the_command_index(tmp_path):
    state, calls = run_main(tmp_path, ["--refresh"])
    calls["refresh"].assert_called_once_with(
        state / "prefix", state / "commands", "/usr/libexec/bluefin-homebrew-command")
    calls["machine"].assert_not_called()
    calls["select_owner"].assert_not_called()


def test_main_owner_only_records_the_owner_without_preparing_the_machine(tmp_path):
    state, calls = run_main(tmp_path, ["--owner-only"])
    calls["select_owner"].assert_called_once_with({"OWNER": "alice"})
    calls["record_owner"].assert_called_once_with(state, 1000, 1000)
    calls["machine"].assert_not_called()
    calls["refresh"].assert_not_called()


def test_main_without_flags_prepares_the_machine_under_the_state_lock(tmp_path):
    state, calls = run_main(tmp_path, [])
    calls["machine"].assert_called_once_with()
    assert (state / "commands").is_dir()
    assert (state / "lock").exists()
