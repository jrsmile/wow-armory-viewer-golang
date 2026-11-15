package blizzard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseArmoryURL(t *testing.T) {
	for _, raw := range []string{
		"https://worldofwarcraft.blizzard.com/en-gb/character/eu/argent-dawn/élune/",
		"https://worldofwarcraft.com/en-us/character/us/area-52/test?foo=bar",
	} {
		if _, err := ParseArmoryURL(raw); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ParseArmoryURL("https://worldofwarcraft.blizzard.com/de-de/worldsoul/eu/armory/character/onyxia/Taddie")
	if err != nil || got != (CharacterURL{"eu", "onyxia", "taddie"}) {
		t.Fatal(got, err)
	}
	for _, raw := range []string{
		"http://worldofwarcraft.blizzard.com/en-us/character/us/area-52/test",
		"https://worldofwarcraft.blizzard.com.evil.test/en-us/character/us/a/b",
		"https://user@worldofwarcraft.blizzard.com/en-us/character/us/a/b",
		"https://worldofwarcraft.blizzard.com:443/en-us/character/us/a/b",
		"https://worldofwarcraft.blizzard.com/en-us/character/cn/a/b",
		"https://worldofwarcraft.blizzard.com/en-us/character/us/a/%2f",
		"https://worldofwarcraft.blizzard.com/en-us/character/us/a/..",
		"https://worldofwarcraft.blizzard.com/de-de/worldsoul/cn/armory/character/a/b",
		"https://worldofwarcraft.blizzard.com/de-de/worldsoul/eu/other/character/a/b",
	} {
		if _, err := ParseArmoryURL(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestOfficialAppearanceAndTokenReuse(t *testing.T) {
	authCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			authCalls++
			id, secret, ok := r.BasicAuth()
			if !ok || id != "test-id" || secret != "test-secret" {
				t.Error("missing OAuth basic authentication")
			}
			fmt.Fprint(w, `{"access_token":"test-token","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Query().Get("namespace") != "profile-eu" ||
			r.URL.Path != "/profile/wow/character/realm/example/appearance" {
			t.Errorf("unexpected profile request %s", r.URL)
		}
		fmt.Fprint(w, `{"customizations":[{"option":{"id":10},"choice":{"id":20}}]}`)
	}))
	defer server.Close()
	c := New("test-id", "test-secret")
	c.OAuthURL, c.APIBase = server.URL+"/token", func(string) string { return server.URL }
	c.ProxyURL = "http://proxy.invalid" // credentials take precedence
	for range 2 {
		data, err := c.AppearanceJSON(context.Background(), CharacterURL{"eu", "realm", "example"})
		if err != nil || !strings.Contains(string(data), `"id":20`) {
			t.Fatal(string(data), err)
		}
	}
	if authCalls != 1 {
		t.Fatalf("OAuth requested %d times", authCalls)
	}
}

func TestAppearanceProxyWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("Authorization") != "" || q.Get("region") != "eu" || q.Get("realm") != "tarren-mill" || q.Get("character") != "naowh" {
			t.Errorf("unexpected proxy request %s", r.URL)
		}
		fmt.Fprint(w, `{"playable_race":{"id":10}}`)
	}))
	defer server.Close()
	c := New("", "")
	c.ProxyURL = server.URL
	data, err := c.AppearanceJSON(context.Background(), CharacterURL{"eu", "tarren-mill", "naowh"})
	if err != nil || !strings.Contains(string(data), `"id":10`) {
		t.Fatal(string(data), err)
	}
}

func TestMissingCredentials(t *testing.T) {
	c := New("", "")
	c.ProxyURL = ""
	_, err := c.AppearanceJSON(context.Background(), CharacterURL{"us", "realm", "test"})
	if err == nil || !strings.Contains(err.Error(), "BLIZZARD_CLIENT_ID") {
		t.Fatal(err)
	}
}
