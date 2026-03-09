package db

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

var RootHash = computeSHA256("ROOT")

func computeSHA256(data string) string {
	sum := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", sum)
}

func NodeHash(parentHash string, msgJSON []byte) string {
	return computeSHA256(parentHash + "\n" + string(msgJSON))
}

func CanonicalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
