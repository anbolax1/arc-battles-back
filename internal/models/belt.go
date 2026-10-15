package models

import "time"

// Belt - пояс лиги: у кого он сейчас, сколько на нём ржавчины и вся история с первого матча на сайте.
type Belt struct {
	Holder    *BeltPlayer  `json:"holder,omitempty"` // нет - пояс ничей
	Since     *time.Time   `json:"since,omitempty"`
	Defenses  int          `json:"defenses"`            // защит у нынешнего носителя
	Rust      int          `json:"rust"`                // игровых дней подряд без матча носителя
	RustLimit int          `json:"rustLimit"`           // на столько игровых дней без матча пояс выпадает
	DroppedAt *time.Time   `json:"droppedAt,omitempty"` // пояс ничей с этого момента
	Leaders   []BeltPlayer `json:"leaders,omitempty"`   // ничей пояс разыграет ближайший матч одного из них; пусто - первый матч сезона
	HonorAt   int          `json:"honorAt"`             // защит за всё время для тега «Почётный носитель»
	Reigns    []BeltReign  `json:"reigns"`              // по времени, последнее - нынешнее
}

type BeltPlayer struct {
	UserID      string `json:"userId"`
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`
}

// BeltReign - одно владение поясом: от матча, где его взяли, до поражения или до того, как он выпал.
type BeltReign struct {
	Holder   BeltPlayer    `json:"holder"`
	From     time.Time     `json:"from"`
	To       *time.Time    `json:"to,omitempty"`
	How      string        `json:"how"` // first - первый матч лиги, take - обыграл носителя, pick - подобрал ничей пояс
	MatchID  string        `json:"matchId"`
	Opponent *BeltPlayer   `json:"opponent,omitempty"` // кого обыграл, когда взял пояс
	Score    []int         `json:"score,omitempty"`    // счёт этого матча: свой, соперника
	Defenses []BeltDefense `json:"defenses"`
	End      string        `json:"end,omitempty"` // lost - проиграл, drop - выпал; пусто - держит сейчас
	LostTo   *BeltPlayer   `json:"lostTo,omitempty"`
	Time     int           `json:"time"`               // в который раз игрок берёт пояс
	Newcomer int           `json:"newcomer,omitempty"` // какой он по счёту носитель в истории лиги; только у первого раза
}

type BeltDefense struct {
	At       time.Time  `json:"at"`
	MatchID  string     `json:"matchId"`
	Opponent BeltPlayer `json:"opponent"`
	Score    []int      `json:"score,omitempty"`
	Double   bool       `json:"double,omitempty"` // матч с жетоном ×2
}

// LiveBelt - идущий матч разыгрывает пояс лиги; после матча - чем кончилось.
type LiveBelt struct {
	HolderParticipantID string `json:"holderParticipantId,omitempty"` // пусто - пояс ничей и его разыгрывает этот матч
	Defenses            int    `json:"defenses"`
	Result              string `json:"result,omitempty"` // после матча: defense - отбился, take - пояс сменил хозяина, pick - подобрал ничей
}
