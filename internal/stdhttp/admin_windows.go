//go:build windows

package stdhttp

import "golang.org/x/sys/windows"

func detectRunningAsAdmin() (bool, error) {
	token := windows.GetCurrentProcessToken()
	sid, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}
	admin, err := token.IsMember(sid)
	if err != nil {
		return false, err
	}
	return admin, nil
}
