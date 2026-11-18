# Security Policy

This is the security policy of a community-maintained fork of File Browser, at [github.com/werewolfboy13/filebrowser](https://github.com/werewolfboy13/filebrowser). The original project, [github.com/filebrowser/filebrowser](https://github.com/filebrowser/filebrowser), was archived on 2026-09-01 and gets no fixes. This fork continues from its last release, v2.63.23, and owes it nearly all of its code.

## Supported Versions

| Version                                              | Supported |
| ---------------------------------------------------- | --------- |
| The latest release of this fork, and its main branch | ✅        |
| Earlier releases of this fork                        | ❌        |
| Releases of the original project, up to v2.63.23     | ❌        |

A fix goes into the main branch and into the next release. It is not carried back to an earlier release.

## Reporting a Vulnerability

Report a vulnerability privately, through GitHub: on the [Security](https://github.com/werewolfboy13/filebrowser/security) page of this fork's repository, choose **Report a vulnerability**, or go straight to the [form](https://github.com/werewolfboy13/filebrowser/security/advisories/new). Do not open a public issue for it.

Please include, where possible:

- The version or the commit the issue was found at
- A plaintext proof of concept (no binaries)
- Steps to reproduce
- Recommended remediation, if any

This fork is maintained by volunteers. A report is read and answered as time allows, and no time is promised for an answer or for a fix.

## Before Reporting

- **Check that it concerns this fork.** The [advisories of the original project](https://github.com/filebrowser/filebrowser/security/advisories) describe its releases. Some of what they describe has been changed here, so check against this fork's code, and say in the report which advisory it relates to if there is one.
- **Check that it is not a known limitation**, as listed below.

## Known Limitations

These are known, and are not fixed:

- **Command execution: the command runner, hooks and the interactive shell.** The feature is off by default. Turned on (`--disableExec=false`), it has to be treated as shell access to the host for everyone who can use it: the original project published many advisories about it and concluded that it would need a rewrite to be made safe. In this fork's Docker image it cannot run anything, because the image has no shell and no other program. Background: [#5199](https://github.com/filebrowser/filebrowser/issues/5199) of the original project, and [`docs/command-execution.md`](docs/command-execution.md).
- **Sessions are tokens that the server does not keep.** This fork ends all sessions of a user when the user's password, permissions or scope change. Logging out does not: a token that has leaked stays valid until it expires, or until the user's sessions are ended in one of those ways. Background: [#5216](https://github.com/filebrowser/filebrowser/issues/5216) of the original project.
- **Rules on filesystems that do not tell names apart by case or by form.** A rule that denies a path is written for one spelling of it. On a filesystem that ignores case or normalises names, which is the default on macOS and Windows, another spelling can name the same file, and it may not be covered by the rule in every case. Serve files from a case-sensitive filesystem where rules matter.
- **Several instances on the same files.** Instances that share a directory do not lock a file against one another while it is uploaded: each instance only keeps its own requests for one upload apart. The Redis cache that they can share ([`compose.redis.yaml`](compose.redis.yaml)) keeps track of the uploads that are in progress, and is not a lock.

However it is run, put File Browser behind a reverse proxy that terminates TLS, do not expose it to the internet directly, and run it unprivileged in a container with only the directory it serves mounted: see the [README](README.md#security).

## Credit

File Browser was written by its original authors and contributors, who maintained it until 2026. Their security advisories remain the record of what was found in it up to then.
