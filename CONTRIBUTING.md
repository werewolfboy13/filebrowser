# Building File Browser

This is a community-maintained fork of File Browser, at [github.com/werewolfboy13/filebrowser](https://github.com/werewolfboy13/filebrowser). The original project, [github.com/filebrowser/filebrowser](https://github.com/filebrowser/filebrowser), was archived on 2026-09-01 and takes no changes. Issues and pull requests for this fork belong in the fork's repository, and a vulnerability is reported as [SECURITY.md](SECURITY.md) describes.

## Project Structure

The backend side of the application is written in [Go](https://golang.org/), while the frontend (located on a subdirectory of the same name) is written in [Vue.js](https://vuejs.org/). Due to the tight coupling required by some features, basic knowledge of both Go and Vue.js is recommended.

* Learn Go: [https://github.com/golang/go/wiki/Learn](https://github.com/golang/go/wiki/Learn)
* Learn Vue.js: [https://vuejs.org/guide/introduction.html](https://vuejs.org/guide/introduction.html)

To build the fork, clone the fork's repository, not the original one, which does not have the changes made here:

```bash
git clone https://github.com/werewolfboy13/filebrowser
```

The Go module is `github.com/werewolfboy13/filebrowser/v3`.

`go install github.com/werewolfboy13/filebrowser/v3@latest` is not a way to install File Browser. It builds the server only: the web client is not in the repository, it is built from [`frontend`](frontend) and embedded in the binary by `task build` and by the Dockerfile. A binary from `go install` answers every page with `404 Not Found`, serves only the API, and calls itself `v(untracked)/(unknown)`. `@latest` is the newest release, or the newest release candidate for as long as there is no release. Before the fork's first v3 tag it is the tip of the default branch: the tags that came with the fork, up to `v2.63.23`, are the original project's and belong to its module path.

We use [Taskfile](https://taskfile.dev/) to manage the different processes (building, releasing, etc) automatically.

## Build

You can fully build the project in order to produce a binary by running:

```bash
task build
```

## Development

For development, there are a few things to have in mind.

### Frontend

We use [Node.js](https://nodejs.org/en/) on the frontend to manage the build process. Prepare the frontend environment:

```bash
# From the root of the repo, go to frontend/
cd frontend

# Install the dependencies
pnpm install
```

If you just want to develop the backend, you can create a static build of the frontend:

```bash
pnpm run build
```

If you want to develop the frontend, start a development server which watches for changes:

```bash
pnpm run dev
```

Please note that you need to access File Browser's interface through the development server of the frontend.

### Backend

First prepare the backend environment by downloading all required dependencies:

```bash
go mod download
```

You can now build or run File Browser as any other Go project:

```bash
# Build
go build

# Run
go run .
```

## Documentation

Documentation lives in [`docs`](docs) as plain Markdown and is no longer built into a site. The command line reference in [`docs/cli`](docs/cli) is generated from the commands themselves:

```bash
task docs:cli:generate
```

## Release

A version is released by pushing its tag. The release job in [`.github/workflows/ci.yaml`](.github/workflows/ci.yaml) then builds the binaries and puts them on the fork's [releases page](https://github.com/werewolfboy13/filebrowser/releases), and builds the Docker image and pushes it to the name in the repository variable `IMAGE_NAME`. Without that variable the job publishes nothing. The [README](README.md#maintainer-notes-releasing) lists what has to be set up in the repository first.

The tag is made with Task, which runs [commit-and-tag-version](https://github.com/absolute-version/commit-and-tag-version) 13.3.0. It needs Go and pnpm. Run it on the branch that is released, with everything committed:

```bash
# A release candidate. The first one of a version names the version: this tags v3.0.0-rc.1.
task release:rc VERSION=3.0.0

# Every further candidate of that version: v3.0.0-rc.2, and so on.
task release:rc

# The release itself: v3.0.0.
task release
```

An exported `VERSION` or `FLAGS` environment variable is picked up by these tasks as if it had been given to them, `VERSION` by `task release:rc` and `FLAGS` by `task release`, so unset them first.

Each of these:

1. generates the command line reference in [`docs/cli`](docs/cli) again, and commits it if it changed;
2. shows what it is about to do, without doing it: the new section of [`CHANGELOG.md`](CHANGELOG.md) and the tag;
3. asks `Do you wish to proceed?`;
4. writes the section to `CHANGELOG.md`, commits it as `chore(release): 3.0.0-rc.1`, and tags that commit `v3.0.0-rc.1`.

The tag and its commit are signed, so Git has to be able to sign where the task runs: with a GPG key, which takes `gpg` and a key for the committer's address or the one named in `user.signingkey`, or with an SSH key, which takes `gpg.format` set to `ssh` and `user.signingkey` set to the public key's file. To release unsigned instead, set `SIGN` to `''` at the top of [`Taskfile.yml`](Taskfile.yml), the one place that decides it. The task checks for Go, pnpm and a working signature before it writes anything, and it does not run while `CHANGELOG.md` has changes that are not committed, which a run that failed at its commit leaves behind: `git restore --staged --worktree CHANGELOG.md` undoes them. Nor does it run when the last commit is a release commit without a tag, which a run that failed at its tag leaves behind: tag that commit by hand, as the task's message shows, or take it back with `git reset --keep HEAD^`.

Nothing is pushed. To publish, push the branch and the tag to the fork, for example `git push --follow-tags origin master` where `origin` is the fork. In a checkout where `origin` is the archived original project, name the fork's remote instead, as in `git push --follow-tags fork master`: the command that the tool prints at its end always says `origin`.

What decides the version:

- **The fork's first tag is `v3.0.0-rc.1`**, made with `task release:rc VERSION=3.0.0`. `task release` refuses to run for as long as the last version tag is one of the original project's: the commits since `v2.63.23` hold a breaking change, so it would tag `v3.0.0` at once, a full release, for which the release job moves the image tags `latest` and `v3`.
- **`task release:rc VERSION=x.y.z`** tags `vx.y.z-rc.1`. It refuses a `VERSION` that is not three numbers, and one whose first candidate exists.
- **`task release:rc`** without `VERSION` tags the candidate after the last one, and refuses when the last version tag is not a candidate: the tool would number the first candidate of a version `rc.0`.
- **`task release`** after candidates tags their version without the suffix. Otherwise the version follows from the [Conventional Commits](https://www.conventionalcommits.org) since the last release: `v3.0.1` after fixes only, `v3.1.0` after a `feat`, `v4.0.0` after a breaking change (`feat!:`, or a `BREAKING CHANGE:` footer).
- The section of a release in the changelog lists every change since the release before it, the ones that its candidates listed included. The section of a candidate lists the changes since the tag before it.

The links in a new section of the changelog, to commits, comparisons and issues, go to the fork whatever the remotes of the clone are called and wherever they point: [`.versionrc`](.versionrc) sets them. (A clone without a remote called `origin` gets no links to commits, only their hashes.) A `#123` in a commit message becomes a link to issue 123 of the fork, and so does `filebrowser/filebrowser#123`. To refer to an issue of the original project, write its address in full.

The release job only takes a tag that is a version: `v`, three numbers without leading zeros, and for a prerelease a hyphen and its name, in which a number has no leading zero either, such as `v3.0.0` or `v3.0.0-rc.1`. For a prerelease it pushes the image under that one tag and marks the GitHub release as a prerelease. For a full release it also moves the image tags `v3` and `latest`.

## Translations

The Transifex integration stopped on 2026-09-01 and translations submitted there no longer reach this repository. Locale files live in [`frontend/src/i18n`](frontend/src/i18n) and can be edited directly.

## Authentication Provider

To build a new authentication provider, you need to implement the [Auther interface](auth/auth.go), whose method will be called on the login page after the user has submitted their login data.

```go
// Auther is the authentication interface.
type Auther interface {
    // Auth is called to authenticate a request.
    Auth(r *http.Request, s *users.Storage, root string) (*users.User, error)
}
```

After implementing the interface you should:

1. Add it to [`auth` directory](auth).
2. Add it to the [configuration parser](cmd/config.go) for the CLI.
3. Add it to the [`authBackend.Get`](storage/bolt/auth.go).

If you need to add more flags, please update the function `addConfigFlags`.

