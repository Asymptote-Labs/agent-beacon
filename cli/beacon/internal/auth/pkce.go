package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const (
	CodeVerifierLength = 32
	StateLength        = 32
)

type PKCEParams struct {
	CodeVerifier  string
	CodeChallenge string
	State         string
}

func GeneratePKCE() (*PKCEParams, error) {
	verifierBytes := make([]byte, CodeVerifierLength)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, err
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	hash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	stateBytes := make([]byte, StateLength)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, err
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	return &PKCEParams{
		CodeVerifier:  codeVerifier,
		CodeChallenge: codeChallenge,
		State:         state,
	}, nil
}
