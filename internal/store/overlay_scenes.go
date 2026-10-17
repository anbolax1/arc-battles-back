package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// sceneKeepName - сцена, в которую переезжает общая раскладка прежних версий.
const sceneKeepName = "Общая раскладка"

// SceneLayout - раскладка сцены для состояния оверлея с отметкой, что выбрана она.
// nil - такой сцены нет, оверлей тогда рисует раскладку по умолчанию.
func (s *Store) SceneLayout(ctx context.Context, id string) (*models.OverlayLayout, error) {
	if id == "" {
		return nil, nil
	}
	p, err := s.GetOverlayPresetByKey(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l := parseLayout(p.Layout)
	l.ActivePresetID = p.ID
	return &l, nil
}

// SelectOverlayScene кладёт в состояние оверлея раскладку сцены id, пустой id - уже выбранной.
// Так /overlay показывает выбранную сцену, и правки сцены доходят до него сразу.
func (s *Store) SelectOverlayScene(ctx context.Context, id string) error {
	ls, err := s.liveState(ctx)
	if err != nil {
		return err
	}
	if id == "" && ls.Layout != nil {
		id = ls.Layout.ActivePresetID
	}
	if ls.Layout, err = s.SceneLayout(ctx, id); err != nil {
		return err
	}
	return s.saveLiveState(ctx, ls)
}

// EnsureOverlayScene выполняется при запуске: общая раскладка прежних версий, которой нет ни в одной
// сцене, сама становится сценой, иначе /overlay потерял бы её. Пустую раскладку хранить незачем.
func (s *Store) EnsureOverlayScene(ctx context.Context) error {
	ls, err := s.liveState(ctx)
	if err != nil {
		return err
	}
	if ls.Layout != nil && anyVisible(ls.Layout.Widgets) {
		id, err := s.sceneFor(ctx, *ls.Layout)
		if err != nil {
			return err
		}
		ls.Layout.ActivePresetID = id
		if err := s.saveLiveState(ctx, ls); err != nil {
			return err
		}
	}
	return s.SelectOverlayScene(ctx, "")
}

// sceneFor - сцена с той же раскладкой (выбранная - в первую очередь), а если такой нет - новая.
func (s *Store) sceneFor(ctx context.Context, l models.OverlayLayout) (string, error) {
	presets, err := s.ListOverlayPresets(ctx)
	if err != nil {
		return "", err
	}
	key := layoutKey(l)
	for _, p := range presets {
		if p.ID == l.ActivePresetID && layoutKey(parseLayout(p.Layout)) == key {
			return p.ID, nil
		}
	}
	for _, p := range presets {
		if layoutKey(parseLayout(p.Layout)) == key {
			return p.ID, nil
		}
	}
	l.ActivePresetID = ""
	raw, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	p, err := s.CreateOverlayPreset(ctx, sceneKeepName, Slugify(sceneKeepName), raw)
	return p.ID, err
}

func parseLayout(raw []byte) models.OverlayLayout {
	var l models.OverlayLayout
	_ = json.Unmarshal(raw, &l)
	if l.Widgets == nil {
		l.Widgets = []models.WidgetInstance{}
	}
	return l
}

// layoutKey - раскладка без отметки о выбранной сцене: так копия сравнивается со сценой.
func layoutKey(l models.OverlayLayout) string {
	l.ActivePresetID = ""
	if l.Widgets == nil {
		l.Widgets = []models.WidgetInstance{}
	}
	b, _ := json.Marshal(l)
	return string(b)
}

func anyVisible(widgets []models.WidgetInstance) bool {
	for _, w := range widgets {
		if w.Visible {
			return true
		}
	}
	return false
}

func (s *Store) liveState(ctx context.Context) (models.LiveState, error) {
	var ls models.LiveState
	data, err := s.GetLiveState(ctx)
	if err != nil {
		return ls, err
	}
	err = json.Unmarshal(data, &ls)
	return ls, err
}

func (s *Store) saveLiveState(ctx context.Context, ls models.LiveState) error {
	if ls.Tasks == nil {
		ls.Tasks = []models.Task{}
	}
	b, err := json.Marshal(ls)
	if err != nil {
		return err
	}
	return s.SetLiveState(ctx, b)
}
