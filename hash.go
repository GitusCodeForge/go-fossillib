package fossillib

import (
	"crypto/sha1"
	"crypto/sha3"
	"encoding/hex"
	"strings"
)

// artifactFullHash recovers the complete artifact name from its content and a
// prefix. Fossil names an artifact by the SHA1 or SHA3-256 hash of its raw
// bytes, so a fetched artifact can be canonicalized without another process.
//
// A 40-character prefix is ambiguous: it is a complete SHA1 name or the first
// 40 characters of a SHA3-256 name. An exact SHA1 match wins; otherwise the
// SHA3-256 name is used when it matches.
func artifactFullHash(raw []byte, prefix string) string {
	prefix = normalizeHash(prefix)
	if prefix == "" {
		return ""
	}
	if len(prefix) == 64 {
		sum := sha3.Sum256(raw)
		full := hex.EncodeToString(sum[:])
		if strings.HasPrefix(full, prefix) {
			return full
		}
		return ""
	}
	sha1sum := sha1.Sum(raw)
	sha1hex := hex.EncodeToString(sha1sum[:])
	if sha1hex == prefix {
		return sha1hex
	}
	sha3sum := sha3.Sum256(raw)
	sha3hex := hex.EncodeToString(sha3sum[:])
	if strings.HasPrefix(sha3hex, prefix) {
		return sha3hex
	}
	if strings.HasPrefix(sha1hex, prefix) {
		return sha1hex
	}
	return ""
}
