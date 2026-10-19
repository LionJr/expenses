package agent

import "github.com/LionJr/expenses/internal/expense"

func tool(name, description string, properties map[string]any, required ...string) Tool {
	if required == nil {
		required = []string{}
	}
	return Tool{Type: "function", Function: Definition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}}
}
func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func Tools() []Tool {
	category := map[string]any{"type": "string", "enum": expense.Categories}
	fields := func() map[string]any {
		return map[string]any{"amount_rubles": str("Положительная сумма в рублях, десятичная строка, например 350.50"), "category": category, "spent_at": str("Дата YYYY-MM-DD"), "comment": str("Комментарий до 1000 символов")}
	}
	filters := func() map[string]any {
		return map[string]any{"date_from": str("Начало периода YYYY-MM-DD включительно"), "date_to": str("Конец периода YYYY-MM-DD включительно"), "category": category}
	}
	list := filters()
	list["search"] = str("Подстрока комментария или категории")
	list["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 10}
	list["selection"] = map[string]any{"type": "string", "enum": []string{"by_date", "latest_created"}}
	patch := fields()
	patch["id"] = str("ID из результата поиска")
	patch["expected_amount_rubles"] = str("Прежняя сумма, если пользователь её упомянул; проверяется до изменения")
	return []Tool{
		tool("clarify_request", "Если для операции недостаточно данных или запрос непонятен, запросить уточнение. Приложение сформирует вопрос; не отвечай обычным текстом.", map[string]any{"field": map[string]any{"type": "string", "enum": []string{"amount", "date", "category", "comment", "expense", "single_expense", "request"}}}, "field"),
		tool("create_expense", "Создать один расход текущего пользователя", fields(), "amount_rubles", "category", "spent_at"),
		tool("list_expenses", "Показать список собственных расходов: «покажи траты за неделю», «покажи расходы на здоровье за этот месяц». Перед изменением или удалением обязателен поиск. При нескольких совпадениях уточнить выбор.", list),
		tool("summarize_expenses", "Точная сумма расходов за период и категорию для вопросов «сколько потратил». Для просьбы показать расходы используй list_expenses.", filters()),
		tool("update_expense", "Запросить изменение найденного расхода, в том числе при нескольких совпадениях: приложение само запросит выбор. При упоминании старой суммы передать expected_amount_rubles", patch, "id"),
		tool("delete_expense", "Запросить удаление найденного собственного расхода. При нескольких совпадениях приложение сохранит запрос и покажет список для выбора", map[string]any{"id": str("ID из поиска")}, "id"),
	}
}
