#define _GNU_SOURCE
#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <limits.h>
#include <linux/capability.h>
#include <poll.h>
#include <pwd.h>
#include <sched.h>
#include <signal.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mount.h>
#include <sys/mman.h>
#include <sys/prctl.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <systemd/sd-bus.h>
#include <termios.h>
#include <unistd.h>

#define HELPER "/usr/libexec/bluefin-homebrew-enter"
#define PREFIX "/home/linuxbrew/.linuxbrew"
/* One immutable, caller-owned request. Neither environment nor package argv
   belongs in the privileged sudo command line (including journal _CMDLINE). */
struct request_header { uint32_t magic, environment_size, argument_count; };
#define REQUEST_MAGIC UINT32_C(0x42464842)
#define MACHINE "bluefin-homebrew"
extern char **environ;
static volatile sig_atomic_t child_pid;
static volatile sig_atomic_t resized;
static struct termios saved_terminal;
static bool terminal_saved;
static bool relay_kernel_signals;
static void restore_terminal(void) {
    if (terminal_saved) tcsetattr(STDIN_FILENO, TCSANOW, &saved_terminal);
}

static void fail(const char *message) {
    fprintf(stderr, "bluefin-homebrew: %s%s%s\n", message, errno ? ": " : "", errno ? strerror(errno) : "");
    exit(125);
}
static void require(bool value, const char *message) {
    if (!value) { errno = 0; fail(message); }
}
static void *allocate(size_t size) {
    void *value = calloc(1, size);
    if (!value) fail("allocation failed");
    return value;
}
static unsigned long number(const char *value) {
    char *end = NULL;
    require(value && *value, "missing sudo caller identity");
    errno = 0;
    unsigned long result = strtoul(value, &end, 10);
    require(!errno && !*end && result > 0 && result < 65534, "invalid sudo caller identity");
    return result;
}
static int descriptor_number(const char *value) {
    char *end = NULL;
    require(value && *value, "missing environment descriptor number");
    errno = 0;
    long fd = strtol(value, &end, 10);
    require(!errno && !*end && fd >= 3 && fd < INT_MAX, "invalid environment descriptor number");
    return (int)fd;
}
static int caller_closefrom(void) {
    DIR *directory = opendir("/proc/self/fd");
    if (!directory) fail("cannot inspect caller descriptors");
    int last = 2;
    struct dirent *entry;
    errno = 0;
    while ((entry = readdir(directory))) {
        if (entry->d_name[0] == '.') continue;
        int fd = atoi(entry->d_name);
        if (fd != dirfd(directory) && fd > last) last = fd;
    }
    if (errno) fail("cannot enumerate caller descriptors");
    closedir(directory);
    require(last < INT_MAX, "caller descriptor number is too large");
    return last + 1;
}
static bool command_name(const char *name) {
    return *name && !strchr(name, '/') && strcmp(name, ".") && strcmp(name, "..");
}
static int trusted_file(const char *path) {
    int fd = open(path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) fail("cannot open trusted runtime file");
    struct stat st;
    if (fstat(fd, &st) < 0) fail("cannot inspect runtime file");
    require(S_ISREG(st.st_mode) && st.st_uid == 0 && !(st.st_mode & 022), "untrusted runtime file");
    return fd;
}
static bool below(const char *path, const char *root) {
    size_t n = strlen(root);
    return !strncmp(path, root, n) && (!path[n] || path[n] == '/');
}
static void authorize_cwd(uid_t uid, const char *cwd) {
    require(cwd[0] == '/', "cwd must be absolute");
    char resolved[PATH_MAX];
    if (!realpath(cwd, resolved)) fail("cwd no longer exists");
    require(!strcmp(cwd, resolved), "cwd must be physical and normalized");
    if (!strcmp(cwd, "/")) return;
    FILE *stream = fdopen(trusted_file("/run/bluefin-homebrew/roots"), "r");
    if (!stream) fail("cannot read shared roots");
    char *line = NULL;
    size_t capacity = 0;
    bool allowed = false;
    char caller[32];
    snprintf(caller, sizeof caller, "%u", (unsigned)uid);
    while (getline(&line, &capacity, stream) >= 0) {
        line[strcspn(line, "\n")] = 0;
        char *colon = strchr(line, ':');
        if (!colon) continue;
        *colon++ = 0;
        if ((!strcmp(line, caller) || !strcmp(line, "*")) && below(cwd, colon)) allowed = true;
    }
    free(line);
    fclose(stream);
    require(allowed, "cwd is outside the caller's authorized home/project roots");
}
static void signal_child(int signal_number, siginfo_t *information, void *context) {
    (void)context;
    if (signal_number == SIGWINCH) { resized = 1; return; }
    /* A foreground pipeline already receives terminal-generated signals. */
    if (!relay_kernel_signals && information->si_code == SI_KERNEL) return;
    if (child_pid > 0) kill((pid_t)child_pid, signal_number);
}
static int wait_child(pid_t pid) {
    child_pid = pid;
    struct sigaction action = {.sa_sigaction = signal_child, .sa_flags = SA_SIGINFO};
    sigemptyset(&action.sa_mask);
    int signals[] = {SIGHUP, SIGINT, SIGTERM, SIGQUIT};
    for (size_t i = 0; i < sizeof signals / sizeof *signals; i++) sigaction(signals[i], &action, NULL);
    int status;
    while (waitpid(pid, &status, 0) < 0) if (errno != EINTR) fail("cannot wait for command");
    child_pid = 0;
    if (WIFSIGNALED(status)) {
        int signal_number = WTERMSIG(status);
        signal(signal_number, SIG_DFL);
        raise(signal_number);
        return 128 + signal_number;
    }
    return WEXITSTATUS(status);
}
struct terminal { int master, slave; };
static struct terminal terminal_open(uid_t uid, gid_t gid) {
    struct terminal terminal = {.master = -1, .slave = -1};
    struct stat input, output, error;
    if (!isatty(0) || !isatty(1) || !isatty(2)) return terminal;
    if (fstat(0, &input) < 0 || fstat(1, &output) < 0 || fstat(2, &error) < 0) fail("cannot inspect terminal");
    if (input.st_rdev != output.st_rdev || input.st_rdev != error.st_rdev) return terminal;
    /* A background job must keep the original terminal, not relay/read the
       foreground shell's input or tcsetattr() it (which raises SIGTTOU).
       ENOTTY also covers a supplied PTY without a controlling session. */
    pid_t foreground = tcgetpgrp(STDIN_FILENO);
    if (foreground >= 0 && foreground != getpgrp()) return terminal;
    if (foreground < 0 && errno != ENOTTY) fail("cannot inspect terminal foreground group");
    terminal.master = posix_openpt(O_RDWR | O_NOCTTY | O_CLOEXEC);
    if (terminal.master < 0 || grantpt(terminal.master) < 0 || unlockpt(terminal.master) < 0)
        fail("cannot allocate guest controlling terminal");
    char slave[PATH_MAX];
    require(ptsname_r(terminal.master, slave, sizeof slave) == 0, "cannot identify guest terminal");
    terminal.slave = open(slave, O_RDWR | O_NOCTTY | O_CLOEXEC);
    if (terminal.slave < 0 || fchown(terminal.slave, uid, gid) < 0 || fchmod(terminal.slave, 0600) < 0)
        fail("cannot initialize guest terminal");
    if (tcgetattr(0, &saved_terminal) < 0 || tcsetattr(terminal.slave, TCSANOW, &saved_terminal) < 0)
        fail("cannot preserve terminal settings");
    struct winsize size;
    if (ioctl(0, TIOCGWINSZ, &size) == 0) ioctl(terminal.slave, TIOCSWINSZ, &size);
    return terminal;
}
static void write_bytes(int fd, const char *data, size_t size) {
    while (size) {
        ssize_t count = write(fd, data, size);
        if (count < 0 && errno == EINTR) continue;
        if (count <= 0) fail("terminal disconnected");
        data += count; size -= (size_t)count;
    }
}
static int terminal_relay(pid_t pid, struct terminal terminal) {
    close(terminal.slave);
    child_pid = pid;
    struct sigaction action = {.sa_sigaction = signal_child, .sa_flags = SA_SIGINFO};
    sigemptyset(&action.sa_mask);
    int signals[] = {SIGHUP, SIGINT, SIGTERM, SIGQUIT, SIGWINCH};
    for (size_t i = 0; i < sizeof signals / sizeof *signals; i++) sigaction(signals[i], &action, NULL);
    struct termios raw = saved_terminal;
    cfmakeraw(&raw);
    terminal_saved = true;
    atexit(restore_terminal);
    if (tcsetattr(0, TCSANOW, &raw) < 0) fail("cannot relay raw terminal input");
    struct pollfd fds[] = {{.fd = 0, .events = POLLIN}, {.fd = terminal.master, .events = POLLIN}};
    char data[16384];
    for (;;) {
        if (resized) {
            struct winsize size;
            if (ioctl(0, TIOCGWINSZ, &size) == 0) ioctl(terminal.master, TIOCSWINSZ, &size);
            resized = 0;
        }
        int result = poll(fds, 2, 100);
        if (result < 0 && errno == EINTR) continue;
        if (result < 0) fail("cannot relay terminal");
        if (fds[1].revents & (POLLIN | POLLHUP | POLLERR)) {
            ssize_t count = read(terminal.master, data, sizeof data);
            if (count <= 0) break;
            write_bytes(1, data, (size_t)count);
        }
        if (fds[0].revents & (POLLIN | POLLHUP)) {
            ssize_t count = read(0, data, sizeof data);
            if (count > 0) write_bytes(terminal.master, data, (size_t)count);
            else { fds[0].fd = -1; kill(pid, SIGHUP); }
        }
        siginfo_t exited = {0};
        if (waitid(P_PID, (id_t)pid, &exited, WEXITED | WNOHANG | WNOWAIT) < 0 && errno != EINTR)
            fail("cannot monitor terminal command");
        if (exited.si_pid && !(fds[1].revents & POLLIN)) break;
    }
    close(terminal.master);
    restore_terminal(); terminal_saved = false;
    return wait_child(pid);
}
static void launch(int argc, char **argv) {
    require(getuid() != 0, "Homebrew and guest package commands must run as a non-root user");
    const char *name = strrchr(argv[0], '/');
    name = name ? name + 1 : argv[0];
    int first = 1;
    if (!strcmp(name, "bluefin-homebrew-command")) {
        require(argc >= 3 && !strcmp(argv[1], "--command"), "use --command NAME for direct generic launcher invocation");
        name = argv[2];
        first = 3;
    }
    require(command_name(name), "invalid package command name");
    char *cwd = getcwd(NULL, 0);
    if (!cwd) fail("cannot obtain caller cwd");
    int environment = memfd_create("bluefin-homebrew-environment", MFD_ALLOW_SEALING | MFD_CLOEXEC);
    if (environment < 0) fail("cannot create private caller environment");
    struct request_header header = {.magic = REQUEST_MAGIC, .argument_count = (uint32_t)(argc - first)};
    write_bytes(environment, (const char *)&header, sizeof header);
    for (char **entry = environ; *entry; entry++) {
        size_t size = strlen(*entry) + 1;
        require(size <= UINT32_MAX - header.environment_size, "caller environment is too large");
        write_bytes(environment, *entry, size);
        header.environment_size += (uint32_t)size;
    }
    for (int j = first; j < argc; j++) write_bytes(environment, argv[j], strlen(argv[j]) + 1);
    if (pwrite(environment, &header, sizeof header, 0) != (ssize_t)sizeof header)
        fail("cannot frame private caller request");
    if (fcntl(environment, F_ADD_SEALS, F_SEAL_WRITE | F_SEAL_GROW | F_SEAL_SHRINK | F_SEAL_SEAL) < 0)
        fail("cannot seal caller environment");
    if (environment < 3) {
        int original = environment;
        environment = fcntl(original, F_DUPFD_CLOEXEC, 3);
        if (environment < 0) fail("cannot reserve private caller environment descriptor");
        close(original);
    }
    if (fcntl(environment, F_SETFD, 0) < 0) fail("cannot preserve caller environment descriptor");
    char environment_fd[32], closefrom[48];
    snprintf(environment_fd, sizeof environment_fd, "%d", environment);
    snprintf(closefrom, sizeof closefrom, "--close-from=%d", caller_closefrom());
    char *args[10] = {0};
    size_t i = 0;
    args[i++] = "/usr/bin/sudo";
    args[i++] = "--non-interactive";
    args[i++] = closefrom;
    args[i++] = "--";
    args[i++] = HELPER;
    args[i++] = (char *)name;
    args[i++] = cwd;
    /* Use a free descriptor, never replace the caller's fd3 or process
       substitution FDs. Sudo preserves them; the helper closes only its own
       root-opened machine handles before the unprivileged scope exec. */
    args[i++] = environment_fd;
    args[i++] = "--";
    execv(args[0], args);
    fail("cannot start fixed-machine helper");
}
struct machine {
    int root, pidfd, placement;
    int namespaces[7];
};
static const char *namespace_names[] = {"user", "mnt", "uts", "ipc", "net", "cgroup", "pid"};
static struct machine pin_machine(uid_t uid) {
    sd_bus *bus = NULL;
    sd_bus_message *reply = NULL;
    require(sd_bus_open_system(&bus) >= 0, "cannot connect to host machined");
    require(sd_bus_call_method(bus, "org.freedesktop.machine1", "/org/freedesktop/machine1",
                              "org.freedesktop.machine1.Manager", "GetMachine", NULL, &reply,
                              "s", MACHINE) >= 0, "Homebrew machine is not ready; inspect bluefin-homebrew.service");
    const char *object;
    require(sd_bus_message_read(reply, "o", &object) >= 0, "cannot identify fixed machine");
    char *name = NULL, *service = NULL;
    uint32_t leader = 0;
    require(sd_bus_get_property_string(bus, "org.freedesktop.machine1", object,
                                      "org.freedesktop.machine1.Machine", "Name", NULL, &name) >= 0,
            "cannot verify machine name");
    require(sd_bus_get_property_string(bus, "org.freedesktop.machine1", object,
                                      "org.freedesktop.machine1.Machine", "Service", NULL, &service) >= 0,
            "cannot verify machine service");
    require(!strcmp(name, MACHINE) && !strcmp(service, "systemd-nspawn"), "unexpected machine registration");
    require(sd_bus_get_property_trivial(bus, "org.freedesktop.machine1", object,
                                       "org.freedesktop.machine1.Machine", "Leader", NULL, 'u', &leader) >= 0
            && leader > 1, "cannot obtain machine leader");
    struct machine machine;
    machine.pidfd = (int)syscall(SYS_pidfd_open, leader, 0);
    if (machine.pidfd < 0) fail("cannot pin machine leader");
    char path[80];
    snprintf(path, sizeof path, "/proc/%u", leader);
    int proc = open(path, O_DIRECTORY | O_PATH | O_CLOEXEC);
    if (proc < 0) fail("machine exited");
    struct stat process;
    if (fstat(proc, &process) < 0) fail("cannot verify machine leader identity");
    require(process.st_uid == 0, "machine leader must be host-root-owned");
    int membership = openat(proc, "cgroup", O_RDONLY | O_CLOEXEC);
    if (membership < 0) fail("cannot verify fixed machine service membership");
    char cgroup[4096];
    ssize_t length = read(membership, cgroup, sizeof cgroup - 1);
    close(membership);
    require(length > 0, "cannot read machine service membership");
    cgroup[length] = 0;
    const char *unit = strstr(cgroup, "/bluefin-homebrew.service");
    require(unit && (unit[strlen("/bluefin-homebrew.service")] == '/' ||
                     unit[strlen("/bluefin-homebrew.service")] == '\n'),
            "machine leader is outside the fixed Homebrew service");
    require(!strncmp(cgroup, "0::/", 4), "unified host cgroup hierarchy required");
    size_t root_length = (size_t)(unit - cgroup) + strlen("/bluefin-homebrew.service");
    /* nspawn inserts its payload subtree under the keep-unit service to keep
       host and guest managers from fighting over delegation-root attributes. */
    require(!strncmp(cgroup + root_length, "/payload", strlen("/payload")) &&
            (cgroup[root_length + strlen("/payload")] == '/' ||
             cgroup[root_length + strlen("/payload")] == '\n'), "unexpected nspawn payload cgroup");
    root_length += strlen("/payload");
    cgroup[root_length] = 0;
    char destination[PATH_MAX];
    int size = snprintf(destination, sizeof destination,
                        "/sys/fs/cgroup%s/user.slice/user-%u.slice/user@%u.service/init.scope/cgroup.procs",
                        cgroup + 3, (unsigned)uid, (unsigned)uid);
    require(size > 0 && (size_t)size < sizeof destination, "guest user cgroup path is too long");
    machine.placement = open(destination, O_WRONLY | O_CLOEXEC | O_NOFOLLOW);
    if (machine.placement < 0) fail("guest user manager's delegated cgroup is not ready");
    machine.root = openat(proc, "root", O_DIRECTORY | O_PATH | O_CLOEXEC);
    if (machine.root < 0) fail("cannot pin machine root");
    for (size_t i = 0; i < sizeof namespace_names / sizeof *namespace_names; i++) {
        snprintf(path, sizeof path, "ns/%s", namespace_names[i]);
        machine.namespaces[i] = openat(proc, path, O_RDONLY | O_CLOEXEC);
        if (machine.namespaces[i] < 0) fail("cannot pin machine namespaces");
    }
    close(proc);
    struct pollfd check = {.fd = machine.pidfd, .events = POLLIN};
    require(poll(&check, 1, 0) == 0, "machine leader exited while pinning namespaces");
    free(name);
    free(service);
    sd_bus_message_unref(reply);
    sd_bus_unref(bus);
    return machine;
}
static void drop_privileges(uid_t uid, gid_t gid, gid_t *groups, int count) {
    for (int cap = 0; cap <= CAP_LAST_CAP; cap++)
        if (prctl(PR_CAPBSET_DROP, cap, 0, 0, 0) < 0 && errno != EINVAL) fail("cannot drop bounding capabilities");
    if (setgroups((size_t)count, groups) < 0 || setresgid(gid, gid, gid) < 0 || setresuid(uid, uid, uid) < 0)
        fail("cannot assume guest caller credentials");
    struct __user_cap_header_struct header = {.version = _LINUX_CAPABILITY_VERSION_3};
    struct __user_cap_data_struct data[2] = {{0}};
    if (syscall(SYS_capset, &header, &data) < 0 || prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0)
        fail("cannot drop command privileges");
    if (prctl(PR_CAP_AMBIENT, PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0) < 0) fail("cannot clear ambient capabilities");
}
struct caller_environment { char *data; size_t size; char **arguments; size_t argument_count; };
static struct caller_environment read_environment(uid_t uid, int fd) {
    struct stat metadata;
    if (fstat(fd, &metadata) < 0) fail("missing private environment descriptor");
    int seals = F_SEAL_WRITE | F_SEAL_GROW | F_SEAL_SHRINK | F_SEAL_SEAL;
    int actual_seals = fcntl(fd, F_GET_SEALS);
    require(S_ISREG(metadata.st_mode) && metadata.st_uid == uid && metadata.st_size >= (off_t)sizeof(struct request_header) &&
            metadata.st_size <= sysconf(_SC_ARG_MAX) &&
            actual_seals >= 0 && (actual_seals & seals) == seals,
            "caller environment must be an immutable sealed file");
    struct request_header header;
    require(pread(fd, &header, sizeof header, 0) == (ssize_t)sizeof header && header.magic == REQUEST_MAGIC,
            "invalid private request header");
    size_t size = (size_t)metadata.st_size - sizeof header;
    require(header.environment_size <= size && header.argument_count <= size - header.environment_size,
            "invalid private request lengths");
    struct caller_environment environment = {.size = header.environment_size, .argument_count = header.argument_count};
    environment.data = allocate(size + 1);
    environment.arguments = allocate(((size_t)header.argument_count + 1) * sizeof(char *));
    size_t offset = 0;
    while (offset < size) {
        ssize_t count = pread(fd, environment.data + offset, size - offset, (off_t)(sizeof header + offset));
        if (count < 0 && errno == EINTR) continue;
        require(count > 0, "cannot read private caller environment");
        offset += (size_t)count;
    }
    close(fd);
    require(!environment.size || !environment.data[environment.size - 1], "invalid environment framing");
    for (char *entry = environment.data; entry < environment.data + environment.size;
         entry += strlen(entry) + 1) {
        char *equal = strchr(entry, '=');
        require(equal && equal != entry, "invalid caller environment assignment");
    }
    char *argument = environment.data + environment.size;
    for (size_t i = 0; i < environment.argument_count; i++) {
        char *end = memchr(argument, 0, size - (size_t)(argument - environment.data));
        require(end != NULL, "invalid command argument framing");
        environment.arguments[i] = argument;
        argument = end + 1;
    }
    require(argument == environment.data + size, "trailing private request data");
    return environment;
}
static void enter(struct machine machine, uid_t uid, gid_t gid, gid_t *groups, int count,
                  const char *user, const char *home, const char *cwd, const char *command,
                  struct caller_environment environment, bool prefix_writable, struct terminal terminal) {
    /* A user manager cannot pull a PID out of the host SSH cgroup: cgroup-v2
       common-ancestor write checks forbid it. Host root places this child in
       the caller's already-running guest manager subtree before namespace/UID
       cutover. The unprivileged guest then creates the actual transient scope. */
    char host_pid[32];
    int pid_length = snprintf(host_pid, sizeof host_pid, "%ld\n", (long)getpid());
    if (write(machine.placement, host_pid, (size_t)pid_length) != pid_length)
        fail("cannot place caller in the delegated guest user cgroup");
    close(machine.placement);
    /* Host root enters namespaces owned by its own user namespace first;
       entering the child user namespace last retains the required authority. */
    for (size_t step = 0; step < sizeof namespace_names / sizeof *namespace_names; step++) {
        size_t i = (step + 1) % (sizeof namespace_names / sizeof *namespace_names);
        if (setns(machine.namespaces[i], 0) < 0) fail("cannot enter pinned guest namespace");
        close(machine.namespaces[i]);
    }
    close(machine.pidfd);
    if (fchdir(machine.root) < 0 || chroot(".") < 0 || chdir("/") < 0) fail("cannot enter pinned guest root");
    close(machine.root);
    /* Enter the guest PID namespace too: the user manager must see guest PIDs,
       not a host PID that could name an unrelated process inside the machine. */
    pid_t pid = fork();
    if (pid < 0) fail("cannot enter guest PID namespace");
    if (pid > 0) exit(wait_child(pid));
    if (terminal.slave >= 0) {
        if (setsid() < 0 || ioctl(terminal.slave, TIOCSCTTY, 0) < 0)
            fail("cannot establish guest terminal session");
        for (int fd = 0; fd < 3; fd++) if (dup2(terminal.slave, fd) < 0) fail("cannot connect guest terminal");
        close(terminal.slave);
    }
    if (!prefix_writable) {
        struct mount_attr readonly = {.attr_set = MOUNT_ATTR_RDONLY};
        if (unshare(CLONE_NEWNS) < 0 || mount(NULL, "/", NULL, MS_REC | MS_PRIVATE, NULL) < 0 ||
            mount_setattr(AT_FDCWD, PREFIX, AT_RECURSIVE, &readonly, sizeof readonly) < 0)
            fail("cannot protect shared prefix from nonowner mutation");
    }
    drop_privileges(uid, gid, groups, count);
    clearenv();
    for (char *assignment = environment.data; assignment < environment.data + environment.size;) {
        char *next = assignment + strlen(assignment) + 1;
        char *equal = strchr(assignment, '=');
        *equal++ = 0;
        if (setenv(assignment, equal, 1) < 0) fail("cannot install caller environment");
        assignment = next;
    }
    char runtime[64], address[96];
    snprintf(runtime, sizeof runtime, "/run/user/%u", (unsigned)uid);
    snprintf(address, sizeof address, "unix:path=%s/bus", runtime);
    setenv("HOME", home, 1);
    setenv("USER", user, 1);
    setenv("LOGNAME", user, 1);
    setenv("XDG_RUNTIME_DIR", runtime, 1);
    setenv("DBUS_SESSION_BUS_ADDRESS", address, 1);
    setenv("PATH", PREFIX "/bin:" PREFIX "/sbin:/usr/bin:/usr/sbin", 1);
    unsetenv("SYSTEMD_BUS_ADDRESS");
    unsetenv("SUDO_UID"); unsetenv("SUDO_GID"); unsetenv("SUDO_USER");
    /* Never load caller-controlled libraries into the immutable scope launcher. */
    unsetenv("LD_PRELOAD"); unsetenv("LD_LIBRARY_PATH"); unsetenv("LD_AUDIT");
    if (chdir(cwd) < 0) fail("caller cannot access guest cwd");
    setenv("PWD", cwd, 1);
    char **args = allocate((environment.argument_count + 13) * sizeof(char *));
    size_t n = 0;
    args[n++] = "/usr/bin/systemd-run";
    args[n++] = "--user"; args[n++] = "--scope"; args[n++] = "--quiet";
    /* systemd otherwise publishes the complete argv as the scope Description. */
    args[n++] = "--description=Homebrew package command";
    args[n++] = "--expand-environment=no"; args[n++] = "--collect"; args[n++] = "--";
    args[n++] = "/usr/bin/bash";
    args[n++] = "-p";
    args[n++] = "/usr/lib/bluefin-homebrew/guest/command";
    args[n++] = (char *)command;
    for (size_t i = 0; i < environment.argument_count; i++) args[n++] = environment.arguments[i];
    execv(args[0], args);
    fail("cannot execute immutable guest scope launcher");
}
static int helper(int argc, char **argv) {
    require(getuid() == 0 && geteuid() == 0, "fixed-machine helper requires restricted sudo");
    require(argc == 5 && command_name(argv[1]), "invalid fixed-machine command");
    uid_t uid = (uid_t)number(getenv("SUDO_UID"));
    gid_t gid = (gid_t)number(getenv("SUDO_GID"));
    struct passwd *account = getpwuid(uid);
    require(account && account->pw_gid == gid, "sudo caller must match a provisioned host account");
    char *user = strdup(account->pw_name), *home = strdup(account->pw_dir);
    if (!user || !home) fail("allocation failed");
    int count = 0;
    getgrouplist(user, gid, NULL, &count);
    gid_t *groups = allocate((size_t)count * sizeof(gid_t));
    require(getgrouplist(user, gid, groups, &count) >= 0, "cannot obtain host supplementary groups");
    require(!strcmp(argv[4], "--"), "missing argument delimiter");
    struct caller_environment environment = read_environment(uid, descriptor_number(argv[3]));
    authorize_cwd(uid, argv[2]);
    FILE *owner = fdopen(trusted_file("/var/lib/bluefin-homebrew/owner"), "r");
    unsigned owner_uid, owner_gid;
    require(owner && fscanf(owner, "%u:%u", &owner_uid, &owner_gid) == 2, "cannot read persistent prefix owner");
    fclose(owner);
    require(strcmp(argv[1], "brew") || uid == owner_uid, "only the configured prefix owner may run brew");
    struct machine machine = pin_machine(uid);
    struct terminal terminal = terminal_open(uid, gid);
    relay_kernel_signals = terminal.master >= 0;
    pid_t pid = fork();
    if (pid < 0) fail("cannot start guest command");
    if (!pid) {
        if (terminal.master >= 0) close(terminal.master);
        enter(machine, uid, gid, groups, count, user, home, argv[2], argv[1],
              environment, uid == owner_uid, terminal);
    }
    close(machine.root); close(machine.pidfd);
    close(machine.placement);
    for (size_t i = 0; i < sizeof namespace_names / sizeof *namespace_names; i++) close(machine.namespaces[i]);
    int status = terminal.master >= 0 ? terminal_relay(pid, terminal) : wait_child(pid);
    relay_kernel_signals = false;
    free(environment.data);
    free(environment.arguments);
    if (!strcmp(argv[1], "brew")) {
        /* A root-owned index is refreshed synchronously after package mutation;
           root never interprets or executes a writable prefix file. */
        pid = fork();
        if (pid < 0) fail("cannot refresh command index");
        if (!pid) {
            clearenv(); setenv("PATH", "/usr/bin:/usr/sbin", 1);
            execl("/usr/libexec/bluefin-homebrew-prepare", "bluefin-homebrew-prepare", "--refresh", (char *)NULL);
            _exit(125);
        }
        int refreshed = wait_child(pid);
        if (!status && refreshed) status = refreshed;
    }
    free(user); free(home); free(groups);
    return status;
}
int main(int argc, char **argv) {
    const char *name = strrchr(argv[0], '/');
    name = name ? name + 1 : argv[0];
    if (!strcmp(name, "bluefin-homebrew-enter")) return helper(argc, argv);
    launch(argc, argv);
    return 125;
}
