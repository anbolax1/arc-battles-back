package store

import "testing"

// Копия сцены в состоянии оверлея совпадает с самой сценой, даже если в сцене лежат отметка о выборе,
// поля прежних версий и выключенные настройки, которых в копии нет.
func TestLayoutKeySameScene(t *testing.T) {
	scene := parseLayout([]byte(`{"version":1,"stageBg":{"on":false,"opacity":0.4},"activePresetId":"x","widgets":[
		{"id":"sb","type":"scoreboard","x":0.35,"y":0.01,"scale":1,"z":1,"visible":true,"hidePenalty":true,"hideTitle":false,"bg":{"on":true,"opacity":1}}]}`))
	copyOf := parseLayout([]byte(`{"widgets":[{"bg":{"opacity":1,"on":true},"id":"sb","type":"scoreboard","x":0.35,"y":0.01,"scale":1,"z":1,"visible":true}],
		"version":1,"stageBg":{"opacity":0.4,"on":false}}`))
	if layoutKey(scene) != layoutKey(copyOf) {
		t.Fatalf("одна и та же раскладка не совпала:\n%s\n%s", layoutKey(scene), layoutKey(copyOf))
	}
	copyOf.Widgets[0].X = 0.4
	if layoutKey(scene) == layoutKey(copyOf) {
		t.Fatal("разные раскладки совпали")
	}
}

func TestAnyVisible(t *testing.T) {
	l := parseLayout([]byte(`{"widgets":[{"id":"a","visible":false},{"id":"b","visible":false}]}`))
	if anyVisible(l.Widgets) {
		t.Fatal("все виджеты скрыты, а раскладка считается видимой")
	}
	l.Widgets[1].Visible = true
	if !anyVisible(l.Widgets) {
		t.Fatal("виджет виден, а раскладка считается пустой")
	}
	if anyVisible(parseLayout([]byte(`{}`)).Widgets) {
		t.Fatal("в пустой раскладке нашёлся видимый виджет")
	}
}
