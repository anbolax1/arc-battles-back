package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// ErrAutoTag - тег роли или победителя сезона выдаёт сайт: его не удалить и не выдать вручную.
var ErrAutoTag = errors.New("тег выдаётся автоматически")

// winnerTagColor - золотой, как у бейджа чемпиона.
const winnerTagColor = "#ffc53d"

// seasonWinnerTagName - «Победитель сезона 2» из названия «Сезон 2».
func seasonWinnerTagName(season string) string {
	name := strings.TrimSpace(season)
	if rest, ok := strings.CutPrefix(name, "Сезон "); ok {
		return "Победитель сезона " + rest
	}
	return "Победитель: " + name
}

const tagCols = `t.id, t.name, t.color, t.visible, COALESCE(t.role, ''), t.season_id, COALESCE(sn.name, '')`

// tagOrder - сначала роли, затем победители сезонов (свежие выше), затем остальные.
const tagOrder = `(t.role IS NULL), (t.season_id IS NULL), sn.started_at DESC`

// roleLevel - уровень роли из колонки col по той же иерархии, что и права доступа; 0 - не роль.
func roleLevel(col string) string {
	var b strings.Builder
	b.WriteString("CASE " + col)
	for _, r := range models.Roles() {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", r, r.Level())
	}
	b.WriteString(" ELSE 0 END")
	return b.String()
}

// holdsRoleTag - у пользователя есть тег роли, если его роль не ниже: организатор тоже игрок.
func holdsRoleTag(user, tag string) string {
	return roleLevel(tag+".role") + " > 0 AND " + roleLevel(user+".role") + " >= " + roleLevel(tag+".role")
}

func scanTag(row pgx.Row) (models.Tag, error) {
	var t models.Tag
	err := row.Scan(&t.ID, &t.Name, &t.Color, &t.Visible, &t.Role, &t.SeasonID, &t.SeasonName)
	return t, err
}

func autoTag(t models.Tag) bool { return t.Role != "" || t.SeasonID != nil }

// ListTags - все теги с теми, кому они выданы; у тега роли вместо списка - сколько у него держателей.
func (s *Store) ListTags(ctx context.Context) ([]models.Tag, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+tagCols+`, CASE WHEN t.role IS NOT NULL
		         THEN (SELECT COUNT(*) FROM users u WHERE `+holdsRoleTag("u", "t")+`)
		         ELSE (SELECT COUNT(*) FROM user_tags ut WHERE ut.tag_id = t.id) END
		FROM tags t LEFT JOIN seasons sn ON sn.id = t.season_id
		ORDER BY `+tagOrder+`, lower(t.name)`)
	if err != nil {
		return nil, err
	}
	out := []models.Tag{}
	byID := map[string]int{}
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Visible, &t.Role, &t.SeasonID, &t.SeasonName, &t.HolderCount); err != nil {
			rows.Close()
			return nil, err
		}
		t.Holders = []models.TagHolder{}
		byID[t.ID] = len(out)
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hr, err := s.Pool.Query(ctx, `
		SELECT ut.tag_id, u.id, u.login, u.display_name
		FROM user_tags ut JOIN users u ON u.id = ut.user_id
		ORDER BY lower(COALESCE(NULLIF(u.display_name, ''), u.login))`)
	if err != nil {
		return nil, err
	}
	defer hr.Close()
	for hr.Next() {
		var tagID string
		var h models.TagHolder
		if err := hr.Scan(&tagID, &h.UserID, &h.Login, &h.DisplayName); err != nil {
			return nil, err
		}
		if i, ok := byID[tagID]; ok {
			out[i].Holders = append(out[i].Holders, h)
		}
	}
	return out, hr.Err()
}

func (s *Store) getTag(ctx context.Context, id string) (models.Tag, error) {
	t, err := scanTag(s.Pool.QueryRow(ctx,
		`SELECT `+tagCols+` FROM tags t LEFT JOIN seasons sn ON sn.id = t.season_id WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) CreateTag(ctx context.Context, name, color string, visible bool) (models.Tag, error) {
	var id string
	if err := s.Pool.QueryRow(ctx,
		`INSERT INTO tags (name, color, visible) VALUES ($1, $2, $3) RETURNING id`, name, color, visible).Scan(&id); err != nil {
		return models.Tag{}, err
	}
	return s.getTag(ctx, id)
}

// UpdateTag меняет название, цвет и видимость тега, в том числе у тегов ролей и победителей сезонов.
func (s *Store) UpdateTag(ctx context.Context, id, name, color string, visible bool) (models.Tag, error) {
	ct, err := s.Pool.Exec(ctx, `UPDATE tags SET name = $2, color = $3, visible = $4 WHERE id = $1`, id, name, color, visible)
	if err != nil {
		return models.Tag{}, err
	}
	if ct.RowsAffected() == 0 {
		return models.Tag{}, ErrNotFound
	}
	return s.getTag(ctx, id)
}

func (s *Store) DeleteTag(ctx context.Context, id string) error {
	t, err := s.getTag(ctx, id)
	if err != nil {
		return err
	}
	if autoTag(t) {
		return ErrAutoTag
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM tags WHERE id = $1`, id)
	return err
}

// SetTagHolder выдаёт тег игроку или забирает его.
func (s *Store) SetTagHolder(ctx context.Context, tagID, userID string, on bool) error {
	t, err := s.getTag(ctx, tagID)
	if err != nil {
		return err
	}
	if autoTag(t) {
		return ErrAutoTag
	}
	if !on {
		_, err := s.Pool.Exec(ctx, `DELETE FROM user_tags WHERE tag_id = $1 AND user_id = $2`, tagID, userID)
		return err
	}
	if _, err := s.GetUser(ctx, userID); err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO user_tags (user_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, tagID)
	return err
}

// TagsForUsers - теги игроков по их id, теги ролей первыми (старшая роль выше); onlyVisible оставляет
// те, что видны на сайте: их показывает организатор и не скрыл сам игрок.
func (s *Store) TagsForUsers(ctx context.Context, userIDs []string, onlyVisible bool) (map[string][]models.UserTag, error) {
	out := map[string][]models.UserTag{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT x.user_id, t.id, t.name, t.color, t.visible,
		       EXISTS (SELECT 1 FROM user_hidden_tags uh WHERE uh.user_id = x.user_id AND uh.tag_id = t.id) AS hidden
		FROM (
			SELECT u.id AS user_id, r.id AS tag_id, u.created_at AS given_at
			FROM users u JOIN tags r ON `+holdsRoleTag("u", "r")+` WHERE u.id = ANY($1)
			UNION ALL
			SELECT ut.user_id, ut.tag_id, ut.created_at FROM user_tags ut WHERE ut.user_id = ANY($1)
		) x
		JOIN tags t ON t.id = x.tag_id
		LEFT JOIN seasons sn ON sn.id = t.season_id
		WHERE NOT $2 OR (t.visible AND NOT EXISTS (
			SELECT 1 FROM user_hidden_tags uh WHERE uh.user_id = x.user_id AND uh.tag_id = t.id))
		ORDER BY `+tagOrder+`, `+roleLevel("t.role")+` DESC, x.given_at`, userIDs, onlyVisible)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		var t models.UserTag
		if err := rows.Scan(&uid, &t.ID, &t.Name, &t.Color, &t.Visible, &t.HiddenByUser); err != nil {
			return nil, err
		}
		out[uid] = append(out[uid], t)
	}
	return out, rows.Err()
}

// UserTags - теги одного игрока (см. TagsForUsers).
func (s *Store) UserTags(ctx context.Context, userID string, onlyVisible bool) ([]models.UserTag, error) {
	m, err := s.TagsForUsers(ctx, []string{userID}, onlyVisible)
	if err != nil {
		return nil, err
	}
	return m[userID], nil
}

// SetTagHiddenByUser убирает свой тег из профиля или возвращает его. Чужой тег трогать нельзя.
func (s *Store) SetTagHiddenByUser(ctx context.Context, userID, tagID string, hidden bool) error {
	tags, err := s.UserTags(ctx, userID, false)
	if err != nil {
		return err
	}
	own := false
	for _, t := range tags {
		own = own || t.ID == tagID
	}
	if !own {
		return ErrNotFound
	}
	if hidden {
		_, err = s.Pool.Exec(ctx,
			`INSERT INTO user_hidden_tags (user_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, tagID)
	} else {
		_, err = s.Pool.Exec(ctx, `DELETE FROM user_hidden_tags WHERE user_id = $1 AND tag_id = $2`, userID, tagID)
	}
	return err
}

// SyncSeasonWinnerTags выдаёт победителю каждого завершённого сезона его тег: держатель тега -
// всегда первое место таблицы 1×1 этого сезона.
func (s *Store) SyncSeasonWinnerTags(ctx context.Context) error {
	seasons, err := s.ListSeasons(ctx)
	if err != nil {
		return err
	}
	for _, sn := range seasons {
		if sn.Status != "finished" {
			continue
		}
		rows, err := s.Leaderboard(ctx, "1x1", sn.ID)
		if err != nil {
			return err
		}
		var tagID string
		if err := s.Pool.QueryRow(ctx, `
			INSERT INTO tags (name, color, season_id) VALUES ($1, $2, $3)
			ON CONFLICT (season_id) WHERE season_id IS NOT NULL DO UPDATE SET season_id = EXCLUDED.season_id
			RETURNING id`, seasonWinnerTagName(sn.Name), winnerTagColor, sn.ID).Scan(&tagID); err != nil {
			return err
		}
		winner := ""
		if len(rows) > 0 {
			winner = rows[0].UserID
		}
		if _, err := s.Pool.Exec(ctx, `DELETE FROM user_tags WHERE tag_id = $1 AND user_id <> $2`, tagID, winner); err != nil {
			return err
		}
		if winner != "" {
			if _, err := s.Pool.Exec(ctx,
				`INSERT INTO user_tags (user_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, winner, tagID); err != nil {
				return err
			}
		}
	}
	// Сезон снова открыли - победителя у него пока нет.
	_, err = s.Pool.Exec(ctx, `
		DELETE FROM tags WHERE season_id IS NOT NULL
		  AND season_id NOT IN (SELECT id FROM seasons WHERE status = 'finished')`)
	return err
}
