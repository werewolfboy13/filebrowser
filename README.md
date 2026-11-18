> [!WARNING]
> 
> **File Browser is archived on 2026-09-01**. The last planned release has already shipped. There will be no further releases, bug fixes, or security fixes.   

> [!NOTE]
>
> **This repository is a fork**, which continues from the last release of the archived project. Nothing here is published by the File Browser project. The `filebrowser/filebrowser` images on Docker Hub and the project's other downloads do not have the changes made here: this build has its own Docker image, which is built from this repository. See [Docker](#docker).

<p align="center">
  <img src="./branding/banner.png" width="550"/>
</p>

File Browser provides a file managing interface within a specified directory and it can be used to upload, delete, preview and edit your files. It is a **create-your-own-cloud**-kind of software where you can just install it on your server, direct it to a path and access your files through a nice web interface.

**Background:** [Goodbye File Browser, for Real This Time](https://hacdias.com/2026/07/28/filebrowser/), July 2026.

## Security

Published advisories are listed under [security advisories](https://github.com/filebrowser/filebrowser/security/advisories),
and reporting instructions are in [SECURITY.md](SECURITY.md). Two known issue classes
remain unaddressed and will not be fixed:

- **Command execution, runner, and hooks.** This feature is plagued with vulnerabilities across many published advisories, and would need a full rewrite to be made safe. It is disabled by default; if you re-enable it with `--disable-exec=false`, treat the ability to run commands as equivalent to shell access on the host. Background: [#5199](https://github.com/filebrowser/filebrowser/issues/5199).
- **Session and JWT handling.** Sessions are self-contained JWTs rather than server-side identifiers, so they cannot be revoked, which means that logout, password changes, and renewal leave previously issued tokens valid until they expire, and the same refresh token can be redeemed repeatedly. Assume a leaked token is valid until expiry. Background: [#5216](https://github.com/filebrowser/filebrowser/issues/5216).

If you keep running File Browser, treat it as unmaintained software:

- **Do not expose it directly to the internet.** Put it behind a reverse proxy that terminates TLS and performs its own authentication.
- **Keep the command runner disabled.** It is off by default, so leave it off. See [#5199](https://github.com/filebrowser/filebrowser/issues/5199) and [`docs/command-execution.md`](docs/command-execution.md).
- **Run it unprivileged, inside a container**, with only the directory you intend to serve mounted into it.

## Docker

The image is built from the [Dockerfile](Dockerfile) in this repository, on [Docker Hardened Images](https://docs.docker.com/dhi/), which take a `docker login dhi.io` to pull:

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

- It holds the server and nothing else: no shell, no init system, no `PUID` and `PGID`. The server runs as the user 1000, or as the one that `--user` names.
- It needs no capability and no writable root filesystem, and [`compose.yaml`](compose.yaml) runs it that way.
- It is configured with `FB_` environment variables, such as `FB_PORT`, or with `/config/settings.json`, which the server creates on its first start.
- It checks its own health with `filebrowser health`. A health check from a Compose file that was written for the original image, with `/healthcheck.sh` or a shell command, has to go.
- The Command Runner cannot run anything in it.
- It can be built for `linux/amd64` and `linux/arm64` only.

[`docs/installation.md`](docs/installation.md#docker) has the details, and what changes for an installation that used the original image.

## Documentation

Documentation on how to install, configure, and build this project lives in [`docs`](docs) in this repository.

[CONTRIBUTING.md](CONTRIBUTING.md) documents how to build and develop the project, which remains useful to anyone forking it.

## Before Publishing This Fork

This fork has no home of its own yet. Until it is decided where it lives, these are left as they are, and each has to be looked at before the repository is made public:

- **[`.github/CODEOWNERS`](.github/CODEOWNERS)** names `@filebrowser/maintainers`, a team that only the original organization has.
- **[CONTRIBUTING.md](CONTRIBUTING.md)** has the reader clone `github.com/filebrowser/filebrowser`, and [`docs/installation.md`](docs/installation.md) sends the reader there to build this repository: followed as written, that builds the original code, without the changes made here.
- **[SECURITY.md](SECURITY.md)**, the [Security](#security) section above and the notice that the server logs on every start (`cmd/root.go`) point to the original project's advisories and say that nothing gets fixed. The fork has no place of its own to report a vulnerability to.
- **The issue and pull request templates** in [`.github`](.github) link to the original project's issues, releases and discussions.
- **The tags of the original project.** Every tag up to `v2.63.23` carries the original project's workflow, which releases on any pushed `v*` tag, with nothing that checks where it runs. GitHub runs the workflow of the commit that a tag points to, so pushing these tags to a new repository that has Actions enabled starts that workflow. It should stop early there, for lack of the `GH_PAT` secret it uses, but this was not tried. Push the old tags before Actions is enabled, or all of them in one push: GitHub starts nothing when more than three tags are pushed at once.
- **The first release.** The release job in [`.github/workflows/ci.yaml`](.github/workflows/ci.yaml) has never run. It needs the repository variable `IMAGE_NAME`, and the secrets `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` for the base images. Make the first tag a prerelease, such as `v3.0.0-rc.1`, which moves neither `latest` nor the major tag of the image.
- **The branch that CI runs on.** Pushes are built on `master` and `modernize`. Another default branch has to be added to the workflow, in `on.push.branches` and in the condition of the `docker` job.

## License

[Apache License 2.0](LICENSE) © File Browser Contributors
