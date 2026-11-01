package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// validValue проверяет тип значения и диапазон (для процента — 0..100).
func validValue(valueType string, value int) (string, bool) {
	switch valueType {
	case "", models.ValueFixed:
		return models.ValueFixed, value >= 0
	case models.ValuePercent:
		return models.ValuePercent, value >= 0 && value <= 100
	default:
		return "", false
	}
}

func defaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// ---- Задания ----

type taskBody struct {
	Text      string   `json:"text"`
	Points    int      `json:"points"`
	ValueType string   `json:"valueType"`
	Kind      string   `json:"kind"`
	Source    string   `json:"source"`
	Author    string   `json:"author"`
	Title     string   `json:"title"`
	Name      string   `json:"name"`
	Category  string   `json:"category"` // старое поле: protocol означает задание на время
	Tag       string   `json:"tag"`
	MapCodes  []string `json:"mapCodes"`
	Active    *bool    `json:"active"`
	Format    string   `json:"format"`
	Rarity    string   `json:"rarity"`
	RoundType string   `json:"roundType"`
}

// taskTag - тег задания; старый клиент присылает протокол как category=protocol - это задание на время.
func taskTag(tag, category string) string {
	if tag == models.TaskTagNormal || tag == models.TaskTagTimed {
		return tag
	}
	if category == "protocol" {
		return models.TaskTagTimed
	}
	return models.TaskTagNormal
}

// Баллы по умолчанию: обычное задание - 2, задание на время - 1 (награды 3 сезона).
// Редкость на баллы не влияет.
const (
	defaultTaskPoints  = 2
	defaultTimedPoints = 1
)

// classValue возвращает значение классификации; пустое - умолчание, незнакомое - ошибка.
func classValue(name, v, def string, allowed []string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s: недопустимое значение «%s»", name, v)
}

func (b taskBody) toModel() (models.CatalogTask, error) {
	vt, ok := validValue(b.ValueType, b.Points)
	if !ok {
		return models.CatalogTask{}, errors.New("valueType должен быть fixed или percent (процент 0..100)")
	}
	format, err := classValue("format", b.Format, models.TaskFormatAny, models.TaskFormats)
	if err != nil {
		return models.CatalogTask{}, err
	}
	rarity, err := classValue("rarity", b.Rarity, models.RarityCommon, models.TaskRarities)
	if err != nil {
		return models.CatalogTask{}, err
	}
	roundType, err := classValue("roundType", b.RoundType, models.RoundTypeAny, models.TaskRoundTypes)
	if err != nil {
		return models.CatalogTask{}, err
	}
	tag := taskTag(b.Tag, b.Category)
	points := b.Points
	if points == 0 {
		points = defaultTaskPoints
		if tag == models.TaskTagTimed {
			points = defaultTimedPoints
		}
	}
	active := true
	if b.Active != nil {
		active = *b.Active
	}
	return models.CatalogTask{
		Text:      strings.TrimSpace(b.Text),
		Points:    points,
		ValueType: vt,
		Kind:      store.NormalizePlayerType(b.Kind),
		Source:    defaultStr(b.Source, "official"),
		Author:    strings.TrimSpace(b.Author),
		Title:     strings.TrimSpace(b.Title),
		Name:      strings.Trim(strings.TrimSpace(b.Name), "«»"),
		Tag:       tag,
		MapCodes:  b.MapCodes,
		Active:    active,
		Format:    format,
		Rarity:    rarity,
		RoundType: roundType,
	}, nil
}

// parseTask - задание из тела запроса; без текста его нет.
func (b taskBody) parseTask() (models.CatalogTask, error) {
	t, err := b.toModel()
	if err != nil {
		return t, err
	}
	if t.Text == "" {
		return t, errors.New("укажите текст задания")
	}
	return t, nil
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var b taskBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	t, err := b.parseTask()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.Store.CreateCatalogTasks(r.Context(), []models.CatalogTask{t})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res[0].Duplicate {
		writeError(w, http.StatusConflict, "такое задание уже есть: то же название, описание, пул и формат")
		return
	}
	writeJSON(w, http.StatusCreated, res[0].Task)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var b taskBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	t, err := b.parseTask()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.Store.UpdateCatalogTask(r.Context(), chi.URLParam(r, "id"), t)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "задание не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteCatalogTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Сколько заданий можно добавить за один раз.
const maxBulkTasks = 500

// bulkSkip - строка пачки, которая не добавлена, и почему.
type bulkSkip struct {
	Index  int    `json:"index"` // номер строки в присланном списке, с 0
	Reason string `json:"reason"`
}

// POST /api/catalog/tasks/bulk {items: [...]} - вставка списком: общие настройки приходят в каждом
// элементе. Строки с ошибкой и дубли пропускаются с причиной, остальные добавляются одной транзакцией.
func (s *Server) handleBulkCreateTasks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []taskBody `json:"items"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	if len(body.Items) == 0 || len(body.Items) > maxBulkTasks {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("нужно от 1 до %d заданий", maxBulkTasks))
		return
	}
	skipped := []bulkSkip{}
	valid := make([]models.CatalogTask, 0, len(body.Items))
	index := make([]int, 0, len(body.Items)) // номер строки для каждого корректного задания
	for i, b := range body.Items {
		t, err := b.parseTask()
		if err != nil {
			skipped = append(skipped, bulkSkip{Index: i, Reason: err.Error()})
			continue
		}
		valid = append(valid, t)
		index = append(index, i)
	}
	created := []models.CatalogTask{}
	if len(valid) > 0 {
		res, err := s.Store.CreateCatalogTasks(r.Context(), valid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for i, rr := range res {
			if rr.Duplicate {
				skipped = append(skipped, bulkSkip{Index: index[i], Reason: "такое задание уже есть"})
				continue
			}
			created = append(created, *rr.Task)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"created": created, "skipped": skipped})
}

// ---- Усложнения ----

type complicationBody struct {
	Text      string `json:"text"`
	Penalty   int    `json:"penalty"`
	ValueType string `json:"valueType"`
	Source    string `json:"source"`
	Author    string `json:"author"`
	Title     string `json:"title"`
}

func (b complicationBody) toModel() (models.CatalogComplication, bool) {
	vt, ok := validValue(b.ValueType, b.Penalty)
	if !ok {
		return models.CatalogComplication{}, false
	}
	return models.CatalogComplication{
		Text:      strings.TrimSpace(b.Text),
		Penalty:   b.Penalty,
		ValueType: vt,
		Source:    defaultStr(b.Source, "official"),
		Author:    strings.TrimSpace(b.Author),
		Title:     strings.TrimSpace(b.Title),
	}, ok
}

func (s *Server) handleCreateComplication(w http.ResponseWriter, r *http.Request) {
	var b complicationBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	c, ok := b.toModel()
	if c.Text == "" {
		writeError(w, http.StatusBadRequest, "укажите текст усложнения")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "valueType должен быть fixed или percent (процент 0..100)")
		return
	}
	created, err := s.Store.CreateCatalogComplication(r.Context(), c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateComplication(w http.ResponseWriter, r *http.Request) {
	var b complicationBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный JSON")
		return
	}
	c, ok := b.toModel()
	if c.Text == "" {
		writeError(w, http.StatusBadRequest, "укажите текст усложнения")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "valueType должен быть fixed или percent (процент 0..100)")
		return
	}
	updated, err := s.Store.UpdateCatalogComplication(r.Context(), chi.URLParam(r, "id"), c)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "усложнение не найдено")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteComplication(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteCatalogComplication(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
