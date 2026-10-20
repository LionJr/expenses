package postgres

import "fmt"

func expenseListQuery(latest bool) string {
	// Only application-owned SQL fragments are interpolated. Filter values remain
	// bound parameters. Reuse the same ordering inside and outside the page CTE.
	order := "page.spent_at DESC, page.id DESC"
	if latest {
		order = "page.created_at DESC, page.id DESC"
	}
	// One statement gives the page and its aggregate the same MVCC snapshot.
	query := `WITH filtered AS (
	              SELECT *
	              FROM expenses
	              WHERE ($1::bigint = 0 OR user_id = $1)
	                  AND ($2 = '' OR category = $2)
	                  AND ($3 = '' OR spent_at >= NULLIF($3, '')::date)
	                  AND ($4 = '' OR spent_at <= NULLIF($4, '')::date)
	                  AND ($5 = '' OR strpos(lower(comment || ' ' || category), lower($5)) > 0)
	                  AND ($8::bigint = 0 OR id = $8)
	          ), page AS (
	              SELECT *
	              FROM filtered AS page
	              ORDER BY %s
	              LIMIT $6 OFFSET $7
	          ), totals AS (
	              SELECT count(*) AS n, COALESCE(sum(amount), 0)::text AS amount
	              FROM filtered
	          )
	          SELECT totals.n AS total,
	                 totals.amount AS total_amount,
	                 page.id,
	                 page.user_id,
	                 page.amount,
	                 page.category,
	                 to_char(page.spent_at, 'YYYY-MM-DD') AS spent_at,
	                 page.comment,
	                 page.created_at,
	                 page.updated_at,
	                 page.version
	          FROM totals
	          LEFT JOIN page ON true
	          ORDER BY %s`
	return fmt.Sprintf(query, order, order)
}
