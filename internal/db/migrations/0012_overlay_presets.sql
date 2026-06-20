-- +goose Up
-- Общие (глобальные) пресеты раскладки оверлея: организатор сохраняет шаблоны
-- расположения/настроек виджетов и переключается между ними. Доступны всем
-- (один список на проект, не на пользователя). layout хранится как jsonb «как есть»
-- (полный OverlayLayout), чтобы переживать любые будущие поля без правок схемы.
CREATE TABLE IF NOT EXISTS overlay_presets (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    name       text NOT NULL,
    layout     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS overlay_presets;
