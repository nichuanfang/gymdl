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
	rows, err := db.QueryContext(ctx, `SELECT name,artist,COALESCE(MAX(album),''),count(*),MAX(date::timestamptz) FROM `+table+` WHERE COALESCE(name,'')<>'' AND COALESCE(artist,'')<>'' GROUP BY name,artist ORDER BY count(*) DESC,name,artist LIMIT 20001`)
	if err != nil {
		return nil, fmt.Errorf("收听历史不可用，请检查连接及表结构")
	}
	defer rows.Close()
	var out []Track
	for rows.Next() {
		var t Track
		if err = rows.Scan(&t.Title, &t.Artist, &t.Album, &t.Plays, &t.LastPlayed); err != nil {
			return nil, fmt.Errorf("收听历史数据无法读取")
		}
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
