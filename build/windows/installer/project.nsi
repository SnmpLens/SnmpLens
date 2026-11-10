Unicode true

; This file must stay UTF-8 *with* a byte order mark: without one, makensis
; reads it in the system's ANSI code page and every accented string comes out
; garbled. internal/app/installer_test.go checks it. The release passes the
; version on makensis's command line (/DINFO_PRODUCTVERSION) rather than
; rewriting this file, which is how the mark used to be lost.

####
## SnmpLens installer
##
##   - Current user (no administrator rights) or all users (Program Files,
##     through UAC). Only an all-users install can run as a Windows service:
##     the service runs this executable as a system account, and a copy an
##     ordinary user can replace would hand that account to whoever replaces
##     it.
##   - Optionally installs the Windows service, which supervises from boot
##     with nobody signed in (SnmpLens service install).
##   - Stops the service for an update and starts it again; the uninstaller
##     removes it, its data only if asked.
##
## Silent install:
##   SnmpLens-<version>-setup.exe /S                     (current user)
##   SnmpLens-<version>-setup.exe /S /ALLUSERS            (all users, elevated)
##   SnmpLens-<version>-setup.exe /S /ALLUSERS /SERVICE   (and the service)
####

!include "MUI2.nsh"
!include "x64.nsh"
!include "nsDialogs.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "WinMessages.nsh"
!include "StrFunc.nsh"

; StrFunc functions are declared once before use.
${StrStr}

!define INFO_PRODUCTNAME "SnmpLens"
!define INFO_COMPANYNAME "Geoffrey Lecoq"
!ifndef INFO_PRODUCTVERSION
    !define INFO_PRODUCTVERSION "1.0.0"
!endif
!define INFO_COPYRIGHT "(c) 2026 Geoffrey Lecoq"
!define PRODUCT_EXECUTABLE "${INFO_PRODUCTNAME}.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${INFO_PRODUCTNAME}"
!define APP_RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
; The Windows service SnmpLens runs as (Settings > Background, or below).
!define SERVICE_NAME "SnmpLens"

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\..\SnmpLens-${INFO_PRODUCTVERSION}-setup.exe"

; Launch without admin - elevation happens only if user picks "All Users"
RequestExecutionLevel user
InstallDir ""

; Installer compression
SetCompressor /SOLID lzma
SetCompressorDictSize 32

; UI branding
!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
!define MUI_ABORTWARNING
BrandingText "${INFO_PRODUCTNAME} v${INFO_PRODUCTVERSION} - ${INFO_COMPANYNAME}"

; Finish page: run app + link to GitHub
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!define MUI_FINISHPAGE_RUN_TEXT "Launch ${INFO_PRODUCTNAME}"
!define MUI_FINISHPAGE_LINK "Visit ${INFO_PRODUCTNAME} on GitHub"
!define MUI_FINISHPAGE_LINK_LOCATION "https://github.com/SnmpLens/SnmpLens"

; =====================================================================
; Variables
; =====================================================================

Var InstallMode      ; "user" or "admin"
Var RadioUser
Var RadioAdmin
Var IsElevated       ; "1" if relaunched with /ALLUSERS
Var ChkDesktop       ; Checkbox: Desktop shortcut
Var ChkStartMenu     ; Checkbox: Start Menu shortcut
Var ChkAutoStart     ; Checkbox: Launch on Windows startup
Var ChkService       ; Checkbox: Run as a Windows service
Var OptDesktop
Var OptStartMenu
Var OptAutoStart
Var OptService
Var ServiceInstalled  ; "1" when the Windows service exists already
Var ServiceWasRunning ; "1" when StopService stopped it

; =====================================================================
; Init - detect previous install / elevated relaunch
; =====================================================================

Function .onInit
    StrCpy $InstallMode "user"
    StrCpy $IsElevated "0"
    StrCpy $OptDesktop "1"
    StrCpy $OptStartMenu "1"
    StrCpy $OptAutoStart "0"
    StrCpy $OptService "0"
    StrCpy $ServiceWasRunning "0"

    ${GetParameters} $0
    ; /SERVICE: install the Windows service too (all users only)
    ClearErrors
    ${GetOptions} $0 "/SERVICE" $1
    ${IfNot} ${Errors}
        StrCpy $OptService "1"
    ${EndIf}

    ; Check if relaunched with /ALLUSERS flag (elevated process)
    ClearErrors
    ${GetOptions} $0 "/ALLUSERS" $1
    ${IfNot} ${Errors}
        StrCpy $InstallMode "admin"
        StrCpy $IsElevated "1"
        SetShellVarContext all
        StrCpy $INSTDIR "$PROGRAMFILES\${INFO_PRODUCTNAME}"
    ${Else}
        StrCpy $INSTDIR "$LOCALAPPDATA\${INFO_PRODUCTNAME}"
    ${EndIf}

    ; Detect existing installation and pre-fill install dir
    ReadRegStr $0 HKCU "${UNINST_KEY}" "InstallLocation"
    ${If} $0 != ""
    ${AndIf} $IsElevated != "1"
        StrCpy $INSTDIR $0
    ${EndIf}
    ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
    ${If} $0 != ""
        ; An all-users copy is updated where it is: a second copy beside it
        ; would leave its service running the old one.
        StrCpy $INSTDIR $0
        StrCpy $InstallMode "admin"
    ${EndIf}

    ; The Windows service, when it exists: stopped for the update, started
    ; again after.
    StrCpy $ServiceInstalled "0"
    nsExec::ExecToStack 'sc.exe query ${SERVICE_NAME}'
    Pop $0
    Pop $1
    ${If} $0 == 0
        StrCpy $ServiceInstalled "1"
    ${EndIf}
FunctionEnd

; =====================================================================
; Custom page: Install mode (Current User / All Users)
; =====================================================================

Function InstallModePage
    ; Skip if already elevated
    ${If} $IsElevated == "1"
        Abort
    ${EndIf}

    nsDialogs::Create 1018
    Pop $0

    ${NSD_CreateGroupBox} 10u 5u 280u 85u "Installation mode"
    Pop $0

    ${NSD_CreateRadioButton} 25u 22u 250u 14u "Current user only - no admin rights needed"
    Pop $RadioUser

    ${NSD_CreateLabel} 40u 38u 240u 10u "Installs to: $LOCALAPPDATA\${INFO_PRODUCTNAME}"
    Pop $0

    ${NSD_CreateRadioButton} 25u 55u 250u 14u "All users - requires administrator rights"
    Pop $RadioAdmin

    ${NSD_CreateLabel} 40u 71u 240u 10u "Installs to: $PROGRAMFILES\${INFO_PRODUCTNAME}"
    Pop $0

    ; An all-users copy is there already: updating it is the default.
    ${If} $InstallMode == "admin"
        ${NSD_SetState} $RadioAdmin ${BST_CHECKED}
    ${Else}
        ${NSD_SetState} $RadioUser ${BST_CHECKED}
    ${EndIf}

    ${NSD_CreateLabel} 10u 98u 280u 30u "$(mode_service)"
    Pop $0

    nsDialogs::Show
FunctionEnd

Function InstallModeLeave
    ${NSD_GetState} $RadioAdmin $0
    ${If} $0 == ${BST_CHECKED}
        ${If} $OptService == "1"
            ExecShell "runas" "$EXEPATH" "/ALLUSERS /SERVICE"
        ${Else}
            ExecShell "runas" "$EXEPATH" "/ALLUSERS"
        ${EndIf}
        Quit
    ${Else}
        StrCpy $InstallMode "user"
        StrCpy $INSTDIR "$LOCALAPPDATA\${INFO_PRODUCTNAME}"
    ${EndIf}
FunctionEnd

; =====================================================================
; Custom page: Options (shortcuts, autostart, the service)
; =====================================================================

; $0 is "1" when $INSTDIR is under Program Files, where only administrators
; can write: the only place the service may run from.
Function InProgramFiles
    StrCpy $0 "0"
    StrLen $2 "$PROGRAMFILES"
    StrCpy $1 "$INSTDIR" $2
    ${If} $1 == "$PROGRAMFILES"
        StrCpy $0 "1"
    ${EndIf}
    StrLen $2 "$PROGRAMFILES64"
    StrCpy $1 "$INSTDIR" $2
    ${If} $1 == "$PROGRAMFILES64"
        StrCpy $0 "1"
    ${EndIf}
FunctionEnd

Function OptionsPage
    nsDialogs::Create 1018
    Pop $0

    ${NSD_CreateGroupBox} 10u 5u 280u 130u "Options"
    Pop $0

    ${NSD_CreateCheckBox} 25u 20u 250u 14u "Create desktop shortcut"
    Pop $ChkDesktop
    ${NSD_SetState} $ChkDesktop ${BST_CHECKED}

    ${NSD_CreateCheckBox} 25u 36u 250u 14u "Create Start Menu shortcut"
    Pop $ChkStartMenu
    ${NSD_SetState} $ChkStartMenu ${BST_CHECKED}

    ${NSD_CreateCheckBox} 25u 52u 250u 14u "Launch ${INFO_PRODUCTNAME} on Windows startup"
    Pop $ChkAutoStart

    ${If} $InstallMode == "admin"
        ${If} $ServiceInstalled == "1"
            ${NSD_CreateLabel} 25u 72u 255u 28u "$(service_updated)"
            Pop $0
        ${Else}
            ${NSD_CreateCheckBox} 25u 72u 255u 14u "$(service_checkbox)"
            Pop $ChkService
            ${If} $OptService == "1"
                ${NSD_SetState} $ChkService ${BST_CHECKED}
            ${EndIf}
            Call InProgramFiles
            ${If} $0 == "1"
                ${NSD_CreateLabel} 37u 88u 245u 28u "$(service_note)"
                Pop $0
            ${Else}
                EnableWindow $ChkService 0
                ${NSD_SetState} $ChkService ${BST_UNCHECKED}
                ${NSD_CreateLabel} 37u 88u 245u 28u "$(mode_service)"
                Pop $0
            ${EndIf}
        ${EndIf}
    ${EndIf}

    ${NSD_CreateLabel} 25u 118u 250u 10u "These options can be changed later in Windows settings."
    Pop $0

    nsDialogs::Show
FunctionEnd

Function OptionsPageLeave
    ${NSD_GetState} $ChkDesktop $OptDesktop
    ${NSD_GetState} $ChkStartMenu $OptStartMenu
    ${NSD_GetState} $ChkAutoStart $OptAutoStart
    ${If} $InstallMode == "admin"
    ${AndIf} $ServiceInstalled != "1"
        ${NSD_GetState} $ChkService $OptService
    ${EndIf}
FunctionEnd

; =====================================================================
; The service
; =====================================================================

; Stops the service when it runs and waits for it to stop (30 s at most),
; recording in $ServiceWasRunning whether it ran. It runs the very executable
; being replaced, and killing it would only have the service manager start it
; again over the files.
Function StopService
    StrCpy $ServiceWasRunning "0"
    ${If} $InstallMode != "admin"
        Return
    ${EndIf}
    nsExec::ExecToStack 'sc.exe query ${SERVICE_NAME}'
    Pop $0
    Pop $1
    ${If} $0 != 0
        Return
    ${EndIf}
    ${StrStr} $2 $1 "RUNNING"
    ${If} $2 == ""
        Return
    ${EndIf}
    StrCpy $ServiceWasRunning "1"
    DetailPrint "Stopping the SnmpLens service"
    nsExec::ExecToLog 'sc.exe stop ${SERVICE_NAME}'
    Pop $0
    StrCpy $3 0
    ${Do}
        Sleep 500
        nsExec::ExecToStack 'sc.exe query ${SERVICE_NAME}'
        Pop $0
        Pop $1
        ${StrStr} $2 $1 "STOPPED"
        ${If} $2 != ""
            ${ExitDo}
        ${EndIf}
        IntOp $3 $3 + 1
    ${LoopUntil} $3 >= 60
FunctionEnd

; Starts the service again if StopService stopped it: an update must not leave
; the machine without the supervision it relies on. A service an administrator
; had stopped stays stopped.
Function RestartService
    ${If} $ServiceWasRunning == "1"
        DetailPrint "Starting the SnmpLens service again"
        nsExec::ExecToLog 'sc.exe start ${SERVICE_NAME}'
        Pop $0
    ${EndIf}
FunctionEnd

; =====================================================================
; Pages
; =====================================================================

!insertmacro MUI_PAGE_WELCOME
Page custom InstallModePage InstallModeLeave
!insertmacro MUI_PAGE_LICENSE "..\..\..\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
Page custom OptionsPage OptionsPageLeave
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; Languages
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "French"
!insertmacro MUI_LANGUAGE "German"
!insertmacro MUI_LANGUAGE "Spanish"
!insertmacro MUI_LANGUAGE "SimpChinese"

LangString mode_service    ${LANG_ENGLISH}     "Only an installation for all users, in Program Files, can run as a Windows service, which supervises from the moment the PC starts, even when nobody is signed in."
LangString mode_service    ${LANG_FRENCH}      "Seule une installation pour tous les utilisateurs, dans Program Files, peut fonctionner comme service Windows, qui supervise dès le démarrage du PC, même sans session ouverte."
LangString mode_service    ${LANG_GERMAN}      "Nur eine Installation für alle Benutzer in Program Files kann als Windows-Dienst laufen, der ab dem Start des PCs überwacht, auch ohne angemeldete Benutzer."
LangString mode_service    ${LANG_SPANISH}     "Solo una instalación para todos los usuarios, en Program Files, puede funcionar como servicio de Windows, que supervisa desde que arranca el PC, incluso sin nadie conectado."
LangString mode_service    ${LANG_SIMPCHINESE} "只有为所有用户安装在 Program Files 中，才能作为 Windows 服务运行：从电脑启动起即开始监控，即使无人登录。"

LangString service_checkbox ${LANG_ENGLISH}     "Run SnmpLens as a Windows service"
LangString service_checkbox ${LANG_FRENCH}      "Faire fonctionner SnmpLens comme service Windows"
LangString service_checkbox ${LANG_GERMAN}      "SnmpLens als Windows-Dienst ausführen"
LangString service_checkbox ${LANG_SPANISH}     "Ejecutar SnmpLens como servicio de Windows"
LangString service_checkbox ${LANG_SIMPCHINESE} "将 SnmpLens 作为 Windows 服务运行"

LangString service_note    ${LANG_ENGLISH}     "It supervises from boot, with nobody signed in. It starts empty: Settings > Background hands it this computer's sessions and history."
LangString service_note    ${LANG_FRENCH}      "Il supervise dès le démarrage, sans session ouverte. Il démarre vide : Paramètres › Arrière-plan lui transmet les sessions et l'historique de cet ordinateur."
LangString service_note    ${LANG_GERMAN}      "Er überwacht ab dem Start, ohne angemeldete Benutzer. Er startet leer: Einstellungen › Hintergrund übergibt ihm die Sitzungen und den Verlauf dieses Computers."
LangString service_note    ${LANG_SPANISH}     "Supervisa desde el arranque, sin nadie conectado. Arranca vacío: Ajustes › Segundo plano le transfiere las sesiones y el historial de este equipo."
LangString service_note    ${LANG_SIMPCHINESE} "它从开机起即开始监控，无需登录。它以空配置启动：设置 › 后台会将本机的会话和历史记录移交给它。"

LangString service_updated ${LANG_ENGLISH}     "The SnmpLens Windows service is installed: it is stopped for the update and started again after."
LangString service_updated ${LANG_FRENCH}      "Le service Windows SnmpLens est installé : il est arrêté le temps de la mise à jour puis relancé."
LangString service_updated ${LANG_GERMAN}      "Der Windows-Dienst SnmpLens ist installiert: Er wird für die Aktualisierung angehalten und danach wieder gestartet."
LangString service_updated ${LANG_SPANISH}     "El servicio de Windows SnmpLens está instalado: se detiene durante la actualización y se vuelve a iniciar después."
LangString service_updated ${LANG_SIMPCHINESE} "SnmpLens Windows 服务已安装：更新期间会停止，更新后重新启动。"

LangString service_failed  ${LANG_ENGLISH}     "The SnmpLens Windows service could not be installed. Settings > Background says why and can try again."
LangString service_failed  ${LANG_FRENCH}      "Le service Windows SnmpLens n'a pas pu être installé. Paramètres › Arrière-plan dit pourquoi et peut réessayer."
LangString service_failed  ${LANG_GERMAN}      "Der Windows-Dienst SnmpLens konnte nicht installiert werden. Einstellungen › Hintergrund nennt den Grund und kann es erneut versuchen."
LangString service_failed  ${LANG_SPANISH}     "No se pudo instalar el servicio de Windows SnmpLens. Ajustes › Segundo plano indica el motivo y puede volver a intentarlo."
LangString service_failed  ${LANG_SIMPCHINESE} "无法安装 SnmpLens Windows 服务。设置 › 后台会说明原因并可重试。"

LangString un_need_admin   ${LANG_ENGLISH}     "SnmpLens was installed for all users: removing it needs administrator rights. Continue?"
LangString un_need_admin   ${LANG_FRENCH}      "SnmpLens a été installé pour tous les utilisateurs : le désinstaller demande les droits administrateur. Continuer ?"
LangString un_need_admin   ${LANG_GERMAN}      "SnmpLens wurde für alle Benutzer installiert: Zum Entfernen sind Administratorrechte nötig. Fortfahren?"
LangString un_need_admin   ${LANG_SPANISH}     "SnmpLens se instaló para todos los usuarios: desinstalarlo requiere derechos de administrador. ¿Continuar?"
LangString un_need_admin   ${LANG_SIMPCHINESE} "SnmpLens 是为所有用户安装的：卸载需要管理员权限。是否继续？"

LangString un_service_data ${LANG_ENGLISH}     "Also delete the Windows service's data: its monitoring history and the credentials it supervises with?"
LangString un_service_data ${LANG_FRENCH}      "Supprimer aussi les données du service Windows : son historique de supervision et les identifiants qu'il utilise ?"
LangString un_service_data ${LANG_GERMAN}      "Auch die Daten des Windows-Dienstes löschen: seinen Überwachungsverlauf und die Zugangsdaten, mit denen er überwacht?"
LangString un_service_data ${LANG_SPANISH}     "¿Eliminar también los datos del servicio de Windows: su historial de supervisión y las credenciales que utiliza?"
LangString un_service_data ${LANG_SIMPCHINESE} "是否同时删除 Windows 服务的数据：其监控历史记录和监控所用的凭据？"

; =====================================================================
; Install
; =====================================================================

Section "Install"
    ; The service first: it runs this very executable.
    Call StopService

    ; Kill running instance before install/upgrade
    nsExec::ExecToLog 'taskkill /F /IM "${PRODUCT_EXECUTABLE}"'
    Pop $0

    SetOutPath "$INSTDIR"

    ; Application
    File "..\..\..\build\bin\${PRODUCT_EXECUTABLE}"

    ; Uninstaller
    WriteUninstaller "$INSTDIR\uninstall.exe"

    ; Desktop shortcut (optional)
    ${If} $OptDesktop == ${BST_CHECKED}
        CreateShortcut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    ${EndIf}

    ; Start Menu shortcut (optional)
    ${If} $OptStartMenu == ${BST_CHECKED}
        CreateDirectory "$SMPROGRAMS\${INFO_PRODUCTNAME}"
        CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
        CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}\Uninstall ${INFO_PRODUCTNAME}.lnk" "$INSTDIR\uninstall.exe"
    ${EndIf}

    ; Auto-start on Windows boot (optional)
    ${If} $OptAutoStart == ${BST_CHECKED}
        ${If} $InstallMode == "admin"
            WriteRegStr HKLM "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}" "$INSTDIR\${PRODUCT_EXECUTABLE}"
        ${Else}
            WriteRegStr HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}" "$INSTDIR\${PRODUCT_EXECUTABLE}"
        ${EndIf}
    ${EndIf}

    ; Calculate installed size
    ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
    IntFmt $0 "0x%08X" $0

    ; Add/Remove Programs registry
    ${If} $InstallMode == "admin"
        WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${INFO_PRODUCTNAME}"
        WriteRegStr HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${PRODUCT_EXECUTABLE},0"
        WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${INFO_PRODUCTVERSION}"
        WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "${INFO_COMPANYNAME}"
        WriteRegStr HKLM "${UNINST_KEY}" "URLInfoAbout" "https://github.com/SnmpLens/SnmpLens"
        WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
        WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
        WriteRegDWORD HKLM "${UNINST_KEY}" "EstimatedSize" $0
        WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
        WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1
    ${Else}
        WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${INFO_PRODUCTNAME}"
        WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${PRODUCT_EXECUTABLE},0"
        WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${INFO_PRODUCTVERSION}"
        WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "${INFO_COMPANYNAME}"
        WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "https://github.com/SnmpLens/SnmpLens"
        WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
        WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
        WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" $0
        WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
        WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
    ${EndIf}

    ; The service: started again after an update, installed when asked. The
    ; executable installs it, as Settings > Background does: under its virtual
    ; account, with its directory locked down and its firewall rule, and lets
    ; the account that approved this drive it from its window.
    ${If} $ServiceInstalled == "1"
        Call RestartService
    ${ElseIf} $InstallMode == "admin"
    ${AndIf} $OptService == ${BST_CHECKED}
        DetailPrint "Installing the SnmpLens service"
        nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXECUTABLE}" service install --operator-self'
        Pop $0
        ${If} $0 != 0
            MessageBox MB_OK|MB_ICONEXCLAMATION "$(service_failed)" /SD IDOK
        ${EndIf}
    ${EndIf}
SectionEnd

; =====================================================================
; Uninstall
; =====================================================================

Function un.onInit
    ; All users when the all-users entry is this very copy: a per-user copy can
    ; sit beside an all-users one, and removing it must not take the other's
    ; entry, or its service, with it.
    StrCpy $InstallMode "user"
    ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
    ${If} $0 == $INSTDIR
        StrCpy $InstallMode "admin"
        UserInfo::GetAccountType
        Pop $1
        ${If} $1 != "Admin"
            MessageBox MB_YESNO|MB_ICONEXCLAMATION "$(un_need_admin)" /SD IDNO IDYES elevate
            Abort
            elevate:
                ExecShell "runas" "$INSTDIR\uninstall.exe"
                Quit
        ${EndIf}
    ${EndIf}
FunctionEnd

Section "Uninstall"
    ; The service runs this executable: it goes first, while the program that
    ; knows how to remove it — with its firewall rule and its event log
    ; source — is still here. Its data goes only when asked.
    ${If} $InstallMode == "admin"
        nsExec::ExecToStack 'sc.exe query ${SERVICE_NAME}'
        Pop $0
        Pop $1
        ${If} $0 == 0
            DetailPrint "Removing the SnmpLens service"
            StrCpy $2 ""
            MessageBox MB_YESNO|MB_ICONQUESTION "$(un_service_data)" /SD IDNO IDNO keepData
                StrCpy $2 " --purge"
            keepData:
            nsExec::ExecToLog '"$INSTDIR\${PRODUCT_EXECUTABLE}" service uninstall$2'
            Pop $0
        ${EndIf}
    ${EndIf}

    ; Kill running instance
    nsExec::ExecToLog 'taskkill /F /IM "${PRODUCT_EXECUTABLE}"'
    Pop $0

    ; Files
    Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    ; Shortcuts (try both contexts)
    SetShellVarContext current
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}\${INFO_PRODUCTNAME}.lnk"
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}\Uninstall ${INFO_PRODUCTNAME}.lnk"
    RMDir "$SMPROGRAMS\${INFO_PRODUCTNAME}"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    SetShellVarContext all
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}\${INFO_PRODUCTNAME}.lnk"
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}\Uninstall ${INFO_PRODUCTNAME}.lnk"
    RMDir "$SMPROGRAMS\${INFO_PRODUCTNAME}"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    ; Auto-start (remove from both)
    DeleteRegValue HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
    DeleteRegValue HKLM "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"

    ; Registry: this copy's entry only
    ${If} $InstallMode == "admin"
        DeleteRegKey HKLM "${UNINST_KEY}"
    ${Else}
        DeleteRegKey HKCU "${UNINST_KEY}"
    ${EndIf}
SectionEnd
