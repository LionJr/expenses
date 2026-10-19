package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Store struct {
	DB      *sql.DB
	Allowed map[int64]bool
}
type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Open(ctx context.Context, dsn string, ids []int64) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	s := &Store{DB: db, Allowed: map[int64]bool{}}
	for _, id := range ids {
		s.Allowed[id] = true
	}
	for attempt := 0; attempt < 15; attempt++ {
		err = db.PingContext(ctx)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			db.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("database connection: %w", err)
	}
	if err = s.migrate(ctx, ids); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) migrate(ctx context.Context, ids []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(65102933)"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY)"); err != nil {
		return err
	}
	names, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, name := range names {
		if name.IsDir() {
			continue
		}
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", name.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		data, e := migrations.Files.ReadFile(name.Name())
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("migration %s: %w", name.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", name.Name()); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, "INSERT INTO users(id) VALUES($1) ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
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

const columns = "id,user_id,amount,category,to_char(spent_at,'YYYY-MM-DD'),comment,created_at,updated_at,version"

func scan(row interface{ Scan(...any) error }) (expense.Expense, error) {
	var e expense.Expense
	err := row.Scan(&e.ID, &e.UserID, &e.Amount, &e.Category, &e.SpentAt, &e.Comment, &e.CreatedAt, &e.UpdatedAt, &e.Version)
	if errors.Is(err, sql.ErrNoRows) {
		err = expense.ErrNotFound
	}
	e.CreatedAt = e.CreatedAt.UTC()
	e.UpdatedAt = e.UpdatedAt.UTC()
	return e, dataError(err)
}
func (s *Store) List(ctx context.Context, f expense.Filter) (expense.Page, error) {
	return list(ctx, s.DB, f)
}
func list(ctx context.Context, q queryer, f expense.Filter) (expense.Page, error) {
	p := expense.Page{Items: []expense.Expense{}}
	if err := f.Validate(); err != nil {
		return p, err
	}
	// One statement gives the page and its aggregate the same MVCC snapshot.
	where := ` WHERE ($1::bigint=0 OR user_id=$1) AND ($2='' OR category=$2) AND ($3='' OR spent_at>=NULLIF($3,'')::date) AND ($4='' OR spent_at<=NULLIF($4,'')::date) AND ($5='' OR strpos(lower(comment || ' ' || category),lower($5))>0) AND ($8::bigint=0 OR id=$8)`
	order := "spent_at DESC,id DESC"
	if f.Latest {
		order = "created_at DESC,id DESC"
	}
	query := `WITH filtered AS (SELECT * FROM expenses` + where + `), page AS (SELECT * FROM filtered ORDER BY ` + order + ` LIMIT $6 OFFSET $7), totals AS (SELECT count(*) AS n,COALESCE(sum(amount),0)::text AS amount FROM filtered) SELECT totals.n,totals.amount, page.id,page.user_id,page.amount,page.category,to_char(page.spent_at,'YYYY-MM-DD'),page.comment,page.created_at,page.updated_at,page.version FROM totals LEFT JOIN page ON true ORDER BY `
	if f.Latest {
		query += "page.created_at DESC,page.id DESC"
	} else {
		query += "page.spent_at DESC,page.id DESC"
	}
	rows, err := q.QueryContext(ctx, query, f.UserID, f.Category, f.From, f.To, f.Search, f.Limit, f.Offset, f.ID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var sum string
		var id, uid, amount, version sql.NullInt64
		var cat, date, comment sql.NullString
		var created, updated sql.NullTime
		if err = rows.Scan(&p.Total, &sum, &id, &uid, &amount, &cat, &date, &comment, &created, &updated, &version); err != nil {
			return p, err
		}
		p.TotalAmount, err = strconv.ParseInt(sum, 10, 64)
		if err != nil {
			return p, expense.Invalid("сумма отчёта превышает допустимый диапазон")
		}
		if id.Valid {
			p.Items = append(p.Items, expense.Expense{ID: id.Int64, UserID: uid.Int64, Amount: amount.Int64, Category: cat.String, SpentAt: date.String, Comment: comment.String, CreatedAt: created.Time.UTC(), UpdatedAt: updated.Time.UTC(), Version: version.Int64})
		}
	}
	return p, rows.Err()
}
func (s *Store) Create(ctx context.Context, i expense.Input) (expense.Expense, error) {
	if !s.Allowed[i.UserID] {
		return expense.Expense{}, expense.Invalid("участник не разрешён")
	}
	var result expense.Expense
	err := inTransaction(ctx, s.DB, func(q queryer) error {
		var err error
		result, err = create(ctx, q, i)
		return err
	})
	return result, err
}
func create(ctx context.Context, q queryer, i expense.Input) (expense.Expense, error) {
	if err := i.Validate(); err != nil {
		return expense.Expense{}, err
	}
	return scan(q.QueryRowContext(ctx, "INSERT INTO expenses(user_id,amount,category,spent_at,comment) VALUES($1,$2,$3,$4::date,$5) RETURNING "+columns, i.UserID, i.Amount, i.Category, i.SpentAt, i.Comment))
}
func (s *Store) Update(ctx context.Context, owner, id int64, p expense.Patch) (expense.Expense, error) {
	var result expense.Expense
	err := inTransaction(ctx, s.DB, func(q queryer) error {
		var err error
		result, err = update(ctx, q, owner, id, p)
		return err
	})
	return result, err
}
func update(ctx context.Context, q queryer, owner, id int64, p expense.Patch) (expense.Expense, error) {
	if err := p.Validate(); err != nil {
		return expense.Expense{}, err
	}
	if id <= 0 {
		return expense.Expense{}, expense.Invalid("неверный ID")
	}
	e, err := scan(q.QueryRowContext(ctx, `UPDATE expenses SET amount=COALESCE($3,amount),category=COALESCE($4,category),spent_at=COALESCE($5::date,spent_at),comment=COALESCE($6,comment),updated_at=now(),version=version+1 WHERE id=$1 AND ($2::bigint=0 OR user_id=$2) AND ($7::bigint IS NULL OR amount=$7) AND ($8::bigint IS NULL OR version=$8) RETURNING `+columns, id, owner, p.Amount, p.Category, p.SpentAt, p.Comment, p.ExpectedAmount, p.ExpectedVersion))
	if errors.Is(err, expense.ErrNotFound) && p.ExpectedVersion != nil {
		var exists bool
		if lookupErr := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM expenses WHERE id=$1 AND ($2::bigint=0 OR user_id=$2))", id, owner).Scan(&exists); lookupErr != nil {
			return e, lookupErr
		}
		if exists {
			return e, expense.ErrConflict
		}
	}
	return e, err
}
func (s *Store) Delete(ctx context.Context, owner, id int64) error {
	return inTransaction(ctx, s.DB, func(q queryer) error { return remove(ctx, q, owner, id) })
}
func remove(ctx context.Context, q queryer, owner, id int64) error {
	r, err := q.ExecContext(ctx, "DELETE FROM expenses WHERE id=$1 AND ($2::bigint=0 OR user_id=$2)", id, owner)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return expense.ErrNotFound
	}
	return err
}

// Mutate atomically journals the result alongside the change. Replays never apply it twice.
func (s *Store) Mutate(ctx context.Context, key string, owner int64, kind string, id int64, i expense.Input, p expense.Patch) (json.RawMessage, error) {
	if owner <= 0 || !s.Allowed[owner] {
		return nil, expense.ErrNotFound
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", key); err != nil {
		return nil, err
	}
	var saved []byte
	err = tx.QueryRowContext(ctx, "SELECT result FROM expense_operations WHERE operation_key=$1 AND user_id=$2", key, owner).Scan(&saved)
	if err == nil {
		return saved, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var result any
	switch kind {
	case "create_expense":
		i.UserID = owner
		result, err = create(ctx, tx, i)
	case "update_expense":
		result, err = update(ctx, tx, owner, id, p)
	case "delete_expense":
		err = remove(ctx, tx, owner, id)
		result = map[string]any{"deleted_id": strconv.FormatInt(id, 10)}
	default:
		return nil, expense.Invalid("неизвестная операция")
	}
	if err != nil {
		return nil, err
	}
	saved, err = json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(saved, &envelope); err != nil {
		return nil, err
	}
	envelope["operation"], _ = json.Marshal(kind)
	saved, err = json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO expense_operations(operation_key,user_id,result) VALUES($1,$2,$3::jsonb)", key, owner, string(saved)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}
func (s *Store) LoadSession(ctx context.Context, id int64) (json.RawMessage, error) {
	var b []byte
	err := s.DB.QueryRowContext(ctx, "SELECT data FROM bot_sessions WHERE user_id=$1 AND expires_at>now()", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}
func (s *Store) SaveSession(ctx context.Context, id int64, b json.RawMessage) error {
	if err := validateJSONText(b); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, "INSERT INTO bot_sessions(user_id,data,expires_at) VALUES($1,$2::jsonb,now()+interval '30 minutes') ON CONFLICT(user_id) DO UPDATE SET data=EXCLUDED.data,expires_at=EXCLUDED.expires_at", id, string(b))
	return err
}
func (s *Store) StartUpdate(ctx context.Context, id, user int64) (string, string, error) {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO telegram_updates(update_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING", id, user)
	if err != nil {
		return "", "", err
	}
	var state, reply string
	err = s.DB.QueryRowContext(ctx, "SELECT state,reply FROM telegram_updates WHERE update_id=$1 AND user_id=$2", id, user).Scan(&state, &reply)
	return state, reply, err
}
func (s *Store) FinishUpdate(ctx context.Context, id int64, reply string, session json.RawMessage, user int64) error {
	if err := expense.Text(reply); err != nil {
		return err
	}
	if err := validateJSONText(session); err != nil {
		return err
	}
	var metadata struct {
		Choice *botstate.Choice `json:"choice"`
	}
	if err := json.Unmarshal(session, &metadata); err != nil {
		return err
	}
	var choiceID int64
	if metadata.Choice != nil {
		choiceID = metadata.Choice.UpdateID
	}
	isPrompt := metadata.Choice != nil && choiceID == id && metadata.Choice.State == botstate.AwaitingDelivery
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE telegram_updates SET state='done',reply=$2,is_choice_prompt=$3 WHERE update_id=$1", id, reply, isPrompt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO bot_sessions(user_id,data,expires_at) VALUES($1,$2::jsonb,now()+interval '30 minutes') ON CONFLICT(user_id) DO UPDATE SET data=EXCLUDED.data,expires_at=EXCLUDED.expires_at", user, string(session)); err != nil {
		return err
	}

	// Cancel only obsolete choice prompts. Receipts and ordinary replies remain
	// deliverable, while a waiting prompt survives non-mutating delivery warnings.
	if _, err = tx.ExecContext(ctx, "UPDATE telegram_updates SET state='superseded' WHERE user_id=$1 AND state='done' AND is_choice_prompt AND update_id<>$2", user, choiceID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) MarkSent(ctx context.Context, id int64) error { return s.markSent(ctx, id, 0) }
func (s *Store) MarkChoiceSent(ctx context.Context, id, messageID int64) error {
	return s.markSent(ctx, id, messageID)
}
func (s *Store) markSent(ctx context.Context, id, messageID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var user int64
	var isPrompt bool
	err = tx.QueryRowContext(ctx, "UPDATE telegram_updates SET state='sent' WHERE update_id=$1 AND state='done' RETURNING user_id,is_choice_prompt", id).Scan(&user, &isPrompt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if isPrompt {
		if messageID <= 0 {
			return expense.Invalid("нет ID отправленного списка")
		}
		_, err = tx.ExecContext(ctx, `UPDATE bot_sessions SET data=jsonb_set(jsonb_set(data,'{choice,state}',to_jsonb($3::text),false),'{choice,message_id}',to_jsonb($5::text),true)
   WHERE user_id=$1 AND expires_at>now() AND data->'choice'->>'update_id'=$2 AND data->'choice'->>'state'=$4`, user, strconv.FormatInt(id, 10), string(botstate.AwaitingSelection), string(botstate.AwaitingDelivery), strconv.FormatInt(messageID, 10))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) ChoicePromptCurrent(ctx context.Context, id, user int64) (bool, error) {
	var current bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bot_sessions WHERE user_id=$1 AND expires_at>now() AND data->'choice'->>'update_id'=$2 AND data->'choice'->>'state'=$3)`, user, strconv.FormatInt(id, 10), string(botstate.AwaitingDelivery)).Scan(&current)
	return current, err
}
func (s *Store) SupersedeDelivery(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE telegram_updates SET state='superseded' WHERE update_id=$1 AND state='done'", id)
	return err
}

// OperationResult is checked before re-running state-dependent agent logic.
func (s *Store) OperationResult(ctx context.Context, key string, owner int64) (json.RawMessage, error) {
	if owner <= 0 || !s.Allowed[owner] {
		return nil, expense.ErrNotFound
	}
	var result []byte
	err := s.DB.QueryRowContext(ctx, "SELECT result FROM expense_operations WHERE operation_key=$1 AND user_id=$2", key, owner).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return result, err
}

func (s *Store) PendingDeliveries(ctx context.Context, limit int) ([]botstate.Delivery, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT update_id,user_id,reply,delivery_attempts,is_choice_prompt FROM telegram_updates WHERE state='done' AND retry_at<=now() ORDER BY retry_at,update_id LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pending := []botstate.Delivery{}
	for rows.Next() {
		var d botstate.Delivery
		if err = rows.Scan(&d.UpdateID, &d.UserID, &d.Reply, &d.Attempts, &d.ChoicePrompt); err != nil {
			return nil, err
		}
		pending = append(pending, d)
	}
	return pending, rows.Err()
}

// Failed deliveries are terminal after three attempts or a permanent rejection.
// Transient failures remain in the outbox without holding the polling offset.
func (s *Store) FailDelivery(ctx context.Context, id int64, code int, permanent bool, retryAt time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var user int64
	var isPrompt bool
	var state string
	err = tx.QueryRowContext(ctx, `UPDATE telegram_updates
 SET delivery_attempts=delivery_attempts+1,last_error_code=$2,retry_at=$4,
 state=CASE WHEN $3 OR delivery_attempts+1>=3 THEN 'failed'::telegram_update_state ELSE 'done'::telegram_update_state END
 WHERE update_id=$1 AND state='done' RETURNING user_id,is_choice_prompt,state`, id, code, permanent, retryAt).Scan(&user, &isPrompt, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if isPrompt && state == "failed" {
		_, err = tx.ExecContext(ctx, `UPDATE bot_sessions SET data=data-'choice'-'candidates'-'pending'-'candidate_total'
   WHERE user_id=$1 AND data->'choice'->>'update_id'=$2 AND data->'choice'->>'state'=$3`, user, strconv.FormatInt(id, 10), string(botstate.AwaitingDelivery))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Explicit transactions keep a canceled write from completing in autocommit
// after its caller has already received a deadline error. BeginTx also arranges
// rollback on context cancellation; Commit checks the transaction context.
func inTransaction(ctx context.Context, db *sql.DB, operation func(queryer) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = operation(tx); err != nil {
		return err
	}
	return tx.Commit()
}
