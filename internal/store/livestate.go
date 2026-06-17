package store

import "context"

// GetLiveState возвращает сырое jsonb-состояние оверлея (единственная строка id=1).
func (s *Store) GetLiveState(ctx context.Context) ([]byte, error) {
	var data []byte
	if err := s.Pool.QueryRow(ctx, `SELECT data FROM live_state WHERE id = 1`).Scan(&data); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []byte("{}"), nil
	}
	return data, nil
}

func (s *Store) SetLiveState(ctx context.Context, data []byte) error {
	_, err := s.Pool.Exec(ctx, `UPDATE live_state SET data = $1, updated_at = now() WHERE id = 1`, string(data))
	return err
}
