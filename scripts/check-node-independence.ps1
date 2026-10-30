# Host no-Node verification lane (spec cursor-sdk-standalone, task 5.1).
# Linux-authoritative: the lane proves Node absence inside an unprivileged
# user+mount namespace, which no Windows host can provide.
$ErrorActionPreference = "Stop"
$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
Write-Error "make node-independence is Linux-authoritative; run it in the Ubuntu CI lane or a POSIX checkout."
exit 1