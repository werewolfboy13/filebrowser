# Use multi-stage builds to build File Browser from source and keep the final image small
# All stages use Docker Hardened Images, which need a `docker login dhi.io` to pull.

# When the image's SBOM is generated, also scan the build context. It is declared before the first
# FROM, so it applies to the final image's SBOM.
ARG BUILDKIT_SBOM_SCAN_CONTEXT=true

# Stage 1: Build the web client
# The dev variant includes the shell and Corepack needed to install dependencies and build the
# client. The client is the same for every platform, so this stage runs on the platform of the
# machine that builds, also when the image is built for another one.
FROM --platform=$BUILDPLATFORM dhi.io/node:26-dev AS frontend

WORKDIR /opt/workdir/frontend

# Corepack downloads the pnpm version that the packageManager field of package.json names, and
# checks it against the hash there, without asking first. In CI mode, a test left marked `.only`
# fails the run.
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    CI=true

# Install dependencies exactly as the lockfile lists them. The last two names are patterns, so the
# copy also works while the project has no .npmrc or pnpm-workspace.yaml.
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/.npmr[c] frontend/pnpm-workspace.yam[l] ./
RUN corepack enable pnpm && \
    pnpm install --frozen-lockfile

# Copy the rest of the client's source files
COPY frontend ./

# Run the tests, so an image that fails them is never built or pushed, then check the types and
# build the client (the build script does both). Without a network, so nothing here can reach an
# outside service.
RUN --network=none pnpm run test && \
    pnpm run build

# Stage 2: Build the server
# The dev variant includes the shell and the Go toolchain. Go compiles for other platforms itself,
# so this stage also runs on the platform of the machine that builds.
FROM --platform=$BUILDPLATFORM dhi.io/golang:1.27-dev AS build

WORKDIR /opt/workdir

# The runtime image has no C library, so the binary must not need one.
ENV CGO_ENABLED=0

# /etc/mime.types, which Go reads to tell a file's type from its extension, for the types that
# File Browser's own list (files/mime.go) does not have. The runtime image has no such file.
RUN apt-get update && \
    apt-get install -y --no-install-recommends media-types && \
    rm -rf /var/lib/apt/lists/*

# Download the Go modules, and check them against go.sum
COPY go.mod go.sum ./
RUN go mod download && \
    go mod verify

# Copy the source files, and the client, which is embedded in the binary (frontend/assets.go)
COPY . .
COPY --from=frontend /opt/workdir/frontend/dist ./frontend/dist

# Run the checks and the tests, so an image that fails them is never built or pushed. Without a
# network, so no test can reach an outside service.
RUN --network=none go vet ./... && \
    go test ./...

# The version and the commit that `filebrowser version` and the web client show, which CI passes
# in. A plain build leaves them empty, and the binary keeps "(untracked)" and "(unknown)". They are
# declared after the tests, so a new commit does not run the tests again when the source is the same.
ARG VERSION
ARG GIT_SHA

# Set by BuildKit to the platform the image is built for.
ARG TARGETOS
ARG TARGETARCH

# Build the binary. The commit is cut to seven characters, as in the released binaries.
RUN --network=none \
    package=github.com/filebrowser/filebrowser/v2/version && \
    ldflags="-s -w" && \
    if [ -n "$VERSION" ]; then ldflags="$ldflags -X $package.Version=$VERSION"; fi && \
    if [ -n "$GIT_SHA" ]; then ldflags="$ldflags -X $package.CommitSHA=$(printf '%.7s' "$GIT_SHA")"; fi && \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="$ldflags" -o /out/filebrowser .

# The runtime stage cannot run mkdir, so the empty directories for its volumes are made here.
RUN mkdir -p /out/srv /out/config /out/database

# Stage 3: Runtime
# The static image has no shell, package manager or C library: only CA certificates, time zones and
# the like. It has no `latest` tag, so the tag is the version of the image's definition, which
# still gets rebuilt with security patches.
FROM dhi.io/static:20250419

# Copy only the necessary files from the build stage. The binary and the default settings belong to
# root, so the server cannot change them.
COPY --from=build /out/filebrowser /bin/filebrowser
COPY --from=build /etc/mime.types /etc/mime.types
COPY docker/defaults/settings.json /defaults/settings.json

# The directories for the files to serve, the settings and the database, which belong to the user
# the server runs as.
COPY --from=build --chown=1000:1000 /out/srv /srv
COPY --from=build --chown=1000:1000 /out/config /config
COPY --from=build --chown=1000:1000 /out/database /database

# With no shell, there is no entrypoint script to prepare the settings. The server does it itself:
# it reads /config/settings.json unless --config names another file, and on the first start it
# creates that file from the default settings (cmd/config_seed.go).
ENV FB_CONFIG=/config/settings.json \
    FB_CONFIG_TEMPLATE=/defaults/settings.json

# The static image's own user is 65532. File Browser's image has always run as 1000, and the
# volumes and bind mounts of existing installations belong to that user.
USER 1000:1000

# Check that the binary starts in this image, as this user. With no shell here, the test runs as a
# plain command.
RUN [ "/bin/filebrowser", "version" ]

VOLUME /srv /config /database

# The commit the image was built from, which CI passes in, so the image can say which build it is.
# It is set last, so a new commit does not change any earlier layer of this stage.
ARG GIT_SHA
LABEL org.opencontainers.image.revision="${GIT_SHA}"
ENV GIT_SHA=${GIT_SHA}

# Expose ports. The server runs as a regular user, and can listen on port 80 because Docker lets
# every user in a container listen on the ports below 1024.
EXPOSE 80

# The runtime image has no shell or wget, so the check is a command of the binary. It requests
# /health on the address and port in FB_ADDRESS and FB_PORT, or else in the settings file.
HEALTHCHECK --start-period=2s --interval=5s --timeout=3s \
  CMD [ "/bin/filebrowser", "health" ]

# Run service. Arguments given to `docker run` after the image name are passed to the binary, as
# flags of the server or as another command, such as `users add`.
ENTRYPOINT [ "/bin/filebrowser" ]
