param(
    [Parameter(Mandatory)]
    [string] $Installer
)

$ErrorActionPreference = 'Stop'
$installerPath = (Resolve-Path -LiteralPath $Installer).Path
$root = Join-Path ([IO.Path]::GetTempPath()) ("silencecut-install-" + [guid]::NewGuid().ToString('N'))
$installPath = Join-Path $root 'app'
$log = Join-Path $root 'setup.log'
$uninstallKey = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\app.silencecut.desktop_is1'
foreach ($hive in @('HKCU:', 'HKLM:')) {
    if (Test-Path (Join-Path $hive $uninstallKey)) {
        throw 'Use a Windows account or runner without an existing SilenceCut installation.'
    }
}

New-Item -ItemType Directory -Path $root | Out-Null
$setup = Start-Process -FilePath $installerPath -ArgumentList @(
    '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/DIR=`"$installPath`"", "/LOG=`"$log`""
) -WindowStyle Hidden -Wait -PassThru
if ($setup.ExitCode -ne 0) {
    Get-Content -LiteralPath $log -ErrorAction SilentlyContinue
    throw "Installer returned $($setup.ExitCode)"
}

$app = $null
$uninstaller = Join-Path $installPath 'unins000.exe'
try {
    foreach ($file in @('SilenceCut.exe', 'Notices\SilenceCut.txt', 'Notices\Go\LICENSE', 'Notices\FFmpeg\LICENSE.md')) {
        if (!(Test-Path -LiteralPath (Join-Path $installPath $file) -PathType Leaf)) {
            throw "Installed file missing: $file"
        }
    }
    $previousPath = $env:PATH
    try {
        $env:PATH = "$env:SystemRoot\System32;$env:SystemRoot"
        $app = Start-Process -FilePath (Join-Path $installPath 'SilenceCut.exe') -WorkingDirectory $root -WindowStyle Hidden -PassThru
    } finally {
        $env:PATH = $previousPath
    }
    if ($app.WaitForExit(5000)) {
        throw "Installed application exited during startup: $($app.ExitCode)"
    }
} finally {
    if ($app -and !$app.HasExited) {
        if (!$app.CloseMainWindow() -or !$app.WaitForExit(5000)) {
            Stop-Process -Id $app.Id -ErrorAction SilentlyContinue
            $app.WaitForExit()
        }
    }
    if (Test-Path -LiteralPath $uninstaller) {
        $remove = Start-Process -FilePath $uninstaller -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART' -WindowStyle Hidden -Wait -PassThru
        if ($remove.ExitCode -ne 0) {
            throw "Uninstaller returned $($remove.ExitCode)"
        }
    }
}
if (Test-Path -LiteralPath (Join-Path $installPath 'SilenceCut.exe')) {
    throw 'Uninstaller left the app executable installed.'
}
Write-Output 'Windows installation, isolated launch and uninstallation passed.'
