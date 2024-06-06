// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package msal

import (
	"strings"
	"testing"
)

func TestNewCredFromAzureKeyVaultJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{"invalidJSON", "{not JSON, but perhaps secret}", true},
		{"unexpectedPassword", `{"value":"42", "Password":"placeholder"}`, true},
		{"unexpectedPassword", `{"value":"Hello!"}`, true},
		{"notPFX", `{"value":"SGVsbG8h"}`, true},

		{"good", `{"value":"` + strings.ReplaceAll(testData, "\n", "") + `"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newCredFromAzureKeyVaultJSON([]byte(tt.json))
			if (err != nil) != tt.wantErr {
				t.Errorf("NewCredFromAzureKeyVaultJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

var testData = // Test data from golang.org\x\crypto@v0.24.0\pkcs12\pkcs12_test.go
`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
HQ8CAggA`
