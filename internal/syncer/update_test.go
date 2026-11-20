package syncer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func updateReq(t *testing.T, h *Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("X-Sync-Token", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUpdateEndpoints(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{Token: "секрет", UpdateDir: dir}

	if got := updateReq(t, h, "/update", "секрет").Code; got != http.StatusNotFound {
		t.Fatalf("без latest.json ждали 404, получили %d", got)
	}
	if got := updateReq(t, h, "/update", "чужой").Code; got != http.StatusUnauthorized {
		t.Fatalf("с чужим секретом ждали 401, получили %d", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "GOARC-Setup-1.2.3.exe"), []byte("exe-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "latest.json"),
		[]byte(`{"version":"1.2.3","file":"GOARC-Setup-1.2.3.exe","sha256":"abc","size":8}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := updateReq(t, h, "/update", "секрет")
	if rec.Code != http.StatusOK {
		t.Fatalf("описание: %d %s", rec.Code, rec.Body.String())
	}
	rec = updateReq(t, h, "/update/file", "секрет")
	if rec.Code != http.StatusOK || rec.Body.String() != "exe-body" {
		t.Fatalf("файл: %d %q", rec.Code, rec.Body.String())
	}

	// Имя файла из latest.json не должно выводить за папку обновлений.
	if err := os.WriteFile(filepath.Join(dir, "latest.json"),
		[]byte(`{"version":"1.2.3","file":"../secret.exe","sha256":"abc","size":8}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := updateReq(t, h, "/update/file", "секрет").Code; got != http.StatusNotFound {
		t.Fatalf("плохое имя файла: ждали 404, получили %d", got)
	}

	h.UpdateDir = ""
	if got := updateReq(t, h, "/update", "секрет").Code; got != http.StatusNotFound {
		t.Fatalf("папка не задана: ждали 404, получили %d", got)
	}
}
