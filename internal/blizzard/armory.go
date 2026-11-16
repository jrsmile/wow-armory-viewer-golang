package blizzard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
)

// GameForever marks a World of Warcraft: Forever character. Forever is
// realmless: characters are addressed by a "<name>-<surname>" slug.
const GameForever = "forever"

type CharacterURL struct {
	Region string `json:"region"`
	Realm  string `json:"realm"`
	Name   string `json:"name"`
	Game   string `json:"game,omitempty"` // "" (retail) or GameForever
}

func ParseArmoryURL(raw string) (CharacterURL, error) {
	var character CharacterURL
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		(u.Hostname() != "worldofwarcraft.blizzard.com" && u.Hostname() != "worldofwarcraft.com") {
		return character, fmt.Errorf("enter an HTTPS character URL from worldofwarcraft.blizzard.com")
	}
	// Classic layout: /<locale>/character/<region>/<realm>/<name>
	// Current layout: /<locale>/<section>/<region>/armory/character/<realm>/<name>
	// (e.g. /de-de/worldsoul/eu/armory/character/onyxia/taddie).
	// Forever layout: /<locale>/forever/<region>/armory/character/<name>-<surname>
	var region, realm, name, game string
	switch parts := strings.Split(strings.Trim(u.Path, "/"), "/"); {
	case len(parts) == 5 && parts[1] == "character":
		region, realm, name = parts[2], parts[3], parts[4]
	case len(parts) == 7 && parts[3] == "armory" && parts[4] == "character":
		region, realm, name = parts[2], parts[5], parts[6]
	case len(parts) == 6 && parts[1] == GameForever && parts[3] == "armory" && parts[4] == "character":
		region, name, game = parts[2], parts[5], GameForever
	default:
		return character, fmt.Errorf("expected /<locale>/character/<region>/<realm>/<name>, /<locale>/<section>/<region>/armory/character/<realm>/<name> or /<locale>/forever/<region>/armory/character/<name>-<surname>")
	}
	switch region {
	case "us", "eu", "kr", "tw":
	default:
		return character, fmt.Errorf("only retail us, eu, kr, and tw profiles are supported")
	}
	values := []string{realm, name}
	if game == GameForever {
		values = values[1:]
	}
	for _, value := range values {
		if value == "" || value == "." || value == ".." || len(value) > 128 {
			return character, fmt.Errorf("invalid character or realm slug")
		}
		for _, r := range value {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '\'' {
				return character, fmt.Errorf("invalid character or realm slug")
			}
		}
	}
	return CharacterURL{region, strings.ToLower(realm), strings.ToLower(name), game}, nil
}

type Client struct {
	ClientID     string
	ClientSecret string
	HTTP         *http.Client
	OAuthURL     string
	APIBase      func(string) string
	// ProxyURL serves Blizzard's /appearance JSON without credentials; it is
	// queried with region, realm and character parameters when no client
	// credentials are configured. Empty disables the fallback.
	ProxyURL string
	// WebBase is the Armory website used for Forever profiles; empty means
	// DefaultWebBase.
	WebBase string
	mu      sync.Mutex
	token   string
	expires time.Time
}

func New(id, secret string) *Client {
	return &Client{ClientID: id, ClientSecret: secret,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		OAuthURL: "https://oauth.battle.net/token",
		APIBase:  func(region string) string { return "https://" + region + ".api.blizzard.com" },
		ProxyURL: DefaultProxy}
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return "", fmt.Errorf("set BLIZZARD_CLIENT_ID and BLIZZARD_CLIENT_SECRET locally; create credentials at https://develop.battle.net")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.OAuthURL,
		strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("Blizzard OAuth request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Blizzard OAuth HTTP %d; check your local credentials", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return "", fmt.Errorf("invalid OAuth response")
	}
	if token.AccessToken == "" || token.ExpiresIn <= 0 {
		return "", fmt.Errorf("OAuth response has no valid token")
	}
	c.token = token.AccessToken
	c.expires = time.Now().Add(time.Duration(token.ExpiresIn)*time.Second - 30*time.Second)
	return c.token, nil
}

// DefaultProxy is a public pass-through of the Blizzard character appearance API.
const DefaultProxy = "https://marlam.in/we/api.php"

func (c *Client) get(ctx context.Context, u, token string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return data, resp.StatusCode, err
}

// HasCredentials reports whether official API credentials are configured.
func (c *Client) HasCredentials() bool { return c.ClientID != "" && c.ClientSecret != "" }

// AppearanceJSON returns the character's current /appearance document
// (race, gender, customization choices and transmogrified item appearances).
func (c *Client) AppearanceJSON(ctx context.Context, character CharacterURL) ([]byte, error) {
	if character.Game == GameForever {
		return c.foreverAppearance(ctx, character)
	}
	var u, token string
	if c.HasCredentials() || c.ProxyURL == "" {
		var err error
		if token, err = c.accessToken(ctx); err != nil {
			return nil, err
		}
		q := url.Values{"namespace": {"profile-" + character.Region}, "locale": {"en_US"}}
		u = c.APIBase(character.Region) + "/profile/wow/character/" + url.PathEscape(character.Realm) + "/" + url.PathEscape(character.Name) + "/appearance?" + q.Encode()
	} else {
		q := url.Values{"region": {character.Region}, "realm": {character.Realm}, "character": {character.Name}}
		u = c.ProxyURL + "?" + q.Encode()
	}
	data, status, err := c.get(ctx, u, token, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("appearance request failed: %w", err)
	}
	if status == http.StatusUnauthorized && token != "" {
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("appearance HTTP %d for %s/%s (profile may be private, misspelled, or not recently logged out)", status, character.Realm, character.Name)
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid appearance response")
	}
	return data, nil
}
