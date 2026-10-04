package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// MaxImageBytes - предел для картинки-превью: обложка, а не фотоальбом.
const MaxImageBytes = 10 << 20

var (
	ErrNotImage      = errors.New("это не картинка: подойдут JPG, PNG, WebP или GIF")
	ErrImageTooLarge = errors.New("картинка больше 10 МБ")
	ErrBadImageURL   = errors.New("нужна ссылка на картинку, начинающаяся с http:// или https://")
	ErrImageFetch    = errors.New("не удалось скачать картинку по ссылке")
)

// imageExt - форматы, которые браузер покажет как есть.
var imageExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// imageClient скачивает превью по ссылке; дольше ждать картинку нет смысла.
var imageClient = &http.Client{Timeout: 20 * time.Second}

// SaveImage сохраняет картинку в папку dir хранилища под именем name и отдаёт относительный путь.
// Формат определяется по содержимому, а не по заголовкам запроса.
func (p *Processor) SaveImage(dir, name string, src io.Reader) (string, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(src, head)
	if errors.Is(err, io.EOF) {
		return "", ErrNotImage
	}
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	ext, ok := imageExt[http.DetectContentType(head[:n])]
	if !ok {
		return "", ErrNotImage
	}
	if err := os.MkdirAll(p.abs(dir), 0o755); err != nil {
		return "", err
	}
	rel := dir + "/" + name + ext
	f, err := os.Create(p.abs(rel))
	if err != nil {
		return "", err
	}
	// Байт сверх предела показывает, что картинка слишком большая.
	written, err := io.Copy(f, io.LimitReader(io.MultiReader(bytes.NewReader(head[:n]), src), MaxImageBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && written > MaxImageBytes {
		err = ErrImageTooLarge
	}
	if err != nil {
		_ = os.Remove(p.abs(rel))
		return "", err
	}
	return rel, nil
}

// FetchImage скачивает картинку по ссылке к нам: ссылки Discord и других CDN со временем перестают
// открываться. Подробности сетевой ошибки наружу не отдаём - только код ответа.
func (p *Processor) FetchImage(ctx context.Context, rawURL, dir, name string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", ErrBadImageURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", ErrBadImageURL
	}
	req.Header.Set("Accept", "image/*")
	resp, err := imageClient.Do(req)
	if err != nil {
		return "", ErrImageFetch
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: сервер ответил %d", ErrImageFetch, resp.StatusCode)
	}
	if resp.ContentLength > MaxImageBytes {
		return "", ErrImageTooLarge
	}
	return p.SaveImage(dir, name, resp.Body)
}
