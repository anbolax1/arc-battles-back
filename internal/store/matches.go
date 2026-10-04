package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrLiveMatchExists - уже идёт другой матч: новый не создаём, пока старый не завершён или не отменён.
	ErrLiveMatchExists = errors.New("уже идёт другой матч")
	// ErrNoMap - у раунда нет карты: сначала пики-баны.
	ErrNoMap = errors.New("у раунда нет карты")
)

// MatchSide - сторона нового матча: игрок (1×1) или пара игроков (2×2).
type MatchSide struct {
	UserID  string
	Members []string
	Name    string
}

// NewMatch - параметры нового матча.
type NewMatch struct {
	Mode             string
	PlayerType       string
	RatingMultiplier int
	Format           string     // match | show
	StartsAt         *time.Time // когда начнётся шоу-матч
	Prize            string     // приз шоу-матча
	Sides            [2]MatchSide
}

type sideInfo struct {
	part  models.Participant
	mmr   int
	isNew bool
}

// describeSide собирает участника стороны и его MMR в текущем сезоне.
func (s *Store) describeSide(ctx context.Context, mode string, side MatchSide, rule seasonRule) (sideInfo, error) {
	if mode == "2x2" {
		if len(side.Members) != 2 || side.Members[0] == side.Members[1] {
			return sideInfo{}, ErrConflict
		}
		members := make([]map[string]string, 0, 2)
		names := make([]string, 0, 2)
		for _, id := range side.Members {
			u, err := s.GetUser(ctx, id)
			if err != nil {
				return sideInfo{}, err
			}
			members = append(members, map[string]string{"name": userName(u), "userId": u.ID})
			names = append(names, userName(u))
		}
		raw, _ := json.Marshal(members)
		name := strings.TrimSpace(side.Name)
		if name == "" {
			name = strings.Join(names, " & ")
		}
		p := models.Participant{Kind: "team", Name: name, Members: raw}
		key, _, _, _ := teamKeyFromParticipant(p)
		mmr, err := s.teamSeasonMmr(ctx, key, rule)
		if err != nil {
			return sideInfo{}, err
		}
		var games int
		if err := s.Pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM team_mmr_history WHERE team_key = $1 AND season_key = $2`, key, rule.Key).Scan(&games); err != nil {
			return sideInfo{}, err
		}
		return sideInfo{part: p, mmr: mmr, isNew: games == 0}, nil
	}

	u, err := s.GetUser(ctx, side.UserID)
	if err != nil {
		return sideInfo{}, err
	}
	mmr, err := s.userSeasonMmr(ctx, u.ID, "1x1", rule)
	if err != nil {
		return sideInfo{}, err
	}
	var games int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(tournament_id) FROM mmr_history WHERE user_id = $1 AND mode = '1x1' AND season_key = $2`, u.ID, rule.Key).Scan(&games); err != nil {
		return sideInfo{}, err
	}
	uid := u.ID
	return sideInfo{part: models.Participant{Kind: "player", Name: userName(u), UserID: &uid}, mmr: mmr, isNew: games == 0}, nil
}

func userName(u models.User) string {
	if strings.TrimSpace(u.DisplayName) != "" {
		return u.DisplayName
	}
	return u.Login
}

// CreateMatch заводит матч: обычный сразу становится текущим, шоу-матч ждёт своего времени в расписании.
// Сторону A получает новичок сезона или тот, у кого меньше MMR - она ходит первой в пиках-банах.
func (s *Store) CreateMatch(ctx context.Context, in NewMatch) (models.Tournament, error) {
	status, startsAt := "live", time.Now()
	if in.Format == FormatShow {
		status = "upcoming"
		if in.StartsAt != nil {
			startsAt = *in.StartsAt
		}
	} else {
		in.Format = FormatMatch
		if live, err := s.CurrentMatchID(ctx); err == nil && live != "" {
			return models.Tournament{}, ErrLiveMatchExists
		}
	}
	if in.Mode != "2x2" {
		in.Mode = "1x1"
	}
	if in.RatingMultiplier != 2 {
		in.RatingMultiplier = 1
	}
	rounds := FormatRounds(in.Format)
	rule := s.activeSeasonRule(ctx)
	a, err := s.describeSide(ctx, in.Mode, in.Sides[0], rule)
	if err != nil {
		return models.Tournament{}, err
	}
	b, err := s.describeSide(ctx, in.Mode, in.Sides[1], rule)
	if err != nil {
		return models.Tournament{}, err
	}
	if in.Mode == "1x1" && *a.part.UserID == *b.part.UserID {
		return models.Tournament{}, ErrConflict
	}
	if (b.isNew && !a.isNew) || (a.isNew == b.isNew && b.mmr < a.mmr) {
		a, b = b, a
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return models.Tournament{}, err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO tournaments (title, mode, player_type, status, total_rounds, maps, starts_at, rating_multiplier, season_id, format, prize)
		VALUES ($1, $2, $3, $4, $5, '[]', $6, $7, (SELECT id FROM seasons WHERE status = 'active' LIMIT 1), $8, $9)
		RETURNING id`,
		a.part.Name+" vs "+b.part.Name, in.Mode, NormalizePlayerType(in.PlayerType), status, rounds, startsAt,
		in.RatingMultiplier, in.Format, in.Prize).Scan(&id); err != nil {
		return models.Tournament{}, err
	}
	for n := 1; n <= rounds; n++ {
		if _, err := tx.Exec(ctx, `INSERT INTO rounds (tournament_id, number, status) VALUES ($1, $2, 'pending')`, id, n); err != nil {
			return models.Tournament{}, err
		}
	}
	for i, side := range []sideInfo{a, b} {
		members := []byte("[]")
		if len(side.part.Members) > 0 {
			members = side.part.Members
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO participants (tournament_id, kind, user_id, name, seed, members)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			id, side.part.Kind, side.part.UserID, side.part.Name, i+1, string(members)); err != nil {
			return models.Tournament{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return models.Tournament{}, err
	}
	for _, side := range []sideInfo{a, b} {
		for _, uid := range participantUsers(side.part) {
			_ = s.MarkRegistrationPlaced(ctx, uid, id)
		}
	}
	return s.GetTournament(ctx, id)
}

// StartShowMatch выводит запланированный шоу-матч в эфир: он становится текущим и попадает в сезон,
// который идёт сейчас.
func (s *Store) StartShowMatch(ctx context.Context, id string) error {
	if live, err := s.CurrentMatchID(ctx); err == nil && live != "" && live != id {
		return ErrLiveMatchExists
	}
	ct, err := s.Pool.Exec(ctx, `
		UPDATE tournaments SET status = 'live', updated_at = now(),
		       season_id = (SELECT id FROM seasons WHERE status = 'active' LIMIT 1)
		WHERE id = $1 AND status = 'upcoming'`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// SetShowPrize меняет приз шоу-матча; пусто - без приза.
func (s *Store) SetShowPrize(ctx context.Context, id, prize string) error {
	ct, err := s.Pool.Exec(ctx, `UPDATE tournaments SET prize = $2, updated_at = now() WHERE id = $1`, id, prize)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ShowPreviewPath - файл картинки-превью в хранилище медиа; пусто - превью нет.
func (s *Store) ShowPreviewPath(ctx context.Context, id string) string {
	var path string
	_ = s.Pool.QueryRow(ctx, `SELECT preview_path FROM tournaments WHERE id = $1`, id).Scan(&path)
	return path
}

// SetShowPreview запоминает картинку-превью шоу-матча; пусто - убрать.
func (s *Store) SetShowPreview(ctx context.Context, id, path string) error {
	ct, err := s.Pool.Exec(ctx, `UPDATE tournaments SET preview_path = $2, updated_at = now() WHERE id = $1`, id, path)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RescheduleShowMatch переносит шоу-матч, пока он не начался.
func (s *Store) RescheduleShowMatch(ctx context.Context, id string, at time.Time) error {
	ct, err := s.Pool.Exec(ctx,
		`UPDATE tournaments SET starts_at = $2, updated_at = now() WHERE id = $1 AND status = 'upcoming'`, id, at)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// CurrentMatchID - матч, который сейчас идёт (пусто, если такого нет).
func (s *Store) CurrentMatchID(ctx context.Context) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx,
		`SELECT id FROM tournaments WHERE status = 'live' ORDER BY updated_at DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// LastFinishedMatchID - последний завершённый матч (для главной, когда сейчас никто не играет).
func (s *Store) LastFinishedMatchID(ctx context.Context) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx,
		`SELECT id FROM tournaments WHERE status = 'finished' ORDER BY COALESCE(starts_at, created_at) DESC, updated_at DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// StartNextRound начинает первый раунд или переходит к следующему; задания раунда раздаются сразу.
func (s *Store) StartNextRound(ctx context.Context, tournamentID string) (int, error) {
	rounds, err := s.ListRounds(ctx, tournamentID)
	if err != nil {
		return 0, err
	}
	live := -1
	for i, r := range rounds {
		if r.Status == "live" {
			live = i
		}
	}
	next := -1
	if live == -1 {
		for i, r := range rounds {
			if r.Status == "pending" {
				next = i
				break
			}
		}
	} else if live+1 < len(rounds) {
		next = live + 1
	}
	if next == -1 {
		return 0, ErrConflict
	}
	if rounds[next].MapCode == "" && rounds[next].Map == "" {
		return 0, ErrNoMap
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if live >= 0 {
		if _, err := tx.Exec(ctx, `UPDATE rounds SET status = 'finished' WHERE id = $1`, rounds[live].ID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE rounds SET status = 'live' WHERE id = $1`, rounds[next].ID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tournaments SET status = 'live', updated_at = now() WHERE id = $1`, tournamentID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if err := s.DealRoundTasks(ctx, rounds[next].ID); err != nil {
		return 0, err
	}
	return rounds[next].Number, nil
}

// taskSlot - вид задания в раздаче раунда.
type taskSlot struct {
	category string
	onMap    bool
}

// DealRoundTasks выдаёт каждой стороне на раунд задание, задание на карту раунда и протокол из
// пула под тип игроков; повторов в матче нет. Если на карту заданий нет, выдаётся ещё одно общее.
func (s *Store) DealRoundTasks(ctx context.Context, roundID string) error {
	var tournamentID, playerType, mapCode string
	if err := s.Pool.QueryRow(ctx, `
		SELECT r.tournament_id, t.player_type, COALESCE(r.map_code, '')
		FROM rounds r JOIN tournaments t ON t.id = r.tournament_id WHERE r.id = $1`, roundID).
		Scan(&tournamentID, &playerType, &mapCode); err != nil {
		return err
	}
	var dealt bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM round_bonus_tasks WHERE round_id = $1)`, roundID).Scan(&dealt); err != nil {
		return err
	}
	if dealt {
		return nil
	}
	parts, err := s.ListParticipants(ctx, tournamentID)
	if err != nil {
		return err
	}
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Seed < parts[j].Seed })
	slots := []taskSlot{{"task", false}, {"task", true}, {"protocol", false}}
	for _, p := range parts {
		for _, sl := range slots {
			id, err := s.pickTask(ctx, tournamentID, playerType, sl, mapCode, "")
			if errors.Is(err, ErrNotFound) && sl.onMap {
				id, err = s.pickTask(ctx, tournamentID, playerType, taskSlot{"task", false}, "", "")
			}
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := s.Pool.Exec(ctx, `
				INSERT INTO round_bonus_tasks (round_id, participant_id, task_id) VALUES ($1, $2, $3)
				ON CONFLICT (round_id, participant_id, task_id) DO NOTHING`, roundID, p.ID, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// pickTask - случайное действующее задание нужного вида, ещё не выданное в этом матче.
func (s *Store) pickTask(ctx context.Context, tournamentID, playerType string, sl taskSlot, mapCode, except string) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `
		SELECT ct.id FROM catalog_tasks ct
		WHERE ct.active AND ct.category = $3
		  AND ($2 = 'pvpve' OR ct.kind = $2 OR ct.kind = 'pvpve')
		  AND (($4 = '' AND ct.map_code IS NULL) OR ct.map_code = NULLIF($4, ''))
		  AND ct.id <> $5
		  AND ct.id NOT IN (
		      SELECT rbt.task_id FROM round_bonus_tasks rbt JOIN rounds r ON r.id = rbt.round_id
		      WHERE r.tournament_id = $1)
		ORDER BY random() LIMIT 1`,
		tournamentID, playerType, sl.category, mapForSlot(sl, mapCode), except).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func mapForSlot(sl taskSlot, mapCode string) string {
	if sl.onMap {
		return mapCode
	}
	return ""
}

// RerollTask меняет невыполненное задание на другое того же вида (выпало невыполнимое).
func (s *Store) RerollTask(ctx context.Context, assignmentID string) error {
	var tournamentID, playerType, taskID, category, taskMap, roundMap string
	var completed *string
	err := s.Pool.QueryRow(ctx, `
		SELECT r.tournament_id, t.player_type, rbt.task_id, ct.category, COALESCE(ct.map_code, ''),
		       COALESCE(r.map_code, ''), rbt.completed_by
		FROM round_bonus_tasks rbt
		JOIN rounds r ON r.id = rbt.round_id
		JOIN tournaments t ON t.id = r.tournament_id
		JOIN catalog_tasks ct ON ct.id = rbt.task_id
		WHERE rbt.id = $1`, assignmentID).
		Scan(&tournamentID, &playerType, &taskID, &category, &taskMap, &roundMap, &completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if completed != nil {
		return ErrConflict
	}
	sl := taskSlot{category: category, onMap: taskMap != ""}
	next, err := s.pickTask(ctx, tournamentID, playerType, sl, roundMap, taskID)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE round_bonus_tasks SET task_id = $2 WHERE id = $1`, assignmentID, next)
	return err
}

// FinishMatch завершает матч: побеждает сторона с большим счётом, при равенстве - ничья без
// изменения MMR. Досрочно - оставшиеся раунды не играются.
func (s *Store) FinishMatch(ctx context.Context, tournamentID string) (models.Tournament, error) {
	parts, err := s.ListParticipants(ctx, tournamentID)
	if err != nil {
		return models.Tournament{}, err
	}
	var winner *string
	best, tie := -1, false
	for _, p := range parts {
		total, err := s.RecomputeParticipantPoints(ctx, p.ID)
		if err != nil {
			return models.Tournament{}, err
		}
		switch {
		case total > best:
			best, tie = total, false
			id := p.ID
			winner = &id
		case total == best:
			tie = true
		}
	}
	if tie {
		winner = nil
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE rounds SET status = 'finished' WHERE tournament_id = $1 AND status = 'live'`, tournamentID); err != nil {
		return models.Tournament{}, err
	}
	if _, err := s.Pool.Exec(ctx, `
		UPDATE tournaments SET status = 'finished', winner_participant_id = $2, updated_at = now() WHERE id = $1`,
		tournamentID, winner); err != nil {
		return models.Tournament{}, err
	}
	if err := s.ApplyTournamentMmr(ctx, tournamentID); err != nil {
		return models.Tournament{}, err
	}
	return s.GetTournament(ctx, tournamentID)
}

// AdjustRoundPoints добавляет стороне ручные очки за раунд (нок рейдера и т.п.); ручные очки не
// уходят ниже нуля, поэтому фактическое изменение может быть меньше запрошенного.
func (s *Store) AdjustRoundPoints(ctx context.Context, roundID, participantID string, delta int) (int, error) {
	var before int
	err := s.Pool.QueryRow(ctx,
		`SELECT points FROM round_entries WHERE round_id = $1 AND participant_id = $2`, roundID, participantID).Scan(&before)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	after := before + delta
	if after < 0 {
		after = 0
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO round_entries (round_id, participant_id, points) VALUES ($1, $2, $3)
		ON CONFLICT (round_id, participant_id) DO UPDATE SET points = EXCLUDED.points, updated_at = now()`,
		roundID, participantID, after); err != nil {
		return 0, err
	}
	return after - before, nil
}

// ListMatchPlayers - игроки для выбора сторон: MMR и счёт в текущем сезоне, сыгравшие сверху.
func (s *Store) ListMatchPlayers(ctx context.Context) ([]models.MatchPlayer, error) {
	rule := s.activeSeasonRule(ctx)
	rows, err := s.Pool.Query(ctx, `
		WITH h AS (
			SELECT h.user_id, SUM(h.delta) AS total,
			       SUM(CASE WHEN h.delta > 0 THEN t.games ELSE 0 END) AS wins,
			       SUM(CASE WHEN h.delta < 0 THEN t.games ELSE 0 END) AS losses
			FROM mmr_history h LEFT JOIN tournaments t ON t.id = h.tournament_id
			WHERE h.mode = '1x1' AND h.season_key = $1
			GROUP BY h.user_id
		)
		SELECT u.id, u.login, u.display_name, ($2::int + COALESCE(h.total, 0))::int,
		       COALESCE(h.wins, 0)::int, COALESCE(h.losses, 0)::int,
		       h.user_id IS NULL, u.password_hash LIKE '!%'
		FROM users u LEFT JOIN h ON h.user_id = u.id
		ORDER BY h.user_id IS NULL, ($2::int + COALESCE(h.total, 0)) DESC, lower(u.login)`, rule.Key, rule.Start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.MatchPlayer{}
	for rows.Next() {
		var p models.MatchPlayer
		if err := rows.Scan(&p.ID, &p.Login, &p.DisplayName, &p.Mmr, &p.Wins, &p.Losses, &p.IsNew, &p.IsPlaceholder); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreatePlaceholderUser заводит игрока по нику прямо из формы матча: войти он сможет, когда
// организатор выдаст ссылку для входа. Уже существующий ник просто находится.
func (s *Store) CreatePlaceholderUser(ctx context.Context, nickname string) (models.User, bool, error) {
	nick := strings.Join(strings.Fields(nickname), " ")
	if u, err := s.GetUserByLogin(ctx, nick); err == nil {
		return u, false, nil
	}
	u, err := s.CreateUser(ctx, nick, nick, "!manual", models.RoleUser)
	if errors.Is(err, ErrLoginTaken) {
		u, err = s.GetUserByLogin(ctx, nick)
		return u, false, err
	}
	return u, err == nil, err
}

// CompleteLegendaryInMatch засчитывает легендарку стороне в раунде матча (навсегда).
func (s *Store) CompleteLegendaryInMatch(ctx context.Context, legendaryID, tournamentID, participantID string, roundNumber int) error {
	p, err := s.GetParticipant(ctx, participantID)
	if err != nil {
		return err
	}
	if p.TournamentID != tournamentID {
		return ErrConflict
	}
	var roundID *string
	var mapName string
	var rid string
	if err := s.Pool.QueryRow(ctx,
		`SELECT id, map FROM rounds WHERE tournament_id = $1 AND number = $2`, tournamentID, roundNumber).Scan(&rid, &mapName); err == nil {
		roundID = &rid
	}
	if _, err := s.CompleteLegendary(ctx, legendaryID, models.LegendaryCompletion{
		UserID:        p.UserID,
		ParticipantID: &p.ID,
		Nickname:      p.Name,
		TournamentID:  &tournamentID,
		Map:           mapName,
		RoundID:       roundID,
	}); err != nil {
		return err
	}
	_, err = s.RecomputeParticipantPoints(ctx, participantID)
	return err
}
