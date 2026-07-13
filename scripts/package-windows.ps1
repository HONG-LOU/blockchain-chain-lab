param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$')]
    [string]$Version,
    [string]$OutputRoot = (Join-Path $PSScriptRoot '..\dist')
)

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$commit = (git -C $repo rev-parse HEAD).Trim()
$buildTime = [DateTime]::UtcNow.ToString('o')
$packageName = "chainlab-$Version-windows-amd64"
$packageDir = Join-Path $OutputRoot $packageName
$archive = Join-Path $OutputRoot "$packageName.zip"
$checksums = Join-Path $OutputRoot "$packageName-SHA256SUMS.txt"

if ((Test-Path -LiteralPath $packageDir) -or (Test-Path -LiteralPath $archive) -or (Test-Path -LiteralPath $checksums)) {
    throw "Release output already exists for $packageName"
}
New-Item -ItemType Directory -Path $packageDir -Force | Out-Null

$ldflags = "-s -w -X chainlab/internal/buildinfo.Version=$Version -X chainlab/internal/buildinfo.Commit=$commit -X chainlab/internal/buildinfo.BuildTime=$buildTime"
go build -trimpath -ldflags $ldflags -o (Join-Path $packageDir 'chainlab.exe') ./cmd/chainlab
Copy-Item (Join-Path $repo 'README.md') $packageDir
Copy-Item (Join-Path $repo 'README.zh-CN.md') $packageDir
Copy-Item (Join-Path $repo 'docs\desktop-quick-start.md') (Join-Path $packageDir 'QUICK-START.md')

$versionInfo = & (Join-Path $packageDir 'chainlab.exe') version | ConvertFrom-Json
if ($versionInfo.version -ne $Version -or $versionInfo.commit -ne $commit -or $versionInfo.os -ne 'windows' -or $versionInfo.arch -ne 'amd64') {
    throw 'Packaged version metadata did not verify'
}

Compress-Archive -Path (Join-Path $packageDir '*') -DestinationPath $archive -CompressionLevel Optimal
$lines = @(
    "{0}  {1}" -f (Get-FileHash -Algorithm SHA256 (Join-Path $packageDir 'chainlab.exe')).Hash.ToLowerInvariant(), "$packageName/chainlab.exe"
    "{0}  {1}" -f (Get-FileHash -Algorithm SHA256 $archive).Hash.ToLowerInvariant(), (Split-Path $archive -Leaf)
)
[IO.File]::WriteAllLines($checksums, $lines, [Text.UTF8Encoding]::new($false))

[pscustomobject]@{
    Version = $Version
    Commit = $commit
    Package = $packageDir
    Archive = $archive
    Checksums = $checksums
}
