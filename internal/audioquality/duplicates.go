package audioquality

import (
	"context"
	"encoding/json"
)

// DuplicateReview records a decision for two exact file versions. The versions
// are sorted by path so reversing the pair preserves the user's decision.
type DuplicateReview struct {
	First, Second FileVersion
	TrashedPath   string
}

func (s *Store) SaveDuplicateReview(ctx context.Context, library string, user int64, review DuplicateReview) error {
	payload, err := json.Marshal(review)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audio_quality_duplicate_reviews(library,user_id,first_path,second_path,payload)
		VALUES(?,?,?,?,?) ON CONFLICT(library,user_id,first_path,second_path) DO UPDATE SET payload=excluded.payload`,
		library, user, review.First.Path, review.Second.Path, payload)
	return err
}

func (s *Store) DuplicateReviews(ctx context.Context, library string, user int64) ([]DuplicateReview, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload FROM audio_quality_duplicate_reviews WHERE library=? AND user_id=?`, library, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reviews []DuplicateReview
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var review DuplicateReview
		if err := json.Unmarshal(payload, &review); err != nil {
			return nil, err
		}
		reviews = append(reviews, review)
	}
	return reviews, rows.Err()
}
