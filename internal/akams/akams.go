package akams

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
)

const (
	apiBaseUrl = "https://redirectionapi.trafficmanager.net/api/aka"
	endpoint   = "https://microsoft.onmicrosoft.com/redirectionapi"
	authority  = "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/oauth2/authorize"
)

type Client struct {
	tenant string
	client confidential.Client
}

func NewClient(id, secret, tenant string) (*Client, error) {
	cred, err := confidential.NewCredFromSecret(secret)
	if err != nil {
		return nil, err
	}
	client, err := confidential.New(authority, id, cred)
	if err != nil {
		return nil, err
	}
	return &Client{tenant: tenant, client: client}, nil
}

func (c *Client) CreateBulk(ctx context.Context, links []Link) error {
	// Bulk limitation is 50_000 bytes in body, max items is 300.
	// Limit the max size to 100 which is typically ~70% of the overall allowable size.
	const bulkSize = 100
	for i := 0; i < len(links); i += bulkSize {
		end := i + bulkSize
		if end > len(links) {
			end = len(links)
		}
		req, err := c.newRequest(ctx, "POST", "/bulk", links[i:end])
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("failed to create bulk links: %s", resp.Status)
		}
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method string, url string, body any) (*http.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	auth, err := c.client.AcquireTokenByCredential(ctx, []string{endpoint + "/.default"})
	if err != nil {
		return nil, err
	}
	url = c.apiUrlTarget() + "/" + url
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *Client) apiUrlTarget() string {
	return apiBaseUrl + "/1/" + c.tenant
}
