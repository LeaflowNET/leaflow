# Install the leaflow CLI on Windows.
#
#   irm https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.ps1 | iex
#
# Remove it again:
#
#   & ([scriptblock]::Create((irm https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.ps1))) -Uninstall
#
# Environment:
#
#   LEAFLOW_VERSION         version to install, default the latest release
#   LEAFLOW_INSTALL_DIR     where to put the binary, default
#                           %LOCALAPPDATA%\Programs\leaflow
#   LEAFLOW_NO_PATH         set to skip adding the directory to PATH
#   LEAFLOW_UNINSTALL       set to uninstall instead of install
#   LEAFLOW_PURGE           set to uninstall and also remove the configuration
#
# Configured through the environment because `iex` runs the script text and has
# nowhere to put arguments. The switches are for every other way of calling
# this: from a file, or from a script block as above.

param(
    [switch]$Uninstall,
    [switch]$Purge
)

$ErrorActionPreference = 'Stop'

# Invoke-WebRequest repaints a progress bar on every chunk, which on a slow
# console costs more time than the download.
$ProgressPreference = 'SilentlyContinue'

$Repo = 'LeaflowNET/leaflow'
$Binary = 'leaflow.exe'

# Windows PowerShell 5.1 takes the .NET default, which on an unpatched machine
# excludes TLS 1.2 — and github.com requires it. Without this the download fails
# as "connection closed", which reads like a network problem and is not one.
[Net.ServicePointManager]::SecurityProtocol =
    [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

function Write-Step($message) { Write-Host $message }

function Stop-Install($message) {
    Write-Host "error: $message" -ForegroundColor Red
    exit 1
}

# A 32-bit PowerShell on 64-bit Windows says x86 in PROCESSOR_ARCHITECTURE and
# puts the real answer in PROCESSOR_ARCHITEW6432.
function Get-Architecture {
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }

    switch ($arch) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        'x86'   { Stop-Install '32-bit Windows is not supported; the releases are amd64 and arm64' }
        default { Stop-Install "unsupported architecture: $arch" }
    }
}

function Get-InstallDir {
    if ($env:LEAFLOW_INSTALL_DIR) { return $env:LEAFLOW_INSTALL_DIR }

    return Join-Path $env:LOCALAPPDATA 'Programs\leaflow'
}

# Where the CLI itself keeps config.yaml and the credential files, resolved the
# same way it resolves them: Go's os.UserHomeDir is %USERPROFILE% here.
function Get-ConfigDir {
    if ($env:LEAFLOW_CONFIG_DIR) { return $env:LEAFLOW_CONFIG_DIR }
    if ($env:XDG_CONFIG_HOME) { return Join-Path $env:XDG_CONFIG_HOME 'leaflow' }

    return Join-Path $env:USERPROFILE '.config\leaflow'
}

# The redirect target of /releases/latest names the tag, which avoids both a
# JSON parser and the API's rate limit for unauthenticated callers.
function Get-LatestVersion {
    $url = "https://github.com/$Repo/releases/latest"

    $location = $null

    try {
        $response = Invoke-WebRequest -Uri $url -UseBasicParsing -MaximumRedirection 0 -ErrorAction Stop
        $location = $response.Headers['Location']
    } catch {
        # PowerShell 7 raises on a 3xx when redirects are off; 5.1 raises a
        # WebException. The header is on the response either way.
        $failed = $_.Exception.Response
        if ($failed) {
            try { $location = $failed.Headers['Location'] } catch { }

            if (-not $location -and $failed.Headers.Location) {
                $location = $failed.Headers.Location.ToString()
            }
        }
    }

    if ($location -is [array]) { $location = $location[0] }

    if (-not $location) { Stop-Install 'cannot determine the latest version' }

    $version = ($location.ToString() -split '/')[-1]

    if (-not $version -or $version -eq 'latest') {
        Stop-Install 'cannot determine the latest version'
    }

    return $version
}

function Get-Download($uri, $path) {
    try {
        Invoke-WebRequest -Uri $uri -OutFile $path -UseBasicParsing
    } catch {
        Stop-Install "cannot download $uri`n       $($_.Exception.Message)"
    }
}

# Refuses rather than warns: the point of fetching the checksums is to not run
# an unverified binary.
function Test-Checksum($archive, $sums, $name) {
    $actual = (Get-FileHash -Path $archive -Algorithm SHA256).Hash.ToLower()

    $expected = $null

    foreach ($line in Get-Content -Path $sums) {
        $fields = $line -split '\s+' | Where-Object { $_ }
        if ($fields.Count -lt 2) { continue }

        # goreleaser writes binary-mode entries as *name.
        if ($fields[1].TrimStart('*') -eq $name) {
            $expected = $fields[0].ToLower()
            break
        }
    }

    if (-not $expected) { Stop-Install "no checksum published for $name" }

    if ($actual -ne $expected) {
        Stop-Install "checksum mismatch for $name`n       expected $expected`n       got      $actual"
    }
}

# Windows holds a running image open without delete sharing, so this fails while
# leaflow is running — including an MCP server sitting in an editor, which is
# not a window anyone thinks of as "leaflow running".
function Install-Binary($source, $target) {
    try {
        Copy-Item -Path $source -Destination $target -Force
    } catch {
        Stop-Install (
            "cannot write $target`n" +
            "       Close anything using leaflow and try again. An MCP server started by`n" +
            "       an editor counts: check with  Get-Process leaflow"
        )
    }
}

# Read from the User scope rather than from $env:Path, which is the User and
# Machine lists already joined: writing that back would copy every system entry
# into the user's own, where it would never see a system update again.
function Add-ToPath($directory) {
    $current = [Environment]::GetEnvironmentVariable('Path', 'User')

    $entries = @()
    if ($current) { $entries = $current -split ';' | Where-Object { $_ } }

    foreach ($entry in $entries) {
        if ($entry.TrimEnd('\') -ieq $directory.TrimEnd('\')) {
            return $false
        }
    }

    $updated = (@($entries) + $directory) -join ';'
    [Environment]::SetEnvironmentVariable('Path', $updated, 'User')

    # The stored change reaches new processes only, so this session is given the
    # directory too — otherwise the next thing printed, run leaflow, fails in
    # the window the user is standing in.
    $env:Path = "$env:Path;$directory"

    return $true
}

# The mirror of Add-ToPath, and it reads the User scope for the same reason.
#
# Only an exact entry goes: a directory someone put on PATH for their own
# reasons, or one holding other tools, is not this script's to edit beyond the
# line it wrote itself.
function Remove-FromPath($directory) {
    $current = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $current) { return $false }

    $entries = @($current -split ';' | Where-Object { $_ })
    $kept = @($entries | Where-Object { $_.TrimEnd('\') -ine $directory.TrimEnd('\') })

    if ($kept.Count -eq $entries.Count) { return $false }

    [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'User')

    # This session holds its own copy, so it is corrected too. Otherwise leaflow
    # still resolves in the window the uninstall was run from, which reads as an
    # uninstall that did nothing.
    $env:Path = (@($env:Path -split ';' |
        Where-Object { $_ -and $_.TrimEnd('\') -ine $directory.TrimEnd('\') }) -join ';')

    return $true
}

$script:Removed = $false

# Remove-Target deletes one path and says so. A path that is not there is not a
# failure: uninstalling twice has to end the same way as uninstalling once.
function Remove-Target($path, $hint) {
    if (-not (Test-Path -LiteralPath $path)) { return }

    try {
        Remove-Item -LiteralPath $path -Recurse -Force -ErrorAction Stop
    } catch {
        $message = "cannot remove $path`n       $($_.Exception.Message)"
        if ($hint) { $message = "cannot remove $path`n$hint" }

        Stop-Install $message
    }

    Write-Step "Removed $path"
    $script:Removed = $true
}

function Invoke-Install {
    $architecture = Get-Architecture

    $version = $env:LEAFLOW_VERSION
    if (-not $version) { $version = Get-LatestVersion }

    $installDir = Get-InstallDir

    # Archive names carry the version without its leading v.
    $number = $version -replace '^v', ''
    $archive = "leaflow_${number}_windows_${architecture}.zip"
    $base = "https://github.com/$Repo/releases/download/$version"

    $work = Join-Path ([System.IO.Path]::GetTempPath()) ("leaflow-install-" + [System.Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work -Force | Out-Null

    try {
        Write-Step "Downloading leaflow $version for windows_$architecture..."

        Get-Download "$base/$archive" (Join-Path $work $archive)
        Get-Download "$base/checksums.txt" (Join-Path $work 'checksums.txt')

        Test-Checksum (Join-Path $work $archive) (Join-Path $work 'checksums.txt') $archive

        Expand-Archive -Path (Join-Path $work $archive) -DestinationPath (Join-Path $work 'unpacked') -Force

        $extracted = Join-Path $work "unpacked\$Binary"
        if (-not (Test-Path $extracted)) { Stop-Install "$Binary is not in the archive" }

        New-Item -ItemType Directory -Path $installDir -Force | Out-Null

        $target = Join-Path $installDir $Binary
        Install-Binary $extracted $target

        Write-Step "Installed $target"

        $added = $false
        if (-not $env:LEAFLOW_NO_PATH) {
            $added = Add-ToPath $installDir
        }

        Write-Step ''

        if ($added) {
            Write-Step "Added $installDir to your PATH."
            Write-Step 'Open a new terminal for it to apply everywhere, then run: leaflow login'
        } else {
            Write-Step 'Run: leaflow login'
        }

        # Printed, not written. PowerShell scans no directory for completion, so
        # the only place it can go is the profile, and that runs on every shell
        # start.
        Write-Step ''
        Write-Step 'For tab completion, add to $PROFILE:'
        Write-Step '  leaflow completion powershell | Out-String | Invoke-Expression'
    } finally {
        Remove-Item -Path $work -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# Invoke-Uninstall undoes what Invoke-Install does, and only that. The
# completion line is left to its owner: this script prints it and never writes
# it, and $PROFILE holds far more than leaflow.
function Invoke-Uninstall {
    $installDir = Get-InstallDir
    $configDir = Get-ConfigDir
    $target = Join-Path $installDir $Binary

    # Before the binary goes, because it is the only thing that can do this:
    # credentials may sit in Windows Credential Manager, which is not a file
    # this script can reach, and logging out also revokes the refresh token,
    # which deleting a file never does.
    #
    # Best effort. Being offline, or never having logged in, is no reason to
    # refuse to uninstall. It covers the current context only — any other
    # context keeps its Credential Manager entry, while its credential file goes
    # with the configuration directory below.
    if ($Purge -and (Test-Path -LiteralPath $target)) {
        $code = 1

        # $ErrorActionPreference is lowered around the call because Windows
        # PowerShell turns a native command's stderr into an error record, and
        # under 'Stop' that would end the uninstall over a failed logout — the
        # one part of it that is allowed to fail.
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'

        try {
            & $target logout *> $null
            $code = $LASTEXITCODE
        } catch {
            $code = 1
        } finally {
            $ErrorActionPreference = $previous
        }

        if ($code -eq 0) {
            Write-Step 'Signed out.'
        } else {
            Write-Step 'Could not sign out; a Credential Manager entry may be left behind.'
        }
    }

    Remove-Target $target (
        "       Close anything using leaflow and try again. An MCP server started by`n" +
        "       an editor counts: check with  Get-Process leaflow"
    )

    # Only when it is empty, and only best effort: this script created the
    # directory, but a directory someone else has since put files in is theirs.
    if ((Test-Path -LiteralPath $installDir) -and
        -not (Get-ChildItem -LiteralPath $installDir -Force -ErrorAction SilentlyContinue)) {
        Remove-Item -LiteralPath $installDir -Force -ErrorAction SilentlyContinue
    }

    if (Remove-FromPath $installDir) {
        Write-Step "Removed $installDir from your PATH."
        $script:Removed = $true
    }

    if ($Purge) {
        # LEAFLOW_CONFIG_DIR is someone's own value, and pointed at a home or a
        # drive root it would turn an uninstall into something unrecoverable.
        $resolved = $configDir.TrimEnd('\')

        if (-not $resolved -or $resolved -match '^[A-Za-z]:$' -or
            ($env:USERPROFILE -and $resolved -ieq $env:USERPROFILE.TrimEnd('\'))) {
            Stop-Install "refusing to remove $configDir"
        }

        Remove-Target $configDir
    }

    if (-not $script:Removed) {
        Write-Step "Nothing to remove: no leaflow installation in $installDir."
        return
    }

    Write-Step ''
    Write-Step 'Uninstalled.'

    if (-not $Purge -and (Test-Path -LiteralPath $configDir)) {
        Write-Step "Kept $configDir. Pass -Purge to remove it and sign out."
    }

    Write-Step 'If you added the completion line to $PROFILE, remove it there.'
}

if ($env:LEAFLOW_UNINSTALL) { $Uninstall = $true }
if ($env:LEAFLOW_PURGE) { $Purge = $true }

# Purging on its own would be an installation that begins by deleting the
# credentials it is about to want.
if ($Purge) { $Uninstall = $true }

if ($Uninstall) {
    Invoke-Uninstall
} else {
    Invoke-Install
}
