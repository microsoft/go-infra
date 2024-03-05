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
	wantBody, err := json.Marshal(links)
	if err != nil {
		t.Fatal(err)
	}
	srv, client := setup(t, srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: string(wantBody) + "\n"})
	defer srv.Close()
	if err := client.CreateBulk(context.Background(), links); err != nil {
		t.Fatal(err)
	}
}

func TestCreateBulk_Fail(t *testing.T) {
	errStr := "fake error"
	status := []int{http.StatusNotFound, http.StatusInternalServerError}
	for _, s := range status {
		t.Run(fmt.Sprint(s), func(t *testing.T) {
			srv, client := setup(t, srvHandler{respStatus: s, respBody: errStr, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: "[]\n"})
			defer srv.Close()
			err := client.CreateBulk(context.Background(), []akams.CreateLinkRequest{})
			testRequestFail(t, err, s, errStr)
		})
	}
}

func TestUpdateBulk(t *testing.T) {
	links := []akams.UpdateLinkRequest{
		{ShortURL: "short", TargetURL: "target", LastModifiedBy: "lm"},
		{ShortURL: "short2", TargetURL: "target2", Owners: "o"},
	}
	wantBody, err := json.Marshal(links)
	if err != nil {
		t.Fatal(err)
	}
	status := []int{http.StatusAccepted, http.StatusNoContent, http.StatusNotFound}
	for _, s := range status {
		t.Run(fmt.Sprint(s), func(t *testing.T) {
			srv, client := setup(t, srvHandler{respStatus: s, wantMethod: http.MethodPut, wantPath: "bulk", wantBody: string(wantBody) + "\n"})
			defer srv.Close()
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
			srv, client := setup(t, srvHandler{respStatus: s, respBody: errStr, wantMethod: http.MethodPut, wantPath: "bulk", wantBody: "[]\n"})
			defer srv.Close()
			err := client.UpdateBulk(context.Background(), []akams.UpdateLinkRequest{})
			testRequestFail(t, err, s, errStr)
		})
	}
}

func TestCreateOrUpdateBulk_NeedCreateOnly(t *testing.T) {
	// Simulate a scenario where all links are new.
	links := []akams.CreateLinkRequest{
		{ShortURL: "short", TargetURL: "target", CreatedBy: "cb"},
		{ShortURL: "short2", TargetURL: "target2", LastModifiedBy: "lm", Owners: "o"},
	}
	wantBody, err := json.Marshal(links)
	if err != nil {
		t.Fatal(err)
	}
	srv, client := setup(t, srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk", wantBody: string(wantBody) + "\n"})
	defer srv.Close()
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
	srv, client := setup(t,
		srvHandler{respStatus: http.StatusBadRequest, wantMethod: http.MethodPost, wantPath: "bulk"},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodGet, wantPath: links[0].ShortURL},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodGet, wantPath: links[1].ShortURL},
		srvHandler{respStatus: http.StatusAccepted, wantMethod: http.MethodPut, wantPath: "bulk"},
	)
	defer srv.Close()
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
	srv, client := setup(t,
		srvHandler{respStatus: http.StatusBadRequest, wantMethod: http.MethodPost, wantPath: "bulk"},
		srvHandler{respStatus: http.StatusNotFound, wantMethod: http.MethodGet, wantPath: links[0].ShortURL},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodGet, wantPath: links[1].ShortURL},
		srvHandler{respStatus: http.StatusOK, wantMethod: http.MethodPost, wantPath: "bulk"},
		srvHandler{respStatus: http.StatusAccepted, wantMethod: http.MethodPut, wantPath: "bulk"},
	)
	defer srv.Close()
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
	srv, client := setup(t,
		srvHandler{respStatus: status, respBody: errStr, wantMethod: http.MethodPost, wantPath: "bulk"},
	)
	defer srv.Close()
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
	srv, client := setup(t,
		srvHandler{respStatus: http.StatusBadRequest, wantMethod: http.MethodPost, wantPath: "bulk"},
		srvHandler{respStatus: status, respBody: errStr, wantMethod: http.MethodGet, wantPath: links[0].ShortURL},
	)
	defer srv.Close()
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

func setup(t *testing.T, handler ...srvHandler) (*httptest.Server, *akams.Client) {
	tentant := "tenant"
	host := akams.HostO365COM
	basePath := fmt.Sprintf("/aka/%s/%s/", host, tentant)
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
			w.Write([]byte(h.respBody))
		}
		query++
	}))
	client, err := akams.NewClientCustom(srv.URL, host, tentant, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return srv, client
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
