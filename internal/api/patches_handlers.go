package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/battle-for-respect/backend/internal/store"
)

// GET /api/patches?season= - каталог нашивок сезона; без season - текущий сезон, а без него - последний.
func (s *Server) handlePatchCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var sn models.Season
	var err error
	if id := r.URL.Query().Get("season"); id != "" {
		sn, err = s.Store.GetSeason(ctx, id)
	} else if sn, err = s.Store.ActiveSeason(ctx); errors.Is(err, store.ErrNotFound) {
		var list []models.Season
		if list, err = s.Store.ListSeasons(ctx); err == nil {
			if len(list) == 0 {
				err = store.ErrNotFound
			} else {
				sn = list[0]
			}
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "сезон не найден")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cat, err := s.Store.PatchCatalogFor(ctx, sn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cat)
}

// syncPatches пересчитывает нашивки после изменения матчей. Сбой пересчёта не ломает действие в пульте:
// нашивки догонит следующий пересчёт.
func (s *Server) syncPatches(ctx context.Context) []store.PatchNew {
	fresh, err := s.Store.SyncPatches(ctx)
	if err != nil {
		log.Printf("нашивки: %v", err)
		return nil
	}
	return fresh
}

// patchFlashKeep - сколько последних нашивок держит состояние оверлея; старше уже показаны.
const patchFlashKeep = 8

// flashPatches кладёт в состояние оверлея нашивки, полученные в этом матче, - оверлей покажет их плашками.
func (s *Server) flashPatches(ctx context.Context, tournamentID string, fresh []store.PatchNew) {
	var items []models.PatchFlash
	var names map[string]string
	for _, p := range fresh {
		if p.MatchID != tournamentID {
			continue
		}
		if names == nil {
			names = map[string]string{}
			if t, err := s.Store.GetTournament(ctx, tournamentID); err == nil {
				for _, part := range t.Participants {
					if part.UserID != nil {
						names[*part.UserID] = part.Name
					}
				}
			}
		}
		items = append(items, models.PatchFlash{
			ID:   p.SeasonID + ":" + p.UserID + ":" + p.Code + ":" + strconv.Itoa(p.Tier),
			Name: names[p.UserID], Code: p.Code, Tier: p.Tier, Detail: p.Detail,
		})
	}
	if len(items) == 0 {
		return
	}
	data, _ := s.Store.GetLiveState(ctx)
	var stored models.LiveState
	_ = json.Unmarshal(data, &stored)
	if stored.TournamentID == nil || *stored.TournamentID != tournamentID {
		stored.PatchFlashes = nil
	}
	stored.PatchFlashes = append(stored.PatchFlashes, items...)
	if n := len(stored.PatchFlashes); n > patchFlashKeep {
		stored.PatchFlashes = stored.PatchFlashes[n-patchFlashKeep:]
	}
	if norm, err := json.Marshal(stored); err == nil {
		_ = s.Store.SetLiveState(ctx, norm)
	}
}
