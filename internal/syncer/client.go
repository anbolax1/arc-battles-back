package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Client - сторона приложения: ходит на сайт.
type Client struct {
	Base  string // https://сайт, без хвостового слэша
	Token string
	// short - обычные запросы с таймаутом; long - передача файлов без ограничения по времени.
	short, long *http.Client
	// bytes - сколько данных (запросы и ответы, без файлов) прошло за текущий проход.
	bytes atomic.Int64
}

func NewClient(base, token string) *Client {
	return &Client{
		Base:  strings.TrimRight(base, "/"),
		Token: token,
		short: &http.Client{Timeout: 2 * time.Minute},
		long:  &http.Client{},
	}
}

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/api/sync"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Sync-Token", c.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("сайт ответил %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func (c *Client) json(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		c.bytes.Add(int64(len(b)))
		body = bytes.NewReader(b)
	}
	resp, err := c.do(ctx, c.short, method, path, body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	c.bytes.Add(int64(len(raw)))
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) Hello(ctx context.Context) (helloResp, error) {
	var h helloResp
	return h, c.json(ctx, http.MethodGet, "/hello", nil, &h)
}

func (c *Client) Manifest(ctx context.Context, tables []string) (Hashes, error) {
	h := Hashes{}
	return h, c.json(ctx, http.MethodPost, "/manifest", manifestReq{Tables: tables}, &h)
}

func (c *Client) Rows(ctx context.Context, table string, keys []string) (map[string]json.RawMessage, error) {
	var r rowsResp
	if err := c.json(ctx, http.MethodPost, "/rows", rowsReq{Table: table, Keys: keys}, &r); err != nil {
		return nil, err
	}
	return r.Rows, nil
}

func (c *Client) Apply(ctx context.Context, changes []TableChange) (ApplyResult, error) {
	var r ApplyResult
	return r, c.json(ctx, http.MethodPost, "/apply", applyReq{Changes: changes}, &r)
}

func (c *Client) Done(ctx context.Context, tables []string) error {
	return c.json(ctx, http.MethodPost, "/done", doneReq{Tables: tables}, nil)
}

func (c *Client) Logins(ctx context.Context) (map[string]string, error) {
	m := map[string]string{}
	return m, c.json(ctx, http.MethodGet, "/logins", nil, &m)
}

func (c *Client) MediaList(ctx context.Context) ([]MediaFile, error) {
	var f []MediaFile
	return f, c.json(ctx, http.MethodGet, "/media", nil, &f)
}

// MediaGet скачивает файл потоком во временный файл и переименовывает его на место.
func (c *Client) MediaGet(ctx context.Context, rel, full string) error {
	resp, err := c.do(ctx, c.long, http.MethodGet, "/media/file?path="+url.QueryEscape(rel), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return writeFileAtomic(full, resp.Body)
}

// MediaPut отправляет файл на сайт потоком.
func (c *Client) MediaPut(ctx context.Context, rel string, size int64, src io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.Base+"/api/sync/media/file?path="+url.QueryEscape(rel), src)
	if err != nil {
		return err
	}
	req.Header.Set("X-Sync-Token", c.Token)
	req.ContentLength = size
	resp, err := c.long.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("сайт ответил %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// Schema - команда создания таблицы на сайте; found=false, если таблицы там нет.
func (c *Client) Schema(ctx context.Context, table string) (ddl string, found bool, err error) {
	resp, err := c.do(ctx, c.short, http.MethodGet, "/schema?table="+url.QueryEscape(table), nil, "")
	if err != nil {
		if strings.Contains(err.Error(), "сайт ответил 404") {
			return "", false, nil
		}
		return "", false, err
	}
	defer resp.Body.Close()
	var out struct {
		DDL string `json:"ddl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false, err
	}
	return out.DDL, true, nil
}

func (c *Client) Digests(ctx context.Context, tables []string) (map[string]Digest, error) {
	d := map[string]Digest{}
	return d, c.json(ctx, http.MethodPost, "/digests", manifestReq{Tables: tables}, &d)
}
