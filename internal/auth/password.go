package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost — стоимость хеширования (2^cost раундов). 12 — разумный баланс
// стойкости и скорости на текущий момент.
const bcryptCost = 12

// MaxPasswordBytes — лимит bcrypt: пароли длиннее 72 байт обрезаются/отвергаются,
// поэтому ограничиваем длину ещё на валидации.
const MaxPasswordBytes = 72

// HashPassword возвращает bcrypt-хеш пароля (соль хранится внутри хеша).
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword сравнивает пароль с хешем за постоянное время (внутри bcrypt).
// Возвращает true только при совпадении.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
