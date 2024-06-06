// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package msal wraps Microsoft Authentication Library, providing transport for
// authenticated requests compatible with [http.RoundTripper].
package msal

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"golang.org/x/crypto/pkcs12"
)

// MicrosoftAuthority is the authority for Microsoft accounts.
const MicrosoftAuthority = "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47"

// ConfidentialCredentialTransport is an http.RoundTripper that makes requests
// with the "Authorization Bearer" header set to the token acquired from the
// confidential client.
type ConfidentialCredentialTransport struct {
	Client confidential.Client
	Scopes []string

	// Transport is the underlying HTTP transport to use when making requests.
	// It will default to http.DefaultTransport if nil.
	Transport http.RoundTripper
}

// NewConfidentialTransportFromSecret creates a new ConfidentialCredentialTransport.
// authority is the URL of a token authority such as "https://login.microsoftonline.com/<your tenant>".
func NewConfidentialTransportFromSecret(authority, clientID, clientSecret string) (*ConfidentialCredentialTransport, error) {
	cred, err := confidential.NewCredFromSecret(clientSecret)
	if err != nil {
		return nil, err
	}
	client, err := confidential.New(authority, clientID, cred)
	if err != nil {
		return nil, err
	}
	return &ConfidentialCredentialTransport{Client: client}, nil
}

// NewConfidentialTransportFromAzureKeyVaultJSON creates a new ConfidentialCredentialTransport.
//
// authority is the URL of a token authority such as
// "https://login.microsoftonline.com/<your tenant>".
//
// vaultJSON is the JSON content of a certificate stored in Azure Key Vault, as
// returned by 'az keyvault secret show'. It should be a JSON object with a
// property 'value' that contains a base64-encoded PFX-encoded certificate with
// private key.
func NewConfidentialTransportFromAzureKeyVaultJSON(authority, clientID string, vaultJSON []byte) (*ConfidentialCredentialTransport, error) {
	cred, err := newCredFromAzureKeyVaultJSON(vaultJSON)
	if err != nil {
		return nil, err
	}
	client, err := confidential.New(authority, clientID, cred, confidential.WithX5C())
	if err != nil {
		return nil, err
	}
	return &ConfidentialCredentialTransport{Client: client}, nil
}

// RoundTrip implements the RoundTripper interface.
func (t *ConfidentialCredentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	auth, err := t.Client.AcquireTokenSilent(req.Context(), t.Scopes)
	if err != nil {
		// Cache miss, authenticate with AcquireTokenByCredential.
		auth, err = t.Client.AcquireTokenByCredential(req.Context(), t.Scopes)
		if err != nil {
			return nil, err
		}
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	return t.transport().RoundTrip(req)
}

func (t *ConfidentialCredentialTransport) transport() http.RoundTripper {
	if t.Transport != nil {
		return t.Transport
	}
	return http.DefaultTransport
}

// newCredFromAzureKeyVaultJSON creates a new confidential.Credential based on
// the content of a JSON string in the format returned by 'az keyvault secret
// show'. Errors are intentionally vague.
func newCredFromAzureKeyVaultJSON(vaultJSON []byte) (confidential.Credential, error) {
	fail := func(err string) (confidential.Credential, error) {
		return confidential.Credential{}, errors.New(err)
	}
	var data struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(vaultJSON, &data); err != nil {
		return fail("unable to decode JSON")
	}
	pfx, err := base64.StdEncoding.DecodeString(data.Value)
	if err != nil {
		return fail("unable to decode base64 value")
	}
	blocks, err := pkcs12.ToPEM(pfx, "")
	if err != nil {
		return fail("unable to convert PFX data to PEM blocks")
	}
	// Multiple blocks are expected. Find the private key and certificates.
	var pemBuf bytes.Buffer
	for _, block := range blocks {
		// confidential.CertFromPEM decides which key parsing function to use
		// based on the Type string, so adjust it here if necessary. It may make
		// sense to support this in confidential.CertFromPEM itself by falling
		// back to other parsing functions upon failure, considering Azure Key
		// Vault is widely used. See issue:
		// https://github.com/AzureAD/microsoft-authentication-library-for-go/issues/488
		if block.Type == "PRIVATE KEY" {
			_, err = x509.ParsePKCS1PrivateKey(block.Bytes)
			if err == nil {
				// Now we know ParsePKCS1PrivateKey is what we need. Tell
				// confidential.CertFromPEM to use it by setting Type to the
				// string that it expects for this type of key.
				block.Type = "RSA PRIVATE KEY"
			}
		}
		err := pem.Encode(&pemBuf, block)
		if err != nil {
			return fail("unable to encode PEM block")
		}
	}
	certs, priv, err := confidential.CertFromPEM(pemBuf.Bytes(), "")
	if err != nil {
		return fail("unable to create cert from PEM blocks")
	}
	return confidential.NewCredFromCert(certs, priv)
}
