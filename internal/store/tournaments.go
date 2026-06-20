package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

const tournamentCols = `id, title, mode, status, total_rounds, maps, starts_at, winner_participant_id, created_at, updated_at`

func scanTournament(row pgx.Row) (models.Tournament, error) {
	var t models.Tournament
	var mapsRaw []byte
	err := row.Scan(&t.ID, &t.Title, &t.Mode, &t.Status, &t.TotalRounds, &mapsRaw,
		&t.StartsAt, &t.WinnerParticipantID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	if len(mapsRaw) > 0 {
		_ = json.Unmarshal(mapsRaw, &t.Maps)
	}
	if t.Maps == nil {
		t.Maps = []string{}
	}
	return t, nil
}

func (s *Store) CreateTournament(ctx context.Context, t models.Tournament) (models.Tournament, error) {
	maps := []byte("[]")
	if t.Maps != nil {
		maps, _ = json.Marshal(t.Maps)
	}
	if t.Mode == "" {
		t.Mode = "1x1"
	}
	if t.Status == "" {
		t.Status = "upcoming"
	}
	if t.TotalRounds == 0 {
		t.TotalRounds = 3
	}
	// season_id — текущий активный сезон (подзапросом): новые турниры идут в него.
	const q = `
		INSERT INTO tournaments (title, mode, status, total_rounds, maps, starts_at, season_id)
		VALUES ($1, $2, $3, $4, $5, $6, (SELECT id FROM seasons WHERE status = 'active' LIMIT 1))
		RETURNING ` + tournamentCols
	return scanTournament(s.Pool.QueryRow(ctx, q, t.Title, t.Mode, t.Status, t.TotalRounds, string(maps), t.StartsAt))
}

// UpdateTournamentMeta частично правит «шапку» турнира: название и/или время начала.
// startsAtSet=true со startsAt=nil очищает дату (NULL). ErrNotFound — если турнира нет.
func (s *Store) UpdateTournamentMeta(ctx context.Context, id string, title *string, startsAtSet bool, startsAt *time.Time) error {
	sets := []string{}
	args := []any{}
	n := 1
	if title != nil {
		sets = append(sets, fmt.Sprintf("title = $%d", n))
		args = append(args, *title)
		n++
	}
	if startsAtSet {
		sets = append(sets, fmt.Sprintf("starts_at = $%d", n))
		args = append(args, startsAt)
		n++
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)
	q := `UPDATE tournaments SET ` + strings.Join(sets, ", ") + fmt.Sprintf(` WHERE id = $%d`, n)
	ct, err := s.Pool.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTournament удаляет турнир и каскадом всё связанное: участников, раунды,
// результаты раундов, назначенные стартовые/бонусные задания и штрафы-усложнения
// (через FK ON DELETE CASCADE). Пулы каталога (задания/усложнения/стартовые) НЕ трогаются.
// Поставленных в турнир игроков возвращаем в общий пул заявок. ErrNotFound — если турнира нет.
func (s *Store) DeleteTournament(ctx context.Context, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Вернуть в пул тех, кто был поставлен в этот турнир из заявки (иначе их заявка
	// осталась бы «accepted» без турнира — registrations.tournament_id здесь ON DELETE SET NULL).
	if _, err := tx.Exec(ctx,
		`UPDATE registrations SET status = 'pending', tournament_id = NULL, decided_at = NULL
		   WHERE tournament_id = $1 AND status = 'accepted'`, id); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `DELETE FROM tournaments WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func (s *Store) ListTournaments(ctx context.Context, status string) ([]models.Tournament, error) {
	q := `SELECT ` + tournamentCols + `,
		(SELECT COUNT(*) FROM participants WHERE tournament_id = tournaments.id),
		CASE WHEN tournaments.mode = '2x2' THEN (
			(SELECT COUNT(*) FROM participants p WHERE p.tournament_id = tournaments.id) < 2
			OR EXISTS (SELECT 1 FROM participants p WHERE p.tournament_id = tournaments.id AND jsonb_array_length(p.members) < 2)
		) ELSE (
			(SELECT COUNT(*) FROM participants p WHERE p.tournament_id = tournaments.id) < 2
		) END
		FROM tournaments`
	args := []any{}
	if status != "" {
		q += ` WHERE status = $1`
		args = append(args, status)
	}
	q += ` ORDER BY COALESCE(starts_at, created_at) DESC`

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Tournament{}
	for rows.Next() {
		var t models.Tournament
		var mapsRaw []byte
		if err := rows.Scan(&t.ID, &t.Title, &t.Mode, &t.Status, &t.TotalRounds, &mapsRaw,
			&t.StartsAt, &t.WinnerParticipantID, &t.CreatedAt, &t.UpdatedAt, &t.ParticipantCount, &t.HasSpace); err != nil {
			return nil, err
		}
		if len(mapsRaw) > 0 {
			_ = json.Unmarshal(mapsRaw, &t.Maps)
		}
		if t.Maps == nil {
			t.Maps = []string{}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTournament(ctx context.Context, id string) (models.Tournament, error) {
	t, err := scanTournament(s.Pool.QueryRow(ctx, `SELECT `+tournamentCols+` FROM tournaments WHERE id = $1`, id))
	if err != nil {
		return t, err
	}
	if t.Participants, err = s.ListParticipants(ctx, id); err != nil {
		return t, err
	}
	t.ParticipantCount = len(t.Participants)
	if t.Rounds, err = s.ListRounds(ctx, id); err != nil {
		return t, err
	}
	return t, nil
}

// DemoteLiveExcept переводит все live-турниры, кроме указанного, в статус upcoming
// (одновременно в эфире может быть только один турнир).
func (s *Store) DemoteLiveExcept(ctx context.Context, exceptID string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE tournaments SET status = 'upcoming', updated_at = now() WHERE status = 'live' AND id <> $1`, exceptID)
	return err
}

// TournamentStatus возвращает только статус турнира (для проверок).
func (s *Store) TournamentStatus(ctx context.Context, id string) (string, error) {
	var status string
	err := s.Pool.QueryRow(ctx, `SELECT status FROM tournaments WHERE id = $1`, id).Scan(&status)
	return status, err
}

func (s *Store) statusBy(ctx context.Context, q, arg string) (string, error) {
	var st string
	err := s.Pool.QueryRow(ctx, q, arg).Scan(&st)
	return st, err
}

// Статус турнира по связанной сущности (для блокировки правок завершённого турнира).
func (s *Store) StatusByRound(ctx context.Context, roundID string) (string, error) {
	return s.statusBy(ctx, `SELECT t.status FROM rounds r JOIN tournaments t ON t.id = r.tournament_id WHERE r.id = $1`, roundID)
}
func (s *Store) StatusByParticipant(ctx context.Context, participantID string) (string, error) {
	return s.statusBy(ctx, `SELECT t.status FROM participants p JOIN tournaments t ON t.id = p.tournament_id WHERE p.id = $1`, participantID)
}
func (s *Store) StatusByStarterAssignment(ctx context.Context, id string) (string, error) {
	return s.statusBy(ctx, `SELECT t.status FROM round_starter_tasks rst JOIN rounds r ON r.id = rst.round_id JOIN tournaments t ON t.id = r.tournament_id WHERE rst.id = $1`, id)
}
func (s *Store) StatusByBonusAssignment(ctx context.Context, id string) (string, error) {
	return s.statusBy(ctx, `SELECT t.status FROM round_bonus_tasks rbt JOIN rounds r ON r.id = rbt.round_id JOIN tournaments t ON t.id = r.tournament_id WHERE rbt.id = $1`, id)
}

func (s *Store) UpdateTournamentStatus(ctx context.Context, id, status string) (models.Tournament, error) {
	return scanTournament(s.Pool.QueryRow(ctx,
		`UPDATE tournaments SET status = $2, updated_at = now() WHERE id = $1 RETURNING `+tournamentCols,
		id, status))
}

// SetWinnerTop ставит победителем участника с наибольшими очками (для авто-победителя
// при завершении турнира). Если участников нет — победитель остаётся пустым.
func (s *Store) SetWinnerTop(ctx context.Context, tournamentID string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE tournaments SET winner_participant_id = (
			SELECT id FROM participants WHERE tournament_id = $1 ORDER BY total_points DESC, seed ASC LIMIT 1
		), updated_at = now() WHERE id = $1`, tournamentID)
	return err
}

// ClearWinner снимает победителя (при возврате турнира из «завершён» в другой статус).
func (s *Store) ClearWinner(ctx context.Context, tournamentID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE tournaments SET winner_participant_id = NULL, updated_at = now() WHERE id = $1`, tournamentID)
	return err
}

func (s *Store) SetTournamentWinner(ctx context.Context, id, participantID string) (models.Tournament, error) {
	return scanTournament(s.Pool.QueryRow(ctx,
		`UPDATE tournaments SET winner_participant_id = $2, status = 'finished', updated_at = now()
		 WHERE id = $1 RETURNING `+tournamentCols,
		id, participantID))
}
