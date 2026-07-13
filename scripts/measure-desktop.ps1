param(
    [ValidateSet('solo', 'four-validator')]
    [string]$Mode = 'solo',
    [ValidateRange(10, 86400)]
    [int]$DurationSeconds = 60,
    [string]$Executable = (Join-Path $PSScriptRoot '..\dist\chainlab-v0.1.0-dev-windows-amd64\chainlab.exe'),
    [string]$OutputRoot = (Join-Path $PSScriptRoot '..\output\desktop-measurements')
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true
$executablePath = (Resolve-Path -LiteralPath $Executable).Path
$executableVersion = & $executablePath version | ConvertFrom-Json
$executableSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $executablePath).Hash.ToLowerInvariant()
$harnessPath = (Resolve-Path -LiteralPath $PSCommandPath).Path
$harnessSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $harnessPath).Hash.ToLowerInvariant()
$runId = '{0}-{1}-{2}' -f (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ'), $Mode, [guid]::NewGuid().ToString('N').Substring(0, 8)
$artifactDir = Join-Path $OutputRoot $runId
$ownedDataRoot = Join-Path $env:TEMP "chainlab-measure-$runId"
$chainID = "chainlab-measure-$($runId.Substring($runId.Length - 8))"
New-Item -ItemType Directory -Path $artifactDir -Force | Out-Null
New-Item -ItemType Directory -Path $ownedDataRoot -Force | Out-Null

function Get-DirectoryBytes([string]$Path) {
    $sum = (Get-ChildItem -LiteralPath $Path -File -Recurse -Force -ErrorAction SilentlyContinue | Measure-Object -Property Length -Sum).Sum
    if ($null -eq $sum) { return [int64]0 }
    return [int64]$sum
}

function Get-AdapterTotals {
    $stats = Get-NetAdapterStatistics -ErrorAction SilentlyContinue
    return [pscustomobject]@{
        ReceivedBytes = [int64](($stats | Measure-Object -Property ReceivedBytes -Sum).Sum)
        SentBytes = [int64](($stats | Measure-Object -Property SentBytes -Sum).Sum)
    }
}

function Find-PortBlock([int]$Count) {
    for ($attempt = 0; $attempt -lt 100; $attempt++) {
        $base = Get-Random -Minimum 30000 -Maximum (65000 - $Count)
        $listeners = @()
        try {
            for ($offset = 0; $offset -lt $Count; $offset++) {
                $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $base + $offset)
                $listener.Start()
                $listeners += $listener
            }
            return $base
        } catch {
        } finally {
            foreach ($listener in $listeners) { $listener.Stop() }
        }
    }
    throw 'Unable to find a free local port block'
}

function Wait-DesktopStatus([string]$DataDir, [int]$TimeoutSeconds = 60) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        try {
            $raw = & $executablePath desktop status --data-dir $DataDir 2>$null
            if ($LASTEXITCODE -eq 0) {
                $status = $raw | ConvertFrom-Json
                if ($status.status -eq 'running' -and $status.height -gt 0) { return $status }
            }
        } catch {
        }
        Start-Sleep -Milliseconds 200
    } while ((Get-Date) -lt $deadline)
    throw "Desktop did not become ready: $DataDir"
}

function Start-Desktop([string]$DataDir, [int]$Index) {
    $stdout = Join-Path $artifactDir "node-$Index.log"
    $stderr = Join-Path $artifactDir "node-$Index.err.log"
    return Start-Process -FilePath $executablePath -ArgumentList @('desktop', 'start', '--data-dir', $DataDir) -RedirectStandardOutput $stdout -RedirectStandardError $stderr -WindowStyle Hidden -PassThru
}

$dataDirs = @()
$processes = @()
$startupStarted = Get-Date
try {
    if ($Mode -eq 'solo') {
        $data = Join-Path $ownedDataRoot 'solo'
        & $executablePath desktop init --data-dir $data --chain-id $chainID | Out-Null
        $dataDirs = @($data)
    } else {
        $base = Find-PortBlock 30
        $identityFiles = @()
        for ($index = 0; $index -lt 4; $index++) {
            $data = Join-Path $ownedDataRoot "validator-$index"
            & $executablePath desktop identity --data-dir $data --role validator --name "validator-$index" --p2p-address "127.0.0.1:$($base + $index)" | Out-Null
            $dataDirs += $data
            $identityFiles += (Join-Path $data 'identity.json')
        }
        $invitation = Join-Path $ownedDataRoot 'invitation.json'
        $invitationArgs = @('desktop', 'invitation', '--out', $invitation, '--chain-id', $chainID, '--network-name', 'Measurement Network')
        foreach ($identity in $identityFiles) { $invitationArgs += @('--identity', $identity) }
        & $executablePath @invitationArgs | Out-Null
        for ($index = 0; $index -lt 4; $index++) {
            & $executablePath desktop join --data-dir $dataDirs[$index] --invitation $invitation --abci-port ($base + 5 + $index) --rpc-port ($base + 10 + $index) --control-port ($base + 15 + $index) --explorer-port ($base + 20 + $index) | Out-Null
        }
    }

    $desktopConfig = Get-Content -Raw -LiteralPath (Join-Path $dataDirs[0] 'desktop.json') | ConvertFrom-Json
    $blockIntervalSeconds = [uint64]$desktopConfig.contract.block_interval_seconds
    if ($blockIntervalSeconds -eq 0) { throw 'Desktop block interval must be positive' }
    $diskStart = Get-DirectoryBytes $ownedDataRoot
    $networkStart = Get-AdapterTotals
    for ($index = 0; $index -lt $dataDirs.Count; $index++) {
        $processes += Start-Desktop $dataDirs[$index] $index
    }
    $readyStatuses = @()
    foreach ($data in $dataDirs) { $readyStatuses += Wait-DesktopStatus $data }
    $startupSeconds = ((Get-Date) - $startupStarted).TotalSeconds

    $cpuStart = @{}
    foreach ($process in $processes) {
        $current = Get-Process -Id $process.Id
        $cpuStart[$process.Id] = $current.TotalProcessorTime.TotalSeconds
    }
    $samples = [Collections.Generic.List[object]]::new()
    $sampleDeadline = (Get-Date).AddSeconds($DurationSeconds)
    do {
        foreach ($process in $processes) {
            $current = Get-Process -Id $process.Id -ErrorAction Stop
            $children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($process.Id)" -ErrorAction SilentlyContinue)
            $managedChildCount = @($children | Where-Object Name -ne 'conhost.exe').Count
            $consoleHostCount = @($children | Where-Object Name -eq 'conhost.exe').Count
            [void]$samples.Add([pscustomobject]@{
                Timestamp = (Get-Date).ToUniversalTime().ToString('o')
                PID = $process.Id
                WorkingSetBytes = [int64]$current.WorkingSet64
                PeakWorkingSetBytes = [int64]$current.PeakWorkingSet64
                CPUSeconds = $current.TotalProcessorTime.TotalSeconds
                ManagedChildProcesses = $managedChildCount
                ConsoleHostProcesses = $consoleHostCount
            })
        }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $sampleDeadline)

    $endStatuses = @()
    foreach ($data in $dataDirs) { $endStatuses += Wait-DesktopStatus $data 5 }
    $networkEnd = Get-AdapterTotals
    $diskEnd = Get-DirectoryBytes $ownedDataRoot
    $listeningBeforeStop = @(Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $processes.Id -contains $_.OwningProcess } | Select-Object LocalAddress, LocalPort, OwningProcess)
    $cpuEnd = @{}
    foreach ($process in $processes) { $cpuEnd[$process.Id] = (Get-Process -Id $process.Id).TotalProcessorTime.TotalSeconds }

    for ($index = 0; $index -lt $dataDirs.Count; $index++) {
        & $executablePath desktop stop --data-dir $dataDirs[$index] | Out-Null
    }
    $stopDeadline = (Get-Date).AddSeconds(20)
    do {
        $remaining = @($processes | Where-Object { Get-Process -Id $_.Id -ErrorAction SilentlyContinue })
        if ($remaining.Count -eq 0) { break }
        Start-Sleep -Milliseconds 200
    } while ((Get-Date) -lt $stopDeadline)

    $listeningAfterStop = @(Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $processes.Id -contains $_.OwningProcess })
    $logicalProcessors = (Get-CimInstance Win32_ComputerSystem).NumberOfLogicalProcessors
    $cpuDelta = 0.0
    foreach ($process in $processes) { $cpuDelta += $cpuEnd[$process.Id] - $cpuStart[$process.Id] }
    $steadySamples = @($samples | Select-Object -Skip ([math]::Min(10 * $processes.Count, $samples.Count)))
    if ($steadySamples.Count -eq 0) { $steadySamples = $samples }
    $machine = [pscustomobject]@{
        OS = Get-CimInstance Win32_OperatingSystem | Select-Object Caption, Version, OSArchitecture, @{N='TotalVisibleMemoryBytes';E={[int64]$_.TotalVisibleMemorySize * 1KB}}
        CPU = Get-CimInstance Win32_Processor | Select-Object Name, NumberOfCores, NumberOfLogicalProcessors, MaxClockSpeed
        Volume = Get-Volume -DriveLetter ([IO.Path]::GetPathRoot($ownedDataRoot).Substring(0,1)) | Select-Object DriveLetter, FileSystem, Size, SizeRemaining
        GoVersion = $executableVersion.go_version
    }
    $result = [pscustomobject]@{
        Protocol = 'chainlab-desktop-measurement-v1'
        RunID = $runId
        Mode = $Mode
        StartedAt = $startupStarted.ToUniversalTime().ToString('o')
        CompletedAt = (Get-Date).ToUniversalTime().ToString('o')
        DurationSeconds = $DurationSeconds
        SampleDelaySeconds = 1
        BlockIntervalSeconds = $blockIntervalSeconds
        Executable = [pscustomobject]@{
            Path = $executablePath
            SHA256 = $executableSHA256
            Version = $executableVersion
        }
        Harness = [pscustomobject]@{
            Path = $harnessPath
            SHA256 = $harnessSHA256
        }
        Machine = $machine
        DataRoot = $ownedDataRoot
        ArtifactDirectory = (Resolve-Path $artifactDir).Path
        StartupSeconds = [math]::Round($startupSeconds, 3)
        NodeCount = $processes.Count
        ProcessIDs = @($processes.Id)
        HeightStart = @($readyStatuses.height)
        HeightEnd = @($endStatuses.height)
        HeightDelta = @(for ($index = 0; $index -lt $endStatuses.Count; $index++) { [int64]$endStatuses[$index].height - [int64]$readyStatuses[$index].height })
        PeakRSSBytes = [int64](($samples | Measure-Object -Property PeakWorkingSetBytes -Maximum).Maximum)
        SteadyRSSAverageBytes = [int64](($steadySamples | Measure-Object -Property WorkingSetBytes -Average).Average)
        IdleCPUPercentOfMachine = [math]::Round(100 * $cpuDelta / ($DurationSeconds * $logicalProcessors), 3)
        DiskBytesStart = $diskStart
        DiskBytesEnd = $diskEnd
        DiskBytesGrowth = $diskEnd - $diskStart
        AdapterReceivedBytesDelta = $networkEnd.ReceivedBytes - $networkStart.ReceivedBytes
        AdapterSentBytesDelta = $networkEnd.SentBytes - $networkStart.SentBytes
        AdapterTrafficScope = 'system-wide adapter delta; not process-isolated'
        ListeningBeforeStop = $listeningBeforeStop
        MaximumManagedChildProcesses = [int](($samples | Measure-Object -Property ManagedChildProcesses -Maximum).Maximum)
        MaximumConsoleHostProcesses = [int](($samples | Measure-Object -Property ConsoleHostProcesses -Maximum).Maximum)
        CleanShutdown = [pscustomobject]@{
            ProcessesExited = @($processes | Where-Object { Get-Process -Id $_.Id -ErrorAction SilentlyContinue }).Count -eq 0
            ManagedListenersRemaining = $listeningAfterStop.Count
            RuntimeFilesRemaining = @($dataDirs | Where-Object { Test-Path (Join-Path $_ 'runtime.json') }).Count
        }
        Samples = $samples
    }
    $artifact = Join-Path $artifactDir 'measurement.json'
    [IO.File]::WriteAllText($artifact, ($result | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
    $result | Select-Object RunID, Mode, DurationSeconds, StartupSeconds, NodeCount, HeightDelta, PeakRSSBytes, SteadyRSSAverageBytes, IdleCPUPercentOfMachine, DiskBytesGrowth, CleanShutdown, ArtifactDirectory
} finally {
    foreach ($process in $processes) {
        if (Get-Process -Id $process.Id -ErrorAction SilentlyContinue) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
    }
    $resolvedOwned = [IO.Path]::GetFullPath($ownedDataRoot)
    $tempRoot = [IO.Path]::GetFullPath($env:TEMP)
    if ($resolvedOwned.StartsWith($tempRoot + [IO.Path]::DirectorySeparatorChar) -and (Split-Path $resolvedOwned -Leaf).StartsWith('chainlab-measure-')) {
        Remove-Item -LiteralPath $resolvedOwned -Recurse -Force -ErrorAction SilentlyContinue
    }
}
