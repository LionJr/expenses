package postgres

import (
	"context"

	"github.com/LionJr/expenses/internal/expense"
)

func (s *Store) Users(ctx context.Context) ([]expense.User, error) {
	var rows []userRow
	query := `SELECT id, name, username
	          FROM users
	          ORDER BY id`
	if err := s.db.SelectContext(ctx, &rows, query); err != nil {
		return nil, err
	}
	users := make([]expense.User, 0, len(rows))
	for _, row := range rows {
		if s.allowed[row.ID] {
			users = append(users, expense.User(row))
		}
	}
	return users, nil
}

func (s *Store) UpdateUser(ctx context.Context, id int64, name, username string) error {
	if err := expense.Text(name + username); err != nil {
		return err
	}
	if !s.allowed[id] {
		return expense.ErrNotFound
	}
	query := `UPDATE users
	          SET name = $2, username = $3
	          WHERE id = $1`
	_, err := s.db.ExecContext(ctx, query, id, name, username)
	return err
}
