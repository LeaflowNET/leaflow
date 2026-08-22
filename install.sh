#!/usr/bin/env sh
#
# Install the leaflow CLI.
#
#   curl -fsSL https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.sh | sh
#
# Remove it again:
#
#   curl -fsSL https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.sh | sh -s -- --uninstall
#
# Environment:
#
#   LEAFLOW_VERSION         version to install, default the latest release
#   LEAFLOW_INSTALL_DIR     where to put the binary, default ~/.local/bin
#   LEAFLOW_NO_COMPLETION   set to skip installing shell completion
#   LEAFLOW_UNINSTALL       set to uninstall instead of install
#   LEAFLOW_PURGE           set to uninstall and also remove the configuration
#
# The two variables duplicate --uninstall and --purge because `sh -s --` is the
# part of a piped invocation people get wrong, and because install.ps1 cannot
# take arguments at all: whatever works there should work here too.
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

UNINSTALL="${LEAFLOW_UNINSTALL:-}"
PURGE="${LEAFLOW_PURGE:-}"

# As --purge does below. Reading LEAFLOW_PURGE as "install, and by the way
# delete the configuration" would be an installation that starts by throwing
# away the credentials.
[ -z "$PURGE" ] || UNINSTALL=1

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
    say "Install the leaflow CLI, or remove one that is already there."
    say ""
    say "  --uninstall   remove the binary and the completion scripts"
    say "  --purge       also remove ~/.config/leaflow, credentials included"
    say "  --help        print this"
    say ""
    say "Through a pipe, options go after -s --:"
    say "  curl -fsSL <url> | sh -s -- --uninstall"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --uninstall) UNINSTALL=1 ;;
        # Purging alone would leave an installed CLI with no configuration,
        # which is not a state anyone wants to end up in.
        --purge) UNINSTALL=1; PURGE=1 ;;
        -h | --help) usage; exit 0 ;;
        *) die "unknown option: $1 (run with --help)" ;;
    esac
    shift
done

need() {
    command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

# Only the install path needs these. Requiring tar, or a download tool, to
# delete a few files would make a broken machine impossible to clean up.
setup_download() {
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
}

detect_platform() {
    os=$(uname -s)
    arch=$(uname -m)

    case "$os" in
        Linux) os=linux ;;
        Darwin) os=darwin ;;
        # Git Bash can run this but cannot finish: a persistent PATH on Windows
        # is the user's environment, not a file to append to.
        MINGW* | MSYS* | CYGWIN*)
            die "on Windows, run this in PowerShell instead:
       irm https://raw.githubusercontent.com/${REPO}/main/install.ps1 | iex" ;;
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

install() {
    setup_download

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
            say "Start a new shell so completion loads, then run: ${BINARY} login"
            ;;
        *)
            # Appending to an rc file does not change the shell that is running
            # now, so telling someone to add the line and then run the command
            # sends them straight into "command not found". Reloading is the
            # step, and it is also what makes the completion script take effect.
            say ""
            say "${INSTALL_DIR} is not on your PATH yet."
            say ""
            case "${SHELL:-}" in
                */fish)
                    # fish_add_path applies to this shell and future ones, so
                    # there is nothing to reload for PATH.
                    say "  fish_add_path ${INSTALL_DIR}"
                    say ""
                    say "Then start a new shell (completion needs one) and run: ${BINARY} login"
                    ;;
                */zsh)
                    say "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc"
                    say "  source ~/.zshrc"
                    say ""
                    say "Then run: ${BINARY} login"
                    ;;
                *)
                    say "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc"
                    say "  source ~/.bashrc"
                    say ""
                    say "Then run: ${BINARY} login"
                    ;;
            esac
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

    # Output is discarded and summarised below, so that PATH and completion
    # produce one instruction between them rather than two that read as two
    # separate problems.
    #
    # Nothing here depends on flags the binary may not have: this script is
    # fetched from main while the binary comes from a release, so the two are
    # not the same version. A missing command just means no completion.
    "${INSTALL_DIR}/${BINARY}" install-completion >/dev/null 2>&1 || return 0

    say "Installed shell completion."
}

REMOVED=0

# remove_path deletes one path and says so.
#
# A path that is not there is not a failure: uninstalling twice has to end the
# same way as uninstalling once, and neither the completion files nor the
# configuration are guaranteed to exist in the first place.
remove_path() {
    [ -e "$1" ] || [ -L "$1" ] || return 0

    rm -rf "$1" || die "cannot remove $1"

    say "Removed $1"
    REMOVED=1
}

# uninstall undoes what this script does, and only that.
#
# The PATH line is left alone: this script never wrote one — it prints the line
# and lets the reader add it — and ~/.local/bin is shared with every other tool
# that installs the same way. Removing it would break them.
uninstall() {
    binary="${INSTALL_DIR}/${BINARY}"
    config_home="${XDG_CONFIG_HOME:-${HOME}/.config}"
    config_dir="${LEAFLOW_CONFIG_DIR:-${config_home}/${BINARY}}"

    # Before the binary goes, because it is the only thing that can do this:
    # credentials may live in the system keychain, which is not a file this
    # script can reach, and logging out also revokes the refresh token, which
    # deleting a file never does.
    #
    # Best effort. Being offline, or never having logged in, is no reason to
    # refuse to uninstall. This covers the current context only — any other
    # context keeps its keychain entry, while its credential file goes with the
    # configuration directory below.
    if [ -n "$PURGE" ] && [ -x "$binary" ]; then
        if "$binary" logout >/dev/null 2>&1; then
            say "Signed out."
        else
            say "Could not sign out; a keychain entry may be left behind."
        fi
    fi

    remove_path "$binary"

    # Left behind by an install that was interrupted between the two renames.
    remove_path "${binary}.new"

    data_home="${XDG_DATA_HOME:-${HOME}/.local/share}"

    # Every shell, not just the current one: the completion file was written
    # for whichever shell was in use at install time, and someone who has
    # switched since would otherwise keep a completion for a missing command.
    remove_path "${data_home}/bash-completion/completions/${BINARY}"
    remove_path "${HOME}/.zsh/completions/_${BINARY}"
    remove_path "${config_home}/fish/completions/${BINARY}.fish"

    if [ -n "$PURGE" ]; then
        # LEAFLOW_CONFIG_DIR is someone's own value, and set to / or to a home
        # directory it would turn an uninstall into something unrecoverable.
        case "$config_dir" in
            "" | "/" | "${HOME}" | "${HOME}/") die "refusing to remove ${config_dir}" ;;
        esac

        remove_path "$config_dir"
    fi

    if [ "$REMOVED" -eq 0 ]; then
        say "Nothing to remove: no ${BINARY} installation in ${INSTALL_DIR}."
    else
        say ""
        say "Uninstalled."

        if [ -z "$PURGE" ] && [ -d "$config_dir" ]; then
            say "Kept ${config_dir}. Pass --purge to remove it and sign out."
        fi
    fi

    # A copy from Homebrew, go install, or a second install directory is not
    # this script's to delete. Saying nothing would be worse: the command still
    # runs afterwards, which reads as an uninstall that did not work.
    other=$(command -v "$BINARY" 2>/dev/null || true)
    if [ -n "$other" ]; then
        say ""
        say "Note: ${other} is still on your PATH and was not installed by this script."
    fi
}

main() {
    if [ -n "$UNINSTALL" ]; then
        uninstall
    else
        install
    fi
}

main
