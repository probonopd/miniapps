# minisshd

A persistent, genuinely passwordless SSH daemon for devices that would otherwise need USB
(`adb root`) for shell access - in use on
Allwinner and Amlogic Android TV boxes (X96Q, X96 Mini).

Built after both of the obvious options turned out not to work:

- **Off-the-shelf Android SSH-server apps** (SimpleSSHD, and a `com.daemon.ssh` app found
  pre-installed on one of these devices): both bundle dropbear builds that, with no
  `authorized_keys` configured, always fall back to generating a fresh single-use password
  per connection (or per process restart) instead of accepting a blank one - an intentional
  dropbear safety behavior, not a bug, and not configurable away in either app.
- **SSH keys**: genuinely passwordless, but requires distributing a private key to every
  machine that wants to connect - defeats the actual goal of working seamlessly from any
  machine on the private network.

## What this is

A custom static build of dropbear ([dropbear/](dropbear/), built by `dropbear/build.sh` - see
that folder's own patches for the exact changes), patched to:

1. Accept a blank password via the SSH `"none"` auth method - the client does not even see a
   password prompt, the connection is simply accepted. This needed two changes beyond
   dropbear's standard (but normally password/PAM-gated) `-B` flag:
   - `allowblankpass` defaults to on, since the `-B` flag that normally sets it is only
     compiled in alongside password/PAM auth, which needs `crypt()` - unavailable here (see
     below).
   - On Android, `getpwnam()` cannot see bionic's built-in passwd table at all when called
     from a binary statically linked against glibc (this build's only practical option -
     bionic's own libc is not something a cross-compiled binary can link against from
     outside the Android build system), so it always returns NULL. Patched to synthesize an
     identity instead of failing the login: uid/gid from the dropbear process's own
     `getuid()`/`getgid()` (see "No root" below), shell `/system/bin/sh`, home
     `/storage/emulated/0` (see "SFTP home directory" below), and a blank password field.
2. Skip the `/etc/shells` validation for that same fixed shell - Android has no `/etc/shells`
   file for `getusershell()` to read, so the check would otherwise reject every login.
3. Build without password/PAM auth at all - `crypt()` isn't available when statically linked
   against glibc and running on Android, and it's not needed since the `"none"`-method
   bypass above is the only auth path this build supports.
4. Reject any connection whose source address isn't RFC1918/link-local/loopback, at `accept()`
   time, before any protocol exchange happens - this is the whole point of a blank-password
   `"none"`-method bypass only ever being safe on a private network in the first place. It is
   checked against the actual TCP peer address, not which network interface the packet arrived
   on, so it still holds even if the router were ever configured to forward this port from the
   WAN - that traffic reaches the same interface as LAN traffic once NAT'd, so an interface-based
   restriction (`-l`/`SO_BINDTODEVICE`) would not have caught it, but a source-address check does.

Cross-compiled with `arm-linux-gnueabihf-gcc` (`apt install gcc-arm-linux-gnueabihf` on
Debian/Ubuntu) for armeabi-v7a, statically linked so it has no runtime dependency on
Android's own libc at all.

## SFTP

dropbear itself ships no SFTP implementation - it execs a separate `sftp-server` binary when
a client requests the `sftp` subsystem (`svr-chansession.c`), normally at a fixed, compile-time
path (`/usr/libexec/sftp-server` upstream) that obviously doesn't exist on Android. Without one,
an SFTP client (or any SSH client's SFTP-based file-transfer feature) gets exactly the error
that first surfaced this: `/usr/libexec/sftp-server: inaccessible or not found`, then the
connection closes.

Fixed with OpenSSH's own `sftp-server` - the canonical, most widely interoperable
implementation - cross-compiled the same way as dropbear ([sftp-server/](sftp-server/), built
by `sftp-server/build.sh`):

1. Built **`--without-openssl`**: `sftp-server` does no cryptography of its own (the SSH
   transport's encryption already happened inside dropbear before anything reaches it), so
   this avoids pulling in the much larger OpenSSL dependency entirely. OpenSSH's build still
   requires zlib unconditionally (used by the transport layer elsewhere in the same shared
   build), so `sftp-server/build.sh` cross-compiles a small static zlib first, purely to
   satisfy `configure` - `sftp-server` itself does not compress anything either.
2. Patched for the exact same reason as dropbear's `fill_passwd()`: `getpwuid()` returns NULL
   on this statically-linked-glibc-on-bionic combination, and upstream's `sftp-server-main.c`
   treats that as fatal (`"No user found for uid %lu"`, exits 1). Patched to synthesize a
   `struct passwd` instead (same uid/gid/shell/home values as dropbear's own synthesis) - see
   `sftp-server/patches/openssh-no-android-passwd.patch`. The first attempt at this patch left
   `pw_gecos` as `NULL`, which crashed a few lines later in OpenSSH's own `pwcopy()`
   (`xstrdup(pw->pw_gecos)` has no null-check, unlike the `pw_passwd` field right next to it) -
   worth remembering if extending this further: every field `pwcopy()` touches needs a
   non-NULL value, not just the ones `sftp-server-main.c` itself reads.
3. **Runtime-resolved path, not a compile-time one**: `SFTPSERVER_PATH` is baked into dropbear
   at compile time, but this binary's actual on-device path (the app's own `nativeLibraryDir`)
   is only known at install time and differs per install. Patched dropbear
   (`dropbear/patches/dropbear-sftpserver-env-override.patch`, in `svr-chansession.c`'s
   `sessioncommand()`) to check the `DROPBEAR_SFTPSERVER_PATH` environment variable first,
   falling back to the compiled-in default only if unset. `MinisshdService` sets it when
   launching dropbear, pointing at `libsftpserver.so` in its own `nativeLibraryDir`. This
   lookup happens in dropbear's parent process, before it forks off the per-connection child -
   so it isn't affected by dropbear's usual environment-clearing for the session shell (no `-e`
   flag is needed or used): by the time that clearing happens, the resolved path is already a
   plain string baked into the forked child's memory, not something it still needs to look up.

## SFTP home directory

The synthesized identity's home (`pw_dir`, used as the SFTP/SSH session's initial working
directory) is `/storage/emulated/0` - the actual shared storage root (`Download`, `Music`,
`Pictures`, `DCIM`, ...), not `/data/local/tmp`. The first version of this used
`/data/local/tmp`, which looks world-writable at a glance (world-traversable, shows up fine in
`pwd`/`ls`) but is actually `shell:shell drwxrwx--x` - a regular app uid can list it but not
write to it, which a real SFTP upload attempt caught immediately (`Permission denied`) despite
every read-only check passing. Shared storage needs `WRITE_EXTERNAL_STORAGE`/
`READ_EXTERNAL_STORAGE` in the manifest and - since there is no UI to show a runtime
permission prompt - granted explicitly after install:

```sh
adb shell pm grant org.minisshd android.permission.WRITE_EXTERNAL_STORAGE
adb shell pm grant org.minisshd android.permission.READ_EXTERNAL_STORAGE
```

This only sets the *starting* directory - there is no chroot or sandboxing anywhere in this
setup (same as any unconfigured OpenSSH `sftp-server`/dropbear install), so `cd /`,
`cd /data/local/tmp`, or any other absolute path works from an SFTP client exactly as it would
over the SSH shell, limited only by the app uid's normal Unix (and SELinux, mostly permissive
on these devices) permissions - not by anything this project adds.

## No root

The login this gives you is whatever uid the dropbear process itself runs as - there is no
privilege escalation anywhere in this setup. Running it from an app means that's the app's
own sandboxed uid (`u0_a8x`), not root. This was a deliberate choice, not a limitation worth
chasing further: these devices' `su` binaries are a dead end for an app-initiated escalation
(toybox's `su` only lets an already-root caller switch to a *less* privileged user - the
opposite direction; the other is a never-configured, long-defunct "Superuser" app with no
working grant path). Real root-level work still goes over USB via `adb root`.

## The app

A tiny app (package `org.minisshd`) with no launcher icon and no UI at all - the same
boot-persistence shape as minicall's `BootReceiver`/`CallService`, hosting our own binaries
instead of minicall's helper:

- `BootReceiver` starts a foreground service at `BOOT_COMPLETED` (a persistent,
  low-importance notification is unavoidable for a background service on modern Android, but
  there is nothing to tap and no other visible footprint).
- `MinisshdService` generates an ed25519 host key once, kept in the app's private storage (so
  the fingerprint clients see survives reboots instead of regenerating every time), then runs
  `dropbear -F -E -p 2222 -r <hostkey>` with `DROPBEAR_SFTPSERVER_PATH` set (see "SFTP" above)
  and relaunches it (with backoff) if it ever exits. dropbear, dropbearkey and sftp-server
  travel as `lib*.so` files in the APK (same trick as minicall's helper), so Android extracts
  them, executable, at install time - nothing to unpack at runtime.

## Building and installing

```sh
sh dropbear/build.sh       # once, or after changing a dropbear/patches/*.patch
sh sftp-server/build.sh    # once, or after changing a sftp-server/patches/*.patch
sh build.sh
adb install -r minisshd.apk
adb shell pm grant org.minisshd android.permission.WRITE_EXTERNAL_STORAGE
adb shell pm grant org.minisshd android.permission.READ_EXTERNAL_STORAGE
adb shell am start-foreground-service -n org.minisshd/.MinisshdService   # first run only
```

The `pm grant` calls and `adb shell am start-foreground-service` are only needed once per
install (an upgrade install, `adb install -r`, keeps previously granted permissions) - after
that, `BOOT_COMPLETED` keeps it running across reboots on its own.

## Using it

```sh
ssh -o PreferredAuthentications=none root@<device-ip> -p 2222
```

No password prompt, no key, works from any machine on the private network that can reach the
device - that `-o PreferredAuthentications=none` is only there because most SSH clients try
`publickey` before `none` and would otherwise fail if you happen to have an unrelated key
loaded in your agent; without any keys loaded, a plain `ssh root@<ip> -p 2222` works as-is.
The username given does not matter - any username is accepted and mapped to the same
synthesized identity. SFTP works the same way, through the same port, no separate setup:

```sh
sftp -o PreferredAuthentications=none root@<device-ip> -p 2222
```

or via any Bonjour/zeroconf-aware SFTP client once it's discovered (see "Zeroconf" below) -
no IP needed at all in that case.

Installed and verified (including a full reboot, both devices):

- `x96q`: replaces SimpleSSHD, which is now `pm disable-user`'d.
- `x96mini`: replaces `com.daemon.ssh`, now `pm disable-user`'d.

## Zeroconf

While running, `MinisshdService` advertises `_ssh._tcp` and `_sftp-ssh._tcp` on port 2222 via
`NsdManager` (Android's own mDNS/DNS-SD implementation), under the name `Build.MODEL` (`X96Q`,
`X96mini`) - so clients don't need to already know the device's IP;
`avahi-browse`, `dns-sd`, or any Bonjour-aware SFTP/SSH client can find it. A `WifiManager`
multicast lock is held for the service's lifetime, since mDNS multicast is commonly suppressed
on WiFi to save power otherwise.
