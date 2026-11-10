Unicode true

; This file must stay UTF-8 *with* a byte order mark: without one, makensis
; reads it in the system's ANSI code page and every accented string comes out
; garbled. internal/app/installer_test.go checks it. The release passes the
; version on makensis's command line (/DINFO_PRODUCTVERSION) rather than
; rewriting this file, which is how the mark used to be lost.

####
## SnmpLens installer
##
##   - Asks for its language, in the five the application speaks, and remembers
##     it for the elevated copy it relaunches and for the uninstaller.
##   - Current user (no administrator rights) or all users (64-bit Program
##     Files, through UAC). Only an all-users install can run as a Windows
##     service: the service runs this executable as a system account, and a copy
##     an ordinary user can replace would hand that account to whoever replaces
##     it.
##   - Installing for all users REPLACES the copy installed for the current
##     user: its shortcuts and its start at sign-in move to the new copy, the
##     settings and the history, which are the user's whichever copy runs, stay.
##     An existing all-users copy is updated where it is.
##   - Optionally installs the Windows service, which supervises from boot with
##     nobody signed in (SnmpLens service install). Stops it for an update and
##     starts it again; the uninstaller removes it, its data only if asked.
##   - Start at sign-in is the current user's entry, quoted, as the application
##     itself writes it — never a machine-wide one the application cannot see.
##
## Silent install:
##   SnmpLens-<version>-setup.exe /S                     (current user)
##   SnmpLens-<version>-setup.exe /S /ALLUSERS            (all users, elevated)
##   SnmpLens-<version>-setup.exe /S /ALLUSERS /SERVICE   (and the service)
## A silent update of an all-users copy started without the rights asks for
## them and goes on elevated.
####

!include "MUI2.nsh"
!include "x64.nsh"
!include "nsDialogs.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "WinMessages.nsh"
!include "StrFunc.nsh"

; StrFunc functions are declared once per side before use.
${StrStr}
${UnStrStr}

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

; The language asked for at start is remembered for the uninstaller.
!define MUI_LANGDLL_REGISTRY_ROOT "HKCU"
!define MUI_LANGDLL_REGISTRY_KEY "Software\${INFO_PRODUCTNAME}"
!define MUI_LANGDLL_REGISTRY_VALUENAME "InstallerLanguage"

; Finish page: run app + link to GitHub
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!define MUI_FINISHPAGE_RUN_TEXT "$(finish_run)"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchApp
!define MUI_FINISHPAGE_LINK "$(finish_link)"
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
Var ChkAutoStart     ; Checkbox: Start at sign-in
Var ChkService       ; Checkbox: Run as a Windows service
Var OptDesktop
Var OptStartMenu
Var OptAutoStart
Var OptService
Var ExistingAll      ; where the all-users copy is, if there is one
Var ExistingUser     ; where the current user's copy is, if there is one
Var ReplaceUser      ; the current user's copy this all-users install replaces
Var ServiceInstalled  ; "1" when the Windows service exists already
Var ServiceWasRunning ; "1" when StopService stopped it

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
!insertmacro MUI_RESERVEFILE_LANGDLL

LangString mode_header ${LANG_ENGLISH}     "Installation mode"
LangString mode_header ${LANG_FRENCH}      "Mode d'installation"
LangString mode_header ${LANG_GERMAN}      "Installationsart"
LangString mode_header ${LANG_SPANISH}     "Modo de instalación"
LangString mode_header ${LANG_SIMPCHINESE} "安装方式"

LangString mode_subheader ${LANG_ENGLISH}     "For you only, or for everyone using this computer."
LangString mode_subheader ${LANG_FRENCH}      "Pour vous seul, ou pour tous les utilisateurs de cet ordinateur."
LangString mode_subheader ${LANG_GERMAN}      "Nur für Sie oder für alle Benutzer dieses Computers."
LangString mode_subheader ${LANG_SPANISH}     "Solo para usted, o para todos los usuarios de este equipo."
LangString mode_subheader ${LANG_SIMPCHINESE} "仅为您安装，或为本机所有用户安装。"

LangString mode_group ${LANG_ENGLISH}     "Install SnmpLens"
LangString mode_group ${LANG_FRENCH}      "Installer SnmpLens"
LangString mode_group ${LANG_GERMAN}      "SnmpLens installieren"
LangString mode_group ${LANG_SPANISH}     "Instalar SnmpLens"
LangString mode_group ${LANG_SIMPCHINESE} "安装 SnmpLens"

LangString mode_user ${LANG_ENGLISH}     "For me only - no administrator rights needed"
LangString mode_user ${LANG_FRENCH}      "Pour moi seulement - sans droits administrateur"
LangString mode_user ${LANG_GERMAN}      "Nur für mich - keine Administratorrechte nötig"
LangString mode_user ${LANG_SPANISH}     "Solo para mí - sin derechos de administrador"
LangString mode_user ${LANG_SIMPCHINESE} "仅为我安装 - 无需管理员权限"

LangString mode_user_dir ${LANG_ENGLISH}     "In $LOCALAPPDATA\${INFO_PRODUCTNAME}"
LangString mode_user_dir ${LANG_FRENCH}      "Dans $LOCALAPPDATA\${INFO_PRODUCTNAME}"
LangString mode_user_dir ${LANG_GERMAN}      "In $LOCALAPPDATA\${INFO_PRODUCTNAME}"
LangString mode_user_dir ${LANG_SPANISH}     "En $LOCALAPPDATA\${INFO_PRODUCTNAME}"
LangString mode_user_dir ${LANG_SIMPCHINESE} "位置：$LOCALAPPDATA\${INFO_PRODUCTNAME}"

LangString mode_admin ${LANG_ENGLISH}     "For all users - Windows asks for administrator rights"
LangString mode_admin ${LANG_FRENCH}      "Pour tous les utilisateurs - Windows demande les droits administrateur"
LangString mode_admin ${LANG_GERMAN}      "Für alle Benutzer - Windows fragt nach Administratorrechten"
LangString mode_admin ${LANG_SPANISH}     "Para todos los usuarios - Windows pide derechos de administrador"
LangString mode_admin ${LANG_SIMPCHINESE} "为所有用户安装 - Windows 会请求管理员权限"

LangString mode_admin_dir ${LANG_ENGLISH}     "In $PROGRAMFILES64\${INFO_PRODUCTNAME}"
LangString mode_admin_dir ${LANG_FRENCH}      "Dans $PROGRAMFILES64\${INFO_PRODUCTNAME}"
LangString mode_admin_dir ${LANG_GERMAN}      "In $PROGRAMFILES64\${INFO_PRODUCTNAME}"
LangString mode_admin_dir ${LANG_SPANISH}     "En $PROGRAMFILES64\${INFO_PRODUCTNAME}"
LangString mode_admin_dir ${LANG_SIMPCHINESE} "位置：$PROGRAMFILES64\${INFO_PRODUCTNAME}"

LangString mode_admin_existing_dir ${LANG_ENGLISH}     "Updates the copy installed in $ExistingAll"
LangString mode_admin_existing_dir ${LANG_FRENCH}      "Met à jour la copie installée dans $ExistingAll"
LangString mode_admin_existing_dir ${LANG_GERMAN}      "Aktualisiert die Kopie in $ExistingAll"
LangString mode_admin_existing_dir ${LANG_SPANISH}     "Actualiza la copia instalada en $ExistingAll"
LangString mode_admin_existing_dir ${LANG_SIMPCHINESE} "更新安装在 $ExistingAll 中的副本"

LangString mode_replace_user ${LANG_ENGLISH}     "Installing for all users replaces the copy installed for you in $ExistingUser: its shortcuts and start at sign-in move to the new copy, and your settings and history stay."
LangString mode_replace_user ${LANG_FRENCH}      "Installer pour tous remplace la copie installée pour vous dans $ExistingUser : ses raccourcis et le démarrage à la connexion passent à la nouvelle copie, vos réglages et votre historique restent."
LangString mode_replace_user ${LANG_GERMAN}      "Die Installation für alle ersetzt die für Sie installierte Kopie in $ExistingUser: Verknüpfungen und Start bei Anmeldung gehen auf die neue Kopie über, Ihre Einstellungen und Ihr Verlauf bleiben."
LangString mode_replace_user ${LANG_SPANISH}     "Instalar para todos reemplaza la copia instalada para usted en $ExistingUser: sus accesos directos y el inicio al iniciar sesión pasan a la nueva copia, y su configuración e historial se conservan."
LangString mode_replace_user ${LANG_SIMPCHINESE} "为所有用户安装将替换为您安装在 $ExistingUser 中的副本：其快捷方式和登录时启动将转到新副本，您的设置和历史记录保留不变。"

LangString mode_service ${LANG_ENGLISH}     "Only an installation for all users, in Program Files, can run as a Windows service, which supervises from the moment the PC starts, even when nobody is signed in."
LangString mode_service ${LANG_FRENCH}      "Seule une installation pour tous les utilisateurs, dans Program Files, peut fonctionner comme service Windows, qui supervise dès le démarrage du PC, même sans session ouverte."
LangString mode_service ${LANG_GERMAN}      "Nur eine Installation für alle Benutzer in Program Files kann als Windows-Dienst laufen, der ab dem Start des PCs überwacht, auch ohne angemeldete Benutzer."
LangString mode_service ${LANG_SPANISH}     "Solo una instalación para todos los usuarios, en Program Files, puede funcionar como servicio de Windows, que supervisa desde que arranca el PC, incluso sin nadie conectado."
LangString mode_service ${LANG_SIMPCHINESE} "只有为所有用户安装在 Program Files 中，才能作为 Windows 服务运行：从电脑启动起即开始监控，即使无人登录。"

LangString opt_header ${LANG_ENGLISH}     "Options"
LangString opt_header ${LANG_FRENCH}      "Options"
LangString opt_header ${LANG_GERMAN}      "Optionen"
LangString opt_header ${LANG_SPANISH}     "Opciones"
LangString opt_header ${LANG_SIMPCHINESE} "选项"

LangString opt_subheader ${LANG_ENGLISH}     "Shortcuts, start at sign-in and the Windows service."
LangString opt_subheader ${LANG_FRENCH}      "Raccourcis, démarrage à la connexion et service Windows."
LangString opt_subheader ${LANG_GERMAN}      "Verknüpfungen, Start bei Anmeldung und Windows-Dienst."
LangString opt_subheader ${LANG_SPANISH}     "Accesos directos, inicio al iniciar sesión y servicio de Windows."
LangString opt_subheader ${LANG_SIMPCHINESE} "快捷方式、登录时启动和 Windows 服务。"

LangString opt_group ${LANG_ENGLISH}     "Options"
LangString opt_group ${LANG_FRENCH}      "Options"
LangString opt_group ${LANG_GERMAN}      "Optionen"
LangString opt_group ${LANG_SPANISH}     "Opciones"
LangString opt_group ${LANG_SIMPCHINESE} "选项"

LangString opt_desktop ${LANG_ENGLISH}     "Create a desktop shortcut"
LangString opt_desktop ${LANG_FRENCH}      "Créer un raccourci sur le bureau"
LangString opt_desktop ${LANG_GERMAN}      "Desktop-Verknüpfung erstellen"
LangString opt_desktop ${LANG_SPANISH}     "Crear un acceso directo en el escritorio"
LangString opt_desktop ${LANG_SIMPCHINESE} "创建桌面快捷方式"

LangString opt_startmenu ${LANG_ENGLISH}     "Create a Start menu shortcut"
LangString opt_startmenu ${LANG_FRENCH}      "Créer un raccourci dans le menu Démarrer"
LangString opt_startmenu ${LANG_GERMAN}      "Startmenü-Verknüpfung erstellen"
LangString opt_startmenu ${LANG_SPANISH}     "Crear un acceso directo en el menú Inicio"
LangString opt_startmenu ${LANG_SIMPCHINESE} "创建开始菜单快捷方式"

LangString opt_autostart ${LANG_ENGLISH}     "Start SnmpLens when I sign in"
LangString opt_autostart ${LANG_FRENCH}      "Démarrer SnmpLens à ma connexion"
LangString opt_autostart ${LANG_GERMAN}      "SnmpLens bei meiner Anmeldung starten"
LangString opt_autostart ${LANG_SPANISH}     "Iniciar SnmpLens al iniciar sesión"
LangString opt_autostart ${LANG_SIMPCHINESE} "登录时启动 SnmpLens"

LangString opt_note ${LANG_ENGLISH}     "All of these can be changed later; start at sign-in in SnmpLens, Settings > Background."
LangString opt_note ${LANG_FRENCH}      "Tout cela se change ensuite ; le démarrage à la connexion dans SnmpLens, Paramètres › Arrière-plan."
LangString opt_note ${LANG_GERMAN}      "All das lässt sich später ändern; den Start bei Anmeldung in SnmpLens, Einstellungen › Hintergrund."
LangString opt_note ${LANG_SPANISH}     "Todo esto se puede cambiar después; el inicio al iniciar sesión en SnmpLens, Ajustes › Segundo plano."
LangString opt_note ${LANG_SIMPCHINESE} "以上选项之后都可更改；登录时启动可在 SnmpLens 的 设置 › 后台 中更改。"

LangString service_checkbox ${LANG_ENGLISH}     "Run SnmpLens as a Windows service"
LangString service_checkbox ${LANG_FRENCH}      "Faire fonctionner SnmpLens comme service Windows"
LangString service_checkbox ${LANG_GERMAN}      "SnmpLens als Windows-Dienst ausführen"
LangString service_checkbox ${LANG_SPANISH}     "Ejecutar SnmpLens como servicio de Windows"
LangString service_checkbox ${LANG_SIMPCHINESE} "将 SnmpLens 作为 Windows 服务运行"

LangString service_note ${LANG_ENGLISH}     "It supervises from boot, with nobody signed in. It starts empty: Settings > Background hands it this computer's sessions and history."
LangString service_note ${LANG_FRENCH}      "Il supervise dès le démarrage, sans session ouverte. Il démarre vide : Paramètres › Arrière-plan lui transmet les sessions et l'historique de cet ordinateur."
LangString service_note ${LANG_GERMAN}      "Er überwacht ab dem Start, ohne angemeldete Benutzer. Er startet leer: Einstellungen › Hintergrund übergibt ihm die Sitzungen und den Verlauf dieses Computers."
LangString service_note ${LANG_SPANISH}     "Supervisa desde el arranque, sin nadie conectado. Arranca vacío: Ajustes › Segundo plano le transfiere las sesiones y el historial de este equipo."
LangString service_note ${LANG_SIMPCHINESE} "它从开机起即开始监控，无需登录。它以空配置启动：设置 › 后台会将本机的会话和历史记录移交给它。"

LangString service_updated ${LANG_ENGLISH}     "The SnmpLens Windows service is installed: it is stopped for the update and started again after."
LangString service_updated ${LANG_FRENCH}      "Le service Windows SnmpLens est installé : il est arrêté le temps de la mise à jour puis relancé."
LangString service_updated ${LANG_GERMAN}      "Der Windows-Dienst SnmpLens ist installiert: Er wird für die Aktualisierung angehalten und danach wieder gestartet."
LangString service_updated ${LANG_SPANISH}     "El servicio de Windows SnmpLens está instalado: se detiene durante la actualización y se vuelve a iniciar después."
LangString service_updated ${LANG_SIMPCHINESE} "SnmpLens Windows 服务已安装：更新期间会停止，更新后重新启动。"

LangString service_failed ${LANG_ENGLISH}     "The SnmpLens Windows service could not be installed. Settings > Background says why and can try again."
LangString service_failed ${LANG_FRENCH}      "Le service Windows SnmpLens n'a pas pu être installé. Paramètres › Arrière-plan dit pourquoi et peut réessayer."
LangString service_failed ${LANG_GERMAN}      "Der Windows-Dienst SnmpLens konnte nicht installiert werden. Einstellungen › Hintergrund nennt den Grund und kann es erneut versuchen."
LangString service_failed ${LANG_SPANISH}     "No se pudo instalar el servicio de Windows SnmpLens. Ajustes › Segundo plano indica el motivo y puede volver a intentarlo."
LangString service_failed ${LANG_SIMPCHINESE} "无法安装 SnmpLens Windows 服务。设置 › 后台会说明原因并可重试。"

LangString finish_run ${LANG_ENGLISH}     "Launch SnmpLens"
LangString finish_run ${LANG_FRENCH}      "Lancer SnmpLens"
LangString finish_run ${LANG_GERMAN}      "SnmpLens starten"
LangString finish_run ${LANG_SPANISH}     "Iniciar SnmpLens"
LangString finish_run ${LANG_SIMPCHINESE} "启动 SnmpLens"

LangString finish_link ${LANG_ENGLISH}     "SnmpLens on GitHub"
LangString finish_link ${LANG_FRENCH}      "SnmpLens sur GitHub"
LangString finish_link ${LANG_GERMAN}      "SnmpLens auf GitHub"
LangString finish_link ${LANG_SPANISH}     "SnmpLens en GitHub"
LangString finish_link ${LANG_SIMPCHINESE} "GitHub 上的 SnmpLens"

LangString un_need_admin ${LANG_ENGLISH}     "SnmpLens was installed for all users: removing it needs administrator rights. Continue?"
LangString un_need_admin ${LANG_FRENCH}      "SnmpLens a été installé pour tous les utilisateurs : le désinstaller demande les droits administrateur. Continuer ?"
LangString un_need_admin ${LANG_GERMAN}      "SnmpLens wurde für alle Benutzer installiert: Zum Entfernen sind Administratorrechte nötig. Fortfahren?"
LangString un_need_admin ${LANG_SPANISH}     "SnmpLens se instaló para todos los usuarios: desinstalarlo requiere derechos de administrador. ¿Continuar?"
LangString un_need_admin ${LANG_SIMPCHINESE} "SnmpLens 是为所有用户安装的：卸载需要管理员权限。是否继续？"

LangString un_service_data ${LANG_ENGLISH}     "Also delete the Windows service's data: its monitoring history and the credentials it supervises with?"
LangString un_service_data ${LANG_FRENCH}      "Supprimer aussi les données du service Windows : son historique de supervision et les identifiants qu'il utilise ?"
LangString un_service_data ${LANG_GERMAN}      "Auch die Daten des Windows-Dienstes löschen: seinen Überwachungsverlauf und die Zugangsdaten, mit denen er überwacht?"
LangString un_service_data ${LANG_SPANISH}     "¿Eliminar también los datos del servicio de Windows: su historial de supervisión y las credenciales que utiliza?"
LangString un_service_data ${LANG_SIMPCHINESE} "是否同时删除 Windows 服务的数据：其监控历史记录和监控所用的凭据？"

; =====================================================================
; Init - language, the copies already there, an elevated relaunch
; =====================================================================

Function .onInit
    ; The installer is a 32-bit program: without this, Windows redirects its
    ; all-users registry entries under WOW6432Node.
    SetRegView 64

    StrCpy $InstallMode "user"
    StrCpy $IsElevated "0"
    StrCpy $OptDesktop ${BST_CHECKED}
    StrCpy $OptStartMenu ${BST_CHECKED}
    StrCpy $OptAutoStart ${BST_UNCHECKED}
    StrCpy $OptService ${BST_UNCHECKED}
    StrCpy $ServiceWasRunning "0"

    ${GetParameters} $0

    ; The language: asked for, or the one the copy that relaunched this chose.
    ClearErrors
    ${GetOptions} $0 "/LANG=" $1
    ${If} ${Errors}
        !insertmacro MUI_LANGDLL_DISPLAY
    ${Else}
        StrCpy $LANGUAGE $1
    ${EndIf}

    ; /SERVICE: install the Windows service too (all users only)
    ClearErrors
    ${GetOptions} $0 "/SERVICE" $1
    ${IfNot} ${Errors}
        StrCpy $OptService ${BST_CHECKED}
    ${EndIf}

    ; The copies already there. Earlier installers wrote the all-users entry in
    ; the 32-bit view; it is read there too, and moved when this one writes.
    ReadRegStr $ExistingUser HKCU "${UNINST_KEY}" "InstallLocation"
    ReadRegStr $ExistingAll HKLM "${UNINST_KEY}" "InstallLocation"
    ${If} $ExistingAll == ""
        SetRegView 32
        ReadRegStr $ExistingAll HKLM "${UNINST_KEY}" "InstallLocation"
        SetRegView 64
    ${EndIf}

    ; Start at sign-in is kept as it was: ticked when there is an entry. An
    ; earlier all-users install wrote it machine-wide from this 32-bit program,
    ; so in the 32-bit view: read in the 64-bit one alone, the box came up
    ; unticked, the old entry was removed, and the update lost it.
    ReadRegStr $1 HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
    ReadRegStr $2 HKLM "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
    ${If} $2 == ""
        SetRegView 32
        ReadRegStr $2 HKLM "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
        SetRegView 64
    ${EndIf}
    ${If} $1 != ""
    ${OrIf} $2 != ""
        StrCpy $OptAutoStart ${BST_CHECKED}
    ${EndIf}

    ClearErrors
    ${GetOptions} $0 "/ALLUSERS" $1
    ${IfNot} ${Errors}
        ; The elevated relaunch, or a silent all-users install.
        StrCpy $InstallMode "admin"
        StrCpy $IsElevated "1"
        SetShellVarContext all
        ${If} $ExistingAll != ""
            StrCpy $INSTDIR $ExistingAll
        ${Else}
            StrCpy $INSTDIR "$PROGRAMFILES64\${INFO_PRODUCTNAME}"
        ${EndIf}
        ; The current user's copy this replaces, when this elevated copy runs
        ; in the same account: an administrator who elevated for someone else
        ; reads another HKCU, and their own copy is not the one that was meant.
        ClearErrors
        ${GetOptions} $0 "/REPLACEUSER=" $1
        ${IfNot} ${Errors}
        ${AndIf} $1 != ""
        ${AndIf} $1 == $ExistingUser
            StrCpy $ReplaceUser $1
        ${EndIf}
    ${ElseIf} $ExistingAll != ""
        ; An all-users copy is updated where it is: a second copy beside it
        ; would leave its service running the old one.
        StrCpy $InstallMode "admin"
        StrCpy $INSTDIR $ExistingAll
        ${If} ${Silent}
            ; Silently, nobody presses Next on the mode page: ask for the
            ; rights here, once, and go on elevated.
            StrCpy $1 "/S /ALLUSERS /LANG=$LANGUAGE"
            ${If} $ExistingUser != ""
                StrCpy $1 '$1 /REPLACEUSER="$ExistingUser"'
            ${EndIf}
            ${If} $OptService == ${BST_CHECKED}
                StrCpy $1 "$1 /SERVICE"
            ${EndIf}
            ExecShell "runas" "$EXEPATH" "$1"
            Quit
        ${EndIf}
    ${ElseIf} $ExistingUser != ""
        StrCpy $INSTDIR $ExistingUser
    ${Else}
        StrCpy $INSTDIR "$LOCALAPPDATA\${INFO_PRODUCTNAME}"
    ${EndIf}

    ; The Windows service, when it exists: stopped for the update, started
    ; again after.
    StrCpy $ServiceInstalled "0"
    nsExec::ExecToStack 'sc.exe query ${SERVICE_NAME}'
    Pop $1
    Pop $2
    ${If} $1 == 0
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

    !insertmacro MUI_HEADER_TEXT "$(mode_header)" "$(mode_subheader)"
    nsDialogs::Create 1018
    Pop $0

    ${NSD_CreateGroupBox} 10u 0u 280u 76u "$(mode_group)"
    Pop $0

    ${NSD_CreateRadioButton} 25u 14u 250u 14u "$(mode_user)"
    Pop $RadioUser

    ${NSD_CreateLabel} 40u 29u 240u 10u "$(mode_user_dir)"
    Pop $0

    ${NSD_CreateRadioButton} 25u 44u 250u 14u "$(mode_admin)"
    Pop $RadioAdmin

    ${If} $ExistingAll != ""
        ${NSD_CreateLabel} 40u 59u 240u 10u "$(mode_admin_existing_dir)"
    ${Else}
        ${NSD_CreateLabel} 40u 59u 240u 10u "$(mode_admin_dir)"
    ${EndIf}
    Pop $0

    ; An all-users copy is there already: updating it is the default.
    ${If} $InstallMode == "admin"
        ${NSD_SetState} $RadioAdmin ${BST_CHECKED}
    ${Else}
        ${NSD_SetState} $RadioUser ${BST_CHECKED}
    ${EndIf}

    ; What choosing all users does to the copy installed for this user —
    ; whether or not an all-users copy is there already, since either way
    ; InstallModeLeave hands it to the elevated copy to replace.
    ${If} $ExistingUser != ""
    ${AndIf} $ExistingUser != $ExistingAll
        ${NSD_CreateLabel} 10u 82u 280u 26u "$(mode_replace_user)"
        Pop $0
    ${EndIf}

    ${NSD_CreateLabel} 10u 110u 280u 26u "$(mode_service)"
    Pop $0

    nsDialogs::Show
FunctionEnd

Function InstallModeLeave
    ${NSD_GetState} $RadioAdmin $0
    ${If} $0 == ${BST_CHECKED}
        StrCpy $1 "/ALLUSERS /LANG=$LANGUAGE"
        ${If} $ExistingUser != ""
            StrCpy $1 '$1 /REPLACEUSER="$ExistingUser"'
        ${EndIf}
        ${If} $OptService == ${BST_CHECKED}
            StrCpy $1 "$1 /SERVICE"
        ${EndIf}
        ExecShell "runas" "$EXEPATH" "$1"
        Quit
    ${Else}
        StrCpy $InstallMode "user"
        SetShellVarContext current
        ${If} $ExistingUser != ""
            StrCpy $INSTDIR $ExistingUser
        ${Else}
            StrCpy $INSTDIR "$LOCALAPPDATA\${INFO_PRODUCTNAME}"
        ${EndIf}
    ${EndIf}
FunctionEnd

; =====================================================================
; Custom page: Options (shortcuts, start at sign-in, the service)
; =====================================================================

; $0 is "1" when $INSTDIR is under Program Files, where only administrators
; can write: the only place the service may run from.
; The finish page's launch. An all-users install runs elevated, and so would
; the application started from it — as the administrator, who may not be the
; user signed in, with drag and drop from Explorer refused by the integrity
; level, and a second launch from a shortcut unable to reach it, so that
; nothing seemed to open. Explorer starts it as the signed-in user, unelevated.
Function LaunchApp
    ${If} $InstallMode == "admin"
        Exec '"$WINDIR\explorer.exe" "$INSTDIR\${PRODUCT_EXECUTABLE}"'
    ${Else}
        Exec '"$INSTDIR\${PRODUCT_EXECUTABLE}"'
    ${EndIf}
FunctionEnd

Function InProgramFiles
    StrCpy $0 "0"
    StrLen $2 "$PROGRAMFILES64"
    StrCpy $1 "$INSTDIR" $2
    ${If} $1 == "$PROGRAMFILES64"
        StrCpy $0 "1"
    ${EndIf}
    StrLen $2 "$PROGRAMFILES32"
    StrCpy $1 "$INSTDIR" $2
    ${If} $1 == "$PROGRAMFILES32"
        StrCpy $0 "1"
    ${EndIf}
FunctionEnd

Function OptionsPage
    !insertmacro MUI_HEADER_TEXT "$(opt_header)" "$(opt_subheader)"
    nsDialogs::Create 1018
    Pop $0

    ${NSD_CreateGroupBox} 10u 0u 280u 134u "$(opt_group)"
    Pop $0

    ${NSD_CreateCheckBox} 25u 14u 250u 14u "$(opt_desktop)"
    Pop $ChkDesktop
    ${NSD_SetState} $ChkDesktop $OptDesktop

    ${NSD_CreateCheckBox} 25u 30u 250u 14u "$(opt_startmenu)"
    Pop $ChkStartMenu
    ${NSD_SetState} $ChkStartMenu $OptStartMenu

    ${NSD_CreateCheckBox} 25u 46u 250u 14u "$(opt_autostart)"
    Pop $ChkAutoStart
    ${NSD_SetState} $ChkAutoStart $OptAutoStart

    ${If} $InstallMode == "admin"
        ${If} $ServiceInstalled == "1"
            ${NSD_CreateLabel} 25u 66u 255u 28u "$(service_updated)"
            Pop $0
        ${Else}
            ${NSD_CreateCheckBox} 25u 66u 255u 14u "$(service_checkbox)"
            Pop $ChkService
            ${NSD_SetState} $ChkService $OptService
            Call InProgramFiles
            ${If} $0 == "1"
                ${NSD_CreateLabel} 37u 82u 245u 28u "$(service_note)"
                Pop $0
            ${Else}
                EnableWindow $ChkService 0
                ${NSD_SetState} $ChkService ${BST_UNCHECKED}
                ${NSD_CreateLabel} 37u 82u 245u 28u "$(mode_service)"
                Pop $0
            ${EndIf}
        ${EndIf}
    ${EndIf}

    ${NSD_CreateLabel} 25u 114u 255u 18u "$(opt_note)"
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
; The service, and the copy an all-users install replaces
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

; Removes what the current user's own uninstaller would, except what the new
; copy shares with it: the settings and the history live per user whichever
; copy runs. Its start at sign-in now starts the new copy, and its desktop
; shortcut gives way to the all-users one — recreated beside it, it put two
; SnmpLens icons on this user's desktop — so nothing the user set up goes
; missing and nothing appears twice.
Function ReplaceUserCopy
    SetShellVarContext current
    ${If} $ReplaceUser != $INSTDIR
        Delete "$ReplaceUser\${PRODUCT_EXECUTABLE}"
        Delete "$ReplaceUser\uninstall.exe"
        RMDir "$ReplaceUser"
    ${EndIf}
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}\${INFO_PRODUCTNAME}.lnk"
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}\Uninstall ${INFO_PRODUCTNAME}.lnk"
    RMDir "$SMPROGRAMS\${INFO_PRODUCTNAME}"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
    ReadRegStr $0 HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
    ${StrStr} $1 $0 "$ReplaceUser\"
    ${If} $1 != ""
        StrCpy $OptAutoStart ${BST_CHECKED}
    ${EndIf}
    DeleteRegKey HKCU "${UNINST_KEY}"
    SetShellVarContext all
FunctionEnd

; =====================================================================
; Install
; =====================================================================

Section "Install"
    ; The service first: it runs this very executable.
    Call StopService

    ; Kill running instances before install/upgrade — the replaced per-user
    ; copy's too.
    nsExec::ExecToLog 'taskkill /F /IM "${PRODUCT_EXECUTABLE}"'
    Pop $0

    SetOutPath "$INSTDIR"

    ; Application
    File "..\..\..\build\bin\${PRODUCT_EXECUTABLE}"

    ; Uninstaller
    WriteUninstaller "$INSTDIR\uninstall.exe"

    ; The copy installed for this user, replaced by this one.
    ${If} $ReplaceUser != ""
        Call ReplaceUserCopy
    ${EndIf}

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

    ; Start at sign-in: the current user's entry, quoted — Program Files has a
    ; space — exactly as Settings > Background writes it, so that the
    ; application sees it and can remove it. A machine-wide entry earlier
    ; installers wrote is one it could neither see nor remove: it goes.
    ${If} $OptAutoStart == ${BST_CHECKED}
        WriteRegStr HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}" '"$INSTDIR\${PRODUCT_EXECUTABLE}"'
    ${Else}
        DeleteRegValue HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
    ${EndIf}
    ${If} $InstallMode == "admin"
        DeleteRegValue HKLM "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
        SetRegView 32
        DeleteRegValue HKLM "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
        SetRegView 64
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
        ; The entry an earlier installer left in the 32-bit view, moved: two
        ; entries would show SnmpLens twice in Installed apps.
        SetRegView 32
        DeleteRegKey HKLM "${UNINST_KEY}"
        SetRegView 64
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
    SetRegView 64
    !insertmacro MUI_UNGETLANGUAGE
    ; All users when the all-users entry is this very copy: a per-user copy can
    ; sit beside an all-users one, and removing it must not take the other's
    ; entry, or its service, with it. Earlier installers wrote that entry in the
    ; 32-bit view.
    StrCpy $InstallMode "user"
    ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
    ${If} $0 == ""
        SetRegView 32
        ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
        SetRegView 64
    ${EndIf}
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

    ; Start at sign-in, when it starts this copy: left behind, it would run a
    ; file that no longer exists at every sign-in. And any machine-wide entry,
    ; in both views.
    ReadRegStr $0 HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
    ${UnStrStr} $1 $0 "$INSTDIR\"
    ${If} $1 != ""
        DeleteRegValue HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
    ${EndIf}
    ${If} $InstallMode == "admin"
        DeleteRegValue HKLM "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
        SetRegView 32
        DeleteRegValue HKLM "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}"
        SetRegView 64
    ${EndIf}

    ; Registry: this copy's entry only, in both views.
    ${If} $InstallMode == "admin"
        DeleteRegKey HKLM "${UNINST_KEY}"
        SetRegView 32
        DeleteRegKey HKLM "${UNINST_KEY}"
        SetRegView 64
    ${Else}
        DeleteRegKey HKCU "${UNINST_KEY}"
    ${EndIf}
SectionEnd
