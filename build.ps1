# Builds collab-server for Windows, Linux and macOS into dist/.
# Uses a local Go toolchain when available, otherwise the golang Docker image.
# Reads EditorCollaboration.uplugin (a symlink to the plugin descriptor).
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$descriptorPath = Join-Path $PSScriptRoot "EditorCollaboration.uplugin"
$shortcutPath = Join-Path $PSScriptRoot "EditorCollaboration.uplugin.lnk"
if (-not (Test-Path -LiteralPath $descriptorPath) -and (Test-Path -LiteralPath $shortcutPath)) {
    $shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcutPath)
    if ([string]::IsNullOrWhiteSpace($shortcut.TargetPath) -or -not (Test-Path -LiteralPath $shortcut.TargetPath)) {
        throw "EditorCollaboration.uplugin.lnk points at $($shortcut.TargetPath), which does not exist."
    }
    $descriptorPath = $shortcut.TargetPath
}
if (-not (Test-Path -LiteralPath $descriptorPath)) {
    throw "EditorCollaboration.uplugin does not resolve. Build from the Unreal tree, where this file links to ../EditorCollaboration.uplugin."
}
$descriptorText = Get-Content -Raw -LiteralPath $descriptorPath
$trimmed = $descriptorText.Trim()
if (-not $trimmed.StartsWith("{")) {
    $linked = $trimmed.Split("`n")[0].Trim()
    if (-not [System.IO.Path]::IsPathRooted($linked)) {
        $linked = Join-Path $PSScriptRoot $linked
    }
    if (-not (Test-Path -LiteralPath $linked)) {
        throw "EditorCollaboration.uplugin points at $linked, which does not exist."
    }
    $descriptorText = Get-Content -Raw -LiteralPath $linked
}
$descriptor = $descriptorText | ConvertFrom-Json
$pluginVersion = [string]$descriptor.VersionName
$pluginMarket = [string]$descriptor.MarketplaceURL
if ([string]::IsNullOrWhiteSpace($pluginVersion)) {
    throw "VersionName is missing in EditorCollaboration.uplugin."
}
$ldflags = "-s -w -X main.pluginVersion=$pluginVersion -X main.pluginMarketplaceURL=$pluginMarket"

$targets = @(
    @{ OS = "windows"; Arch = "amd64"; Ext = ".exe" },
    @{ OS = "linux";   Arch = "amd64"; Ext = "" },
    @{ OS = "linux";   Arch = "arm64"; Ext = "" },
    @{ OS = "darwin";  Arch = "amd64"; Ext = "" },
    @{ OS = "darwin";  Arch = "arm64"; Ext = "" }
)

New-Item -ItemType Directory -Force -Path dist | Out-Null

if (Get-Command go -ErrorAction SilentlyContinue) {
    $env:CGO_ENABLED = "0"
    foreach ($t in $targets) {
        $env:GOOS = $t.OS
        $env:GOARCH = $t.Arch
        $out = "dist/collab-server-$($t.OS)-$($t.Arch)$($t.Ext)"
        Write-Host "Building $out"
        go build -trimpath -ldflags $ldflags -o $out .
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $out" }
    }
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}
elseif (Get-Command docker -ErrorAction SilentlyContinue) {
    $script = ($targets | ForEach-Object {
        "echo Building dist/collab-server-$($_.OS)-$($_.Arch)$($_.Ext) && GOOS=$($_.OS) GOARCH=$($_.Arch) go build -trimpath -ldflags='$ldflags' -o dist/collab-server-$($_.OS)-$($_.Arch)$($_.Ext) ."
    }) -join " && "
    docker run --rm -v "${PSScriptRoot}:/src" -w /src -e CGO_ENABLED=0 golang:1.27-alpine sh -c $script
    if ($LASTEXITCODE -ne 0) { throw "docker build failed (is Docker Desktop running?)" }
}
else {
    throw "Neither Go (https://go.dev/dl/) nor Docker is installed."
}

$pluginJson = '{"version":"' + $pluginVersion + '","marketplaceUrl":"' + $pluginMarket + '"}'
Set-Content -Path (Join-Path $PSScriptRoot "dist/plugin.json") -Value $pluginJson -Encoding ascii -NoNewline
Add-Content -Path (Join-Path $PSScriptRoot "dist/plugin.json") -Value "" -Encoding ascii

$lines = Get-ChildItem (Join-Path $PSScriptRoot "dist") -File | Where-Object { $_.Name -ne "SHA256SUMS" } | ForEach-Object {
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLower()
    "{0}  {1}" -f $hash, $_.Name
}
Set-Content -Path (Join-Path $PSScriptRoot "dist/SHA256SUMS") -Value $lines -Encoding ascii

Write-Host "Done. Binaries are in $PSScriptRoot\dist"
