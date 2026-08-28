// Package config enumerates the environment the ConfigHub server reads,
// and renders an initial deployment from it.
//
// A server reads its configuration variable by variable at the point of use,
// which answers "what does this line need" and not "what does this server need
// to run" -- the question anyone deploying one has to answer.
//
// This package answers it as a table, and generates deployment configuration
// from that table. Config produced this way cannot name a variable the server
// does not read, because there is nowhere for one to come from; a
// hand-maintained file can, and eventually does.
//
// It is imported by the server itself and by installers. Not by cub: validating
// a public JWK goes through jwk/publickey, which is a separate package precisely
// so that jwkset stays out of cub and cub-worker, and importing this would put
// it back.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/confighub/sdk/core/jwk"
	"github.com/confighub/sdk/core/jwk/publickey"
)

// Constants describing the identity a fresh server bootstraps with.
//
// They live here rather than in the server because the generator needs them
// before any server exists: it writes the administrator's private key, and a
// key is unusable unless it names the identity it belongs to. The server reads
// the same constants back, so there is one definition rather than two that have
// to agree.
const (
	// AdminJWKEnv carries the bootstrap administrator's PUBLIC key. Not a
	// secret: the whole design stores public keys only, which is what makes it
	// safe in a ConfigMap.
	AdminJWKEnv = "CONFIGHUB_LOCAL_ADMIN_JWK"

	// LocalIdentityPrefix marks an identity ConfigHub owns rather than mirrors
	// from an identity provider.
	LocalIdentityPrefix = "confighub:local:"

	LocalOrgExternalID   = LocalIdentityPrefix + "org:default"
	LocalAdminExternalID = LocalIdentityPrefix + "admin"
)

// Placement decides which object a variable is rendered into, and it is a
// property of the variable rather than of the renderer.
//
// Getting this wrong once puts a database password in a ConfigMap, so it is
// stated per-variable here rather than decided at render time. A deployment
// that sources secrets from a secret manager gets the split from that manager;
// generated config has no such thing, so the split comes from here.
type Placement int

const (
	// InConfigMap is for values that are not sensitive. They may be committed,
	// logged, and read by anyone who can read the namespace.
	InConfigMap Placement = iota

	// InSecret is for values that are. Note that "secret" here means only that
	// the value must not be world-readable -- it says nothing about who may
	// write it, which for public keys is the property that actually matters.
	InSecret
)

// Var is one environment variable the server reads.
type Var struct {
	Name      string
	Placement Placement

	// Doc is why this exists, in one line, for the comment above the key.
	Doc string

	// Value is what will be rendered. Empty means the variable is omitted
	// entirely rather than rendered as "", so that an unset optional variable
	// and one deliberately set to empty stay distinguishable.
	Value string

	// Generated marks a value this package minted rather than one the caller
	// supplied. Generated values must survive re-running the generator: a fresh
	// JWT_PRIVATE_KEY_JWK invalidates every session, and a fresh
	// WORKER_MASTER_SECRET breaks every worker that has already enrolled.
	Generated bool
}

// RandomSecret returns 32 bytes of randomness, base64url encoded.
//
// Used for values that are pure entropy with no structure -- the worker master
// secret and the generated database password. 256 bits because these are HMAC
// keys and passwords that are never typed by a human, so there is no reason to
// be shorter.
func RandomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("reading randomness: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GenerateSigningKey produces a JWT_PRIVATE_KEY_JWK value.
//
// This is the key the server signs every ConfigHub token with. It is optional
// today, and when it is unset InitJWTService generates a fresh one at every
// start -- which silently invalidates all sessions on restart and makes more
// than one replica reject each other's tokens. Generating it here is what makes
// the default path the correct one rather than something an operator has to
// know to do.
func GenerateSigningKey() (string, error) {
	privateJWK, _, err := jwk.GenerateRSASigningKey()
	if err != nil {
		return "", fmt.Errorf("generating a signing key: %w", err)
	}
	return privateJWK, nil
}

// AdminKeypair is the operator's credential for the bootstrap administrator.
//
// The two halves have opposite destinations and must not be handled together.
// The public half is not sensitive and belongs in ordinary configuration:
// ConfigHub stores public keys, never a password verifier. The private half
// must never reach the cluster, since that is the whole reason the public half
// is safe to publish.
type AdminKeypair struct {
	PublicJWK  string
	PrivateJWK string
}

// GenerateAdminKeypair mints an Ed25519 keypair for the bootstrap administrator.
//
// Ed25519 rather than RSA: it is in the assertion algorithm allowlist, the keys
// are short enough to paste into an environment variable without wrapping, and
// there is no parameter to choose badly.
// The private half records the administrator's external id, which an assertion
// signed with this key carries as its issuer. A readable constant, so nothing
// has to derive or look up an identifier before the server exists.
func GenerateAdminKeypair() (*AdminKeypair, error) {
	pair, err := jwk.GenerateEd25519(LocalAdminExternalID)
	if err != nil {
		return nil, fmt.Errorf("generating an administrator keypair: %w", err)
	}
	return &AdminKeypair{
		PublicJWK:  string(pair.PublicJWK),
		PrivateJWK: string(pair.PrivateJWK),
	}, nil
}

// ValidateAdminPublicJWK rejects anything that is not a usable public key,
// including a JWK carrying private material.
//
// The server performs this check too, at bootstrap. Doing it here as well means
// a typo is caught while the operator is still looking at the terminal, rather
// than at first start when they have moved on.
//
// It calls the server's own check rather than approximating it, so the two
// cannot disagree: a key accepted here and refused at bootstrap would be exactly
// the failure this is meant to prevent. That is what brings jwkset in through
// jwk/publickey -- see the package comment.
func ValidateAdminPublicJWK(raw string) error {
	if !json.Valid([]byte(raw)) {
		return fmt.Errorf("not valid JSON: expected a public JWK object")
	}
	if _, err := publickey.FromJWK(json.RawMessage(raw)); err != nil {
		return fmt.Errorf("not a usable public JWK: %w", err)
	}
	return nil
}

// PublicJWKFromPrivate recovers the public half of a stored private key.
//
// The two halves differ only by the members that carry secret material and the
// one naming the identity, so the public key an instance should trust can be
// derived from the private key its operator already holds. That is what lets an
// uninstall followed by an install keep working with the key you have, rather
// than stranding it.
//
// Validated on the way out, so a corrupt or unsupported key is refused here
// rather than at the server's first start.
func PublicJWKFromPrivate(privateJWK []byte) (string, error) {
	var members map[string]any
	if err := json.Unmarshal(privateJWK, &members); err != nil {
		return "", fmt.Errorf("not a usable private key: %w", err)
	}

	// Everything that must not be published: secret material across every key
	// type, plus the member recording which identity holds the key, which is a
	// property of the holder's copy and not of the key.
	for _, member := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k", jwk.UserExternalIDMember} {
		delete(members, member)
	}

	out, err := json.Marshal(members)
	if err != nil {
		return "", err
	}
	if _, err := publickey.FromJWK(out); err != nil {
		return "", fmt.Errorf("the stored private key does not yield a usable public key: %w", err)
	}
	return string(out), nil
}
