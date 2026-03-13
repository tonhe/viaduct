; Inno Setup script for Viaduct (via) — Windows installer
; Installs via.exe and adds the install directory to the user PATH

#define MyAppName "Viaduct"
#define MyAppExeName "via.exe"
#define MyAppPublisher "tonhe"
#define MyAppURL "https://github.com/tonhe/viaduct"

[Setup]
AppId={{B8A3D2E1-4F5C-6A7B-8D9E-0F1A2B3C4D5E}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}/issues
DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
OutputBaseFilename=via_{#MyAppVersion}_windows_{#MyAppArch}_installer
OutputDir={#OutputPath}
Compression=lzma
SolidCompression=yes
ArchitecturesAllowed={#InnoArch}
ArchitecturesInstallIn64BitMode={#InnoArch}
ChangesEnvironment=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
LicenseFile={#SourceDir}\LICENSE
MinVersion=10.0

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
Source: "{#SourceDir}\{#MyAppExeName}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"

[Code]
procedure CurStepChanged(CurStep: TSetupStep);
var
  Path: string;
  AppDir: string;
begin
  if CurStep = ssPostInstall then
  begin
    AppDir := ExpandConstant('{app}');
    if IsAdminInstallMode then
    begin
      RegQueryStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Path);
      if Pos(Uppercase(AppDir), Uppercase(Path)) = 0 then
      begin
        Path := Path + ';' + AppDir;
        RegWriteStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Path);
      end;
    end
    else
    begin
      RegQueryStringValue(HKCU, 'Environment', 'Path', Path);
      if Pos(Uppercase(AppDir), Uppercase(Path)) = 0 then
      begin
        Path := Path + ';' + AppDir;
        RegWriteStringValue(HKCU, 'Environment', 'Path', Path);
      end;
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Path: string;
  AppDir: string;
  P: Integer;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    AppDir := ExpandConstant('{app}');
    if IsAdminInstallMode then
    begin
      RegQueryStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Path);
      P := Pos(';' + Uppercase(AppDir), Uppercase(Path));
      if P > 0 then
      begin
        Delete(Path, P, Length(AppDir) + 1);
        RegWriteStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Path);
      end
      else
      begin
        P := Pos(Uppercase(AppDir) + ';', Uppercase(Path));
        if P > 0 then
        begin
          Delete(Path, P, Length(AppDir) + 1);
          RegWriteStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Path);
        end
        else
        begin
          P := Pos(Uppercase(AppDir), Uppercase(Path));
          if P > 0 then
          begin
            Delete(Path, P, Length(AppDir));
            RegWriteStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', Path);
          end;
        end;
      end;
    end
    else
    begin
      RegQueryStringValue(HKCU, 'Environment', 'Path', Path);
      P := Pos(';' + Uppercase(AppDir), Uppercase(Path));
      if P > 0 then
      begin
        Delete(Path, P, Length(AppDir) + 1);
        RegWriteStringValue(HKCU, 'Environment', 'Path', Path);
      end
      else
      begin
        P := Pos(Uppercase(AppDir) + ';', Uppercase(Path));
        if P > 0 then
        begin
          Delete(Path, P, Length(AppDir) + 1);
          RegWriteStringValue(HKCU, 'Environment', 'Path', Path);
        end
        else
        begin
          P := Pos(Uppercase(AppDir), Uppercase(Path));
          if P > 0 then
          begin
            Delete(Path, P, Length(AppDir));
            RegWriteStringValue(HKCU, 'Environment', 'Path', Path);
          end;
        end;
      end;
    end;
  end;
end;
