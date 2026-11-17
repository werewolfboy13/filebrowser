# Installation

File Browser is a single binary and can be used as standalone executable. It can also run in a [Docker](https://www.docker.com) container, from an image that is built from this repository. The installation and first time setup is quite straightforward independently of which system you use.

> [!NOTE]
>
> This repository is a fork of File Browser, a project that was archived on 2026-09-01. Nothing here is published by the File Browser project, and the project's own downloads do not have the changes made here. That goes for the `filebrowser/filebrowser` images on Docker Hub, the Homebrew tap, the download scripts and the releases of `github.com/filebrowser/filebrowser`.

## Binary

### This Build, From Source

There are no downloads of this build. With [Go](https://go.dev), [pnpm](https://pnpm.io) and [Task](https://taskfile.dev) installed, build the binary in a clone of this repository, as [CONTRIBUTING.md](../CONTRIBUTING.md) describes:

```sh
task build
./filebrowser -r /path/to/your/files
```

To run it in a container instead, without installing any of these, see [Docker](#docker).

### The Original Project's Last Release

The package manager and the download scripts below install the last release of the original File Browser project, not this build. That release gets no fixes any more.

#### Brew

```sh
brew tap filebrowser/tap
brew install filebrowser
filebrowser -r /path/to/your/files
```

#### Unix

```sh
curl -fsSL https://raw.githubusercontent.com/filebrowser/get/master/get.sh | bash
filebrowser -r /path/to/your/files
```

#### Windows

```sh
iwr -useb https://raw.githubusercontent.com/filebrowser/get/master/get.ps1 | iex
filebrowser -r /path/to/your/files
```

File Browser is now up and running. Read the ["First Boot"](#first-boot) section for more information.

## Docker

The image is built from this repository, with the [Dockerfile](../Dockerfile) at its root. It is not the `filebrowser/filebrowser` image of the original project, and it does not behave the same in every respect: read [Coming From the Original Image](#coming-from-the-original-image) before you replace one with the other.

### Building the Image

The Dockerfile builds the web client and the server from source and runs their tests, so nothing but Docker is needed. Its base images are [Docker Hardened Images](https://docs.docker.com/dhi/), which take a login with a Docker account to pull:

```sh
docker login dhi.io
docker build --pull -t filebrowser:local .
```

The image can be built for `linux/amd64` and `linux/arm64`. It cannot be built for 32-bit ARM (`linux/arm/v6`, `linux/arm/v7`), for which the base images do not exist.

### Running the Image

```sh
docker run \
    -v filebrowser_data:/srv \
    -v filebrowser_database:/database \
    -v filebrowser_config:/config \
    -p 8080:80 \
    filebrowser:local
```

Where `filebrowser_data`, `filebrowser_database` and `filebrowser_config` are Docker [volumes](https://docs.docker.com/engine/storage/volumes/), where the data, database and configuration will be stored, respectively. The default configuration and database will be automatically initialized.

The server needs no capability, no new privilege and no writable root filesystem, so it can also run like this:

```sh
docker run \
    --read-only --cap-drop ALL --security-opt no-new-privileges \
    -v filebrowser_data:/srv \
    -v filebrowser_database:/database \
    -v filebrowser_config:/config \
    -p 8080:80 \
    filebrowser:local
```

File Browser is now up and running. Read the ["First Boot"](#first-boot) section for more information.

### Docker Compose

[`compose.yaml`](../compose.yaml) builds the image and runs it with the restrictions above, reachable from the same machine only, on port 8000:

```sh
docker compose up --build
```

Its volumes are the three of the `docker run` command above, under the same names, so it continues an installation that was started with that command, and the other way around.

One File Browser instance needs nothing else. For several instances that serve the same files, [`compose.redis.yaml`](../compose.redis.yaml) adds the Redis that they share for uploads. It says at its top how to use it.

### User and Permissions

The server runs as the user with UID 1000 and GID 1000. To run it as another user, use Docker's [`--user`](https://docs.docker.com/engine/containers/run/#user) option, for example `--user "$(id -u):$(id -g)"`. The image has no init system that could switch users after the start, so it does not know the `PUID` and `PGID` variables of other images: they are ignored.

> [!NOTE]
>
> When using [bind mounts](https://docs.docker.com/engine/storage/bind-mounts/), that is, when you mount a path on the host in the container, you must manually ensure that they have the correct **permissions**. Docker does not do this automatically for you. The host directories must be readable and writable by the user running inside the container. You can use the [`chown`](https://linux.die.net/man/1/chown) command to change the owner of those paths.

### Configuration

The image has nothing in it but the server: no shell, no init system and no entrypoint script. Whatever follows the image name in `docker run`, or is in `command:` in a Compose file, is passed to the `filebrowser` binary, as flags of the server or as [another command](cli/filebrowser.md).

- **The settings file** is `/config/settings.json`, which the `FB_CONFIG` environment variable names. When that file does not exist, the server creates it on its start from `/defaults/settings.json`, which `FB_CONFIG_TEMPLATE` names: port 80 on every address, the files in `/srv` and the database in `/database/filebrowser.db`. A file that exists is never replaced. With the `--config` flag, the server reads the file you name instead, and creates nothing.
- **Single settings**, such as the port, are best set as environment variables: `-e FB_PORT=8080`, `-e FB_ADDRESS=0.0.0.0`, `-e FB_BASE_URL=/files`. Every flag of the [server](cli/filebrowser.md) has one. Flags after the image name work as well: `filebrowser:local --port 8080`.
- **Other commands** use the same volumes, and need the server to be stopped, because the running server holds the database: `docker run --rm -v filebrowser_database:/database -v filebrowser_config:/config filebrowser:local users ls`. With `docker exec` in the running container, such a command fails after a second with `Error: timeout`.

### Health Check

The image checks itself with [`filebrowser health`](cli/filebrowser-health.md), which requests `/health` from the server. It finds the server wherever it listens: once the server has started, it writes that down in the file that `FB_HEALTH_FILE` names, `/dev/shm/filebrowser-health.json`, and the check reads it. So a server that is started with `--port`, `--address`, `--baseURL`, `--cert` and `--key` or `--config` is checked in the right place, like one that is configured through the environment, the settings file or the database, and like one that listens on a unix socket.

A container without `/dev/shm`, which is what `--ipc=none` gives, has no such file. The check then only knows the environment and the settings file, as a server without flags would read them. Either configure the server through those, or point `FB_HEALTH_FILE` at a file in a directory that the server can write to.

### Stopping

When the container is stopped, the server takes no new connections, gives the requests in progress 8 seconds to finish, cuts off the ones that are left, closes the database and exits. Docker has to wait longer than that before it kills the container, or the server is killed in the middle of it. `compose.yaml` sets 15 seconds. For `docker run`, `--stop-timeout 15` does the same, and `docker stop -t 15` does it for one stop. Without them, Docker waits for its default, which is 10 seconds according to its documentation.

### Coming From the Original Image

The image keeps what an installation of `filebrowser/filebrowser` depends on: the files in `/srv`, the settings in `/config/settings.json`, the database in `/database/filebrowser.db`, port 80 and the user 1000. Its volumes can be used as they are. These things are different:

- **No `s6` image and no `PUID` and `PGID`.** There is one image. Where the `s6` image ran with `-e PUID=1001 -e PGID=1001`, use `--user 1001:1001`.
- **No shell and no scripts.** `/init.sh`, `/healthcheck.sh` and `tini` do not exist, and `docker exec ... sh` has nothing to start. A Compose file that sets `entrypoint:` to one of them, or wraps the command in `sh -c`, has to give that up: the entrypoint is `/bin/filebrowser`, and `command:` holds only its flags.
- **Health checks that call a shell fail.** That is a `healthcheck:` whose `test:` is a single string or starts with `CMD-SHELL`, or one that runs `/healthcheck.sh`, `wget` or `curl`. Remove it, so that the image's own check is used, or write it as `test: ["CMD", "/bin/filebrowser", "health"]`.
- **The Command Runner cannot run anything.** See [Command Execution](command-execution.md#docker).
- **Only `linux/amd64` and `linux/arm64`.** The original image was also built for `linux/arm/v6` and `linux/arm/v7`.

## First Boot

Your instance is now up and running. File Browser will automatically bootstrap a database, in which the configuration and the users are stored. You can find the address in which your instance is running, as well as the randomly generated password for the user `admin`, in the console logs.

> [!WARNING]
>
> The automatically generated password for the user `admin` is only displayed once. If you fail to remember it, you will need to manually delete the database and start File Browser again.

Although this is the fastest way to bootstrap an instance, we recommend you to take a look at other possible options, by checking [`config init`](cli/filebrowser-config-init.md) and [`config set`](cli/filebrowser-config-set.md), to make the installation as safe and customized as it can be.

If your goal is to have a public-facing deployment, we recommend taking a look at the [deployment](deployment.md) page for more information on how you can secure your installation.
