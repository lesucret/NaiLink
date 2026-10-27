/* SPDX-License-Identifier: MIT
 * Keep Tor's loader/libraries private and terminate it if sing-box crashes.
 * This small static launcher replaces itself; it adds no resident process.
 */
#include <limits.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <unistd.h>

int main(int argc, char **argv) {
    pid_t parent = getppid();
    if (parent <= 1 || prctl(PR_SET_PDEATHSIG, SIGTERM) || getppid() != parent || argc > 96)
        return 125;
    char root[PATH_MAX], loader[PATH_MAX], libraries[PATH_MAX], tor[PATH_MAX];
    ssize_t length = readlink("/proc/self/exe", root, sizeof(root) - 1);
    if (length < 1 || length >= (ssize_t)sizeof(root) - 1) return 125;
    root[length] = 0;
    char *slash = strrchr(root, '/');
    if (!slash) return 125;
    *slash = 0;
    if (snprintf(loader, sizeof(loader), "%s/tor/lib/" LOADER_NAME, root) >= (int)sizeof(loader) ||
        snprintf(libraries, sizeof(libraries), "%s/tor/lib", root) >= (int)sizeof(libraries) ||
        snprintf(tor, sizeof(tor), "%s/tor/bin/tor", root) >= (int)sizeof(tor)) return 125;
    unsetenv("LD_PRELOAD"); unsetenv("LD_AUDIT"); unsetenv("LD_LIBRARY_PATH");
    if (setenv("OPENSSL_CONF", "/dev/null", 1)) return 125;
    char **arguments = calloc((size_t)argc + 4, sizeof(char *));
    if (!arguments) return 125;
    arguments[0] = loader; arguments[1] = "--library-path";
    arguments[2] = libraries; arguments[3] = tor;
    for (int i = 1; i < argc; ++i) arguments[i + 3] = argv[i];
    execv(loader, arguments);
    return 126;
}
