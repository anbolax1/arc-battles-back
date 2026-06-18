package config

import (
	"bufio"
	"log"
	"os"
	"strings"
)

// Config — все настройки сервиса, читаются из переменных окружения (и из .env при наличии).
type Config struct {
	BindAddr     string // адрес прослушивания (в проде — 127.0.0.1:PORT за nginx); пусто → ":"+Port
	Port         string
	DatabaseURL  string
	JWTSecret    string
	FrontendURL  string
	CookieDomain string
	CookieSecure bool
	TrustProxy   bool // за обратным прокси (nginx) брать IP из X-Real-IP; иначе только из TCP-соединения

	// Бутстрап организатора: при старте гарантируется аккаунт SuperadminLogin с ролью superadmin.
	// Пароль организатора управляется этим SuperadminPassword — задаётся/обновляется на старте.
	// Пусто → бутстрап пропускается (существующий аккаунт и пароль не трогаем).
	SuperadminLogin    string
	SuperadminPassword string

	// Хайлайты: каталог для видео/превью и внешние утилиты для обработки клипов.
	MediaDir    string
	YtDlpPath   string
	FfmpegPath  string
	FfprobePath string
}

// devJWTSecret — небезопасный дефолт: допустим только при явном dev-флаге.
const devJWTSecret = "dev-insecure-secret-change-me-please-32b"

// Load читает .env (если есть) и собирает конфиг из окружения.
func Load() Config {
	loadDotEnv(".env")

	return Config{
		BindAddr:           env("BIND_ADDR", ""),
		Port:               env("PORT", "8080"),
		DatabaseURL:        env("DATABASE_URL", "postgres://respect:respect@localhost:5433/respect?sslmode=disable"),
		JWTSecret:          env("JWT_SECRET", devJWTSecret),
		FrontendURL:        env("FRONTEND_URL", "http://localhost:3000"),
		CookieDomain:       env("COOKIE_DOMAIN", ""),
		CookieSecure:       env("COOKIE_SECURE", "false") == "true",
		TrustProxy:         env("TRUST_PROXY", "false") == "true",
		SuperadminLogin:    strings.TrimSpace(env("SUPERADMIN_LOGIN", "Istwood")),
		SuperadminPassword: env("SUPERADMIN_PASSWORD", ""),
		MediaDir:           env("MEDIA_DIR", "./media"),
		YtDlpPath:          env("YTDLP_PATH", "yt-dlp"),
		FfmpegPath:         env("FFMPEG_PATH", "ffmpeg"),
		FfprobePath:        env("FFPROBE_PATH", "ffprobe"),
	}
}

// ListenAddr — адрес для http.Server.
func (c Config) ListenAddr() string {
	if c.BindAddr != "" {
		return c.BindAddr
	}
	return ":" + c.Port
}

// Validate проверяет критичные для безопасности настройки и аварийно завершает старт,
// если стойкого секрета нет. Fail-closed: слабый/дефолтный JWT_SECRET допустим ТОЛЬКО при
// явном dev-флаге (APP_ENV=dev или ALLOW_INSECURE_JWT=true), независимо от COOKIE_SECURE —
// иначе токены можно подделать и обойти RBAC.
func (c Config) Validate() {
	weakSecret := c.JWTSecret == devJWTSecret || len(c.JWTSecret) < 32
	allowInsecure := os.Getenv("APP_ENV") == "dev" || os.Getenv("ALLOW_INSECURE_JWT") == "true"
	if weakSecret {
		if !allowInsecure {
			log.Fatal("JWT_SECRET обязателен: задайте случайную строку ≥32 символов " +
				"(для локальной разработки выставьте APP_ENV=dev)")
		}
		log.Println("ВНИМАНИЕ: слабый JWT_SECRET разрешён только из-за dev-флага — НИКОГДА не используйте в проде")
	}
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
