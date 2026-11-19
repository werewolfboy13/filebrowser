# Security Policy

This is the security policy of a community-maintained fork of File Browser, at [github.com/werewolfboy13/filebrowser](https://github.com/werewolfboy13/filebrowser). The original project, [github.com/filebrowser/filebrowser](https://github.com/filebrowser/filebrowser), was archived on 2026-09-01 and gets no fixes. This fork continues from its last release, v2.63.23, and owes it nearly all of its code.

## Supported Versions

| Version                                              | Supported |
| ---------------------------------------------------- | --------- |
| The latest release of this fork, and its main branch | ✅        |
| Earlier releases of this fork                        | ❌        |
| Releases of the original project, up to v2.63.23     | ❌        |

A fix goes into the main branch and into the next release. It is not carried back to an earlier release. This fork has not made a release yet: until it has, the main branch is what is supported.

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

- **Command execution: the command runner, hooks and the interactive shell.** The feature is off by default. Turned on (`--disableExec=false`), it has to be treated as shell access to the host for everyone who can use it: the original project published many advisories about it and concluded that it would need a rewrite to be made safe. In this fork's Docker image it cannot work: the image has no shell and no other program, so there is nothing for a command to run. Background: [#5199](https://github.com/filebrowser/filebrowser/issues/5199) of the original project, and [`docs/command-execution.md`](docs/command-execution.md).
- **There is no server-side logout.** Sessions are tokens that the server does not keep. Sessions are revoked on password, permission and scope changes: all sessions of that user end then. Logging out only removes the token from the browser, so a token that has leaked stays valid until it expires, or until the user's sessions are revoked in one of those ways. Background: [#5216](https://github.com/filebrowser/filebrowser/issues/5216) of the original project.
- **Rules go by names, and the filesystem decides which names are one file.** A rule that denies a path compares names by their canonical Unicode form, so that it holds however a name is composed (`é` as one character, or as `e` and an accent), and, where the directory that is served is on a filesystem that ignores case, by case folding as well (`STRASSE` for `straße`). That is more than a filesystem takes for one name, on purpose. A rule that allows a path is matched exactly, as it is written, and on a filesystem that ignores case also in lower case, as it always was: it does not allow a name that the filesystem may keep apart from its own. A rule that is a regular expression is compared as it is written, and takes `(?i)` to ignore case. What is left: whether the filesystem ignores case is found out once, when the server starts, for the directory that is served. A filesystem that is mounted below that directory and differs from it is not noticed, so serve files from one filesystem where rules matter.
- **Upload locking across several instances is not supported.** Instances that share a directory do not lock a file against one another while it is uploaded: each instance only keeps its own requests for one upload apart. The Redis cache that they can share ([`compose.redis.yaml`](compose.redis.yaml)) keeps track of the uploads that are in progress, and is not a lock.

However it is run, put File Browser behind a reverse proxy that terminates TLS, do not expose it to the internet directly, and run it unprivileged in a container with only the directory it serves mounted: see the [README](README.md#security).

## Credit

File Browser was written by its original authors and contributors, who maintained it until 2026. Their security advisories remain the record of what was found in it up to then.
