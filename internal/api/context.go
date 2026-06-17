package api

import (
	"context"

	"github.com/battle-for-respect/backend/internal/models"
)

type ctxKey int

const userCtxKey ctxKey = iota

func withUser(ctx context.Context, u *models.User) context.Context {
	return context.WithValue(ctx, userCtxKey, u)
}

func userFrom(ctx context.Context) (*models.User, bool) {
	u, ok := ctx.Value(userCtxKey).(*models.User)
	return u, ok && u != nil
}
