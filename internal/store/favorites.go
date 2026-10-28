package store

import (
	"context"
	"errors"
	"slices"

	"github.com/battle-for-respect/backend/internal/models"
	"github.com/jackc/pgx/v5"
)

// MaxFavoriteTeammates - сколько любимых тиммейтов можно выбрать.
const MaxFavoriteTeammates = 5

// Названия - как в русской версии ARC Raiders, порядок - как в списке организатора.
var (
	FavoriteWeapons = []string{
		"Чайник", "Трещотка", "Заколка", "Строчильщик", "Ферро", "Бурлетта", "Эль-Торо", "Арпеджио", "Наковальня",
		"Охотник", "Торренте", "Ястреб", "Бунтарь", "Канто", "Буря", "Рысь", "Вулкан", "Беттина", "Бронелом",
		"Юпитер", "Уравнитель", "Афелий", "Долабра", "Задира", "Шпилька",
	}
	FavoriteAugments = []string{
		"Добыча 1", "Добыча 2", "Добыча 3 (Выживший)", "Добыча 3 (Осторожность)", "Добыча 3 (Хранитель)",
		"Стратегия 1", "Стратегия 2", "Стратегия 3 (Оборона)", "Стратегия 3 (Восстановление)", "Стратегия 3 (Дым)",
		"Стратегия 3 (Лечение)", "Бой 1", "Бой 2", "Бой 3 (Агрессия)", "Бой 3 (Атака с Фланга)",
	}
	FavoriteShields = []string{"Лёгкий щит", "Средний щит", "Тяжёлый щит"}
)

// ErrBadFavorite - такого нет в списке, из которого выбирают любимое.
var ErrBadFavorite = errors.New("такого варианта нет в списке")

// FavoritesInput - что игрок выбрал; пустая строка - не выбрано.
type FavoritesInput struct {
	Teammates []string
	MapCode   string
	Weapon    string
	Augment   string
	Shield    string
}

func (s *Store) FavoriteOptions(ctx context.Context) (models.FavoriteOptions, error) {
	maps, err := s.ListMaps(ctx)
	return models.FavoriteOptions{Maps: maps, Weapons: FavoriteWeapons, Augments: FavoriteAugments, Shields: FavoriteShields}, err
}

// GetFavorites - любимое игрока; filled - выбрано хоть что-то. Удалённые аккаунты из тиммейтов выпадают.
func (s *Store) GetFavorites(ctx context.Context, userID string) (models.PlayerFavorites, bool, error) {
	f := models.PlayerFavorites{Teammates: []models.TeamMember{}}
	var ids []string
	err := s.Pool.QueryRow(ctx, `
		SELECT pf.teammates, pf.map_code, COALESCE(m.name, ''), pf.weapon, pf.augment, pf.shield
		FROM player_favorites pf LEFT JOIN maps m ON m.code = pf.map_code
		WHERE pf.user_id = $1`, userID).Scan(&ids, &f.MapCode, &f.Map, &f.Weapon, &f.Augment, &f.Shield)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	if len(ids) > 0 {
		rows, err := s.Pool.Query(ctx, `
			SELECT u.id, u.login, u.display_name, u.avatar_url
			FROM unnest($1::text[]) WITH ORDINALITY AS x(id, n) JOIN users u ON u.id = x.id
			ORDER BY x.n`, ids)
		if err != nil {
			return f, false, err
		}
		defer rows.Close()
		for rows.Next() {
			var m models.TeamMember
			if err := rows.Scan(&m.UserID, &m.Login, &m.DisplayName, &m.AvatarURL); err != nil {
				return f, false, err
			}
			f.Teammates = append(f.Teammates, m)
		}
		if err := rows.Err(); err != nil {
			return f, false, err
		}
	}
	filled := len(f.Teammates) > 0 || f.MapCode != "" || f.Weapon != "" || f.Augment != "" || f.Shield != ""
	return f, filled, nil
}

// SaveFavorites проверяет выбор по спискам и сохраняет его целиком.
func (s *Store) SaveFavorites(ctx context.Context, userID string, in FavoritesInput) error {
	for _, c := range []struct {
		v    string
		list []string
	}{{in.Weapon, FavoriteWeapons}, {in.Augment, FavoriteAugments}, {in.Shield, FavoriteShields}} {
		if c.v != "" && !slices.Contains(c.list, c.v) {
			return ErrBadFavorite
		}
	}
	if in.MapCode != "" {
		var ok bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM maps WHERE code = $1 AND active)`, in.MapCode).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrBadFavorite
		}
	}
	mates := []string{}
	for _, id := range in.Teammates {
		if id != "" && id != userID && !slices.Contains(mates, id) {
			mates = append(mates, id)
		}
	}
	if len(mates) > MaxFavoriteTeammates {
		return ErrBadFavorite
	}
	if len(mates) > 0 {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id = ANY($1)`, mates).Scan(&n); err != nil {
			return err
		}
		if n != len(mates) {
			return ErrNotFound
		}
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO player_favorites (user_id, teammates, map_code, weapon, augment, shield, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (user_id) DO UPDATE SET teammates = EXCLUDED.teammates, map_code = EXCLUDED.map_code,
			weapon = EXCLUDED.weapon, augment = EXCLUDED.augment, shield = EXCLUDED.shield, updated_at = now()`,
		userID, mates, in.MapCode, in.Weapon, in.Augment, in.Shield)
	return err
}

// SearchPlayers - игроки сайта по нику для выбора тиммейтов, сначала те, чей ник начинается с запроса.
func (s *Store) SearchPlayers(ctx context.Context, q string, limit int) ([]models.TeamMember, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, login, display_name, avatar_url FROM users
		WHERE login ILIKE $1 OR display_name ILIKE $1
		ORDER BY (lower(login) LIKE lower($2) OR lower(display_name) LIKE lower($2)) DESC, lower(display_name), lower(login)
		LIMIT $3`, "%"+likeEscape(q)+"%", likeEscape(q)+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.TeamMember{}
	for rows.Next() {
		var m models.TeamMember
		if err := rows.Scan(&m.UserID, &m.Login, &m.DisplayName, &m.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
