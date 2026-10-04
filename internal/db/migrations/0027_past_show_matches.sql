-- +goose Up
-- Прошлые шоу-матчи пришли из arcarena без отметки шоу-матча: отмечаем каждый явно, по ключу импорта.
-- 26.09.2026 Takenslolz vs Hubaaa.
UPDATE tournaments SET format = 'show' WHERE ext_key = 'arena|9120b79a-b6c7-418c-8e7f-719dca448fd9';

-- +goose Down
UPDATE tournaments SET format = 'match' WHERE ext_key = 'arena|9120b79a-b6c7-418c-8e7f-719dca448fd9';
