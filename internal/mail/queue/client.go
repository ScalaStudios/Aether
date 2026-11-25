package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Client struct {
	url  string
	key  string
	http *http.Client
}

func NewClient(baseURL, key string) *Client {
	return &Client{url: strings.TrimRight(baseURL, "/"), key: key, http: &http.Client{}}
}

func (c *Client) List(ctx context.Context, f Filter, limit int) (*Listing, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	for key, value := range map[string]string{"sender": f.Sender, "recipientDomain": f.RecipientDomain, "error": f.Error, "queue": f.Queue} {
		if value != "" {
			q.Set(key, value)
		}
	}
	var l Listing
	return &l, c.do(ctx, http.MethodGet, "/queue?"+q.Encode(), nil, &l)
}

func (c *Client) Apply(ctx context.Context, action string, in ActionRequest) (int, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	var out ActionResult
	return out.Affected, c.do(ctx, http.MethodPost, "/queue/"+action, body, &out)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("postfix queue service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&failure)
		return fmt.Errorf("postfix queue service answered %d: %s", resp.StatusCode, failure.Error)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
