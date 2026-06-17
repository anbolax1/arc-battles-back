package config

import (
	"bufio"
	"os"
	"strings"
)

// Config — все настройки сервиса, читаются из переменных окружения (и из .env при наличии).
type Config struct {
	Port               string
	DatabaseURL        string
	JWTSecret          string
	TwitchClientID     string
	TwitchClientSecret string
	TwitchRedirectURL  string
	FrontendURL        string
	CookieDomain       string
	CookieSecure       bool
	OrganizerLogins    []string

	// Хайлайты: каталог для видео/превью и внешние утилиты для обработки клипов.
	MediaDir    string
	YtDlpPath   string
	FfmpegPath  string
	FfprobePath string
}

// Load читает .env (если есть) и собирает конфиг из окружения.
func Load() Config {
	loadDotEnv(".env")

	c := Config{
		Port:               env("PORT", "8080"),
		DatabaseURL:        env("DATABASE_URL", "postgres://respect:respect@localhost:5433/respect?sslmode=disable"),
		JWTSecret:          env("JWT_SECRET", "dev-insecure-secret-change-me-please-32b"),
		TwitchClientID:     env("TWITCH_CLIENT_ID", ""),
		TwitchClientSecret: env("TWITCH_CLIENT_SECRET", ""),
		TwitchRedirectURL:  env("TWITCH_REDIRECT_URL", "http://localhost:8080/api/auth/twitch/callback"),
		FrontendURL:        env("FRONTEND_URL", "http://localhost:3000"),
		CookieDomain:       env("COOKIE_DOMAIN", ""),
		CookieSecure:       env("COOKIE_SECURE", "false") == "true",
		MediaDir:           env("MEDIA_DIR", "./media"),
		YtDlpPath:          env("YTDLP_PATH", "yt-dlp"),
		FfmpegPath:         env("FFMPEG_PATH", "ffmpeg"),
		FfprobePath:        env("FFPROBE_PATH", "ffprobe"),
	}

	for _, l := range strings.Split(env("ORGANIZER_TWITCH_LOGINS", ""), ",") {
		if l = strings.TrimSpace(strings.ToLower(l)); l != "" {
			c.OrganizerLogins = append(c.OrganizerLogins, l)
		}
	}
	return c
}

// IsOrganizerLogin сообщает, нужно ли автоматически выдать роль organizer этому логину.
func (c Config) IsOrganizerLogin(login string) bool {
	login = strings.ToLower(login)
	for _, l := range c.OrganizerLogins {
		if l == login {
			return true
		}
	}
	return false
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadDotEnv — простой парсер .env без внешних зависимостей.
// Уже выставленные переменные окружения имеют приоритет над файлом.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}
