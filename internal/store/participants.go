package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound — запись не найдена (для отдачи 404 на уровне хендлера).
var ErrNotFound = errors.New("запись не найдена")

const participantCols = `id, tournament_id, kind, user_id, name, seed, total_points, members`

// ParticipantUpdate — частичное обновление участника (nil-поля не трогаются).
type ParticipantUpdate struct {
	TotalPoints *int
	Name        *string
	Seed        *int
	UserID      *string
	Members     json.RawMessage
}

func scanParticipant(row pgx.Row) (models.Participant, error) {
	var p models.Participant
	var members []byte
	err := row.Scan(&p.ID, &p.TournamentID, &p.Kind, &p.UserID, &p.Name, &p.Seed, &p.TotalPoints, &members)
	if err != nil {
		return p, err
	}
	if len(members) > 0 {
		p.Members = json.RawMessage(members)
	} else {
		p.Members = json.RawMessage("[]")
	}
	return p, nil
}

func (s *Store) AddParticipant(ctx context.Context, p models.Participant) (models.Participant, error) {
	members := []byte("[]")
	if len(p.Members) > 0 {
		members = p.Members
	}
	if p.Kind == "" {
		p.Kind = "player"
	}
	const q = `
		INSERT INTO participants (tournament_id, kind, user_id, name, seed, members)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + participantCols
	return scanParticipant(s.Pool.QueryRow(ctx, q, p.TournamentID, p.Kind, p.UserID, p.Name, p.Seed, string(members)))
}

func (s *Store) ListParticipants(ctx context.Context, tournamentID string) ([]models.Participant, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT `+participantCols+` FROM participants WHERE tournament_id = $1 ORDER BY total_points DESC, seed ASC`,
		tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Participant{}
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetParticipant(ctx context.Context, id string) (models.Participant, error) {
	p, err := scanParticipant(s.Pool.QueryRow(ctx, `SELECT `+participantCols+` FROM participants WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) RemoveParticipant(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM participants WHERE id = $1`, id)
	return err
}

// HasParticipantForUser — есть ли уже участник этого пользователя в турнире
// (для идемпотентного приёма заявки — без дублей).
func (s *Store) HasParticipantForUser(ctx context.Context, tournamentID, userID string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM participants WHERE tournament_id = $1 AND user_id = $2)`,
		tournamentID, userID).Scan(&exists)
	return exists, err
}

// UpdateParticipant частично обновляет участника. Возвращает ErrNotFound, если id нет.
func (s *Store) UpdateParticipant(ctx context.Context, id string, u ParticipantUpdate) (models.Participant, error) {
	sets := []string{}
	args := []any{}
	n := 1
	add := func(col string, v any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, n))
		args = append(args, v)
		n++
	}
	if u.TotalPoints != nil {
		add("total_points", *u.TotalPoints)
	}
	if u.Name != nil {
		add("name", strings.TrimSpace(*u.Name))
	}
	if u.Seed != nil {
		add("seed", *u.Seed)
	}
	if u.UserID != nil {
		add("user_id", *u.UserID)
	}
	if u.Members != nil {
		add("members", string(u.Members))
	}

	if len(sets) == 0 {
		// нечего менять — вернём текущего (или 404)
		p, err := scanParticipant(s.Pool.QueryRow(ctx,
			`SELECT `+participantCols+` FROM participants WHERE id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return p, ErrNotFound
		}
		return p, err
	}

	args = append(args, id)
	q := `UPDATE participants SET ` + strings.Join(sets, ", ") +
		fmt.Sprintf(` WHERE id = $%d RETURNING `, n) + participantCols
	p, err := scanParticipant(s.Pool.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}
