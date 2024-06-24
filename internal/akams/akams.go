// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// akams package provides a client for the aka.ms API.
// See https://aka.ms/akaapi for more information.
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

const (
	defaultBulkSize     = 300
	defaultMaxSizeBytes = 50_000
)

// ResponseError is an error returned by the aka.ms API.
type ResponseError struct {
	StatusCode int
	Body       string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("request failed: %d\n%s", e.StatusCode, e.Body)
}

// Client is a client for the aka.ms API.
type Client struct {
	baseURL      *url.URL
	httpClient   *http.Client
	bulkSize     int
	maxSizeBytes int
}

// NewClient creates a new [Client].
func NewClient(tenant string, httpClient *http.Client) (*Client, error) {
	const apiProdBaseUrl = "https://redirectionapi.trafficmanager.net/api"
	return NewClientCustom(apiProdBaseUrl, HostAkaMs, tenant, httpClient)
}

// NewClientCustom creates a new [Client] with a custom API base URL and host.
func NewClientCustom(apiBaseURL string, host Host, tenant string, httpClient *http.Client) (*Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	baseURL, err := url.Parse(apiBaseURL)
	if err != nil {
		return nil, err
	}
	baseURL = baseURL.JoinPath("aka", string(host), tenant, "/")
	return &Client{baseURL: baseURL, httpClient: httpClient, bulkSize: defaultBulkSize, maxSizeBytes: defaultMaxSizeBytes}, nil
}

// SetBulkLimit sets the maximum number of items and the maximum size in bytes
// for bulk operations. The default is 300 items and 50_000 bytes.
// Setting a value of 0 or negative for bulkSize or maxSizeBytes will reset the limit to the default.
func (c *Client) SetBulkLimit(bulkSize int, maxSizeBytes int) {
	if bulkSize <= 0 {
		c.bulkSize = defaultBulkSize
	} else {
		c.bulkSize = bulkSize
	}
	if maxSizeBytes <= 0 {
		c.maxSizeBytes = defaultMaxSizeBytes
	} else {
		c.maxSizeBytes = maxSizeBytes
	}
}

// CreateBulk creates multiple links in bulk.
func (c *Client) CreateBulk(ctx context.Context, links []CreateLinkRequest) error {
	return chunkSlice(links, c.bulkSize, c.maxSizeBytes, func(r io.Reader) error {
		req, err := c.newRequest(ctx, http.MethodPost, "bulk", r)
		if err != nil {
			return err
		}
		resp, err := c.do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return c.reqError(resp)
		}
		return nil
	})
}

// UpdateBulk updates multiple links in bulk.
func (c *Client) UpdateBulk(ctx context.Context, links []UpdateLinkRequest) error {
	return chunkSlice(links, c.bulkSize, c.maxSizeBytes, func(r io.Reader) error {
		req, err := c.newRequest(ctx, http.MethodPut, "bulk", r)
		if err != nil {
			return err
		}
		resp, err := c.do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
			// success
		default:
			return c.reqError(resp)
		}
		return nil
	})
}

// CreateOrUpdateBulk creates or updates multiple links in bulk.
// If a link already exists, it will be updated.
// If a link does not exist, it will be created.
// If any link fails to be created or updated, the function will return an error.
func (c *Client) CreateOrUpdateBulk(ctx context.Context, links []CreateLinkRequest) error {
	// First try to create all links.
	err := c.CreateBulk(ctx, links)
	if err == nil {
		// All links were created successfully.
		return nil
	}
	// Bad request error is returned when some links already exist.
	if e, ok := err.(*ResponseError); !ok || e.StatusCode != http.StatusBadRequest {
		return err
	}
	// We need to identify which links already exist and which don't.
	toCreate := make([]CreateLinkRequest, 0, len(links))
	toUpdate := make([]UpdateLinkRequest, 0, len(links))
	for _, l := range links {
		exists, err := c.exists(ctx, l.ShortURL)
		if err != nil {
			return err
		}
		if !exists {
			toCreate = append(toCreate, l)
		} else {
			toUpdate = append(toUpdate, l.ToUpdateLinkRequest())
		}
	}
	// Create the links that don't exist and update the ones that do.
	if len(toCreate) != 0 {
		if err := c.CreateBulk(ctx, toCreate); err != nil {
			return err
		}
	}
	if len(toUpdate) != 0 {
		if err := c.UpdateBulk(ctx, toUpdate); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) exists(ctx context.Context, shortURL string) (bool, error) {
	req, err := c.newRequest(ctx, http.MethodGet, shortURL, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.do(req)
	if err != nil {
		return false, err
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

func (c *Client) reqError(resp *http.Response) error {
	// Try to preserve the response body. It may have important context to fix the issue.
	body, _ := io.ReadAll(resp.Body)
	return &ResponseError{StatusCode: resp.StatusCode, Body: string(body)}
}

func (c *Client) newRequest(ctx context.Context, method string, urlStr string, body io.Reader) (*http.Request, error) {
	u := c.baseURL.JoinPath(urlStr)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	return resp, nil
}

// chunkSlice chunks s, encodes each chunk into JSON,
// and calls fn with the encoded chunk.
// The maximum encoded size of each chunk is limited to maxSizeBytes,
// and each chunk has at most bulkSize items.
// The order of the items is preserved.
// If the size of an item is larger than maxSizeBytes, an error will be returned.
// If fn returns an error, the function will stop and return the error.
func chunkSlice[T any](s []T, bulkSize int, maxSizeBytes int, fn func(io.Reader) error) error {
	var buf bytes.Buffer
	buf.WriteByte('[')
	if len(s) == 0 {
		buf.WriteByte(']')
		return fn(&buf)
	}
	// We try to convert the slice to JSON in chunks to avoid hitting the maximum request size.
	// The end result have the same encoding as if we had used json.Marshal.
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	var callFn bool
	for i := 0; i < len(s); {
		lastSize := buf.Len() // keep in case we need to rewind.
		if err := enc.Encode(s[i]); err != nil {
			return err
		}
		buf.Truncate(buf.Len() - 1) // Remove the trailing newline added by enc.Encode.
		buf.WriteByte(',')          // Add a comma to separate items.
		if buf.Len() > maxSizeBytes {
			// The last item was too big.
			if size := buf.Len() - lastSize; size > maxSizeBytes {
				// The last item is too big to fit in a chunk.
				return fmt.Errorf("item %d is too large: %d bytes > %d byte maximum", i, size, maxSizeBytes)
			}
			// Rewind and call fn.
			buf.Truncate(lastSize)
			callFn = true
		} else {
			// The last item fits, continue.
			i++
			// Call fn if we reached the end or the bulk size.
			callFn = i == len(s) || i%bulkSize == 0
		}
		if callFn {
			buf.Truncate(buf.Len() - 1) // Remove the trailing comma.
			buf.WriteByte(']')          // Close the JSON array.
			if err := fn(&buf); err != nil {
				return err
			}
			buf.Reset()        // Reset the buffer for the next chunk.
			buf.WriteByte('[') // Open a new JSON array.
		}
	}
	return nil
}
