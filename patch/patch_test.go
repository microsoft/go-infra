// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import "testing"

func TestHeaderSetAuthor(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
	}{
		{
			name:    "no co-authors",
			subject: "[PATCH] Subject\n\nBody mentioning Co-authored-by: unchanged.\n\n",
			want:    "[PATCH] Subject\n\nBody mentioning Co-authored-by: unchanged.\n\n",
		},
		{
			name:    "multiple co-authors",
			subject: "[PATCH] Subject\n\nBody.\n\nCo-authored-by: Copilot <copilot@example.com>\nCo-authored-by: Person <person@example.com>\n\n",
			want:    "[PATCH] Subject\n\nBody.\n",
		},
		{
			name:    "mixed case and other trailers",
			subject: "[PATCH] Subject\n\nco-AUTHORED-by: Person <person@example.com>\nSigned-off-by: Reviewer <reviewer@example.com>\nCO-AUTHORED-BY: Bot <bot@example.com>\n\n",
			want:    "[PATCH] Subject\n\nSigned-off-by: Reviewer <reviewer@example.com>\n",
		},
		{
			name:    "no body",
			subject: "[PATCH] Subject\n\nCo-authored-by: Person <person@example.com>\n\n",
			want:    "[PATCH] Subject\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Header{FromAuthor: "Original <original@example.com>", Subject: tt.subject}
			const author = "Bot <bot@example.com>"
			h.SetAuthor(author)
			if h.FromAuthor != author {
				t.Errorf("FromAuthor = %q, want %q", h.FromAuthor, author)
			}
			if h.Subject != tt.want {
				t.Errorf("Subject = %q, want %q", h.Subject, tt.want)
			}
			h.SetAuthor(author)
			if h.Subject != tt.want {
				t.Errorf("repeated SetAuthor changed Subject to %q", h.Subject)
			}
		})
	}
}
