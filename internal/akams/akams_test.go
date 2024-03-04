package akams_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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
		{ShortURL: "short", TargetURL: "target"},
		{ShortURL: "short2", TargetURL: "target2"},
	}
	tentant := "tenant"
	host := akams.HostO365COM
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if want := fmt.Sprintf("/aka/%s/%s/bulk", host, tentant); r.URL.Path != want {
			t.Errorf("expected path %s, got %s", want, r.URL.Path)
		}
		var got []akams.CreateLinkRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, links) {
			t.Errorf("expected %+v, got %+v", links, got)
		}
	}))
	client, err := akams.NewClientCustom(srv.URL, host, tentant, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CreateBulk(context.Background(), links); err != nil {
		t.Fatal(err)
	}
}

func TestCreateBulkFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("fake error"))
	}))
	client, err := akams.NewClientCustom(srv.URL, akams.HostO365COM, "tenant", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.CreateBulk(context.Background(), []akams.CreateLinkRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want, got := "request failed: 500\nfake error", err.Error(); want != got {
		t.Errorf("expected: %q, got: %q", want, got)
	}
}
