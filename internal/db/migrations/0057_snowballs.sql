-- +goose Up
-- Снежки между игроками: бросок в профиль. Считаются с последнего матча получателя, поэтому хранятся все броски.
CREATE TABLE snowballs (
    id         bigserial PRIMARY KEY,
    from_user  text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    to_user    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX snowballs_to_idx ON snowballs (to_user, created_at DESC);
CREATE INDEX snowballs_from_idx ON snowballs (from_user, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS snowballs;
