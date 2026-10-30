package trust

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

func resolveUnderRoot(root, rel string) (string, Reason, error) {
	if root == "" {
		return "", ReasonRootRequired, fmt.Errorf("empty root")
	}
	if rel == "" || filepath.IsAbs(rel) || path.IsAbs(rel) || strings.Contains(rel, `\`) {
		return "", ReasonPathEscape, fmt.Errorf("absolute or non-portable executable path")
	}
	cleanRel := path.Clean(rel)
	if cleanRel == ".." || strings.HasPrefix(cleanRel, "../") || cleanRel != rel {
		return "", ReasonPathEscape, fmt.Errorf("escape")
	}
	joined := filepath.Join(root, filepath.FromSlash(rel))
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", ReasonPathEscape, err
	}
	cleanJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", ReasonPathEscape, err
	}
	relOut, err := filepath.Rel(cleanRoot, cleanJoined)
	if err != nil || relOut == ".." || strings.HasPrefix(relOut, ".."+string(filepath.Separator)) {
		return "", ReasonPathEscape, fmt.Errorf("escape")
	}
	return cleanJoined, ReasonOK, nil
}
