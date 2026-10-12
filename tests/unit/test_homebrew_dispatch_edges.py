"""Real consumer edge regressions; no replacement for the original nine cases.

Run as root on the isolated immutable-image lab, before and after the bridge fix:
BLUEFIN_HOMEBREW_TEST_USER=<owner> python3 -m unittest discover -s tests/unit -p test_homebrew_dispatch_edges.py -v
No machine restart, replacement bridge, compiler, or preinstalled Brew package is used.
"""
import errno
import fcntl
import os
import pty
import pwd
import re
import select
import signal
import struct
import subprocess
import tempfile
import termios
import time
import unittest
import uuid
from pathlib import Path


@unittest.skipUnless(os.environ.get("BLUEFIN_HOMEBREW_TEST_USER"), "requires real immutable lab machine")
class DispatchEdgeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if os.geteuid() != 0:
            raise RuntimeError("Run these isolated-machine regressions as root")
        cls.user = pwd.getpwnam(os.environ["BLUEFIN_HOMEBREW_TEST_USER"])
        cls.work = Path(tempfile.mkdtemp(prefix="homebrew-edges-", dir=cls.user.pw_dir))
        os.chown(cls.work, cls.user.pw_uid, cls.user.pw_gid)
        cls.name = f"bluefin-edge-probe-{os.getpid()}"
        cls.probe = Path("/var/lib/bluefin-homebrew/prefix/bin") / cls.name
        cls.probe.write_text(
            '#!/usr/bin/bash\nset -euo pipefail\n'
            'mode=$1; shift\n'
            'case $mode in\n'
            ' background) [[ -t 0 && -t 1 && -t 2 ]]; printf "edge-background-ran\\n" ;;\n'
            ' foreground) [[ -t 0 && -t 1 && -t 2 ]]; printf "edge-input-ready\\n"; '
            'read -r value; [[ $value == foreground-input ]]; printf "edge-foreground-ran\\n" ;;\n'
            ' fd3) read -r value <&3; [[ $value == fd-three ]]; printf "edge-fd3-ran\\n" ;;\n'
            ' high) read -r value <&63; [[ $value == fd-sixtythree ]]; '
            'printf "edge-high-ran\\n" >&64; cat /proc/self/cgroup ;;\n'
            ' consume) [[ $(cat "$1") == process-substitution-input ]]; printf "edge-substitution-ran\\n" ;;\n'
            ' produce) printf "edge-substitution-output\\n" > "$1" ;;\n'
            ' descriptors) for path in /proc/self/fd/*; do '
            'readlink "$path" || :; done ;;\n'
            ' sensitive) output=$1; release=$2; shift 2; printf "%s\\0" "$@" > "$output"; '
            'while [[ ! -e $release ]]; do sleep 0.1; done ;;\n'
            ' *) exit 97 ;;\nesac\n'
        )
        cls.probe.chmod(0o755)
        os.chown(cls.probe, cls.user.pw_uid, cls.user.pw_gid)
        cls.launcher = ["/usr/libexec/bluefin-homebrew-command", "--command", cls.name]

    @classmethod
    def tearDownClass(cls):
        import shutil

        cls.probe.unlink(missing_ok=True)
        shutil.rmtree(cls.work)

    def become_owner(self):
        os.initgroups(self.user.pw_name, self.user.pw_gid)
        os.setgid(self.user.pw_gid)
        os.setuid(self.user.pw_uid)

    def owner_shell(self, script, *args, **kwargs):
        return subprocess.run(["/usr/bin/bash", "--noprofile", "--norc", "-c", script,
                               "edge-shell", *args, *self.launcher], cwd=self.work,
                              preexec_fn=self.become_owner, capture_output=True, timeout=30, **kwargs)

    def tty_session(self, line, expect_input=False):
        master, slave = pty.openpty()

        def session():
            os.setsid()
            fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
            self.become_owner()

        env = {**os.environ, "PS1": "edge-shell-ready> ", "PS2": "edge-more> "}
        process = subprocess.Popen(["/usr/bin/bash", "--noprofile", "--norc", "-i"],
                                   cwd=self.work, env=env, stdin=slave, stdout=slave, stderr=slave,
                                   preexec_fn=session)
        os.close(slave)
        output = bytearray()
        sent = False
        fed = False
        deadline = time.monotonic() + 30
        try:
            while time.monotonic() < deadline:
                if not select.select([master], [], [], 0.2)[0]:
                    continue
                try:
                    chunk = os.read(master, 65536)
                except OSError as error:
                    if error.errno == errno.EIO:
                        break
                    raise
                if not chunk:
                    break
                output.extend(chunk)
                if not sent and b"edge-shell-ready> " in output:
                    os.write(master, line.encode() + b"\n")
                    sent = True
                if expect_input and not fed and b"edge-input-ready" in output:
                    os.write(master, b"foreground-input\n")
                    fed = True
                # The echoed printf format contains %s, not an actual status.
                if re.search(rb"edge-status:[0-9]+", output):
                    os.write(master, b"exit\nexit\n")
                    break
            else:
                self.fail(f"TTY job did not finish (possible stop/input theft): {bytes(output)!r}")
            process.wait(timeout=5)
            return bytes(output)
        finally:
            # A failing old-image background job can be stopped in a separate
            # process group. Root cleans only this new session and descendants,
            # including guest-PTY children, rather than orphaning a supervisor.
            table = subprocess.run(["ps", "-eo", "pid=,ppid=,sid="], check=True,
                                   capture_output=True, text=True).stdout
            rows = [tuple(map(int, line.split())) for line in table.splitlines()]
            victims = {pid for pid, _, sid in rows if sid == process.pid}
            while True:
                descendants = {pid for pid, parent, _ in rows if parent in victims}
                if descendants <= victims:
                    break
                victims.update(descendants)
            for pid in victims:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            process.wait(timeout=5)
            os.close(master)

    def test_background_job_does_not_stop_or_steal_shell_input(self):
        command = " ".join(self.launcher)
        output = self.tty_session(
            f"{command} background & edge_job=$!; wait \"$edge_job\"; "
            'edge_status=$?; printf "edge-status:%s\\n" "$edge_status"'
        )
        self.assertIn(b"edge-background-ran", output)
        self.assertIn(b"edge-status:0", output)
        self.assertNotIn(b"Stopped", output)

    def test_foreground_job_retains_interactive_input(self):
        command = " ".join(self.launcher)
        output = self.tty_session(
            f'{command} foreground; edge_status=$?; printf "edge-status:%s\\n" "$edge_status"',
            expect_input=True,
        )
        self.assertIn(b"edge-foreground-ran", output)
        self.assertIn(b"edge-status:0", output)

    def test_caller_fd3_is_not_replaced_by_environment(self):
        result = self.owner_shell('exec 3<<<fd-three; "$@" fd3')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, b"edge-fd3-ran\n")

    def test_higher_read_write_descriptors_cross_real_user_scope(self):
        target = self.work / "higher-output"
        result = self.owner_shell('output=$1; shift; exec 63<<<fd-sixtythree; '
                                  'exec 64>"$output"; "$@" high', str(target))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(target.read_bytes(), b"edge-high-ran\n")
        self.assertIn(f"user-{self.user.pw_uid}.slice".encode(), result.stdout)
        self.assertIn(b".scope", result.stdout)

    def test_dev_fd_process_substitution_reads_in_guest_proc_namespace(self):
        result = self.owner_shell('"$@" consume <(printf process-substitution-input)')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, b"edge-substitution-ran\n")

    def test_dev_fd_process_substitution_writes_and_delivers_eof(self):
        target = self.work / "substitution-output"
        result = self.owner_shell('output=$1; shift; "$@" produce >(cat >"$output"); '
                                  'status=$?; sink=$!; wait "$sink"; exit "$status"', str(target))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(target.read_bytes(), b"edge-substitution-output\n")

    def test_root_namespace_and_cgroup_handles_never_reach_package(self):
        result = self.owner_shell('"$@" descriptors')
        self.assertEqual(result.returncode, 0, result.stderr)
        for marker in (b"/cgroup.procs", b"anon_inode:[pidfd]", b"mnt:[", b"user:[",
                       b"pid:[", b"cgroup:[", b"uts:[", b"net:[", b"ipc:[",
                       b"bluefin-homebrew-environment"):
            self.assertNotIn(marker, result.stdout)
        self.assertNotIn(b"/", result.stdout.splitlines(), "Pinned root descriptor reached the package")

    def test_private_environment_requires_caller_uid_seals_and_framing(self):
        for case in ("root-owned", "unsealed", "bad-framing"):
            with self.subTest(case=case):
                fd = os.memfd_create("edge-invalid-environment", os.MFD_ALLOW_SEALING | os.MFD_CLOEXEC)
                try:
                    payload = b"EDGE=value" if case == "bad-framing" else b"EDGE=value\0"
                    os.write(fd, struct.pack("=III", 0x42464842, len(payload), 0) + payload)
                    if case != "root-owned":
                        os.fchown(fd, self.user.pw_uid, self.user.pw_gid)
                    if case != "unsealed":
                        seals = fcntl.F_SEAL_WRITE | fcntl.F_SEAL_GROW | fcntl.F_SEAL_SHRINK | fcntl.F_SEAL_SEAL
                        fcntl.fcntl(fd, fcntl.F_ADD_SEALS, seals)
                    result = subprocess.run(
                        ["/usr/bin/sudo", "--non-interactive", f"--close-from={fd + 1}", "--",
                         "/usr/libexec/bluefin-homebrew-enter", self.name, str(self.work), str(fd),
                         "--"], cwd=self.work, preexec_fn=self.become_owner,
                        pass_fds=(fd,), capture_output=True, timeout=30,
                    )
                    self.assertEqual(result.returncode, 125, result.stderr)
                    expected = (b"invalid environment framing" if case == "bad-framing" else
                                b"caller environment must be an immutable sealed file")
                    self.assertIn(expected, result.stderr)
                    self.assertNotIn(b"edge-background-ran", result.stdout)
                finally:
                    os.close(fd)

    def test_sensitive_argv_is_delivered_but_not_logged_or_scope_description(self):
        secret = "bluefin-edge-secret-" + uuid.uuid4().hex
        argv_file = self.work / "sensitive-argv"
        release = self.work / "sensitive-release"
        cursor_output = subprocess.run(["journalctl", "--no-pager", "--lines=0", "--show-cursor"],
                                       check=True, capture_output=True, text=True).stdout
        cursor = cursor_output.split("-- cursor: ", 1)[1].strip()
        process = subprocess.Popen([*self.launcher, "sensitive", str(argv_file), str(release), secret],
                                   cwd=self.work, preexec_fn=self.become_owner,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 30
            while process.poll() is None and time.monotonic() < deadline:
                if argv_file.exists() and argv_file.read_bytes() == secret.encode() + b"\0":
                    break
                time.sleep(0.1)
            self.assertTrue(argv_file.exists(), "Real guest command did not become ready")
            self.assertEqual(argv_file.read_bytes(), secret.encode() + b"\0")
            with self.subTest(boundary="user-scope-description"):
                scopes = subprocess.run(["systemctl", "--user", f"--machine={self.user.pw_name}@bluefin-homebrew",
                                         "list-units", "--type=scope", "--all", "--plain", "--full", "--no-legend"],
                                        check=True, capture_output=True, text=True, timeout=10)
                self.assertNotIn(secret, scopes.stdout)
        finally:
            release.touch()
            stdout, stderr = process.communicate(timeout=30)
        self.assertEqual(process.returncode, 0, (stdout, stderr))
        subprocess.run(["journalctl", "--sync"], check=True, capture_output=True)
        journal = subprocess.run(["journalctl", "--no-pager", f"--after-cursor={cursor}", "--output=json"],
                                 check=True, capture_output=True, text=True).stdout
        with self.subTest(boundary="sudo-allowed-command-journal"):
            self.assertNotIn(secret, journal, "Sensitive argv leaked into allowed-command journal logging")


if __name__ == "__main__":
    unittest.main()
