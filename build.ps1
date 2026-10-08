# Builds collab-server for Windows, Linux and macOS into dist/.
# Uses a local Go toolchain when available, otherwise the golang Docker image.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

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
        go build -trimpath -ldflags="-s -w" -o $out .
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $out" }
    }
    Remove-Item Env:GOOS, Env:GOARCH
}
elseif (Get-Command docker -ErrorAction SilentlyContinue) {
    $script = ($targets | ForEach-Object {
        "echo Building dist/collab-server-$($_.OS)-$($_.Arch)$($_.Ext) && GOOS=$($_.OS) GOARCH=$($_.Arch) go build -trimpath -ldflags='-s -w' -o dist/collab-server-$($_.OS)-$($_.Arch)$($_.Ext) ."
    }) -join " && "
    docker run --rm -v "${PSScriptRoot}:/src" -w /src -e CGO_ENABLED=0 golang:1.27-alpine sh -c $script
    if ($LASTEXITCODE -ne 0) { throw "docker build failed (is Docker Desktop running?)" }
}
else {
    throw "Neither Go (https://go.dev/dl/) nor Docker is installed."
}

Write-Host "Done. Binaries are in $PSScriptRoot\dist"
