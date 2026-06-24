package store

import (
	"context"
	"errors"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const legendaryCols = `id, text, points, kind, source, author, title, status`

func scanLegendary(row pgx.Row) (models.CatalogLegendary, error) {
	var l models.CatalogLegendary
	err := row.Scan(&l.ID, &l.Text, &l.Points, &l.Kind, &l.Source, &l.Author, &l.Title, &l.Status)
	return l, err
}

// ListLegendary — все легендарные контракты + запись о выполнении (если выполнен).
func (s *Store) ListLegendary(ctx context.Context) ([]models.CatalogLegendary, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+legendaryCols+` FROM legendary_contracts ORDER BY sort_order, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.CatalogLegendary{}
	index := map[string]int{}
	for rows.Next() {
		l, err := scanLegendary(rows)
		if err != nil {
			return nil, err
		}
		index[l.ID] = len(out)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	cRows, err := s.Pool.Query(ctx, `
		SELECT lcc.id, lcc.legendary_contract_id, lcc.user_id, lcc.participant_id, lcc.nickname,
		       lcc.tournament_id, lcc.map, lcc.completed_at, COALESCE(t.title, '')
		FROM legendary_contract_completions lcc
		LEFT JOIN tournaments t ON t.id = lcc.tournament_id`)
	if err != nil {
		return nil, err
	}
	defer cRows.Close()
	for cRows.Next() {
		var c models.LegendaryCompletion
		if err := cRows.Scan(&c.ID, &c.LegendaryContractID, &c.UserID, &c.ParticipantID, &c.Nickname,
			&c.TournamentID, &c.Map, &c.CompletedAt, &c.TournamentTitle); err != nil {
			return nil, err
		}
		if i, ok := index[c.LegendaryContractID]; ok {
			cc := c
			out[i].Completion = &cc
		}
	}
	return out, cRows.Err()
}

// GetLegendary возвращает один легендарный контракт с данными о выполнении (если есть).
func (s *Store) GetLegendary(ctx context.Context, id string) (models.CatalogLegendary, error) {
	l, err := scanLegendary(s.Pool.QueryRow(ctx, `SELECT `+legendaryCols+` FROM legendary_contracts WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	var c models.LegendaryCompletion
	err = s.Pool.QueryRow(ctx, `
		SELECT lcc.id, lcc.legendary_contract_id, lcc.user_id, lcc.participant_id, lcc.nickname,
		       lcc.tournament_id, lcc.map, lcc.completed_at, COALESCE(t.title, '')
		FROM legendary_contract_completions lcc
		LEFT JOIN tournaments t ON t.id = lcc.tournament_id
		WHERE lcc.legendary_contract_id = $1`, id).
		Scan(&c.ID, &c.LegendaryContractID, &c.UserID, &c.ParticipantID, &c.Nickname,
			&c.TournamentID, &c.Map, &c.CompletedAt, &c.TournamentTitle)
	if err == nil {
		l.Completion = &c
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return l, err
	}
	return l, nil
}

func (s *Store) CreateLegendary(ctx context.Context, l models.CatalogLegendary) (models.CatalogLegendary, error) {
	if l.Points <= 0 {
		l.Points = 10
	}
	const q = `
		INSERT INTO legendary_contracts (text, points, kind, source, author, title, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM legendary_contracts))
		RETURNING ` + legendaryCols
	return scanLegendary(s.Pool.QueryRow(ctx, q, l.Text, l.Points, l.Kind, l.Source, l.Author, l.Title))
}

func (s *Store) UpdateLegendary(ctx context.Context, id string, l models.CatalogLegendary) (models.CatalogLegendary, error) {
	if l.Points <= 0 {
		l.Points = 10
	}
	const q = `
		UPDATE legendary_contracts
		SET text = $2, points = $3, kind = $4, source = $5, author = $6, title = $7
		WHERE id = $1
		RETURNING ` + legendaryCols
	res, err := scanLegendary(s.Pool.QueryRow(ctx, q, id, l.Text, l.Points, l.Kind, l.Source, l.Author, l.Title))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, ErrNotFound
	}
	return res, err
}

func (s *Store) DeleteLegendary(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM legendary_contracts WHERE id = $1`, id)
	return err
}

// CompleteLegendary фиксирует выполнение легендарного контракта (один раз навсегда → ErrConflict,
// если уже выполнен). Возвращает participantID (если задан) для пересчёта его очков.
func (s *Store) CompleteLegendary(ctx context.Context, id string, c models.LegendaryCompletion) (*string, error) {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO legendary_contract_completions
			(legendary_contract_id, user_id, participant_id, nickname, tournament_id, map)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, c.UserID, c.ParticipantID, c.Nickname, c.TournamentID, c.Map)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConflict
		}
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE legendary_contracts SET status = 'done' WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return c.ParticipantID, nil
}

// UncompleteLegendary снимает выполнение (возвращает контракт в пул доступных). Возвращает
// participantID прежнего исполнителя (если был) для пересчёта очков.
func (s *Store) UncompleteLegendary(ctx context.Context, id string) (*string, error) {
	var pid *string
	_ = s.Pool.QueryRow(ctx,
		`SELECT participant_id FROM legendary_contract_completions WHERE legendary_contract_id = $1`, id).Scan(&pid)
	if _, err := s.Pool.Exec(ctx,
		`DELETE FROM legendary_contract_completions WHERE legendary_contract_id = $1`, id); err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE legendary_contracts SET status = 'available' WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return pid, nil
}
