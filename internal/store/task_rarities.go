package store

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// ErrBadRarities - в списке редкостей пусто или есть незнакомая.
var ErrBadRarities = errors.New("выберите хотя бы одну редкость из списка")

// raritySQL - редкости по порядку от обычной к легендарной: номер в массиве = ранг редкости.
var raritySQL = "ARRAY['" + strings.Join(models.TaskRarities, "','") + "']"

// NormalizeTaskRarities проверяет список редкостей, убирает повторы и ставит в порядке от обычной к легендарной.
func NormalizeTaskRarities(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, ErrBadRarities
	}
	out := make([]string, 0, len(in))
	for _, r := range models.TaskRarities {
		if slices.Contains(in, r) {
			out = append(out, r)
		}
	}
	for _, r := range in {
		if !slices.Contains(models.TaskRarities, r) {
			return nil, ErrBadRarities
		}
	}
	return out, nil
}

// rarityRanks - ранги (с 1) выбранных редкостей: по ним раздача ищет ближайшие задания.
func rarityRanks(rarities []string) []int32 {
	out := make([]int32, 0, len(rarities))
	for _, r := range rarities {
		if i := slices.Index(models.TaskRarities, r); i >= 0 {
			out = append(out, int32(i+1))
		}
	}
	return out
}

// GetUserTaskRarities - какие редкости заданий хочет получать игрок (по умолчанию все).
func (s *Store) GetUserTaskRarities(ctx context.Context, userID string) ([]string, error) {
	var out []string
	err := s.Pool.QueryRow(ctx, `SELECT task_rarities FROM users WHERE id = $1`, userID).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return out, err
}

// SaveUserTaskRarities сохраняет предпочтения игрока и возвращает их в каноническом виде.
func (s *Store) SaveUserTaskRarities(ctx context.Context, userID string, rarities []string) ([]string, error) {
	norm, err := NormalizeTaskRarities(rarities)
	if err != nil {
		return nil, err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET task_rarities = $2 WHERE id = $1`, userID, norm)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return norm, nil
}

// sideRarities - какие редкости заданий выдавать стороне матча: ручная настройка стороны, а если её нет -
// предпочтения игроков стороны из профиля (у команды 2×2 складываются), а если и их нет - любые.
func (s *Store) sideRarities(ctx context.Context, p models.Participant) ([]string, error) {
	var manual []string
	if err := s.Pool.QueryRow(ctx, `SELECT task_rarities FROM participants WHERE id = $1`, p.ID).Scan(&manual); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if len(manual) > 0 {
		return manual, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT task_rarities FROM users WHERE id = ANY($1)`, participantUsers(p))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var rs []string
		if err := rows.Scan(&rs); err != nil {
			return nil, err
		}
		for _, r := range rs {
			if !slices.Contains(all, r) {
				all = append(all, r)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return slices.Clone(models.TaskRarities), nil
	}
	return all, nil
}
