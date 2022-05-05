// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"testing"
)

var testTag = "go1.2.3"

func Test_tagChecker_Check_existingTag(t *testing.T) {
	local, upstream := newFixture(t)
	commit := addTag(t, upstream)

	c := &tagChecker{
		gitRepo: *local,
		Tag:     testTag,
	}

	got, err := c.Check()
	if err != nil {
		t.Fatal(err)
	}
	if got != commit {
		t.Errorf("Check() got = %v, want %v", got, commit)
	}
}

func Test_tagChecker_Check_tagOnSecondCall(t *testing.T) {
	local, upstream := newFixture(t)

	c := &tagChecker{
		gitRepo: *local,
		Tag:     testTag,
	}

	if _, err := c.Check(); err == nil {
		t.Error("expected error: Check should fail when tag doesn't exist in upstream")
	}

	commit := addTag(t, upstream)

	got, err := c.Check()
	if err != nil {
		t.Fatal(err)
	}
	if got != commit {
		t.Errorf("Check() got = %v, want %v", got, commit)
	}
}

func Test_boringChecker_findBoringReleaseCommit1(t *testing.T) {
	// https://github.com/golang/go/blob/dev.boringcrypto/misc/boring/RELEASES
	content := `# This file lists published Go+BoringCrypto releases.
# Each line describes a single release: <version> <git commit> <target> <URL> <sha256sum>
go1.16.14b7 e90b835f3071 linux-amd64 https://go-boringcrypto.storage.googleapis.com/go1.16.14b7.linux-amd64.tar.gz 5024e1231d33b9dfffdd7821132dd32eccd42e7415f25618dc8c7304b335edd9
go1.16.14b7 e90b835f3071 src https://go-boringcrypto.storage.googleapis.com/go1.16.14b7.src.tar.gz caef2ef601bcc588e6bcb511087c9620200723a4c74191b725fbda94c3be884b
go1.17.8b7 4ea866a9969f linux-amd64 https://go-boringcrypto.storage.googleapis.com/go1.17.8b7.linux-amd64.tar.gz 4a1fa2c8d77309e1ef5bafe7e80e75c06e70c0ae1212d9f3d95485017155491d
go1.17.8b7 4ea866a9969f src https://go-boringcrypto.storage.googleapis.com/go1.17.8b7.src.tar.gz e42ac342c315d33c47434299a24f33137e7099f278ee6669404c4d7e49e17bcf
go1.16.15b7 649671b08fbd linux-amd64 https://go-boringcrypto.storage.googleapis.com/go1.16.15b7.linux-amd64.tar.gz 4d62f517786266019c721c35330e23da123eb184eadb5a79379fe81d31d856db
go1.16.15b7 649671b08fbd src https://go-boringcrypto.storage.googleapis.com/go1.16.15b7.src.tar.gz 54fc7f2ec0b72b0aaf7726eb5f7f57885252ef46c2c1ca238090cc57850e3ef7
go1.18b7 0622ea4d9068 linux-amd64 https://go-boringcrypto.storage.googleapis.com/go1.18b7.linux-amd64.tar.gz baa33bc66b8df97a3c5a328637b85f04d5629f139dc2df946c09ab7214510c61
go1.18b7 0622ea4d9068 src https://go-boringcrypto.storage.googleapis.com/go1.18b7.src.tar.gz 6028ffee59903934a3182d45ee3e0c1c9f47fb98f05d9bbb2fabb4771db60792
go1.18.1b7 d003f0850a7d linux-amd64 https://go-boringcrypto.storage.googleapis.com/go1.18.1b7.linux-amd64.tar.gz a5b3985341de6ca54f6a8e13e9ae695f0ee202207e25f082c3895a8fc6f89f64
go1.18.1b7 d003f0850a7d src https://go-boringcrypto.storage.googleapis.com/go1.18.1b7.src.tar.gz c7f91549b3a197e4a08f64e07546855ca8f82d597f60fd23c7ad2f082640a9fe
go1.17.9b7 ed86dfc4e441 linux-amd64 https://go-boringcrypto.storage.googleapis.com/go1.17.9b7.linux-amd64.tar.gz 9469d1b4c10f59c921c4666c52baba5f6ca63b1cce0eca95e03b5713ef27577c
go1.17.9b7 ed86dfc4e441 src https://go-boringcrypto.storage.googleapis.com/go1.17.9b7.src.tar.gz 5d6bfe543a9a2bf6d8749973c771e40127b8020a769ecc5fb41d0dbd7deae9a6`

	tests := []struct {
		name    string
		version string
		want    string
		wantErr bool
	}{
		{"find existing", "1.16.15", "649671b08fbd", false},
		{"error if not existing", "1.18.12", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &boringChecker{Version: tt.version}
			got, err := c.findBoringReleaseCommit(content)
			if (err != nil) != tt.wantErr {
				t.Errorf("findBoringReleaseCommit() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("findBoringReleaseCommit() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func newFixture(t *testing.T) (local, upstream *gitRepo) {
	local = newEmptyGitRepo(t)
	upstream = newUpstreamGitRepo(t)
	local.Upstream = upstream.GitDir
	return
}

func newEmptyGitRepo(t *testing.T) *gitRepo {
	c := &gitRepo{GitDir: t.TempDir()}
	if err := c.runGitCmd("init"); err != nil {
		t.Fatal(err)
	}
	return c
}

func newUpstreamGitRepo(t *testing.T) *gitRepo {
	upstream := newEmptyGitRepo(t)
	if err := upstream.runGitCmd("config", "--local", "user.name", "test"); err != nil {
		t.Fatal(err)
	}
	if err := upstream.runGitCmd("config", "--local", "user.email", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := upstream.runGitCmd("commit", "--allow-empty", "-m", "Initial commit"); err != nil {
		t.Fatal(err)
	}
	return upstream
}

func addTag(t *testing.T, upstream *gitRepo) string {
	if err := upstream.runGitCmd("tag", testTag); err != nil {
		t.Fatal(err)
	}
	commit, err := upstream.revParse(testTag)
	if err != nil {
		t.Fatal(err)
	}
	return commit
}
