package store

import "context"

// TaskPoolIDs - обычные задания, которые могут выпасть в раунде: общие и его карты, ещё не выданные в матче.
func TaskPoolIDs(ctx context.Context, s *Store, roundID, mapCode string) (map[string]bool, error) {
	pool, err := s.normalTaskPool(ctx, roundID, mapCode)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(pool))
	for _, t := range pool {
		out[t.ID] = true
	}
	return out, nil
}
