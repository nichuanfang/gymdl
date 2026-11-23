package audioquality

import (
	"context"
	"errors"
	"time"
)

const (
	SessionRunning = "running"
	SessionPaused  = "paused"
	SessionResults = "results"
)

// Session stores the Bot card and scan mode. File-level progress is already
// committed under audio_quality_results as each file is inspected.
type Session struct {
	ChatID    int64
	Token     string
	Phase     string
	ExpiresAt time.Time
	Payload   []byte
}

func (s *Store) SaveSession(ctx context.Context, session Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_quality_sessions(chat_id,token,phase,expires_at,payload)
		VALUES(?,?,?,?,?) ON CONFLICT(chat_id) DO UPDATE SET token=excluded.token,phase=excluded.phase,
		expires_at=excluded.expires_at,payload=excluded.payload`,
		session.ChatID, session.Token, session.Phase, session.ExpiresAt.Unix(), session.Payload)
	return err
}

func (s *Store) PauseSession(ctx context.Context, chat int64, token string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE audio_quality_sessions SET phase=?
		WHERE chat_id=? AND token=? AND phase=?`, SessionPaused, chat, token, SessionRunning)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("scan is no longer running")
	}
	return err
}

func (s *Store) Sessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id,token,phase,expires_at,payload
		FROM audio_quality_sessions WHERE phase IN (?,?) OR expires_at>?
		ORDER BY expires_at DESC`, SessionRunning, SessionPaused, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		var session Session
		var expires int64
		if err := rows.Scan(&session.ChatID, &session.Token, &session.Phase, &expires, &session.Payload); err != nil {
			return nil, err
		}
		session.ExpiresAt = time.Unix(expires, 0)
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}
