-- +goose Up
-- Посев легендарных контрактов из документа организатора (22.06.2026). Награда 10 баллов.
-- Boosty-именные.
INSERT INTO legendary_contracts (text, points, kind, source, author, title, sort_order) VALUES
  ('«Легендарный Гренадер»: Уничтожьте Бастиона, используя только Лёгкие и Самонаводящиеся гранаты.', 10, 'pve', 'boosty', 'Ex_Gamer_MtFk', '«Архитектор Арены»', 1),
  ('«Массакра»: Убейте 3 Рейдеров в одном месте не дальше 10 метров друг от друга.', 10, 'pvp', 'boosty', 'Red_Buddy', '«Советник Арены»', 2);

-- Легендарные PvE.
INSERT INTO legendary_contracts (text, points, kind, source, sort_order) VALUES
  ('«Коллекционер»: Найдите 3 Дубликата Чертежей за один рейд.', 10, 'pve', 'official', 10),
  ('«Охотник за знаниями»: Найдите 5 Чертежей за один рейд.', 10, 'pve', 'official', 11),
  ('«Полевой исследователь»: Обыщите 10 ARC Зондов за один рейд.', 10, 'pve', 'official', 12),
  ('«Пацифист»: Выполните все задания раунда без использования оружия.', 10, 'pve', 'official', 13),
  ('«Фул Хаус»: Закончите рейд с Бурей, Рысью и Вулканом в одном рейде.', 10, 'pve', 'official', 14),
  ('«Железный запас»: Закончите рейд с 5 Наковальнями в одном рейде.', 10, 'pve', 'official', 15),
  ('«Взломщик Корпусов»: Закончите рейд с 3 Бронеломами в одном рейде.', 10, 'pve', 'official', 16),
  ('«Большой куш»: Закончите рейд с ценностью снаряжения свыше 250.000+.', 10, 'pve', 'official', 17),
  ('«Сердце Матриарха»: Закончите рейд с 3 Ядрами Матриарха в одном рейде.', 10, 'pve', 'official', 18),
  ('«Сердце Королевы»: Закончите рейд с 3 Ядрами Королевы в одном рейде.', 10, 'pve', 'official', 19);

-- Легендарные PvP.
INSERT INTO legendary_contracts (text, points, kind, source, sort_order) VALUES
  ('«Охотник на рейдеров»: Убейте 5 Рейдеров за один рейд.', 10, 'pvp', 'official', 30),
  ('«Элитный Снайпер»: Убейте Рейдера с расстояния 200+ метров.', 10, 'pvp', 'official', 31),
  ('«Василий Зайцев»: Убейте 3 Рейдеров из Снайперской Винтовки Ястреб.', 10, 'pvp', 'official', 32),
  ('«Тихая смерть»: Убейте 4 Рейдеров из Глушителя.', 10, 'pvp', 'official', 33),
  ('«Двойной разрыв»: Сделайте двойное убийство гранатой.', 10, 'pvp', 'official', 34),
  ('«Импульс»: Убейте 2 Рейдера после переката.', 10, 'pvp', 'official', 35),
  ('«Питер Паркер»: Убейте 2 Рейдера после спуска с Зиплайна.', 10, 'pvp', 'official', 36),
  ('«Из тени в бой»: Убейте 2 Рейдера после использования Крюка-Кошки.', 10, 'pvp', 'official', 37),
  ('«Малыш»: Убейте 2 разных Рейдеров с пистолета Заколка.', 10, 'pvp', 'official', 38),
  ('«Яблоко Ньютона»: Убейте 2 разных Рейдеров после получения урона от падения с высоты.', 10, 'pvp', 'official', 39);

-- Уже выполненные (из документа) — статус done + запись в журнал.
WITH done_pf AS (
  UPDATE legendary_contracts SET status = 'done' WHERE text LIKE '«Полевой исследователь»%' RETURNING id
)
INSERT INTO legendary_contract_completions (legendary_contract_id, nickname, map, completed_at)
SELECT id, 'СЛУЧАЙНЫЕ ПАССАЖИРЫ (MAKAR, JENIFER_POPEZ)', 'Поле Битвы у Дамбы — Электромагнитный Шторм', '2026-06-24'
FROM done_pf;

WITH done_hr AS (
  UPDATE legendary_contracts SET status = 'done' WHERE text LIKE '«Охотник на рейдеров»%' RETURNING id
)
INSERT INTO legendary_contract_completions (legendary_contract_id, nickname, map, completed_at)
SELECT id, 'BELL', 'Бурный Поток — Ночной рейд', '2026-06-22'
FROM done_hr;

-- +goose Down
DELETE FROM legendary_contracts;
