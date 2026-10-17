"""Host unit cases and real-machine regressions; the parent runs these after staging.

Lab: BLUEFIN_HOMEBREW_TEST_USER=<owner> python3 -m unittest discover -s tests/unit -p test_homebrew_dispatch.py
Run as root on an isolated lab host with the real immutable machine installed.
"""
import importlib.machinery
import importlib.util
import os
import pwd
import select
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[2] / "files/homebrew/bluefin-homebrew-prepare"
loader = importlib.machinery.SourceFileLoader("homebrew_prepare", str(SOURCE))
spec = importlib.util.spec_from_loader(loader.name, loader)
prepare = importlib.util.module_from_spec(spec)
loader.exec_module(prepare)


class PreparationTests(unittest.TestCase):
    def test_seed_is_copied_once_without_overwriting_installed_packages(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = root / "seed"
            seed.mkdir()
            (seed / "brew").write_text("initial")
            prefix = root / "prefix"
            prepare.seed_prefix(seed, prefix, os.getuid(), os.getgid())
            (prefix / "package").write_text("user-installed")
            (seed / "brew").write_text("new image")
            prepare.seed_prefix(seed, prefix, os.getuid(), os.getgid())
            self.assertEqual((prefix / "brew").read_text(), "initial")
            self.assertEqual((prefix / "package").read_text(), "user-installed")

    def test_command_index_tracks_executables_removals_and_never_indexes_brew(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            prefix = root / "prefix"
            (prefix / "bin").mkdir(parents=True)
            (prefix / "sbin").mkdir()
            for name in ["tool", "brew", "not-executable"]:
                (prefix / "bin" / name).write_text("#!/bin/sh\n")
            (prefix / "bin/tool").chmod(0o755)
            (prefix / "bin/brew").chmod(0o755)
            (prefix / "sbin/service").symlink_to("../bin/tool")
            commands = root / "commands"
            prepare.refresh_commands(prefix, commands, "/usr/libexec/bluefin-homebrew-command")
            self.assertEqual(set(os.listdir(commands)), {"tool", "service"})
            self.assertEqual(os.readlink(commands / "tool"), "/usr/libexec/bluefin-homebrew-command")
            (prefix / "bin/tool").unlink()
            prepare.refresh_commands(prefix, commands, "/usr/libexec/bluefin-homebrew-command")
            self.assertEqual(os.listdir(commands), [])

    def test_owner_cannot_silently_change_after_seed(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            prepare.record_owner(state, 1000, 1000)
            prepare.record_owner(state, 1000, 1000)
            with self.assertRaises(RuntimeError):
                prepare.record_owner(state, 1001, 1001)
            self.assertEqual((state / "owner").read_text(), "1000:1000\n")

    def test_bad_console_name_and_password_confirmation_do_not_abort_owner_creation(self):
        answers = iter(["root", "alice", "first", "different", "valid", "valid"])
        account = pwd.struct_passwd(("alice", "x", 1000, 1000, "Owner", "/var/home/alice", "/bin/bash"))
        created = False

        def lookup(name):
            if created and name == "alice":
                return account
            raise KeyError(name)

        def execute(*args, **kwargs):
            nonlocal created
            if args[0] == "systemd-ask-password":
                return subprocess.CompletedProcess(args, 0, stdout=next(answers))
            if args[0] == "useradd":
                self.assertIsNone(next(answers, None), "Do not create an account before valid input and confirmation")
                created = True
            return subprocess.CompletedProcess(args, 0)

        with tempfile.TemporaryDirectory() as directory, \
                patch.object(prepare, "STATE", Path(directory)), \
                patch.object(prepare, "credential", return_value=""), \
                patch.object(prepare, "login_users", return_value=[]), \
                patch.object(prepare.pwd, "getpwnam", side_effect=lookup), \
                patch.object(prepare, "run", side_effect=execute), \
                patch.object(prepare, "trusted"), patch.object(prepare.Path, "mkdir"):
            owner = prepare.select_owner({})
        self.assertEqual(owner.pw_uid, 1000)
        self.assertEqual(lookup("alice").pw_dir, "/var/home/alice")

    def test_shared_path_serialization_rejects_mount_and_line_delimiters(self):
        for path in ["/srv/project:other", "/srv/project\nBind=/root:/root", "/srv/project\r", "+/root", "relative"]:
            with self.subTest(path=path), self.assertRaises(ValueError):
                prepare.nspawn_bind(path, path)

    def test_absolute_guest_prefix_symlinks_are_indexed_and_removed_when_dangling(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            prefix = root / "prefix"
            (prefix / "bin").mkdir(parents=True)
            executable = prefix / "Cellar/tool/1/bin/tool"
            executable.parent.mkdir(parents=True)
            executable.write_text("#!/bin/sh\n")
            executable.chmod(0o755)
            (prefix / "bin/tool name@").symlink_to("/home/linuxbrew/.linuxbrew/Cellar/tool/1/bin/tool")
            commands = root / "commands"
            prepare.refresh_commands(prefix, commands, "/usr/libexec/bluefin-homebrew-command")
            self.assertEqual(os.listdir(commands), ["tool name@"])
            executable.unlink()
            prepare.refresh_commands(prefix, commands, "/usr/libexec/bluefin-homebrew-command")
            self.assertEqual(os.listdir(commands), [])

    def test_owner_directory_links_never_publish_host_only_executables(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private = root / "private"
            private.mkdir()
            (private / "hidden-tool").write_text("#!/bin/sh\n")
            (private / "hidden-tool").chmod(0o755)
            prefix = root / "prefix"
            prefix.mkdir()
            (prefix / "bin").symlink_to(private)
            commands = root / "commands"
            prepare.refresh_commands(prefix, commands, "/usr/libexec/bluefin-homebrew-command")
            self.assertEqual(os.listdir(commands), [])
            (prefix / "bin").unlink()
            (prefix / "bin").mkdir()
            (prefix / "bin/tool").symlink_to(private / "hidden-tool")
            prepare.refresh_commands(prefix, commands, "/usr/libexec/bluefin-homebrew-command")
            self.assertEqual(os.listdir(commands), [])

    def test_guest_accounts_use_host_ids_without_forced_password_expiry(self):
        with tempfile.TemporaryDirectory() as directory:
            etc = Path(directory)
            (etc / "passwd").write_text("root:x:0:0:root:/root:/usr/bin/bash\n"
                                        "linuxbrew:x:1001:1001:Brew:/home/linuxbrew:/usr/bin/bash\n")
            (etc / "group").write_text("root:x:0:\nlinuxbrew:x:1001:\n")
            (etc / "shadow").write_text("root:!:20000:0:99999:7:::\nlinuxbrew:!:20000:0:99999:7:::\n")
            user = pwd.struct_passwd(("bluefin-test-account", "x", 1234, os.getgid(), "Owner", "/var/home/owner", "/bin/bash"))
            prepare.sync_accounts(etc, [user])
            self.assertIn(f"bluefin-test-account:x:1234:{os.getgid()}:Owner:/var/home/owner:/usr/bin/bash",
                          (etc / "passwd").read_text())
            self.assertNotIn("linuxbrew", (etc / "passwd").read_text())
            entry = next(line for line in (etc / "shadow").read_text().splitlines()
                         if line.startswith("bluefin-test-account:"))
            self.assertEqual(entry.split(":")[1], "!")
            self.assertGreater(int(entry.split(":")[2]), 0,
                               "A zero shadow last-change date forces password change and breaks systemd-user PAM")


@unittest.skipUnless(os.environ.get("BLUEFIN_HOMEBREW_TEST_USER"), "requires staged real lab machine")
class MachineTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.user = pwd.getpwnam(os.environ["BLUEFIN_HOMEBREW_TEST_USER"])
        cls.home = Path(cls.user.pw_dir)
        cls.work = Path(tempfile.mkdtemp(prefix="homebrew-regression-", dir=cls.home))
        os.chown(cls.work, cls.user.pw_uid, cls.user.pw_gid)
        cls.probe = Path("/var/lib/bluefin-homebrew/prefix/bin/bluefin-test-probe")
        cls.probe.write_text('#!/usr/bin/bash\n'
                             'printf "%s\\0" "$(id -u)" "$(id -g)" "$PWD" "$@"\n'
                             'printf "stderr-marker" >&2\n'
                             'grep -E "^(CapEff|NoNewPrivs):" /proc/self/status >&2\n'
                             'cat /proc/self/cgroup >&2\n'
                             'cat\n')
        cls.probe.chmod(0o755)
        os.chown(cls.probe, cls.user.pw_uid, cls.user.pw_gid)
        subprocess.run(["/usr/libexec/bluefin-homebrew-prepare", "--refresh"], check=True)
        cls.observer = "bluefin-regression-other"
        subprocess.run(["useradd", "--create-home", "--home-dir", f"/var/home/{cls.observer}",
                        "--shell", "/bin/bash", cls.observer], check=True)
        subprocess.run(["systemctl", "stop", "bluefin-homebrew.service"], check=True)
        subprocess.run(["systemctl", "restart", "bluefin-homebrew-prepare.service"], check=True)
        subprocess.run(["systemctl", "start", "bluefin-homebrew.service"], check=True)

    @classmethod
    def tearDownClass(cls):
        cls.probe.unlink(missing_ok=True)
        subprocess.run(["/usr/libexec/bluefin-homebrew-prepare", "--refresh"], check=True)
        subprocess.run(["systemctl", "stop", "bluefin-homebrew.service"], check=True)
        # Host sudo/PAM starts this fixture account's user manager too. Stop its
        # known units before deleting it; never race logind's UserStopDelaySec.
        observer_uid = pwd.getpwnam(cls.observer).pw_uid
        subprocess.run(["systemctl", "stop", f"user@{observer_uid}.service",
                        f"user-runtime-dir@{observer_uid}.service"], check=True)
        subprocess.run(["userdel", "--remove", cls.observer], check=True)
        subprocess.run(["systemctl", "restart", "bluefin-homebrew-prepare.service"], check=True)
        subprocess.run(["systemctl", "start", "bluefin-homebrew.service"], check=True)
        shutil.rmtree(cls.work)

    def run_guest(self, command, *args, input=b"", **kwargs):
        return subprocess.run(["sudo", "-u", self.user.pw_name, "--", command, *args],
                              cwd=self.work, input=input, capture_output=True, **kwargs)

    def test_pipe_argv_uid_gid_cwd_and_real_user_scope(self):
        result = self.run_guest("/var/lib/bluefin-homebrew/commands/bluefin-test-probe",
                                "", "two words", "$HOME", "${HOME}", "a\nb", "--flag", input=b"pipe-data")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.split(b"\0"),
                         [str(self.user.pw_uid).encode(), str(self.user.pw_gid).encode(),
                          str(self.work).encode(), b"", b"two words", b"$HOME", b"${HOME}",
                          b"a\nb", b"--flag", b"pipe-data"])
        self.assertIn(b"stderr-marker", result.stderr)
        self.assertIn(b"CapEff:\t0000000000000000", result.stderr)
        self.assertIn(b"NoNewPrivs:\t1", result.stderr)
        self.assertIn(f"user-{self.user.pw_uid}.slice".encode(), result.stderr)
        self.assertIn(b".scope", result.stderr)

    def test_nonzero_status_is_forwarded(self):
        tool = Path("/var/lib/bluefin-homebrew/prefix/bin/bluefin-test-exit")
        try:
            tool.write_text("#!/usr/bin/bash\nexit 37\n")
            tool.chmod(0o755)
            result = self.run_guest("/usr/libexec/bluefin-homebrew-command", "--command",
                                    "bluefin-test-exit")
            self.assertEqual(result.returncode, 37, result.stderr)
        finally:
            tool.unlink(missing_ok=True)

    def test_signal_termination_is_forwarded_not_reported_as_success(self):
        tool = Path("/var/lib/bluefin-homebrew/prefix/bin/bluefin-test-signal")
        try:
            tool.write_text("#!/usr/bin/bash\nkill -TERM $$\n")
            tool.chmod(0o755)
            result = self.run_guest("/usr/libexec/bluefin-homebrew-command", "--command", "bluefin-test-signal")
            self.assertIn(result.returncode, {-15, 143}, result.stderr)
        finally:
            tool.unlink(missing_ok=True)

    def test_root_brew_and_command_path_escape_fail(self):
        self.assertNotEqual(subprocess.run(["/usr/bin/brew", "--version"], capture_output=True).returncode, 0)
        for name in ["../bin/bash", "/usr/bin/bash", "", "--env"]:
            result = self.run_guest("/usr/libexec/bluefin-homebrew-command", "--command", name)
            self.assertNotEqual(result.returncode, 0)

    def test_owner_brew_is_real_and_prefix_is_persistent(self):
        result = self.run_guest("/usr/bin/brew", "--prefix")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.rstrip(), b"/home/linuxbrew/.linuxbrew")
        marker = Path("/var/lib/bluefin-homebrew/prefix/.regression-persist")
        try:
            marker.write_text("persistent")
            subprocess.run(["systemctl", "restart", "bluefin-homebrew.service"], check=True)
            self.assertEqual(marker.read_text(), "persistent")
            self.assertEqual(self.run_guest("/usr/bin/brew", "--prefix").returncode, 0)
        finally:
            marker.unlink(missing_ok=True)

    def test_terminal_descriptors_are_not_replaced_by_a_pipe(self):
        import fcntl
        import pty
        import struct
        import termios
        tool = Path("/var/lib/bluefin-homebrew/prefix/bin/bluefin-test-tty")
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        try:
            tool.write_text("#!/usr/bin/bash\n[[ -t 0 && -t 1 && -t 2 ]] || exit 41\n"
                            "[[ -r /dev/tty ]] || exit 42\n"
                            "read -r tty_pid tty_group tty_session tty_fg < <(ps -o pid=,pgid=,sid=,tpgid= -p $$)\n"
                            "[[ $tty_pid == $tty_session && $tty_group == $tty_fg ]] || exit 43\n"
                            "read -r rows columns < <(stty size)\n"
                            "[[ $rows == 24 && $columns == 80 ]] || exit 46\n"
                            "printf tty-ok\n")
            tool.chmod(0o755)
            process = subprocess.Popen(["sudo", "-u", self.user.pw_name, "--",
                                        "/usr/libexec/bluefin-homebrew-command", "--command", "bluefin-test-tty"],
                                       cwd=self.work, stdin=slave, stdout=slave, stderr=slave)
            self.assertEqual(process.wait(timeout=30), 0)
            self.assertTrue(select.select([master], [], [], 5)[0])
            self.assertIn(b"tty-ok", os.read(master, 65536))
        finally:
            tool.unlink(missing_ok=True)
            os.close(master)
            os.close(slave)

    def test_multiplexer_survives_disconnected_client_in_lingering_guest_scope(self):
        session = f"bluefin-regression-{os.getpid()}"
        installed = self.run_guest("/usr/bin/brew", "install", "tmux", timeout=600)
        self.assertEqual(installed.returncode, 0, installed.stderr)
        self.assertFalse(Path("/usr/bin/tmux").exists(), "tmux must not be bundled in the host image")
        result = self.run_guest("/var/lib/bluefin-homebrew/commands/tmux", "new-session", "-d", "-s", session)
        self.assertEqual(result.returncode, 0, "Install tmux with host brew before this lab case: " + result.stderr.decode())
        try:
            result = self.run_guest("/var/lib/bluefin-homebrew/commands/tmux", "has-session", "-t", session)
            self.assertEqual(result.returncode, 0, result.stderr)
            result = self.run_guest("/var/lib/bluefin-homebrew/commands/tmux", "display-message", "-p", "-t", session,
                                    "#{pid}")
            self.assertEqual(result.returncode, 0, result.stderr)
            pid = result.stdout.decode().strip()
            result = subprocess.run(["systemd-run", "--machine=bluefin-homebrew", "--pipe", "--wait", "--quiet",
                                     "/usr/bin/cat", f"/proc/{pid}/cgroup"], capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f"user-{self.user.pw_uid}.slice".encode(), result.stdout)
            self.assertIn(b".scope", result.stdout)
        finally:
            self.run_guest("/var/lib/bluefin-homebrew/commands/tmux", "kill-session", "-t", session)

    def test_nonowner_executes_existing_commands_but_cannot_mutate_prefix_or_run_brew(self):
        observer = pwd.getpwnam(self.observer)
        result = subprocess.run(["sudo", "-u", observer.pw_name, "--", "/usr/bin/brew", "--prefix"],
                                cwd=observer.pw_dir, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        tool = Path("/var/lib/bluefin-homebrew/prefix/bin/bluefin-test-readonly")
        try:
            tool.write_text("#!/usr/bin/bash\n"
                            "[[ $(id -u) != $(cat /etc/bluefin-homebrew-owner) ]] || exit 44\n"
                            "if touch /home/linuxbrew/.linuxbrew/.unauthorized 2>/dev/null; then exit 45; fi\n"
                            "printf readonly-ok\n")
            tool.chmod(0o755)
            result = subprocess.run(["sudo", "-u", observer.pw_name, "--", "/usr/libexec/bluefin-homebrew-command",
                                     "--command", "bluefin-test-readonly"], cwd=observer.pw_dir, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, b"readonly-ok")
        finally:
            tool.unlink(missing_ok=True)

    def test_installed_clients_use_host_sockets_from_inside_the_guest_multiplexer(self):
        installed = self.run_guest("/usr/bin/brew", "install", "docker", "podman", "tmux", timeout=1200)
        self.assertEqual(installed.returncode, 0, installed.stderr)
        session = f"bluefin-engines-{os.getpid()}"
        output = self.work / "engines.out"
        script = self.work / "engines.sh"
        (self.work / "volume.txt").write_text("same-host-project-path\n")
        script.write_text(f'#!/usr/bin/bash\nset -e\nexec >"{output}" 2>&1\n'
                          '[[ $(command -v docker) == /home/linuxbrew/.linuxbrew/bin/docker ]]\n'
                          '[[ $(command -v podman) == /home/linuxbrew/.linuxbrew/bin/podman ]]\n'
                          'docker version --format "{{.Server.Version}}"\npodman info --format "{{.Host.Hostname}}"\n'
                          'docker run --rm alpine:latest echo docker-host-api\n'
                          'podman run --rm docker.io/library/alpine:latest echo podman-host-api\n'
                          'docker run --rm -v "$PWD:/project:ro" alpine:latest cat /project/volume.txt\n'
                          'podman run --rm -v "$PWD:/project:ro" docker.io/library/alpine:latest cat /project/volume.txt\n'
                          'echo engines-ok\n')
        script.chmod(0o755)
        os.chown(script, self.user.pw_uid, self.user.pw_gid)
        result = self.run_guest("/var/lib/bluefin-homebrew/commands/tmux", "new-session", "-d", "-s", session,
                                "/usr/bin/bash", str(script))
        self.assertEqual(result.returncode, 0, result.stderr)
        import time
        for _ in range(600):
            if output.exists() and "engines-ok" in output.read_text():
                break
            time.sleep(1)
        self.assertTrue(output.exists(), "guest multiplexer must create output in the same host project path")
        self.assertIn("docker-host-api", output.read_text())
        self.assertIn("podman-host-api", output.read_text())
        self.assertIn("engines-ok", output.read_text())
        self.assertEqual(output.read_text().count("same-host-project-path"), 2)


if __name__ == "__main__":
    unittest.main()
