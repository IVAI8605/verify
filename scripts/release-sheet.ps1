<#
.SYNOPSIS
    从当前工作区与 CHANGELOG 起草本版「验证单」骨架。

.DESCRIPTION
    这个脚本只做一件事：把「本次改了什么」从命令里抓出来，写成一张空表，放到 cache/验证单/。
    它不判断改得对不对，也不替你填证据。目的是让「本次必须验哪些」在开工前就固定下来，
    避免真机冒烟时凭记忆挑几项验、漏掉其中一条。

    为什么用 PowerShell 而不是 bash：这台机器 Git Bash 起不来，bash 脚本等于摆设。

    数据来源：
      1. 版本号   —— internal/web/server.go 的 const Version
      2. 改动条目 —— CHANGELOG.md 中该版本段落下的「- **标题**：说明」行
      3. 改动文件 —— git diff HEAD（已跟踪）+ git ls-files --others（未跟踪）
      4. 模板     —— scripts/templates/验证单.md

.PARAMETER Version
    指定版本号。不传则从 internal/web/server.go 读取。

.PARAMETER OutFile
    输出路径。默认为 cache/验证单/v<版本>.md（/cache/ 已被 .gitignore 忽略，不进仓库）。

.PARAMETER Force
    覆盖已存在的验证单。默认拒绝覆盖，防止把已经填好的证据冲掉。

.EXAMPLE
    pwsh -NoProfile -File scripts/release-sheet.ps1

    按当前工作区版本起草，输出到 cache/验证单/v3.10.4.md

.EXAMPLE
    pwsh -NoProfile -File scripts/release-sheet.ps1 -Force

    已存在时强制重写。只在确认旧单还没填证据时用。
#>
[CmdletBinding()]
param(
    [string]$Version = "",
    [string]$OutFile = "",
    [switch]$Force
)

$ErrorActionPreference = "Stop"
# 中文输出在 PowerShell 5 的控制台里默认按 GBK 解码会乱码，这里显式指定 UTF-8
$OutputEncoding = [System.Text.Encoding]::UTF8
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }

# 脚本放在 scripts/ 下，仓库根目录是它的上一级
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $root

$utf8 = New-Object System.Text.UTF8Encoding($false)

function Read-Utf8([string]$Path) {
    [System.IO.File]::ReadAllText($Path, [System.Text.Encoding]::UTF8)
}

function Invoke-Git([string]$GitArgs) {
    # 只取 stdout；git 的 stderr 在 ErrorActionPreference=Stop 下会被当成错误抛出
    # quotepath=false：否则中文路径会被输出成 "\351\252\214..." 这种八进制转义
    $out = & git -c "core.quotepath=false" $GitArgs.Split(" ") 2>$null
    if ($LASTEXITCODE -ne 0) { throw "git $GitArgs 执行失败" }
    return @($out | Where-Object { $_ -ne "" })
}

# ---------- 1. 版本号 ----------
if (-not $Version) {
    $serverGo = Join-Path $root "internal/web/server.go"
    if (-not (Test-Path $serverGo)) { throw "找不到 internal/web/server.go" }
    $m = [regex]::Match((Read-Utf8 $serverGo), 'const Version = "([^"]+)"')
    if (-not $m.Success) { throw "server.go 里没有找到 const Version" }
    $Version = $m.Groups[1].Value
}

# ---------- 2. CHANGELOG 顶部段落 ----------
$changelogPath = Join-Path $root "CHANGELOG.md"
if (-not (Test-Path $changelogPath)) { throw "找不到 CHANGELOG.md" }
$lines = [System.IO.File]::ReadAllLines($changelogPath, [System.Text.Encoding]::UTF8)

$start = -1
for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match ("^## v" + [regex]::Escape($Version) + "(\s|（|\(|$)")) { $start = $i; break }
}
if ($start -lt 0) { throw "CHANGELOG.md 里没有 v$Version 的段落，请先补 CHANGELOG" }

$end = $lines.Count
for ($i = $start + 1; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^## v') { $end = $i; break }
}

$items = @()
$group = ""
foreach ($line in $lines[$start..($end - 1)]) {
    if ($line -match '^### (.+)$') { $group = $Matches[1].Trim() }
    elseif ($line -match '^- \*\*(.+?)\*\*') {
        $items += [pscustomobject]@{ Group = $group; Title = $Matches[1].Trim() }
    }
}
if ($items.Count -eq 0) {
    throw "v$Version 段落里没解析到条目。脚本只认「- **标题**：说明」这种格式，格式变了要同步改脚本"
}

# ---------- 3. 改动文件 ----------
$tracked = Invoke-Git "diff HEAD --name-only"
$untracked = Invoke-Git "ls-files --others --exclude-standard"
$files = @($tracked + $untracked | Select-Object -Unique | Sort-Object)

# ---------- 4. 生成骨架 ----------
$rows = New-Object System.Text.StringBuilder
$n = 0
foreach ($it in $items) {
    $n++
    $src = if ($it.Group) { "CHANGELOG v$Version · $($it.Group)" } else { "CHANGELOG v$Version" }
    # v3.10.10（P2-13）：标题里的 | 会把 Markdown 表格列撑错位，写入前转义成 \|
    $title = $it.Title -replace '\|', '\|'
    [void]$rows.AppendLine("| V$n | $title | $src |  | 离线/真机 |  |  |")
}

$fileBlock = if ($files.Count -eq 0) { "- （工作区没有改动）" } else { ($files | ForEach-Object { "- $_" }) -join "`r`n" }

$tplPath = Join-Path $root "scripts/templates/验证单.md"
if (-not (Test-Path $tplPath)) { throw "找不到模板 scripts/templates/验证单.md" }

$text = Read-Utf8 $tplPath
$text = $text.Replace("{{VERSION}}", $Version)
$text = $text.Replace("{{GENERATED_AT}}", (Get-Date).ToString("yyyy-MM-dd HH:mm"))
$text = $text.Replace("{{CHANGELOG_COUNT}}", [string]$items.Count)
$text = $text.Replace("{{VALIDATION_ROWS}}", $rows.ToString().TrimEnd())
$text = $text.Replace("{{CHANGED_FILES}}", $fileBlock)

# ---------- 5. 写文件 ----------
if (-not $OutFile) { $OutFile = Join-Path $root ("cache/验证单/v" + $Version + ".md") }
elseif (-not [System.IO.Path]::IsPathRooted($OutFile)) { $OutFile = Join-Path $root $OutFile }

$dir = Split-Path -Parent $OutFile
if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }

if ((Test-Path $OutFile) -and -not $Force) {
    throw "验证单已存在：$OutFile。已填的证据不能被冲掉；确需重写请加 -Force"
}

[System.IO.File]::WriteAllText($OutFile, $text, $utf8)

# ---------- 6. 回报 ----------
Write-Host ""
Write-Host "[验证单已生成] $OutFile"
Write-Host "  版本        : $Version"
Write-Host "  CHANGELOG   : $($items.Count) 条"
Write-Host "  验证表骨架  : $($items.Count) 行（合并同类项可以，但不能少于条目数）"
Write-Host "  改动文件    : $($files.Count) 个"
Write-Host ""
Write-Host "接下来脚本不会替你做的三件事："
Write-Host "  1. 给每行写一个可观测断言（能贴出命令输出的那种，不要写「正常」）"
Write-Host "  2. 执行后把「命令 + 一行输出」填进证据列"
Write-Host "  3. 发版前过关门三问：行数 >= $($items.Count) / 证据列无空 / 未验证项清空或全 N-A"
Write-Host ""
