package dockmcp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Luo-root/pulsar-dock/internal/artifactstore"
)

func validatePersistentPublish(input PublishInput) error {
	if len(input.Payload) == 0 || len(input.Payload) > artifactstore.MaxBytes || !utf8.ValidString(input.Payload) {
		return errors.New("payload must be nonempty UTF-8 text of at most 4194304 bytes")
	}
	for _, value := range []string{input.Kind, input.BaselineDigest, input.IdempotencyKey} {
		if strings.TrimSpace(value) == "" || len(value) > 512 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return errors.New("kind, baseline_digest and idempotency_key must be nonempty UTF-8 values of at most 512 bytes without NUL")
		}
	}
	if input.Digest != "" {
		digest := sha256.Sum256([]byte(input.Payload))
		if input.Digest != hex.EncodeToString(digest[:]) {
			return errors.New("payload digest mismatch")
		}
	}
	return nil
}
