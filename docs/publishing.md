# Publishing

A tag starting with `v` builds six binaries and publishes a release. The
version, commit and date are stamped into the binary, so `leaflow --version`
identifies exactly what someone is running.

```sh
git tag -a v0.3.0 -m v0.3.0
git push origin v0.3.0
```

`leaflow-doctor` runs first: a release must not ship a CLI whose assumptions no
longer match the contracts it embeds.

## Adding winget

Winget is not configured, because publishing to it needs two things that do not
exist yet, and a configured-but-impossible publisher fails every release:

1. a fork of `microsoft/winget-pkgs` under `LeaflowNET`
2. a token that can push to that fork, as the `WINGET_TOKEN` secret —
   `GITHUB_TOKEN` cannot push to a fork

The first submission is worth doing by hand: Microsoft runs automated
validation and a human review, and the first attempt usually comes back with
something to fix. Automating the ones after that is what goreleaser is for.

When both exist, add a `winget` block to `.goreleaser.yaml`. Note that
goreleaser fails template evaluation on a missing variable, so a token
reference must be one that always resolves — set it from the workflow's `env`
rather than reaching for a template function, since the template function set
is not sprig's and `envOrDefault` does not exist there.

## Homebrew

Deliberately absent: a tap is another repository to maintain, and `install.sh`
already covers macOS without one.
