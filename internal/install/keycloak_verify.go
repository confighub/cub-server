package install

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/confighub/sdk/core/jwk"

	"github.com/confighub/cub-server/internal/config"
)

// signClientAssertion builds an RFC 7523 client assertion signed with an
// Ed25519 private JWK, with the kid Keycloak selects the public half by.
func signClientAssertion(privateJWK, clientID, audience string) (string, error) {
	var key struct {
		D string `json:"d"`
	}
	if err := json.Unmarshal([]byte(privateJWK), &key); err != nil || key.D == "" {
		return "", fmt.Errorf("the server's key is not an Ed25519 private JWK")
	}
	seed, err := base64.RawURLEncoding.DecodeString(key.D)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("the server's key is not an Ed25519 private JWK")
	}
	public, err := config.PublicJWKFromPrivate([]byte(privateJWK))
	if err != nil {
		return "", err
	}
	kid, err := jwk.Thumbprint(json.RawMessage(public))
	if err != nil {
		return "", err
	}

	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": kid})
	claims, _ := json.Marshal(map[string]any{
		"iss": clientID,
		"sub": clientID,
		"aud": audience,
		"jti": base64.RawURLEncoding.EncodeToString(jti),
		"iat": now.Unix(),
		"exp": now.Add(time.Minute).Unix(),
	})
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
