[CmdletBinding()]
param([string]$Target = '', [string]$Cargo = 'cargo')

$ErrorActionPreference = 'Stop'
$descriptor = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot '../EditorCollaboration.uplugin') | ConvertFrom-Json
$env:COLLAB_PLUGIN_VERSION = [string]$descriptor.VersionName
$env:COLLAB_PLUGIN_MARKETPLACE = [string]$descriptor.MarketplaceURL
if (-not $Target) {
    $Target = (& rustc -vV | Select-String '^host: ').Line.Substring(6)
}
if ($Target -notmatch '^(x86_64|aarch64)-(pc|unknown|apple)-(windows-msvc|linux-gnu|linux-musl|darwin)$') {
    throw "Unsupported release target $Target"
}
$arch = if ($Matches[1] -eq 'x86_64') { 'amd64' } else { 'arm64' }
$platform = switch -Wildcard ($Matches[3]) {
    'windows-*' { 'windows' }
    'linux-*' { 'linux' }
    'darwin' { 'darwin' }
}
$extension = if ($platform -eq 'windows') { '.exe' } else { '' }
$manifest = Join-Path $PSScriptRoot 'Cargo.toml'
$targetDir = if ($env:CARGO_TARGET_DIR) { $env:CARGO_TARGET_DIR } else { Join-Path $PSScriptRoot 'target' }
& $Cargo build --locked --release --manifest-path $manifest --target $Target --target-dir $targetDir
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$dist = Join-Path $PSScriptRoot 'dist'
New-Item -ItemType Directory -Path $dist -Force | Out-Null
$name = "collab-server-rust-$platform-$arch$extension"
Copy-Item -LiteralPath (Join-Path $targetDir "$Target/release/collab-server$extension") -Destination (Join-Path $dist $name)
$sums = Get-ChildItem -LiteralPath $dist -File -Filter 'collab-server-rust-*' |
    Where-Object { $_.Name -notlike '*.sha256' } |
    Sort-Object Name | ForEach-Object {
        $digest = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        $line = "$digest  $($_.Name)"
        $line | Set-Content -Encoding ascii -LiteralPath ($_.FullName + '.sha256')
        $line
    }
$sums | Set-Content -Encoding ascii -LiteralPath (Join-Path $dist 'SHA256SUMS')
Write-Output (Join-Path $dist $name)
