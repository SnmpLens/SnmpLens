//go:build windows

package sysvc

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Access masks used below. Modify is read, write, execute and delete — enough
// for the service to run its database — without the right to change who else
// may get in.
const (
	accessFull   = "FA"
	accessModify = "0x1301bf"
	accessRead   = "FR"
)

// dirSDDL is the access rules of a directory of the service's: the service
// account, SYSTEM and administrators, and — for the status directory only —
// read access for local users. Protected, so nothing is inherited from above
// and a permissive %ProgramData% cannot leak in.
func dirSDDL(serviceSID string, usersMayRead bool) string {
	sddl := fmt.Sprintf("D:PAI(A;OICI;%s;;;SY)(A;OICI;%s;;;BA)(A;OICI;%s;;;%s)",
		accessFull, accessFull, accessModify, serviceSID)
	if usersMayRead {
		sddl += fmt.Sprintf("(A;OICI;%s;;;BU)", accessRead)
	}
	return sddl
}

// secureDir replaces dir's access rules with dirSDDL's.
func secureDir(dir string, serviceSID string, usersMayRead bool) error {
	sd, err := windows.SecurityDescriptorFromString(dirSDDL(serviceSID, usersMayRead))
	if err != nil {
		return fmt.Errorf("build the access rules for %s: %w", dir, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("build the access rules for %s: %w", dir, err)
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("apply the access rules to %s: %w", dir, err)
	}
	return nil
}

// serviceSID is the security identifier of the service's virtual account. It
// only resolves once the service exists.
func serviceSID() (string, error) {
	sid, _, _, err := windows.LookupSID("", Account)
	if err != nil {
		return "", fmt.Errorf("look up %s: %w", Account, err)
	}
	return sid.String(), nil
}
