package postgres

import (
	"context"

	"github.com/LionJr/expenses/internal/expense"
)

func (s *Store) Users(ctx context.Context) ([]expense.User, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,name,username FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []expense.User{}
	for rows.Next() {
		var u expense.User
		if err = rows.Scan(&u.ID, &u.Name, &u.Username); err != nil {
			return nil, err
		}
		if s.Allowed[u.ID] {
			users = append(users, u)
		}
	}
	return users, rows.Err()
}
func (s *Store) UpdateUser(ctx context.Context, id int64, name, username string) error {
	if err := expense.Text(name + username); err != nil {
		return err
	}
	if !s.Allowed[id] {
		return expense.ErrNotFound
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE users SET name=$2,username=$3 WHERE id=$1", id, name, username)
	return err
}
