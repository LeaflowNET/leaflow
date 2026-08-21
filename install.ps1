# Install the leaflow CLI on Windows.
#
#   irm https://raw.githubusercontent.com/LeaflowNET/leaflow/main/install.ps1 | iex
#
# Environment:
#
#   LEAFLOW_VERSION         version to install, default the latest release
#   LEAFLOW_INSTALL_DIR     where to put the binary, default
#                           %LOCALAPPDATA%\Programs\leaflow
#   LEAFLOW_NO_PATH         set to skip adding the directory to PATH
#
# Configured through the environment because `iex` runs the script text and has
# nowhere to put arguments.

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

$architecture = Get-Architecture

$version = $env:LEAFLOW_VERSION
if (-not $version) { $version = Get-LatestVersion }

$installDir = $env:LEAFLOW_INSTALL_DIR
if (-not $installDir) { $installDir = Join-Path $env:LOCALAPPDATA 'Programs\leaflow' }

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

    # Printed, not written. PowerShell scans no directory for completion, so the
    # only place it can go is the profile, and that runs on every shell start.
    Write-Step ''
    Write-Step 'For tab completion, add to $PROFILE:'
    Write-Step '  leaflow completion powershell | Out-String | Invoke-Expression'
} finally {
    Remove-Item -Path $work -Recurse -Force -ErrorAction SilentlyContinue
}
