package syncer

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// UpdateInfo - описание свежей версии приложения организатора: лежит на сайте в latest.json рядом с установщиком.
type UpdateInfo struct {
	Version string `json:"version"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

const updateInfoName = "latest.json"

// updateFileRe - допустимое имя установщика: так из latest.json нельзя выйти за папку обновлений.
var updateFileRe = regexp.MustCompile(`^GOARC-Setup-[0-9][0-9A-Za-z.\-]*\.exe$`)

func readUpdateInfo(dir string) (UpdateInfo, error) {
	var u UpdateInfo
	b, err := os.ReadFile(filepath.Join(dir, updateInfoName))
	if err != nil {
		return u, err
	}
	if err := json.Unmarshal(b, &u); err != nil {
		return u, err
	}
	if u.Version == "" || !updateFileRe.MatchString(u.File) {
		return u, errors.New("latest.json заполнен неверно")
	}
	return u, nil
}

// update отдаёт описание свежей версии приложения; 404, если обновления на сайте не выложены.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	if h.UpdateDir == "" {
		http.NotFound(w, r)
		return
	}
	u, err := readUpdateInfo(h.UpdateDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// updateFile отдаёт установщик из latest.json; докачка по Range работает, чтобы обрыв не начинал всё заново.
func (h *Handler) updateFile(w http.ResponseWriter, r *http.Request) {
	if h.UpdateDir == "" {
		http.NotFound(w, r)
		return
	}
	u, err := readUpdateInfo(h.UpdateDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(h.UpdateDir, u.File))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	http.ServeContent(w, r, u.File, info.ModTime(), f)
}
