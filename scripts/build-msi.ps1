# Builds the Windows installer for one architecture with WiX Toolset v5
# (dotnet tool install --global wix --version 5.0.2).
#
#   pwsh scripts/build-msi.ps1 <audd.exe> <amd64|arm64> <version> <out.msi>
#
# <version> is the release version without the leading v; a prerelease
# suffix (1.2.0-rc.1) is dropped, since MSI versions are numbers only.
param(
    [Parameter(Mandatory)] [string] $Exe,
    [Parameter(Mandatory)] [ValidateSet('amd64', 'arm64')] [string] $Arch,
    [Parameter(Mandatory)] [string] $Version,
    [Parameter(Mandatory)] [string] $Out
)
$ErrorActionPreference = 'Stop'

$msiVersion = ($Version -split '-')[0]
if ($msiVersion -notmatch '^\d+\.\d+\.\d+$') {
    throw "version '$Version' is not X.Y.Z"
}
$wixArch = @{ amd64 = 'x64'; arm64 = 'arm64' }[$Arch]
$wxs = Join-Path $PSScriptRoot '..\packaging\windows\audd.wxs'

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Out) | Out-Null
wix build $wxs -arch $wixArch -d "Version=$msiVersion" -d "Exe=$((Resolve-Path $Exe).Path)" -o $Out
if ($LASTEXITCODE -ne 0) { throw "wix build failed with exit code $LASTEXITCODE" }
Remove-Item -ErrorAction SilentlyContinue ([IO.Path]::ChangeExtension($Out, '.wixpdb'))
$sum = (Get-FileHash -Algorithm SHA256 $Out).Hash.ToLower()
Write-Output "$Arch $sum"
