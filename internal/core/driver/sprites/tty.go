package sprites

// ShellArgv is the interactive login shell run in a sprite TTY: bash, else sh.
var ShellArgv = []string{"/bin/sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi"}
