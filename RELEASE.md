# Releasing

## How a release happens

A push to `main` starts the `release-please` workflow. The tool reads the new commits and keeps one
open pull request, the Release PR. That PR carries the next version number and the new `CHANGELOG.md`
section. The maintainer merges it. The merge is the release: the tool then creates the tag on the
merge commit and a GitHub Release with the same text. No workflow tags any other merge.

## What the number means

The number covers the tool and the wire protocol together. `docs/PROTOCOL.md` §8 gives the wire
meaning of a major and a minor move.

The repository is private today. `release-please-config.json` sets `versioning: always-bump-patch`,
so every release is `0.0.x`, whatever the change type.

`0.1.0` is the first stable release and the go-public release. The maintainer forces it once. He puts
`Release-As: 0.1.0` in the body of the same pull request that sets `versioning` to `default`. After
that release a feature moves the middle digit and a fix moves the last digit.

## PR titles

A pull request title is the release note for that change. Write it as `type(component): description`.

`feat`, `fix` and `perf` release something. `chore`, `ci`, `docs`, `refactor`, `style` and `test`
release nothing. Add `!` after the type to mark a breaking change, for example
`feat(cli)!: rename the flag`.

After `0.1.0`, `feat` moves the middle digit, `fix` and `perf` the last digit, and `!` the first.

## Merges

Squash merge every pull request. The squash commit takes the PR title, so the title reaches the tool.
A merge commit hides the title and gives the tool the branch commits instead.

## Review

A Release PR needs no code review. The tool writes every line of it. Read the version number and
the changelog section, then merge.

## The token

The workflow reads the repository secret `RELEASE_PLEASE_TOKEN`. It is a fine-grained personal access
token. It is scoped to this repository, with write access to contents, pull requests and issues. It
expires on EXPIRY-DATE-TBD.

The default `GITHUB_TOKEN` is not used. A pull request that it opens starts no workflow, so the
Release PR would carry no CI run.

An absent secret fails the workflow with `Input required and not supplied: token`. An expired token
fails it on the first API call. In both cases no Release PR opens until the maintainer puts a new
token in the secret.

## Installing

`./install.sh` builds the newest release tag in a temporary worktree. Your checkout does not change.
`./install.sh --main` builds the checkout's `HEAD`. With no release tag and no `--main`, the script
exits 2 and names `--main`.
