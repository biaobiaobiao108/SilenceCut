[Setup]
AppId=app.silencecut.desktop
AppName=SilenceCut
AppVersion={#AppVersion}
DefaultDirName={autopf}\SilenceCut
PrivilegesRequired=lowest
ArchitecturesAllowed=x64os
ArchitecturesInstallIn64BitMode=x64os
MinVersion=10.0
DisableProgramGroupPage=yes
UninstallDisplayIcon={app}\SilenceCut.exe
SetupIconFile={#IconFile}
OutputDir={#OutputDir}
OutputBaseFilename=SilenceCut-windows-x64-setup
Compression=lzma2
SolidCompression=yes

[Files]
Source: "{#BundleDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\SilenceCut"; Filename: "{app}\SilenceCut.exe"
