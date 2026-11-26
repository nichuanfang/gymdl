package lyricmeta

import "testing"

func TestFromLyricsText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Evidence
	}{
		{"empty", "", Unknown},
		{"whitespace", "   \n\t ", Unknown},
		{"real lyric", "[00:01.00]你好世界\n[00:05.00]第二句", Present},
		{"plain lyric", "这是一段没有时间轴的歌词", Present},
		{"legacy placeholder", "[00:00:00]此歌曲为没有填词的纯音乐，请您欣赏", Unknown},
		{"placeholder without timestamp", "此歌曲为没有填词的纯音乐，请您欣赏", Unknown},
		// A real lyric that merely mentions the phrase must not be dropped.
		{"lyric mentioning the phrase",
			"[00:01.00]这首此歌曲为没有填词的纯音乐，请您欣赏\n[00:10.00]是一句玩笑\n[00:20.00]第三行\n[00:30.00]第四行", Present},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromLyricsText(tc.text); got != tc.want {
				t.Fatalf("FromLyricsText(%q) = %s, want %s", tc.text, got, tc.want)
			}
		})
	}
}

func TestFromSourceOnlyTrustsExplicitInstrumental(t *testing.T) {
	if got := FromSource("", false); got != Unknown {
		t.Fatalf("missing lyrics must stay unknown, got %s", got)
	}
	if got := FromSource("[00:00:00]此歌曲为没有填词的纯音乐，请您欣赏", false); got != Unknown {
		t.Fatalf("placeholder text alone must not become instrumental, got %s", got)
	}
	if got := FromSource("", true); got != ExplicitInstrumental {
		t.Fatalf("an explicit flag should classify instrumental, got %s", got)
	}
	if got := FromSource("[00:01.00]real lyric", true); got != Present {
		t.Fatalf("real lyrics win over a stale flag, got %s", got)
	}
}

func TestNormalizeAndConflict(t *testing.T) {
	if Normalize("") != Unknown || Normalize("bogus") != Unknown || Normalize(Present) != Present {
		t.Fatal("normalize drifted")
	}
	if !Conflict(Present, ExplicitInstrumental) || !Conflict(ExplicitInstrumental, Present) {
		t.Fatal("vocal/instrumental must conflict in both orders")
	}
	for _, pair := range [][2]Evidence{
		{Present, Present},
		{ExplicitInstrumental, ExplicitInstrumental},
		{Unknown, Present},
		{Unknown, ExplicitInstrumental},
		{Unknown, Unknown},
		{"", Present},
	} {
		if Conflict(pair[0], pair[1]) {
			t.Fatalf("%s vs %s should not conflict", pair[0], pair[1])
		}
	}
}

func TestReadFileMissingPathIsUnknown(t *testing.T) {
	if got := ReadFile("/definitely/not/a/real/audio/file.flac"); got != Unknown {
		t.Fatalf("unreadable file must be unknown, got %s", got)
	}
}
