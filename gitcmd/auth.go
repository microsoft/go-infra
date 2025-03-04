// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gitcmd

// URLAuther manipulates a Git repository URL (GitHub, AzDO, ...) such that Git commands taking a
// remote will work with the URL. This is intentionally vague: it could add an access token into the
// URL, or it could simply make the URL compatible with environmental auth on the machine (SSH).
// Other packages may implement this interface for various services and authentication types.
type URLAuther interface {
	// InsertAuth inserts authentication into the URL and returns it, or if the auther doesn't
	// apply, returns the url without any modifications.
	InsertAuth(url string) string
}

// NoAuther does nothing to URLs.
type NoAuther struct{}

func (NoAuther) InsertAuth(url string) string {
	return url
}

// MultiAuther tries multiple authers in sequence. Stops and returns the result when any auther
// makes a change to the URL.
type MultiAuther struct {
	Authers []URLAuther
}

func (m MultiAuther) InsertAuth(url string) string {
	for _, a := range m.Authers {
		if authUrl := a.InsertAuth(url); authUrl != url {
			return authUrl
		}
	}
	return url
}
