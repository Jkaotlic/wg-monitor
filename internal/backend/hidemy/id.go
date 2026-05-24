package hidemy

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

func ServerID(serverIP string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(serverIP)))
	return fmt.Sprintf("%x", sum[:6])
}
