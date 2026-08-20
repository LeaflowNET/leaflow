#!/usr/bin/env sh
#
# Install the leaflow CLI.
#
#   curl -fsSL https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.sh | sh
#
# Environment:
#
#   LEAFLOW_VERSION         version to install, default the latest release
#   LEAFLOW_INSTALL_DIR     where to put the binary, default ~/.local/bin
#   LEAFLOW_NO_COMPLETION   set to skip installing shell completion
#
# Written for POSIX sh, not bash: this runs on whatever /bin/sh happens to be,
# including Alpine's ash inside a container.

set -eu

REPO="LeaflowNET/leaflow"
BINARY="leaflow"

# ~/.local/bin, not /usr/local/bin. Installing without sudo is worth more than
# being on the default PATH: a script piped from the internet should not be
# asking for a password, and on macOS /usr/local is not writable by default
# anyway. Override with LEAFLOW_INSTALL_DIR to install system-wide.
INSTALL_DIR="${LEAFLOW_INSTALL_DIR:-${HOME}/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

need() {
    command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

need uname
need mkdir
need tar

# curl or wget, whichever is present. Containers often have exactly one.
if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
    fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
    fetch_stdout() { wget -qO- "$1"; }
else
    die "curl or wget is required"
fi

detect_platform() {
    os=$(uname -s)
    arch=$(uname -m)

    case "$os" in
        Linux) os=linux ;;
        Darwin) os=darwin ;;
        # Windows users get a zip from the releases page; unzip is not something
        # to assume here, and PATH setup differs enough to be worth its own text.
        MINGW* | MSYS* | CYGWIN*)
            die "on Windows, download the zip from https://github.com/${REPO}/releases" ;;
        *) die "unsupported operating system: $os" ;;
    esac

    case "$arch" in
        x86_64 | amd64) arch=amd64 ;;
        aarch64 | arm64) arch=arm64 ;;
        *) die "unsupported architecture: $arch" ;;
    esac

    printf '%s_%s' "$os" "$arch"
}

latest_version() {
    # The redirect target of /releases/latest names the tag, which avoids both a
    # JSON parser and the API's rate limit for unauthenticated callers.
    if command -v curl >/dev/null 2>&1; then
        url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")
    else
        url=$(wget -qS --max-redirect=10 -O /dev/null "https://github.com/${REPO}/releases/latest" 2>&1 |
            awk '/^  Location: /{print $2}' | tail -1)
    fi

    version=${url##*/}

    [ -n "$version" ] && [ "$version" != "latest" ] || die "cannot determine the latest version"

    printf '%s' "$version"
}

verify_checksum() {
    archive=$1
    sums=$2
    name=$3

    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$archive" | cut -d' ' -f1)
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "$archive" | cut -d' ' -f1)
    else
        # Refuse rather than continue. The whole point of downloading the
        # checksums is to not run an unverified binary, and "verified if you
        # happen to have the tool" is not a property anyone can rely on.
        die "sha256sum or shasum is required to verify the download"
    fi

    expected=$(awk -v want="$name" '$2 == want || $2 == "*"want {print $1}' "$sums")

    [ -n "$expected" ] || die "no checksum published for ${name}"
    [ "$actual" = "$expected" ] || die "checksum mismatch for ${name}: expected ${expected}, got ${actual}"
}

main() {
    platform=$(detect_platform)

    version="${LEAFLOW_VERSION:-}"
    if [ -z "$version" ]; then
        version=$(latest_version)
    fi

    # Archive names carry the version without its leading v.
    number=${version#v}
    archive="${BINARY}_${number}_${platform}.tar.gz"
    base="https://github.com/${REPO}/releases/download/${version}"

    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT INT TERM

    say "Downloading ${BINARY} ${version} for ${platform}..."
    fetch "${base}/${archive}" "${tmp}/${archive}" ||
        die "cannot download ${base}/${archive}"

    fetch "${base}/checksums.txt" "${tmp}/checksums.txt" ||
        die "cannot download the checksums"

    verify_checksum "${tmp}/${archive}" "${tmp}/checksums.txt" "$archive"

    # Extracted from inside the directory rather than with -C: BSD tar resolves
    # -f relative to the directory it changed into, which doubles the path.
    (cd "$tmp" && tar -xzf "$archive") ||
        die "cannot extract ${archive}"

    [ -f "${tmp}/${BINARY}" ] || die "${BINARY} is not in the archive"

    mkdir -p "$INSTALL_DIR" || die "cannot create ${INSTALL_DIR}"

    # Move into place via the destination directory so the rename is atomic:
    # replacing a binary that is currently running is fine, overwriting it in
    # place is not.
    chmod +x "${tmp}/${BINARY}"
    mv "${tmp}/${BINARY}" "${INSTALL_DIR}/${BINARY}.new" ||
        die "cannot write to ${INSTALL_DIR}"
    mv "${INSTALL_DIR}/${BINARY}.new" "${INSTALL_DIR}/${BINARY}" ||
        die "cannot install to ${INSTALL_DIR}"

    say "Installed ${INSTALL_DIR}/${BINARY}"

    install_completion

    case ":${PATH}:" in
        *":${INSTALL_DIR}:"*)
            say ""
            say "Run: ${BINARY} login"
            ;;
        *)
            say ""
            say "${INSTALL_DIR} is not on your PATH. Add it:"
            say ""
            case "${SHELL:-}" in
                */fish) say "  fish_add_path ${INSTALL_DIR}" ;;
                */zsh) say "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc" ;;
                *) say "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc" ;;
            esac
            say ""
            say "Then run: ${BINARY} login"
            ;;
    esac
}

# install_completion writes a completion script for the current shell.
#
# Writing the file is safe to do unasked: it goes to a directory the shell
# already scans, and a stale one is inert. Editing ~/.zshrc is not, so that is
# never done here — install-completion prints the one line to add when zsh
# needs it.
#
# Failure is not an error. Completion is a convenience, and a shell this does
# not know about should not make an installation look like it failed.
install_completion() {
    [ -z "${LEAFLOW_NO_COMPLETION:-}" ] || return 0

    # Probed first, because this script is fetched from main and may be running
    # against an older release that has no such command. Letting that print its
    # own error would make a successful installation look broken.
    "${INSTALL_DIR}/${BINARY}" install-completion --help >/dev/null 2>&1 || return 0

    "${INSTALL_DIR}/${BINARY}" install-completion 2>&1 || true
}

main
