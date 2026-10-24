CREATE TABLE application_currency (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    code TEXT NOT NULL CHECK (code IN ('TMT', 'RUB', 'USD', 'EUR', 'AZN'))
);

-- Versions before currency configuration always stored expenses as RUB.
-- Preserve that interpretation even if every expense was later deleted.
INSERT INTO application_currency(singleton, code)
SELECT true, 'RUB'
WHERE EXISTS (SELECT 1 FROM expenses)
   OR EXISTS (SELECT 1 FROM expense_operations);
