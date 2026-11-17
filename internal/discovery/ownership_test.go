package discovery

import (
	"context"
	"testing"
	"time"
)

func TestOwnedWorkMatchesLibraryDespiteMessyCredits(t *testing.T) {
	library := &NavidromeLibrary{byTitle: map[string][]Track{}, expires: time.Now().Add(time.Minute)}
	add := func(title, artist, album string, duration int) {
		track := Track{Title: title, Artist: artist, Album: album, Duration: duration, Verified: true}
		key := ownershipTitle(title)
		library.byTitle[key] = append(library.byTitle[key], track)
	}
	add("Wound Up", "Jordyn Edmonds", "Wound Up", 102)
	add("执迷不悔", "王菲", "执迷不悔 (Remastered 2019)", 268)
	add("瓦解", "Nan Quan Mama", "南拳妈妈的夏天", 222)
	add("Price", "目黒将司", "PERSONA5 オリジナル・サウンドトラック", 109)
	add("Miçanga (feat. BaianaSystem) [with Alok, Kawz]", "SOMETHING ELSE • Stephan Jolk", "Miçanga (feat. BaianaSystem) [with Alok, Kawz] - Single", 186)
	add("竜の岛", "Nintendo Sound Team", "ゼルダの伝说 风のタクト HD SOUND SELECTION", 101)
	add("Song", "Artist A", "Album A", 200)
	add("Song", "Artist B", "Album B", 180)
	add("Go", "Someone", "Short", 100)

	cases := []struct {
		title, artist, album string
		duration             int
		want                 bool
	}{
		{"Wound Up", "Jordyn Edmonds", "Wound Up", 155, true},
		{"執迷不悔", "王菲", "執迷不悔", 0, true},
		{"執迷不悔", "Faye Wong", "執迷不悔", 0, true},
		{"瓦解", "南拳媽媽", "南拳媽媽的夏天", 222, true},
		{"Price", "アトラスサウンドチーム", "PERSONA5 ORIGINAL SOUNDTRACK", 109, true},
		{"Miçanga (feat. BaianaSystem) [with Alok, Kawz]", "Something Else • Stephan Jolk • Alok • Baianasystem • Kawz", "Miçanga (feat. BaianaSystem) [with Alok, Kawz]", 186, true},
		{"竜の島", "永田権太", "The Legend of Zelda: The Wind Waker HD Sound Selection", 96, true},
		{"Song Remix", "Artist A", "Album A", 200, false},
		{"Song", "Artist C", "Other Album", 200, false},
		{"Go", "Different Person", "Other", 90, false},
		{"Missing", "Nobody", "None", 10, false},
	}
	for _, tc := range cases {
		owned, err := library.Contains(context.Background(), Track{Title: tc.title, Artist: tc.artist, Album: tc.album, Duration: tc.duration})
		if err != nil || owned != tc.want {
			t.Errorf("%s / %s owned=%v err=%v want %v", tc.title, tc.artist, owned, err, tc.want)
		}
	}
}

func TestRecommendSkipsWorksTheLibraryAlreadyHas(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owned := Track{Title: "Wound Up", Artist: "Jordyn Edmonds", Album: "Wound Up", Duration: 155, Plays: 138, Favorite: true}
	missing := Track{Title: "Not In Library", Artist: "New Artist", Plays: 10}
	library := &NavidromeLibrary{
		byTitle: map[string][]Track{ownershipTitle(owned.Title): {{Title: owned.Title, Artist: owned.Artist, Album: owned.Album, Duration: 102, Verified: true}}},
		expires: time.Now().Add(time.Minute),
	}
	svc := Service{Store: store, Provider: stubProvider{}, Library: library, History: func(context.Context) ([]Track, error) {
		return []Track{owned, missing}, nil
	}}
	batch, err := svc.Rediscover(ctx, 1, "netease", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Tracks) != 1 || batch.Tracks[0].Title != "Not In Library" || batch.SkippedOwned != 1 {
		t.Fatalf("batch=%+v", batch)
	}
}
