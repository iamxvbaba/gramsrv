[CmdletBinding(SupportsShouldProcess)]
param(
    [string]$Root = (Join-Path $PSScriptRoot "..\data\langpack"),
    [Parameter(Mandatory = $true)]
    [string]$VersionsCsv
)

# Rename local language-pack files to the versions exported by
# scripts/db/get_langpack_versions.sql. No hard-coded workstation path or
# overwrite is used; -WhatIf can be supplied for a dry run.
$rootPath = (Resolve-Path -LiteralPath $Root -ErrorAction Stop).Path
$rows = Import-Csv -LiteralPath $VersionsCsv
$targets = @{}
foreach ($row in $rows) {
    $key = "{0}|{1}" -f $row.lang_pack, $row.lang_code
    $targets[$key] = [int64]$row.version
}

$pattern = '^(?<pack>.+)_(?<code>[^_]+)_v(?<version>\d+)(?<suffix>\.strings)$'
Get-ChildItem -LiteralPath $rootPath -Recurse -File -Filter '*.strings' | ForEach-Object {
    if ($_.Name -notmatch $pattern) { return }
    $key = "{0}|{1}" -f $Matches.pack, $Matches.code
    if (-not $targets.ContainsKey($key)) { return }
    $current = [int64]$Matches.version
    $target = $targets[$key]
    if ($target -le $current) { return }

    $newName = '{0}_{1}_v{2}{3}' -f $Matches.pack, $Matches.code, $target, $Matches.suffix
    $destination = Join-Path $_.DirectoryName $newName
    if (Test-Path -LiteralPath $destination) {
        throw "Refusing to overwrite existing language pack: $destination"
    }
    if ($PSCmdlet.ShouldProcess($_.FullName, "Rename to $newName")) {
        Rename-Item -LiteralPath $_.FullName -NewName $newName
    }
}
