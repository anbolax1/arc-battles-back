-- +goose Up
-- Свой адрес у каждого пресета: /overlay/<slug>. Организатор заводит в OBS по
-- браузер-источнику на пресет и раскладывает их по сценам — оверлей переключается
-- сменой сцены, а не выбором пресета в админке.
-- Слаг человекочитаемый (транслит названия), уникальный; правится в админке.
ALTER TABLE overlay_presets ADD COLUMN IF NOT EXISTS slug text;

-- Бэкфилл уже сохранённых пресетов: порядковый номер гарантирует уникальность
-- до создания индекса (осмысленный адрес организатор задаст в админке).
UPDATE overlay_presets p
   SET slug = 'preset-' || n.num
  FROM (SELECT id, row_number() OVER (ORDER BY created_at, id) AS num FROM overlay_presets) n
 WHERE p.id = n.id AND (p.slug IS NULL OR btrim(p.slug) = '');

ALTER TABLE overlay_presets ALTER COLUMN slug SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS overlay_presets_slug_key ON overlay_presets (slug);

-- +goose Down
DROP INDEX IF EXISTS overlay_presets_slug_key;
ALTER TABLE overlay_presets DROP COLUMN IF EXISTS slug;
