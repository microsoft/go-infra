package akams

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const Scope = "https://microsoft.onmicrosoft.com/redirectionapi"

// Host is the host identifier.
type Host string

const (
	HostAkaMs           Host = "1"
	HostGoMicrosoftCOM  Host = "2"
	HostSpoMs           Host = "3"
	HostOfficeCOM       Host = "4"
	HostOffice365COM    Host = "5"
	HostO365COM         Host = "6"
	HostMicrosoft365COM Host = "7"
)

// Client is a client for the aka.ms API.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// NewClient creates a new [Client].
func NewClient(tenant string, httpClient *http.Client) (*Client, error) {
	const apiProdBaseUrl = "https://redirectionapi.trafficmanager.net/api"
	return NewClientCustom(apiProdBaseUrl, HostAkaMs, tenant, httpClient)
}

func NewClientCustom(apiBaseURL string, host Host, tenant string, httpClient *http.Client) (*Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	baseURL, err := url.Parse(apiBaseURL)
	if err != nil {
		return nil, err
	}
	baseURL = baseURL.JoinPath("aka", string(host), tenant)
	return &Client{baseURL: baseURL, httpClient: httpClient}, nil
}

// CreateBulk creates multiple links in bulk.
func (c *Client) CreateBulk(ctx context.Context, links []Link) error {
	req, err := c.newRequest(ctx, "POST", "/bulk", links)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("request failed: %d\n%s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method string, urlStr string, body any) (*http.Request, error) {
	u := c.baseURL.JoinPath(urlStr)
	var buf io.ReadWriter
	if body != nil {
		buf = &bytes.Buffer{}
		enc := json.NewEncoder(buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}
