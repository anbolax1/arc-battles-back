package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// TwitchOAuth возвращает конфиг OAuth2 для входа через Twitch.
func TwitchOAuth(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"user:read:email"},
		Endpoint:     endpoints.Twitch,
	}
}

type TwitchUser struct {
	ID              string `json:"id"`
	Login           string `json:"login"`
	DisplayName     string `json:"display_name"`
	ProfileImageURL string `json:"profile_image_url"`
	Email           string `json:"email"`
}

// FetchTwitchUser получает профиль авторизованного пользователя из Helix API.
func FetchTwitchUser(ctx context.Context, clientID, accessToken string) (TwitchUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.twitch.tv/helix/users", nil)
	if err != nil {
		return TwitchUser{}, err
	}
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return TwitchUser{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return TwitchUser{}, fmt.Errorf("twitch helix /users: status %d", resp.StatusCode)
	}

	var body struct {
		Data []TwitchUser `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return TwitchUser{}, err
	}
	if len(body.Data) == 0 {
		return TwitchUser{}, fmt.Errorf("twitch helix /users: empty data")
	}
	return body.Data[0], nil
}
