package discovery

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var historyTableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func LoadHistory(ctx context.Context, dsn, table string) ([]Track, error) {
	if dsn == "" {
		return nil, fmt.Errorf("尚未配置 ai.history_dsn 收听历史")
	}
	if !historyTableName.MatchString(table) {
		return nil, fmt.Errorf("收听历史表名无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("无法打开收听历史")
	}
	defer db.Close()
	// The album is the one this title/artist was scrobbled with most often.
	// MAX(album) used to pick the lexicographic name, so a single stray album
	// tag replaced the real one. Plays stay the total across albums.
	rows, err := db.QueryContext(ctx, `WITH albums AS (
  SELECT name, artist, COALESCE(album, '') AS album, count(*) AS album_plays, MAX(date::timestamptz) AS album_last
  FROM `+table+` WHERE COALESCE(name, '') <> '' AND COALESCE(artist, '') <> ''
  GROUP BY name, artist, COALESCE(album, '')
), picked AS (
  SELECT name, artist, album,
         SUM(album_plays) OVER (PARTITION BY name, artist) AS plays,
         MAX(album_last) OVER (PARTITION BY name, artist) AS last_played,
         row_number() OVER (PARTITION BY name, artist ORDER BY album_plays DESC, album) AS rn
  FROM albums
)
SELECT name, artist, album, plays, last_played FROM picked WHERE rn = 1 ORDER BY plays DESC, name, artist LIMIT 20001`)
	if err != nil {
		return nil, fmt.Errorf("收听历史不可用，请检查连接及表结构")
	}
	defer rows.Close()
	var out []Track
	for rows.Next() {
		var t Track
		var plays int64
		if err = rows.Scan(&t.Title, &t.Artist, &t.Album, &plays, &t.LastPlayed); err != nil {
			return nil, fmt.Errorf("收听历史数据无法读取")
		}
		t.Plays = int(plays)
		out = append(out, t)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("收听历史读取中断")
	}
	if len(out) > 20000 {
		return nil, fmt.Errorf("收听历史超过 20000 首，需缩小数据范围")
	}
	return out, nil
}
