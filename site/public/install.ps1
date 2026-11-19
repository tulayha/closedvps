# Installs closedvps from its GitHub release:  irm https://closedvps.com/install.ps1 | iex
# $env:CLOSEDVPS_VERSION = "v1.2.3" picks a release (default: latest). The download is checked against the
# release's checksums.txt. Installs to %LOCALAPPDATA%\Programs\closedvps and adds it to your user PATH.
& {
    $ErrorActionPreference = 'Stop'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    $repo = 'tulayha/closedvps'
    $arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
    $base = if ($env:CLOSEDVPS_DOWNLOAD_BASE) { $env:CLOSEDVPS_DOWNLOAD_BASE } # CI tests the script against a local build
            elseif ($env:CLOSEDVPS_VERSION) { "https://github.com/$repo/releases/download/$env:CLOSEDVPS_VERSION" }
            else { "https://github.com/$repo/releases/latest/download" }
    $file = "closedvps_windows_$arch.zip"
    $dir = Join-Path $env:LOCALAPPDATA 'Programs\closedvps'
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("closedvps-" + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "Downloading $file"
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$file" -OutFile (Join-Path $tmp $file)
        Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

        $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match " $([regex]::Escape($file))$" }
        if (-not $line) { throw "$file is not listed in checksums.txt" }
        $want = ($line -split ' ')[0]
        $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $file)).Hash.ToLower()
        if ($got -ne $want) { throw "checksum mismatch for $file; not installing" }

        Expand-Archive -Path (Join-Path $tmp $file) -DestinationPath $tmp -Force
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        Copy-Item (Join-Path $tmp 'closedvps.exe') (Join-Path $dir 'closedvps.exe') -Force
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $dir) {
        [Environment]::SetEnvironmentVariable('Path', (($userPath, $dir) -join ';').Trim(';'), 'User')
    }
    if (($env:Path -split ';') -notcontains $dir) { $env:Path = "$env:Path;$dir" }
    Write-Host "Installed $(& (Join-Path $dir 'closedvps.exe') version) to $dir"
    Write-Host 'Next: closedvps create'
}
