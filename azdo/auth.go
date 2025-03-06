// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdo

import (
	"fmt"

	"github.com/microsoft/go-infra/stringutil"
)

// AzDOPATAuther adds a PAT into the https-style Azure DevOps repository URL.
type AzDOPATAuther struct {
	PAT string
}

func (a AzDOPATAuther) InsertAuth(url string) string {
	if a.PAT == "" {
		return url
	}
	const azdoDncengPrefix = "https://dnceng@dev.azure.com/"
	if after, found := stringutil.CutPrefix(url, azdoDncengPrefix); found {
		url = fmt.Sprintf(
			// Username doesn't matter. PAT is identity.
			"https://arbitraryusername:%v@dev.azure.com/%v",
			a.PAT, after)
	}
	return url
}
