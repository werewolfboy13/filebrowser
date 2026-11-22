# Installation

File Browser is a single binary and can be used as standalone executable. It can also run in a [Docker](https://www.docker.com) container, from an image that is built from this repository. The installation and first time setup is quite straightforward independently of which system you use.

> [!NOTE]
>
> This is a community-maintained fork of File Browser, at [github.com/werewolfboy13/filebrowser](https://github.com/werewolfboy13/filebrowser). The original project was archived on 2026-09-01. Nothing here is published by it, and its own downloads do not have the changes made here. That goes for the `filebrowser/filebrowser` images on Docker Hub, the Homebrew tap `filebrowser/tap`, the download scripts and the releases of `github.com/filebrowser/filebrowser`: they install the original project's last release, which gets no fixes any more.

## Binary

### Releases

A released version of this fork has its binaries on the fork's [releases page](https://github.com/werewolfboy13/filebrowser/releases), for Linux, macOS, Windows, FreeBSD and OpenBSD. Unpack the archive for your system and run the binary:

```sh
./filebrowser -r /path/to/your/files
```

There is no release yet. Until there is one, and for anything newer than the last one, build it from source.

### From Source

With [Go](https://go.dev), [pnpm](https://pnpm.io) and [Task](https://taskfile.dev) installed, build the binary in a clone of this repository, as [CONTRIBUTING.md](../CONTRIBUTING.md) describes:

```sh
task build
./filebrowser -r /path/to/your/files
```

To run it in a container instead, without installing any of these, see [Docker](#docker).

File Browser is now up and running. Read the ["First Boot"](#first-boot) section for more information.

## Docker

The image is built from this repository, with the [Dockerfile](../Dockerfile) at its root. It is not the `filebrowser/filebrowser` image of the original project, and it does not behave the same in every respect: read [Coming From the Original Image](#coming-from-the-original-image) before you replace one with the other.

A released version of this fork has its image on Docker Hub, as `werewolfboy13/filebrowser`. It is pulled with `docker pull werewolfboy13/filebrowser:<tag>`, without a login, and can take the place of `filebrowser:local` in the commands below. There is no release yet. Until there is one, and for anything newer than the last one, build the image.

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
    --stop-timeout 15 \
    -v filebrowser_data:/srv \
    -v filebrowser_database:/database \
    -v filebrowser_config:/config \
    -p 8080:80 \
    filebrowser:local
```

Where `filebrowser_data`, `filebrowser_database` and `filebrowser_config` are Docker [volumes](https://docs.docker.com/engine/storage/volumes/), where the data, database and configuration will be stored, respectively. The default configuration and database will be automatically initialized. `--stop-timeout 15` gives the server the time it takes to stop: see [Stopping](#stopping).

The server needs no capability, no new privilege and no writable root filesystem, so it can also run like this:

```sh
docker run \
    --read-only --cap-drop ALL --security-opt no-new-privileges \
    --stop-timeout 15 \
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

`docker compose up --build` builds on the base images that are already on the machine, and does not look for newer ones. To rebuild with their latest security patches, run `docker compose build --pull` first, which is what `--pull` does for `docker build`.

Its volumes are the three of the `docker run` command above, under the same names, so it continues an installation that was started with that command, and the other way around.

One File Browser instance needs nothing else. For several instances that serve the same files, [`compose.redis.yaml`](../compose.redis.yaml) adds the Redis that they share for uploads. It says at its top how to use it. Redis keeps track of the uploads in progress and locks nothing: two instances can still write the same file at the same time, which is a [known limitation](../SECURITY.md#known-limitations).

### User and Permissions

The server runs as the user with UID 1000 and GID 1000. To run it as another user, use Docker's [`--user`](https://docs.docker.com/engine/containers/run/#user) option, for example `--user "$(id -u):$(id -g)"`. The image has no init system that could switch users after the start, so it does not know the `PUID` and `PGID` variables of other images: they are ignored.

Every mount then has to belong to that user before the server starts, the named volumes too. The image cannot see to that: it has no shell and no `chown`. And Docker gives an empty volume the owner of the image's directory, the user 1000, every time a container starts with it. So a new volume does not work for another user, and neither does one whose owner was changed while it was still empty. That leaves:

- **Bind mounts** of directories that belong to the user: see the note below.
- **Volumes whose owner is changed with another image**, and which are then mounted with `nocopy`, so that Docker leaves the owner as it is:

  ```sh
  docker run --rm --user 0 --entrypoint chown \
      -v filebrowser_data:/srv \
      -v filebrowser_database:/database \
      -v filebrowser_config:/config \
      alpine -R "$(id -u):$(id -g)" /srv /database /config

  docker run \
      --user "$(id -u):$(id -g)" \
      --stop-timeout 15 \
      -v filebrowser_data:/srv:nocopy \
      -v filebrowser_database:/database:nocopy \
      -v filebrowser_config:/config:nocopy \
      -p 8080:80 \
      filebrowser:local
  ```

  The first command also hands an installation that ran as the user 1000 over to the new user.

> [!NOTE]
>
> When using [bind mounts](https://docs.docker.com/engine/storage/bind-mounts/), that is, when you mount a path on the host in the container, you must manually ensure that they have the correct **permissions**. Docker does not do this automatically for you. The host directories must be readable and writable by the user running inside the container. You can use the [`chown`](https://linux.die.net/man/1/chown) command to change the owner of those paths.

### Configuration

The image has nothing in it but the server: no shell, no init system and no entrypoint script. Whatever follows the image name in `docker run`, or is in `command:` in a Compose file, is passed to the `filebrowser` binary, as flags of the server or as [another command](cli/filebrowser.md).

- **The settings file** is `/config/settings.json`, which the `FB_CONFIG` environment variable names. When that file does not exist, the server creates it on its start from `/defaults/settings.json`, which `FB_CONFIG_TEMPLATE` names: port 80 on every address, the files in `/srv` and the database in `/database/filebrowser.db`. A file with anything in it is never replaced. An empty one is filled, such as the one that `touch settings.json` makes before it is mounted at `/config/settings.json` by itself. The template is JSON, so it is only copied to a file whose name ends in `.json`, in lower case, which is how the server tells the format of its settings: with `FB_CONFIG` pointing at a `.yaml` or `.toml` file, write that file yourself, and a name such as `settings.JSON` is refused as a type that cannot be read. With the `--config` flag, the server reads the file you name instead, and creates nothing. Either way, the file that is named has to be there and readable by then: the server does not start without it, and says which file it is.
- **Single settings**, such as the port, are best set as environment variables: `-e FB_PORT=8080`, `-e FB_ADDRESS=0.0.0.0`, `-e FB_BASE_URL=/files`. Every flag of the [server](cli/filebrowser.md) has one. Flags after the image name work as well: `filebrowser:local --port 8080`.
- **A unix socket** takes a settings file of its own, named with `--config` or `FB_CONFIG`. The file that the image creates sets `port` and `address`, and the server refuses a socket next to those: `--socket` or `FB_SOCKET` alone ends in `Error: --socket flag cannot be used with --address, --port, --key nor --cert`. The file has `socket` in their place, and the rest of what the image's file has:

  ```json
  {
    "socket": "/sock/filebrowser.sock",
    "database": "/database/filebrowser.db",
    "root": "/srv"
  }
  ```

  The directory of the socket has to be a mount that the server's user can write to. A server that stops removes its socket. One that is killed leaves it behind, and the next start removes it, once it has found that nothing answers on it. Anything else stops the start and stays as it is: a file of that name that is not a socket, and a socket that another server answers on.
- **Other commands** use the same volumes, and need the server to be stopped, because the running server holds the database: `docker run --rm -v filebrowser_database:/database -v filebrowser_config:/config filebrowser:local users ls`. With `docker exec` in the running container, such a command fails after a second with `Error: timeout`.
- **Commands that write a file** write it to the working directory when its path is not a full one. In the image that is `/`, where the server's user cannot write, so `users export users.json` ends in `Error: open users.json: permission denied`. Give the full path of a file in a volume, or make a volume the working directory with `-w /config`: `docker run --rm -w /config -v filebrowser_database:/database -v filebrowser_config:/config filebrowser:local users export users.json` writes `/config/users.json`. `users import --replace` needs `-w /config` in any case: it writes the users it is about to delete to `users.backup.json` in the working directory first. It can be used only while no user is an administrator, as in a database without users: the last administrator cannot be deleted, so with one registered it refuses and changes nothing. To change the users that are there, use `users import --overwrite /config/users.json`. What `users export`, `config export` and that backup write holds the password hashes of the users or the key that signs their sessions, so these files are readable by their owner only, the server's user, also when they replace a file that others could read.

### Health Check

The image checks itself with [`filebrowser health`](cli/filebrowser-health.md), which requests `/health` from the server. It finds the server wherever it listens: once the server has started, it writes that down in the file that `FB_HEALTH_FILE` names, `/dev/shm/filebrowser-health.json`, and the check reads it. So a server that is started with `--port`, `--address`, `--baseURL`, `--cert` and `--key` or `--config` is checked in the right place, like one that is configured through the environment, the settings file or the database, and like one that listens on a unix socket.

A container without `/dev/shm`, which is what `--ipc=none` gives, has no such file. The check then only knows the environment and the settings file, as a server without flags would read them. Either configure the server through those, or point `FB_HEALTH_FILE` at a file in a directory that the server can write to.

The file belongs to one container. Do not let two containers share it, which happens when both get the same `FB_HEALTH_FILE` in a volume that both mount, and with the default file when they share `/dev/shm`: `--ipc=host`, or `--ipc=container:<name>` (in Compose, `ipc: host` and `ipc: "container:<name>"`, or `ipc: "service:<name>"`). The server is process 1 in each container, so the one that starts second takes the first one's record for one that a killed server left behind, and replaces it. The first container's check then finds a record that is not its own, falls back to the environment and the settings file, and looks where a server that was started with flags does not listen: `filebrowser health` fails with `connection refused`, and the container is reported as `unhealthy`, also after the second one has stopped, until it is restarted. Give each container its own file: `-e FB_HEALTH_FILE=/dev/shm/filebrowser-health-a.json` for one of two that have to share `/dev/shm`.

### Stopping

When the container is stopped, the server takes no new connections, gives the requests in progress 8 seconds to finish, cuts off the ones that are left, closes the database and exits. Docker has to wait longer than that before it kills the container, or the server is killed in the middle of it: the container then ends with the exit code 137, and the server has not closed the database.

`compose.yaml` sets 15 seconds. For `docker run`, `--stop-timeout 15` does the same, as in the commands above, and `docker stop -t 15` does it for one stop. Do not rely on Docker's default instead. It is 10 seconds according to Docker's documentation, but not every version waits that long: Docker Desktop 29 was measured to kill a container after about 3 seconds.

The 8 seconds can be changed with the `--shutdownTimeout` flag or the `FB_SHUTDOWN_TIMEOUT` environment variable, which take a duration with its unit, such as `2s` or `1m`. Keep it below the time that Docker waits.

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
