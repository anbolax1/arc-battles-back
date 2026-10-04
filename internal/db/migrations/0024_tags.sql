-- +goose Up
-- Теги игроков: организатор заводит их и выдаёт кому угодно. Тег с ролью есть у всех с этой ролью,
-- тег с сезоном выдаётся сам победителю сезона.
CREATE TABLE IF NOT EXISTS tags (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    name       text NOT NULL,
    color      text NOT NULL DEFAULT '#ffc53d',
    visible    boolean NOT NULL DEFAULT true, -- виден на сайте; иначе только в кабинете
    role       text,
    season_id  text REFERENCES seasons(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS tags_role_uniq ON tags (role) WHERE role IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS tags_season_uniq ON tags (season_id) WHERE season_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS user_tags (
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tag_id     text NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, tag_id)
);
CREATE INDEX IF NOT EXISTS user_tags_tag ON user_tags (tag_id);

-- Свои теги игрок может скрыть из профиля; скрытый организатором тег он включить не может.
CREATE TABLE IF NOT EXISTS user_hidden_tags (
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tag_id  text NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, tag_id)
);

INSERT INTO tags (name, color, role) VALUES
    ('Игрок', '#9a9aa6', 'user'),
    ('Организатор', '#e070ff', 'superadmin')
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS user_hidden_tags;
DROP TABLE IF EXISTS user_tags;
DROP TABLE IF EXISTS tags;
