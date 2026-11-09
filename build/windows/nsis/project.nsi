Unicode true

####
## PostSonde's Windows installer (docs/packaging-windows.md).
##
## Built by scripts/package-windows-installer.sh, which passes:
##   -DARG_WAILS_AMD64_BINARY=<exe> or -DARG_WAILS_ARM64_BINARY=<exe>
##                                    the app (scripts/build-windows.sh)
##   -DINFO_PRODUCTVERSION=<version>  scripts/version.sh, e.g. 0.1.0 or 0.0.0~git1234abc
##   -DVERSION_NUMERIC=<a.b.c.d>      the same as four numbers, for the PE version resource
##   -DOUTFILE=<path>                 PostSonde-<version>-windows-<arch>-setup.exe
##
## The names (company, product, copyright) come from wails_tools.nsh, which
## `wails3 task common:update:build-assets` generates from build/config.yml.
## Only its defines and its architecture macros are used: its
## wails.webview2runtime macro would run Microsoft's WebView2 bootstrapper,
## which downloads the runtime without asking. This installer downloads
## nothing (PRIVACY.md): it checks for the runtime and, when it is missing,
## says where to get it.
##
## What it does: a per-user install (no administrator rights, no UAC
## prompt) into %LOCALAPPDATA%\Programs\PostSonde, a Start-menu shortcut,
## an uninstaller with its Apps > Installed apps (Add/Remove Programs)
## entry. No desktop shortcut, no file associations, no services, nothing
## started in the background.
####

!define WAILS_INSTALL_SCOPE "user"
!define REQUEST_EXECUTION_LEVEL "user"
!define PRODUCT_EXECUTABLE "PostSonde.exe"
!define UNINST_KEY_NAME "PostSonde"
!include "wails_tools.nsh"

!ifndef VERSION_NUMERIC
    !define VERSION_NUMERIC "${INFO_PRODUCTVERSION}.0"
!endif
!ifndef OUTFILE
    !define OUTFILE "..\..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe"
!endif

# The Evergreen WebView2 Runtime's client ID and Microsoft's download page
# (https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution).
!define WEBVIEW2_CLIENT "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!define WEBVIEW2_URL "https://developer.microsoft.com/microsoft-edge/webview2/"

# Where PostSonde keeps its files for the user (internal/core/appdirs):
# config in %APPDATA%\PostSonde, data, cache and logs in
# %LOCALAPPDATA%\PostSonde; WebView2's profile in %APPDATA%\PostSonde.exe
# (Wails' default: the executable's name under %APPDATA%).
!define APPDIRS_NAME "PostSonde"

# The project's pages, shown as links in Apps > Installed apps (the
# uninstall entry's URLInfoAbout and HelpLink). Nothing opens them.
!define HOMEPAGE_URL "https://github.com/sorrelfox/PostSonde"
!define HELP_URL "https://github.com/sorrelfox/PostSonde/issues"

Name "${INFO_PRODUCTNAME}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
InstallDirRegKey HKCU "${UNINST_KEY}" "InstallLocation"
BrandingText "${INFO_PRODUCTNAME} ${INFO_PRODUCTVERSION}"
ShowInstDetails show
ShowUninstDetails show

# Reproducible output: solid LZMA, and no file times stored in the
# installer (the installed files get the time of the install).
SetCompressor /SOLID lzma
SetDateSave off

VIProductVersion "${VERSION_NUMERIC}"
VIFileVersion    "${VERSION_NUMERIC}"
VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI2.nsh"
!include "nsDialogs.nsh"
!include "LogicLib.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# 164 x 314, 24-bit: the icon on the app's dark background
# (assets/icons/README.md says how to regenerate it).
!define MUI_WELCOMEFINISHPAGE_BITMAP "wizard.bmp"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_NOAUTOCLOSE
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${INFO_PRODUCTNAME}"

!insertmacro MUI_PAGE_WELCOME
Page custom WebView2PageCreate
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!define MUI_COMPONENTSPAGE_NODESC
!define MUI_COMPONENTSPAGE_TEXT_TOP "Project folders are never touched. Tick the box to also delete your ${INFO_PRODUCTNAME} settings, history, cookies, saved secrets, logs and web view cache."
!insertmacro MUI_UNPAGE_COMPONENTS
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

## Code signing, only when scripts/package-windows-installer.sh has a
## certificate: it then passes SIGN_COMMAND (scripts/sign-windows.sh), which
## signs the uninstaller before it is packed and the installer last; %1 is
## the file. Without it, both stay unsigned.
!ifdef SIGN_COMMAND
!uninstfinalize '"${SIGN_COMMAND}" "%1"' = 0
!finalize '"${SIGN_COMMAND}" "%1"' = 0
!endif

Var WebView2Version

# Sets $WebView2Version to the installed WebView2 Runtime's version, or ""
# when there is none. Microsoft's documented check: the "pv" value of the
# runtime's client key, per machine (the 32-bit registry view, which is
# WOW6432Node on 64-bit Windows) or per user; empty or 0.0.0.0 means not
# installed. The native view is read too, in case it is only there.
!macro postsonde.readWebView2 ROOT
    ${If} $WebView2Version == ""
        ReadRegStr $0 ${ROOT} "SOFTWARE\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_CLIENT}" "pv"
        ${If} $0 != ""
        ${AndIf} $0 != "0.0.0.0"
            StrCpy $WebView2Version $0
        ${EndIf}
    ${EndIf}
!macroend

Function DetectWebView2
    StrCpy $WebView2Version ""
    SetRegView 32
    !insertmacro postsonde.readWebView2 HKLM
    !insertmacro postsonde.readWebView2 HKCU
    SetRegView 64
    !insertmacro postsonde.readWebView2 HKLM
    !insertmacro postsonde.readWebView2 HKCU
    SetRegView default
FunctionEnd

# Shown only when the runtime is missing. Next installs anyway (PostSonde
# then won't start until the runtime is there), Cancel quits. The link
# opens Microsoft's page in the browser when clicked; nothing is
# downloaded by the installer.
Function WebView2PageCreate
    ${If} $WebView2Version != ""
        Abort
    ${EndIf}
    !insertmacro MUI_HEADER_TEXT "Microsoft Edge WebView2 Runtime not found" "${INFO_PRODUCTNAME} needs it to show its window."
    nsDialogs::Create 1018
    Pop $0
    ${If} $0 == error
        Abort
    ${EndIf}
    ${NSD_CreateLabel} 0 0 100% 52u "${INFO_PRODUCTNAME} draws its window with the Microsoft Edge WebView2 Runtime, which isn't installed for this computer or this user. Windows 11 comes with it; on Windows 10 it usually arrives with Microsoft Edge.$\r$\n$\r$\nThis installer doesn't download anything. Get the Evergreen Runtime from Microsoft (the Bootstrapper, or the Standalone Installer for x64 or ARM64):"
    Pop $0
    ${NSD_CreateLink} 0 56u 100% 12u "${WEBVIEW2_URL}"
    Pop $0
    SetCtlColors $0 0000EE transparent
    ${NSD_OnClick} $0 WebView2OpenLink
    ${NSD_CreateLabel} 0 76u 100% 44u "Click Next to install ${INFO_PRODUCTNAME} now; it will start once the runtime is installed. Or click Cancel, install the runtime, and run this installer again."
    Pop $0
    nsDialogs::Show
FunctionEnd

Function WebView2OpenLink
    Pop $0
    ExecShell "open" "${WEBVIEW2_URL}"
FunctionEnd

# Stops (Retry or Cancel) while PostSonde.exe in $INSTDIR is running: a
# running executable can't be opened for writing, so it couldn't be
# replaced or deleted. A silent run cancels.
!macro postsonde.ensureNotRunning
    postsonde_check_running:
    ${If} ${FileExists} "$INSTDIR\${PRODUCT_EXECUTABLE}"
        ClearErrors
        FileOpen $0 "$INSTDIR\${PRODUCT_EXECUTABLE}" a
        ${If} ${Errors}
            MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "${INFO_PRODUCTNAME} is running. Close all ${INFO_PRODUCTNAME} windows, then click Retry." /SD IDCANCEL IDRETRY postsonde_check_running
            Abort "${INFO_PRODUCTNAME} is running."
        ${EndIf}
        FileClose $0
    ${EndIf}
!macroend

Function .onInit
    !insertmacro wails.checkArchitecture
    Call DetectWebView2
FunctionEnd

Section "-Install"
    SetShellVarContext current

    ${If} $WebView2Version == ""
        DetailPrint "Microsoft Edge WebView2 Runtime: not installed. ${INFO_PRODUCTNAME} won't start until it is: ${WEBVIEW2_URL}"
    ${Else}
        DetailPrint "Microsoft Edge WebView2 Runtime: $WebView2Version"
    ${EndIf}

    !insertmacro postsonde.ensureNotRunning

    SetOutPath $INSTDIR
    !insertmacro wails.files

    # The license texts the bundled components require (THIRD_PARTY_NOTICES.md).
    SetOutPath "$INSTDIR\licenses"
    File "/oname=LICENSE.txt" "..\..\..\LICENSE"
    File "..\..\..\THIRD_PARTY_NOTICES.md"
    SetOutPath "$INSTDIR\licenses\fonts\instrument-sans"
    File "..\..\..\frontend\src\assets\fonts\instrument-sans\OFL.txt"
    SetOutPath "$INSTDIR\licenses\fonts\jetbrains-mono"
    File "..\..\..\frontend\src\assets\fonts\jetbrains-mono\OFL.txt"

    # The shortcut's working directory is the output path.
    SetOutPath $INSTDIR
    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    WriteUninstaller "$INSTDIR\uninstall.exe"
    WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${INFO_PRODUCTNAME}"
    WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${INFO_PRODUCTVERSION}"
    WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "${INFO_COMPANYNAME}"
    WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "${HOMEPAGE_URL}"
    WriteRegStr HKCU "${UNINST_KEY}" "HelpLink" "${HELP_URL}"
    WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$\"$INSTDIR\${PRODUCT_EXECUTABLE}$\",0"
    WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
    WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
    WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" "$\"$INSTDIR\uninstall.exe$\" /S"
    WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
    WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
    ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
    WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" $0
SectionEnd

# Always: the program, the shortcut and the uninstall entry. Only the files
# this installer wrote are deleted, and the folder only when it is then
# empty (it may be a portable copy's folder, with postsonde-data/ in it).
Section "-un.Program"
    SetShellVarContext current
    !insertmacro postsonde.ensureNotRunning

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"
    Delete "$INSTDIR\licenses\LICENSE.txt"
    Delete "$INSTDIR\licenses\THIRD_PARTY_NOTICES.md"
    Delete "$INSTDIR\licenses\fonts\instrument-sans\OFL.txt"
    Delete "$INSTDIR\licenses\fonts\jetbrains-mono\OFL.txt"
    RMDir "$INSTDIR\licenses\fonts\instrument-sans"
    RMDir "$INSTDIR\licenses\fonts\jetbrains-mono"
    RMDir "$INSTDIR\licenses\fonts"
    RMDir "$INSTDIR\licenses"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"
    DeleteRegKey HKCU "${UNINST_KEY}"
SectionEnd

# Off by default, and so never in a silent uninstall: the user's settings
# and data in the default locations. A data directory moved in the
# settings (SET-03), POSTSONDE_HOME and portable copies are not touched.
Section /o "un.Delete my settings and data"
    SetShellVarContext current
    RMDir /r "$APPDATA\${APPDIRS_NAME}"
    RMDir /r "$LOCALAPPDATA\${APPDIRS_NAME}"
    RMDir /r "$APPDATA\${PRODUCT_EXECUTABLE}"
SectionEnd
