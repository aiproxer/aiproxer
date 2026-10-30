package configsource

import (
	"crypto/sha256"
	"testing"
)

// TestClassifyAtomicReplacement_SchemeTransitionFailsClosed proves that an
// identity-provenance change cannot downgrade an in-place rewrite to eligible.
// If the accepted baseline was produced by statx (device+inode+birth time) and
// a later read falls back to device+inode only, the coarser identity cannot
// prove the candidate is a different physical file, so classification must
// reject rather than publish.
func TestClassifyAtomicReplacement_SchemeTransitionFailsClosed(t *testing.T) {
	t.Parallel()
	digestA := sha256.Sum256([]byte("body-a"))
	digestB := sha256.Sum256([]byte("body-b"))

	statxID := FileIdentity{Platform: "linux", Scheme: identitySchemeStatxBirthTime, Opaque: sha256.Sum256([]byte("statx"))}
	fallbackID := FileIdentity{Platform: "linux", Scheme: identitySchemeDeviceInode, Opaque: sha256.Sum256([]byte("fallback"))}
	otherPlatformID := FileIdentity{Platform: "other", Scheme: identitySchemeDeviceInode, Opaque: sha256.Sum256([]byte("fallback"))}

	cases := []struct {
		name      string
		active    FileIdentity
		candidate FileIdentity
	}{
		{name: "statx_then_fallback", active: statxID, candidate: fallbackID},
		{name: "fallback_then_statx", active: fallbackID, candidate: statxID},
		{name: "platform_change", active: fallbackID, candidate: otherPlatformID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			active := ActiveSourceVersion{HandleIdentity: tc.active, PrivateDigest: digestA}
			cand := SourceSnapshot{HandleIdentity: tc.candidate, PrivateDigest: digestB, Bytes: []byte("body-b")}
			res, cat, err := ClassifyAtomicReplacement(active, cand)
			if res != AtomicReject || cat != CategoryNonAtomicUpdate || err == nil {
				t.Fatalf("provenance change must fail closed: res=%q cat=%q err=%v", res, cat, err)
			}
		})
	}
}
