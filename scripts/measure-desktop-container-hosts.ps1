param(
    [ValidateRange(10, 3600)]
    [int]$DurationSeconds = 30,
    [string]$Executable = (Join-Path $PSScriptRoot '..\dist\chainlab-v0.1.0-rc1-linux-amd64\chainlab'),
    [string]$Image = 'postgres:16',
    [string]$OutputRoot = (Join-Path $PSScriptRoot '..\output\desktop-measurements')
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$executablePath = (Resolve-Path -LiteralPath $Executable).Path
$runID = '{0}-container-hosts-{1}' -f (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ'), [guid]::NewGuid().ToString('N').Substring(0, 8)
$suffix = $runID.Substring($runID.Length - 8).ToLowerInvariant()
$artifactDir = Join-Path $OutputRoot $runID
$networkName = "chainlab-$suffix"
$label = "chainlab.measurement.run=$runID"
$binaryMount = "type=bind,source=$executablePath,target=/usr/local/bin/chainlab,readonly"
$evidenceMount = "type=bind,source=$artifactDir,target=/evidence"
$containerNames = @()
$volumeNames = @()
$cleanup = $null
$result = $null
New-Item -ItemType Directory -Path $artifactDir -Force | Out-Null

function Invoke-Docker([string[]]$Arguments) {
    $output = & docker @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
    return $output
}

function Test-Docker([string[]]$Arguments) {
    & docker @Arguments *> $null
    return $LASTEXITCODE -eq 0
}

function Invoke-Node([string]$Name, [string[]]$Arguments) {
    return Invoke-Docker (@('exec', $Name, '/usr/local/bin/chainlab') + $Arguments)
}

function Get-NodeStatus([string]$Name) {
    $raw = Invoke-Node $Name @('desktop', 'status', '--data-dir', '/data/node')
    return ($raw -join "`n") | ConvertFrom-Json
}

function Wait-NodeStatus([string]$Name, [int]$TimeoutSeconds = 60) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        try {
            $status = Get-NodeStatus $Name
            if ($status.status -eq 'running' -and [int64]$status.height -gt 0) {
                return $status
            }
        } catch {
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "desktop node did not become ready: $Name"
}

function Get-NodeHTTP([string]$Name, [string]$Path, [int]$Port) {
    $script = "exec 3<>/dev/tcp/127.0.0.1/$Port; printf 'GET $Path HTTP/1.0\r\nHost: localhost\r\nConnection: close\r\n\r\n' >&3; cat <&3"
    $response = (Invoke-Docker @('exec', $Name, 'bash', '-c', $script)) -join "`n"
    if ($response -notmatch 'HTTP/1\.[01] 200') {
        throw "HTTP request failed in ${Name}: $Path"
    }
    $jsonStart = $response.IndexOf('{')
    $htmlStart = $response.IndexOf('<')
    $bodyCandidates = @($jsonStart, $htmlStart) | Where-Object { $_ -ge 0 }
    if ($bodyCandidates.Count -eq 0) { throw "HTTP response has no JSON or HTML body in ${Name}: $Path" }
    $bodyStart = ($bodyCandidates | Measure-Object -Minimum).Minimum
    return $response.Substring([int]$bodyStart)
}

function Get-CometRPC([string]$Name, [string]$Path) {
    return (Get-NodeHTTP $Name $Path 26670) | ConvertFrom-Json
}

function Wait-PeerMesh([string[]]$Names, [int]$ExpectedPeers, [int]$TimeoutSeconds = 45) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $counts = @()
        $ready = $true
        foreach ($name in $Names) {
            try {
                $count = [int](Get-CometRPC $name '/net_info').result.n_peers
                $counts += $count
                if ($count -ne $ExpectedPeers) { $ready = $false }
            } catch {
                $ready = $false
            }
        }
        if ($ready) { return $counts }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    throw "peer mesh did not reach $ExpectedPeers peers per node; last counts: $($counts -join ',')"
}

function Wait-CommonState([string[]]$Names, [int]$TimeoutSeconds = 45) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $statuses = @()
        try {
            foreach ($name in $Names) { $statuses += Get-NodeStatus $name }
            $first = $statuses[0]
            $same = @($statuses | Where-Object {
                $_.height -eq $first.height -and $_.block_hash -eq $first.block_hash -and $_.app_hash -eq $first.app_hash
            }).Count -eq $statuses.Count
            if ($same) { return $statuses }
        } catch {
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "nodes did not converge to one directly observed height/block/app state: $($Names -join ',')"
}

function Get-Account([string]$Name, [string]$Address) {
    $raw = Invoke-Node $Name @('desktop', 'account', '--data-dir', '/data/node', '--address', $Address)
    return ($raw -join "`n") | ConvertFrom-Json
}

function Wait-AccountBalance([string[]]$Names, [string]$Address, [uint64]$Balance, [int]$TimeoutSeconds = 45) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $accounts = @()
        $ready = $true
        foreach ($name in $Names) {
            try {
                $account = Get-Account $name $Address
                $accounts += $account
                if ([uint64]$account.balance -ne $Balance) { $ready = $false }
            } catch {
                $ready = $false
            }
        }
        if ($ready) { return $accounts }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "account $Address did not converge to balance $Balance on $($Names -join ',')"
}

function Get-ContainerValue([string]$Name, [string]$Path) {
    return [int64]((Invoke-Docker @('exec', $Name, 'cat', $Path)) -join '').Trim()
}

function Add-IPv4Offset([string]$Address, [uint32]$Offset) {
    $bytes = [Net.IPAddress]::Parse($Address).GetAddressBytes()
    if ($bytes.Count -ne 4) { throw "Docker measurement network is not IPv4: $Address" }
    [array]::Reverse($bytes)
    $value = [BitConverter]::ToUInt32($bytes, 0) + $Offset
    $result = [BitConverter]::GetBytes($value)
    [array]::Reverse($result)
    return [Net.IPAddress]::new($result).ToString()
}

function Get-ContainerDiskBytes([string]$Name) {
    $raw = (Invoke-Docker @('exec', $Name, 'du', '-sb', '/data/node')) -join ''
    return [int64](($raw -split '\s+')[0])
}

function Get-CPUUsageMicroseconds([string]$Name) {
    $raw = (Invoke-Docker @('exec', $Name, 'cat', '/sys/fs/cgroup/cpu.stat')) -join "`n"
    $line = @($raw -split "`n" | Where-Object { $_ -match '^usage_usec\s+' })[0]
    return [int64](($line -split '\s+')[1])
}

function Get-NamespaceEvidence([string]$Name) {
    $inspect = ((Invoke-Docker @('inspect', $Name)) -join "`n" | ConvertFrom-Json)[0]
    return [pscustomobject]@{
        Name = $Name
        ContainerID = $inspect.Id
        IPAddress = $inspect.NetworkSettings.Networks.$networkName.IPAddress
        NetworkNamespaceInode = [int64](((Invoke-Docker @('exec', $Name, 'stat', '-Lc', '%i', '/proc/1/ns/net')) -join '').Trim())
        MountNamespaceInode = [int64](((Invoke-Docker @('exec', $Name, 'stat', '-Lc', '%i', '/proc/1/ns/mnt')) -join '').Trim())
        PIDNamespaceInode = [int64](((Invoke-Docker @('exec', $Name, 'stat', '-Lc', '%i', '/proc/1/ns/pid')) -join '').Trim())
        NanoCPUs = [int64]$inspect.HostConfig.NanoCpus
        MemoryLimitBytes = [int64]$inspect.HostConfig.Memory
        PidsLimit = [int64]$inspect.HostConfig.PidsLimit
        ReadonlyRootfs = [bool]$inspect.HostConfig.ReadonlyRootfs
        DataVolume = [string]@($inspect.Mounts | Where-Object Destination -eq '/data')[0].Name
    }
}

function Stop-Node([string]$Name) {
    Invoke-Node $Name @('desktop', 'stop', '--data-dir', '/data/node') | Out-Null
    $exitCode = [int](((Invoke-Docker @('wait', $Name)) -join '').Trim())
    if ($exitCode -ne 0) { throw "desktop container exited with code ${exitCode}: $Name" }
}

function Start-StoppedNode([string]$Name) {
    $started = Get-Date
    Invoke-Docker @('start', $Name) | Out-Null
    $status = Wait-NodeStatus $Name
    return [pscustomobject]@{
        Name = $Name
        Seconds = [math]::Round(((Get-Date) - $started).TotalSeconds, 3)
        Height = [int64]$status.height
    }
}

try {
    Invoke-Docker @('version', '--format', '{{.Server.Version}}') | Out-Null
    if (-not (Test-Docker @('image', 'inspect', $Image))) {
        throw "required local Docker image is unavailable: $Image"
    }
    $imageInspect = ((Invoke-Docker @('image', 'inspect', $Image)) -join "`n" | ConvertFrom-Json)[0]
    $version = ((Invoke-Docker @(
        'run', '--rm', '--cpus', '4', '--memory', '8g', '--pids-limit', '256', '--read-only',
        '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m', '--mount', $binaryMount,
        $Image, '/usr/local/bin/chainlab', 'version'
    )) -join "`n" | ConvertFrom-Json)
    if ($version.os -ne 'linux' -or $version.arch -ne 'amd64') {
        throw 'container-host evidence requires a Linux amd64 ChainLab artifact'
    }

    $networkCreated = $false
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        $candidateCIDR = '10.253.{0}.0/24' -f (Get-Random -Minimum 1 -Maximum 250)
        & docker network create --subnet $candidateCIDR --label $label $networkName *> $null
        if ($LASTEXITCODE -eq 0) {
            $networkCreated = $true
            break
        }
    }
    if (-not $networkCreated) { throw 'unable to create an isolated Docker measurement subnet' }
    $networkInspect = ((Invoke-Docker @('network', 'inspect', $networkName)) -join "`n" | ConvertFrom-Json)[0]
    $networkCIDR = [string]$networkInspect.IPAM.Config[0].Subnet
    $networkBase = ($networkCIDR -split '/')[0]
    $validatorNames = @(for ($index = 0; $index -lt 4; $index++) { "chainlab-$suffix-validator-$index" })
    $validatorVolumes = @(for ($index = 0; $index -lt 4; $index++) { "chainlab-$suffix-validator-$index-data" })
    $validatorIPs = @(for ($index = 0; $index -lt 4; $index++) { Add-IPv4Offset $networkBase ([uint32](10 + $index)) })
    $containerNames += $validatorNames
    $volumeNames += $validatorVolumes

    for ($index = 0; $index -lt 4; $index++) {
        $volume = $validatorVolumes[$index]
        $setup = "chainlab-$suffix-setup-$index"
        Invoke-Docker @('volume', 'create', '--label', $label, $volume) | Out-Null
        Invoke-Docker @(
            'run', '--name', $setup, '--label', $label, '--network', $networkName, '--read-only',
            '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m', '--mount', $binaryMount,
            '--mount', "type=volume,source=$volume,target=/data", $Image,
            '/usr/local/bin/chainlab', 'desktop', 'identity', '--data-dir', '/data/node',
            '--role', 'validator', '--name', "validator-$index", '--p2p-address', "$($validatorIPs[$index]):26680"
        ) | Out-Null
        Invoke-Docker @('cp', "${setup}:/data/node/identity.json", (Join-Path $artifactDir "identity-$index.json")) | Out-Null
        Invoke-Docker @('rm', $setup) | Out-Null
    }

    $invitationArgs = @(
        'run', '--rm', '--network', $networkName, '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m',
        '--mount', $binaryMount, '--mount', $evidenceMount, $Image, '/usr/local/bin/chainlab',
        'desktop', 'invitation', '--out', '/evidence/invitation.json', '--chain-id', "chainlab-container-$suffix",
        '--network-name', 'Container Host Evidence'
    )
    for ($index = 0; $index -lt 4; $index++) { $invitationArgs += @('--identity', "/evidence/identity-$index.json") }
    Invoke-Docker $invitationArgs | Out-Null
    $invitationRaw = Get-Content -Raw -LiteralPath (Join-Path $artifactDir 'invitation.json')
    $secretTerms = 'private_key|priv_key|priv_validator|node_key|mnemonic|secret|control_token|account_key'
    if ($invitationRaw -match $secretTerms) { throw 'public invitation contains a private-material field name' }

    for ($index = 0; $index -lt 4; $index++) {
        $volume = $validatorVolumes[$index]
        Invoke-Docker @(
            'run', '--rm', '--network', $networkName, '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m',
            '--mount', $binaryMount, '--mount', $evidenceMount, '--mount', "type=volume,source=$volume,target=/data",
            $Image, '/usr/local/bin/chainlab', 'desktop', 'join', '--data-dir', '/data/node', '--invitation', '/evidence/invitation.json'
        ) | Out-Null
    }

    $startupStarted = Get-Date
    for ($index = 0; $index -lt 4; $index++) {
        Invoke-Docker @(
            'run', '--detach', '--name', $validatorNames[$index], '--hostname', $validatorNames[$index],
            '--network', $networkName, '--ip', $validatorIPs[$index], '--label', $label, '--cpus', '4', '--memory', '8g', '--pids-limit', '256',
            '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m', '--mount', $binaryMount,
            '--mount', "type=volume,source=$($validatorVolumes[$index]),target=/data", $Image,
            '/usr/local/bin/chainlab', 'desktop', 'start', '--data-dir', '/data/node'
        ) | Out-Null
    }
    $readyStatuses = @()
    foreach ($name in $validatorNames) { $readyStatuses += Wait-NodeStatus $name }
    $startupSeconds = [math]::Round(((Get-Date) - $startupStarted).TotalSeconds, 3)
    $peerCounts = Wait-PeerMesh $validatorNames 3
    $namespaceEvidence = @($validatorNames | ForEach-Object { Get-NamespaceEvidence $_ })
    if (@($namespaceEvidence.IPAddress | Sort-Object -Unique).Count -ne 4 -or
        @($namespaceEvidence.NetworkNamespaceInode | Sort-Object -Unique).Count -ne 4 -or
        @($namespaceEvidence.MountNamespaceInode | Sort-Object -Unique).Count -ne 4 -or
        @($namespaceEvidence.PIDNamespaceInode | Sort-Object -Unique).Count -ne 4 -or
        @($namespaceEvidence.DataVolume | Sort-Object -Unique).Count -ne 4) {
        throw 'validator containers do not have distinct IP, namespace, and data-volume ownership'
    }
    foreach ($hostEvidence in $namespaceEvidence) {
        if ($hostEvidence.NanoCPUs -ne 4000000000 -or $hostEvidence.MemoryLimitBytes -ne 8GB -or
            $hostEvidence.PidsLimit -ne 256 -or -not $hostEvidence.ReadonlyRootfs) {
            throw "container resource/isolation contract mismatch: $($hostEvidence.Name)"
        }
    }

    $recipient = '0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    $firstTransfer = ((Invoke-Node $validatorNames[0] @(
        'desktop', 'transfer', '--data-dir', '/data/node', '--to', $recipient, '--value', '100'
    )) -join "`n" | ConvertFrom-Json)
    Wait-AccountBalance $validatorNames $recipient 100 | Out-Null
    $idleStartStatuses = @($validatorNames | ForEach-Object { Get-NodeStatus $_ })

    $cpuStart = @{}
    $rxStart = @{}
    $txStart = @{}
    $diskStart = @{}
    foreach ($name in $validatorNames) {
        $cpuStart[$name] = Get-CPUUsageMicroseconds $name
        $rxStart[$name] = Get-ContainerValue $name '/sys/class/net/eth0/statistics/rx_bytes'
        $txStart[$name] = Get-ContainerValue $name '/sys/class/net/eth0/statistics/tx_bytes'
        $diskStart[$name] = Get-ContainerDiskBytes $name
    }
    $samples = @()
    $sampleDeadline = (Get-Date).AddSeconds($DurationSeconds)
    do {
        foreach ($name in $validatorNames) {
            $samples += [pscustomobject]@{
                Timestamp = (Get-Date).ToUniversalTime().ToString('o')
                Name = $name
                MemoryCurrentBytes = Get-ContainerValue $name '/sys/fs/cgroup/memory.current'
                CPUUsageMicroseconds = Get-CPUUsageMicroseconds $name
            }
        }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $sampleDeadline)
    $idleEndStatuses = @($validatorNames | ForEach-Object { Get-NodeStatus $_ })
    $resourceEvidence = @()
    foreach ($name in $validatorNames) {
        $nodeSamples = @($samples | Where-Object Name -eq $name)
        $cpuEnd = Get-CPUUsageMicroseconds $name
        $rxEnd = Get-ContainerValue $name '/sys/class/net/eth0/statistics/rx_bytes'
        $txEnd = Get-ContainerValue $name '/sys/class/net/eth0/statistics/tx_bytes'
        $diskEnd = Get-ContainerDiskBytes $name
        $resourceEvidence += [pscustomobject]@{
            Name = $name
            PeakCgroupMemoryBytes = Get-ContainerValue $name '/sys/fs/cgroup/memory.peak'
            AverageCgroupMemoryBytes = [int64](($nodeSamples | Measure-Object MemoryCurrentBytes -Average).Average)
            CPUPercentOfFourCPUQuota = [math]::Round(100 * ($cpuEnd - $cpuStart[$name]) / ($DurationSeconds * 1000000 * 4), 3)
            NetworkReceivedBytes = $rxEnd - $rxStart[$name]
            NetworkSentBytes = $txEnd - $txStart[$name]
            DiskBytesStart = $diskStart[$name]
            DiskBytesEnd = $diskEnd
            DiskBytesGrowth = $diskEnd - $diskStart[$name]
        }
    }

    Stop-Node $validatorNames[2]
    $threeOfFourStart = Get-NodeStatus $validatorNames[0]
    Start-Sleep -Seconds 7
    $threeOfFourEnd = Get-NodeStatus $validatorNames[0]
    if ([int64]$threeOfFourEnd.height -le [int64]$threeOfFourStart.height) { throw '3-of-4 network did not progress' }

    Stop-Node $validatorNames[3]
    $haltStart = @($validatorNames[0..1] | ForEach-Object { Get-NodeStatus $_ })
    $transferCommand = "/usr/local/bin/chainlab desktop transfer --data-dir /data/node --to $recipient --value 200 >/data/queued.json 2>/data/queued.err; echo `$? >/data/queued.exit"
    Invoke-Docker @('exec', '--detach', $validatorNames[0], 'sh', '-c', $transferCommand) | Out-Null
    Start-Sleep -Seconds 2
    $mempoolCount = [int](Get-CometRPC $validatorNames[0] '/num_unconfirmed_txs').result.n_txs
    if ($mempoolCount -lt 1) { throw '2-of-4 node did not accept the queued transaction into mempool' }
    Start-Sleep -Seconds 10
    $haltEnd = @($validatorNames[0..1] | ForEach-Object { Get-NodeStatus $_ })
    for ($index = 0; $index -lt 2; $index++) {
        if ($haltEnd[$index].height -ne $haltStart[$index].height -or $haltEnd[$index].app_hash -ne $haltStart[$index].app_hash) {
            throw "2-of-4 node advanced: $($validatorNames[$index])"
        }
    }

    $validator2Recovery = Start-StoppedNode $validatorNames[2]
    $queuedDeadline = (Get-Date).AddSeconds(30)
    do {
        if (Test-Docker @('exec', $validatorNames[0], 'test', '-s', '/data/queued.json')) { break }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $queuedDeadline)
    if (-not (Test-Docker @('exec', $validatorNames[0], 'test', '-s', '/data/queued.json'))) {
        $queuedError = (Invoke-Docker @('exec', $validatorNames[0], 'sh', '-c', 'cat /data/queued.err 2>/dev/null || true')) -join "`n"
        throw "queued transaction did not commit after restoring 3-of-4: $queuedError"
    }
    $queuedTransfer = ((Invoke-Docker @('exec', $validatorNames[0], 'cat', '/data/queued.json')) -join "`n" | ConvertFrom-Json)
    Wait-AccountBalance $validatorNames[0..2] $recipient 300 | Out-Null
    $validator3Recovery = Start-StoppedNode $validatorNames[3]
    Wait-AccountBalance $validatorNames $recipient 300 | Out-Null
    $finalValidatorStates = Wait-CommonState $validatorNames
    $restoredPeerCounts = Wait-PeerMesh $validatorNames 3

    $observerName = "chainlab-$suffix-observer"
    $observerVolume = "chainlab-$suffix-observer-data"
    $observerIP = Add-IPv4Offset $networkBase 20
    $containerNames += $observerName
    $volumeNames += $observerVolume
    Invoke-Docker @('volume', 'create', '--label', $label, $observerVolume) | Out-Null
    Invoke-Docker @(
        'run', '--rm', '--network', $networkName, '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m',
        '--mount', $binaryMount, '--mount', "type=volume,source=$observerVolume,target=/data", $Image,
        '/usr/local/bin/chainlab', 'desktop', 'identity', '--data-dir', '/data/node', '--role', 'observer',
        '--name', 'observer-0', '--p2p-address', "${observerIP}:26680"
    ) | Out-Null
    Invoke-Docker @(
        'run', '--rm', '--network', $networkName, '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m',
        '--mount', $binaryMount, '--mount', $evidenceMount, '--mount', "type=volume,source=$observerVolume,target=/data", $Image,
        '/usr/local/bin/chainlab', 'desktop', 'join', '--data-dir', '/data/node', '--invitation', '/evidence/invitation.json'
    ) | Out-Null
    $observerStarted = Get-Date
    Invoke-Docker @(
        'run', '--detach', '--name', $observerName, '--hostname', $observerName, '--network', $networkName, '--ip', $observerIP,
        '--label', $label, '--cpus', '4', '--memory', '8g', '--pids-limit', '256', '--read-only',
        '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m', '--mount', $binaryMount,
        '--mount', "type=volume,source=$observerVolume,target=/data", $Image,
        '/usr/local/bin/chainlab', 'desktop', 'start', '--data-dir', '/data/node'
    ) | Out-Null
    $observerStatus = Wait-NodeStatus $observerName
    Wait-AccountBalance @($observerName) $recipient 300 | Out-Null
    $observerCaughtUpDeadline = (Get-Date).AddSeconds(30)
    do {
        $observerCometStatus = Get-CometRPC $observerName '/status'
        if (-not [bool]$observerCometStatus.result.sync_info.catching_up) { break }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $observerCaughtUpDeadline)
    if ([bool]$observerCometStatus.result.sync_info.catching_up) { throw 'observer did not finish catching up' }
    $observerSyncSeconds = [math]::Round(((Get-Date) - $observerStarted).TotalSeconds, 3)
    $observerExplorer = Get-NodeHTTP $observerName '/' 8547
    if ($observerExplorer -notmatch 'ChainLab Desktop') { throw 'observer Explorer did not render the desktop page' }
    $observerHasValidatorKey = Test-Docker @('exec', $observerName, 'test', '-e', '/data/node/identity/config/priv_validator_key.json')
    $observerHasValidatorState = Test-Docker @('exec', $observerName, 'test', '-e', '/data/node/identity/data/priv_validator_state.json')
    if ($observerHasValidatorKey -or $observerHasValidatorState) { throw 'observer contains validator signing material' }
    $observerTransferOutput = & docker exec $observerName /usr/local/bin/chainlab desktop transfer --data-dir /data/node --to $recipient --value 1 2>&1
    $observerTransferExit = $LASTEXITCODE
    if ($observerTransferExit -eq 0 -or ($observerTransferOutput -join "`n") -notmatch 'no local validator signing key') {
        throw 'observer local signing did not fail closed'
    }
    $observerNamespace = Get-NamespaceEvidence $observerName

    Stop-Node $observerName
    foreach ($name in $validatorNames) { Stop-Node $name }
    foreach ($name in $containerNames) {
        $logPath = Join-Path $artifactDir "$name.log"
        [IO.File]::WriteAllText($logPath, ((Invoke-Docker @('logs', $name)) -join "`n"), [Text.UTF8Encoding]::new($false))
    }
    $runtimeFilesRemaining = 0
    for ($index = 0; $index -lt $volumeNames.Count; $index++) {
        if (-not (Test-Docker @(
            'run', '--rm', '--read-only', '--mount', "type=volume,source=$($volumeNames[$index]),target=/data,readonly",
            $Image, 'sh', '-c', 'test ! -e /data/node/runtime.json'
        ))) { $runtimeFilesRemaining++ }
    }

    $result = [pscustomobject]@{
        Protocol = 'chainlab-desktop-container-host-measurement-v1'
        RunID = $runID
        StartedAt = $startupStarted.ToUniversalTime().ToString('o')
        DurationSeconds = $DurationSeconds
        Artifact = $version
        Environment = [pscustomobject]@{
            DockerServerVersion = ((Invoke-Docker @('version', '--format', '{{.Server.Version}}')) -join '').Trim()
            Image = $Image
            ImageID = [string]$imageInspect.Id
            ImageDigest = [string]$imageInspect.RepoDigests[0]
            Network = $networkName
            NetworkCIDR = $networkCIDR
            IsolationScope = 'distinct Linux containers, IP addresses, network/mount/PID namespaces, cgroups, and named data volumes on one Docker Desktop/WSL2 kernel'
            Limitation = 'Not four physical homes, not independent kernels or power domains, not WAN/dynamic-IP/sleep-resume evidence, and not the Windows 4-core/8-GiB reference-machine gate.'
        }
        StartupSeconds = $startupSeconds
        InitialPeerCounts = $peerCounts
        Namespaces = $namespaceEvidence + $observerNamespace
        Idle = [pscustomobject]@{
            HeightStart = @($idleStartStatuses.height)
            HeightEnd = @($idleEndStatuses.height)
            HeightDelta = @(for ($index = 0; $index -lt 4; $index++) { [int64]$idleEndStatuses[$index].height - [int64]$idleStartStatuses[$index].height })
            PerValidator = $resourceEvidence
            TrafficScope = 'per-container eth0 byte counters; ChainLab is the only long-running process in each measured container'
        }
        Quorum = [pscustomobject]@{
            InitialTransfer = $firstTransfer
            ThreeOfFourHeightDelta = [int64]$threeOfFourEnd.height - [int64]$threeOfFourStart.height
            TwoOfFourStableSeconds = 12
            TwoOfFourHeights = @($haltEnd.height)
            QueuedMempoolTransactions = $mempoolCount
            Validator2Recovery = $validator2Recovery
            QueuedTransfer = $queuedTransfer
            Validator3Recovery = $validator3Recovery
            FinalHeight = [int64]$finalValidatorStates[0].height
            FinalBlockHash = [string]$finalValidatorStates[0].block_hash
            FinalAppHash = [string]$finalValidatorStates[0].app_hash
            RestoredPeerCounts = $restoredPeerCounts
            FinalRecipientBalance = 300
        }
        Observer = [pscustomobject]@{
            SyncSeconds = $observerSyncSeconds
            Height = [int64]$observerStatus.height
            Profile = [string]$observerStatus.profile
            CatchingUp = [bool]$observerCometStatus.result.sync_info.catching_up
            AccountBalance = 300
            ExplorerRendered = $true
            HasValidatorKey = $observerHasValidatorKey
            HasValidatorState = $observerHasValidatorState
            LocalSigningRejected = $observerTransferExit -ne 0
            ExternalRawBroadcastEvidence = 'Covered separately by TestHomeValidatorQuorumRecoveryAndObserverBroadcast on the shipped tree.'
        }
        PublicInvitation = [pscustomobject]@{
            SHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $artifactDir 'invitation.json')).Hash.ToLowerInvariant()
            PrivateMaterialFieldScan = 'clean'
        }
        CleanShutdown = [pscustomobject]@{
            ContainersExited = @($containerNames | Where-Object { (Invoke-Docker @('inspect', '--format', '{{.State.Running}}', $_)) -join '' -eq 'false' }).Count -eq $containerNames.Count
            RuntimeFilesRemaining = $runtimeFilesRemaining
        }
        Samples = $samples
    }
} finally {
    foreach ($name in $containerNames) { & docker rm --force $name *> $null }
    $ownedContainerIDs = @(& docker ps --all --quiet --filter "label=$label")
    if ($ownedContainerIDs.Count -gt 0) { & docker rm --force @ownedContainerIDs *> $null }
    foreach ($volume in $volumeNames) { & docker volume rm --force $volume *> $null }
    & docker network rm $networkName *> $null
    $cleanup = [pscustomobject]@{
        ContainersRemaining = @(& docker ps --all --quiet --filter "label=$label").Count
        VolumesRemaining = @(& docker volume ls --quiet --filter "label=$label").Count
        NetworksRemaining = @(& docker network ls --quiet --filter "label=$label").Count
    }
}

if ($null -eq $result) { throw 'container-host measurement did not produce a result' }
$result | Add-Member -NotePropertyName Cleanup -NotePropertyValue $cleanup
$artifact = Join-Path $artifactDir 'measurement.json'
[IO.File]::WriteAllText($artifact, ($result | ConvertTo-Json -Depth 10), [Text.UTF8Encoding]::new($false))
$artifactHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $artifact).Hash.ToLowerInvariant()
$result | Select-Object RunID, DurationSeconds, StartupSeconds, InitialPeerCounts, @{N='FinalHeight';E={$_.Quorum.FinalHeight}}, @{N='ObserverSyncSeconds';E={$_.Observer.SyncSeconds}}, CleanShutdown, Cleanup, @{N='Artifact';E={$artifact}}, @{N='ArtifactSHA256';E={$artifactHash}}
