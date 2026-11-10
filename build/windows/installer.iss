; FileLoad 合一安装包脚本
; 打包 FileLoad.exe + WinFsp MSI，一次安装到位
; 构建: ISCC.exe build\windows\installer.iss  （先确保仓库根目录 dist\ 下有 FileLoad.exe、fileload.ico 和 winfsp-2.1.msi）

#define MyAppName "FileLoad"
#define MyAppVersion "1.0.0"
#define MyAppPublisher "KoLoRang"
#define MyAppExeName "FileLoad.exe"

[Setup]
; 以下相对路径均相对仓库根目录
SourceDir=..\..
AppId={{8B7C1E2A-4F6D-4E8A-9C3B-5D2E7A1F0C44}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
UninstallDisplayIcon={app}\{#MyAppExeName}
OutputDir=dist
OutputBaseFilename=FileLoad-Setup-{#MyAppVersion}
Compression=lzma2/max
SolidCompression=yes
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
WizardStyle=modern
SetupIconFile=dist\fileload.ico
; 显式保留安装位置选择页（默认即显示，此处明确声明防止被覆盖）
DisableDirPage=no
DisableProgramGroupPage=no
; 升级时默认带入上次目录，但用户仍可修改
UsePreviousAppDir=yes
; 自动更新时关闭仍在运行的旧版本（程序自身也会先退出）
CloseApplications=force
RestartApplications=no
AllowNoIcons=yes

[Languages]
Name: "chinesesimp"; MessagesFile: "compiler:Languages\ChineseSimplified.isl"

[CustomMessages]
chinesesimp.WinFspTitle=安装 WinFsp 驱动
chinesesimp.WinFspDesc=FileLoad 需要 WinFsp 内核驱动来创建盘符，即将静默安装（约 10 秒）。
chinesesimp.DirPageNote=提示：FileLoad 本体装到下面选择的目录；WinFsp 驱动固定装到系统目录（内核驱动必须如此），不受此选项影响。

[Files]
Source: "dist\FileLoad.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "dist\fileload.ico"; DestDir: "{app}"; Flags: ignoreversion
Source: "dist\winfsp-2.1.msi"; DestDir: "{tmp}"; Flags: deleteafterinstall ignoreversion

[Dirs]
; 缓存目录无需预创建，程序运行时自建

[Icons]
; 显式指定 IconFilename，保证快捷方式图标不依赖 exe 资源解析
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\fileload.ico"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\fileload.ico"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"

[Run]
; 静默安装 WinFsp MSI（已装过会自动跳过升级判定）
Filename: "msiexec.exe"; Parameters: "/i ""{tmp}\winfsp-2.1.msi"" /qn /norestart"; StatusMsg: "{cm:WinFspDesc}"; Check: WinFspNeedsInstall; Flags: runhidden waituntilterminated
; 安装完成启动程序（可选勾选）
Filename: "{app}\{#MyAppExeName}"; Description: "{cm:LaunchProgram,{#MyAppName}}"; Flags: nowait postinstall skipifsilent
; 程序内「立即更新」以 /SILENT 运行安装包：装完自动以原用户身份重新启动 FileLoad
Filename: "{app}\{#MyAppExeName}"; Flags: nowait runasoriginaluser; Check: WizardSilent

[UninstallRun]
; 卸载时关闭运行中的 FileLoad（含托盘实例）
Filename: "{cmd}"; Parameters: "/c taskkill /IM FileLoad.exe /F"; Flags: runhidden; RunOnceId: "KillApp"

[UninstallDelete]
Type: filesandordirs; Name: "{app}"

[Code]
function WinFspNeedsInstall(): Boolean;
var
  Installed: Boolean;
begin
  // 检测 WinFsp 是否已注册（HKLM 下 64 位视图）
  Installed := RegKeyExists(HKLM, 'SOFTWARE\WOW6432Node\WinFsp') or
               RegKeyExists(HKLM, 'SOFTWARE\WinFsp');
  Result := not Installed;
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  // 在目录选择页追加说明（驱动位置不受安装目录影响）
  if CurStep = ssInstall then
    WizardForm.DirEdit.Hint := ExpandConstant('{cm:DirPageNote}');
end;
