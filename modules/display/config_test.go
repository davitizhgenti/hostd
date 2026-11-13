package display

import "testing"

func TestNavConfig(t *testing.T) {
	ok := Config{Nav: map[string]string{"south": "back", "ls": "", "rs": "home", "lt": "page_up"}}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]string{{"banana": "up"}, {"south": "fly"}} {
		if err := (&Config{Nav: bad}).Check(); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// Any canonical button can be bound, not only guide, start and select.
	if _, _, err := Bindings(nil, map[string]string{"lb": "display.menu", "north": "display.menu"}); err != nil {
		t.Errorf("binding lb and north: %v", err)
	}
}
