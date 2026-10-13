<#
.SYNOPSIS
Collect Windows host CPU, physical memory, and ICMP probe loss for NetWatch.
.DESCRIPTION
Run this script in Windows PowerShell or PowerShell on Windows. It reads the
project's private .env file without executing its contents or logging the token.
Default probes use loopback; pass -PingTarget for another authorized destination.
#>
[CmdletBinding()]
param (
    [string] $DeviceId = 'windows-host',
    [string] $ApiUrl = '',
    [string] $PingTarget = '127.0.0.1',
    [string] $SettingsPath = '',
    [ValidateRange(1, 3600)]
    [int] $IntervalSeconds = 10,
    [ValidateRange(0, 2147483647)]
    [int] $Count = 0
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Read-NetWatchSettings {
    param ([string] $Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw 'Missing .env file. Run scripts/setup.sh from Ubuntu first.'
    }

    $settings = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        if ($line -match '^([A-Z_]+)=(.*)$') {
            $settings[$Matches[1]] = $Matches[2]
        }
    }
    if (-not $settings.ContainsKey('NETWATCH_API_TOKEN')) {
        throw 'NETWATCH_API_TOKEN is missing from .env.'
    }
    return $settings
}

function Read-WindowsCpuPercent {
    # The CIM class names are independent of localized Get-Counter path names.
    $processor = Get-CimInstance -ClassName Win32_PerfFormattedData_PerfOS_Processor `
        -Filter "Name='_Total'" | Select-Object -First 1
    if ($null -eq $processor -or $null -eq $processor.PercentProcessorTime) {
        throw 'Windows did not provide an aggregate CPU reading.'
    }
    return [double] $processor.PercentProcessorTime
}

function Read-WindowsMemoryPercent {
    $operatingSystem = Get-CimInstance -ClassName Win32_OperatingSystem
    $total = [double] $operatingSystem.TotalVisibleMemorySize
    $free = [double] $operatingSystem.FreePhysicalMemory
    if ($total -le 0 -or $free -lt 0 -or $free -gt $total) {
        throw 'Windows returned invalid physical-memory counters.'
    }
    return 100.0 * ($total - $free) / $total
}

function Measure-WindowsProbeLoss {
    param ([string] $Target)

    $probe = [System.Net.NetworkInformation.Ping]::new()
    $successful = 0
    $probeCount = 5
    try {
        for ($index = 0; $index -lt $probeCount; $index++) {
            $reply = $probe.Send($Target, 1000)
            if ($reply.Status -eq [System.Net.NetworkInformation.IPStatus]::Success) {
                $successful++
            }
            if ($index -lt $probeCount - 1) {
                Start-Sleep -Milliseconds 200
            }
        }
    } finally {
        $probe.Dispose()
    }
    return 100.0 * ($probeCount - $successful) / $probeCount
}

function Assert-ValidPercentage {
    param ([double] $Value, [string] $Name)

    if ([double]::IsNaN($Value) -or [double]::IsInfinity($Value) -or $Value -lt 0 -or $Value -gt 100) {
        throw "Invalid $Name percentage from Windows."
    }
}

if ($env:OS -ne 'Windows_NT') {
    throw 'Run this collector on Windows, not inside Linux PowerShell.'
}
if ([string]::IsNullOrWhiteSpace($DeviceId) -or [string]::IsNullOrWhiteSpace($PingTarget)) {
    throw 'DeviceId and PingTarget must not be empty.'
}

$projectPath = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($SettingsPath)) {
    $SettingsPath = Join-Path $projectPath '.env'
}
$settings = Read-NetWatchSettings -Path $SettingsPath
if ([string]::IsNullOrWhiteSpace($ApiUrl)) {
    $port = 8080
    if ($settings.ContainsKey('HTTP_PORT')) {
        $port = [int] $settings['HTTP_PORT']
    }
    if ($port -lt 1 -or $port -gt 65535) {
        throw 'HTTP_PORT must be a valid TCP port.'
    }
    $ApiUrl = "http://localhost:$port"
}
$endpoint = $ApiUrl.TrimEnd('/') + '/api/telemetry'
$headers = @{ Authorization = 'Bearer ' + $settings['NETWATCH_API_TOKEN'] }
$failedAttempts = 0

Write-Host "Collecting Windows host metrics as $DeviceId. Press Ctrl+C to stop."
for ($attempt = 1; $Count -eq 0 -or $attempt -le $Count; $attempt++) {
    try {
        $lossPercent = Measure-WindowsProbeLoss -Target $PingTarget
        $cpuPercent = Read-WindowsCpuPercent
        $memoryPercent = Read-WindowsMemoryPercent
        Assert-ValidPercentage -Value $cpuPercent -Name 'CPU'
        Assert-ValidPercentage -Value $memoryPercent -Name 'memory'
        Assert-ValidPercentage -Value $lossPercent -Name 'probe loss'

        $reading = @{
            device_id = $DeviceId
            source = 'windows'
            timestamp = [DateTimeOffset]::UtcNow.ToString('o')
            cpu_percent = $cpuPercent
            memory_percent = $memoryPercent
            packet_loss_pct = $lossPercent
        }
        $response = Invoke-RestMethod -Uri $endpoint -Method Post -Headers $headers `
            -ContentType 'application/json' -Body ($reading | ConvertTo-Json -Compress) `
            -TimeoutSec 10
        if ([string]::IsNullOrWhiteSpace($response.event_id)) {
            throw 'NetWatch did not confirm the accepted event.'
        }
        Write-Host ('Published Windows metrics: CPU={0:F1}%, memory={1:F1}%, probe loss={2:F1}%.' -f `
            $cpuPercent, $memoryPercent, $lossPercent)
    } catch {
        $failedAttempts++
        Write-Warning "Collection attempt $attempt failed; no replacement reading was generated: $($_.Exception.Message)"
    }

    if ($Count -eq 0 -or $attempt -lt $Count) {
        Start-Sleep -Seconds $IntervalSeconds
    }
}
if ($failedAttempts -gt 0) {
    throw "$failedAttempts collection attempts failed."
}
