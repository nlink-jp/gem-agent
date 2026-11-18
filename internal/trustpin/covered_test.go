package trustpin

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// covered is what a skill's digest is computed from, by name: each
// covered file's line, and a link's target. The tests of the walk's
// bounds read it to see what the walk reached.
func covered(root *os.Root) (files map[string]string, cut bool) {
	entries, cut := manifestRoot(root, SkillFiles, SkillWalk, SkillDepth)
	files = make(map[string]string, len(entries))
	for _, e := range entries {
		files[e.rel] = e.line
	}
	return files, cut
}

// contentLine is the line of a file whose whole content is data.
func contentLine(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}
