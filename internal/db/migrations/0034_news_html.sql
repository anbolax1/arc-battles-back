-- +goose Up
-- Текст новостей теперь HTML из редактора. Прежний простой текст - абзацы через пустую строку, переносы
-- строк и ссылки, которые сайт делал кликабельными, - переводим в ту же разметку.
UPDATE news SET body = '<p>' || regexp_replace(regexp_replace(regexp_replace(
        replace(replace(replace(regexp_replace(trim(body), E'\r\n?', E'\n', 'g'), '&', '&amp;'), '<', '&lt;'), '>', '&gt;'),
        '(https?://[^\s<"]*[^]\s<".,!?:;»)])', '<a href="\1" rel="nofollow noreferrer noopener" target="_blank">\1</a>', 'g'),
        E'\n\\s*\n\\s*', '</p><p>', 'g'),
        E'\n', '<br>', 'g') || '</p>'
WHERE body <> '';

-- +goose Down
UPDATE news SET body = replace(replace(replace(regexp_replace(
        replace(replace(replace(body, '</p><p>', E'\n\n'), '<br>', E'\n'), '</blockquote>', E'\n\n'),
        '<[^>]+>', '', 'g'), '&lt;', '<'), '&gt;', '>'), '&amp;', '&');
