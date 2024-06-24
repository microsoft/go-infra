// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package akams_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/microsoft/go-infra/internal/akams"
)

func TestNewClient(t *testing.T) {
	_, err := akams.NewClient("tenant", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = akams.NewClient("tenant", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewClientCustom(t *testing.T) {
	_, err := akams.NewClientCustom("https://example.com", akams.HostO365COM, "tenant", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = akams.NewClientCustom("https://example.com", akams.HostO365COM, "tenant", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = akams.NewClientCustom("://example.com", akams.HostO365COM, "tenant", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCreateBulk(t *testing.T) {
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
		{ShortURL: "short2", TargetURL: "target2", LastModifiedBy: "lm", Owners: "o"},
	}
	wantBody := mustMarshal(t, links)
	close, client := setup(t, srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: wantBody})
	defer close()
	if err := client.CreateBulk(context.Background(), links); err != nil {
		t.Fatal(err)
	}
}

func TestCreateBulk_Chunked_BulkSize(t *testing.T) {
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
		{ShortURL: "short1", TargetURL: "target1", CreatedBy: "cb1"},
		{ShortURL: "short2", TargetURL: "target2", CreatedBy: "cb2"},
	}
	wantBody1 := mustMarshal(t, links[:2])
	wantBody2 := mustMarshal(t, links[2:])
	close, client := setup(t,
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: wantBody1},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: wantBody2},
	)
	defer close()
	client.SetBulkLimit(2, 0)
	if err := client.CreateBulk(context.Background(), links); err != nil {
		t.Fatal(err)
	}
}

func TestCreateBulk_Chunked_BodySize(t *testing.T) {
	links := []akams.CreateLinkRequest{
		{ShortURL: "short0", TargetURL: "target0", CreatedBy: "cb0"},
		{ShortURL: "short1", TargetURL: "target1", CreatedBy: "cb1"},
		{ShortURL: "short2", TargetURL: "target2", CreatedBy: "cb2"},
	}
	wantBody1 := mustMarshal(t, links[:1])
	wantBody2 := mustMarshal(t, links[1:2])
	wantBody3 := mustMarshal(t, links[2:])
	close, client := setup(t,
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: wantBody1},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: wantBody2},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: wantBody3},
	)
	defer close()
	client.SetBulkLimit(0, len(wantBody1))
	if err := client.CreateBulk(context.Background(), links); err != nil {
		t.Fatal(err)
	}
}

func TestCreateBulk_Fail_Request(t *testing.T) {
	errStr := "fake error"
	status := []int{http.StatusNotFound, http.StatusInternalServerError}
	for _, s := range status {
		t.Run(fmt.Sprint(s), func(t *testing.T) {
			close, client := setup(t, srvHandler{respStatus: s, respBody: errStr, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: "[]"})
			defer close()
			err := client.CreateBulk(context.Background(), []akams.CreateLinkRequest{})
			testRequestFail(t, err, s, errStr)
		})
	}
}

func TestCreateBulk_Fail_MaxSize(t *testing.T) {
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
	}
	close, client := setup(t)
	defer close()
	client.SetBulkLimit(0, 1)
	err := client.CreateBulk(context.Background(), links)
	wantErr := "item 0 is too large: 91 bytes > 1 byte maximum"
	if err == nil || err.Error() != wantErr {
		t.Fatalf("expected %q, got %q", wantErr, err)
	}
}

func TestUpdateBulk(t *testing.T) {
	links := []akams.UpdateLinkRequest{
		{ShortURL: "short", TargetURL: "target", LastModifiedBy: "lm"},
		{ShortURL: "short2", TargetURL: "target2", Owners: "o"},
	}
	wantBody := mustMarshal(t, links)
	status := []int{http.StatusAccepted, http.StatusNoContent, http.StatusNotFound}
	for _, s := range status {
		t.Run(fmt.Sprint(s), func(t *testing.T) {
			close, client := setup(t, srvHandler{respStatus: s, wantMethod: http.MethodPut, wantPath: "bulk", wantBody: wantBody})
			defer close()
			if err := client.UpdateBulk(context.Background(), links); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateBulk_Fail(t *testing.T) {
	errStr := "fake error"
	status := []int{http.StatusOK, http.StatusInternalServerError}
	for _, s := range status {
		t.Run(fmt.Sprint(s), func(t *testing.T) {
			close, client := setup(t, srvHandler{respStatus: s, respBody: errStr, wantMethod: http.MethodPut, wantPath: "bulk", wantBody: "[]"})
			defer close()
			err := client.UpdateBulk(context.Background(), []akams.UpdateLinkRequest{})
			testRequestFail(t, err, s, errStr)
		})
	}
}

func TestUpdateBulk_Fail_MaxSize(t *testing.T) {
	links := []akams.UpdateLinkRequest{
		{ShortURL: "short", TargetURL: "target"},
	}
	close, client := setup(t)
	defer close()
	client.SetBulkLimit(0, 1)
	err := client.UpdateBulk(context.Background(), links)
	wantErr := "item 0 is too large: 74 bytes > 1 byte maximum"
	if err == nil || err.Error() != wantErr {
		t.Fatalf("expected %q, got %q", wantErr, err)
	}
}

func TestCreateOrUpdateBulk_NeedCreateOnly(t *testing.T) {
	// Simulate a scenario where all links are new.
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
		{ShortURL: "short2", TargetURL: "target2", LastModifiedBy: "lm", Owners: "o"},
	}
	wantBody := mustMarshal(t, links)
	close, client := setup(t, srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: wantBody})
	defer close()
	if err := client.CreateOrUpdateBulk(context.Background(), links); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrUpdateBulk_NeedUpdateOnly(t *testing.T) {
	// Simulate a scenario where some links already exist and some don't.
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
		{ShortURL: "short2", TargetURL: "target2", CreatedBy: "cb2"},
	}
	close, client := setup(t,
		srvHandler{respStatus: http.StatusBadRequest, wantMethod: http.MethodPost, wantPath: "bulk"},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodGet, wantPath: links[0].ShortURL},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodGet, wantPath: links[1].ShortURL},
		srvHandler{respStatus: http.StatusAccepted, wantMethod: http.MethodPut, wantPath: "bulk"},
	)
	defer close()
	if err := client.CreateOrUpdateBulk(context.Background(), links); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrUpdateBulk_NeedUpdateAndCreate(t *testing.T) {
	// Simulate a scenario where some links already exist and some don't.
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
		{ShortURL: "short2", TargetURL: "target2", CreatedBy: "cb2"},
	}
	close, client := setup(t,
		srvHandler{respStatus: http.StatusBadRequest, wantMethod: http.MethodPost, wantPath: "bulk"},
		srvHandler{respStatus: http.StatusNotFound, wantMethod: http.MethodGet, wantPath: links[0].ShortURL},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodGet, wantPath: links[1].ShortURL},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk"},
		srvHandler{respStatus: http.StatusAccepted, wantMethod: http.MethodPut, wantPath: "bulk"},
	)
	defer close()
	if err := client.CreateOrUpdateBulk(context.Background(), links); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrUpdateBulk_CreateFail(t *testing.T) {
	// Simulate a scenario where some links already exist and some don't.
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
	}
	status := http.StatusExpectationFailed
	errStr := "fake error"
	close, client := setup(t,
		srvHandler{respStatus: status, respBody: errStr, wantMethod: http.MethodPost, wantPath: "bulk"},
	)
	defer close()
	err := client.CreateOrUpdateBulk(context.Background(), links)
	testRequestFail(t, err, status, errStr)
}

func TestCreateOrUpdateBulk_GetFail(t *testing.T) {
	// Simulate a scenario where some links already exist and some don't.
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
	}
	status := http.StatusExpectationFailed
	errStr := "fake error"
	close, client := setup(t,
		srvHandler{respStatus: http.StatusBadRequest, wantMethod: http.MethodPost, wantPath: "bulk"},
		srvHandler{respStatus: status, respBody: errStr, wantMethod: http.MethodGet, wantPath: links[0].ShortURL},
	)
	defer close()
	err := client.CreateOrUpdateBulk(context.Background(), links)
	testRequestFail(t, err, status, errStr)
}

type srvHandler struct {
	respStatus int
	respBody   string
	wantMethod string
	wantPath   string
	wantBody   string
}

func setup(t *testing.T, handler ...srvHandler) (closer func(), client *akams.Client) {
	tenant := "tenant"
	host := akams.HostO365COM
	basePath := fmt.Sprintf("/aka/%s/%s/", host, tenant)
	var query int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query >= len(handler) {
			t.Fatalf("unexpected query: %d", query)
		}
		h := handler[query]
		if r.Method != h.wantMethod {
			t.Errorf("[%d] expected %s, got %s", query, h.wantMethod, r.Method)
		}
		if want := basePath + h.wantPath; r.URL.Path != want {
			t.Errorf("[%d] expected %s, got %s", query, want, r.URL.Path)
		}
		if h.wantBody != "" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if h.wantBody != string(body) {
				t.Errorf("[%d] expected %#q, got %#q", query, h.wantBody, string(body))
			}
		}
		w.WriteHeader(h.respStatus)
		if h.respBody != "" {
			if _, err := w.Write([]byte(h.respBody)); err != nil {
				t.Fatal(err)
			}
		}
		query++
	}))
	client, err := akams.NewClientCustom(srv.URL, host, tenant, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		srv.Close()
		if query != len(handler) {
			t.Errorf("expected %d queries, got %d", len(handler), query)
		}
	}, client
}

func testRequestFail(t *testing.T, err error, wantStatusCode int, wantErr string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want, got := fmt.Sprintf("request failed: %d\n%s", wantStatusCode, wantErr), err.Error(); want != got {
		t.Errorf("expected: %q, got: %q", want, got)
	}
	err1, ok := err.(*akams.ResponseError)
	if !ok {
		t.Fatalf("expected *akams.ResponseError, got %T", err)
	}
	if got := err1.StatusCode; wantStatusCode != got {
		t.Errorf("expected: %d, got: %d", wantStatusCode, got)
	}
}

func mustMarshal(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
