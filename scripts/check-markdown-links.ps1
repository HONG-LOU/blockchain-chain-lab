param(
    [string]$Repository = (Join-Path $PSScriptRoot '..')
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath $Repository).Path
$files = @(
    Get-Item (Join-Path $root 'README.md'), (Join-Path $root 'README.zh-CN.md')
    Get-ChildItem (Join-Path $root 'docs') -Filter '*.md' -File -Recurse
)
$failures = @()
$pattern = '!?(?:\[[^\]]*\])\((?<target>[^)]+)\)'

foreach ($file in $files) {
    $content = Get-Content -Raw -LiteralPath $file.FullName
    foreach ($match in [regex]::Matches($content, $pattern)) {
        $target = $match.Groups['target'].Value.Trim()
        if ($target.StartsWith('<') -and $target.EndsWith('>')) { $target = $target.Substring(1, $target.Length - 2) }
        if ($target -match '^(?:https?://|mailto:|#)') { continue }
        $pathPart = [Uri]::UnescapeDataString(($target -split '#', 2)[0])
        if ([string]::IsNullOrWhiteSpace($pathPart)) { continue }
        $resolved = [IO.Path]::GetFullPath((Join-Path $file.DirectoryName $pathPart))
        if (-not $resolved.StartsWith($root + [IO.Path]::DirectorySeparatorChar) -and $resolved -ne $root) {
            $failures += "$($file.FullName): link escapes repository: $target"
        } elseif (-not (Test-Path -LiteralPath $resolved)) {
            $failures += "$($file.FullName): missing local link: $target"
        }
    }
}

if ($failures.Count -gt 0) {
    $failures | ForEach-Object { Write-Error $_ }
    exit 1
}
Write-Output "Validated local Markdown links in $($files.Count) files."
