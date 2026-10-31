package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

// ListeningReport is the read-only aggregate behind the WebUI listening page.
// Every number comes from the Last.fm scrobble table; the handler never
// substitutes guessed data when the history source is unavailable.
type ListeningReport struct {
	Available     bool               `json:"available"`
	Month         string             `json:"month"`
	MonthLabel    string             `json:"month_label"`
	Scrobbles     int                `json:"scrobbles"`
	PrevScrobbles int                `json:"prev_scrobbles"`
	Artists       int                `json:"artists"`
	Tracks        int                `json:"tracks"`
	Albums        int                `json:"albums"`
	Minutes       int                `json:"minutes"`
	TopArtists    []ReportArtist     `json:"top_artists"`
	TopTracks     []ReportTrack      `json:"top_tracks"`
	TopAlbums     []ReportAlbum      `json:"top_albums"`
	Trend         []ReportMonthPoint `json:"trend"`
	Heatmap       []ReportDay        `json:"heatmap"`
	Hours         []ReportHour       `json:"hours"`
	FirstScrobble string             `json:"first_scrobble,omitempty"`
	LastScrobble  string             `json:"last_scrobble,omitempty"`
}

type ReportArtist struct {
	Name    string `json:"name"`
	Image   string `json:"image,omitempty"`
	Plays   int    `json:"plays"`
	Minutes int    `json:"minutes"`
}

type ReportTrack struct {
	Name    string `json:"name"`
	Artist  string `json:"artist"`
	Album   string `json:"album,omitempty"`
	Image   string `json:"image,omitempty"`
	Plays   int    `json:"plays"`
	Minutes int    `json:"minutes"`
}

type ReportAlbum struct {
	Name   string `json:"name"`
	Artist string `json:"artist"`
	Image  string `json:"image,omitempty"`
	Plays  int    `json:"plays"`
}

type ReportMonthPoint struct {
	Month   string `json:"month"`
	Plays   int    `json:"plays"`
	Artists int    `json:"artists"`
}

type ReportDay struct {
	Date  string `json:"date"`
	Plays int    `json:"plays"`
}

type ReportHour struct {
	Hour  int `json:"hour"`
	Plays int `json:"plays"`
}

// HandleListeningReport GET /api/web/listening/report?month=YYYY-MM
func HandleListeningReport(c *gin.Context) {
	cfg := GetWebConfig()
	if cfg == nil || cfg.AI == nil || strings.TrimSpace(cfg.AI.HistoryDSN) == "" {
		response.Fail(c, http.StatusBadRequest, "未配置收听历史数据库（ai.history_dsn）")
		return
	}
	month := strings.TrimSpace(c.Query("month"))
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		response.Fail(c, http.StatusBadRequest, "month 格式必须是 YYYY-MM")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	report, err := loadListeningReport(ctx, cfg.AI.HistoryDSN, cfg.AI.HistoryTable, month)
	if err != nil {
		response.Fail(c, http.StatusBadGateway, "读取收听报告失败: "+err.Error())
		return
	}
	response.Success(c, report)
}

func loadListeningReport(ctx context.Context, dsn, table, month string) (*ListeningReport, error) {
	table = strings.TrimSpace(table)
	if !validReportTable(table) {
		return nil, fmt.Errorf("历史表名不合法")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("连接收听历史失败: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("收听历史不可用: %w", err)
	}

	start, err := time.ParseInLocation("2006-01", month, time.Local)
	if err != nil {
		return nil, err
	}
	end := start.AddDate(0, 1, 0)
	prevStart := start.AddDate(0, -1, 0)

	report := &ListeningReport{
		Available:  true,
		Month:      month,
		MonthLabel: start.Format("2006 年 1 月"),
		TopArtists: make([]ReportArtist, 0),
		TopTracks:  make([]ReportTrack, 0),
		TopAlbums:  make([]ReportAlbum, 0),
		Trend:      make([]ReportMonthPoint, 0),
		Heatmap:    make([]ReportDay, 0),
		Hours:      make([]ReportHour, 0),
	}
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s
WHERE date::timestamptz >= $1 AND date::timestamptz < $2`, table), start, end).Scan(&report.Scrobbles); err != nil {
		return nil, fmt.Errorf("统计本月播放失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s
WHERE date::timestamptz >= $1 AND date::timestamptz < $2`, table), prevStart, start).Scan(&report.PrevScrobbles); err != nil {
		return nil, fmt.Errorf("统计上月播放失败: %w", err)
	}
	if report.Scrobbles == 0 {
		return report, nil
	}

	aggQuery := fmt.Sprintf(`SELECT count(DISTINCT coalesce(nullif(artist,''),'')), count(DISTINCT coalesce(nullif(name,''),'')), count(DISTINCT coalesce(nullif(album,''),''))
FROM %s WHERE date::timestamptz >= $1 AND date::timestamptz < $2`, table)
	if err := db.QueryRowContext(ctx, aggQuery, start, end).Scan(&report.Artists, &report.Tracks, &report.Albums); err != nil {
		return nil, fmt.Errorf("统计曲库维度失败: %w", err)
	}
	// Scrobbles carry no duration, so estimate a conservative 3.5 minutes per
	// play instead of presenting an exact figure.
	report.Minutes = report.Scrobbles * 7 / 2

	report.TopArtists, err = loadTopArtists(ctx, db, table, start, end)
	if err != nil {
		return nil, err
	}
	report.TopTracks, err = loadTopTracks(ctx, db, table, start, end)
	if err != nil {
		return nil, err
	}
	report.TopAlbums, err = loadTopAlbums(ctx, db, table, start, end)
	if err != nil {
		return nil, err
	}
	report.Trend, err = loadTrend(ctx, db, table)
	if err != nil {
		return nil, err
	}
	report.Heatmap, err = loadHeatmap(ctx, db, table, start, end)
	if err != nil {
		return nil, err
	}
	report.Hours, err = loadHours(ctx, db, table, start, end)
	if err != nil {
		return nil, err
	}
	var first, last sql.NullString
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT min(date), max(date) FROM %s
WHERE date::timestamptz >= $1 AND date::timestamptz < $2`, table), start, end).Scan(&first, &last); err == nil {
		report.FirstScrobble = first.String
		report.LastScrobble = last.String
	}
	return report, nil
}

func loadTopArtists(ctx context.Context, db *sql.DB, table string, start, end time.Time) ([]ReportArtist, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT artist, coalesce(max(regexp_replace(image,'/64s/','/300x300/')),''), count(*)
FROM %s WHERE date::timestamptz >= $1 AND date::timestamptz < $2 AND coalesce(artist,'') <> ''
GROUP BY artist ORDER BY count(*) DESC, artist ASC LIMIT 8`, table), start, end)
	if err != nil {
		return nil, fmt.Errorf("读取 Top 艺人失败: %w", err)
	}
	defer rows.Close()
	out := make([]ReportArtist, 0, 8)
	for rows.Next() {
		var item ReportArtist
		if err := rows.Scan(&item.Name, &item.Image, &item.Plays); err != nil {
			return nil, err
		}
		item.Minutes = item.Plays * 7 / 2
		out = append(out, item)
	}
	return out, rows.Err()
}

func loadTopTracks(ctx context.Context, db *sql.DB, table string, start, end time.Time) ([]ReportTrack, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT name, artist, coalesce(max(album),''), coalesce(max(regexp_replace(image,'/64s/','/300x300/')),''), count(*)
FROM %s WHERE date::timestamptz >= $1 AND date::timestamptz < $2 AND coalesce(name,'') <> ''
GROUP BY name, artist ORDER BY count(*) DESC, name ASC LIMIT 10`, table), start, end)
	if err != nil {
		return nil, fmt.Errorf("读取 Top 曲目失败: %w", err)
	}
	defer rows.Close()
	out := make([]ReportTrack, 0, 10)
	for rows.Next() {
		var item ReportTrack
		if err := rows.Scan(&item.Name, &item.Artist, &item.Album, &item.Image, &item.Plays); err != nil {
			return nil, err
		}
		item.Minutes = item.Plays * 7 / 2
		out = append(out, item)
	}
	return out, rows.Err()
}

func loadTopAlbums(ctx context.Context, db *sql.DB, table string, start, end time.Time) ([]ReportAlbum, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT album, coalesce(max(artist),''), coalesce(max(regexp_replace(image,'/64s/','/300x300/')),''), count(*)
FROM %s WHERE date::timestamptz >= $1 AND date::timestamptz < $2 AND coalesce(album,'') <> ''
GROUP BY album ORDER BY count(*) DESC, album ASC LIMIT 6`, table), start, end)
	if err != nil {
		return nil, fmt.Errorf("读取 Top 专辑失败: %w", err)
	}
	defer rows.Close()
	out := make([]ReportAlbum, 0, 6)
	for rows.Next() {
		var item ReportAlbum
		if err := rows.Scan(&item.Name, &item.Artist, &item.Image, &item.Plays); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func loadTrend(ctx context.Context, db *sql.DB, table string) ([]ReportMonthPoint, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT to_char(date::timestamptz,'YYYY-MM'), count(*), count(DISTINCT coalesce(nullif(artist,''),''))
FROM %s GROUP BY 1 ORDER BY 1 DESC LIMIT 12`, table))
	if err != nil {
		return nil, fmt.Errorf("读取月度趋势失败: %w", err)
	}
	defer rows.Close()
	out := make([]ReportMonthPoint, 0, 12)
	for rows.Next() {
		var item ReportMonthPoint
		if err := rows.Scan(&item.Month, &item.Plays, &item.Artists); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func loadHeatmap(ctx context.Context, db *sql.DB, table string, start, end time.Time) ([]ReportDay, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT to_char(date::timestamptz,'YYYY-MM-DD'), count(*)
FROM %s WHERE date::timestamptz >= $1 AND date::timestamptz < $2 GROUP BY 1 ORDER BY 1`, table), start, end)
	if err != nil {
		return nil, fmt.Errorf("读取日播放失败: %w", err)
	}
	defer rows.Close()
	out := make([]ReportDay, 0, 31)
	for rows.Next() {
		var item ReportDay
		if err := rows.Scan(&item.Date, &item.Plays); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func loadHours(ctx context.Context, db *sql.DB, table string, start, end time.Time) ([]ReportHour, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT extract(hour from date::timestamptz)::int, count(*)
FROM %s WHERE date::timestamptz >= $1 AND date::timestamptz < $2 GROUP BY 1 ORDER BY 1`, table), start, end)
	if err != nil {
		return nil, fmt.Errorf("读取时段分布失败: %w", err)
	}
	defer rows.Close()
	out := make([]ReportHour, 0, 24)
	for rows.Next() {
		var item ReportHour
		if err := rows.Scan(&item.Hour, &item.Plays); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func validReportTable(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i, r := range name {
		digit := r >= '0' && r <= '9'
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		ok := r == '_' || letter || (digit && i > 0)
		if !ok {
			return false
		}
	}
	return true
}
