//go:build windows

package trust

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
	"golang.org/x/sys/windows"
)

// bindVerified stages digest-addressed bytes under a private directory with a
// restrictive DACL. The exclusive destination handle stays open for write,
// sync, seek, hash, ACL apply/readback, and LaunchSource retention.
// CreateProcess is intentionally not called (Phase 3).
//
// Residual note: Windows may retain SYSTEM/Administrators privileged access
// outside this DACL via OS policy. We verify protected owner-user ALLOW and
// reject unexpected non-privileged trustees; we do not claim absolute
// exclusion of SYSTEM/admin residual rights.
func bindVerified(f *os.File, m sdkmanifest.Manifest, digest string, opt VerifyOptions) VerifyResult {
	if opt.StagingDir == "" {
		_ = f.Close()
		return VerifyResult{Reason: ReasonStagingUnsupported, Err: fmt.Errorf("staging dir required")}
	}
	if err := ensurePrivateStagingDir(opt.StagingDir); err != nil {
		_ = f.Close()
		return VerifyResult{Reason: mapStagingReason(err), Err: err}
	}
	staged := filepath.Join(opt.StagingDir, digest+".exe")
	sf, err := exclusiveStageHeld(f, staged, digest)
	_ = f.Close()
	if err != nil {
		return VerifyResult{Reason: mapStagingReason(err), Err: err}
	}
	return VerifyResult{
		Artifact: &VerifiedArtifact{
			Manifest: m, DigestHex: digest, Strategy: BindingProtectedStaging,
			StagedPath: staged, file: sf,
		},
		Reason: ReasonOK,
	}
}

func mapStagingReason(err error) Reason {
	switch {
	case errors.Is(err, ReasonACLUnverified):
		return ReasonACLUnverified
	case errors.Is(err, ReasonSubstitution):
		return ReasonSubstitution
	default:
		return ReasonStagingFailed
	}
}

func ensurePrivateStagingDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return applyAndVerifyRestrictiveACLByPath(dir)
}

// exclusiveStageHeld creates dest exclusively, copies from src, syncs, rewinds,
// hashes, applies+verifies ACL — all on the same held destination handle.
func exclusiveStageHeld(src *os.File, dest, digest string) (*os.File, error) {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ | windows.GENERIC_WRITE | windows.WRITE_DAC | windows.READ_CONTROL)
	h, err := windows.CreateFile(
		p,
		access,
		0, // exclusive
		nil,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	dst := os.NewFile(uintptr(h), dest)
	if dst == nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("newfile")
	}
	cleanup := func() {
		_ = dst.Close()
		_ = os.Remove(dest)
	}
	if _, err := io.Copy(dst, src); err != nil {
		cleanup()
		return nil, err
	}
	if err := dst.Sync(); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	sum, err := hashFile(dst)
	if err != nil {
		cleanup()
		return nil, err
	}
	if sum != digest {
		cleanup()
		return nil, fmt.Errorf("%w", ReasonSubstitution)
	}
	if err := applyRestrictiveACLHandle(h); err != nil {
		cleanup()
		return nil, err
	}
	if err := verifyRestrictiveACLHandle(h); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: %v", ReasonACLUnverified, err)
	}
	id, err := fileIdentity(h)
	if err != nil {
		cleanup()
		return nil, err
	}
	_ = dst.Close()
	// Reopen launch-ready: FILE_SHARE_READ only (no write/delete). CreateProcess
	// succeeds with this share; rename/delete remain denied while held.
	h2, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		_ = os.Remove(dest)
		return nil, err
	}
	id2, err := fileIdentity(h2)
	if err != nil || id2 != id {
		_ = windows.CloseHandle(h2)
		_ = os.Remove(dest)
		return nil, fmt.Errorf("%w: launch reopen identity mismatch", ReasonSubstitution)
	}
	out := os.NewFile(uintptr(h2), dest)
	if out == nil {
		_ = windows.CloseHandle(h2)
		_ = os.Remove(dest)
		return nil, fmt.Errorf("newfile")
	}
	return out, nil
}

func currentUserSID() (*windows.SID, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer func() { _ = token.Close() }()
	userToken, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return userToken.User.Sid, nil
}

func ownerOnlyDACL(sid *windows.SID) (*windows.ACL, error) {
	return windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
}

func applyRestrictiveACLHandle(h windows.Handle) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("acl token: %w", err)
	}
	dacl, err := ownerOnlyDACL(sid)
	if err != nil {
		return fmt.Errorf("acl build: %w", err)
	}
	if err := windows.SetSecurityInfo(
		h,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		return fmt.Errorf("acl set: %w", err)
	}
	return nil
}

func applyAndVerifyRestrictiveACLByPath(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("acl token: %w", err)
	}
	dacl, err := ownerOnlyDACL(sid)
	if err != nil {
		return fmt.Errorf("acl build: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		return fmt.Errorf("acl set: %w", err)
	}
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("%w: readback: %v", ReasonACLUnverified, err)
	}
	return verifyRestrictiveSD(sd, sid)
}

func verifyRestrictiveACLHandle(h windows.Handle) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(
		h,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	return verifyRestrictiveSD(sd, sid)
}

func verifyRestrictiveSD(sd *windows.SECURITY_DESCRIPTOR, userSID *windows.SID) error {
	ctrl, _, err := sd.Control()
	if err != nil {
		return err
	}
	if ctrl&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("dacl not protected")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("missing dacl")
	}
	if dacl.AceCount == 0 {
		return fmt.Errorf("empty dacl")
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	adminsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	sawUser := false
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("non-allow ace")
		}
		entrySID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case userSID.Equals(entrySID):
			sawUser = true
		case systemSID.Equals(entrySID), adminsSID.Equals(entrySID):
			// Documented residual privileged trustees; accepted only as residual.
		default:
			return fmt.Errorf("unexpected trustee %s", entrySID.String())
		}
	}
	if !sawUser {
		return fmt.Errorf("owner user ace missing")
	}
	return nil
}
