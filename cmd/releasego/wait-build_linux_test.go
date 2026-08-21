// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/microsoft/azure-devops-go-api/azuredevops"
	"github.com/microsoft/azure-devops-go-api/azuredevops/build"
)

// TestAzDOGetBuildConnectionReset exercises the error returned by the real AzDO
// client when its HTTP connection is reset. The server sets SO_LINGER to zero
// before closing the socket, which reliably makes Linux send a TCP RST and
// makes Go return a *url.Error wrapping a *net.OpError and syscall.ECONNRESET.
// Other platforms may surface this forced close differently, such as io.EOF, so
// the _linux_test.go suffix keeps the test from being flaky on those platforms.
func TestAzDOGetBuildConnectionReset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.URL.Path == "/_apis" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"count":1,"value":[{"area":"build","id":"0cd358e1-9217-4d94-8269-1c1ee6f93dcf","maxVersion":"7.2","minVersion":"2.0","releasedVersion":"7.2","resourceName":"Builds","resourceVersion":2,"routeTemplate":"{project}/_apis/build/builds/{buildId}"}]}`)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/internal/_apis/build/builds/2990681" {
			http.NotFound(w, r)
			return
		}

		connection, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijacking connection: %v", err)
			return
		}
		tcpConnection, ok := connection.(*net.TCPConn)
		if !ok {
			connection.Close()
			t.Errorf("connection has type %T, want *net.TCPConn", connection)
			return
		}
		if err := tcpConnection.SetLinger(0); err != nil {
			t.Errorf("setting connection linger: %v", err)
		}
		if err := tcpConnection.Close(); err != nil {
			t.Errorf("closing connection: %v", err)
		}
	}))
	defer server.Close()

	connection := azuredevops.NewAnonymousConnection(server.URL)
	client := &build.ClientImpl{Client: *azuredevops.NewClient(connection, server.URL)}
	project := "internal"
	buildID := 2990681
	_, err := client.GetBuild(context.Background(), build.GetBuildArgs{
		BuildId: &buildID,
		Project: &project,
	})
	if err == nil {
		t.Fatal("GetBuild() returned no error, want connection reset")
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Fatalf("GetBuild error has type %T, want wrapped *url.Error: %v", err, err)
	}
	if _, ok := errors.AsType[*net.OpError](err); !ok {
		t.Fatalf("GetBuild error does not wrap *net.OpError: %v", err)
	}
	if !isKnownAPIFlakiness(err) {
		t.Fatalf("isKnownAPIFlakiness(GetBuild error) = false, error type %T: %v", err, err)
	}
}
