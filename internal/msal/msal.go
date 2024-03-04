package msal

import (
	"net/http"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
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

// NewConfidentialTransport creates a new ConfidentialCredentialTransport.
// authority is the URL of a token authority such as "https://login.microsoftonline.com/<your tenant>".
func NewConfidentialTransport(authority, clientID, clientSecret string) (*ConfidentialCredentialTransport, error) {
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
