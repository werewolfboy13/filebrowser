> [!NOTE]
>
> **This is a community-maintained fork of [File Browser](https://github.com/filebrowser/filebrowser).** It continues from v2.63.23, the last release of the original project, which was archived on 2026-09-01 and gets no further releases or fixes. Nothing here is published by that project, and its downloads, such as the `filebrowser/filebrowser` images on Docker Hub, do not have the changes made here. The fork lives at [github.com/werewolfboy13/filebrowser](https://github.com/werewolfboy13/filebrowser): its issues, its releases and its security reports belong there.

<p align="center">
  <img src="./branding/banner.png" width="550"/>
</p>

File Browser provides a file managing interface within a specified directory and it can be used to upload, delete, preview and edit your files. It is a **create-your-own-cloud**-kind of software where you can just install it on your server, direct it to a path and access your files through a nice web interface.

**Background:** [Goodbye File Browser, for Real This Time](https://hacdias.com/2026/07/28/filebrowser/), July 2026, by the author of the original project.

## Security

Report a vulnerability privately, as [SECURITY.md](SECURITY.md) describes, and not in a public issue. The [advisories of the original project](https://github.com/filebrowser/filebrowser/security/advisories) stay where they are: they describe that project's releases, and not everything in them still holds for this fork.

Two classes of issue are known and not fixed. [SECURITY.md](SECURITY.md#known-limitations) has these and the other known limitations:

- **Command execution, runner, and hooks.** The original project published many advisories about this feature, and it would need a full rewrite to be made safe. It is disabled by default; if you re-enable it with `--disableExec=false`, treat the ability to run commands as equivalent to shell access on the host. In this fork's Docker image it cannot work, because the image has nothing in it to run. Background: [#5199](https://github.com/filebrowser/filebrowser/issues/5199) of the original project.
- **Session and JWT handling.** Sessions are self-contained JWTs rather than server-side identifiers. Sessions are revoked on password, permission and scope changes; there is no server-side logout. Assume a leaked token is valid until it expires, or until the user's sessions are revoked in one of those ways. Background: [#5216](https://github.com/filebrowser/filebrowser/issues/5216) of the original project.

However you run File Browser:

- **Do not expose it directly to the internet.** Put it behind a reverse proxy that terminates TLS and performs its own authentication.
- **Keep the command runner disabled.** It is off by default, so leave it off. See [`docs/command-execution.md`](docs/command-execution.md).
- **Run it unprivileged, inside a container**, with only the directory you intend to serve mounted into it.

## Docker

A released version of this fork has its image at `ghcr.io/werewolfboy13/filebrowser`. There is no release yet, and anything newer than a release is built from the [Dockerfile](Dockerfile) in this repository, on [Docker Hardened Images](https://docs.docker.com/dhi/), which take a `docker login dhi.io` to pull:

```sh
docker login dhi.io
docker compose up --build
```

That builds the image, runs the tests of the client and the server in the build, and serves File Browser on `http://127.0.0.1:8000`. The password of the user `admin` is in the log of the first start (`docker compose logs`).

`docker compose up --build` builds on the base images that are already on the machine, and does not look for newer ones. To rebuild with their latest security patches, run `docker compose build --pull` first.

Or without Compose:

```sh
docker build --pull -t filebrowser:local .
docker run --stop-timeout 15 -v filebrowser_data:/srv -v filebrowser_database:/database -v filebrowser_config:/config -p 8080:80 filebrowser:local
```

What to know about the image:

- It holds the server and nothing else: no shell, no init system, no `PUID` and `PGID`. The server runs as the user 1000, or as the one that `--user` names, to whom every mount, volumes included, then has to belong already: see [User and Permissions](docs/installation.md#user-and-permissions).
- It needs no capability and no writable root filesystem, and [`compose.yaml`](compose.yaml) runs it that way.
- It is configured with `FB_` environment variables, such as `FB_PORT`, or with `/config/settings.json`, which the server creates on its first start.
- It checks its own health with `filebrowser health`. A health check from a Compose file that was written for the original image, with `/healthcheck.sh` or a shell command, has to go.
- The Command Runner cannot run anything in it.
- It can be built for `linux/amd64` and `linux/arm64` only.

[`docs/installation.md`](docs/installation.md#docker) has the details, and what changes for an installation that used the original image.

## Documentation

Documentation on how to install, configure, and build this project lives in [`docs`](docs) in this repository.

[CONTRIBUTING.md](CONTRIBUTING.md) documents how to build and develop it.

## Maintainer Notes: Releasing

The repository is public, with its issue tracker and private vulnerability reporting open, and it has the tags of the original project up to `v2.63.23`, which came with the fork. No version of the fork has been released from it yet. This is what a release still takes by hand, in the repository's settings on GitHub:

1. **Set the repository variable `IMAGE_NAME`** (Settings, Secrets and variables, Actions, Variables) to `ghcr.io/werewolfboy13/filebrowser`. An image on GitHub's registry is pushed with the release job's own token, so no credential for pushing is needed. Without the variable, a tag releases nothing.
2. **Add the secrets `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN`**, of any Docker account. The base images come from `dhi.io`, which takes a login to pull from. Without them, the image is not built on pushes and pull requests, which the job says in a notice, and the release job fails at its login, before it has pushed anything. After adding them, get one green "Build Docker image" job on the default branch in which the build step of that name ran, before pushing the first tag: the Dockerfile has not been built on a GitHub runner yet. Without the secrets the job is green too, in a few seconds, with that step skipped. Adding the secrets starts no run by itself: re-run the workflow of the last commit, or push a commit.
3. **Tag `v3.0.0-rc.1` first**, with `task release:rc VERSION=3.0.0`: [CONTRIBUTING.md](CONTRIBUTING.md#release) has the procedure. The release job has never run, and for a prerelease it moves neither `latest` nor the major tag of the image. Until there is a tag of the fork's own, a build calls itself by the original project's last tag and what came after it, such as `2.63.23-214-g57dab95d`.
4. **Check the visibility of the image's package after the first push** (the repository's Packages, Package settings). A package that is not public cannot be pulled without a login.
5. **Consider protecting the released branch and the version tags** (Settings, Rules). The repository has no rule yet, so everyone who may push to it may push a `v` tag, and a tag is what releases.
6. **After the first release, remove what it makes untrue:** the sentences that say there is no release yet, here under [Docker](#docker), twice in [`docs/installation.md`](docs/installation.md) and under the table in [SECURITY.md](SECURITY.md).

Pushes are built and tested on the branches `master` and `modernize`, and every pull request is. Another branch to build on has to be added to [`.github/workflows/ci.yaml`](.github/workflows/ci.yaml), in `on.push.branches` and in the condition of the `docker` job.

## License

[Apache License 2.0](LICENSE) © File Browser Contributors
