// Package media отвечает за файлы хайлайтов: скачивание твич-клипов (yt-dlp),
// приём загруженных файлов, генерацию превью (ffmpeg) и длительности (ffprobe),
// а также безопасную выдачу файлов из локального хранилища.
//
// Это локальная реализация (диск VPS). Логика спрятана за методами Processor —
// при переезде в S3/MinIO/R2 достаточно заменить реализацию хранения/выдачи.
package media

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Видео для просмотра на сайте: клипы Twitch приходят в 1080p 60 fps по 6-10 Мбит/с, а у зрителя
// и у канала сервера столько нет - плеер останавливается дозагрузить. 720p 30 fps до 2.5 Мбит/с
// идёт без остановок; клип выше webMaxHeight или тяжелее webMaxBitrate пережимается.
const (
	webMaxHeight  = 720
	webFPS        = 30
	webMaxRate    = "2500k"
	webBufSize    = "5000k"
	webMaxBitrate = 3_200_000
)

type Processor struct {
	dir     string // корень хранилища
	ytdlp   string
	ffmpeg  string
	ffprobe string
	webMu   sync.Mutex // пережимаем по одному клипу: у сервера одно ядро
}

func NewProcessor(dir, ytdlp, ffmpeg, ffprobe string) *Processor {
	_ = os.MkdirAll(filepath.Join(dir, "highlights"), 0o755)
	return &Processor{dir: dir, ytdlp: ytdlp, ffmpeg: ffmpeg, ffprobe: ffprobe}
}

func (p *Processor) abs(rel string) string { return filepath.Join(p.dir, filepath.FromSlash(rel)) }

func videoRel(id string) string   { return "highlights/" + id + ".mp4" }
func thumbRel(id string) string   { return "highlights/" + id + ".jpg" }
func previewRel(id string) string { return "highlights/" + id + ".preview.mp4" }

// DownloadClip скачивает твич-клип в наш MP4 через yt-dlp, делает превью и узнаёт длительность.
// Возвращает относительные (slash) пути для БД/URL.
func (p *Processor) DownloadClip(ctx context.Context, id, clipURL string) (file, thumb, preview string, dur int, err error) {
	out := p.abs(videoRel(id))
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.ytdlp,
		"--no-playlist", "--no-progress", "--quiet",
		"-f", "best[ext=mp4]/mp4/best",
		"-o", out, clipURL)
	if b, e := cmd.CombinedOutput(); e != nil {
		return "", "", "", 0, fmt.Errorf("yt-dlp: %v: %s", e, strings.TrimSpace(string(b)))
	}
	if fi, e := os.Stat(out); e != nil || fi.Size() == 0 {
		return "", "", "", 0, fmt.Errorf("yt-dlp: файл не создан")
	}
	return videoRel(id), p.makeThumb(cctx, id), p.makePreview(cctx, id), p.probeDuration(cctx, out), nil
}

// SaveUpload сохраняет загруженный видео-файл, делает превью-кадр, превью-видео и длительность.
func (p *Processor) SaveUpload(ctx context.Context, id string, src io.Reader) (file, thumb, preview string, dur int, err error) {
	out := p.abs(videoRel(id))
	f, e := os.Create(out)
	if e != nil {
		return "", "", "", 0, e
	}
	if _, e := io.Copy(f, src); e != nil {
		_ = f.Close()
		_ = os.Remove(out)
		return "", "", "", 0, e
	}
	if e := f.Close(); e != nil {
		_ = os.Remove(out)
		return "", "", "", 0, e
	}
	return videoRel(id), p.makeThumb(ctx, id), p.makePreview(ctx, id), p.probeDuration(ctx, out), nil
}

// makeThumb — кадр-превью. Best-effort: при отсутствии ffmpeg/ошибке вернёт "".
func (p *Processor) makeThumb(ctx context.Context, id string) string {
	out := p.abs(thumbRel(id))
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.ffmpeg, "-y", "-ss", "0.5", "-i", p.abs(videoRel(id)),
		"-frames:v", "1", "-vf", "scale=640:-2", out)
	if cmd.Run() != nil {
		return ""
	}
	if fi, e := os.Stat(out); e != nil || fi.Size() == 0 {
		return ""
	}
	return thumbRel(id)
}

// makePreview — лёгкое превью-видео для автоплея в «стене»: первые 5 сек, без звука,
// ширина 480, сжато. Best-effort: при отсутствии ffmpeg/ошибке вернёт "".
func (p *Processor) makePreview(ctx context.Context, id string) string {
	out := p.abs(previewRel(id))
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.ffmpeg, "-y", "-i", p.abs(videoRel(id)),
		"-t", "5", "-an", "-vf", "scale=480:-2",
		"-c:v", "libx264", "-crf", "32", "-preset", "veryfast", "-movflags", "+faststart", out)
	if cmd.Run() != nil {
		return ""
	}
	if fi, e := os.Stat(out); e != nil || fi.Size() == 0 {
		return ""
	}
	return previewRel(id)
}

// LightenLater пережимает клип для сайта в фоне. Путь к файлу не меняется: готовая лёгкая версия
// просто подменяет исходную.
func (p *Processor) LightenLater(id string) {
	go func() {
		if err := p.lighten(context.Background(), p.abs(videoRel(id))); err != nil {
			log.Printf("хайлайт %s: облегчение видео: %v", id, err)
		}
	}()
}

// LightenExisting в фоне пережимает тяжёлые клипы, загруженные раньше.
func (p *Processor) LightenExisting() {
	go func() {
		files, _ := filepath.Glob(filepath.Join(p.dir, "highlights", "*.mp4"))
		for _, f := range files {
			if strings.HasSuffix(f, ".preview.mp4") || strings.HasSuffix(f, ".web.mp4") {
				continue
			}
			if err := p.lighten(context.Background(), f); err != nil {
				log.Printf("облегчение %s: %v", filepath.Base(f), err)
			}
		}
	}()
}

func (p *Processor) lighten(ctx context.Context, file string) error {
	p.webMu.Lock()
	defer p.webMu.Unlock()
	if !p.heavy(ctx, file) {
		return nil
	}
	tmp := strings.TrimSuffix(file, ".mp4") + ".web.mp4"
	cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := lowPriority(cctx, p.ffmpeg, "-y", "-i", file,
		"-vf", fmt.Sprintf("scale=-2:'min(%d,ih)',fps=%d", webMaxHeight, webFPS),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-maxrate", webMaxRate, "-bufsize", webBufSize,
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-threads", "1", tmp)
	if b, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		out := strings.TrimSpace(string(b))
		if len(out) > 300 {
			out = out[len(out)-300:]
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, out)
	}
	return os.Rename(tmp, file)
}

// heavy - клип выше 720p или с битрейтом больше webMaxBitrate. Если ffprobe не смог прочитать
// файл, не трогаем его.
func (p *Processor) heavy(ctx context.Context, file string) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(cctx, p.ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=height:format=bit_rate", "-of", "default=nw=1", file).Output()
	if err != nil {
		return false
	}
	var height, bitrate int
	for _, line := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		n, _ := strconv.Atoi(v)
		switch k {
		case "height":
			height = n
		case "bit_rate":
			bitrate = n
		}
	}
	return height > webMaxHeight || bitrate > webMaxBitrate
}

// lowPriority запускает команду с низким приоритетом, где есть nice: пережатие не тормозит сайт.
func lowPriority(ctx context.Context, name string, args ...string) *exec.Cmd {
	if nice, err := exec.LookPath("nice"); err == nil {
		return exec.CommandContext(ctx, nice, append([]string{"-n", "19", name}, args...)...)
	}
	return exec.CommandContext(ctx, name, args...)
}

// probeDuration — длительность в секундах через ffprobe. Best-effort: при ошибке 0.
func (p *Processor) probeDuration(ctx context.Context, file string) int {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.ffprobe, "-v", "error",
		"-show_entries", "format=duration", "-of", "default=nw=1:nk=1", file)
	b, err := cmd.Output()
	if err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return 0
	}
	return int(f + 0.5)
}

// Remove удаляет файлы хайлайта (видео, превью-кадр, превью-видео) — при удалении.
func (p *Processor) Remove(paths ...string) {
	for _, rel := range paths {
		if rel != "" {
			_ = os.Remove(p.abs(rel))
		}
	}
}

// Resolve превращает относительный путь (из URL после /media/) в абсолютный путь файла,
// защищаясь от выхода за пределы каталога хранилища. ok=false — нет файла/попытка traversal.
func (p *Processor) Resolve(rel string) (string, bool) {
	root := filepath.Clean(p.dir)
	full := filepath.Join(root, filepath.Clean("/"+filepath.FromSlash(rel)))
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", false
	}
	if fi, err := os.Stat(full); err != nil || fi.IsDir() {
		return "", false
	}
	return full, true
}
