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

type ResponseError struct {
	StatusCode int
	Body       string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("request failed: %d\n%s", e.StatusCode, e.Body)
}

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
	baseURL = baseURL.JoinPath("aka", string(host), tenant, "/")
	return &Client{baseURL: baseURL, httpClient: httpClient}, nil
}

func (c *Client) exists(shortURL string) (bool, error) {
	req, err := c.newRequest(context.Background(), http.MethodGet, shortURL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %v", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, c.reqError(resp)
	}

}

// CreateBulk creates multiple links in bulk.
func (c *Client) CreateBulk(ctx context.Context, links []CreateLinkRequest) error {
	req, err := c.newRequest(ctx, http.MethodPost, "bulk", links)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.reqError(resp)
	}
	return nil
}

func (c *Client) CreateOrUpdateBulk(ctx context.Context, links []CreateLinkRequest) error {
	err := c.CreateBulk(ctx, links)
	if err == nil {
		// All links were created successfully.
		return nil
	}
	// Bad request error is returned when some links already exist.
	if e, ok := err.(*ResponseError); !ok || e.StatusCode != http.StatusBadRequest {
		return err
	}

	toCreate := make([]CreateLinkRequest, 0, len(links))
	toUpdate := make([]UpdateLinkRequest, 0, len(links))
	for _, l := range links {
		exists, err := c.exists(l.ShortURL)
		if err != nil {
			return err
		}
		if !exists {
			toCreate = append(toCreate, l)
		} else {
			toUpdate = append(toUpdate, UpdateLinkRequest{
				ShortURL:       l.ShortURL,
				TargetURL:      l.TargetURL,
				MobileURL:      l.MobileURL,
				IsAllowParam:   l.IsAllowParam,
				IsTrackParam:   l.IsTrackParam,
				Description:    l.Description,
				GroupOwner:     l.GroupOwner,
				LastModifiedBy: l.LastModifiedBy,
				Owners:         l.Owners,
				Category:       l.Category,
				IsActive:       l.IsActive,
			})
		}
	}
	if len(toUpdate) != 0 {
		if err := c.UpdateBulk(ctx, toUpdate); err != nil {
			return err
		}
	}
	if len(toCreate) != 0 {
		if err := c.CreateBulk(ctx, toCreate); err != nil {
			return err
		}
	}
	return nil
}

// UpdateBulk updates multiple links in bulk.
func (c *Client) UpdateBulk(ctx context.Context, links []UpdateLinkRequest) error {
	req, err := c.newRequest(ctx, http.MethodPut, "bulk", links)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
		// success
	default:
		return c.reqError(resp)
	}
	return nil
}

func (c *Client) reqError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return &ResponseError{StatusCode: resp.StatusCode, Body: string(body)}
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
