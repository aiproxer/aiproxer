# Configuration source integrity

This guide documents the filesystem boundary and local certification setup for fixed configuration sources. For the reload workflow and its outcomes, see [Runtime configuration reload](runtime-config-reload.md).

## Linux support boundary

Linux runtime comparison uses an inode lease only when the open configuration-file handle provides positive evidence for the ext4 kernel driver. Detection checks the handle's `statx` mount ID and device number, corroborates the ext4 filesystem magic from that same handle, and requires one matching `ext4` entry in `/proc/self/mountinfo` in the process's mount namespace. The proc entry must be visible, readable, complete, and unambiguous. Filesystem magic or a pathname match alone is insufficient.

The accepted source and each runtime candidate must have the ext4 lease and the same device major/minor. A candidate on a different device, or missing any required handle or mount evidence, fails closed with the existing source-integrity reload outcome. The implementation does not infer support from a path prefix or promise inode-lifetime guarantees for other Linux filesystems.

If a valid configuration can be read at startup but this lease evidence is unavailable, startup keeps the existing bounded-load behavior. A later runtime comparison that needs the lease fails closed with `source-integrity-failed`; the running configuration stays active. This distinction lets operators start on unsupported Linux storage while making runtime recovery behavior explicit.

## Lease lifetime and cleanup

On supported ext4 storage, the active configuration source keeps a read-only file descriptor open while its effective configuration is active. This pins the accepted inode so ext4 cannot reuse it during that lifetime. A rejected, canceled, or pre-publication candidate does not replace the active lease. After a successful source adoption, the prior lease closes when outstanding comparisons release their borrows.

Shutdown drains admitted reload work before requesting the active source's close. If the caller's `Host.Close` deadline expires while a reload or source borrow remains, close returns at the deadline and final cleanup stays armed. Releasing the last borrow completes it. A close failure is cached; later cleanup observes the same completion result instead of retrying the close.

Runtime results and safe diagnostics use the existing bounded source-integrity and cleanup vocabulary. They do not expose source paths, configuration contents, raw close errors, or panic values.

## Local Linux certification

The mandatory certification must run on writable ext4 storage that the process can identify through its open handle and `/proc/self/mountinfo`. Set `TMPDIR` explicitly so temporary test files land on that supported storage. If that filesystem is mounted `noexec`, set `GOTMPDIR` to an existing writable, executable directory for Go's temporary build artifacts:

```bash
export TMPDIR=/path/to/writable/ext4/tmp
# Set this when TMPDIR is mounted noexec.
export GOTMPDIR=/path/to/writable/executable/go-tmp
bash scripts/configsource-certify.sh
bash scripts/configsource-fault-check.sh
```

`configsource-certify.sh` runs the two tagged ext4 certification tests. One test performs 1,000 rejected-candidate/recovery rename cycles; the other performs a separate 1,000-replacement series. They check that every recovery is eligible, the accepted inode is not reused while pinned, and its identity remains stable. The script fails if either exact test is missing, skipped, or does not pass. The fault check also requires exact lifecycle tests and subtests to be discovered and pass. Unsupported storage is a failure for these mandatory gates, not a skip. The normal Linux precommit gate runs both scripts for staged Go changes.

The local scripts use the supplied directories and do not create mounts. In GitHub Actions, only the Ubuntu test job creates the disposable ext4 loop fixture under `RUNNER_TEMP`; its cleanup targets that job-owned fixture. Windows and macOS retain their ordinary portable builds and tests.

## Other platforms

Windows retains its existing `win-fileid` identity and runtime reload behavior. macOS and other platforms with existing adapters retain their current behavior; this change adds no identity-uniqueness guarantee for them. See [Runtime configuration reload](runtime-config-reload.md) for the operator reload contract.
