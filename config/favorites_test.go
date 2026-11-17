package config

import "testing"

func TestFavoritesDefaultsAndExplicitIntervals(t *testing.T) {
	c := &Config{}
	c.setDefaults()
	if c.Favorites == nil || c.Favorites.Enable || c.Favorites.PollIntervalMinutes != 10 || c.Favorites.SummaryIntervalMinutes != 30 {
		t.Fatalf("unsafe defaults %+v", c.Favorites)
	}
	c.Favorites = &FavoritesConfig{Enable: true, PollIntervalMinutes: 2, SummaryIntervalMinutes: 5}
	c.setDefaults()
	if !c.Favorites.Enable || c.Favorites.PollIntervalMinutes != 2 || c.Favorites.SummaryIntervalMinutes != 5 {
		t.Fatal("explicit settings lost")
	}
}
