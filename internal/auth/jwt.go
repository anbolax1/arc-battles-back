package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// IssueToken подписывает JWT с subject=userID и ролью.
func IssueToken(secret, userID, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ParseToken проверяет подпись/срок и возвращает userID, роль и момент выпуска (iat).
// iat нужен для серверной ревокации сессий (сравнение с users.tokens_valid_after).
func ParseToken(secret, tokenStr string) (userID, role string, issuedAt time.Time, err error) {
	var claims Claims
	token, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return "", "", time.Time{}, err
	}
	if !token.Valid {
		return "", "", time.Time{}, errors.New("invalid token")
	}
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	return claims.Subject, claims.Role, issuedAt, nil
}
